package rtc

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"

	"github.com/KeshavGoyal26/tessera/pkg/errs"
	"github.com/KeshavGoyal26/tessera/pkg/rtc/buffer"
	"github.com/KeshavGoyal26/tessera/pkg/rtc/bwe"
	"github.com/KeshavGoyal26/tessera/pkg/rtc/forward"
	"github.com/KeshavGoyal26/tessera/pkg/stats"
)

// DownTrackParams configure a DownTrack.
type DownTrackParams struct {
	// SubscriberID is the subscribing participant SID.
	SubscriberID string
	Receiver     *ReceiverImpl
	// TrackID/StreamID announced in the subscriber SDP (msid).
	TrackID  string
	StreamID string
	// TWCC is the subscriber connection's transport-wide CC state (nil = off).
	TWCC *TWCCSender
	// Priority for bandwidth allocation (higher first).
	Priority uint32
}

// TWCCSender is shared by all DownTracks of one subscriber PeerConnection:
// it numbers packets with transport-wide sequence numbers and feeds the
// estimator.
type TWCCSender struct {
	seq atomic.Uint32
	Est bwe.Estimator
}

// DownTrackImpl is the DownTrack implementation.
type DownTrackImpl struct {
	p      DownTrackParams
	r      *ReceiverImpl
	fwd    *forward.Forwarder
	mime   string
	kind   webrtc.RTPCodecType
	bound  atomic.Bool
	closed atomic.Bool

	mu     sync.Mutex // serialises media writes with retransmissions and padding
	writer webrtc.TrackLocalWriter
	ssrc   uint32
	pt     uint8
	hdr    rtp.Header
	rtx    []byte
	twccID uint8
	twccB  [2]byte
	prio   atomic.Uint32
	// Forwarding stops while the subscriber disabled the track or the bandwidth
	// allocator paused it (guarded by pauseMu).
	pauseMu               sync.Mutex
	disabled, allocPaused bool
	// lastMarker: the last forwarded packet ended a frame, so padding can be
	// inserted without splitting a frame (guarded by mu).
	lastMarker bool

	firstPkt   atomic.Bool
	onFirst    func()
	onBind     func()
	meter      stats.Meter
	nacks      stats.Counter
	plis       stats.Counter
	retx       stats.Counter
	rrLost     atomic.Uint32
	rrFraction atomic.Uint32
	redStrip   bool // guarded by mu: send RED as its primary Opus frame
	rrJitter   atomic.Uint32
	rttMs      atomic.Uint32
}

// NewDownTrack creates a DownTrack for a Receiver.
func NewDownTrack(p DownTrackParams) *DownTrackImpl {
	c := p.Receiver.Codec()
	d := &DownTrackImpl{
		p: p, r: p.Receiver, fwd: forward.NewForwarder(c.MimeType),
		mime: c.MimeType, kind: p.Receiver.Kind(), rtx: make([]byte, buffer.MaxPacketSize),
	}
	if c.ClockRate != 0 {
		d.fwd.SetClockRate(c.ClockRate)
	}
	d.prio.Store(p.Priority)
	return d
}

// Bind implements webrtc.TrackLocal.
func (d *DownTrackImpl) Bind(ctx webrtc.TrackLocalContext) (webrtc.RTPCodecParameters, error) {
	want := d.r.Codec()
	strip := false
	if strings.EqualFold(want.MimeType, MimeTypeRED) && !hasCodec(ctx.CodecParameters(), MimeTypeRED) {
		// The subscriber cannot take RED (Firefox, the recorder, Go/Java clients):
		// send the primary Opus frame of each packet.
		want.MimeType, strip = webrtc.MimeTypeOpus, true
	}
	var chosen *webrtc.RTPCodecParameters
	for _, c := range ctx.CodecParameters() {
		if !strings.EqualFold(c.MimeType, want.MimeType) {
			continue
		}
		cc := c
		if chosen == nil {
			chosen = &cc
		}
		if strings.EqualFold(want.MimeType, webrtc.MimeTypeH264) && fmtpProfile(c.SDPFmtpLine) == fmtpProfile(want.SDPFmtpLine) {
			chosen = &cc
			break
		}
	}
	if chosen == nil {
		return webrtc.RTPCodecParameters{}, webrtc.ErrUnsupportedCodec
	}
	d.mu.Lock()
	d.writer = ctx.WriteStream()
	d.ssrc = uint32(ctx.SSRC())
	d.pt = uint8(chosen.PayloadType)
	d.redStrip = strip
	if lg := d.r.p.Logger; lg != nil {
		lg.Debug("downtrack bound", "track", d.p.TrackID, "subscriber", d.p.SubscriberID, "want", d.r.Codec().MimeType,
			"codec", chosen.MimeType, "payload_type", chosen.PayloadType, "red_strip", strip, "offered", len(ctx.CodecParameters()))
	}
	if d.p.TWCC != nil {
		for _, e := range ctx.HeaderExtensions() {
			if e.URI == extTWCC {
				d.twccID = uint8(e.ID)
			}
		}
	}
	d.mu.Unlock()
	d.bound.Store(true)
	if d.kind == webrtc.RTPCodecTypeVideo {
		d.r.SendPLI(true)
	}
	if d.onBind != nil {
		go d.onBind()
	}
	return *chosen, nil
}

