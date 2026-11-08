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

func TestPlaceRoom(t *testing.T) {
	c, mr := newTestClient(t)
	ctx := context.Background()
	if _, err := c.PlaceRoom(ctx, "r1"); !errors.Is(err, ErrNoMediaNode) {
		t.Fatalf("no media node: err %v", err)
	}
	draining := media("m0", 0)
	draining.Draining = true
	for _, n := range []NodeInfo{draining, media("m1", 5), media("m2", 2), {ID: "e1", Role: config.RoleEdge}} {
		_ = c.Heartbeat(ctx, n)
	}
	n, err := c.PlaceRoom(ctx, "r1")
	if err != nil || n.ID != "m2" {
		t.Fatalf("least-loaded live media node: got %+v err %v", n, err)
	}
	// Placed rooms stay put, even when another node becomes less loaded.
	_ = c.Heartbeat(ctx, media("m1", 0))
	if n, _ := c.PlaceRoom(ctx, "r1"); n.ID != "m2" {
		t.Fatalf("room moved to %s", n.ID)
	}
	// The hosting node disappears: the room is placed again.
	mr.FastForward(nodeTTL + time.Second)
	for _, n := range []NodeInfo{media("m1", 0), media("m3", 1)} {
		_ = c.Heartbeat(ctx, n)
	}
	if n, err := c.PlaceRoom(ctx, "r1"); err != nil || n.ID != "m1" {
		t.Fatalf("after m2 left: got %+v err %v", n, err)
	}
	if id, _ := c.RoomNode(ctx, "r1"); id != "m1" {
		t.Fatalf("room key %q", id)
	}
}

func TestKeepRooms(t *testing.T) {
	c, mr := newTestClient(t)
	ctx := context.Background()
	_ = c.Heartbeat(ctx, media("m1", 0))
	if _, err := c.PlaceRoom(ctx, "r1"); err != nil {
		t.Fatal(err)
	}
	mr.FastForward(roomTTL + time.Second) // the claim expired (e.g. a Redis failover lost it)
	_ = c.Heartbeat(ctx, media("m1", 0))
	conflicts, err := c.KeepRooms(ctx, "m1", []string{"r1"})
	if err != nil || len(conflicts) != 0 {
		t.Fatalf("conflicts %v err %v", conflicts, err)
	}
	if id, _ := c.RoomNode(ctx, "r1"); id != "m1" {
		t.Fatalf("the hosting node must reclaim its room, key %q", id)
	}
	if conflicts, _ := c.KeepRooms(ctx, "m2", []string{"r1"}); len(conflicts) != 1 {
		t.Fatalf("another node's room must be reported, got %v", conflicts)
	}
}
