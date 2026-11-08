// Package buffer implements the per-SSRC packet ring buffer used for NACK
// retransmission, RTP header and codec descriptor parsing (into ExtPacket),
// sequence/timestamp unwrapping and receive statistics.
//
// Hot path: Write is called once per received packet and must not allocate
// in steady state (target P2 in docs/ARCHITECTURE.md).
package buffer

import (
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/KeshavGoyal26/tessera/pkg/errs"
)

// MaxPacketSize is the slot size of the ring (MTU-sized packets).
const MaxPacketSize = 1500

// ExtPacket is a received RTP packet plus derived metadata. ExtPackets live
// in the ring's slot array; pointers handed to DownTracks are valid only
// during the fan-out call and must not be retained.
type ExtPacket struct {
	// Arrival is monotonic nanoseconds.
	Arrival int64
	// Unwrapped 64-bit sequence number and timestamp.
	ExtSequenceNumber uint64
	ExtTimestamp      uint64

	SequenceNumber uint16
	Timestamp      uint32
	SSRC           uint32
	PayloadType    uint8
	Marker         bool
	// Padding-only packets are tracked but not forwarded.
	PaddingOnly bool
	// Late is set when a higher sequence number was already received on this
	// stream (NACK recovery or reordering).
	Late bool

	// Raw is the full packet in the ring slot; Payload is a sub-slice.
	Raw        []byte
	HeaderSize int
	Payload    []byte

	// KeyFrame: the packet starts a keyframe (H.264: carries its SPS).
	KeyFrame bool

	// Audio level (RFC 6464): 0 loudest .. 127 silence.
	HasAudioLevel bool
	AudioLevel    uint8
	VoiceActivity bool
}

// Config configures a Buffer.
type Config struct {
	// Slots is the ring capacity in packets.
	Slots int
	// ClockRate of the codec (90000 video, 48000 Opus).
	ClockRate uint32
	// MimeType selects the payload parser (video/H264, audio/opus).
	MimeType string
	// AudioLevelExtID is the negotiated RFC 6464 extension id (0 = none).
	AudioLevelExtID uint8
}

// Stats are receive statistics for RTCP RR and metrics.
type Stats struct {
	PacketsReceived   uint64
	BytesReceived     uint64
	PacketsLost       uint64
	PacketsDuplicate  uint64
	PacketsOutOfOrder uint64
	NACKsSent         uint64
	// Jitter in RTP timestamp units.
	Jitter       float64
	HighestExtSN uint64
	// Expected is the number of packets expected (highest - first + 1).
	Expected uint64
	// LastSN/LastTS are the latest in-order sequence number and timestamp.
	LastTS uint32
}

type codecKind uint8

const (
	codecOther codecKind = iota
	codecH264
	codecOpus
)

// Buffer is a per-SSRC ring of recent packets. Write is called by exactly one
// goroutine (the layer reader); GetPacket and Stats may be called
// concurrently from RTCP goroutines.
type Buffer struct {
	cfg   Config
	codec codecKind
	// h264SPS is set once the stream has sent an SPS in band (atomic: parsePayload
	// also runs for retransmissions on RTCP goroutines).
	h264SPS atomic.Bool

	mu     sync.Mutex // guards slot contents against concurrent GetPacket
	slab   *[]byte
	pkts   []ExtPacket
	sizes  []uint16
	extSNs []uint64

	// Unwrapping state, writer goroutine only.
	started     bool
	highestSN   uint64
	firstSN     uint64
	highestTS   uint64
	lastTransit int64
	hdr         Header

	received, bytes, dup, ooo, nacks atomic.Uint64
	rtt                              atomic.Int64 // ns, sender round trip (NACK timing)
	highest, first                   atomic.Uint64
	jitterBits                       atomic.Uint64
	lastTS                           atomic.Uint32
	// missing tracks not-yet-received SNs for NACK generation (writer side).
	nack nackQueue
}

// New creates a Buffer. Memory for slots is allocated lazily from a pool.
func New(cfg Config) *Buffer {
	if cfg.Slots <= 0 {
		cfg.Slots = 500
	}
	if cfg.ClockRate == 0 {
		cfg.ClockRate = 90000
	}
	b := &Buffer{cfg: cfg}
	switch m := strings.ToLower(cfg.MimeType); m {
	case "video/h264":
		b.codec = codecH264
	case "audio/opus":
		b.codec = codecOpus
	}
	return b
}

// Config returns the buffer configuration.
func (b *Buffer) Config() Config { return b.cfg }

var errDuplicate = errs.New(errs.CodeAlreadyExists, "duplicate packet")
var errTooOld = errs.New(errs.CodeFailedPrecondition, "packet too old")

