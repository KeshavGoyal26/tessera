// Package simulcast implements per-DownTrack forwarding decisions: layer
// selection and switching on keyframes, and the RTP munging that keeps the
// outgoing stream continuous across switches (sequence numbers, timestamps,
// VP8 picture id / TL0PICIDX / KEYIDX). See ADR-0006 and ARCHITECTURE.md 4.3.
//
// A Forwarder is owned by exactly one DownTrack. GetTranslation is called
// from the Receiver's layer goroutines (one per simulcast layer, so up to
// three concurrently); a small mutex serialises them. Target layers are set
// by the allocator and by subscriber preferences.
package simulcast

import (
	"strings"
	"sync"

	"github.com/KeshavGoyal26/tessera/pkg/errs"
	"github.com/KeshavGoyal26/tessera/pkg/rtc/buffer"
	"github.com/KeshavGoyal26/tessera/pkg/rtc/types"
)

// TranslationParams says what to do with one incoming packet for one DownTrack.
type TranslationParams struct {
	// Drop the packet (wrong layer, waiting for keyframe, paused, muted).
	Drop bool
	// IsSwitch is true for the first packet of a new layer (or of a resume).
	IsSwitch bool
	// RequestKeyFrame asks the Receiver to send PLI for KeyFrameLayer.
	RequestKeyFrame bool
	KeyFrameLayer   int32
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

// nackHistory is the number of forwarded packets remembered for NACK translation.
const nackHistory = 512

type sentPacket struct {
	inSN   uint16
	layer  int8
	valid  bool
	ts     uint32
	pid    uint16
	tl0    uint8
	key    uint8
	rtxCnt uint8
	rtxAt  int64
}

// NACKInfo describes a forwarded packet for retransmission.
type NACKInfo struct {
	Layer     int32
	Original  uint16
	Timestamp uint32
	VP8       VP8Translation
}

// Forwarder decides and translates packets for one DownTrack.
type Forwarder struct {
	mime      string
	isVideo   bool
	isVP8     bool
	clockRate uint32

	mu sync.Mutex
	// Layer state.
	maxLayer    types.VideoLayer // subscriber preference
	allocTarget types.VideoLayer // allocator decision (InvalidVideoLayer = no constraint / paused)
	allocSet    bool
	available   int32 // highest spatial layer the publisher sends
	current     types.VideoLayer
	muted       bool
	resync      bool
	switches    uint64

	// Munging state.
	started      bool
	snOffset     uint64
	tsOffset     uint64
	lastInExt    uint64 // last in-order incoming ext SN on the current layer
	switchExt    uint64 // first ext SN forwarded on the current layer
	lastOutExtSN uint64
	lastOutExtTS uint64
	lastArrival  int64
	// VP8.
	pidOffset, lastOutPID uint16
	tl0Offset, lastOutTL0 uint8
	keyOffset, lastOutKey uint8
	vp8Started            bool
	droppedPID            uint16
	droppedPIDValid       bool

	sent [nackHistory]sentPacket
}

// NewForwarder creates a forwarder for the codec MIME type. Video defaults to
// the highest layer (capped by SetAvailableLayers); audio has one layer.
func NewForwarder(mime string) *Forwarder {
	m := strings.ToLower(mime)
	f := &Forwarder{
		mime:        mime,
		isVideo:     strings.HasPrefix(m, "video/"),
		isVP8:       m == "video/vp8",
		clockRate:   48000,
		maxLayer:    types.VideoLayer{Spatial: types.MaxSpatialLayers - 1, Temporal: types.MaxTemporalLayers - 1},
		allocTarget: types.InvalidVideoLayer,
		current:     types.InvalidVideoLayer,
	}
	if f.isVideo {
		f.clockRate = 90000
	}
	return f
}

// SetClockRate overrides the codec clock rate (defaults 90 kHz video, 48 kHz audio).
func (f *Forwarder) SetClockRate(r uint32) {
	f.mu.Lock()
	f.clockRate = r
	f.mu.Unlock()
}

// SetMaxLayer caps the layer (subscriber preference).
func (f *Forwarder) SetMaxLayer(l types.VideoLayer) {
	f.mu.Lock()
	f.maxLayer = l
	f.mu.Unlock()
}

// MaxLayer returns the subscriber preference cap.
func (f *Forwarder) MaxLayer() types.VideoLayer {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maxLayer
}

// SetTargetLayer sets the allocator's decision. InvalidVideoLayer pauses video.
func (f *Forwarder) SetTargetLayer(l types.VideoLayer) {
	f.mu.Lock()
	f.allocTarget = l
	f.allocSet = true
	f.mu.Unlock()
}

// SetAvailableLayers sets the highest spatial layer the publisher sends.
func (f *Forwarder) SetAvailableLayers(maxSpatial int32) {
	f.mu.Lock()
	f.available = maxSpatial
	f.mu.Unlock()
}

// CurrentLayer returns the layer currently forwarded.
func (f *Forwarder) CurrentLayer() types.VideoLayer {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.current
}

// TargetLayer returns the layer the forwarder is switching to (or forwarding).
func (f *Forwarder) TargetLayer() types.VideoLayer {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.targetLocked()
}

// Switches returns the number of layer switches performed.
func (f *Forwarder) Switches() uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.switches
}

