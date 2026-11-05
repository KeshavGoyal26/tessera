package rtc

import (
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"

	"github.com/KeshavGoyal26/tessera/pkg/errs"
	"github.com/KeshavGoyal26/tessera/pkg/rtc/buffer"
	"github.com/KeshavGoyal26/tessera/pkg/rtc/types"
	"github.com/KeshavGoyal26/tessera/pkg/stats"
)

// FeedbackWriter sends RTCP to the publisher.
type FeedbackWriter func(pkts []rtcp.Packet) error

// ReceiverParams configure a Receiver.
type ReceiverParams struct {
	TrackID  string
	StreamID string
	Kind     webrtc.RTPCodecType
	Codec    webrtc.RTPCodecParameters
	// AudioLevelExtID of the publisher PC (0 = none).
	AudioLevelExtID uint8
	WriteRTCP       FeedbackWriter
	// PLIThrottle is the minimum interval between PLIs per layer.
	PLIThrottle time.Duration
	// BufferSlots overrides the ring size.
	BufferSlots int
	Logger      *slog.Logger
}

type layer struct {
	spatial int32
	src     RTPSource
	buf     *buffer.Buffer
	ssrc    uint32
	rid     string
	meter   stats.Meter
	lastPLI atomic.Int64
	plis    stats.Counter
	nacks   stats.Counter
	// Last sender report: NTP time and RTP timestamp, arrival.
	srNTP     atomic.Uint64
	srRTP     atomic.Uint32
	srArrival atomic.Int64
	// RR interval state (RTCP ticker goroutine only).
	rrExpected, rrReceived uint64
}

// ReceiverImpl is the Receiver implementation.
type ReceiverImpl struct {
	p      ReceiverParams
	isVP8  bool
	layers [types.MaxSpatialLayers]atomic.Pointer[layer]
	maxL   atomic.Int32

	dtMu      sync.Mutex
	downs     atomic.Pointer[[]DownTrack]
	closed    atomic.Bool
	wg        sync.WaitGroup
	onMaxSub  func(int32)
	onClosed  func()
	audio     audioLevel
	closeOnce sync.Once
}

// NewReceiver creates a Receiver.
func NewReceiver(p ReceiverParams) *ReceiverImpl {
	if p.PLIThrottle == 0 {
		p.PLIThrottle = 500 * time.Millisecond
	}
	if p.Logger == nil {
		p.Logger = slog.Default()
	}
	r := &ReceiverImpl{p: p, isVP8: strings.EqualFold(p.Codec.MimeType, webrtc.MimeTypeVP8)}
	r.maxL.Store(-1)
	empty := []DownTrack{}
	r.downs.Store(&empty)
	return r
}

// TrackID implements Receiver.
func (r *ReceiverImpl) TrackID() string { return r.p.TrackID }

// StreamID implements Receiver.
func (r *ReceiverImpl) StreamID() string { return r.p.StreamID }

// Kind implements Receiver.
func (r *ReceiverImpl) Kind() webrtc.RTPCodecType { return r.p.Kind }

// Codec implements Receiver.
func (r *ReceiverImpl) Codec() webrtc.RTPCodecParameters { return r.p.Codec }

// MaxSpatial returns the highest layer added so far (-1 none).
func (r *ReceiverImpl) MaxSpatial() int32 { return r.maxL.Load() }

