package room

import (
	"maps"
	"sync/atomic"

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
	// PLI back upstream; watched (if set) is told when the track gains its first
	// subscriber or loses its last one, so the relay can pause it upstream. It
	// returns the track's SID in this room.
	PublishTrack(info *tesserav1.TrackInfo, codec webrtc.RTPCodecParameters, audioLevelExtID uint8,
		src rtc.RTPSource, writeRTCP rtc.FeedbackWriter, watched func(bool)) (string, error)
	UnpublishTrack(sid string)
	// SetInfo mirrors a change of the presenter's name, metadata or attributes upstream
	// (e.g. the playlist URL of their live recording); unchanged info is not broadcast.
	SetInfo(name, metadata string, attributes map[string]string)
	// SetTrackMuted mirrors a mute change of a relayed track (sid in this room).
	SetTrackMuted(sid string, muted bool)
	// SetQuality takes the presenter's connection quality as measured on the media node
	// (this node only sees the relay's connection).
	SetQuality(q tesserav1.ConnectionQuality, score float32)
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
	p.state, p.pulled = tesserav1.ParticipantInfo_STATE_ACTIVE, true
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
	src rtc.RTPSource, writeRTCP rtc.FeedbackWriter, watched func(bool)) (string, error) {
	p := rp.p
	m := p.room.m
	ti := proto.Clone(info).(*tesserav1.TrackInfo)
	ti.Sid = newSID("TR_")
	ti.Muted = info.GetMuted() // a presenter who joined muted stays muted here
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
	if watched != nil {
		// The callback runs under the receiver's lock: record the latest count and let
		// one goroutine report it, so reports never arrive out of order.
		var count atomic.Int32
		kick := make(chan struct{}, 1)
		closed := make(chan struct{})
		recv.OnDownTracksChanged(func(n int) {
			count.Store(int32(n))
			select {
			case kick <- struct{}{}:
			default:
			}
		})
		prev := recv.OnClosedHook()
		recv.OnClosed(func() { close(closed); prev() })
		go func() {
			last := false
			for {
				select {
				case <-closed:
					return
				case <-kick:
					if w := count.Load() > 0; w != last {
						last = w
						watched(w)
					}
				}
			}
		}()
	}
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

// SetTrackMuted implements RemotePublisher.
func (rp *remotePublisher) SetTrackMuted(sid string, muted bool) {
	rp.p.mu.Lock()
	t := rp.p.tracks[sid]
	same := t == nil || t.info.GetMuted() == muted
	rp.p.mu.Unlock()
	if same {
		return
	}
	if _, err := rp.p.setTrackMuted(sid, muted); err == nil {
		rp.p.room.broadcastParticipant(rp.p)
	}
}

// SetQuality implements RemotePublisher.
func (rp *remotePublisher) SetQuality(q tesserav1.ConnectionQuality, score float32) {
	s := stats.QualityUnknown
	switch q {
	case tesserav1.ConnectionQuality_CONNECTION_QUALITY_EXCELLENT:
		s = stats.QualityExcellent
	case tesserav1.ConnectionQuality_CONNECTION_QUALITY_GOOD:
		s = stats.QualityGood
	case tesserav1.ConnectionQuality_CONNECTION_QUALITY_POOR:
		s = stats.QualityPoor
	case tesserav1.ConnectionQuality_CONNECTION_QUALITY_LOST:
		s = stats.QualityLost
	}
	rp.p.mu.Lock()
	rp.p.upQuality, rp.p.upScore = s, float64(score)
	rp.p.mu.Unlock()
}

// SetInfo implements RemotePublisher.
func (rp *remotePublisher) SetInfo(name, metadata string, attributes map[string]string) {
	p := rp.p
	p.mu.Lock()
	changed := p.name != name || p.metadata != metadata || !maps.Equal(p.attributes, attributes)
	if changed {
		p.name, p.metadata, p.attributes = name, metadata, maps.Clone(attributes)
	}
	p.mu.Unlock()
	if changed {
		p.room.broadcastParticipant(p)
	}
}

func (rp *remotePublisher) Close() {
	rp.p.room.removeParticipant(rp.p, tesserav1.DisconnectReason_DISCONNECT_REASON_CLIENT_INITIATED, false)
}