func fmtpProfile(fmtp string) string {
	for _, kv := range strings.Split(fmtp, ";") {
		if k, v, ok := strings.Cut(strings.TrimSpace(kv), "="); ok && k == "profile-level-id" {
			return strings.ToLower(v)[:min(len(v), 4)]
		}
	}
	return ""
}

// Unbind implements webrtc.TrackLocal.
func (d *DownTrackImpl) Unbind(webrtc.TrackLocalContext) error {
	d.bound.Store(false)
	return nil
}

// ID implements webrtc.TrackLocal.
func (d *DownTrackImpl) ID() string { return d.p.TrackID }

// RID implements webrtc.TrackLocal.
func (d *DownTrackImpl) RID() string { return "" }

// StreamID implements webrtc.TrackLocal.
func (d *DownTrackImpl) StreamID() string { return d.p.StreamID }

// Kind implements webrtc.TrackLocal.
func (d *DownTrackImpl) Kind() webrtc.RTPCodecType { return d.kind }

// SubscriberID implements DownTrack.
func (d *DownTrackImpl) SubscriberID() string { return d.p.SubscriberID }

// Receiver returns the source Receiver.
func (d *DownTrackImpl) Receiver() *ReceiverImpl { return d.r }

// OnFirstPacket registers a callback for the first forwarded packet.
func (d *DownTrackImpl) OnFirstPacket(f func()) { d.onFirst = f }

// OnBind registers a callback invoked when the transceiver is negotiated.
func (d *DownTrackImpl) OnBind(f func()) { d.onBind = f }

// WriteRTP implements DownTrack.
func (d *DownTrackImpl) WriteRTP(pkt *buffer.ExtPacket) error {
	if !d.bound.Load() || d.closed.Load() {
		return nil
	}
	tp, err := d.fwd.GetTranslation(pkt)
	if err != nil {
		return err
	}
	if tp.Drop {
		if tp.RequestKeyFrame {
			d.r.SendPLI(false)
		}
		return nil
	}
	d.mu.Lock()
	payload := pkt.Payload
	if d.redStrip {
		p, ok := redPrimary(payload)
		if !ok {
			d.mu.Unlock()
			return nil
		}
		payload = p
	}
	err = d.writeLocked(tp.SequenceNumber, tp.Timestamp, pkt.Marker, payload, false)
	if err == nil && !pkt.Late {
		// Only the newest packet tells whether the stream is between frames: a late one
		// (recovered after loss) that ended an older frame would let probe padding split
		// the frame being sent, which the subscriber then cannot assemble.
		d.lastMarker = pkt.Marker
	}
	d.mu.Unlock()
	if err == nil {
		d.meter.Add(len(payload) + 12)
		if !d.firstPkt.Load() && d.firstPkt.CompareAndSwap(false, true) && d.onFirst != nil {
			go d.onFirst()
		}
	}
	return err
}

func (d *DownTrackImpl) writeLocked(sn uint16, ts uint32, marker bool, payload []byte, padding bool) error {
	h := &d.hdr
	h.Version = 2
	h.Padding, h.PaddingSize = padding, 0
	size := len(payload) + 20
	if padding {
		h.PaddingSize = 255
		size = bwe.PaddingPacketBytes
	}
	h.PayloadType = d.pt
	h.SequenceNumber = sn
	h.Timestamp = ts
	h.Marker = marker
	h.SSRC = d.ssrc
	if d.twccID != 0 {
		seq := uint16(d.p.TWCC.seq.Add(1))
		d.twccB[0], d.twccB[1] = byte(seq>>8), byte(seq)
		_ = h.SetExtension(d.twccID, d.twccB[:])
		d.p.TWCC.Est.OnPacketSent(seq, size, Now(), padding)
	}
	_, err := d.writer.WriteRTP(h, payload)
	h.Padding, h.PaddingSize = false, 0
	return err
}

