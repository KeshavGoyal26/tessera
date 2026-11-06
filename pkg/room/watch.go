package room

import (
	"google.golang.org/protobuf/proto"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/errs"
	"github.com/KeshavGoyal26/tessera/pkg/rtc"
)

// PublishedTrack is a published track as seen by an in-process consumer (the recorder).
type PublishedTrack struct {
	Info     *tesserav1.TrackInfo
	Identity string // publishing participant
	Receiver *rtc.ReceiverImpl
}

// TrackWatcher reports a room's published tracks to an in-process consumer.
type TrackWatcher interface {
	// WatchTracks calls onTrack for every track published in the room, now and later,
	// until stop is called. A track may be reported twice (deduplicate by SID). done is
	// closed when the room closes. onTrack must not block.
	WatchTracks(room string, onTrack func(PublishedTrack)) (stop func(), done <-chan struct{}, err error)
}

var _ TrackWatcher = (*manager)(nil)

// WatchTracks implements TrackWatcher.
func (m *manager) WatchTracks(name string, onTrack func(PublishedTrack)) (func(), <-chan struct{}, error) {
	r := m.getRoom(name)
	if r == nil {
		return nil, nil, errs.New(errs.CodeNotFound, "room %q not found", name)
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, nil, errs.New(errs.CodeNotFound, "room %q closed", name)
	}
	r.watchSeq++
	id := r.watchSeq
	if r.watchers == nil {
		r.watchers = map[uint64]func(PublishedTrack){}
	}
	r.watchers[id] = onTrack
	r.mu.Unlock()
	// Registered first, then the existing tracks: a track published in between is
	// reported twice rather than missed.
	for _, p := range r.Participants() {
		for _, t := range p.publishedTracks() {
			onTrack(publishedTrack(p, t))
		}
	}
	stop := func() {
		r.mu.Lock()
		delete(r.watchers, id)
		r.mu.Unlock()
	}
	return stop, r.done, nil
}

func publishedTrack(p *Participant, t *pubTrack) PublishedTrack {
	return PublishedTrack{Info: proto.Clone(t.info).(*tesserav1.TrackInfo), Identity: p.identity, Receiver: t.recv}
}

// notifyWatchers reports a newly published track to in-process watchers.
func (r *Room) notifyWatchers(p *Participant, t *pubTrack) {
	r.mu.RLock()
	fns := make([]func(PublishedTrack), 0, len(r.watchers))
	for _, f := range r.watchers {
		fns = append(fns, f)
	}
	r.mu.RUnlock()
	for _, f := range fns {
		f(publishedTrack(p, t))
	}
}