// AddLayer implements Receiver. buf may be nil (a buffer is created).
func (r *ReceiverImpl) AddLayer(spatial int32, src RTPSource, buf *buffer.Buffer) error {
	if spatial < 0 || spatial >= types.MaxSpatialLayers {
		return errs.New(errs.CodeInvalidArgument, "bad spatial layer %d", spatial)
	}
	if buf == nil {
		slots := r.p.BufferSlots
		clock := r.p.Codec.ClockRate
		if slots == 0 {
			slots = 500
			if r.p.Kind == webrtc.RTPCodecTypeAudio {
				slots = 100
			}
		}
		buf = buffer.New(buffer.Config{Slots: slots, ClockRate: clock, MimeType: r.p.Codec.MimeType, AudioLevelExtID: r.p.AudioLevelExtID, Spatial: spatial})
	}
	l := &layer{spatial: spatial, src: src, buf: buf, ssrc: src.SSRC(), rid: src.RID()}
	if !r.layers[spatial].CompareAndSwap(nil, l) {
		return errs.New(errs.CodeAlreadyExists, "layer %d exists", spatial)
	}
	for {
		m := r.maxL.Load()
		if spatial <= m || r.maxL.CompareAndSwap(m, spatial) {
			break
		}
	}
	for _, dt := range *r.downs.Load() {
		if d, ok := dt.(*DownTrackImpl); ok {
			d.fwd.SetAvailableLayers(r.maxL.Load())
		}
	}
	r.wg.Add(1)
	go r.readLoop(l)
	return nil
}

// readLoop is the per-layer forwarding goroutine (ARCHITECTURE.md 4.1).
func (r *ReceiverImpl) readLoop(l *layer) {
	defer r.wg.Done()
	scratch := make([]byte, buffer.MaxPacketSize)
	nackBuf := make([]uint16, 0, 64)
	nackPkt := &rtcp.TransportLayerNack{MediaSSRC: l.ssrc}
	nackPkts := []rtcp.Packet{nackPkt}
	var lastNack int64
	for {
		n, err := l.src.Read(scratch)
		if err != nil {
			if errors.Is(err, io.EOF) || r.closed.Load() {
				break
			}
			if isTimeout(err) {
				continue
			}
			break
		}
		now := Now()
		ext, err := l.buf.Write(scratch[:n], now)
		if err != nil {
			continue
		}
		l.meter.Add(n)
		if ext.HasAudioLevel {
			r.audio.observe(ext.AudioLevel, ext.VoiceActivity)
		}
		r.fanout(ext, l.spatial)
		if now-lastNack >= 20e6 {
			lastNack = now
			nackBuf = l.buf.MissingSequenceNumbers(nackBuf[:0], now)
			if len(nackBuf) > 0 && r.p.WriteRTCP != nil {
				nackPkt.Nacks = rtcp.NackPairsFromSequenceNumbers(nackBuf)
				if r.p.WriteRTCP(nackPkts) == nil {
					l.nacks.Inc()
					stats.NACKsSent.Inc()
				}
			}
		}
	}
	r.Close()
}

// fanout forwards one packet to every DownTrack inline, preserving
// per-downtrack order.
func (r *ReceiverImpl) fanout(ext *buffer.ExtPacket, spatial int32) {
	for _, dt := range *r.downs.Load() {
		_ = dt.WriteRTP(ext, spatial)
	}
}

type timeoutError interface{ Timeout() bool }

func isTimeout(err error) bool {
	te, ok := err.(timeoutError)
	return ok && te.Timeout()
}

// testHookAddDownTrack runs between AddDownTrack's fast closed check and
// taking dtMu (tests use it to force the Close race).
var testHookAddDownTrack = func() {}

// AddDownTrack implements Receiver.
func (r *ReceiverImpl) AddDownTrack(dt DownTrack) error {
	if r.closed.Load() {
		return errs.New(errs.CodeNotFound, "receiver closed")
	}
	if d, ok := dt.(*DownTrackImpl); ok {
		d.fwd.SetAvailableLayers(r.maxL.Load())
	}
	testHookAddDownTrack()
	r.dtMu.Lock()
	defer r.dtMu.Unlock()
	// Re-check under dtMu: Close sets closed before it takes dtMu to empty
	// the list, so a DownTrack added after that point would never be closed.
	if r.closed.Load() {
		return errs.New(errs.CodeNotFound, "receiver closed")
	}
	old := *r.downs.Load()
	next := make([]DownTrack, 0, len(old)+1)
	for _, d := range old {
		if d.SubscriberID() != dt.SubscriberID() {
			next = append(next, d)
		}
	}
	next = append(next, dt)
	r.downs.Store(&next)
	return nil
}

