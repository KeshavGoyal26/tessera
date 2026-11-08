// Package forward implements per-DownTrack forwarding decisions for one
// single-stream track: when to start (video waits for a keyframe), pause, mute
// and resume, and the RTP munging that keeps the outgoing stream continuous
// across those (sequence numbers, timestamps). Publishers send one stream per
// track (no simulcast, ADR-0012).
//
// A Forwarder is owned by exactly one DownTrack. GetTranslation runs on the
// Receiver's read goroutine; pause/mute/NACK/padding calls come from others, so
// a small mutex serialises them.
package forward

import (
	"strings"
	"sync"

	"github.com/KeshavGoyal26/tessera/pkg/errs"
	"github.com/KeshavGoyal26/tessera/pkg/rtc/buffer"
)

// TranslationParams says what to do with one incoming packet for one DownTrack.
type TranslationParams struct {
	// Drop the packet (paused, muted, waiting for a keyframe, from before a restart).
	Drop bool
	// IsStart is true for the first packet after a (re)start.
	IsStart bool
	// RequestKeyFrame asks the Receiver for a keyframe (video waiting to start).
	RequestKeyFrame bool
	// Munged values to write into the outgoing header.
	SequenceNumber uint16
	Timestamp      uint32
}

// NACKInfo describes a forwarded packet for retransmission.
type NACKInfo struct {
	Original  uint16
	Timestamp uint32
}

// nackHistory is the number of forwarded packets remembered for NACK translation.
const nackHistory = 512

// offsetRanges is how many offset changes are remembered for late packets.
const offsetRanges = 64

// offsetRange holds the sequence-number offset for incoming packets with ext SN >=
// from, up to the next range. The offset changes when packets are absorbed (padding
// from the publisher) or probe padding is inserted; a packet that arrives late (NACK
// recovery, reordering) must use the offset of its own range, not the current one.
type offsetRange struct {
	from uint64
	sn   uint64
}

type sentPacket struct {
	outSN  uint16 // the munged SN this slot holds (slots are reused every nackHistory)
	inSN   uint16
	valid  bool
	ts     uint32
	rtxCnt uint8
	rtxAt  int64
}

// Forwarder decides and translates packets for one DownTrack.
type Forwarder struct {
	isVideo   bool
	clockRate uint32

	mu sync.Mutex
	// muted: the track is muted (publisher or admin); paused: the allocator or the
	// subscriber stopped it. forwarding: started since the last (re)start; video
	// restarts on an in-order keyframe.
	muted, paused, forwarding bool
	restarts                  uint64

	// Munging state.
	started      bool
	snOffset     uint64
	tsOffset     uint64
	lastInExt    uint64 // last in-order incoming ext SN
	startExt     uint64 // first ext SN forwarded since the last (re)start
	lastOutExtSN uint64
	lastOutExtTS uint64
	lastArrival  int64

	sent [nackHistory]sentPacket

	// Ring of offset ranges, newest at rangeHead.
	ranges    [offsetRanges]offsetRange
	nRanges   int
	rangeHead int
}

// NewForwarder creates a forwarder for the codec MIME type.
func NewForwarder(mime string) *Forwarder {
	f := &Forwarder{isVideo: strings.HasPrefix(strings.ToLower(mime), "video/"), clockRate: 48000}
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

// Mute stops forwarding without losing munging state; unmuting restarts.
func (f *Forwarder) Mute(muted bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.muted == muted {
		return
	}
	f.muted = muted
	if !muted {
		f.forwarding = false
	}
}

// Muted reports whether the forwarder is muted.
func (f *Forwarder) Muted() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.muted
}

// SetPaused stops (true) or resumes (false) forwarding: bandwidth allocation or
// a subscriber that disabled the track. Video resumes on a keyframe.
func (f *Forwarder) SetPaused(paused bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.paused == paused {
		return
	}
	f.paused = paused
	if paused {
		f.forwarding = false
	}
}

// Paused reports whether forwarding is paused.
func (f *Forwarder) Paused() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.paused
}

// Forwarding reports whether packets are being forwarded (started, not paused,
// not waiting for a keyframe).
func (f *Forwarder) Forwarding() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.forwarding && !f.paused
}

// Restarts returns how many times forwarding (re)started on a keyframe.
func (f *Forwarder) Restarts() uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.restarts
}

// GetTranslation decides what to do with pkt. It must not allocate.
func (f *Forwarder) GetTranslation(pkt *buffer.ExtPacket) (TranslationParams, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.muted || f.paused {
		return TranslationParams{Drop: true}, nil
	}
	if !f.forwarding {
		if !f.isVideo {
			f.restart(pkt)
			return f.translate(pkt, true), nil
		}
		// Start only on a keyframe start that arrives in order. A late one (its first
		// packet was lost and recovered) follows packets of the same keyframe that
		// were already dropped while waiting: forwarding it would send a keyframe
		// with holes no one can fill. Ask for a fresh keyframe instead.
		if pkt.KeyFrame && !pkt.Late {
			f.restart(pkt)
			f.restarts++
			return f.translate(pkt, true), nil
		}
		return TranslationParams{Drop: true, RequestKeyFrame: true}, nil
	}
	if pkt.ExtSequenceNumber < f.startExt {
		return TranslationParams{Drop: true}, nil // from before the (re)start
	}
	if pkt.PaddingOnly {
		f.dropInOrder(pkt)
		return TranslationParams{Drop: true}, nil
	}
	if pkt.ExtSequenceNumber <= f.lastInExt {
		// Late packet: translate with the offset of its own range.
		r, ok := f.offsetsFor(pkt.ExtSequenceNumber)
		if !ok {
			return TranslationParams{Drop: true}, nil
		}
		return f.translateWith(pkt, false, r.sn), nil
	}
	return f.translate(pkt, false), nil
}