func (f *Forwarder) targetLocked() types.VideoLayer {
	if !f.isVideo {
		return types.VideoLayer{}
	}
	t := f.maxLayer
	if !t.IsValid() {
		return types.InvalidVideoLayer
	}
	if f.allocSet {
		if !f.allocTarget.IsValid() {
			return types.InvalidVideoLayer
		}
		if f.allocTarget.Spatial < t.Spatial {
			t.Spatial = f.allocTarget.Spatial
		}
		if f.allocTarget.Temporal < t.Temporal {
			t.Temporal = f.allocTarget.Temporal
		}
	}
	if t.Spatial > f.available {
		t.Spatial = f.available
	}
	return t
}

// Mute stops forwarding without losing munging state.
func (f *Forwarder) Mute(muted bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.muted == muted {
		return
	}
	f.muted = muted
	if !muted {
		f.resync = true
		if f.isVideo {
			f.current = types.InvalidVideoLayer
		}
	}
}

// Muted reports whether the forwarder is muted.
func (f *Forwarder) Muted() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.muted
}

// GetTranslation decides what to do with pkt (from spatial layer `layer`).
// It must not allocate.
func (f *Forwarder) GetTranslation(pkt *buffer.ExtPacket, layer int32) (TranslationParams, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.muted {
		return TranslationParams{Drop: true}, nil
	}
	if !f.isVideo {
		if f.resync || !f.started {
			f.startLayer(pkt, 0)
			f.resync = false
			return f.translate(pkt, 0, true), nil
		}
		return f.forwardCurrent(pkt, 0), nil
	}

	target := f.targetLocked()
	if !target.IsValid() {
		// Paused by the allocator or subscriber.
		if f.current.IsValid() {
			f.current = types.InvalidVideoLayer
		}
		return TranslationParams{Drop: true}, nil
	}
	cur := f.current
	if layer == target.Spatial && layer != cur.Spatial {
		if pkt.KeyFrame {
			f.startLayer(pkt, layer)
			f.current = types.VideoLayer{Spatial: layer, Temporal: target.Temporal}
			f.switches++
			return f.translate(pkt, layer, true), nil
		}
		if cur.IsValid() {
			// Keep the current layer flowing; ask for a keyframe on the target.
			return TranslationParams{Drop: true, RequestKeyFrame: true, KeyFrameLayer: target.Spatial}, nil
		}
		return TranslationParams{Drop: true, RequestKeyFrame: true, KeyFrameLayer: target.Spatial}, nil
	}
	if !cur.IsValid() || layer != cur.Spatial {
		if !cur.IsValid() {
			return TranslationParams{Drop: true, RequestKeyFrame: true, KeyFrameLayer: target.Spatial}, nil
		}
		return TranslationParams{Drop: true}, nil
	}
	f.current.Temporal = target.Temporal
	// Temporal layer filter (VP8 with TID).
	if f.isVP8 && pkt.VP8.TIDPresent && int32(pkt.VP8.TID) > target.Temporal {
		if pkt.ExtSequenceNumber == f.lastInExt+1 && pkt.VP8.PictureIDPresent &&
			(!f.droppedPIDValid || pkt.VP8.PictureID != f.droppedPID) {
			// Hide the dropped picture so picture ids stay continuous.
			f.pidOffset = (f.pidOffset + 1) & 0x7fff
			f.droppedPID, f.droppedPIDValid = pkt.VP8.PictureID, true
		}
		f.dropInOrder(pkt)
		return TranslationParams{Drop: true}, nil
	}
	return f.forwardCurrent(pkt, layer), nil
}

