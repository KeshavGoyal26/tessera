// Package cluster runs Tessera as two tiers (ADR-0012): public edges in front of
// private media servers, coordinated through Redis. Redis is never on the media
// path: it holds the node registry and which media node hosts each room.
//
// Keys:
//
//	node:<id>   hash (role, private_url, sessions, rooms, draining), expires 10 s after the last heartbeat
//	nodes       set of node ids (stale ids are dropped when their hash is gone)
//	room:<name> id of the media node hosting the room, refreshed by that node, expires 60 s after it stops
package cluster

import (
	"context"
	"crypto/tls"
	"errors"
	"sort"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/KeshavGoyal26/tessera/pkg/config"
	"github.com/KeshavGoyal26/tessera/pkg/errs"
)

const (
	nodeTTL   = 10 * time.Second
	roomTTL   = 60 * time.Second
	nodesKey  = "nodes"
	keyPrefix = "tessera:"
)

// NodeInfo is one node's registry entry.
type NodeInfo struct {
	ID         string
	Role       string
	PrivateURL string
	Sessions   int
	Rooms      int
	Draining   bool
}

// Client talks to the cluster's Redis.
type Client struct {
	rdb *redis.Client
}

// New connects to Redis and checks the connection.
func New(ctx context.Context, cfg config.RedisConfig) (*Client, error) {
	opts := &redis.Options{Addr: cfg.Addr, Username: cfg.Username, Password: cfg.Password, DB: cfg.DB}
	if cfg.TLS {
		opts.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	rdb := redis.NewClient(opts)
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		return nil, errs.Wrap(errs.CodeUnavailable, err, "cluster: redis "+cfg.Addr)
	}
	return &Client{rdb: rdb}, nil
}

// Close closes the Redis connection.
func (c *Client) Close() error { return c.rdb.Close() }

func nodeKey(id string) string   { return keyPrefix + "node:" + id }
func roomKey(name string) string { return keyPrefix + "room:" + name }

// Heartbeat publishes the node's entry; it expires nodeTTL after the last call.
func (c *Client) Heartbeat(ctx context.Context, n NodeInfo) error {
	_, err := c.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.HSet(ctx, nodeKey(n.ID), "role", n.Role, "private_url", n.PrivateURL,
			"sessions", n.Sessions, "rooms", n.Rooms, "draining", strconv.FormatBool(n.Draining))
		p.Expire(ctx, nodeKey(n.ID), nodeTTL)
		p.SAdd(ctx, keyPrefix+nodesKey, n.ID)
		return nil
	})
	return err
}

// Leave removes the node's entry (clean shutdown).
func (c *Client) Leave(ctx context.Context, id string) error {
	_, err := c.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.Del(ctx, nodeKey(id))
		p.SRem(ctx, keyPrefix+nodesKey, id)
		return nil
	})
	return err
}

// Nodes returns the live nodes, ordered by id.
func (c *Client) Nodes(ctx context.Context) ([]NodeInfo, error) {
	ids, err := c.rdb.SMembers(ctx, keyPrefix+nodesKey).Result()
	if err != nil {
		return nil, err
	}
	cmds := make([]*redis.MapStringStringCmd, len(ids))
	if _, err := c.rdb.Pipelined(ctx, func(p redis.Pipeliner) error {
		for i, id := range ids {
			cmds[i] = p.HGetAll(ctx, nodeKey(id))
		}
		return nil
	}); err != nil {
		return nil, err
	}
	var out []NodeInfo
	for i, id := range ids {
		h := cmds[i].Val()
		if len(h) == 0 {
			_ = c.rdb.SRem(ctx, keyPrefix+nodesKey, id).Err() // expired
			continue
		}
		sessions, _ := strconv.Atoi(h["sessions"])
		rooms, _ := strconv.Atoi(h["rooms"])
		out = append(out, NodeInfo{ID: id, Role: h["role"], PrivateURL: h["private_url"],
			Sessions: sessions, Rooms: rooms, Draining: h["draining"] == "true"})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// ErrNoMediaNode is returned when no media node can take a room.
var ErrNoMediaNode = errs.New(errs.CodeUnavailable, "no media server available")

// PlaceRoom returns the media node hosting room, placing the room on the
// least-loaded live media node that is not draining when it has none (or its node
// is gone). Concurrent placements agree: the first claim wins.
func (c *Client) PlaceRoom(ctx context.Context, room string) (NodeInfo, error) {
	nodes, err := c.Nodes(ctx)
	if err != nil {
		return NodeInfo{}, err
	}
	media := map[string]NodeInfo{}
	var best *NodeInfo
	for i := range nodes {
		n := nodes[i]
		if n.Role != config.RoleMedia {
			continue
		}
		media[n.ID] = n
		if !n.Draining && (best == nil || n.Sessions < best.Sessions) {
			best = &nodes[i]
		}
	}
	for range 3 {
		owner, err := c.rdb.Get(ctx, roomKey(room)).Result()
		switch {
		case err == nil:
			if n, ok := media[owner]; ok {
				return n, nil // the room lives there (even while draining: sessions in it continue)
			}
			// Its node is gone: release the claim (only if still that node's) and place again.
			if err := c.releaseIf(ctx, room, owner); err != nil {
				return NodeInfo{}, err
			}
			continue
		case !errors.Is(err, redis.Nil):
			return NodeInfo{}, err
		}
		if best == nil {
			return NodeInfo{}, ErrNoMediaNode
		}
		ok, err := c.rdb.SetNX(ctx, roomKey(room), best.ID, roomTTL).Result()
		if err != nil {
			return NodeInfo{}, err
		}
		if ok {
			return *best, nil
		}
		// Another edge placed it first: read the winner.
	}
	return NodeInfo{}, errs.New(errs.CodeUnavailable, "cluster: could not place room %q", room)
}

// RoomNode returns the id of the node hosting room ("" when none).
func (c *Client) RoomNode(ctx context.Context, room string) (string, error) {
	id, err := c.rdb.Get(ctx, roomKey(room)).Result()
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	return id, err
}

// keepRoom refreshes this node's claim on room (claiming it when it expired). It
// reports false when another node holds the room.
var keepRoom = redis.NewScript(`
local v = redis.call("GET", KEYS[1])
if v == false or v == ARGV[1] then
  redis.call("SET", KEYS[1], ARGV[1], "PX", ARGV[2])
  return 1
end
return 0`)

// KeepRooms refreshes the claims of the rooms hosted by node and returns the rooms
// another node holds (a split, logged by the caller).
func (c *Client) KeepRooms(ctx context.Context, node string, rooms []string) ([]string, error) {
	var conflicts []string
	for _, r := range rooms {
		ok, err := keepRoom.Run(ctx, c.rdb, []string{roomKey(r)}, node, roomTTL.Milliseconds()).Int()
		if err != nil {
			return conflicts, err
		}
		if ok == 0 {
			conflicts = append(conflicts, r)
		}
	}
	return conflicts, nil
}

var releaseIf = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then return redis.call("DEL", KEYS[1]) end
return 0`)

func (c *Client) releaseIf(ctx context.Context, room, node string) error {
	return releaseIf.Run(ctx, c.rdb, []string{roomKey(room)}, node).Err()
}
