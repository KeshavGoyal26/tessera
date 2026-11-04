// Package client is a Go WebRTC client for Tessera built on Pion. It is used
// by end-to-end tests (WP1), the recorder (WP4) and the load tester (WP5),
// so its API is a cross-work-package contract: extend additively only.
//
// Ownership: WP1.
package client

import (
	"context"
	"log/slog"

	"github.com/pion/webrtc/v4"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/errs"
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

// RemoteTrack is a subscribed track. It satisfies recorder.RTPSource.
type RemoteTrack struct {
	SID                 string
	ParticipantIdentity string
	Info                *tesserav1.TrackInfo
	track               *webrtc.TrackRemote
}

// Read reads one RTP packet into b.
func (t *RemoteTrack) Read(b []byte) (int, error) {
	if t.track == nil {
		return 0, errs.NotImplemented("client.RemoteTrack.Read")
	}
	n, _, err := t.track.Read(b)
	return n, err
}

// Codec returns the negotiated codec.
func (t *RemoteTrack) Codec() webrtc.RTPCodecParameters {
	if t.track == nil {
		return webrtc.RTPCodecParameters{}
	}
	return t.track.Codec()
}

// RequestKeyFrame sends a PLI for this track.
func (t *RemoteTrack) RequestKeyFrame() error {
	return errs.NotImplemented("client.RemoteTrack.RequestKeyFrame")
}

// TrackRemote exposes the underlying Pion track.
func (t *RemoteTrack) TrackRemote() *webrtc.TrackRemote { return t.track }

// New creates a client. Phase 0 stub: Connect returns errs.ErrUnimplemented.
func New(opts Options) Client { return &stubClient{} }

type stubClient struct{}

var errNI = errs.NotImplemented("client")

func (*stubClient) Connect(context.Context, string, string) error    { return errNI }
func (*stubClient) Room() *tesserav1.Room                            { return nil }
func (*stubClient) LocalParticipant() *tesserav1.ParticipantInfo     { return nil }
func (*stubClient) RemoteParticipants() []*tesserav1.ParticipantInfo { return nil }
func (*stubClient) PublishTrack(context.Context, webrtc.TrackLocal, PublishOptions) (*tesserav1.TrackInfo, error) {
	return nil, errNI
}
func (*stubClient) PublishSimulcastTrack(context.Context, []webrtc.TrackLocal, PublishOptions) (*tesserav1.TrackInfo, error) {
	return nil, errNI
}
func (*stubClient) UnpublishTrack(context.Context, string) error      { return errNI }
func (*stubClient) SetTrackMuted(context.Context, string, bool) error { return errNI }
func (*stubClient) Subscribe(context.Context, ...string) error        { return errNI }
func (*stubClient) Unsubscribe(context.Context, ...string) error      { return errNI }
func (*stubClient) UpdateTrackSettings(context.Context, *tesserav1.UpdateTrackSettings) error {
	return errNI
}
func (*stubClient) PublishData(context.Context, *tesserav1.DataPacket) error { return errNI }
func (*stubClient) SetCallbacks(Callbacks)                                   {}
func (*stubClient) SignalRTT() int64                                         { return 0 }
func (*stubClient) Disconnect(context.Context) error                         { return nil }