// startLayer computes offsets so pkt continues the outgoing stream.
func (f *Forwarder) startLayer(pkt *buffer.ExtPacket, layer int32) {
	if !f.started {
		f.started = true
		f.snOffset = pkt.ExtSequenceNumber - (1<<16 | uint64(pkt.SequenceNumber))
		f.tsOffset = pkt.ExtTimestamp - (1<<32 | uint64(pkt.Timestamp))
		f.lastOutExtSN = pkt.ExtSequenceNumber - f.snOffset - 1
		f.lastOutExtTS = pkt.ExtTimestamp - f.tsOffset
	} else {
		f.snOffset = pkt.ExtSequenceNumber - (f.lastOutExtSN + 1)
		delta := uint64(1)
		if gap := pkt.Arrival - f.lastArrival; gap > 0 && f.lastArrival != 0 {
			if d := uint64(gap) * uint64(f.clockRate) / 1e9; d > 0 {
				delta = d
			}
		}
		f.tsOffset = pkt.ExtTimestamp - (f.lastOutExtTS + delta)
	}
	f.switchExt = pkt.ExtSequenceNumber
	f.lastInExt = pkt.ExtSequenceNumber - 1
	if f.isVP8 && pkt.VP8.PictureIDPresent {
		if !f.vp8Started {
			f.vp8Started = true
			f.pidOffset, f.tl0Offset, f.keyOffset = 0, 0, 0
		} else {
			f.pidOffset = (pkt.VP8.PictureID - (f.lastOutPID + 1)) & 0x7fff
			f.tl0Offset = pkt.VP8.TL0PICIDX - (f.lastOutTL0 + 1)
			f.keyOffset = (pkt.VP8.KEYIDX - (f.lastOutKey + 1)) & 0x1f
		}
	}
	_ = layer
}

// dropInOrder absorbs an in-order packet that is intentionally not forwarded
// so the outgoing sequence numbers stay gap-free.
func (f *Forwarder) dropInOrder(pkt *buffer.ExtPacket) {
	if pkt.ExtSequenceNumber == f.lastInExt+1 {
		f.snOffset++
		f.lastInExt = pkt.ExtSequenceNumber
	}
}

func (f *Forwarder) forwardCurrent(pkt *buffer.ExtPacket, layer int32) TranslationParams {
	if pkt.ExtSequenceNumber < f.switchExt {
		return TranslationParams{Drop: true} // from before the switch point
	}
	if pkt.PaddingOnly {
		f.dropInOrder(pkt)
		return TranslationParams{Drop: true}
	}
	return f.translate(pkt, layer, false)
}

