package recorder

import (
	"io"
	"sync"
	"sync/atomic"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

// RTPSource decouples the recording pipeline from pkg/client so pipelines
// can be tested with synthetic and fixture RTP before the client exists.
// *client.RemoteTrack satisfies it.
type RTPSource interface {
	// Read reads one RTP packet into b.
	Read(b []byte) (n int, err error)
	Codec() webrtc.RTPCodecParameters
	// RequestKeyFrame asks the publisher for a keyframe (PLI via the SFU).
	RequestKeyFrame() error
}

// MemSource is an in-memory RTPSource fed with Push; Close ends the stream
// with io.EOF once queued packets were read.
type MemSource struct {
	codec     webrtc.RTPCodecParameters
	ch        chan []byte
	once      sync.Once
	KeyFrames atomic.Int64
}

// NewMemSource creates a source for a MIME type ("video/H264", "audio/opus").
func NewMemSource(mime string, fmtp string) *MemSource {
	clock := uint32(90000)
	if mime == "audio/opus" {
		clock = 48000
	}
	return &MemSource{
		codec: webrtc.RTPCodecParameters{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: mime, ClockRate: clock, SDPFmtpLine: fmtp}, PayloadType: 96},
		ch:    make(chan []byte, 4096),
	}
}

// Push queues a packet.
func (m *MemSource) Push(p *rtp.Packet) error {
	b, err := p.Marshal()
	if err != nil {
		return err
	}
	m.ch <- b
	return nil
}

// Close ends the stream.
func (m *MemSource) Close() { m.once.Do(func() { close(m.ch) }) }

// Read implements RTPSource.
func (m *MemSource) Read(b []byte) (int, error) {
	p, ok := <-m.ch
	if !ok {
		return 0, io.EOF
	}
	return copy(b, p), nil
}

// Codec implements RTPSource.
func (m *MemSource) Codec() webrtc.RTPCodecParameters { return m.codec }

// RequestKeyFrame implements RTPSource.
func (m *MemSource) RequestKeyFrame() error { m.KeyFrames.Add(1); return nil }
