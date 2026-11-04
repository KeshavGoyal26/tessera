package recorder

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"time"

	"github.com/pion/rtp"

	"github.com/KeshavGoyal26/tessera/pkg/hls"
)

// Track is a subscribed remote track handed to the recorder by a Connector.
type Track struct {
	SID                 string
	ParticipantIdentity string
	// Kind is "audio" or "video".
	Kind string
	// Source is "camera", "microphone", "screen_share" or "screen_share_audio".
	Source string
	RTP    RTPSource
}

// SRSource is implemented by tracks that expose RTCP sender reports. The
// recorder uses them to align audio and video on the sender's wall clock;
// without it, packet arrival times are used.
type SRSource interface {
	OnSenderReport(func(ntp uint64, rtpTimestamp uint32))
}

type trackStats struct {
	packets, bytes, lost, late, dropped atomic.Uint64
	keyframeReqs                        atomic.Uint64
	lastPacketUnixNano                  atomic.Int64
	width, height                       atomic.Uint32
	err                                 atomic.Value // string
}

// trackRecorder turns one RTPSource into an HLS rendition.
type trackRecorder struct {
	tr     Track
	codec  string // "H264" or "Opus"
	mime   string
	opts   trackOptions
	sink   *storageSink
	stats  trackStats
	logger interface{ Warn(string, ...any) }

	mux  *hls.Muxer
	spec hls.TrackSpec

	reorder *hls.ReorderBuffer
	depack  *hls.H264Depacketizer
	unwrap  hls.Unwrapper
	clock   hls.WallClock
	ssrc    uint32
	haveSSR bool
	rebase  int64
	lastDTS int64
	lastAt  time.Time

	fmtpSPS, fmtpPPS []byte
	lastKeyReq       time.Time
	needKey          bool
	pendingRebase    bool
	sr               atomic.Pointer[srEvent]

	codecString string
	firstWall   atomic.Int64 // unix nano of first sample
	started     chan struct{}

	// FFmpeg fallbacks.
	aacTC   *aacTranscoder
	vp8TC   *vp8Transcoder
	tcOut   <-chan transcoded
	tcDone  bool
	release func()
}

type trackOptions struct {
	segment time.Duration
	live    bool
	origin  time.Time
	// aac transcodes Opus to AAC-LC with FFmpeg.
	aac    bool
	ffmpeg string
	// reserve takes extra capacity units for CPU-heavy pipelines and returns the release func.
	reserve func(n int) func()
}

func newTrackRecorder(tr Track, sink *storageSink, opts trackOptions, logger interface{ Warn(string, ...any) }) (*trackRecorder, error) {
	mime := strings.ToLower(tr.RTP.Codec().MimeType)
	r := &trackRecorder{tr: tr, mime: mime, sink: sink, opts: opts, logger: logger, started: make(chan struct{})}
	switch mime {
	case "video/h264":
		r.codec = "H264"
		r.depack = &hls.H264Depacketizer{}
		r.clock.Rate = 90000
		r.parseFmtp(tr.RTP.Codec().SDPFmtpLine)
	case "video/vp8":
		r.codec = "VP8"
		r.clock.Rate = 90000
	case "audio/opus":
		r.codec = "Opus"
		r.clock.Rate = 48000
	default:
		return nil, fmt.Errorf("unsupported codec %q", tr.RTP.Codec().MimeType)
	}
	r.reorder = hls.NewReorderBuffer()
	if sr, ok := tr.RTP.(SRSource); ok {
		sr.OnSenderReport(func(ntp uint64, ts uint32) { r.srCh(ntp, ts) })
	}
	return r, nil
}

type srEvent struct {
	ntp uint64
	ts  uint32
}

func (r *trackRecorder) srCh(ntp uint64, ts uint32) {
	// Sender reports arrive on another goroutine; they are applied by the
	// processing loop through this atomic mailbox.
	r.sr.Store(&srEvent{ntp, ts})
}