// RemoveDownTrack implements Receiver.
func (r *ReceiverImpl) RemoveDownTrack(subscriberID string) {
	r.dtMu.Lock()
	defer r.dtMu.Unlock()
	old := *r.downs.Load()
	next := make([]DownTrack, 0, len(old))
	for _, d := range old {
		if d.SubscriberID() != subscriberID {
			next = append(next, d)
		}
	}
	r.downs.Store(&next)
}

// DownTracks returns the attached DownTracks.
func (r *ReceiverImpl) DownTracks() []DownTrack { return *r.downs.Load() }

// SendPLI implements Receiver.
func (r *ReceiverImpl) SendPLI(spatial int32, force bool) {
	if spatial < 0 || spatial >= types.MaxSpatialLayers || r.p.WriteRTCP == nil {
		return
	}
	l := r.layers[spatial].Load()
	if l == nil {
		return
	}
	now := Now()
	last := l.lastPLI.Load()
	if !force && now-last < int64(r.p.PLIThrottle) {
		return
	}
	if !l.lastPLI.CompareAndSwap(last, now) {
		return
	}
	if r.p.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: l.ssrc}}) == nil {
		l.plis.Inc()
		stats.PLIsSent.Inc()
	}
}

// ReadPacket implements Receiver.
func (r *ReceiverImpl) ReadPacket(dst []byte, spatial int32, sn uint16) (int, error) {
	if spatial < 0 || spatial >= types.MaxSpatialLayers {
		return 0, errs.ErrNotFound
	}
	l := r.layers[spatial].Load()
	if l == nil {
		return 0, errs.ErrNotFound
	}
	return l.buf.GetPacket(dst, sn)
}

// Buffer returns the buffer of a layer (nil if absent).
func (r *ReceiverImpl) Buffer(spatial int32) *buffer.Buffer {
	if l := r.layers[spatial].Load(); l != nil {
		return l.buf
	}
	return nil
}

// Bitrates implements Receiver (temporal layers are not split: every
// temporal entry holds the spatial layer's bitrate).
func (r *ReceiverImpl) Bitrates() types.Bitrates {
	var b types.Bitrates
	now := time.Now()
	for s := range types.MaxSpatialLayers {
		if l := r.layers[s].Load(); l != nil {
			_, bps := l.meter.Rates(now)
			for t := range types.MaxTemporalLayers {
				b[s][t] = int64(bps)
			}
		}
	}
	return b
}

// OnMaxSubscribedLayerChanged implements Receiver.
func (r *ReceiverImpl) OnMaxSubscribedLayerChanged(f func(spatial int32)) { r.onMaxSub = f }

// OnClosed registers a callback invoked once when the receiver closes
// (publisher track ended).
func (r *ReceiverImpl) OnClosed(f func()) { r.onClosed = f }

// HandleSenderReport records a publisher SR for a layer SSRC.
func (r *ReceiverImpl) HandleSenderReport(sr *rtcp.SenderReport) {
	for s := range types.MaxSpatialLayers {
		if l := r.layers[s].Load(); l != nil && l.ssrc == sr.SSRC {
			l.srNTP.Store(sr.NTPTime)
			l.srRTP.Store(sr.RTPTime)
			l.srArrival.Store(Now())
		}
	}
}

// Close implements Receiver: stops forwarding and closes DownTracks.
func (r *ReceiverImpl) Close() {
	r.closeOnce.Do(func() {
		r.closed.Store(true)
		r.dtMu.Lock()
		downs := *r.downs.Load()
		empty := []DownTrack{}
		r.downs.Store(&empty)
		r.dtMu.Unlock()
		for _, d := range downs {
			d.Close()
		}
		if r.onClosed != nil {
			go r.onClosed()
		}
	})
}

