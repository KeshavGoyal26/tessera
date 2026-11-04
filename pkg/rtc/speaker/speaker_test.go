package speaker

import (
	"testing"
	"time"
)

type src struct{ level, ratio float64 }

func (s *src) ReadLevel() (float64, float64) { return s.level, s.ratio }

func TestHysteresisAndDominance(t *testing.T) {
	o := NewObserver(Config{})
	a, b := &src{}, &src{}
	o.AddTrack("PA_a", "TR_a", a)
	o.AddTrack("PA_b", "TR_b", b)
	if c := o.Tick(time.Now()); len(c) != 0 {
		t.Fatal(c)
	}
	a.level, a.ratio = 0.6, 1
	if c := o.Tick(time.Now()); len(c) != 0 {
		t.Fatal("one interval must not activate", c)
	}
	c := o.Tick(time.Now())
	if len(c) != 1 || c[0].ParticipantSid != "PA_a" || !c[0].Active {
		t.Fatal(c)
	}
	// b slightly louder: a stays dominant (margin).
	b.level, b.ratio = 0.62, 1
	o.Tick(time.Now())
	o.Tick(time.Now())
	if s := o.Speakers(); len(s) != 2 || s[0].ParticipantSid != "PA_a" {
		t.Fatalf("%v", s)
	}
	// a stops; reported inactive after smoothing decays.
	a.level = 0
	var stopped bool
	for range 10 {
		for _, s := range o.Tick(time.Now()) {
			if s.ParticipantSid == "PA_a" && !s.Active {
				stopped = true
			}
		}
	}
	if !stopped {
		t.Fatal("a never reported inactive")
	}
	if s := o.Speakers(); len(s) != 1 || s[0].ParticipantSid != "PA_b" {
		t.Fatalf("%v", s)
	}
}
