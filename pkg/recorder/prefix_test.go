package recorder

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
		// A time alone is not unique (presenters starting in the same second): ID appended.
		{"{room}/{time}", "", "demo/20251114T092545Z/REC_1", ""},
		{pidTmpl, "1048576", "recordings/576/1048576/1763112345123", "recordings/576/1048576/index.json"},
		{pidTmpl, "7", "recordings/007/7/1763112345123", "recordings/007/7/index.json"},
		{pidTmpl, "42", "recordings/042/42/1763112345123", "recordings/042/42/index.json"},
		{pidTmpl, "1000", "recordings/000/1000/1763112345123", "recordings/000/1000/index.json"},
		// Not one path segment: sanitised (and made unique), never a traversal.
		{pidTmpl, "../x", "recordings/" + shard(safePID("../x")) + "/" + safePID("../x") + "/1763112345123", "recordings/" + shard(safePID("../x")) + "/" + safePID("../x") + "/index.json"},
		{pidTmpl, "a/b", "recordings/" + shard(safePID("a/b")) + "/" + safePID("a/b") + "/1763112345123", "recordings/" + shard(safePID("a/b")) + "/" + safePID("a/b") + "/index.json"},
		// Nothing unique per recording: the recording ID is appended (no overwrites).
		{"{pid}", "42", "42/REC_1", "42/index.json"},
		{"{room}", "", "demo/REC_1", ""},
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

// TestSafePIDKeepsIdentitiesApart: identities that sanitise alike get distinct folders;
// a pid that needs no change keeps its name.
func TestSafePIDKeepsIdentitiesApart(t *testing.T) {
	if got := safePID("1048576"); got != "1048576" {
		t.Fatalf("plain pid changed: %q", got)
	}
	a, b := safePID("a b"), safePID("a_b")
	if a == b || b != "a_b" || !strings.HasPrefix(a, "a_b-") || strings.ContainsAny(a, "/ ") {
		t.Fatalf("%q %q", a, b)
	}
	long1, long2 := strings.Repeat("x", 200)+"1", strings.Repeat("x", 200)+"2"
	if safePID(long1) == safePID(long2) {
		t.Fatal("long identities with the same first 128 bytes share a folder")
	}
	for _, bad := range []string{"", ".", ".."} {
		if safePID(bad) != "" {
			t.Fatalf("%q accepted", bad)
		}
	}
}
