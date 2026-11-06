package recorder

import (
	"io"
	"testing"
	"time"

	"github.com/pion/rtp"
)

// The tap is written on the forwarding path: a recorder that stops reading must cost
// dropped packets, never a blocked publisher.
func TestTapNeverBlocksForwarding(t *testing.T) {
	tp := &tap{ch: make(chan []byte, 4), done: make(chan struct{})}
	finished := make(chan struct{})
	go func() {
		for i := range 100 {
			_, _ = tp.WriteRTP(&rtp.Header{Version: 2, SequenceNumber: uint16(i)}, []byte{1, 2, 3})
		}
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("WriteRTP blocked with a full queue")
	}
	if d := tp.dropped.Load(); d != 96 {
		t.Fatalf("dropped %d, want 96", d)
	}
	// Queued packets are still delivered after the stream ends, then EOF.
	tp.end()
	buf := make([]byte, 1500)
	n := 0
	for {
		if _, err := tp.Read(buf); err == io.EOF {
			break
		}
		n++
	}
	if n != 4 {
		t.Fatalf("read %d packets after end, want 4", n)
	}
}
