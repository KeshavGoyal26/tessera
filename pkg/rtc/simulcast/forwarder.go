// Package simulcast implements per-DownTrack forwarding decisions: layer
// selection and switching on keyframes, and the RTP munging that keeps the
// outgoing stream continuous across switches (sequence numbers, timestamps,
// VP8 picture id / TL0PICIDX / KEYIDX). See ADR-0006 and ARCHITECTURE.md 4.3.
//
// A Forwarder is owned by exactly one DownTrack and is called from the
// Receiver's forwarding goroutine; target layers are set concurrently by the
// allocator through atomics.
package simulcast

import (
	"github.com/KeshavGoyal26/tessera/pkg/errs"
	"github.com/KeshavGoyal26/tessera/pkg/rtc/buffer"
	"github.com/KeshavGoyal26/tessera/pkg/rtc/types"
)

// TranslationParams says what to do with one incoming packet for one DownTrack.
type TranslationParams struct {
	// Drop the packet (wrong layer, waiting for keyframe, paused, muted).
	Drop bool
	// IsSwitch is true for the first packet of a new layer.
	IsSwitch bool
	// RequestKeyFrame asks the Receiver to send PLI for the target layer.
	RequestKeyFrame bool
	// Munged values to write into the outgoing header.
	SequenceNumber uint16
	Timestamp      uint32
	// VP8 descriptor rewrite; valid when HasVP8.
	HasVP8 bool
	VP8    VP8Translation
}

// VP8Translation holds rewritten VP8 descriptor fields.
type VP8Translation struct {
	PictureID uint16
	TL0PICIDX uint8
	KEYIDX    uint8
	// HeaderSizeDelta is new descriptor size minus incoming size (7→15 bit picture id).
	HeaderSizeDelta int
}

// Forwarder decides and translates packets for one DownTrack.
type Forwarder struct {
	mime string
}

// NewForwarder creates a forwarder for the codec MIME type.
func NewForwarder(mime string) *Forwarder { return &Forwarder{mime: mime} }

// SetMaxLayer caps the layer (subscriber preference or allocator target).
func (f *Forwarder) SetMaxLayer(l types.VideoLayer) {}

// SetTargetLayer sets the layer the forwarder should switch to.
func (f *Forwarder) SetTargetLayer(l types.VideoLayer) {}

// CurrentLayer returns the layer currently forwarded.
func (f *Forwarder) CurrentLayer() types.VideoLayer { return types.InvalidVideoLayer }

// TargetLayer returns the layer the forwarder is switching to.
func (f *Forwarder) TargetLayer() types.VideoLayer { return types.InvalidVideoLayer }

// Mute stops forwarding without losing munging state.
func (f *Forwarder) Mute(muted bool) {}

// GetTranslation decides what to do with pkt (from spatial layer `layer`).
// It must not allocate.
func (f *Forwarder) GetTranslation(pkt *buffer.ExtPacket, layer int32) (TranslationParams, error) {
	return TranslationParams{Drop: true}, errs.NotImplemented("simulcast.Forwarder.GetTranslation")
}

// TranslateNACK maps a munged sequence number from a subscriber NACK back to
// (spatial layer, original sequence number). ok is false when the packet was
// never forwarded or is too old.
func (f *Forwarder) TranslateNACK(munged uint16) (layer int32, original uint16, ok bool) {
	return 0, 0, false
}

// PaddingParams returns munged (sn, ts) for n padding-only probe packets.
func (f *Forwarder) PaddingParams(n int) (sn uint16, ts uint32, err error) {
	return 0, 0, errs.NotImplemented("simulcast.Forwarder.PaddingParams")
}
