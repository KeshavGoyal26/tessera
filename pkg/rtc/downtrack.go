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
	"github.com/KeshavGoyal26/tessera/pkg/rtc/simulcast"
	"github.com/KeshavGoyal26/tessera/pkg/rtc/types"
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
	fwd    *simulcast.Forwarder
	isVP8  bool
	mime   string
	kind   webrtc.RTPCodecType
	bound  atomic.Bool
	closed atomic.Bool

	mu     sync.Mutex // serialises writes from several layer goroutines
	writer webrtc.TrackLocalWriter
	ssrc   uint32
	pt     uint8
	hdr    rtp.Header
	pay    []byte
	rtx    []byte
	twccID uint8
	twccB  [2]byte
	prio   atomic.Uint32

	firstPkt   atomic.Bool
	onFirst    func()
	onBind     func()
	meter      stats.Meter
	nacks      stats.Counter
	plis       stats.Counter
	retx       stats.Counter
	rrLost     atomic.Uint32
	rrFraction atomic.Uint32
	rrJitter   atomic.Uint32
	rttMs      atomic.Uint32
}

// NewDownTrack creates a DownTrack for a Receiver.
func NewDownTrack(p DownTrackParams) *DownTrackImpl {
	c := p.Receiver.Codec()
	d := &DownTrackImpl{
		p: p, r: p.Receiver, fwd: simulcast.NewForwarder(c.MimeType),
		isVP8: strings.EqualFold(c.MimeType, webrtc.MimeTypeVP8), mime: c.MimeType, kind: p.Receiver.Kind(),
		pay: make([]byte, buffer.MaxPacketSize+8), rtx: make([]byte, buffer.MaxPacketSize),
	}
	if c.ClockRate != 0 {
		d.fwd.SetClockRate(c.ClockRate)
	}
	d.prio.Store(p.Priority)
	d.fwd.SetAvailableLayers(p.Receiver.MaxSpatial())
	return d
}