// ErrDuplicate is returned by Write for packets already in the ring.
var ErrDuplicate error = errDuplicate

// ErrTooOld is returned by Write for packets older than the ring.
var ErrTooOld error = errTooOld

func (b *Buffer) alloc() {
	b.slab = getSlab(b.cfg.Slots)
	b.pkts = make([]ExtPacket, b.cfg.Slots)
	b.sizes = make([]uint16, b.cfg.Slots)
	b.extSNs = make([]uint64, b.cfg.Slots)
}

// SetRTT sets the round-trip time to the sender, used to pace NACK retries.
func (b *Buffer) SetRTT(d time.Duration) { b.rtt.Store(int64(d)) }

// Write copies pkt into the ring, parses it and returns the slot's ExtPacket.
// arrival is monotonic nanoseconds. Duplicates and packets older than the ring
// return ErrDuplicate / ErrTooOld. No allocation in steady state.
func (b *Buffer) Write(pkt []byte, arrival int64) (*ExtPacket, error) {
	h := &b.hdr
	if err := ParseHeader(pkt, h); err != nil {
		return nil, err
	}
	if len(pkt) > MaxPacketSize {
		return nil, errShort
	}
	if b.slab == nil {
		b.alloc()
	}
	// Unwrap the sequence number.
	var ext uint64
	if !b.started {
		b.started = true
		ext = 1<<16 | uint64(h.SequenceNumber)
		b.highestSN, b.firstSN = ext, ext
		b.first.Store(ext)
		b.highestTS = 1<<32 | uint64(h.Timestamp)
	} else {
		diff := int16(h.SequenceNumber - uint16(b.highestSN))
		ext = uint64(int64(b.highestSN) + int64(diff))
	}
	slots := uint64(len(b.pkts))
	if ext+slots <= b.highestSN {
		return nil, errTooOld
	}
	idx := ext % slots
	if ext <= b.highestSN && b.extSNs[idx] == ext && b.sizes[idx] > 0 {
		b.dup.Add(1)
		return nil, errDuplicate
	}
	inOrder := ext > b.highestSN || ext == b.firstSN && b.received.Load() == 0
	if ext > b.highestSN {
		if ext > b.highestSN+1 {
			b.nack.addRange(b.highestSN+1, ext, arrival)
		}
		b.highestSN = ext
		b.highest.Store(ext)
	} else if ext < b.highestSN {
		b.ooo.Add(1)
		b.nack.remove(ext)
	}
	if ext < b.firstSN {
		b.firstSN = ext
		b.first.Store(ext)
	}
	// Unwrap the timestamp relative to the highest seen.
	tsDiff := int32(h.Timestamp - uint32(b.highestTS))
	extTS := uint64(int64(b.highestTS) + int64(tsDiff))
	if inOrder && extTS > b.highestTS {
		b.highestTS = extTS
	}

	b.mu.Lock()
	slab := *b.slab
	raw := slab[int(idx)*MaxPacketSize : int(idx)*MaxPacketSize+len(pkt)]
	copy(raw, pkt)
	b.sizes[idx] = uint16(len(pkt))
	b.extSNs[idx] = ext
	b.mu.Unlock()

	e := &b.pkts[idx]
	*e = ExtPacket{
		Arrival:           arrival,
		ExtSequenceNumber: ext,
		ExtTimestamp:      extTS,
		SequenceNumber:    h.SequenceNumber,
		Timestamp:         h.Timestamp,
		SSRC:              h.SSRC,
		PayloadType:       h.PayloadType,
		Marker:            h.Marker,
		Raw:               raw,
		HeaderSize:        h.HeaderSize,
		Payload:           raw[h.HeaderSize : len(raw)-h.PaddingSize],
	}
	e.PaddingOnly = len(e.Payload) == 0
	e.Late = !inOrder
	b.parsePayload(e, raw, h)

	b.received.Add(1)
	b.bytes.Add(uint64(len(pkt)))
	if inOrder {
		b.lastTS.Store(h.Timestamp)
		b.updateJitter(h.Timestamp, arrival)
	}
	return e, nil
}

