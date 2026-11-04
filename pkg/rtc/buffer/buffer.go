// Package buffer implements the per-SSRC packet ring buffer used for NACK
// retransmission, RTP header and codec descriptor parsing (into ExtPacket),
// sequence/timestamp unwrapping and receive statistics.
//
// Hot path: Write is called once per received packet and must not allocate
// in steady state (target P2 in docs/ARCHITECTURE.md).
package buffer

import (
	"github.com/KeshavGoyal26/tessera/pkg/errs"
)

// MaxPacketSize is the slot size of the ring (MTU-sized packets).
const MaxPacketSize = 1500

// VP8Descriptor is the parsed VP8 payload descriptor (RFC 7741).
type VP8Descriptor struct {
	FirstByte        byte
	PictureIDPresent bool
	// MBit: 15-bit picture id when true, 7-bit otherwise.
	MBit             bool
	PictureID        uint16
	TL0PICIDXPresent bool
	TL0PICIDX        uint8
	TIDPresent       bool
	TID              uint8
	Y                bool
	KEYIDXPresent    bool
	KEYIDX           uint8
	// HeaderSize is the descriptor length in bytes.
	HeaderSize int
	IsKeyFrame bool
}

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

	// Raw is the full packet in the ring slot; Payload is a sub-slice.
	Raw        []byte
	HeaderSize int
	Payload    []byte

	// Spatial/Temporal layer of the packet (temporal from codec descriptor).
	Spatial  int32
	Temporal int32
	KeyFrame bool
	VP8      VP8Descriptor

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
	// MimeType selects the descriptor parser (video/VP8, video/H264, audio/opus).
	MimeType string
	// AudioLevelExtID is the negotiated RFC 6464 extension id (0 = none).
	AudioLevelExtID uint8
	// Spatial layer this SSRC carries.
	Spatial int32
}

// Stats are receive statistics for RTCP RR and metrics.
type Stats struct {
	PacketsReceived   uint64
	BytesReceived     uint64
	PacketsLost       uint64
	PacketsDuplicate  uint64
	PacketsOutOfOrder uint64
	NACKsSent         uint64
	Jitter            float64
	HighestExtSN      uint64
}

// Buffer is a per-SSRC ring of recent packets.
type Buffer struct {
	cfg Config
}

// New creates a Buffer. Memory for slots is allocated lazily from a pool.
func New(cfg Config) *Buffer {
	return &Buffer{cfg: cfg}
}

// Write copies pkt into the ring, parses it and returns the slot's ExtPacket.
// arrival is monotonic nanoseconds.
func (b *Buffer) Write(pkt []byte, arrival int64) (*ExtPacket, error) {
	return nil, errs.NotImplemented("buffer.Write")
}

// GetPacket copies the packet with original sequence number sn into dst and
// returns its length. It fails with errs.ErrNotFound when the packet has been
// overwritten or never arrived.
func (b *Buffer) GetPacket(dst []byte, sn uint16) (int, error) {
	return 0, errs.NotImplemented("buffer.GetPacket")
}

// MissingSequenceNumbers returns sequence numbers to NACK (appending to dst).
func (b *Buffer) MissingSequenceNumbers(dst []uint16, now int64) []uint16 {
	return dst
}

// Stats returns a snapshot of receive statistics.
func (b *Buffer) Stats() Stats { return Stats{} }

// Release returns slot memory to the pool. The Buffer must not be used afterwards.
func (b *Buffer) Release() {}
