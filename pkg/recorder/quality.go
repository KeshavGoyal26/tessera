package recorder

// Connection timeline of a recording: every qualityStep, how the participant's media
// reached the recorder (packets lost after retransmission, video freezes, everything
// stopping). It is what a reviewer sees as glitches, so players draw it under the
// scrubber (green, yellow, red). It is written next to master.m3u8 as quality.json
// every qualityFlushEvery while recording (about a sixth of the uploads the media
// playlists already make, and live players are about that far behind anyway) and once
// more at the end; sample times are media times (seconds from the recording's origin),
// as the player's currentTime.

import (
	"bytes"
	"context"
	"encoding/json"
	"path"
	"time"

	"github.com/KeshavGoyal26/tessera/pkg/storage"
)

const (
	qualityStep       = 4 * time.Second
	qualityFlushEvery = 12 * time.Second
	qualityFile       = "quality.json"
)

// Connection levels of a sample.
const (
	QualityGood = iota // under 3 % loss, video flowing
	QualityFair        // 3-10 % loss, or the video paused 0.7-2 s
	QualityPoor        // over 10 % loss, or the video frozen 2 s or more
	QualityLost        // nothing arrived for 2 s or more on any track (disconnected)
)

// QualitySample is one step of the timeline.
type QualitySample struct {
	T     float64 `json:"t"`      // start, seconds of media time
	Level int     `json:"q"`      // QualityGood..QualityLost
	Loss  float64 `json:"loss"`   // percent of packets lost after retransmission
	GapMs int64   `json:"gap_ms"` // longest stretch without video packets
	Kbps  int     `json:"kbps"`   // received, all tracks
}

// QualityTimeline is quality.json.
type QualityTimeline struct {
	StepMs  int64           `json:"step_ms"`
	Final   bool            `json:"final"`
	Samples []QualitySample `json:"samples"`
}

type qualityMark struct {
	packets, lost, bytes uint64
	gap                  time.Duration // longest gap seen in the current step
}

// qualityLog builds the timeline from the tracks' counters, on the job goroutine.
type qualityLog struct {
	origin  time.Time
	step    int // index of the current step, -1 before the first tick
	marks   map[*recTrack]qualityMark
	allGap  time.Duration // longest gap during which no track received anything
	tl      QualityTimeline
	dirty   bool
	written time.Time
}

func newQualityLog(origin time.Time) *qualityLog {
	return &qualityLog{origin: origin, step: -1, marks: map[*recTrack]qualityMark{}, tl: QualityTimeline{StepMs: qualityStep.Milliseconds()}}
}

// tick runs every job tick (250 ms): it tracks gaps and closes a step when it ends.
func (q *qualityLog) tick(now time.Time, tracks []*recTrack) {
	step := int(now.Sub(q.origin) / qualityStep)
	if q.step >= 0 && step != q.step {
		q.close(tracks)
	}
	if step != q.step {
		q.step = step
		for _, t := range tracks {
			m := q.mark(t)
			m.gap = 0
			q.marks[t] = m
		}
		q.allGap = 0
	}
	// Only the part of a gap inside this step counts: an outage that began in the
	// previous step was judged there.
	stepStart := q.origin.Add(time.Duration(step) * qualityStep)
	newest := time.Duration(-1)
	for _, t := range tracks {
		last := t.rec.stats.lastPacketUnixNano.Load()
		if last == 0 {
			continue // not started yet
		}
		from := time.Unix(0, last)
		if from.Before(stepStart) {
			from = stepStart
		}
		gap := now.Sub(from)
		if newest < 0 || gap < newest {
			newest = gap
		}
		m, ok := q.marks[t]
		if !ok { // a track that appeared during this step
			m = q.mark(t)
			m.packets, m.lost, m.bytes = 0, 0, 0
		}
		if t.rec.codec != "Opus" && gap > m.gap { // audio pauses in silence (DTX)
			m.gap = gap
		}
		q.marks[t] = m
	}
	if newest > q.allGap {
		q.allGap = newest
	}
}

func (q *qualityLog) mark(t *recTrack) qualityMark {
	s := &t.rec.stats
	return qualityMark{packets: s.packets.Load(), lost: s.lost.Load(), bytes: s.bytes.Load()}
}

// close appends the sample of the current step.
func (q *qualityLog) close(tracks []*recTrack) {
	var packets, lost, bytes uint64
	var videoGap time.Duration
	started := false
	for _, t := range tracks {
		m, ok := q.marks[t]
		if !ok {
			continue
		}
		cur := q.mark(t)
		packets += cur.packets - m.packets
		lost += cur.lost - m.lost
		bytes += cur.bytes - m.bytes
		videoGap = max(videoGap, m.gap)
		started = started || t.rec.stats.lastPacketUnixNano.Load() != 0
	}
	if !started {
		return // nothing to judge before the first packet
	}
	s := QualitySample{T: float64(q.step) * qualityStep.Seconds(), GapMs: videoGap.Milliseconds(),
		Kbps: int(bytes * 8 / uint64(qualityStep.Milliseconds()))}
	if packets+lost > 0 {
		s.Loss = float64(int(float64(lost)*1000/float64(packets+lost))) / 10
	}
	s.Level = level(s.Loss, videoGap, q.allGap)
	q.tl.Samples = append(q.tl.Samples, s)
	q.dirty = true
}

func level(loss float64, videoGap, allGap time.Duration) int {
	switch {
	case allGap >= 2*time.Second:
		return QualityLost
	case loss > 10 || videoGap >= 2*time.Second:
		return QualityPoor
	case loss > 3 || videoGap >= 700*time.Millisecond:
		return QualityFair
	}
	return QualityGood
}

// due reports whether the timeline should be written now.
func (q *qualityLog) due(now time.Time) bool {
	return q.dirty && now.Sub(q.written) >= qualityFlushEvery
}

// write stores quality.json next to the master playlist (best effort: the timeline is
// an aid, a failed upload does not fail the recording).
func (r *jobRun) writeQuality(ctx context.Context, final bool) {
	q := r.quality
	q.tl.Final = final
	b, err := json.Marshal(q.tl)
	if err != nil {
		return
	}
	if _, err := r.st.Put(ctx, path.Join(r.prefix, qualityFile), bytes.NewReader(b), int64(len(b)), storage.ContentTypeJSON); err != nil {
		r.w.logger.Warn("quality timeline not written", "recording", r.id, "err", err)
		return
	}
	q.dirty, q.written = false, time.Now()
}

func (r *jobRun) recTracks() []*recTrack {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*recTrack, 0, len(r.tracks))
	for _, sid := range r.order {
		out = append(out, r.tracks[sid])
	}
	return out
}
