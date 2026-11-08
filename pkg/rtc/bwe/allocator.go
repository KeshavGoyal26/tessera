package bwe

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/KeshavGoyal26/tessera/pkg/stats"
)

// allocTick is the allocator period.
const allocTick = 500 * time.Millisecond

// Probing: when a subscriber wants a video track the estimate cannot carry, the
// allocator sends padding for probeDuration so the path's capacity can be
// measured (see estimator probe clusters). Failed probes back off exponentially.
const (
	probeDuration   = 500 * time.Millisecond
	probeMinBackoff = 2 * time.Second
	probeMaxBackoff = 30 * time.Second
	probeTimeout    = probeDuration + 2*time.Second
)

// Trial resume: when every wanted video track is paused, nothing forwards to carry
// probe padding (and without audio, no feedback arrives at all), so the estimate
// could never rise again. After a back-off the first paused track is resumed on
// trial: its own traffic measures the path. The trial goes on while the path shows
// no congestion (no overuse, the estimate not falling) and succeeds once the
// estimate covers the track; congestion, or trialHold without success, pauses it
// again and doubles the back-off.
const (
	// trialGrace: a resumed video starts with a keyframe burst that reads as overuse;
	// the trial is judged only after it.
	trialGrace      = 2 * time.Second
	trialHold       = 30 * time.Second
	trialMinBackoff = 5 * time.Second
	trialMaxBackoff = 60 * time.Second
)

type trackState struct {
	t      AllocatableTrack
	set    bool // a decision was applied
	paused bool // paused by the allocator (applied)
}

// allocator is the per-subscriber StreamAllocator. Publishers send one stream
// per track (ADR-0012), so each video track is either forwarded or paused:
// tracks are taken in priority order while the estimate covers their bitrate.
type allocator struct {
	cfg Config
	est Estimator

	mu       sync.Mutex
	tracks   map[string]*trackState
	onPaused func([]Allocation)
	stop     chan struct{}
	once     sync.Once
	now      func() time.Time

	probing    bool
	probeStart time.Time
	nextProbe  time.Time
	backoff    time.Duration
	probeNeed  int64 // total rate the running probe must prove

	trialID      string    // track resumed on trial ("" = none)
	trialUntil   time.Time // end of the trial
	trialStart   time.Time
	trialEst     int64 // estimate at the end of the grace (0 before)
	trialBackoff time.Duration
	stalledSince time.Time // since when every wanted video track is paused

	started   atomic.Uint64 // probes started
	succeeded atomic.Uint64 // probes that raised the estimate enough
}

// NewAllocator creates a StreamAllocator.
func NewAllocator(cfg Config, est Estimator) Allocator {
	return &allocator{cfg: cfg, est: est, tracks: map[string]*trackState{}, stop: make(chan struct{}), now: time.Now}
}

func (a *allocator) AddTrack(t AllocatableTrack) {
	a.mu.Lock()
	a.tracks[t.ID()] = &trackState{t: t}
	a.mu.Unlock()
}

func (a *allocator) RemoveTrack(id string) {
	a.mu.Lock()
	delete(a.tracks, id)
	a.mu.Unlock()
}

// OnTrackChanged is a no-op: Allocate reads preferences on every tick.
func (a *allocator) OnTrackChanged(string) {}

func (a *allocator) OnPausedChanged(f func([]Allocation)) {
	a.mu.Lock()
	a.onPaused = f
	a.mu.Unlock()
}

func (a *allocator) Start() {
	if !a.cfg.Enabled {
		return
	}
	go func() {
		t := time.NewTicker(allocTick)
		defer t.Stop()
		for {
			select {
			case <-a.stop:
				return
			case <-t.C:
				a.Allocate()
			}
		}
	}()
}

func (a *allocator) Stop() { a.once.Do(func() { close(a.stop) }) }

