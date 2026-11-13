// Package cluster runs Tessera as two tiers (ADR-0012): public edges in front of
// private media servers, coordinated through Redis. Redis is never on the media
// path: it holds the node registry, which media nodes host each group (room), and
// where each presenter is.
//
// Keys (prefix "tessera:"):
//
//	node:<id>               hash (role, private_url, sessions, rooms, draining, seen, stats); expires 10 s after the last heartbeat
//	nodes                   set of node ids (stale ids are dropped when their hash is gone)
//	group:<room>            sorted set of media node ids hosting the room, scored by last refresh (ms)
//	pres:<room>\x00<identity> media node of a presenter; refreshed by that node, expires 60 s after it stops
package cluster

import (
	"context"
	"crypto/tls"
	"errors"
	"math/rand/v2"
	"slices"
	"sort"
	"strconv"
	"strings"
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
	// Seen is the time of the last heartbeat; Stats the node's summary for the cluster
	// dashboard (opaque JSON, a few hundred bytes), both set by Nodes.
	Seen  time.Time
	Stats []byte
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

func nodeKey(id string) string { return keyPrefix + "node:" + id }

// Heartbeat publishes the node's entry; it expires nodeTTL after the last call.
func (c *Client) Heartbeat(ctx context.Context, n NodeInfo) error {
	_, err := c.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.HSet(ctx, nodeKey(n.ID), "role", n.Role, "private_url", n.PrivateURL,
			"sessions", n.Sessions, "rooms", n.Rooms, "draining", strconv.FormatBool(n.Draining),
			"seen", time.Now().UnixMilli(), "stats", n.Stats)
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
		seen, _ := strconv.ParseInt(h["seen"], 10, 64)
		n := NodeInfo{ID: id, Role: h["role"], PrivateURL: h["private_url"],
			Sessions: sessions, Rooms: rooms, Draining: h["draining"] == "true", Seen: time.UnixMilli(seen)}
		if st := h["stats"]; st != "" {
			n.Stats = []byte(st)
		}
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// PickNode picks the index of a presenter's media node among n candidates (random;
// tests replace it).
var PickNode = rand.IntN

// ErrNoMediaNode is returned when no media node can take a presenter.
var ErrNoMediaNode = errs.New(errs.CodeUnavailable, "no media server available")

func presKey(room, identity string) string { return keyPrefix + "pres:" + room + "\x00" + identity }
func groupKey(room string) string          { return keyPrefix + "group:" + room }

// PlacePresenter returns the media node for a presenter of room. A presenter stays
// on its node while that node is alive (a reconnect resumes the session there).
// Otherwise it goes to a random live, not draining media node, whatever its room;
// one group can span several nodes (ADR-0012). Concurrent placements agree.
func (c *Client) PlacePresenter(ctx context.Context, room, identity string) (NodeInfo, error) {
	nodes, err := c.Nodes(ctx)
	if err != nil {
		return NodeInfo{}, err
	}
	media := map[string]NodeInfo{}
	for _, n := range nodes {
		if n.Role == config.RoleMedia {
			media[n.ID] = n
		}
	}
	var live []NodeInfo
	for _, n := range media {
		if !n.Draining {
			live = append(live, n)
		}
	}
	slices.SortFunc(live, func(a, b NodeInfo) int { return strings.Compare(a.ID, b.ID) })
	key := presKey(room, identity)
	for range 3 {
		owner, err := c.rdb.Get(ctx, key).Result()
		switch {
		case err == nil:
			if n, ok := media[owner]; ok {
				return n, nil
			}
			if err := c.releaseIf(ctx, key, owner); err != nil {
				return NodeInfo{}, err
			}
			continue
		case !errors.Is(err, redis.Nil):
			return NodeInfo{}, err
		}
		if len(live) == 0 {
			return NodeInfo{}, ErrNoMediaNode
		}
		best := live[PickNode(len(live))]
		ok, err := c.rdb.SetNX(ctx, key, best.ID, roomTTL).Result()
		if err != nil {
			return NodeInfo{}, err
		}
		if ok {
			// Record the placement now rather than at the node's next heartbeat, so the
			// presenters of a group joining together land on the same node and a burst
			// of new groups spreads over the nodes.
			now := strconv.FormatInt(time.Now().UnixMilli(), 10)
			if err := notePlaced.Run(ctx, c.rdb, []string{groupKey(room), nodeKey(best.ID)}, best.ID, now,
				int((2 * roomTTL).Seconds())).Err(); err != nil {
				return NodeInfo{}, err
			}
			return best, nil
		}
	}
	return NodeInfo{}, errs.New(errs.CodeUnavailable, "cluster: could not place a presenter of %q", room)
}

// RoomNodes returns the live media nodes hosting room (refreshed within roomTTL), by id.
// A node whose heartbeat expired is left out at once, not only after roomTTL.
func (c *Client) RoomNodes(ctx context.Context, room string) ([]string, error) {
	min := strconv.FormatInt(time.Now().Add(-roomTTL).UnixMilli(), 10)
	ids, err := c.rdb.ZRangeByScore(ctx, groupKey(room), &redis.ZRangeBy{Min: min, Max: "+inf"}).Result()
	if err != nil {
		return nil, err
	}
	alive := make([]*redis.IntCmd, len(ids))
	if _, err := c.rdb.Pipelined(ctx, func(p redis.Pipeliner) error {
		for i, id := range ids {
			alive[i] = p.Exists(ctx, nodeKey(id))
		}
		return nil
	}); err != nil {
		return nil, err
	}
	live := ids[:0]
	for i, id := range ids {
		if alive[i].Val() > 0 {
			live = append(live, id)
		}
	}
	sort.Strings(live)
	return live, nil
}

// KeepRooms records that node hosts these rooms and renews its presenters' placement
// (presenters maps room to the identities on this node).
func (c *Client) KeepRooms(ctx context.Context, node string, presenters map[string][]string) error {
	now := time.Now()
	_, err := c.rdb.Pipelined(ctx, func(p redis.Pipeliner) error {
		for room, ids := range presenters {
			p.ZAdd(ctx, groupKey(room), redis.Z{Score: float64(now.UnixMilli()), Member: node})
			p.ZRemRangeByScore(ctx, groupKey(room), "-inf", strconv.FormatInt(now.Add(-roomTTL).UnixMilli(), 10))
			p.Expire(ctx, groupKey(room), 2*roomTTL)
			for _, id := range ids {
				p.Set(ctx, presKey(room, id), node, roomTTL)
			}
		}
		return nil
	})
	return err
}

var releaseIf = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then return redis.call("DEL", KEYS[1]) end
return 0`)

// notePlaced adds a node to a group's hosting set and counts the session on the node
// until its next heartbeat reports the real number (only while the node is registered).
var notePlaced = redis.NewScript(`
redis.call("ZADD", KEYS[1], ARGV[2], ARGV[1])
redis.call("EXPIRE", KEYS[1], ARGV[3])
if redis.call("EXISTS", KEYS[2]) == 1 then redis.call("HINCRBY", KEYS[2], "sessions", 1) end
return 0`)

func (c *Client) releaseIf(ctx context.Context, key, node string) error {
	return releaseIf.Run(ctx, c.rdb, []string{key}, node).Err()
}

// Ended sessions are kept for support ("what happened to user X") after the node that saw
// them is gone: one summary per session, and per identity its last userSessions session ids.
const (
	sessionTTL   = 24 * time.Hour
	userSessions = 20
)

func sessKey(sid string) string      { return keyPrefix + "sess:" + sid }
func userKey(identity string) string { return keyPrefix + "user:" + identity }

// SaveSession stores an ended session's summary (JSON) for sessionTTL. Written once per
// session, so the Redis load follows the session end rate, not the number of users.
func (c *Client) SaveSession(ctx context.Context, identity, sid string, data []byte) error {
	_, err := c.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.Set(ctx, sessKey(sid), data, sessionTTL)
		p.LPush(ctx, userKey(identity), sid)
		p.LTrim(ctx, userKey(identity), 0, userSessions-1)
		p.Expire(ctx, userKey(identity), sessionTTL)
		return nil
	})
	return err
}

// UserSessions returns the saved sessions of identity, newest first.
func (c *Client) UserSessions(ctx context.Context, identity string) ([][]byte, error) {
	sids, err := c.rdb.LRange(ctx, userKey(identity), 0, userSessions-1).Result()
	if err != nil || len(sids) == 0 {
		return nil, err
	}
	keys := make([]string, len(sids))
	for i, s := range sids {
		keys[i] = sessKey(s)
	}
	vals, err := c.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	var out [][]byte
	for _, v := range vals {
		if s, ok := v.(string); ok {
			out = append(out, []byte(s))
		}
	}
	return out, nil
}
