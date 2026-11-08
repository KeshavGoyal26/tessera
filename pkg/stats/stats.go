// Package stats defines the live statistics model of a Tessera node: plain
// snapshot structs (JSON-serialisable, consumed by the admin dashboard and the
// recorder), the Provider interface that produces them, lock-free counters
// used on the packet path, and a Prometheus collector with bounded label
// cardinality. See docs/STATS.md.
//
// Design rules:
//   - The packet path only touches atomics (Meter.Add, Counter.Inc). No locks,
//     no allocation.
//   - Snapshot() is called from control goroutines (dashboard poll, Prometheus
//     scrape) at about 1 Hz. It may take control-plane locks and allocate.
//   - Rates are computed by the meters themselves over windows of at least
//     MinRateWindow, so several concurrent consumers see consistent values.
package stats

import (
	"time"
)

// Provider produces snapshots. Implementations must make Snapshot cheap
// enough to call every second with thousands of tracks.
type Provider interface {
	Snapshot() *Snapshot
}

// ProviderFunc adapts a function to Provider.
type ProviderFunc func() *Snapshot

// Snapshot implements Provider.
func (f ProviderFunc) Snapshot() *Snapshot { return f() }

// Snapshot is the state of one node at one instant.
type Snapshot struct {
	// Time the snapshot was taken.
	Time  time.Time   `json:"time"`
	Node  NodeStats   `json:"node"`
	Rooms []RoomStats `json:"rooms"`
}

// NodeStats are process-wide statistics.
type NodeStats struct {
	NodeID  string `json:"node_id"`
	Version string `json:"version"`
	// StartedAt is the process start time.
	StartedAt time.Time `json:"started_at"`
	// CPUPercent is process CPU usage over the last window, 100 = one core.
	CPUPercent float64 `json:"cpu_percent"`
	NumCPU     int     `json:"num_cpu"`
	// RSSBytes is the resident set size (Linux), or Go heap+stacks elsewhere.
	RSSBytes   uint64 `json:"rss_bytes"`
	HeapBytes  uint64 `json:"heap_bytes"`
	Goroutines int    `json:"goroutines"`
	// GCPauseP99Ns is the p99 GC stop-the-world pause over the process lifetime.
	GCPauseP99Ns uint64 `json:"gc_pause_p99_ns"`
	// GCCPUPercent is the fraction of CPU spent in GC since start.
	GCCPUPercent float64 `json:"gc_cpu_percent"`
	NumGC        uint64  `json:"num_gc"`

	// Media socket traffic (all PeerConnections), per second.
	UDPPacketsInPerSec  float64 `json:"udp_packets_in_per_sec"`
	UDPPacketsOutPerSec float64 `json:"udp_packets_out_per_sec"`
	UDPBytesInPerSec    float64 `json:"udp_bytes_in_per_sec"`
	UDPBytesOutPerSec   float64 `json:"udp_bytes_out_per_sec"`

	PeerConnections int `json:"peer_connections"`
	Rooms           int `json:"rooms"`
	Participants    int `json:"participants"`
	PublishedTracks int `json:"published_tracks"`
	// SubscribedTracks counts DownTracks.
	SubscribedTracks int `json:"subscribed_tracks"`
}