// feedbackCounter is implemented by estimators that know whether TWCC feedback
// has arrived (before it, the estimate is only the configured start value).
type feedbackCounter interface{ Feedbacks() uint64 }

// Allocate runs one allocation round and returns the decisions.
func (a *allocator) Allocate() []Allocation {
	if fc, ok := a.est.(feedbackCounter); ok && fc.Feedbacks() == 0 {
		return nil // no bandwidth information yet: do not constrain
	}
	now := a.now()
	budget := float64(a.est.Estimate()) * 0.95
	a.mu.Lock()
	states := make([]*trackState, 0, len(a.tracks))
	for _, s := range a.tracks {
		states = append(states, s)
	}
	cb := a.onPaused
	a.mu.Unlock()

	var videos []*trackState
	for _, s := range states {
		if !s.t.IsVideo() {
			budget -= float64(s.t.Bitrate()) // audio is never paused
			continue
		}
		videos = append(videos, s)
	}
	sort.Slice(videos, func(i, j int) bool {
		if videos[i].t.Priority() != videos[j].t.Priority() {
			return videos[i].t.Priority() > videos[j].t.Priority()
		}
		return videos[i].t.ID() < videos[j].t.ID()
	})
	on := make([]bool, len(videos))
	for i, s := range videos {
		if !s.t.Wanted() {
			continue // the subscriber disabled it: no bandwidth, not "paused"
		}
		r := float64(s.t.Bitrate())
		if r <= budget || !a.cfg.AllowPause {
			on[i] = true
			budget -= r
		}
	}
	if a.cfg.AllowPause {
		a.trialResume(now, videos, on)
	}
	var out, changed []Allocation
	for i, s := range videos {
		paused := s.t.Wanted() && !on[i]
		if !s.set || paused != s.paused {
			s.t.SetAllocPaused(paused)
			if s.set || paused {
				changed = append(changed, Allocation{TrackID: s.t.ID(), Paused: paused})
			}
			s.set, s.paused = true, paused
		}
		al := Allocation{TrackID: s.t.ID(), Paused: paused}
		if on[i] {
			al.Bitrate = s.t.Bitrate()
		}
		out = append(out, al)
	}
	if len(changed) > 0 && cb != nil {
		cb(changed)
	}
	a.maybeProbe(now, states, videos, on)
	return out
}

// trialResume runs the trial resume (see trialHold) and may force on[i].
func (a *allocator) trialResume(now time.Time, videos []*trackState, on []bool) {
	idx := func(id string) int {
		for i, s := range videos {
			if s.t.ID() == id {
				return i
			}
		}
		return -1
	}
	if a.trialID != "" {
		i := idx(a.trialID)
		switch {
		case i >= 0 && on[i]: // the estimate covers it now: success
			a.trialID, a.trialBackoff, a.stalledSince = "", 0, time.Time{}
		case i >= 0 && videos[i].t.Wanted() && now.Sub(a.trialStart) < trialGrace:
			on[i] = true // the keyframe burst: not judged yet
			return
		case i >= 0 && videos[i].t.Wanted() && now.Before(a.trialUntil) &&
			a.est.State() != StateOveruse && a.est.Estimate() >= a.trialBaseline()*9/10:
			on[i] = true // no congestion: let the estimate grow with the track's traffic
			return
		default: // failed (or the track went away)
			a.trialID = ""
			a.trialBackoff = min(2*max(a.trialBackoff, trialMinBackoff), trialMaxBackoff)
			a.stalledSince = now
		}
	}
	first := -1
	for i, s := range videos {
		if on[i] {
			a.stalledSince = time.Time{} // something forwards: probing can work
			return
		}
		if first < 0 && s.t.Wanted() {
			first = i
		}
	}
	if first < 0 {
		a.stalledSince = time.Time{}
		return
	}
	if a.stalledSince.IsZero() {
		a.stalledSince = now
	}
	// No congestion check here: with nothing sent no feedback arrives, so the state is
	// whatever it was when the video paused (often overuse) and would block every trial.
	if now.Sub(a.stalledSince) >= max(a.trialBackoff, trialMinBackoff) {
		a.trialID, a.trialStart, a.trialUntil, a.trialEst = videos[first].t.ID(), now, now.Add(trialHold), 0
		on[first] = true
	}
}