// Bind implements webrtc.TrackLocal.
func (d *DownTrackImpl) Bind(ctx webrtc.TrackLocalContext) (webrtc.RTPCodecParameters, error) {
	want := d.r.Codec()
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
		d.r.SendPLI(d.fwd.TargetLayer().Spatial, true)
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
func (d *DownTrackImpl) WriteRTP(pkt *buffer.ExtPacket, layer int32) error {
	if !d.bound.Load() || d.closed.Load() {
		return nil
	}
	tp, err := d.fwd.GetTranslation(pkt, layer)
	if err != nil {
		return err
	}
	if tp.Drop {
		if tp.RequestKeyFrame {
			d.r.SendPLI(tp.KeyFrameLayer, false)
		}
		return nil
	}
	if tp.IsSwitch && d.kind == webrtc.RTPCodecTypeVideo {
		stats.LayerSwitches.Inc()
	}
	d.mu.Lock()
	payload := pkt.Payload
	if tp.HasVP8 {
		n := simulcast.RewriteVP8(d.pay, pkt.Payload, &pkt.VP8, &tp.VP8)
		payload = d.pay[:n]
	}
	err = d.writeLocked(tp.SequenceNumber, tp.Timestamp, pkt.Marker, payload)
	d.mu.Unlock()
	if err == nil {
		d.meter.Add(len(payload) + 12)
		if !d.firstPkt.Load() && d.firstPkt.CompareAndSwap(false, true) && d.onFirst != nil {
			go d.onFirst()
		}
	}
	return err
}

func (d *DownTrackImpl) writeLocked(sn uint16, ts uint32, marker bool, payload []byte) error {
	h := &d.hdr
	h.Version = 2
	h.PayloadType = d.pt
	h.SequenceNumber = sn
	h.Timestamp = ts
	h.Marker = marker
	h.SSRC = d.ssrc
	if d.twccID != 0 {
		seq := uint16(d.p.TWCC.seq.Add(1))
		d.twccB[0], d.twccB[1] = byte(seq>>8), byte(seq)
		_ = h.SetExtension(d.twccID, d.twccB[:])
		d.p.TWCC.Est.OnPacketSent(seq, len(payload)+20, Now(), false)
	}
	_, err := d.writer.WriteRTP(h, payload)
	return err
}

// AllocID returns the track SID used by the allocator.
func (d *DownTrackImpl) AllocID() string { return d.p.TrackID }

// IsVideo reports whether this is a video DownTrack.
func (d *DownTrackImpl) IsVideo() bool { return d.kind == webrtc.RTPCodecTypeVideo }

// Priority returns the allocation priority.
func (d *DownTrackImpl) Priority() uint32 { return d.prio.Load() }

// SetPriority sets the allocation priority.
func (d *DownTrackImpl) SetPriority(p uint32) { d.prio.Store(p) }

// Bitrates returns the publisher's measured layer bitrates.
func (d *DownTrackImpl) Bitrates() types.Bitrates { return d.r.Bitrates() }

// SetMaxLayer implements DownTrack.
func (d *DownTrackImpl) SetMaxLayer(l types.VideoLayer) {
	d.fwd.SetMaxLayer(l)
	if d.bound.Load() && d.kind == webrtc.RTPCodecTypeVideo {
		if t := d.fwd.TargetLayer(); t.IsValid() && t != d.fwd.CurrentLayer() {
			d.r.SendPLI(t.Spatial, false)
		}
	}
}

// MaxLayer returns the subscriber preference.
func (d *DownTrackImpl) MaxLayer() types.VideoLayer { return d.fwd.MaxLayer() }

// SetTargetLayer implements DownTrack.
func (d *DownTrackImpl) SetTargetLayer(l types.VideoLayer) { d.fwd.SetTargetLayer(l) }

// TargetLayer returns the effective target layer.
func (d *DownTrackImpl) TargetLayer() types.VideoLayer { return d.fwd.TargetLayer() }

// CurrentLayer implements DownTrack.
func (d *DownTrackImpl) CurrentLayer() types.VideoLayer { return d.fwd.CurrentLayer() }

// Forwarder exposes the forwarder (tests, allocator).
func (d *DownTrackImpl) Forwarder() *simulcast.Forwarder { return d.fwd }

// Mute implements DownTrack.
func (d *DownTrackImpl) Mute(muted bool) {
	d.fwd.Mute(muted)
	if !muted && d.kind == webrtc.RTPCodecTypeVideo {
		d.r.SendPLI(d.fwd.TargetLayer().Spatial, true)
	}
}

// HandleRTCP implements DownTrack.
func (d *DownTrackImpl) HandleRTCP(pkts []rtcp.Packet) {
	for _, p := range pkts {
		switch p := p.(type) {
		case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
			d.plis.Inc()
			stats.PLIsReceived.Inc()
			if l := d.fwd.CurrentLayer(); l.IsValid() {
				d.r.SendPLI(l.Spatial, false)
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
	buf := d.r.Buffer(info.Layer)
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
	if d.isVP8 && e.VP8.PictureIDPresent {
		m := simulcast.RewriteVP8(d.pay, e.Payload, &e.VP8, &info.VP8)
		payload = d.pay[:m]
	}
	if d.writeLocked(sn, info.Timestamp, e.Marker, payload) == nil {
		d.retx.Inc()
		stats.Retransmits.Inc()
	}
}

// SenderReport returns an RTCP SR for this DownTrack derived from the
// publisher's last SR of the current layer, translated through the
// timestamp offset. Called by the subscriber's RTCP ticker.
func (d *DownTrackImpl) SenderReport() *rtcp.SenderReport {
	if !d.bound.Load() {
		return nil
	}
	l := d.fwd.CurrentLayer()
	spatial := l.Spatial
	if d.kind == webrtc.RTPCodecTypeAudio {
		spatial = 0
	}
	if spatial < 0 {
		return nil
	}
	ly := d.r.layers[spatial].Load()
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
	// Advance the publisher's mapping to now.
	elapsed := time.Duration(Now() - ly.srArrival.Load())
	ntp := ly.srNTP.Load() + uint64(elapsed.Seconds()*float64(1<<32))
	rtpTS := ly.srRTP.Load() + uint32(elapsed.Seconds()*float64(clock)) - off
	return &rtcp.SenderReport{SSRC: d.ssrc, NTPTime: ntp, RTPTime: rtpTS,
		PacketCount: uint32(d.meter.Packets()), OctetCount: uint32(d.meter.Bytes())}
}

// DownTrackStats are DownTrack statistics.
type DownTrackStats struct {
	BitrateBps                float64
	Packets                   uint64
	PacketsLost               uint64
	FractionLost              float64
	JitterMs                  float64
	RTTMs                     float64
	NACKs, PLIs, Retransmits  uint64
	LayerSwitches             uint64
	Current, Target, MaxLayer types.VideoLayer
	Muted                     bool
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
		LayerSwitches: d.fwd.Switches(), Current: d.fwd.CurrentLayer(), Target: d.fwd.TargetLayer(), MaxLayer: d.fwd.MaxLayer(),
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