// WritePadding implements bwe.Padder: it sends up to n padding-only packets (255
// bytes each, no payload) on this video stream for bandwidth probing. Padding is
// only inserted between frames, and only when TWCC can measure it. Receivers drop
// padding after counting it; the sequence numbers stay continuous.
func (d *DownTrackImpl) WritePadding(n int) int {
	if n <= 0 || d.kind != webrtc.RTPCodecTypeVideo || !d.bound.Load() || d.closed.Load() {
		return 0
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.twccID == 0 || !d.lastMarker {
		return 0
	}
	sn, ts, err := d.fwd.PaddingParams(n)
	if err != nil {
		return 0
	}
	for i := range n {
		if d.writeLocked(sn+uint16(i), ts, false, nil, true) != nil {
			return i
		}
	}
	stats.PaddingPackets.Add(uint64(n))
	return n
}

// AllocID returns the track SID used by the allocator.
func (d *DownTrackImpl) AllocID() string { return d.p.TrackID }

// IsVideo reports whether this is a video DownTrack.
func (d *DownTrackImpl) IsVideo() bool { return d.kind == webrtc.RTPCodecTypeVideo }

// Priority returns the allocation priority.
func (d *DownTrackImpl) Priority() uint32 { return d.prio.Load() }

// SetPriority sets the allocation priority.
func (d *DownTrackImpl) SetPriority(p uint32) { d.prio.Store(p) }

// Bitrate returns the publisher's measured bitrate (bits per second).
func (d *DownTrackImpl) Bitrate() int64 { return d.r.Bitrate() }

// SetDisabled implements DownTrack: the subscriber stops or resumes the track.
func (d *DownTrackImpl) SetDisabled(disabled bool) {
	d.pauseMu.Lock()
	d.disabled = disabled
	d.applyPausedLocked()
	d.pauseMu.Unlock()
}

// Wanted reports whether the subscriber wants the track (the allocator only
// spends bandwidth on wanted tracks).
func (d *DownTrackImpl) Wanted() bool {
	d.pauseMu.Lock()
	defer d.pauseMu.Unlock()
	return !d.disabled
}

// SetAllocPaused applies the bandwidth allocator's decision.
func (d *DownTrackImpl) SetAllocPaused(paused bool) {
	d.pauseMu.Lock()
	d.allocPaused = paused
	d.applyPausedLocked()
	d.pauseMu.Unlock()
}

func (d *DownTrackImpl) applyPausedLocked() {
	paused := d.disabled || d.allocPaused
	if d.fwd.Paused() == paused {
		return
	}
	d.fwd.SetPaused(paused)
	if !paused && d.bound.Load() && d.kind == webrtc.RTPCodecTypeVideo {
		d.r.SendPLI(false) // video resumes on a keyframe
	}
}

// Forwarder exposes the forwarder (tests).
func (d *DownTrackImpl) Forwarder() *forward.Forwarder { return d.fwd }

// Mute implements DownTrack.
func (d *DownTrackImpl) Mute(muted bool) {
	d.fwd.Mute(muted)
	if !muted && d.kind == webrtc.RTPCodecTypeVideo {
		d.r.SendPLI(true)
	}
}

// HandleRTCP implements DownTrack.
func (d *DownTrackImpl) HandleRTCP(pkts []rtcp.Packet) {
	for _, p := range pkts {
		switch p := p.(type) {
		case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
			d.plis.Inc()
			stats.PLIsReceived.Inc()
			if d.fwd.Forwarding() {
				d.r.SendPLI(false)
			}
		case *rtcp.TransportLayerNack:
			d.nacks.Inc()
			stats.NACKsReceived.Inc()
			for _, pair := range p.Nacks {
				pair.Range(func(sn uint16) bool {
					d.retransmit(sn)
					return true
				})
			}
		case *rtcp.TransportLayerCC:
			if d.p.TWCC != nil {
				d.p.TWCC.Est.OnTransportFeedback(p, time.Now())
			}
		case *rtcp.ReceiverReport:
			for _, rr := range p.Reports {
				if rr.SSRC != d.ssrc {
					continue
				}
				d.rrLost.Store(rr.TotalLost)
				d.rrFraction.Store(uint32(rr.FractionLost))
				d.rrJitter.Store(rr.Jitter)
				if rr.LastSenderReport != 0 {
					now := ntpNow() >> 16
					rtt := uint32(now) - rr.LastSenderReport - rr.Delay
					if ms := uint32(uint64(rtt) * 1000 >> 16); ms < 10000 {
						d.rttMs.Store(ms)
					}
				}
			}
		}
	}
}

func (d *DownTrackImpl) retransmit(sn uint16) {
	minInt := int64(d.rttMs.Load()) * 8e5 // 0.8 * RTT in ns
	info, ok := d.fwd.RetransmitInfo(sn, Now(), minInt)
	if !ok {
		return
	}
	buf := d.r.Buffer()
	if buf == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	n, err := buf.GetPacket(d.rtx, info.Original)
	if err != nil {
		return
	}
	var e buffer.ExtPacket
	if buf.ParseExtPacket(d.rtx[:n], &e) != nil {
		return
	}
	payload := e.Payload
	if d.redStrip { // same as WriteRTP: a non-RED subscriber gets the primary Opus frame
		p, ok := redPrimary(payload)
		if !ok {
			return
		}
		payload = p
	}
	if d.writeLocked(sn, info.Timestamp, e.Marker, payload, false) == nil {
		d.retx.Inc()
		stats.Retransmits.Inc()
	}
}

func hasCodec(cs []webrtc.RTPCodecParameters, mime string) bool {
	for _, c := range cs {
		if strings.EqualFold(c.MimeType, mime) {
			return true
		}
	}
	return false
}

// redPrimary returns the primary (last) block of an RFC 2198 RED payload. Each
// redundant block has a 4-byte header with a 10-bit length; the final header is
// one byte and its block runs to the end. ok is false for a malformed payload.
func redPrimary(b []byte) (primary []byte, ok bool) {
	i, skip := 0, 0
	for {
		if i >= len(b) {
			return nil, false
		}
		if b[i]&0x80 == 0 {
			i++
			break
		}
		if i+4 > len(b) {
			return nil, false
		}
		skip += int(b[i+2]&0x03)<<8 | int(b[i+3])
		i += 4
	}
	if i+skip > len(b) {
		return nil, false
	}
	return b[i+skip:], true
}

// ReportedFractionLost is the loss fraction (0-255) in the subscriber's last
// receiver report for this track.
func (d *DownTrackImpl) ReportedFractionLost() uint8 { return uint8(d.rrFraction.Load()) }

// SenderReport returns an RTCP SR for this DownTrack derived from the
// publisher's last SR, translated through the
// timestamp offset. Called by the subscriber's RTCP ticker.
func (d *DownTrackImpl) SenderReport() *rtcp.SenderReport {
	if !d.bound.Load() {
		return nil
	}
	ly := d.r.st.Load()
	if ly == nil || ly.srArrival.Load() == 0 {
		return nil
	}
	off, ok := d.fwd.TSOffset()
	if !ok {
		return nil
	}
	clock := d.r.Codec().ClockRate
	if clock == 0 {
		clock = 90000
	}
	// Advance the publisher's RTP mapping to now, stamped with this server's
	// clock: the subscriber's RR echoes the NTP time back (LSR), and RTT is
	// computed against ntpNow. Using the publisher's NTP clock here made that
	// subtraction meaningless (any clock offset underflowed it), so RTT was 0.
	elapsed := time.Duration(Now() - ly.srArrival.Load())
	ntp := ntpNow()
	rtpTS := ly.srRTP.Load() + uint32(elapsed.Seconds()*float64(clock)) - off
	return &rtcp.SenderReport{SSRC: d.ssrc, NTPTime: ntp, RTPTime: rtpTS,
		PacketCount: uint32(d.meter.Packets()), OctetCount: uint32(d.meter.Bytes())}
}

// DownTrackStats are DownTrack statistics.
type DownTrackStats struct {
	BitrateBps               float64
	Packets                  uint64
	PacketsLost              uint64
	FractionLost             float64
	JitterMs                 float64
	RTTMs                    float64
	NACKs, PLIs, Retransmits uint64
	// Restarts counts video (re)starts on a keyframe (start, resume, unmute).
	Restarts uint64
	Paused   bool
	Muted    bool
}

// Stats returns DownTrack statistics.
func (d *DownTrackImpl) Stats(now time.Time) DownTrackStats {
	_, bps := d.meter.Rates(now)
	clock := float64(d.r.Codec().ClockRate)
	if clock == 0 {
		clock = 90000
	}
	return DownTrackStats{
		BitrateBps: bps, Packets: d.meter.Packets(), PacketsLost: uint64(d.rrLost.Load()),
		FractionLost: float64(d.rrFraction.Load()) * 100 / 256, JitterMs: float64(d.rrJitter.Load()) * 1000 / clock,
		RTTMs: float64(d.rttMs.Load()), NACKs: d.nacks.Load(), PLIs: d.plis.Load(), Retransmits: d.retx.Load(),
		Restarts: d.fwd.Restarts(), Paused: d.fwd.Paused(),
		Muted: d.fwd.Muted(),
	}
}

// Close implements DownTrack.
func (d *DownTrackImpl) Close() {
	if d.closed.CompareAndSwap(false, true) {
		d.r.RemoveDownTrack(d.p.SubscriberID)
	}
}

// Closed reports whether Close was called.
func (d *DownTrackImpl) Closed() bool { return d.closed.Load() }

var errNotBound = errs.New(errs.CodeFailedPrecondition, "downtrack not bound")

var _ = errNotBound

func ntpNow() uint64 {
	now := time.Now()
	secs := uint64(now.Unix()) + 2208988800
	frac := uint64(now.Nanosecond()) * (1 << 32) / 1e9
	return secs<<32 | frac
}