// restart computes offsets so pkt continues the outgoing stream.
func (f *Forwarder) restart(pkt *buffer.ExtPacket) {
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
	f.forwarding = true
	f.startExt = pkt.ExtSequenceNumber
	f.lastInExt = pkt.ExtSequenceNumber - 1
	f.rangeHead, f.nRanges = 0, 1
	f.ranges[0] = offsetRange{from: pkt.ExtSequenceNumber, sn: f.snOffset}
}

// pushRange records that packets from `from` on use the current offset.
func (f *Forwarder) pushRange(from uint64) {
	f.rangeHead = (f.rangeHead + 1) % offsetRanges
	f.ranges[f.rangeHead] = offsetRange{from: from, sn: f.snOffset}
	if f.nRanges < offsetRanges {
		f.nRanges++
	}
}

// offsetsFor returns the range that applied when packet ext would have been in
// order; ok is false when it is older than the remembered history.
func (f *Forwarder) offsetsFor(ext uint64) (r offsetRange, ok bool) {
	for i := range f.nRanges {
		r = f.ranges[(f.rangeHead-i+offsetRanges)%offsetRanges]
		if ext >= r.from {
			return r, true
		}
	}
	return offsetRange{}, false
}

// dropInOrder absorbs a new (not late) packet that is intentionally not forwarded
// (padding-only), so the outgoing sequence numbers stay gap-free. After a gap the
// packets missing before it keep their slots: they use the offset of the range
// before this one.
func (f *Forwarder) dropInOrder(pkt *buffer.ExtPacket) {
	if pkt.ExtSequenceNumber > f.lastInExt {
		// The missing packets' slots count as used, so padding cannot take them.
		if reserved := pkt.ExtSequenceNumber - 1 - f.snOffset; reserved > f.lastOutExtSN {
			f.lastOutExtSN = reserved
		}
		f.snOffset++
		f.lastInExt = pkt.ExtSequenceNumber
		f.pushRange(pkt.ExtSequenceNumber + 1)
	}
}

func (f *Forwarder) translate(pkt *buffer.ExtPacket, isStart bool) TranslationParams {
	return f.translateWith(pkt, isStart, f.snOffset)
}

func (f *Forwarder) translateWith(pkt *buffer.ExtPacket, isStart bool, snOffset uint64) TranslationParams {
	outSN := pkt.ExtSequenceNumber - snOffset
	outTS := pkt.ExtTimestamp - f.tsOffset
	if pkt.ExtSequenceNumber > f.lastInExt {
		f.lastInExt = pkt.ExtSequenceNumber
	}
	if outSN > f.lastOutExtSN {
		f.lastOutExtSN = outSN
		f.lastOutExtTS = outTS
		f.lastArrival = pkt.Arrival
	}
	tp := TranslationParams{IsStart: isStart, SequenceNumber: uint16(outSN), Timestamp: uint32(outTS)}
	f.sent[uint16(outSN)%nackHistory] = sentPacket{outSN: uint16(outSN), inSN: pkt.SequenceNumber, valid: true, ts: tp.Timestamp}
	return tp
}

// TranslateNACK maps a munged sequence number from a subscriber NACK back to the
// original sequence number. ok is false when the packet was never forwarded or is
// too old.
func (f *Forwarder) TranslateNACK(munged uint16) (original uint16, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.started {
		return 0, false
	}
	if d := uint16(f.lastOutExtSN) - munged; d >= nackHistory {
		return 0, false
	}
	s := f.sent[munged%nackHistory]
	if !s.valid || s.outSN != munged {
		return 0, false
	}
	return s.inSN, true
}

// RetransmitInfo translates a NACKed munged SN for retransmission, applying the
// per-SN throttle: at most 3 resends, no more often than minInterval (nanoseconds,
// e.g. 0.8*RTT). now is monotonic nanoseconds.
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
	// A slot holds the packet sent nackHistory SNs earlier until its own packet is
	// forwarded: a NACK for an SN not forwarded yet (lost upstream, still being
	// recovered) must not resend that older packet. The subscriber would take it in
	// place of the real one, discard the real one as a duplicate, and could never
	// assemble the frame (regression: seconds of frozen video under uplink loss).
	if !s.valid || s.outSN != munged || s.rtxCnt >= 3 || (s.rtxAt != 0 && now-s.rtxAt < minInterval) {
		return NACKInfo{}, false
	}
	s.rtxCnt++
	s.rtxAt = now
	return NACKInfo{Original: s.inSN, Timestamp: s.ts}, true
}

// PaddingParams returns munged (sn, ts) for n padding-only probe packets: the
// first of n consecutive sequence numbers and the last timestamp. Padding shifts
// later packets by n so the sequence stays continuous.
func (f *Forwarder) PaddingParams(n int) (sn uint16, ts uint32, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.started || !f.forwarding || f.paused {
		return 0, 0, errs.New(errs.CodeFailedPrecondition, "forwarder not forwarding")
	}
	sn = uint16(f.lastOutExtSN + 1)
	// Padding has no original packet: forget whatever media used these NACK slots
	// before, or a NACK for lost padding would resend that old packet.
	for i := 0; i < n; i++ {
		f.sent[(sn+uint16(i))%nackHistory].valid = false
	}
	f.lastOutExtSN += uint64(n)
	f.snOffset -= uint64(n)
	// Packets after this point shift by n; late packets from before it keep
	// their old offset and fill their original gap.
	f.pushRange(f.lastInExt + 1)
	return sn, uint32(f.lastOutExtTS), nil
}

// LastOut returns the last munged (extended) SN and TS and the arrival time of
// that packet; used for RTCP SR generation.
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
	return uint32(f.tsOffset), f.started && f.forwarding
}
