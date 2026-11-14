package recorder

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/KeshavGoyal26/tessera/pkg/storage"
)

func TestExpandPrefix(t *testing.T) {
	now := time.UnixMilli(1763112345123)
	const pidTmpl = "recordings/{shard}/{pid}/{start_ms}"
	for _, c := range []struct {
		tmpl, pid, prefix, index string
	}{
		{"", "alice", "demo/REC_1", ""},
		{"{room}/{time}", "", "demo/20251114T092545Z", ""},
		{pidTmpl, "1048576", "recordings/576/1048576/1763112345123", "recordings/576/1048576/index.json"},
		{pidTmpl, "7", "recordings/007/7/1763112345123", "recordings/007/7/index.json"},
		{pidTmpl, "42", "recordings/042/42/1763112345123", "recordings/042/42/index.json"},
		{pidTmpl, "1000", "recordings/000/1000/1763112345123", "recordings/000/1000/index.json"},
		// Not one path segment: sanitised, never a traversal.
		{pidTmpl, "../x", "recordings/" + shard(".._x") + "/.._x/1763112345123", "recordings/" + shard(".._x") + "/.._x/index.json"},
		{pidTmpl, "a/b", "recordings/" + shard("a_b") + "/a_b/1763112345123", "recordings/" + shard("a_b") + "/a_b/index.json"},
		// A template that needs a pid falls back to the default without one.
		{pidTmpl, "", "demo/REC_1", ""},
		{pidTmpl, "..", "demo/REC_1", ""},
	} {
		prefix, index := expandPrefix(c.tmpl, "demo", "REC_1", c.pid, now)
		if prefix != c.prefix || index != c.index {
			t.Errorf("%q pid %q: got %q %q, want %q %q", c.tmpl, c.pid, prefix, index, c.prefix, c.index)
		}
	}
}

func TestShardSpreadsConsecutivePIDs(t *testing.T) {
	seen := map[string]int{}
	for pid := 1_000_000; pid < 1_010_000; pid++ {
		seen[shard(itoa(int64(pid)))]++
	}
	if len(seen) != 1000 {
		t.Fatalf("%d shards", len(seen))
	}
	for s, n := range seen {
		if n != 10 || len(s) != 3 {
			t.Fatalf("shard %q has %d pids", s, n)
		}
	}
	if a, b := shard("user-abc"), shard("user-abc"); a != b || len(a) != 3 {
		t.Fatalf("non-numeric shard %q %q", a, b)
	}
}

func TestAddToIndex(t *testing.T) {
	root := t.TempDir()
	st, err := storage.NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	w := &Worker{}
	ctx := context.Background()
	key := "recordings/576/1048576/index.json"
	for _, m := range []string{"m1", "m2", "m1"} { // m1 again: listed once
		if err := w.addToIndex(ctx, st, key, m); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(filepath.Join(root, key))
	if err != nil {
		t.Fatal(err)
	}
	var idx recordingIndex
	if err := json.Unmarshal(b, &idx); err != nil || len(idx.Recordings) != 2 || idx.Recordings[0] != "m1" || idx.Recordings[1] != "m2" {
		t.Fatalf("%s %v", b, err)
	}
	// A corrupt index is reported, not overwritten.
	if err := os.WriteFile(filepath.Join(root, key), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := w.addToIndex(ctx, st, key, "m3"); err == nil {
		t.Fatal("corrupt index accepted")
	}
}
