package simulcast

import (
	"testing"

	"github.com/KeshavGoyal26/tessera/pkg/rtc/buffer"
)

// Phase 0 placeholder. WP1 replaces it with table-driven tests: sequence
// wrap-around, layer up/down switches on keyframes, loss across switches,
// VP8 picture-id rollover and 7→15-bit expansion, NACK translation.
func TestStubDrops(t *testing.T) {
	tp, _ := NewForwarder("video/VP8").GetTranslation(&buffer.ExtPacket{}, 0)
	if !tp.Drop {
		t.Fatal("stub must drop")
	}
}
