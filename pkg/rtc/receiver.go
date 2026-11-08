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
	// PLIThrottle is the minimum interval between PLIs.
	PLIThrottle time.Duration
	// BufferSlots overrides the ring size.
	BufferSlots int
	Logger      *slog.Logger
}

// stream is the track's incoming RTP stream (one per track: no simulcast).
type stream struct {
	src     RTPSource
	buf     *buffer.Buffer
	ssrc    uint32
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
	p  ReceiverParams
	st atomic.Pointer[stream]

	dtMu      sync.Mutex
	downs     atomic.Pointer[[]DownTrack]
	closed    atomic.Bool
	wg        sync.WaitGroup
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
	r := &ReceiverImpl{p: p}
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

// Start implements Receiver: it attaches the track's stream and starts its
// forwarding goroutine. buf may be nil (a buffer is created).
func (r *ReceiverImpl) Start(src RTPSource, buf *buffer.Buffer) error {
	if buf == nil {
		slots := r.p.BufferSlots
		if slots == 0 {
			slots = 500
			if r.p.Kind == webrtc.RTPCodecTypeAudio {
				slots = 100
			}
		}
		buf = buffer.New(buffer.Config{Slots: slots, ClockRate: r.p.Codec.ClockRate, MimeType: r.p.Codec.MimeType, AudioLevelExtID: r.p.AudioLevelExtID})
	}
	st := &stream{src: src, buf: buf, ssrc: src.SSRC()}
	if !r.st.CompareAndSwap(nil, st) {
		return errs.New(errs.CodeAlreadyExists, "track %s already has a stream", r.p.TrackID)
	}
	r.wg.Add(1)
	go r.readLoop(st)
	return nil
}

// readLoop is the forwarding goroutine (ARCHITECTURE.md 4.1).
func (r *ReceiverImpl) readLoop(st *stream) {
	defer r.wg.Done()
	scratch := make([]byte, buffer.MaxPacketSize)
	nackBuf := make([]uint16, 0, 64)
	nackPkt := &rtcp.TransportLayerNack{MediaSSRC: st.ssrc}
	nackPkts := []rtcp.Packet{nackPkt}
	var lastNack int64
	for {
		n, err := st.src.Read(scratch)
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
		ext, err := st.buf.Write(scratch[:n], now)
		if err != nil {
			continue
		}
		st.meter.Add(n)
		if ext.HasAudioLevel {
			r.audio.observe(ext.AudioLevel, ext.VoiceActivity)
		}
		r.fanout(ext)
		if now-lastNack >= 20e6 {
			lastNack = now
			nackBuf = st.buf.MissingSequenceNumbers(nackBuf[:0], now)
			if len(nackBuf) > 0 && r.p.WriteRTCP != nil {
				nackPkt.Nacks = rtcp.NackPairsFromSequenceNumbers(nackBuf)
				if r.p.WriteRTCP(nackPkts) == nil {
					st.nacks.Inc()
					stats.NACKsSent.Inc()
				}
			}
		}
	}
	r.Close()
}

// fanout forwards one packet to every DownTrack inline, preserving
// per-downtrack order.
func (r *ReceiverImpl) fanout(ext *buffer.ExtPacket) {
	for _, dt := range *r.downs.Load() {
		_ = dt.WriteRTP(ext)
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
func (r *ReceiverImpl) SendPLI(force bool) {
	st := r.st.Load()
	if st == nil || r.p.WriteRTCP == nil {
		return
	}
	now := Now()
	last := st.lastPLI.Load()
	if !force && now-last < int64(r.p.PLIThrottle) {
		return
	}
	if !st.lastPLI.CompareAndSwap(last, now) {
		return
	}
	if r.p.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: st.ssrc}}) == nil {
		st.plis.Inc()
		stats.PLIsSent.Inc()
	}
}

// ReadPacket implements Receiver.
func (r *ReceiverImpl) ReadPacket(dst []byte, sn uint16) (int, error) {
	st := r.st.Load()
	if st == nil {
		return 0, errs.ErrNotFound
	}
	return st.buf.GetPacket(dst, sn)
}