func (r *trackRecorder) parseFmtp(f string) {
	for _, kv := range strings.Split(f, ";") {
		kv = strings.TrimSpace(kv)
		if v, ok := strings.CutPrefix(kv, "sprop-parameter-sets="); ok {
			parts := strings.Split(v, ",")
			if len(parts) >= 2 {
				r.fmtpSPS, _ = base64.StdEncoding.DecodeString(parts[0])
				r.fmtpPPS, _ = base64.StdEncoding.DecodeString(parts[1])
			}
		}
	}
}

// run reads until the source ends or ctx is cancelled, then flushes and
// closes the muxer. It returns nil on a clean end.
func (r *trackRecorder) run(ctx context.Context) error {
	pkts := make(chan *rtp.Packet, 256)
	readErr := make(chan error, 1)
	go func() {
		buf := make([]byte, 1500)
		for {
			n, err := r.tr.RTP.Read(buf)
			if err != nil {
				readErr <- err
				close(pkts)
				return
			}
			p := &rtp.Packet{}
			if err := p.Unmarshal(append([]byte(nil), buf[:n]...)); err != nil {
				continue
			}
			select {
			case pkts <- p:
			case <-ctx.Done():
				readErr <- ctx.Err()
				close(pkts)
				return
			}
		}
	}()
	if r.codec != "Opus" {
		_ = r.tr.RTP.RequestKeyFrame()
		r.lastKeyReq = time.Now()
	}
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	var procErr error
loop:
	for {
		select {
		case p, ok := <-pkts:
			if !ok {
				break loop
			}
			if err := r.handle(ctx, p, time.Now()); err != nil {
				procErr = err
				break loop
			}
		case e, ok := <-r.tcOut:
			if !ok {
				r.tcOut, r.tcDone = nil, true
				continue
			}
			if err := r.writeTranscoded(ctx, e); err != nil {
				procErr = err
				break loop
			}
		case now := <-tick.C:
			for _, p := range r.reorder.Poll(now) {
				if err := r.process(ctx, p, now); err != nil {
					procErr = err
					break loop
				}
			}
			r.maybeKeyframe(now)
		case <-ctx.Done():
			break loop
		}
	}
	// Flush what is buffered even when cancelled; muxing is CPU-only.
	fctx := context.WithoutCancel(ctx)
	if procErr == nil {
		for _, p := range r.reorder.Poll(time.Now().Add(time.Hour)) {
			if procErr = r.process(fctx, p, time.Now()); procErr != nil {
				break
			}
		}
	}
	if r.depack != nil {
		if au := r.depack.Flush(); au != nil && procErr == nil {
			procErr = r.writeAU(fctx, au)
		}
	}
	if err := r.finishTranscoder(fctx); err != nil && procErr == nil {
		procErr = err
	}
	if r.release != nil {
		r.release()
	}
	if r.mux != nil {
		if err := r.mux.Close(fctx); err != nil && procErr == nil {
			procErr = err
		}
	}
	if procErr != nil {
		r.stats.err.Store(procErr.Error())
		return procErr
	}
	select {
	case err := <-readErr:
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) {
			return nil // source closed: a normal end of the track
		}
	default:
	}
	return nil
}

func (r *trackRecorder) maybeKeyframe(now time.Time) {
	if r.codec == "Opus" || r.mux != nil && !r.needKey || now.Sub(r.lastKeyReq) < time.Second {
		return
	}
	r.lastKeyReq = now
	r.stats.keyframeReqs.Add(1)
	_ = r.tr.RTP.RequestKeyFrame()
}

