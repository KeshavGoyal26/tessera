// Package rtc is the SFU engine: PeerConnection transports, Receivers
// (published tracks) and DownTracks (subscriptions), built on Pion's
// low-level transports only (ADR-0001). Sub-packages: buffer, forward, bwe,
// speaker, turn. Publishers send one stream per track (no simulcast, ADR-0012).
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
)

// Engine owns the shared Pion API (SettingEngine with UDP/TCP mux, NAT 1:1,
// media engine with codecs and header extensions, interceptors) and the
// embedded TURN server.
type Engine interface {
	// NewTransport creates one PeerConnection (publisher or subscriber side).
	NewTransport(p TransportParams) (Transport, error)
	// ICEServers returns STUN/TURN servers (with per-session TURN credentials). When
	// prefer names some of the configured relay TURN URLs (rtc.turn_relay), only those
	// are handed out (an edge's own TURN, ADR-0012); otherwise all of them.
	ICEServers(participantSID string, prefer []string) []*tesserav1.ICEServer
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
	// OnTrack is called for each incoming track (publisher side).
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
	// CreateDataChannels creates the `_reliable`/`_lossy` channels
	// (subscriber side).
	CreateDataChannels() error
	// WriteRTCP sends RTCP on this PeerConnection.
	WriteRTCP(pkts []rtcp.Packet) error
	// Stats returns connection statistics.
	Stats() TransportStats
	// PeerConnection exposes the underlying Pion PeerConnection.
	PeerConnection() *webrtc.PeerConnection
	Close() error
}

// RTPSource is a track's incoming RTP stream. Today it is a *webrtc.TrackRemote;
// later a relay from another node (ADR-0012).
type RTPSource interface {
	// Read reads one RTP packet into b (no allocation).
	Read(b []byte) (n int, err error)
	SSRC() uint32
}

// Receiver is one published track.
type Receiver interface {
	TrackID() string
	StreamID() string
	Kind() webrtc.RTPCodecType
	Codec() webrtc.RTPCodecParameters
	// Start attaches the track's stream and starts its forwarding goroutine.
	Start(src RTPSource, buf *buffer.Buffer) error
	AddDownTrack(dt DownTrack) error
	RemoveDownTrack(subscriberID string)
	// SendPLI requests a keyframe, throttled unless force.
	SendPLI(force bool)
	// ReadPacket copies original packet sn into dst (NACK responder).
	ReadPacket(dst []byte, sn uint16) (int, error)
	// Bitrate returns the measured incoming bitrate in bits per second.
	Bitrate() int64
	Close()
}

// DownTrack is one subscriber's view of a Receiver. It implements
// webrtc.TrackLocal and writes directly to the bound TrackLocalWriter.
type DownTrack interface {
	webrtc.TrackLocal
	SubscriberID() string
	// WriteRTP forwards one packet. Hot path: no allocation, no blocking
	// except the socket write.
	WriteRTP(pkt *buffer.ExtPacket) error
	// SetDisabled stops (true) or resumes forwarding at the subscriber's request.
	SetDisabled(disabled bool)
	Mute(muted bool)
	// HandleRTCP processes NACK/PLI/FIR/RR from the subscriber.
	HandleRTCP(pkts []rtcp.Packet)
	Close()
}