func (f *Forwarder) translate(pkt *buffer.ExtPacket, layer int32, isSwitch bool) TranslationParams {
	outSN := pkt.ExtSequenceNumber - f.snOffset
	outTS := pkt.ExtTimestamp - f.tsOffset
	if pkt.ExtSequenceNumber > f.lastInExt {
		f.lastInExt = pkt.ExtSequenceNumber
	}
	if outSN > f.lastOutExtSN {
		f.lastOutExtSN = outSN
		f.lastOutExtTS = outTS
		f.lastArrival = pkt.Arrival
	}
	tp := TranslationParams{IsSwitch: isSwitch, SequenceNumber: uint16(outSN), Timestamp: uint32(outTS)}
	if f.isVP8 && pkt.VP8.PictureIDPresent {
		tp.HasVP8 = true
		pid := (pkt.VP8.PictureID - f.pidOffset) & 0x7fff
		tl0 := pkt.VP8.TL0PICIDX - f.tl0Offset
		key := (pkt.VP8.KEYIDX - f.keyOffset) & 0x1f
		tp.VP8 = VP8Translation{PictureID: pid, TL0PICIDX: tl0, KEYIDX: key}
		if !pkt.VP8.MBit {
			tp.VP8.HeaderSizeDelta = 1
		}
		if int16((pid-f.lastOutPID)<<1) > 0 || isSwitch {
			f.lastOutPID = pid
			if pkt.VP8.TL0PICIDXPresent {
				f.lastOutTL0 = tl0
			}
			if pkt.VP8.KEYIDXPresent {
				f.lastOutKey = key
			}
		}
	}
	f.sent[uint16(outSN)%nackHistory] = sentPacket{inSN: pkt.SequenceNumber, layer: int8(layer), valid: true,
		ts: tp.Timestamp, pid: tp.VP8.PictureID, tl0: tp.VP8.TL0PICIDX, key: tp.VP8.KEYIDX}
	return tp
}

// TranslateNACK maps a munged sequence number from a subscriber NACK back to
// (spatial layer, original sequence number). ok is false when the packet was
// never forwarded or is too old.
func (f *Forwarder) TranslateNACK(munged uint16) (layer int32, original uint16, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.started {
		return 0, 0, false
	}
	if d := uint16(f.lastOutExtSN) - munged; d >= nackHistory {
		return 0, 0, false
	}
	s := f.sent[munged%nackHistory]
	if !s.valid {
		return 0, 0, false
	}
	return int32(s.layer), s.inSN, true
}

// RetransmitInfo translates a NACKed munged SN for retransmission, applying
// the per-SN throttle: at most 3 resends, no more often than minInterval
// (nanoseconds, e.g. 0.8*RTT). now is monotonic nanoseconds.
func (f *Forwarder) RetransmitInfo(munged uint16, now, minInterval int64) (NACKInfo, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.started {
		return NACKInfo{}, false
	}
	if d := uint16(f.lastOutExtSN) - munged; d >= nackHistory {
		return NACKInfo{}, false
	}
	s := &f.sent[munged%nackHistory]
	if !s.valid || s.rtxCnt >= 3 || (s.rtxAt != 0 && now-s.rtxAt < minInterval) {
		return NACKInfo{}, false
	}
	s.rtxCnt++
	s.rtxAt = now
	return NACKInfo{Layer: int32(s.layer), Original: s.inSN, Timestamp: s.ts,
		VP8: VP8Translation{PictureID: s.pid, TL0PICIDX: s.tl0, KEYIDX: s.key}}, true
}

// PaddingParams returns munged (sn, ts) for n padding-only probe packets: the
// first of n consecutive sequence numbers and the last timestamp. Padding
// shifts later packets by n so the sequence stays continuous.
func (f *Forwarder) PaddingParams(n int) (sn uint16, ts uint32, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.started || !f.current.IsValid() {
		return 0, 0, errs.New(errs.CodeFailedPrecondition, "forwarder not started")
	}
	sn = uint16(f.lastOutExtSN + 1)
	f.lastOutExtSN += uint64(n)
	f.snOffset -= uint64(n)
	// A late packet from before this point would now map onto a padding sequence
	// number: drop such packets like those from before a layer switch.
	f.switchExt = max(f.switchExt, f.lastInExt+1)
	return sn, uint32(f.lastOutExtTS), nil
}

// LastOut returns the last munged (extended) SN and TS and the arrival time
// of that packet; used for RTCP SR generation.
func (f *Forwarder) LastOut() (sn uint16, ts uint32, arrival int64, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return uint16(f.lastOutExtSN), uint32(f.lastOutExtTS), f.lastArrival, f.started
}

// TSOffset returns the current timestamp offset (incoming - outgoing) for SR
// translation.
func (f *Forwarder) TSOffset() (uint32, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return uint32(f.tsOffset), f.started && (f.current.IsValid() || !f.isVideo)
}
