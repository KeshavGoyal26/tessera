// Package bwe implements send-side bandwidth estimation from TWCC feedback
// (delay-based trendline + loss-based bound, from scratch) and the
// per-subscriber StreamAllocator that turns the estimate into target layers.
package bwe

import (
	"time"

	"github.com/pion/rtcp"

	"github.com/KeshavGoyal26/tessera/pkg/rtc/types"
)

// CongestionState of the estimator.
type CongestionState int

// Congestion states.
const (
	StateNormal CongestionState = iota
	StateUnderuse
	StateOveruse
)

// Estimator estimates downlink bandwidth of one subscriber PeerConnection.
type Estimator interface {
	// OnPacketSent records a sent packet with its transport-wide sequence number.
	// Called on the hot path: must not allocate or block.
	OnPacketSent(twccSeq uint16, size int, sentAt int64, probe bool)
	// OnTransportFeedback processes a TWCC feedback packet.
	OnTransportFeedback(fb *rtcp.TransportLayerCC, receivedAt time.Time)
	// OnRTT updates the round-trip time (from RR/SR).
	OnRTT(rtt time.Duration)
	// Estimate returns the current estimate in bits per second.
	Estimate() int64
	// State returns the congestion state.
	State() CongestionState
}

// Prober is implemented by estimators that can measure a probe cluster.
type Prober interface {
	StartProbe()
	FinishProbe(now time.Time)
	ProbeResult() (done bool, estimate int64)
}

// Padder is implemented by tracks that can carry probe padding: it sends up to n
// padding-only packets of PaddingPacketBytes and returns how many it sent.
type Padder interface {
	WritePadding(n int) int
}

// PaddingPacketBytes is the size accounted for one padding-only probe packet
// (255 bytes of padding plus RTP header and extension).
const PaddingPacketBytes = 255 + 20

// AllocatableTrack is the allocator's view of a DownTrack.
type AllocatableTrack interface {
	ID() string
	IsVideo() bool
	// Priority: higher first. Screen share defaults higher than camera.
	Priority() uint32
	// MaxLayer requested by the subscriber (preferences/adaptive stream).
	MaxLayer() types.VideoLayer
	// Bitrates measured on the publisher side for each layer.
	Bitrates() types.Bitrates
	// SetTargetLayer applies the allocation (InvalidVideoLayer = paused).
	SetTargetLayer(l types.VideoLayer)
}

// Allocation is the allocator's decision for one track.
type Allocation struct {
	TrackID string
	Target  types.VideoLayer
	Paused  bool
	Bitrate int64
}

// Allocator distributes one subscriber's bandwidth over its DownTracks.
type Allocator interface {
	AddTrack(t AllocatableTrack)
	RemoveTrack(id string)
	// OnTrackChanged is called when preferences or available layers change.
	OnTrackChanged(id string)
	// OnPausedChanged is invoked with tracks paused/resumed by the allocator.
	OnPausedChanged(func(changed []Allocation))
	Start()
	Stop()
}

// Config configures estimation and allocation.
type Config struct {
	Enabled    bool
	AllowPause bool
	Probe      bool
	// MinBitrate/MaxBitrate clamp the estimate.
	MinBitrate int64
	MaxBitrate int64
	// UpgradeHold is how long an estimate must support a higher layer before upgrading.
	UpgradeHold time.Duration
	// StartBitrate is the initial estimate (default 1.5 Mbit/s).
	StartBitrate int64
}
