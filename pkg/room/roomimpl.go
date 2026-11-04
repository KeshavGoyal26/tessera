package room

import (
	"sync"
	"time"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/auth"
	"github.com/KeshavGoyal26/tessera/pkg/errs"
	"github.com/KeshavGoyal26/tessera/pkg/rtc/speaker"
	"github.com/KeshavGoyal26/tessera/pkg/signal"
	"github.com/KeshavGoyal26/tessera/pkg/stats"
)

// Room is one room with its participants.
type Room struct {
	m            *manager
	mu           sync.RWMutex
	info         *tesserav1.Room
	participants map[string]*Participant // by identity
	closed       bool
	emptyTimer   *time.Timer
	created      time.Time

	spkMu    sync.Mutex
	speakers speaker.Observer
	quality  map[string]tesserav1.ConnectionQuality
	done     chan struct{}
}

// SpeakerInterval and QualityInterval drive the room worker.
var (
	SpeakerInterval = 300 * time.Millisecond
	QualityInterval = 5 * time.Second
)

func newRoom(m *manager, info *tesserav1.Room) *Room {
	r := &Room{m: m, info: info, participants: map[string]*Participant{}, created: time.Now(),
		speakers: speaker.NewObserver(speaker.Config{}), quality: map[string]tesserav1.ConnectionQuality{}, done: make(chan struct{})}
	go r.worker()
	return r
}

// worker runs speaker detection and connection quality (ARCHITECTURE.md 4.4).
func (r *Room) worker() {
	spk := time.NewTicker(SpeakerInterval)
	cq := time.NewTicker(QualityInterval)
	defer spk.Stop()
	defer cq.Stop()
	for {
		select {
		case <-r.done:
			return
		case now := <-spk.C:
			r.spkMu.Lock()
			changed := r.speakers.Tick(now)
			r.spkMu.Unlock()
			if len(changed) > 0 {
				r.broadcast(&tesserav1.SignalResponse{Message: &tesserav1.SignalResponse_SpeakersChanged{SpeakersChanged: &tesserav1.SpeakersChanged{Speakers: changed}}}, nil)
			}
		case now := <-cq.C:
			r.connectionQuality(now)
		}
	}
}

func (r *Room) connectionQuality(now time.Time) {
	upd := &tesserav1.ConnectionQualityUpdate{}
	seen := map[string]bool{}
	for _, p := range r.Participants() {
		if p.permission().GetHidden() {
			continue
		}
		ps := p.stats(now)
		seen[p.sid] = true
		q := protoQuality(ps.ConnectionQuality)
		if r.quality[p.sid] != q {
			r.quality[p.sid] = q
			upd.Updates = append(upd.Updates, &tesserav1.ConnectionQualityInfo{ParticipantSid: p.sid, Quality: q, Score: float32(ps.QualityScore)})
		}
	}
	for sid := range r.quality {
		if !seen[sid] {
			delete(r.quality, sid)
		}
	}
	if len(upd.Updates) > 0 {
		r.broadcast(&tesserav1.SignalResponse{Message: &tesserav1.SignalResponse_ConnectionQuality{ConnectionQuality: upd}}, nil)
	}
}

func protoQuality(q string) tesserav1.ConnectionQuality {
	switch q {
	case stats.QualityExcellent:
		return tesserav1.ConnectionQuality_CONNECTION_QUALITY_EXCELLENT
	case stats.QualityGood:
		return tesserav1.ConnectionQuality_CONNECTION_QUALITY_GOOD
	case stats.QualityPoor:
		return tesserav1.ConnectionQuality_CONNECTION_QUALITY_POOR
	case stats.QualityLost:
		return tesserav1.ConnectionQuality_CONNECTION_QUALITY_LOST
	}
	return tesserav1.ConnectionQuality_CONNECTION_QUALITY_UNSPECIFIED
}

// activeSpeakers returns the current speakers for stats.
func (r *Room) activeSpeakers() []*tesserav1.SpeakerInfo {
	r.spkMu.Lock()
	defer r.spkMu.Unlock()
	return r.speakers.Speakers()
}

func (r *Room) addSpeakerSource(p *Participant, t *pubTrack) {
	if t.info.GetType() != tesserav1.TrackType_TRACK_TYPE_AUDIO {
		return
	}
	r.spkMu.Lock()
	r.speakers.AddTrack(p.sid, t.info.GetSid(), t.recv)
	r.spkMu.Unlock()
}

func (r *Room) removeSpeakerSource(trackSID string) {
	r.spkMu.Lock()
	r.speakers.RemoveTrack(trackSID)
	r.spkMu.Unlock()
}

// Name returns the room name.
func (r *Room) Name() string { return r.info.Name }

// ToProto returns a copy of the room info with current counts.
func (r *Room) ToProto() *tesserav1.Room {
	r.mu.RLock()
	defer r.mu.RUnlock()
	info := &tesserav1.Room{
		Sid: r.info.Sid, Name: r.info.Name, EmptyTimeoutSeconds: r.info.EmptyTimeoutSeconds,
		DepartureTimeoutSeconds: r.info.DepartureTimeoutSeconds, MaxParticipants: r.info.MaxParticipants,
		CreationTimeMs: r.info.CreationTimeMs, Metadata: r.info.Metadata, ActiveRecording: r.info.ActiveRecording,
		EnabledCodecs: r.info.EnabledCodecs, RecordingEnabled: r.info.RecordingEnabled,
	}
	for _, p := range r.participants {
		if p.permission().GetHidden() {
			continue
		}
		info.NumParticipants++
		if p.isPublisher() {
			info.NumPublishers++
		}
	}
	return info
}

func (r *Room) setMetadata(md string) {
	r.mu.Lock()
	r.info.Metadata = md
	r.mu.Unlock()
}

