// Package rtc is the SFU engine: PeerConnection transports, Receivers
// (published tracks) and DownTracks (subscriptions), built on Pion's
// low-level transports only (ADR-0001). Sub-packages: buffer, simulcast,
// bwe, speaker, turn, types.
//
// Ownership: WP1. Consumers: pkg/room (all interfaces here) and, for tests,
// pkg/client.
package rtc

import (
	"log/slog"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/rtc/buffer"
	"github.com/KeshavGoyal26/tessera/pkg/rtc/types"
)

// Engine owns the shared Pion API (SettingEngine with UDP/TCP mux, NAT 1:1,
// media engine with codecs and header extensions, interceptors) and the
// embedded TURN server.
type Engine interface {
	// NewTransport creates one PeerConnection (publisher or subscriber side).
	NewTransport(p TransportParams) (Transport, error)
	// ICEServers returns STUN/TURN servers (with per-session TURN credentials).
	ICEServers(participantSID string) []*tesserav1.ICEServer
	// Ready reports whether listeners are bound (used by /readyz).
	Ready() error
	Close() error
}

// TransportParams configures a Transport.
type TransportParams struct {
	ParticipantSID string
	Target         tesserav1.SignalTarget
	// EnabledCodecs restricts negotiated codecs, in preference order.
	EnabledCodecs []*tesserav1.Codec
	Handler       TransportHandler
	Logger        *slog.Logger
}

// TransportHandler receives Transport events. Callbacks run on Pion
// goroutines and must not block.
type TransportHandler interface {
	OnICECandidate(c *webrtc.ICECandidateInit, target tesserav1.SignalTarget)
	// OnOffer is called with a server-generated offer (subscriber side).
	OnOffer(sd *tesserav1.SessionDescription)
	// OnTrack is called for each incoming track or simulcast layer (publisher side).
	OnTrack(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver)
	OnDataPacket(kind tesserav1.DataPacket_Kind, data []byte)
	OnConnectionStateChange(state webrtc.PeerConnectionState)
	// OnDataChannel is called for data channels opened by the client
	// (publisher side).
	OnDataChannel(dc *webrtc.DataChannel)
}

// TransportStats are connection-level statistics.
type TransportStats struct {
	CandidateType string
	ICESetup      time.Duration
	DTLSSetup     time.Duration
	RTT           time.Duration
	Connected     bool
}

// Transport wraps one webrtc.PeerConnection.
type Transport interface {
	// HandleRemoteDescription applies an offer (publisher) or answer (subscriber)
	// and, for offers, returns the answer.
	HandleRemoteDescription(sd *tesserav1.SessionDescription) (*tesserav1.SessionDescription, error)
	AddICECandidate(c webrtc.ICECandidateInit) error
	// Negotiate schedules a (debounced, serialized) offer on the subscriber side.
	Negotiate(iceRestart bool)
	AddDownTrack(dt DownTrack) error
	RemoveDownTrack(dt DownTrack) error
	SendDataPacket(kind tesserav1.DataPacket_Kind, data []byte) error
	// WriteRTCP sends RTCP on this PeerConnection.
	WriteRTCP(pkts []rtcp.Packet) error
	// Stats returns connection statistics.
	Stats() TransportStats
	// PeerConnection exposes the underlying Pion PeerConnection.
	PeerConnection() *webrtc.PeerConnection
	Close() error
}

// RTPSource is one incoming RTP stream (one simulcast layer). Today it is a
// *webrtc.TrackRemote; later a relay from another node (ARCHITECTURE.md 8).
type RTPSource interface {
	// Read reads one RTP packet into b (no allocation).
	Read(b []byte) (n int, err error)
	SSRC() uint32
	RID() string
}

// Receiver is one published track with up to three simulcast layers.
type Receiver interface {
	TrackID() string
	StreamID() string
	Kind() webrtc.RTPCodecType
	Codec() webrtc.RTPCodecParameters
	// AddLayer attaches a layer source and starts its forwarding goroutine.
	AddLayer(spatial int32, src RTPSource, buf *buffer.Buffer) error
	AddDownTrack(dt DownTrack) error
	RemoveDownTrack(subscriberID string)
	// SendPLI requests a keyframe on a layer, throttled unless force.
	SendPLI(spatial int32, force bool)
	// ReadPacket copies original packet sn of a layer into dst (NACK responder).
	ReadPacket(dst []byte, spatial int32, sn uint16) (int, error)
	// Bitrates returns measured layer bitrates.
	Bitrates() types.Bitrates
	// SetMaxSubscribedLayer is fed by dynacast aggregation.
	OnMaxSubscribedLayerChanged(f func(spatial int32))
	Close()
}

// DownTrack is one subscriber's view of a Receiver. It implements
// webrtc.TrackLocal and writes directly to the bound TrackLocalWriter.
type DownTrack interface {
	webrtc.TrackLocal
	SubscriberID() string
	// WriteRTP forwards one packet from spatial layer `layer`. Hot path:
	// no allocation, no blocking except the socket write.
	WriteRTP(pkt *buffer.ExtPacket, layer int32) error
	// SetMaxLayer applies subscriber preferences.
	SetMaxLayer(l types.VideoLayer)
	// SetTargetLayer applies the allocator decision.
	SetTargetLayer(l types.VideoLayer)
	CurrentLayer() types.VideoLayer
	Mute(muted bool)
	// HandleRTCP processes NACK/PLI/FIR/RR from the subscriber.
	HandleRTCP(pkts []rtcp.Packet)
	Close()
}
