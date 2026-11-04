package hls

import (
	"context"
	"errors"
	"testing"

	"github.com/KeshavGoyal26/tessera/pkg/errs"
)

// Phase 0 placeholder. WP4 adds depacketizer tables, golden init/fragment
// bytes, ffprobe validation and playlist tests.
func TestStubMuxer(t *testing.T) {
	m, err := NewMuxer(MuxerConfig{}, TrackSpec{Codec: CodecH264, Timescale: 90000}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.WriteSample(context.Background(), Sample{}); !errors.Is(err, errs.ErrUnimplemented) {
		t.Fatalf("got %v", err)
	}
}
