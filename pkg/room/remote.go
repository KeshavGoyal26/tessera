package room

import (
	"github.com/pion/webrtc/v4"
	"google.golang.org/protobuf/proto"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/auth"
	"github.com/KeshavGoyal26/tessera/pkg/errs"
	"github.com/KeshavGoyal26/tessera/pkg/rtc"
	"github.com/KeshavGoyal26/tessera/pkg/signal"
	"github.com/KeshavGoyal26/tessera/pkg/stats"
)

// RemotePublisher is a participant whose tracks arrive from another node: an edge
// relaying a presenter from a media server to its own viewers (ADR-0012). It has no
// signaling connection and no PeerConnections; the room's participants see it and
// subscribe to its tracks like any other.
type RemotePublisher interface {
	SID() string
	// PublishTrack adds a track fed by src; writeRTCP carries the receiver's NACK and
	// PLI back upstream. It returns the track's SID in this room.
	PublishTrack(info *tesserav1.TrackInfo, codec webrtc.RTPCodecParameters, audioLevelExtID uint8,
		src rtc.RTPSource, writeRTCP rtc.FeedbackWriter) (string, error)
	UnpublishTrack(sid string)
	// Close removes the participant from the room.
	Close()
}

// RemotePublishers lets a node host participants relayed from another node.
type RemotePublishers interface {
	// AddRemotePublisher joins identity into room (created if needed). A participant
	// with the same identity is replaced, as on a rejoin.
	AddRemotePublisher(room, identity, name, metadata string, attributes map[string]string) (RemotePublisher, error)
}

var _ RemotePublishers = (*manager)(nil)

type remotePublisher struct{ p *Participant }

// AddRemotePublisher implements RemotePublishers.
func (m *manager) AddRemotePublisher(roomName, identity, name, metadata string, attributes map[string]string) (RemotePublisher, error) {
	if identity == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "identity required")
	}
	r, err := m.getOrCreateRoom(roomName, nil, true)
	if err != nil {
		return nil, err
	}
	no := false
	claims := &auth.Claims{Subject: identity, Name: name, Metadata: metadata, Attributes: attributes,
		Grants: &auth.RoomGrant{Room: roomName, RoomJoin: true, CanSubscribe: &no, CanPublishData: &no}}
	r.joinMu.Lock()
	defer r.joinMu.Unlock()
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, errs.New(errs.CodeUnavailable, "room closed")
	}
	old := r.participants[identity]
	r.mu.Unlock()
	if old != nil {
		r.removeParticipant(old, tesserav1.DisconnectReason_DISCONNECT_REASON_DUPLICATE_IDENTITY, true)
	}
	p := newParticipant(r, claims, signal.ConnectParams{}, nil)
	p.state = tesserav1.ParticipantInfo_STATE_ACTIVE
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, errs.New(errs.CodeUnavailable, "room closed")
	}
	if r.emptyTimer != nil {
		r.emptyTimer.Stop()
		r.emptyTimer = nil
	}
	r.participants[p.identity] = p
	r.mu.Unlock()
	m.mu.Lock()
	m.bySID[p.sid] = p
	m.mu.Unlock()
	stats.ParticipantJoins.Inc()
	r.broadcastParticipant(p)
	return &remotePublisher{p: p}, nil
}

func (rp *remotePublisher) SID() string { return rp.p.sid }

func (rp *remotePublisher) PublishTrack(info *tesserav1.TrackInfo, codec webrtc.RTPCodecParameters, audioLevelExtID uint8,
	src rtc.RTPSource, writeRTCP rtc.FeedbackWriter) (string, error) {
	p := rp.p
	m := p.room.m
	ti := proto.Clone(info).(*tesserav1.TrackInfo)
	ti.Sid = newSID("TR_")
	ti.Muted = false
	ti.MimeType = codec.MimeType
	kind := webrtc.RTPCodecTypeVideo
	if ti.GetType() == tesserav1.TrackType_TRACK_TYPE_AUDIO {
		kind = webrtc.RTPCodecTypeAudio
	}
	t := &pubTrack{cid: ti.Sid, info: ti}
	t.recv = rtc.NewReceiver(rtc.ReceiverParams{TrackID: ti.Sid, StreamID: p.sid, Kind: kind, Codec: codec,
		AudioLevelExtID: audioLevelExtID, WriteRTCP: writeRTCP, PLIThrottle: m.cfg.RTC.PLIThrottle, Logger: m.logger})
	recv, sid := t.recv, ti.Sid
	recv.OnClosed(func() { p.onReceiverClosed(sid, recv) })
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return "", errs.New(errs.CodeUnavailable, "participant left")
	}
	p.tracks[sid] = t
	p.mu.Unlock()
	if err := recv.Start(src, nil); err != nil {
		recv.Close()
		return "", err
	}
	stats.TrackPublishes.Inc()
	p.room.onTrackPublished(p, t)
	return sid, nil
}

func (rp *remotePublisher) UnpublishTrack(sid string) { rp.p.unpublish(sid) }

func (rp *remotePublisher) Close() {
	rp.p.room.removeParticipant(rp.p, tesserav1.DisconnectReason_DISCONNECT_REASON_CLIENT_INITIATED, false)
}