// Participants returns all participants (including hidden).
func (r *Room) Participants() []*Participant {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Participant, 0, len(r.participants))
	for _, p := range r.participants {
		out = append(out, p)
	}
	return out
}

func (r *Room) byIdentity(id string) *Participant {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.participants[id]
}

func (r *Room) join(claims *auth.Claims, cp signal.ConnectParams, conn signal.Conn) (*Participant, error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, errs.New(errs.CodeUnavailable, "room closed")
	}
	old := r.participants[claims.Identity()]
	if max := r.info.MaxParticipants; max > 0 && old == nil && uint32(len(r.participants)) >= max {
		r.mu.Unlock()
		return nil, errs.New(errs.CodeResourceExhausted, "room full")
	}
	r.mu.Unlock()
	if old != nil {
		r.removeParticipant(old, tesserav1.DisconnectReason_DISCONNECT_REASON_DUPLICATE_IDENTITY, true)
	}
	p := newParticipant(r, claims, cp, conn)
	r.mu.Lock()
	if r.emptyTimer != nil {
		r.emptyTimer.Stop()
		r.emptyTimer = nil
	}
	r.participants[p.identity] = p
	r.mu.Unlock()
	r.m.mu.Lock()
	r.m.bySID[p.sid] = p
	r.m.mu.Unlock()
	if err := p.start(); err != nil {
		r.removeParticipant(p, tesserav1.DisconnectReason_DISCONNECT_REASON_JOIN_FAILURE, false)
		return nil, err
	}
	stats.ParticipantJoins.Inc()
	go r.m.notify(tesserav1.WebhookEventType_WEBHOOK_EVENT_TYPE_PARTICIPANT_JOINED, r, p, nil)
	r.broadcastParticipant(p)
	// Subscribe the newcomer to existing tracks.
	if p.autoSubscribe {
		for _, other := range r.Participants() {
			if other == p {
				continue
			}
			for _, t := range other.publishedTracks() {
				p.subscribe(other, t)
			}
		}
	}
	return p, nil
}

// removeParticipant closes p and tells the others.
func (r *Room) removeParticipant(p *Participant, reason tesserav1.DisconnectReason, sendLeave bool) {
	r.mu.Lock()
	if r.participants[p.identity] == p {
		delete(r.participants, p.identity)
	}
	empty := len(r.participants) == 0
	r.mu.Unlock()
	r.m.mu.Lock()
	if r.m.bySID[p.sid] == p {
		delete(r.m.bySID, p.sid)
	}
	r.m.mu.Unlock()
	if !p.close(reason, sendLeave) {
		return
	}
	// Unsubscribe everybody from p's tracks.
	for _, other := range r.Participants() {
		for _, t := range p.publishedTracks() {
			other.unsubscribe(t.info.GetSid())
		}
	}
	r.broadcastParticipant(p)
	go r.m.notify(tesserav1.WebhookEventType_WEBHOOK_EVENT_TYPE_PARTICIPANT_LEFT, r, p, nil)
	if empty {
		r.armDepartureTimer()
	}
}

func (r *Room) armDepartureTimer() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || len(r.participants) > 0 {
		return
	}
	d := time.Duration(r.info.DepartureTimeoutSeconds) * time.Second
	if r.emptyTimer != nil {
		r.emptyTimer.Stop()
	}
	r.emptyTimer = time.AfterFunc(d, r.closeIfEmpty)
}

func (r *Room) armEmptyTimer() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || len(r.participants) > 0 || r.emptyTimer != nil {
		return
	}
	r.emptyTimer = time.AfterFunc(time.Duration(r.info.EmptyTimeoutSeconds)*time.Second, r.closeIfEmpty)
}

func (r *Room) closeIfEmpty() {
	r.mu.RLock()
	n := len(r.participants)
	r.mu.RUnlock()
	if n == 0 {
		r.close(tesserav1.DisconnectReason_DISCONNECT_REASON_ROOM_DELETED)
	}
}

func (r *Room) close(reason tesserav1.DisconnectReason) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	close(r.done)
	if r.emptyTimer != nil {
		r.emptyTimer.Stop()
	}
	r.mu.Unlock()
	for _, p := range r.Participants() {
		r.removeParticipant(p, reason, true)
	}
	r.m.removeRoom(r)
	go r.m.notify(tesserav1.WebhookEventType_WEBHOOK_EVENT_TYPE_ROOM_FINISHED, r, nil, nil)
}

// broadcast sends msg to every participant except `except`.
func (r *Room) broadcast(msg *tesserav1.SignalResponse, except *Participant) {
	for _, p := range r.Participants() {
		if p != except {
			p.send(msg)
		}
	}
}

// broadcastParticipant sends a ParticipantUpdate about p to the others
// (hidden participants are never announced).
func (r *Room) broadcastParticipant(p *Participant) {
	if p.permission().GetHidden() {
		return
	}
	info := p.ToProto()
	r.broadcast(&tesserav1.SignalResponse{Message: &tesserav1.SignalResponse_Update{
		Update: &tesserav1.ParticipantUpdate{Participants: []*tesserav1.ParticipantInfo{info}}}}, p)
}

// onTrackPublished subscribes interested participants to a new track.
func (r *Room) onTrackPublished(pub *Participant, t *pubTrack) {
	r.addSpeakerSource(pub, t)
	r.broadcastParticipant(pub)
	for _, p := range r.Participants() {
		if p != pub && p.wantsTrack(t.info.GetSid()) {
			p.subscribe(pub, t)
		}
	}
}

func (r *Room) findTrack(sid string) (*Participant, *pubTrack) {
	for _, p := range r.Participants() {
		if t := p.getTrack(sid); t != nil {
			return p, t
		}
	}
	return nil, nil
}
