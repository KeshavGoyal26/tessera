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

// CodecString returns the RFC 6381 codec identifier for playlists.
func (s TrackSpec) CodecString() string {
	switch s.Codec {
	case CodecH264:
		if info, err := ParseSPS(s.SPS); err == nil {
			return info.CodecString()
		}
		return "avc1.42e01f"
	case CodecOpus:
		return "opus"
	case CodecAAC:
		return "mp4a.40.2"
	}
	return ""
}

// Sample is one access unit (video, AVCC length-prefixed) or audio frame.
type Sample struct {
	// DTS in track timescale units, unwrapped (any origin; the muxer rebases).
	DTS int64
	// CTSOffset = PTS - DTS (0 for baseline H.264 and audio).
	CTSOffset int32
	// Duration is optional; the muxer derives it from the next sample's DTS.
	Duration uint32
	KeyFrame bool
	Data     []byte
}

// Segment is a finished media segment.
type Segment struct {
	Name     string
	Data     []byte
	Duration time.Duration
	// ProgramDateTime is the wall-clock time of the first sample.
	ProgramDateTime time.Time
}

// Sink receives muxer output (typically a storage-backed uploader).
type Sink interface {
	WriteInit(ctx context.Context, name string, data []byte) error
	WriteSegment(ctx context.Context, seg Segment) error
	WritePlaylist(ctx context.Context, name string, data []byte) error
}

// Rendition is one entry of a master playlist.
type Rendition struct {
	URI        string
	Codecs     string
	Bandwidth  int
	Width      int
	Height     int
	AudioGroup string
	Name       string
	IsAudio    bool
}