func (b *Buffer) parsePayload(e *ExtPacket, raw []byte, h *Header) {
	if b.cfg.AudioLevelExtID != 0 {
		if v := FindExtension(raw, h, b.cfg.AudioLevelExtID); len(v) > 0 {
			e.HasAudioLevel = true
			e.AudioLevel = v[0] & 0x7f
			e.VoiceActivity = v[0]&0x80 != 0
		}
	}
	if e.PaddingOnly {
		return
	}
	switch b.codec {
	case codecH264:
		// A keyframe starts with its SPS: browsers send SPS and PPS before every IDR.
		// When that packet is lost and comes back late, the IDR fragment after it must
		// not count as the start: a layer switch there forwards the IDR without the
		// parameters of the new resolution (the late SPS is from before the switch and
		// is dropped), the subscriber cannot decode it and freezes until the next
		// keyframe. Streams that never send an SPS in band start on the IDR.
		sps, idr := H264KeyFrameParts(e.Payload)
		if sps && !b.h264SPS.Load() {
			b.h264SPS.Store(true)
		}
		e.KeyFrame = sps || idr && !b.h264SPS.Load()
	}
}

// updateJitter implements the RFC 3550 interarrival jitter estimator.
func (b *Buffer) updateJitter(ts uint32, arrival int64) {
	arrivalRTP := int64(float64(arrival) * float64(b.cfg.ClockRate) / 1e9)
	transit := arrivalRTP - int64(ts)
	if b.lastTransit != 0 {
		d := transit - b.lastTransit
		if d < 0 {
			d = -d
		}
		j := math.Float64frombits(b.jitterBits.Load())
		j += (float64(d) - j) / 16
		b.jitterBits.Store(math.Float64bits(j))
	}
	b.lastTransit = transit
}

// GetPacket copies the packet with original sequence number sn into dst and
// returns its length. It fails with errs.ErrNotFound when the packet has been
// overwritten or never arrived. Safe for concurrent use with Write.
func (b *Buffer) GetPacket(dst []byte, sn uint16) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.slab == nil {
		return 0, errNotFound
	}
	highest := b.highest.Load()
	diff := int16(uint16(highest) - sn)
	if diff < 0 {
		return 0, errNotFound
	}
	ext := highest - uint64(diff)
	idx := ext % uint64(len(b.pkts))
	if b.extSNs[idx] != ext || b.sizes[idx] == 0 {
		return 0, errNotFound
	}
	n := int(b.sizes[idx])
	if len(dst) < n {
		return 0, errShort
	}
	slab := *b.slab
	return copy(dst, slab[int(idx)*MaxPacketSize:int(idx)*MaxPacketSize+n]), nil
}

var errNotFound = errs.New(errs.CodeNotFound, "packet not in buffer")

// ParseExtPacket fills e from a raw packet (e.g. a copy obtained with
// GetPacket) using the buffer's codec configuration. ExtSequenceNumber and
// ExtTimestamp are not set.
func (b *Buffer) ParseExtPacket(raw []byte, e *ExtPacket) error {
	var h Header
	if err := ParseHeader(raw, &h); err != nil {
		return err
	}
	*e = ExtPacket{
		SequenceNumber: h.SequenceNumber,
		Timestamp:      h.Timestamp,
		SSRC:           h.SSRC,
		PayloadType:    h.PayloadType,
		Marker:         h.Marker,
		Raw:            raw,
		HeaderSize:     h.HeaderSize,
		Payload:        raw[h.HeaderSize : len(raw)-h.PaddingSize],
	}
	e.PaddingOnly = len(e.Payload) == 0
	b.parsePayload(e, raw, &h)
	return nil
}

// MissingSequenceNumbers appends sequence numbers to NACK to dst. It must be
// called from the writer goroutine (the layer reader calls it between
// packets on a 20 ms cadence). now is monotonic nanoseconds.
func (b *Buffer) MissingSequenceNumbers(dst []uint16, now int64) []uint16 {
	before := len(dst)
	dst = b.nack.collect(dst, now, b.highestSN, uint64(len(b.pkts)), b.rtt.Load())
	if len(dst) > before {
		b.nacks.Add(uint64(len(dst) - before))
	}
	return dst
}

// Stats returns a snapshot of receive statistics (lock-free).
func (b *Buffer) Stats() Stats {
	s := Stats{
		PacketsReceived:   b.received.Load(),
		BytesReceived:     b.bytes.Load(),
		PacketsDuplicate:  b.dup.Load(),
		PacketsOutOfOrder: b.ooo.Load(),
		NACKsSent:         b.nacks.Load(),
		Jitter:            math.Float64frombits(b.jitterBits.Load()),
		HighestExtSN:      b.highest.Load(),
		LastTS:            b.lastTS.Load(),
	}
	if s.PacketsReceived > 0 {
		s.Expected = s.HighestExtSN - b.first.Load() + 1
		if s.Expected > s.PacketsReceived {
			s.PacketsLost = s.Expected - s.PacketsReceived
		}
	}
	return s
}

// Release returns slot memory to the pool. The Buffer must not be used afterwards.
func (b *Buffer) Release() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.slab != nil {
		putSlab(b.slab)
		b.slab = nil
	}
}
