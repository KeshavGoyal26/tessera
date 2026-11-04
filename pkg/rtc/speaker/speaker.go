// Package speaker implements active speaker detection from the RFC 6464
// audio-level header extension (ARCHITECTURE.md 4.4).
package speaker

import (
	"sort"
	"time"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
)

// LevelSource is implemented by audio Receivers. ReadLevel returns the
// smoothed level (0 = silence .. 1 = loud) and the fraction of recent packets
// flagged as voice, and resets the observation window.
type LevelSource interface {
	ReadLevel() (level float64, activeRatio float64)
}

// Observer tracks the speakers of one room. Methods are called from the room
// worker goroutine only.
type Observer interface {
	AddTrack(participantSID, trackSID string, src LevelSource)
	RemoveTrack(trackSID string)
	// Tick evaluates levels and returns only changed speakers (nil if none).
	Tick(now time.Time) []*tesserav1.SpeakerInfo
	// Speakers returns the current active speakers, loudest first.
	Speakers() []*tesserav1.SpeakerInfo
}

// Config tunes detection.
type Config struct {
	Interval time.Duration
	// MinActiveRatio of voice packets for a track to count as speaking.
	MinActiveRatio float64
	// SwitchMargin a challenger must exceed the current dominant speaker by.
	SwitchMargin float64
	// MaxSpeakers reported at once.
	MaxSpeakers int
}

type entry struct {
	participant string
	src         LevelSource
	smoothed    float64
	active      bool
	streak      int
}

type observer struct {
	cfg      Config
	tracks   map[string]*entry // by track SID
	dominant string            // participant SID
	reported map[string]float64
}

// NewObserver returns a speaker observer.
func NewObserver(cfg Config) Observer {
	if cfg.MinActiveRatio == 0 {
		cfg.MinActiveRatio = 0.3
	}
	if cfg.SwitchMargin == 0 {
		cfg.SwitchMargin = 0.05
	}
	if cfg.MaxSpeakers == 0 {
		cfg.MaxSpeakers = 3
	}
	return &observer{cfg: cfg, tracks: map[string]*entry{}, reported: map[string]float64{}}
}

func (o *observer) AddTrack(participantSID, trackSID string, src LevelSource) {
	o.tracks[trackSID] = &entry{participant: participantSID, src: src}
}

func (o *observer) RemoveTrack(trackSID string) { delete(o.tracks, trackSID) }

// Tick reads every track's level once, smooths it, applies hysteresis
// (2 consecutive active intervals to start, a margin to become dominant)
// and returns the changed speakers.
func (o *observer) Tick(time.Time) []*tesserav1.SpeakerInfo {
	levels := map[string]float64{}
	for _, e := range o.tracks {
		level, ratio := e.src.ReadLevel()
		if ratio < o.cfg.MinActiveRatio {
			level = 0
		}
		e.smoothed = 0.6*e.smoothed + 0.4*level
		if e.smoothed > 0.05 {
			e.streak++
		} else {
			e.streak = 0
		}
		e.active = e.streak >= 2
		if e.active && e.smoothed > levels[e.participant] {
			levels[e.participant] = e.smoothed
		}
	}
	// Dominant speaker with margin.
	best, bestLevel := "", 0.0
	for p, l := range levels {
		if l > bestLevel {
			best, bestLevel = p, l
		}
	}
	if cur, ok := levels[o.dominant]; !ok || best != o.dominant && bestLevel > cur+o.cfg.SwitchMargin {
		o.dominant = best
	}
	speakers := o.rank(levels)
	var changed []*tesserav1.SpeakerInfo
	now := map[string]float64{}
	for _, s := range speakers {
		now[s.ParticipantSid] = float64(s.Level)
		if prev, ok := o.reported[s.ParticipantSid]; !ok || abs(prev-float64(s.Level)) > 0.05 {
			changed = append(changed, s)
		}
	}
	for p := range o.reported {
		if _, ok := now[p]; !ok {
			changed = append(changed, &tesserav1.SpeakerInfo{ParticipantSid: p, Active: false})
		}
	}
	for p, l := range now {
		if _, ok := o.reported[p]; !ok || containsSID(changed, p) {
			o.reported[p] = l
		}
	}
	for p := range o.reported {
		if _, ok := now[p]; !ok {
			delete(o.reported, p)
		}
	}
	return changed
}

func containsSID(list []*tesserav1.SpeakerInfo, sid string) bool {
	for _, s := range list {
		if s.ParticipantSid == sid {
			return true
		}
	}
	return false
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func (o *observer) rank(levels map[string]float64) []*tesserav1.SpeakerInfo {
	out := make([]*tesserav1.SpeakerInfo, 0, len(levels))
	for p, l := range levels {
		out = append(out, &tesserav1.SpeakerInfo{ParticipantSid: p, Level: float32(l), Active: true})
	}
	sort.Slice(out, func(i, j int) bool {
		// Dominant speaker first, then by level.
		if (out[i].ParticipantSid == o.dominant) != (out[j].ParticipantSid == o.dominant) {
			return out[i].ParticipantSid == o.dominant
		}
		if out[i].Level != out[j].Level {
			return out[i].Level > out[j].Level
		}
		return out[i].ParticipantSid < out[j].ParticipantSid
	})
	if len(out) > o.cfg.MaxSpeakers {
		out = out[:o.cfg.MaxSpeakers]
	}
	return out
}

// Speakers implements Observer.
func (o *observer) Speakers() []*tesserav1.SpeakerInfo {
	levels := map[string]float64{}
	for _, e := range o.tracks {
		if e.active && e.smoothed > levels[e.participant] {
			levels[e.participant] = e.smoothed
		}
	}
	return o.rank(levels)
}
