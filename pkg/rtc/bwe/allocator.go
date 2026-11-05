package bwe

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/KeshavGoyal26/tessera/pkg/rtc/types"
	"github.com/KeshavGoyal26/tessera/pkg/stats"
)

// allocTick is the allocator period.
const allocTick = 500 * time.Millisecond

// Probing: when a subscriber wants a higher layer than the estimate allows, the
// allocator sends padding for probeDuration so the path's capacity can be
// measured (see estimator probe clusters). Failed probes back off exponentially.
const (
	probeDuration   = 500 * time.Millisecond
	probeMinBackoff = 2 * time.Second
	probeMaxBackoff = 30 * time.Second
	probeTimeout    = probeDuration + 2*time.Second
	// After a successful probe, upgrades skip UpgradeHold for this long.
	probeBoost = 2 * time.Second
)

type trackState struct {
	t       AllocatableTrack
	applied types.VideoLayer
	set     bool
	paused  bool
	upSince time.Time
	prevMax int32
}

type allocator struct {
	cfg Config
	est Estimator

	mu       sync.Mutex
	tracks   map[string]*trackState
	onPaused func([]Allocation)
	stop     chan struct{}
	once     sync.Once
	now      func() time.Time

	// probe state, used only from Allocate
	probing    bool
	probeStart time.Time
	nextProbe  time.Time
	backoff    time.Duration
	boostUntil time.Time
	started    atomic.Uint64 // probes started
	succeeded  atomic.Uint64 // probes that raised the estimate
}

// NewAllocator returns the stream allocator for one subscriber connection.
func NewAllocator(cfg Config, est Estimator) Allocator {
	if cfg.UpgradeHold == 0 {
		cfg.UpgradeHold = 3 * time.Second
	}
	return &allocator{cfg: cfg, est: est, tracks: map[string]*trackState{}, stop: make(chan struct{}), now: time.Now}
}

func (a *allocator) AddTrack(t AllocatableTrack) {
	a.mu.Lock()
	a.tracks[t.ID()] = &trackState{t: t, applied: types.InvalidVideoLayer, prevMax: t.MaxLayer().Spatial}
	a.mu.Unlock()
}

func (a *allocator) RemoveTrack(id string) {
	a.mu.Lock()
	delete(a.tracks, id)
	a.mu.Unlock()
}

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

type feedbackCounter interface{ Feedbacks() uint64 }

// lowestFrom returns the lowest active spatial layer >= from and <= max.
func layerRate(b types.Bitrates, s int32) int64 { return b[s][types.MaxTemporalLayers-1] }

// Allocate runs one allocation round (exported for tests).
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
			budget -= float64(layerRate(s.t.Bitrates(), 0))
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
	desired := make([]int32, len(videos))
	rates := make([]types.Bitrates, len(videos))
	for i, s := range videos {
		rates[i] = s.t.Bitrates()
		desired[i] = types.InvalidLayer
		max := s.t.MaxLayer()
		if !max.IsValid() {
			continue
		}
		for l := int32(0); l <= max.Spatial && l < types.MaxSpatialLayers; l++ {
			if r := layerRate(rates[i], l); r > 0 {
				if float64(r) <= budget || !a.cfg.AllowPause {
					desired[i] = l
					budget -= float64(r)
				}
				break
			}
		}
	}
	for upgraded := true; upgraded; {
		upgraded = false
		for i, s := range videos {
			if desired[i] < 0 {
				continue
			}
			cur := layerRate(rates[i], desired[i])
			for l := desired[i] + 1; l <= s.t.MaxLayer().Spatial && l < types.MaxSpatialLayers; l++ {
				r := layerRate(rates[i], l)
				if r == 0 {
					continue
				}
				if float64(r-cur) <= budget {
					budget -= float64(r - cur)
					desired[i] = l
					upgraded = true
				}
				break
			}
		}
	}
	var out, changed []Allocation
	for i, s := range videos {
		want := desired[i]
		maxNow := s.t.MaxLayer().Spatial
		prefRaised := maxNow > s.prevMax
		s.prevMax = maxNow
		// Hysteresis: bandwidth-driven upgrades must be sustained for
		// UpgradeHold; upgrades requested by the subscriber are immediate.
		if s.set && want > s.applied.Spatial && s.applied.Spatial >= 0 && !prefRaised && !now.Before(a.boostUntil) {
			if s.upSince.IsZero() {
				s.upSince = now
			}
			if now.Sub(s.upSince) < a.cfg.UpgradeHold {
				want = s.applied.Spatial
			}
		} else {
			s.upSince = time.Time{}
		}
		target := types.InvalidVideoLayer
		if want >= 0 {
			target = types.VideoLayer{Spatial: want, Temporal: types.MaxTemporalLayers - 1}
		}
		paused := want < 0 && s.t.MaxLayer().IsValid()
		if !s.set || target != s.applied {
			s.t.SetTargetLayer(target)
			s.applied, s.set = target, true
		}
		al := Allocation{TrackID: s.t.ID(), Target: target, Paused: paused}
		if want >= 0 {
			al.Bitrate = layerRate(rates[i], want)
		}
		if paused != s.paused {
			s.paused = paused
			changed = append(changed, al)
		}
		out = append(out, al)
	}
	if len(changed) > 0 && cb != nil {
		cb(changed)
	}
	a.maybeProbe(now, states, videos, desired, rates)
	return out
}

// maybeProbe starts a probe when the highest-priority video that wants more than
// it gets could reach its next layer with more bandwidth, and tracks the outcome.
func (a *allocator) maybeProbe(now time.Time, all, videos []*trackState, desired []int32, rates []types.Bitrates) {
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
		if done && res > 0 {
			a.succeeded.Add(1)
			stats.ProbesSucceeded.Inc()
			a.backoff = probeMinBackoff
			a.nextProbe = now.Add(allocTick) // the next layer may need another step
			a.boostUntil = now.Add(probeBoost)
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
		if pd, ok := s.t.(Padder); ok && pad == nil && s.applied.IsValid() {
			pad = pd // any track that is forwarding can carry the padding
		}
		if step != 0 {
			continue
		}
		max := s.t.MaxLayer()
		if !max.IsValid() {
			continue
		}
		cur := int64(0)
		if desired[i] >= 0 {
			cur = layerRate(rates[i], desired[i])
		}
		for l := desired[i] + 1; l <= max.Spatial && l < types.MaxSpatialLayers; l++ {
			if r := layerRate(rates[i], l); r > 0 {
				step = r - cur
				break
			}
		}
	}
	if step <= 0 {
		a.backoff = probeMinBackoff // nothing to gain: probe promptly once there is
		return
	}
	if pad == nil {
		return
	}
	// Probe for 20% above what the upgrade needs, so the estimate that results
	// (95% of the measured rate, then the allocator's own 95% budget) covers it.
	var cur int64
	for _, s := range all {
		if !s.t.IsVideo() {
			cur += layerRate(s.t.Bitrates(), 0)
		} else if s.applied.IsValid() {
			cur += layerRate(s.t.Bitrates(), s.applied.Spatial)
		}
	}
	padRate := (cur+step)*6/5 - cur
	a.probing, a.probeStart = true, now
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
