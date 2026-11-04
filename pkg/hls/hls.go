// Package hls turns RTP into fMP4/CMAF HLS without transcoding (ADR-0007):
// RTP depacketizers (H.264 single NAL/STAP-A/FU-A, Opus), an ISO-BMFF
// writer (init segment with avcC/dOps, moof+mdat fragments), a segmenter
// that cuts on keyframes, and media/master playlist writers.
//
// Ownership: WP4. Pure Go; no FFmpeg here (AAC and VP8 paths live in
// pkg/recorder and pipe through FFmpeg subprocesses).
package hls

import (
	"context"
	"time"

	"github.com/pion/rtp"

	"github.com/KeshavGoyal26/tessera/pkg/errs"
)

// Codec of a muxed track.
type Codec string

// Supported codecs.
const (
	CodecH264 Codec = "H264"
	CodecOpus Codec = "Opus"
	CodecAAC  Codec = "AAC"
)

// TrackSpec describes one track for the init segment.
type TrackSpec struct {
	ID        uint32
	Codec     Codec
	Timescale uint32
	// Video.
	Width, Height uint32
	SPS, PPS      []byte
	// Audio.
	SampleRate uint32
	Channels   uint16
	// Opus pre-skip in samples (dOps).
	PreSkip uint16
	// AudioSpecificConfig for AAC (esds).
	AudioSpecificConfig []byte
}

// Sample is one access unit (video, AVCC length-prefixed) or audio frame.
type Sample struct {
	// DTS in track timescale units, unwrapped.
	DTS int64
	// CTSOffset = PTS - DTS (0 for baseline H.264 and audio).
	CTSOffset int32
	Duration  uint32
	KeyFrame  bool
	Data      []byte
}

// AccessUnit is a depacketized H.264 frame.
type AccessUnit struct {
	RTPTimestamp uint32
	NALUs        [][]byte
	KeyFrame     bool
	// SPS/PPS seen in this AU (for init segment updates).
	SPS, PPS []byte
}

// H264Depacketizer assembles access units from RTP (RFC 6184, packetization-mode 1).
type H264Depacketizer struct{}

// Push adds a packet; it returns a complete AU on frame boundary (marker bit
// or timestamp change), or nil. Lost fragments return errs.ErrMalformed and
// the depacketizer resynchronizes on the next keyframe.
func (d *H264Depacketizer) Push(pkt *rtp.Packet) (*AccessUnit, error) {
	return nil, errs.NotImplemented("hls.H264Depacketizer.Push")
}

// Segment is a finished media segment.
type Segment struct {
	Name     string
	Data     []byte
	Duration time.Duration
	// ProgramDateTime is the wall-clock time of the first sample.
	ProgramDateTime time.Time
	// Discontinuity is set when timing or codec config changed.
	Discontinuity bool
}

// Sink receives muxer output (typically a storage-backed uploader).
type Sink interface {
	WriteInit(ctx context.Context, name string, data []byte) error
	WriteSegment(ctx context.Context, seg Segment) error
	WritePlaylist(ctx context.Context, name string, data []byte) error
}

// MuxerConfig configures a Muxer.
type MuxerConfig struct {
	TargetSegmentDuration time.Duration
	// Live writes an EVENT playlist after every segment.
	Live bool
	// PlaylistName defaults to "index.m3u8".
	PlaylistName string
}

// Muxer writes one track as fMP4 HLS.
type Muxer struct {
	cfg  MuxerConfig
	spec TrackSpec
	sink Sink
}

// NewMuxer creates a muxer and writes the init segment lazily.
func NewMuxer(cfg MuxerConfig, spec TrackSpec, sink Sink) (*Muxer, error) {
	return &Muxer{cfg: cfg, spec: spec, sink: sink}, nil
}

// WriteSample appends a sample, cutting segments on keyframes after the target duration.
func (m *Muxer) WriteSample(ctx context.Context, s Sample) error {
	return errs.NotImplemented("hls.Muxer.WriteSample")
}

// Close flushes the last segment and writes the final VOD playlist with EXT-X-ENDLIST.
func (m *Muxer) Close(ctx context.Context) error {
	return errs.NotImplemented("hls.Muxer.Close")
}

// Rendition is one entry of a master playlist.
type Rendition struct {
	URI        string
	Codecs     string
	Bandwidth  int
	Width      int
	Height     int
	AudioGroup string
	IsAudio    bool
}

// MasterPlaylist encodes a master playlist with demuxed audio (EXT-X-MEDIA).
func MasterPlaylist(renditions []Rendition) ([]byte, error) {
	return nil, errs.NotImplemented("hls.MasterPlaylist")
}
