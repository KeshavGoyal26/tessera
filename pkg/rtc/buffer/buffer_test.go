package buffer

import (
	"errors"
	"testing"

	"github.com/KeshavGoyal26/tessera/pkg/errs"
)

func TestPoolReuse(t *testing.T) {
	b := Pool.Get()
	if len(*b) != MaxPacketSize {
		t.Fatalf("len %d", len(*b))
	}
	Pool.Put(b)
	allocs := testing.AllocsPerRun(100, func() { Pool.Put(Pool.Get()) })
	if allocs > 0 {
		t.Fatalf("pool allocates: %v", allocs)
	}
}

func TestWriteStub(t *testing.T) {
	// Phase 0: replaced by real tests in WP1 (wrap-around, duplicates, NACK lookup, fuzzing).
	_, err := New(Config{Slots: 100, ClockRate: 48000}).Write(make([]byte, 20), 0)
	if !errors.Is(err, errs.ErrUnimplemented) {
		t.Fatalf("got %v", err)
	}
}