// ReadLevel returns the smoothed level (0..1) and voice ratio, resetting
// the observation window (speaker.LevelSource).
func (r *ReceiverImpl) ReadLevel() (level, activeRatio float64) { return r.audio.read() }

// LayerStats is per-layer receive statistics.
type LayerStats struct {
	Spatial    int32
	SSRC       uint32
	RID        string
	BitrateBps float64
	PPS        float64
	Buffer     buffer.Stats
	PLIs       uint64
	NACKs      uint64
}

// LayerStats returns statistics for all present layers.
func (r *ReceiverImpl) LayerStats(now time.Time) []LayerStats {
	var out []LayerStats
	for s := range types.MaxSpatialLayers {
		l := r.layers[s].Load()
		if l == nil {
			continue
		}
		pps, bps := l.meter.Rates(now)
		out = append(out, LayerStats{Spatial: int32(s), SSRC: l.ssrc, RID: l.rid, BitrateBps: bps, PPS: pps, Buffer: l.buf.Stats(), PLIs: l.plis.Load(), NACKs: l.nacks.Load()})
	}
	return out
}

// audioLevel is a lock-free accumulator of RFC 6464 levels.
type audioLevel struct {
	sum    atomic.Uint64 // sum of linearised levels * 1000
	count  atomic.Uint64
	voiced atomic.Uint64
}

func (a *audioLevel) observe(level uint8, voice bool) {
	// 0 dBov = 1.0, 127 = silence; map linearly in dB over 0..60.
	v := uint64(0)
	if level < 60 {
		v = uint64(60-level) * 1000 / 60
	}
	a.sum.Add(v)
	a.count.Add(1)
	if voice || level < 50 {
		a.voiced.Add(1)
	}
}

func (a *audioLevel) read() (float64, float64) {
	n := a.count.Swap(0)
	s := a.sum.Swap(0)
	v := a.voiced.Swap(0)
	if n == 0 {
		return 0, 0
	}
	return float64(s) / float64(n) / 1000, float64(v) / float64(n)
}

// TrackRemoteSource adapts a Pion TrackRemote to RTPSource.
type TrackRemoteSource struct{ T *webrtc.TrackRemote }

// Read implements RTPSource.
func (s TrackRemoteSource) Read(b []byte) (int, error) {
	n, _, err := s.T.Read(b)
	return n, err
}

// SSRC implements RTPSource.
func (s TrackRemoteSource) SSRC() uint32 { return uint32(s.T.SSRC()) }

// RID implements RTPSource.
func (s TrackRemoteSource) RID() string { return s.T.RID() }

// ReceptionReports builds RTCP reception reports for every layer (RR towards
// the publisher). It must be called from a single goroutine (the RTCP ticker).
func (r *ReceiverImpl) ReceptionReports() []rtcp.ReceptionReport {
	var out []rtcp.ReceptionReport
	now := Now()
	for s := range types.MaxSpatialLayers {
		l := r.layers[s].Load()
		if l == nil {
			continue
		}
		st := l.buf.Stats()
		if st.PacketsReceived == 0 {
			continue
		}
		expInt := st.Expected - l.rrExpected
		recvInt := st.PacketsReceived - l.rrReceived
		l.rrExpected, l.rrReceived = st.Expected, st.PacketsReceived
		var frac uint8
		if expInt > recvInt && expInt > 0 {
			frac = uint8((expInt - recvInt) * 256 / expInt)
		}
		rr := rtcp.ReceptionReport{
			SSRC: l.ssrc, FractionLost: frac, TotalLost: uint32(min(st.PacketsLost, 0x7fffff)),
			LastSequenceNumber: uint32(st.HighestExtSN - 1<<16), Jitter: uint32(st.Jitter),
		}
		if at := l.srArrival.Load(); at != 0 {
			rr.LastSenderReport = uint32(l.srNTP.Load() >> 16)
			rr.Delay = uint32((now - at) * 65536 / 1e9)
		}
		out = append(out, rr)
	}
	return out
}