// RoomStats describe one room.
type RoomStats struct {
	SID       string    `json:"sid"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	// ActiveRecording is true while a recording of the room is running.
	ActiveRecording bool               `json:"active_recording"`
	Participants    []ParticipantStats `json:"participants"`
	// ActiveSpeakers, loudest first.
	ActiveSpeakers []SpeakerStats `json:"active_speakers"`
}

// SpeakerStats is one active speaker.
type SpeakerStats struct {
	ParticipantSID string `json:"participant_sid"`
	Identity       string `json:"identity"`
	// Level 0..1 (1 = loud).
	Level float64 `json:"level"`
}

// Connection quality values (match tessera.v1.ConnectionQuality names).
const (
	QualityUnknown   = "unknown"
	QualityLost      = "lost"
	QualityPoor      = "poor"
	QualityGood      = "good"
	QualityExcellent = "excellent"
)

// ICE candidate types of the selected pair (local side of the server).
const (
	CandidateHost  = "host"
	CandidateSrflx = "srflx"
	CandidatePrflx = "prflx"
	CandidateRelay = "relay"
	// CandidateTCP is a host candidate over ICE-TCP.
	CandidateTCP = "tcp"
)

// ParticipantStats describe one participant and its PeerConnections.
type ParticipantStats struct {
	SID      string    `json:"sid"`
	Identity string    `json:"identity"`
	Name     string    `json:"name,omitempty"`
	JoinedAt time.Time `json:"joined_at"`
	// State: joining, joined, active, disconnected.
	State string `json:"state"`
	// Kind: standard, recorder, loadtest.
	Kind   string `json:"kind"`
	Hidden bool   `json:"hidden,omitempty"`
	// ClientSDK, e.g. "js 0.1.0".
	ClientSDK string `json:"client_sdk,omitempty"`

	// ICECandidateType of the remote side of the selected pair (host, srflx,
	// prflx, relay); "tcp" when ICE-TCP is used.
	ICECandidateType string `json:"ice_candidate_type"`
	// ICESetupMs is the time from PeerConnection creation to ICE connected.
	ICESetupMs int64 `json:"ice_setup_ms"`
	// DTLSSetupMs is the time from ICE connected to DTLS connected.
	DTLSSetupMs int64 `json:"dtls_setup_ms"`
	// RTTMs is the media round-trip time (RTCP) or, before media, the signal RTT.
	RTTMs float64 `json:"rtt_ms"`
	// ConnectionQuality is one of the Quality* constants; Score is 1..5.
	ConnectionQuality string  `json:"connection_quality"`
	QualityScore      float64 `json:"quality_score"`
	// BandwidthEstimateBps is the downlink estimate of the subscriber PC.
	BandwidthEstimateBps int64 `json:"bandwidth_estimate_bps"`

	Published  []PublishedTrackStats  `json:"published"`
	Subscribed []SubscribedTrackStats `json:"subscribed"`
}

// PublishedTrackStats describe one published track (a Receiver).
type PublishedTrackStats struct {
	SID string `json:"sid"`
	// Kind: audio or video.
	Kind string `json:"kind"`
	// Source: camera, microphone, screen_share, screen_share_audio.
	Source string `json:"source"`
	Codec  string `json:"codec"`
	Muted  bool   `json:"muted"`
	// SSRC of the track's stream (one per track, no simulcast); 0 before media.
	SSRC uint32 `json:"ssrc"`
	// Width/Height announced by the publisher (video).
	Width  uint32 `json:"width,omitempty"`
	Height uint32 `json:"height,omitempty"`
	// Active is false when no packet arrived in the last window.
	Active      bool    `json:"active"`
	BitrateBps  float64 `json:"bitrate_bps"`
	PacketsRecv uint64  `json:"packets_recv"`
	PacketsLost uint64  `json:"packets_lost"`
	// LossPercent over the last window.
	LossPercent float64 `json:"loss_percent"`
	JitterMs    float64 `json:"jitter_ms"`
	// NACKs sent to the publisher, PLIs/FIRs sent to the publisher.
	NACKs uint64 `json:"nacks"`
	PLIs  uint64 `json:"plis"`
	FIRs  uint64 `json:"firs"`
	// Subscribers is the number of DownTracks attached.
	Subscribers int `json:"subscribers"`
	// AudioLevel 0..1 (audio only).
	AudioLevel float64 `json:"audio_level,omitempty"`
}

// SubscribedTrackStats describe one DownTrack.
type SubscribedTrackStats struct {
	TrackSID     string `json:"track_sid"`
	PublisherSID string `json:"publisher_sid"`
	Kind         string `json:"kind"`
	Codec        string `json:"codec"`
	// BandwidthEstimateBps of the subscriber connection (same for all tracks).
	BandwidthEstimateBps int64   `json:"bandwidth_estimate_bps"`
	BitrateBps           float64 `json:"bitrate_bps"`
	PacketsSent          uint64  `json:"packets_sent"`
	// PacketsLost as reported by the subscriber's receiver reports.
	PacketsLost uint64  `json:"packets_lost"`
	LossPercent float64 `json:"loss_percent"`
	JitterMs    float64 `json:"jitter_ms"`
	RTTMs       float64 `json:"rtt_ms"`
	// Restarts counts video (re)starts on a keyframe (start, resume, unmute).
	Restarts uint64 `json:"restarts"`
	// NACKs/PLIs received from the subscriber; Retransmits sent in answer.
	NACKs       uint64 `json:"nacks"`
	PLIs        uint64 `json:"plis"`
	Retransmits uint64 `json:"retransmits"`
	// Paused by the allocator (bandwidth) or the subscriber; Muted by the publisher.
	Paused bool `json:"paused"`
	Muted  bool `json:"muted"`
}