func (r *trackRecorder) handle(ctx context.Context, p *rtp.Packet, now time.Time) error {
	r.stats.packets.Add(1)
	r.stats.bytes.Add(uint64(len(p.Payload)))
	r.stats.lastPacketUnixNano.Store(now.UnixNano())
	if r.haveSSR && p.SSRC != r.ssrc {
		if err := r.onSSRCChange(ctx, p, now); err != nil {
			return err
		}
	}
	if !r.haveSSR {
		r.haveSSR, r.ssrc = true, p.SSRC
	}
	for _, q := range r.reorder.Push(p, now) {
		if err := r.process(ctx, q, now); err != nil {
			return err
		}
	}
	r.stats.lost.Store(r.reorder.Lost)
	r.stats.late.Store(r.reorder.Late)
	return nil
}

// onSSRCChange resets per-stream state. The output timeline continues from
// the last sample plus the elapsed wall time, so no jump is visible.
func (r *trackRecorder) onSSRCChange(ctx context.Context, p *rtp.Packet, now time.Time) error {
	fctx := context.WithoutCancel(ctx)
	for _, q := range r.reorder.Poll(now.Add(time.Hour)) {
		if err := r.process(fctx, q, now); err != nil {
			return err
		}
	}
	if r.depack != nil {
		if au := r.depack.Flush(); au != nil {
			if err := r.writeAU(fctx, au); err != nil {
				return err
			}
		}
		r.depack = &hls.H264Depacketizer{}
		r.needKey = true
	}
	r.reorder = hls.NewReorderBuffer()
	r.unwrap = hls.Unwrapper{}
	r.ssrc = p.SSRC
	r.pendingRebase = true
	r.clock = hls.WallClock{Rate: r.clock.Rate}
	return nil
}

func (r *trackRecorder) dts(ts uint32, now time.Time) int64 {
	d := r.unwrap.Unwrap(ts)
	r.clock.ObserveArrival(d, now)
	if r.pendingRebase {
		r.pendingRebase = false
		elapsed := int64(now.Sub(r.lastAt).Seconds() * float64(r.clock.Rate))
		r.rebase = r.lastDTS + elapsed - d
	}
	d += r.rebase
	r.lastDTS, r.lastAt = d, now
	return d
}

func (r *trackRecorder) process(ctx context.Context, p *rtp.Packet, now time.Time) error {
	if sr := r.sr.Swap(nil); sr != nil && r.unwrapReady() {
		r.clock.ObserveSR(sr.ntp, r.unwrap.Unwrap(sr.ts))
	}
	switch r.codec {
	case "Opus":
		if len(p.Payload) == 0 {
			return nil
		}
		d := r.dts(p.Timestamp, now)
		if r.opts.aac {
			if r.aacTC == nil {
				tc, err := newAACTranscoder(context.WithoutCancel(ctx), r.opts.ffmpeg, d)
				if err != nil {
					return err
				}
				r.aacTC, r.tcOut = tc, tc.out
				r.codecString = "mp4a.40.2"
				if r.opts.reserve != nil {
					r.release = r.opts.reserve(1)
				}
			}
			return r.aacTC.write(append([]byte(nil), p.Payload...), hls.OpusFrameSamples(p.Payload))
		}
		if r.mux == nil {
			if err := r.createMuxer(hls.TrackSpec{ID: 1, Codec: hls.CodecOpus, Timescale: 48000, Channels: 2}, d); err != nil {
				return err
			}
		}
		return r.mux.WriteSample(ctx, hls.Sample{DTS: d, Data: append([]byte(nil), p.Payload...)})
	case "VP8":
		if r.vp8TC == nil {
			tc, err := newVP8Transcoder(context.WithoutCancel(ctx), r.opts.ffmpeg)
			if err != nil {
				return err
			}
			r.vp8TC, r.tcOut = tc, tc.out
			if r.opts.reserve != nil {
				r.release = r.opts.reserve(3)
			}
		}
		err := r.vp8TC.push(p, func(ts uint32) int64 { return r.dts(ts, now) })
		if errors.Is(err, hls.ErrPacketLoss) {
			r.needKey = true
			r.maybeKeyframe(now)
			err = nil
		}
		return err
	default:
		aus, err := r.depack.Push(p)
		if errors.Is(err, hls.ErrPacketLoss) {
			r.needKey = true
			r.maybeKeyframe(now)
		}
		for _, au := range aus {
			if err := r.writeAU(ctx, au); err != nil {
				return err
			}
		}
		return nil
	}
}

