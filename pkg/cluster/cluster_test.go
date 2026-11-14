package cluster

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
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
	if time.Since(nodes[0].Seen) > 5*time.Second || nodes[0].Stats != nil {
		t.Fatalf("seen %v stats %q", nodes[0].Seen, nodes[0].Stats)
	}
	// The dashboard summary rides on the heartbeat.
	withStats := media("m1", 3)
	withStats.Stats = []byte(`{"cpu":12}`)
	_ = c.Heartbeat(ctx, withStats)
	if nodes, _ := c.Nodes(ctx); string(nodes[1].Stats) != `{"cpu":12}` {
		t.Fatalf("stats %q", nodes[1].Stats)
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
	draining := media("m0", 0)
	draining.Draining = true
	for _, n := range []NodeInfo{draining, media("m1", 5), media("m2", 2), {ID: "e1", Role: config.RoleEdge}} {
		_ = c.Heartbeat(ctx, n)
	}
	// Random over live media nodes, whatever the room: never a draining node or an edge.
	seen := map[string]bool{}
	for i := range 40 {
		n, err := c.PlacePresenter(ctx, "g1", fmt.Sprint("p", i))
		if err != nil || (n.ID != "m1" && n.ID != "m2") {
			t.Fatalf("placed on %+v err %v", n, err)
		}
		seen[n.ID] = true
	}
	if len(seen) != 2 {
		t.Fatalf("one group should spread over both nodes, got %v", seen)
	}
	if ids, _ := c.RoomNodes(ctx, "g1"); len(ids) != 2 {
		t.Fatalf("g1 hosted on %v", ids)
	}
	if _, err := c.PlacePresenter(ctx, "g3", "a"); err != nil {
		t.Fatal(err)
	}
	first, _ := c.PlacePresenter(ctx, "g3", "a")
	// A placed presenter stays (a reconnect resumes there) until its node is gone.
	if n, _ := c.PlacePresenter(ctx, "g3", "a"); n.ID != first.ID {
		t.Fatalf("placed presenter moved to %s", n.ID)
	}
	mr.FastForward(nodeTTL + time.Second)
	_ = c.Heartbeat(ctx, media("m3", 0))
	if n, err := c.PlacePresenter(ctx, "g3", "a"); err != nil || n.ID != "m3" {
		t.Fatalf("after its node left: got %+v err %v", n, err)
	}
}

func TestRoomNodes(t *testing.T) {
	c, mr := newTestClient(t)
	ctx := context.Background()
	_ = c.Heartbeat(ctx, media("m1", 0))
	_ = c.Heartbeat(ctx, media("m2", 0))
	_ = c.KeepRooms(ctx, "m2", map[string][]string{"g1": {"a"}})
	_ = c.KeepRooms(ctx, "m1", map[string][]string{"g1": {"b"}, "g2": {}})
	if ids, err := c.RoomNodes(ctx, "g1"); err != nil || len(ids) != 2 || ids[0] != "m1" || ids[1] != "m2" {
		t.Fatalf("g1 nodes %v err %v", ids, err)
	}
	// A node whose heartbeat expired is dropped at once, not after roomTTL.
	mr.Del(nodeKey("m2"))
	if ids, _ := c.RoomNodes(ctx, "g1"); len(ids) != 1 || ids[0] != "m1" {
		t.Fatalf("dead node still listed: %v", ids)
	}
	// RoomNodes filters by refresh time against the wall clock; miniredis only
	// expires keys, so a stale member is checked through the presenter key instead.
	mr.FastForward(roomTTL + time.Second)
	if v, _ := mr.Get(presKey("g1", "a")); v != "" {
		t.Fatalf("presenter placement must expire with its node's refreshes, got %q", v)
	}
}

type fakeRooms struct{ ps []*tesserav1.ParticipantInfo }

func (f fakeRooms) ListRooms(context.Context, []string) ([]*tesserav1.Room, error) {
	return []*tesserav1.Room{{Name: "g1", NumParticipants: uint32(len(f.ps))}}, nil
}
func (f fakeRooms) ListParticipants(context.Context, string) ([]*tesserav1.ParticipantInfo, error) {
	return f.ps, nil
}
func (fakeRooms) Ready() error { return nil }

// TestRunRenewsPresenters: a media node renews the placement of its presenters, load-test
// ones included (the admin forwarder finds presenters through it), but not of relay or
// recorder sessions; its summary rides on the heartbeat.
func TestRunRenewsPresenters(t *testing.T) {
	c, _ := newTestClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rooms := fakeRooms{ps: []*tesserav1.ParticipantInfo{
		{Identity: "alice", Kind: tesserav1.ParticipantInfo_KIND_STANDARD},
		{Identity: "lt", Kind: tesserav1.ParticipantInfo_KIND_LOADTEST},
		{Identity: "relay:e1", Kind: tesserav1.ParticipantInfo_KIND_RELAY},
		{Identity: "recorder-RC_1", Kind: tesserav1.ParticipantInfo_KIND_RECORDER},
	}}
	go c.Run(ctx, media("m1", 0), rooms, func() []byte { return []byte(`{"cpu":1}`) }, slog.New(slog.DiscardHandler))
	deadline := time.Now().Add(5 * time.Second)
	for {
		n, err := c.rdb.Exists(ctx, presKey("g1", "alice"), presKey("g1", "lt")).Result()
		if err == nil && n == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("presenters not renewed: %d %v", n, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if n, _ := c.rdb.Exists(ctx, presKey("g1", "relay:e1"), presKey("g1", "recorder-RC_1")).Result(); n != 0 {
		t.Fatal("internal sessions must not be placed as presenters")
	}
	if nodes, _ := c.Nodes(ctx); len(nodes) != 1 || string(nodes[0].Stats) != `{"cpu":1}` || nodes[0].Sessions != 4 {
		t.Fatalf("heartbeat %+v", nodes)
	}
}
