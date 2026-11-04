// Package client is a Go WebRTC client for Tessera built on Pion. It is used
// by end-to-end tests (WP1), the recorder (WP4) and the load tester (WP5),
// so its API is a cross-work-package contract: extend additively only.
//
// Ownership: WP1.
package client

import (
	"context"
	"log/slog"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
)

// Options configure a Client.
type Options struct {
	AutoSubscribe bool
	// SDK identifies the client kind in ClientInfo (go, recorder, loadtest).
	SDK tesserav1.ClientInfo_SDK
	// SettingEngine customises Pion (e.g. loopback-only ICE in tests).
	SettingEngine *webrtc.SettingEngine
	// Codecs restricts codecs registered in the media engine (nil = all supported).
	Codecs []webrtc.RTPCodecParameters
	Logger *slog.Logger
	// Interceptors are registered before Pion's defaults (closest to the
	// network), e.g. packet-drop shims in tests.
	Interceptors []interceptor.Factory
	// OnPublisherRTCP receives RTCP arriving for published tracks (PLI,
	// NACK, RR from the server).
	OnPublisherRTCP func(pkts []rtcp.Packet)
	// ForceRelay uses only TURN relay candidates.
	ForceRelay bool
	// DisableResume makes a dropped WebSocket end the session instead of
	// resuming it.
	DisableResume bool
}

// PublishOptions describe a published track.
type PublishOptions struct {
	Name       string
	Source     tesserav1.TrackSource
	Width      uint32
	Height     uint32
	DisableDTX bool
	Stereo     bool
	RED        bool
}

// Callbacks receive client events. They run on client goroutines and must not block.
type Callbacks struct {
	OnTrackSubscribed   func(t *RemoteTrack)
	OnTrackUnsubscribed func(trackSID string)
	OnParticipantUpdate func(p []*tesserav1.ParticipantInfo)
	OnSpeakersChanged   func(s []*tesserav1.SpeakerInfo)
	OnStreamStateUpdate func(u *tesserav1.StreamStateUpdate)
	OnSubscribedQuality func(u *tesserav1.SubscribedQualityUpdate)
	OnConnectionQuality func(u *tesserav1.ConnectionQualityUpdate)
	OnDataPacket        func(p *tesserav1.DataPacket)
	OnReconnecting      func()
	OnReconnected       func()
	OnDisconnected      func(reason tesserav1.DisconnectReason)
}

// Client is a connected Tessera participant.
type Client interface {
	// Connect joins the room; url is the server base URL (ws:// or wss://).
	Connect(ctx context.Context, url, token string) error
	Room() *tesserav1.Room
	LocalParticipant() *tesserav1.ParticipantInfo
	RemoteParticipants() []*tesserav1.ParticipantInfo

	// PublishTrack publishes a single-layer track.
	PublishTrack(ctx context.Context, track webrtc.TrackLocal, opts PublishOptions) (*tesserav1.TrackInfo, error)
	// PublishSimulcastTrack publishes one track with layers (each TrackLocal has RID q, h or f).
	PublishSimulcastTrack(ctx context.Context, layers []webrtc.TrackLocal, opts PublishOptions) (*tesserav1.TrackInfo, error)
	UnpublishTrack(ctx context.Context, trackSID string) error
	SetTrackMuted(ctx context.Context, trackSID string, muted bool) error

	Subscribe(ctx context.Context, trackSIDs ...string) error
	Unsubscribe(ctx context.Context, trackSIDs ...string) error
	UpdateTrackSettings(ctx context.Context, s *tesserav1.UpdateTrackSettings) error
	PublishData(ctx context.Context, p *tesserav1.DataPacket) error

	SetCallbacks(cb Callbacks)
	// SignalRTT is the last measured ping round-trip time in milliseconds.
	SignalRTT() int64
	Disconnect(ctx context.Context) error
}