// trialBaseline is the estimate the trial is judged against: taken once, after the grace.
func (a *allocator) trialBaseline() int64 {
	if a.trialEst == 0 {
		a.trialEst = a.est.Estimate()
	}
	return a.trialEst
}

// maybeProbe probes for the first paused track's bitrate when nothing is probing.
func (a *allocator) maybeProbe(now time.Time, all, videos []*trackState, on []bool) {
	p, ok := a.est.(Prober)
	if !ok || !a.cfg.Probe {
		return
	}
	if a.backoff == 0 {
		a.backoff = probeMinBackoff
	}
	if a.probing {
		done, res := p.ProbeResult()
		if !done && now.Sub(a.probeStart) < probeTimeout {
			return
		}
		a.probing = false
		// Success means the path carries the track the probe was for. A lower
		// result still informs the estimate, but probing again right away would
		// only keep a capped link congested and the track flapping.
		if done && res >= a.probeNeed {
			a.succeeded.Add(1)
			stats.ProbesSucceeded.Inc()
			a.backoff = probeMinBackoff
			a.nextProbe = now.Add(allocTick) // another paused track may need a step
		} else {
			a.backoff = min(a.backoff*2, probeMaxBackoff)
			a.nextProbe = now.Add(a.backoff)
		}
		return
	}
	if now.Before(a.nextProbe) || a.est.State() == StateOveruse {
		return
	}
	var step int64
	var pad Padder
	for i, s := range videos {
		if pd, ok := s.t.(Padder); ok && pad == nil && on[i] {
			pad = pd // any track that is forwarding can carry the padding
		}
		if step == 0 && s.t.Wanted() && !on[i] {
			step = s.t.Bitrate()
		}
	}
	if step <= 0 {
		a.backoff = probeMinBackoff // nothing to gain: probe promptly once there is
		return
	}
	if pad == nil {
		return
	}
	// Probe for 20% above what resuming needs, so the estimate that results (95%
	// of the measured rate, then the allocator's own 95% budget) covers it.
	var cur int64
	for _, s := range all {
		if !s.t.IsVideo() || !s.paused && s.t.Wanted() {
			cur += s.t.Bitrate()
		}
	}
	padRate := (cur+step)*6/5 - cur
	a.probing, a.probeStart, a.probeNeed = true, now, cur+step
	a.started.Add(1)
	stats.ProbesStarted.Inc()
	p.StartProbe()
	go a.sendPadding(p, pad, padRate, probeDuration)
}

// sendPadding paces padding packets at bps for dur, then ends the probe cluster.
func (a *allocator) sendPadding(p Prober, pad Padder, bps int64, dur time.Duration) {
	const tick = 5 * time.Millisecond
	perTick := float64(bps) * tick.Seconds() / 8 / PaddingPacketBytes
	t := time.NewTicker(tick)
	defer t.Stop()
	end := time.Now().Add(dur)
	debt := 0.0
	for {
		select {
		case <-a.stop:
			p.FinishProbe(time.Now())
			return
		case now := <-t.C:
			if now.After(end) {
				p.FinishProbe(now)
				return
			}
			debt = min(debt+perTick, 4*perTick+1) // padding only at frame boundaries: allow catching up a little
			if n := int(debt); n > 0 {
				debt -= float64(pad.WritePadding(n))
			}
		}
	}
}

// ProbeStats returns how many probes were started and how many raised the estimate.
func (a *allocator) ProbeStats() (started, succeeded uint64) {
	return a.started.Load(), a.succeeded.Load()
}