// SetRTT passes the publisher round-trip time to the buffer (NACK pacing).
func (r *ReceiverImpl) SetRTT(d time.Duration) {
	if st := r.st.Load(); st != nil {
		st.buf.SetRTT(d)
	}
}

// Buffer returns the stream's buffer (nil before Start).
func (r *ReceiverImpl) Buffer() *buffer.Buffer {
	if st := r.st.Load(); st != nil {
		return st.buf
	}
	return nil
}

// Bitrate implements Receiver: the measured incoming bitrate in bits per second.
func (r *ReceiverImpl) Bitrate() int64 {
	st := r.st.Load()
	if st == nil {
		return 0
	}
	_, bps := st.meter.Rates(time.Now())
	return int64(bps)
}

// OnClosed registers a callback invoked once when the receiver closes
// (publisher track ended).
func (r *ReceiverImpl) OnClosed(f func()) { r.onClosed = f }

// HandleSenderReport records a publisher SR.
func (r *ReceiverImpl) HandleSenderReport(sr *rtcp.SenderReport) {
	if st := r.st.Load(); st != nil && st.ssrc == sr.SSRC {
		st.srNTP.Store(sr.NTPTime)
		st.srRTP.Store(sr.RTPTime)
		st.srArrival.Store(Now())
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

// StreamStats is receive statistics of the track's stream.
type StreamStats struct {
	SSRC       uint32
	BitrateBps float64
	PPS        float64
	Buffer     buffer.Stats
	PLIs       uint64
	NACKs      uint64
}

// StreamStats returns statistics of the stream; ok is false before Start.
func (r *ReceiverImpl) StreamStats(now time.Time) (StreamStats, bool) {
	st := r.st.Load()
	if st == nil {
		return StreamStats{}, false
	}
	pps, bps := st.meter.Rates(now)
	return StreamStats{SSRC: st.ssrc, BitrateBps: bps, PPS: pps, Buffer: st.buf.Stats(), PLIs: st.plis.Load(), NACKs: st.nacks.Load()}, true
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

func (r *ReceiverImpl) isAudio() bool {
	return strings.HasPrefix(strings.ToLower(r.p.Codec.MimeType), "audio/")
}

// maxSubscriberLoss is the highest loss fraction (0-255) subscribers report.
func (r *ReceiverImpl) maxSubscriberLoss() uint8 {
	var worst uint8
	if downs := r.downs.Load(); downs != nil {
		for _, d := range *downs {
			if l, ok := d.(interface{ ReportedFractionLost() uint8 }); ok {
				worst = max(worst, l.ReportedFractionLost())
			}
		}
	}
	return worst
}

// ReceptionReports builds the RTCP reception report towards the publisher. It
// must be called from a single goroutine (the RTCP ticker).
func (r *ReceiverImpl) ReceptionReports() []rtcp.ReceptionReport {
	st := r.st.Load()
	if st == nil {
		return nil
	}
	bs := st.buf.Stats()
	if bs.PacketsReceived == 0 {
		return nil
	}
	expInt := bs.Expected - st.rrExpected
	recvInt := bs.PacketsReceived - st.rrReceived
	st.rrExpected, st.rrReceived = bs.Expected, bs.PacketsReceived
	var frac uint8
	if expInt > recvInt && expInt > 0 {
		frac = uint8((expInt - recvInt) * 256 / expInt)
	}
	if r.isAudio() {
		// Opus sizes its in-band FEC from the loss its receiver reports. Here
		// that receiver is this server, which sees only the publisher's own
		// link; listeners behind lossy downlinks would get no FEC. Report the
		// worst loss any subscriber sees (as a direct call would).
		frac = max(frac, r.maxSubscriberLoss())
	}
	rr := rtcp.ReceptionReport{
		SSRC: st.ssrc, FractionLost: frac, TotalLost: uint32(min(bs.PacketsLost, 0x7fffff)),
		LastSequenceNumber: uint32(bs.HighestExtSN - 1<<16), Jitter: uint32(bs.Jitter),
	}
	if at := st.srArrival.Load(); at != 0 {
		rr.LastSenderReport = uint32(st.srNTP.Load() >> 16)
		rr.Delay = uint32((Now() - at) * 65536 / 1e9)
	}
	return []rtcp.ReceptionReport{rr}
}
