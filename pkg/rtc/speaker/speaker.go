// Package speaker implements active speaker detection from the RFC 6464
// audio-level header extension (ARCHITECTURE.md 4.4).
package speaker

import (
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

type nopObserver struct{}

// NewObserver returns a speaker observer. Phase 0 stub: never reports speakers.
func NewObserver(cfg Config) Observer { return nopObserver{} }

func (nopObserver) AddTrack(string, string, LevelSource)    {}
func (nopObserver) RemoveTrack(string)                      {}
func (nopObserver) Tick(time.Time) []*tesserav1.SpeakerInfo { return nil }
func (nopObserver) Speakers() []*tesserav1.SpeakerInfo      { return nil }
