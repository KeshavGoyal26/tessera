package recorder

import (
	"testing"
	"time"
)

// TestQualityTimeline drives the timeline with a video and an audio track: a clean step,
// heavy loss, a short video pause, a frozen video, everything stopping, and a recovery.
func TestQualityTimeline(t *testing.T) {
	origin := time.Unix(1000, 0)
	video := &recTrack{rec: &trackRecorder{codec: "H264"}}
	audio := &recTrack{rec: &trackRecorder{codec: "Opus"}}
	tracks := []*recTrack{video, audio}
	q := newQualityLog(origin)
	now := origin
	// recv advances time by d, receiving on the given tracks every 50 ms (and losing
	// lossPct of the video packets) while the clock moves, ticking every 250 ms.
	recv := func(d time.Duration, lossPct uint64, on ...*recTrack) {
		for end := now.Add(d); now.Before(end); {
			now = now.Add(50 * time.Millisecond)
			for _, tr := range on {
				s := &tr.rec.stats
				if tr == video {
					s.packets.Add(100 - lossPct)
					s.lost.Add(lossPct)
				} else {
					s.packets.Add(100)
				}
				s.bytes.Add(1000)
				s.lastPacketUnixNano.Store(now.UnixNano())
			}
			if now.Sub(origin)%(250*time.Millisecond) == 0 {
				q.tick(now, tracks)
			}
		}
	}
	recv(4*time.Second, 0, video, audio)  // step 0: good
	recv(4*time.Second, 15, video, audio) // step 1: 15 % video loss (7.5 % overall): fair
	recv(4*time.Second, 30, video, audio) // step 2: 30 % video loss (15 % overall): poor
	recv(3*time.Second, 0, video, audio)  // step 3: the video pauses 1 s
	recv(time.Second, 0, audio)
	recv(time.Second, 0, video, audio) // step 4: video frozen 3 s, audio flowing
	recv(3*time.Second, 0, audio)
	recv(time.Second, 0, video, audio) // step 5: nothing for 3 s
	recv(3*time.Second, 0)
	recv(4*time.Second, 0, video, audio) // step 6: back
	recv(time.Second, 0, video, audio)   // into step 7
	q.close(tracks)
	want := []int{QualityGood, QualityFair, QualityPoor, QualityFair, QualityPoor, QualityLost, QualityGood, QualityGood}
	if len(q.tl.Samples) != len(want) {
		t.Fatalf("%d samples: %+v", len(q.tl.Samples), q.tl.Samples)
	}
	for i, s := range q.tl.Samples {
		if s.Level != want[i] || s.T != float64(i*4) {
			t.Errorf("step %d: %+v, want level %d", i, s, want[i])
		}
	}
	if s := q.tl.Samples[2]; s.Loss < 14 || s.Loss > 16 || s.Kbps == 0 {
		t.Errorf("loss/kbps of step 2: %+v", s)
	}
	if s := q.tl.Samples[4]; s.GapMs < 2700 { // ticks are 250 ms apart
		t.Errorf("video gap of step 4: %+v", s)
	}
	if !q.dirty || q.due(now) != true {
		t.Error("a new timeline is due for writing")
	}
}

func TestQualityLevels(t *testing.T) {
	for _, c := range []struct {
		loss       float64
		video, all time.Duration
		want       int
	}{
		{0, 0, 0, QualityGood}, {3, 600 * time.Millisecond, 0, QualityGood}, {3.1, 0, 0, QualityFair},
		{0, 700 * time.Millisecond, 0, QualityFair}, {10.1, 0, 0, QualityPoor}, {0, 2 * time.Second, 0, QualityPoor},
		{0, 0, 2 * time.Second, QualityLost},
	} {
		if got := level(c.loss, c.video, c.all); got != c.want {
			t.Errorf("level(%v, %v, %v) = %d, want %d", c.loss, c.video, c.all, got, c.want)
		}
	}
}
