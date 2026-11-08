package cluster

import (
	"context"
	"testing"
	"time"
)

func TestJournalOrphans(t *testing.T) {
	c, mr := newTestClient(t)
	ctx := context.Background()
	a := &Journal{C: c, Node: "m1"}
	b := &Journal{C: c, Node: "m2"}
	_ = c.Heartbeat(ctx, media("m1", 0))
	h, err := a.Begin("RC_1", []byte(`{"recordingId":"RC_1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Put(h, "TR_a", []byte(`{"sid":"TR_a"}`)); err != nil {
		t.Fatal(err)
	}
	if o, _ := b.Orphans(ctx); len(o) != 0 {
		t.Fatalf("a live node's recording is not an orphan: %+v", o)
	}
	if o, _ := a.Orphans(ctx); len(o) != 0 {
		t.Fatalf("a recording running here is not an orphan: %+v", o)
	}
	mr.FastForward(nodeTTL + time.Second) // m1 is gone
	o, err := b.Orphans(ctx)
	if err != nil || len(o) != 1 || o[0].ID != "RC_1" || string(o[0].Job) != `{"recordingId":"RC_1"}` ||
		len(o[0].Tracks) != 1 || string(o[0].Tracks[0]) != `{"sid":"TR_a"}` {
		t.Fatalf("orphan %+v err %v", o, err)
	}
	if o2, _ := (&Journal{C: c, Node: "m3"}).Orphans(ctx); len(o2) != 0 {
		t.Fatal("a claimed orphan must not be handed to another node")
	}
	o[0].Done()
	if ids, _ := c.rdb.SMembers(ctx, keyPrefix+"journals").Result(); len(ids) != 0 {
		t.Fatalf("entry not removed: %v", ids)
	}

	// m1 restarts (same id, alive): its own entries left by the previous run are orphans.
	_ = c.Heartbeat(ctx, media("m1", 0))
	if _, err := a.Begin("RC_2", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	restarted := &Journal{C: c, Node: "m1"}
	if o, _ := restarted.Orphans(ctx); len(o) != 1 || o[0].ID != "RC_2" {
		t.Fatalf("own entry after a restart: %+v", o)
	}
}
