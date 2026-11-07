package room

import (
	"sync"
	"testing"
)

// Snapshots of a participant are built on several goroutines and can reach a client out of
// order; the version lets the client keep the newest. Every snapshot must get a distinct,
// higher version than any taken before it.
func TestParticipantSnapshotVersionsIncrease(t *testing.T) {
	p := &Participant{}
	first := p.ToProto().GetVersion()
	var wg sync.WaitGroup
	seen := make(chan uint32, 100)
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			seen <- p.ToProto().GetVersion()
		}()
	}
	wg.Wait()
	close(seen)
	uniq := map[uint32]bool{}
	for v := range seen {
		if v <= first || uniq[v] {
			t.Fatalf("version %d repeated or not above the first snapshot's %d", v, first)
		}
		uniq[v] = true
	}
	if last := p.ToProto().GetVersion(); last != first+101 {
		t.Fatalf("last version %d, want %d", last, first+101)
	}
}