func (r *trackRecorder) unwrapReady() bool { return r.haveSSR }

func (r *trackRecorder) writeAU(ctx context.Context, au *hls.AccessUnit) error {
	return r.writeAUAt(ctx, au, r.dts(au.RTPTimestamp, time.Now()))
}

// writeTranscoded handles output of an FFmpeg pipeline.
func (r *trackRecorder) writeTranscoded(ctx context.Context, e transcoded) error {
	if e.au != nil {
		return r.writeAUAt(ctx, e.au, e.dts)
	}
	if r.mux == nil {
		spec := hls.TrackSpec{ID: 1, Codec: hls.CodecAAC, Timescale: 48000, SampleRate: 48000, Channels: 2, AudioSpecificConfig: aacConfig48kStereo}
		if err := r.createMuxer(spec, e.dts); err != nil {
			return err
		}
	}
	return r.mux.WriteSample(ctx, hls.Sample{DTS: e.dts, Data: e.data})
}

// finishTranscoder closes FFmpeg's input and writes the frames still in flight.
func (r *trackRecorder) finishTranscoder(ctx context.Context) error {
	var result func() error
	switch {
	case r.aacTC != nil:
		r.aacTC.closeInput()
		result = r.aacTC.result
	case r.vp8TC != nil:
		r.vp8TC.closeInput()
		result = r.vp8TC.result
	default:
		return nil
	}
	if !r.tcDone {
		for e := range r.tcOut {
			if err := r.writeTranscoded(ctx, e); err != nil {
				return err
			}
		}
	}
	return result()
}

func (r *trackRecorder) writeAUAt(ctx context.Context, au *hls.AccessUnit, d int64) error {
	sps, pps := au.SPS, au.PPS
	if sps == nil && r.mux == nil {
		sps, pps = r.fmtpSPS, r.fmtpPPS
	}
	if sps != nil && pps == nil {
		pps = r.spec.PPS
		if pps == nil {
			pps = r.fmtpPPS
		}
	}
	if sps != nil && len(pps) > 0 && !bytes.Equal(sps, r.spec.SPS) {
		info, err := hls.ParseSPS(sps)
		if err != nil {
			return nil //nolint:nilerr // a malformed parameter set is skipped, not fatal
		}
		spec := hls.TrackSpec{ID: 1, Codec: hls.CodecH264, Timescale: 90000, Width: info.Width, Height: info.Height, SPS: sps, PPS: pps}
		if r.mux == nil {
			if err := r.createMuxer(spec, d); err != nil {
				return err
			}
		} else if err := r.mux.UpdateSpec(ctx, spec); err != nil {
			return err
		}
		r.spec = spec
		r.stats.width.Store(info.Width)
		r.stats.height.Store(info.Height)
		r.codecString = info.CodecString()
	}
	if r.mux == nil {
		r.stats.dropped.Add(1)
		return nil
	}
	if au.KeyFrame {
		r.needKey = false
	}
	return r.mux.WriteSample(ctx, hls.Sample{DTS: d, KeyFrame: au.KeyFrame, Data: au.AVCC()})
}

func (r *trackRecorder) createMuxer(spec hls.TrackSpec, dts int64) error {
	start := r.clock.At(dts)
	m, err := hls.NewMuxer(hls.MuxerConfig{
		TargetSegmentDuration: r.opts.segment, Live: r.opts.live, StartTime: start, Origin: r.opts.origin,
	}, spec, r.sink)
	if err != nil {
		return err
	}
	r.mux, r.spec = m, spec
	r.firstWall.Store(start.UnixNano())
	close(r.started)
	if spec.Codec == hls.CodecOpus {
		r.codecString = "opus"
	}
	return nil
}
