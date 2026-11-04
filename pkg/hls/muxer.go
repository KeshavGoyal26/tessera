package hls

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// MuxerConfig configures a Muxer.
type MuxerConfig struct {
	// TargetSegmentDuration defaults to 4 s.
	TargetSegmentDuration time.Duration
	// Live writes an EVENT playlist after every segment.
	Live bool
	// PlaylistName defaults to "index.m3u8".
	PlaylistName string
	// StartTime is the wall-clock time of the first sample (defaults to now).
	StartTime time.Time
	// Origin is a wall-clock time shared by all tracks of a recording. When
	// set, the first sample is placed at (StartTime-Origin) on the output
	// timeline, so tracks stay aligned by timestamp, not only by PDT.
	Origin time.Time
	// MaxGap is the DTS gap that is treated as a discontinuity (default 2x target).
	MaxGap time.Duration
}

// Muxer writes one track as fMP4 HLS. It is not safe for concurrent use.
type Muxer struct {
	cfg  MuxerConfig
	spec TrackSpec
	sink Sink

	video     bool
	started   bool
	baseIn    int64 // input DTS mapped to baseOut
	baseOut   int64
	initCount int
	initName  string
	initDirty bool
	pending   *Sample
	prevEnd   int64 // input DTS of the end of the last committed sample
	lastDur   uint32
	cur       []Sample
	curDTSOut int64
	curPDT    time.Time
	curDisc   bool
	frag      uint32
	segN      int
	segs      []segmentInfo
	wallBase  time.Time
	firstDTS  int64
	closed    bool
}

type segmentInfo struct {
	name string
	dur  float64
	pdt  time.Time
	disc bool
	init string // non-empty when an EXT-X-MAP precedes this segment
}

// NewMuxer creates a muxer; the init segment is written with the first segment.
func NewMuxer(cfg MuxerConfig, spec TrackSpec, sink Sink) (*Muxer, error) {
	if sink == nil {
		return nil, errors.New("hls: nil sink")
	}
	if cfg.TargetSegmentDuration <= 0 {
		cfg.TargetSegmentDuration = 4 * time.Second
	}
	if cfg.PlaylistName == "" {
		cfg.PlaylistName = "index.m3u8"
	}
	if cfg.MaxGap <= 0 {
		cfg.MaxGap = 2 * cfg.TargetSegmentDuration
	}
	if _, err := InitSegment(spec); err != nil {
		return nil, err
	}
	return &Muxer{cfg: cfg, spec: spec, sink: sink, video: spec.Codec == CodecH264, initDirty: true}, nil
}

func (m *Muxer) ticks(d time.Duration) int64 {
	return int64(d) * int64(m.spec.Timescale) / int64(time.Second)
}

func (m *Muxer) wall(outDTS int64) time.Time {
	return m.wallBase.Add(time.Duration((outDTS - m.baseOut) * int64(time.Second) / int64(m.spec.Timescale)))
}

// UpdateSpec switches to new codec parameters (e.g. SPS change). The current
// segment is cut and the next one starts with a new init segment and an
// EXT-X-DISCONTINUITY.
func (m *Muxer) UpdateSpec(ctx context.Context, spec TrackSpec) error {
	if _, err := InitSegment(spec); err != nil {
		return err
	}
	if m.started {
		if err := m.commitPending(); err != nil {
			return err
		}
		if err := m.flush(ctx); err != nil {
			return err
		}
		m.curDisc = true
	}
	m.spec = spec
	m.initDirty = true
	return nil
}

func (m *Muxer) commitPending() error {
	if m.pending == nil {
		return nil
	}
	p := *m.pending
	m.pending = nil
	if p.Duration == 0 {
		p.Duration = m.lastDur
	}
	if p.Duration == 0 {
		p.Duration = uint32(m.ticks(20 * time.Millisecond))
	}
	m.lastDur = p.Duration
	m.prevEnd = p.DTS + int64(p.Duration)
	if len(m.cur) == 0 {
		m.curDTSOut = p.DTS - m.baseIn + m.baseOut
		m.curPDT = m.wall(m.curDTSOut)
	}
	m.cur = append(m.cur, p)
	return nil
}

