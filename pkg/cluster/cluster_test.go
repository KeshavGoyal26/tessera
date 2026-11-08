package cluster

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/KeshavGoyal26/tessera/pkg/config"
)

func newTestClient(t *testing.T) (*Client, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	c, err := New(context.Background(), config.RedisConfig{Addr: mr.Addr()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, mr
}

func media(id string, sessions int) NodeInfo {
	return NodeInfo{ID: id, Role: config.RoleMedia, PrivateURL: "http://" + id, Sessions: sessions}
}

func TestRegistryExpires(t *testing.T) {
	c, mr := newTestClient(t)
	ctx := context.Background()
	for _, n := range []NodeInfo{media("m1", 3), {ID: "e1", Role: config.RoleEdge}} {
		if err := c.Heartbeat(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	nodes, err := c.Nodes(ctx)
	if err != nil || len(nodes) != 2 || nodes[1].ID != "m1" || nodes[1].Sessions != 3 || nodes[1].PrivateURL != "http://m1" {
		t.Fatalf("nodes %+v err %v", nodes, err)
	}
	mr.FastForward(nodeTTL + time.Second) // no heartbeat: both entries expire
	if nodes, _ := c.Nodes(ctx); len(nodes) != 0 {
		t.Fatalf("expired nodes still listed: %+v", nodes)
	}
}

func TestPlacePresenter(t *testing.T) {
	c, mr := newTestClient(t)
	ctx := context.Background()
	if _, err := c.PlacePresenter(ctx, "g1", "a"); !errors.Is(err, ErrNoMediaNode) {
		t.Fatalf("no media node: err %v", err)
	}
	old := SpillSessions
	SpillSessions = 10
	t.Cleanup(func() { SpillSessions = old })
	draining := media("m0", 0)
	draining.Draining = true
	for _, n := range []NodeInfo{draining, media("m1", 5), media("m2", 2), {ID: "e1", Role: config.RoleEdge}} {
		_ = c.Heartbeat(ctx, n)
	}
	if n, err := c.PlacePresenter(ctx, "g1", "a"); err != nil || n.ID != "m2" {
		t.Fatalf("first presenter: least-loaded live media node, got %+v err %v", n, err)
	}
	// The group is hosted on m1 (under the spill threshold): the next presenter joins it
	// there although m2 is less loaded.
	if err := c.KeepRooms(ctx, "m1", map[string][]string{"g1": {}}); err != nil {
		t.Fatal(err)
	}
	if n, _ := c.PlacePresenter(ctx, "g1", "b"); n.ID != "m1" {
		t.Fatalf("second presenter: the node already hosting the group, got %s", n.ID)
	}
	// m1 reaches the spill threshold: the next presenter goes to the least-loaded node.
	_ = c.Heartbeat(ctx, media("m1", 10))
	if n, _ := c.PlacePresenter(ctx, "g1", "c"); n.ID != "m2" {
		t.Fatalf("spill: got %s", n.ID)
	}
	// A placed presenter stays (a reconnect resumes there) until its node is gone.
	if n, _ := c.PlacePresenter(ctx, "g1", "a"); n.ID != "m2" {
		t.Fatalf("placed presenter moved to %s", n.ID)
	}
	mr.FastForward(nodeTTL + time.Second)
	_ = c.Heartbeat(ctx, media("m3", 0))
	if n, err := c.PlacePresenter(ctx, "g1", "a"); err != nil || n.ID != "m3" {
		t.Fatalf("after its node left: got %+v err %v", n, err)
	}
}

func TestRoomNodes(t *testing.T) {
	c, mr := newTestClient(t)
	ctx := context.Background()
	_ = c.KeepRooms(ctx, "m2", map[string][]string{"g1": {"a"}})
	_ = c.KeepRooms(ctx, "m1", map[string][]string{"g1": {"b"}, "g2": {}})
	if ids, err := c.RoomNodes(ctx, "g1"); err != nil || len(ids) != 2 || ids[0] != "m1" || ids[1] != "m2" {
		t.Fatalf("g1 nodes %v err %v", ids, err)
	}
	// RoomNodes filters by refresh time against the wall clock; miniredis only
	// expires keys, so a stale member is checked through the presenter key instead.
	mr.FastForward(roomTTL + time.Second)
	if v, _ := mr.Get(presKey("g1", "a")); v != "" {
		t.Fatalf("presenter placement must expire with its node's refreshes, got %q", v)
	}
}