// WriteSample appends a sample, cutting segments on keyframes after the target duration.
func (m *Muxer) WriteSample(ctx context.Context, s Sample) error {
	if m.closed {
		return errors.New("hls: muxer closed")
	}
	if !m.video {
		s.KeyFrame = true
	}
	if !m.started {
		if !s.KeyFrame {
			return nil // video must start on a keyframe
		}
		m.started = true
		m.wallBase = m.cfg.StartTime
		if m.wallBase.IsZero() {
			m.wallBase = time.Now()
		}
		m.baseIn = s.DTS
		if !m.cfg.Origin.IsZero() && !m.cfg.StartTime.IsZero() && m.cfg.StartTime.After(m.cfg.Origin) {
			m.baseOut = m.ticks(m.cfg.StartTime.Sub(m.cfg.Origin))
		}
		m.firstDTS = s.DTS
		m.pending = &s
		m.curDisc = false
		return nil
	}
	p := m.pending
	delta := s.DTS - p.DTS
	switch {
	case delta <= 0:
		// Clock went backwards (or duplicate): start a new timeline piece.
		if err := m.cutForJump(ctx, s, true); err != nil {
			return err
		}
		if !s.KeyFrame && m.video {
			m.pending = nil
			return nil
		}
		m.pending = &s
		return nil
	case delta > m.ticks(m.cfg.MaxGap):
		if err := m.cutForJump(ctx, s, false); err != nil {
			return err
		}
		if !s.KeyFrame && m.video {
			m.pending = nil
			return nil
		}
		m.pending = &s
		return nil
	}
	p.Duration = uint32(delta)
	if err := m.commitPending(); err != nil {
		return err
	}
	if s.KeyFrame && len(m.cur) > 0 && s.DTS-m.cur[0].DTS >= m.ticks(m.cfg.TargetSegmentDuration) {
		if err := m.flush(ctx); err != nil {
			return err
		}
	}
	m.pending = &s
	return nil
}

// cutForJump closes the current segment and marks the next one as discontinuous.
// For backwards jumps the output timeline continues from the end of the last sample.
func (m *Muxer) cutForJump(ctx context.Context, next Sample, backwards bool) error {
	if err := m.commitPending(); err != nil {
		return err
	}
	if err := m.flush(ctx); err != nil {
		return err
	}
	m.curDisc = true
	if backwards {
		m.baseOut += m.prevEnd - m.baseIn
		m.baseIn = next.DTS
	}
	return nil
}

func (m *Muxer) flush(ctx context.Context) error {
	if len(m.cur) == 0 {
		return nil
	}
	if m.initDirty {
		data, err := InitSegment(m.spec)
		if err != nil {
			return err
		}
		m.initName = "init.mp4"
		if m.initCount > 0 {
			m.initName = fmt.Sprintf("init_%d.mp4", m.initCount)
		}
		if err := m.sink.WriteInit(ctx, m.initName, data); err != nil {
			return err
		}
		m.initCount++
		m.initDirty = false
		m.curDisc = m.curDisc || m.initCount > 1
		m.segs = append(m.segs, segmentInfo{init: m.initName}) // marker, merged below
	}
	var dur int64
	for _, s := range m.cur {
		dur += int64(s.Duration)
	}
	m.frag++
	data := Fragment(m.spec.ID, m.frag, uint64(m.curDTSOut), m.cur)
	name := fmt.Sprintf("seg_%05d.m4s", m.segN)
	d := time.Duration(dur * int64(time.Second) / int64(m.spec.Timescale))
	seg := Segment{Name: name, Data: data, Duration: d, ProgramDateTime: m.curPDT, Discontinuity: m.curDisc}
	if err := m.sink.WriteSegment(ctx, seg); err != nil {
		return err
	}
	m.segN++
	info := segmentInfo{name: name, dur: d.Seconds(), pdt: m.curPDT, disc: m.curDisc}
	// merge init marker into the segment entry
	if n := len(m.segs); n > 0 && m.segs[n-1].name == "" {
		info.init = m.segs[n-1].init
		m.segs = m.segs[:n-1]
	}
	m.segs = append(m.segs, info)
	m.cur = m.cur[:0]
	m.curDisc = false
	if m.cfg.Live {
		return m.writePlaylist(ctx, false)
	}
	return nil
}

func (m *Muxer) writePlaylist(ctx context.Context, final bool) error {
	mp := MediaPlaylist{Live: !final}
	mp.TargetMin = int(m.cfg.TargetSegmentDuration.Seconds() + 0.5)
	for _, s := range m.segs {
		mp.Segments = append(mp.Segments, PlaylistSegment{
			URI: s.name, Duration: s.dur, ProgramDateTime: s.pdt, Discontinuity: s.disc, Map: s.init,
		})
	}
	return m.sink.WritePlaylist(ctx, m.cfg.PlaylistName, mp.Encode())
}

// SegmentCount returns the number of segments written.
func (m *Muxer) SegmentCount() int { return m.segN }

// Close flushes the last segment and writes the final VOD playlist with EXT-X-ENDLIST.
func (m *Muxer) Close(ctx context.Context) error {
	if m.closed {
		return nil
	}
	m.closed = true
	if m.started {
		if err := m.commitPending(); err != nil {
			return err
		}
		if err := m.flush(ctx); err != nil {
			return err
		}
	}
	if len(m.segs) == 0 {
		return nil
	}
	return m.writePlaylist(ctx, true)
}
