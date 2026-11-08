package forward

import (
	"testing"

	"github.com/pion/rtp"

	"github.com/KeshavGoyal26/tessera/pkg/rtc/buffer"
)

// H.264 payloads: a keyframe starts with SPS/PPS (STAP-A), then IDR and delta slices.
var (
	spsPPS = []byte{0x78, 0, 2, 0x67, 1, 0, 1, 0x68}
	delta  = []byte{0x41, 1}
)

// src generates H.264 packets through a real buffer (one packet per frame).
type src struct {
	t     *testing.T
	buf   *buffer.Buffer
	sn    uint16
	ts    uint32
	clock int64
}

func newSrc(t *testing.T, sn uint16) *src {
	return &src{t: t, buf: buffer.New(buffer.Config{Slots: 256, ClockRate: 90000, MimeType: "video/H264"}), sn: sn, ts: 90000}
}

func (s *src) next(payload []byte) *buffer.ExtPacket {
	s.t.Helper()
	p := rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 102, SequenceNumber: s.sn, Timestamp: s.ts, SSRC: 1, Marker: true}, Payload: payload}
	raw, _ := p.Marshal()
	s.clock += 50e6
	e, err := s.buf.Write(raw, s.clock)
	if err != nil {
		s.t.Fatal(err)
	}
	s.sn++
	s.ts += 4500
	return e
}

type outChecker struct {
	t         *testing.T
	started   bool
	lastSN    uint16
	lastTS    uint32
	forwarded int
}

func (c *outChecker) check(tp TranslationParams, err error) {
	c.t.Helper()
	if err != nil {
		c.t.Fatal(err)
	}
	if tp.Drop {
		return
	}
	if c.started {
		if tp.SequenceNumber != c.lastSN+1 {
			c.t.Fatalf("SN gap: %d after %d", tp.SequenceNumber, c.lastSN)
		}
		if int32(tp.Timestamp-c.lastTS) < 0 {
			c.t.Fatalf("TS went back: %d after %d", tp.Timestamp, c.lastTS)
		}
	}
	c.started = true
	c.lastSN, c.lastTS = tp.SequenceNumber, tp.Timestamp
	c.forwarded++
}

// Video starts on a keyframe; pausing and resuming (allocator) or muting and
// unmuting continue the outgoing stream without gaps, each restart on a keyframe.
func TestStartPauseResumeContinuity(t *testing.T) {
	f := NewForwarder("video/H264")
	s := newSrc(t, 65530) // wraps
	c := &outChecker{t: t}
	if tp, _ := f.GetTranslation(s.next(delta)); !tp.Drop || !tp.RequestKeyFrame {
		t.Fatalf("before a keyframe: %+v", tp)
	}
	tp, err := f.GetTranslation(s.next(spsPPS))
	if tp.Drop || !tp.IsStart {
		t.Fatalf("keyframe must start: %+v", tp)
	}
	c.check(tp, err)
	for range 5 {
		c.check(f.GetTranslation(s.next(delta)))
	}
	f.SetPaused(true)
	for range 5 {
		if tp, _ := f.GetTranslation(s.next(delta)); !tp.Drop {
			t.Fatal("paused must drop")
		}
	}
	f.SetPaused(false)
	if tp, _ := f.GetTranslation(s.next(delta)); !tp.Drop || !tp.RequestKeyFrame {
		t.Fatalf("resume must wait for a keyframe: %+v", tp)
	}
	c.check(f.GetTranslation(s.next(spsPPS)))
	c.check(f.GetTranslation(s.next(delta)))
	f.Mute(true)
	if tp, _ := f.GetTranslation(s.next(delta)); !tp.Drop {
		t.Fatal("muted must drop")
	}
	f.Mute(false)
	if tp, _ := f.GetTranslation(s.next(delta)); !tp.Drop || !tp.RequestKeyFrame {
		t.Fatalf("unmute must wait for a keyframe: %+v", tp)
	}
	c.check(f.GetTranslation(s.next(spsPPS)))
	if c.forwarded != 9 || f.Restarts() != 3 {
		t.Fatalf("forwarded %d, restarts %d", c.forwarded, f.Restarts())
	}
}

// Audio keeps the incoming order and gaps, and restarts at once after unmute.
func TestAudioOutOfOrderAndLossPreserveGaps(t *testing.T) {
	f := NewForwarder("audio/opus")
	b := buffer.New(buffer.Config{Slots: 64, MimeType: "audio/opus"})
	mk := func(sn uint16) *buffer.ExtPacket {
		p := rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: sn, Timestamp: uint32(sn) * 960}, Payload: []byte{1}}
		raw, _ := p.Marshal()
		e, err := b.Write(raw, int64(sn)*20e6)
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	var got []uint16
	for _, sn := range []uint16{100, 101, 103, 102, 104} {
		tp, _ := f.GetTranslation(mk(sn))
		got = append(got, tp.SequenceNumber)
	}
	want := []uint16{100, 101, 103, 102, 104}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v", got)
		}
	}
	f.Mute(true)
	if tp, _ := f.GetTranslation(mk(105)); !tp.Drop {
		t.Fatal("muted must drop")
	}
	f.Mute(false)
	tp, _ := f.GetTranslation(mk(200))
	if tp.SequenceNumber != 105 || !tp.IsStart {
		t.Fatalf("%+v", tp)
	}
}

// Padding reserves sequence numbers: media continues after them, and a late packet
// from before the padding fills its own gap instead of landing on a padding SN.
func TestPaddingKeepsSequenceContinuous(t *testing.T) {
	f := NewForwarder("video/H264")
	s := newSrc(t, 1000)
	if _, _, err := f.PaddingParams(3); err == nil {
		t.Fatal("padding before the forwarder started must fail")
	}
	if tp, _ := f.GetTranslation(s.next(spsPPS)); tp.Drop {
		t.Fatalf("%+v", tp)
	}
	late := s.next(delta) // in 1001: held back, arrives after the padding
	tp, _ := f.GetTranslation(s.next(delta))
	last := tp.SequenceNumber // in 1002 (1001 is a gap for now)
	sn, ts, err := f.PaddingParams(3)
	if err != nil || sn != last+1 || ts != tp.Timestamp {
		t.Fatalf("padding sn=%d ts=%d err=%v, last media sn=%d ts=%d", sn, ts, err, last, tp.Timestamp)
	}
	if tp, _ := f.GetTranslation(late); tp.Drop || tp.SequenceNumber != last-1 {
		t.Fatalf("late packet from before the padding: %+v, want sn %d", tp, last-1)
	}
	if tp, _ := f.GetTranslation(s.next(delta)); tp.Drop || tp.SequenceNumber != last+4 {
		t.Fatalf("media after padding: %+v, want sn %d", tp, last+4)
	}
	f.SetPaused(true)
	if _, _, err := f.PaddingParams(1); err == nil {
		t.Fatal("padding while paused must fail")
	}
}

// A padding-only packet from the publisher is absorbed; a packet lost before it keeps
// its slot and fills it when it arrives late, and can then be retransmitted.
func TestLatePacketAfterAbsorbedPaddingKeepsItsPosition(t *testing.T) {
	f := NewForwarder("video/H264")
	s := newSrc(t, 1000)
	key, _ := f.GetTranslation(s.next(spsPPS)) // in 1000
	late := s.next(delta)                      // in 1001: lost upstream, recovered later
	pad := s.next(nil)                         // in 1002: padding only
	if !pad.PaddingOnly {
		t.Fatal("buffer did not mark the empty packet as padding")
	}
	if tp, _ := f.GetTranslation(pad); !tp.Drop {
		t.Fatalf("padding must be absorbed: %+v", tp)
	}
	after, _ := f.GetTranslation(s.next(delta)) // in 1003
	if after.Drop || after.SequenceNumber != key.SequenceNumber+2 {
		t.Fatalf("in 1003: sn %d, want %d (no hole for the padding)", after.SequenceNumber, key.SequenceNumber+2)
	}
	tp, _ := f.GetTranslation(late)
	if tp.Drop || tp.SequenceNumber != key.SequenceNumber+1 {
		t.Fatalf("late in 1001: %+v, want sn %d", tp, key.SequenceNumber+1)
	}
	if orig, ok := f.TranslateNACK(tp.SequenceNumber); !ok || orig != 1001 {
		t.Fatalf("NACK for the late packet: orig=%d ok=%v", orig, ok)
	}
}

// A keyframe start that arrives late (its first packet lost and recovered) must not
// start forwarding: the packets after it were dropped while waiting.
func TestStartIgnoresLateKeyframeStart(t *testing.T) {
	f := NewForwarder("video/H264")
	s := newSrc(t, 1001)
	if tp, _ := f.GetTranslation(s.next(delta)); !tp.Drop || !tp.RequestKeyFrame { // in 1001 first
		t.Fatalf("before any keyframe: %+v", tp)
	}
	s.sn = 1000
	first := s.next(spsPPS) // in 1000, recovered late
	if !first.Late {
		t.Fatal("buffer did not mark the recovered packet late")
	}
	if tp, _ := f.GetTranslation(first); !tp.Drop || !tp.RequestKeyFrame {
		t.Fatalf("started on a late keyframe: %+v", tp)
	}
	s.sn = 1002
	if tp, _ := f.GetTranslation(s.next(spsPPS)); tp.Drop {
		t.Fatalf("an in-order keyframe must start: %+v", tp)
	}
}

// A NACK for an SN whose packet has not been forwarded yet (lost upstream, still being
// recovered) must not be answered with the older packet that used the same history slot.
func TestNACKForUnsentSNIsNotAnsweredFromAnOlderPacket(t *testing.T) {
	f := NewForwarder("audio/opus")
	b := buffer.New(buffer.Config{Slots: 1024, MimeType: "audio/opus"})
	mk := func(sn uint16) *buffer.ExtPacket {
		p := rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: sn, Timestamp: uint32(sn) * 960}, Payload: []byte{1}}
		raw, _ := p.Marshal()
		e, err := b.Write(raw, int64(sn)*20e6)
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	var out uint16
	for sn := uint16(1); sn <= nackHistory+10; sn++ { // fill every slot once
		tp, _ := f.GetTranslation(mk(sn))
		out = tp.SequenceNumber
	}
	tp, _ := f.GetTranslation(mk(nackHistory + 12)) // the one before it is lost upstream
	missing := out + 1
	if tp.SequenceNumber != missing+1 {
		t.Fatalf("forwarded as %d, want %d (a gap for the lost packet)", tp.SequenceNumber, missing+1)
	}
	if orig, ok := f.TranslateNACK(missing); ok {
		t.Fatalf("NACK for the unsent SN %d maps to original %d", missing, orig)
	}
	if info, ok := f.RetransmitInfo(missing, 1e9, 0); ok {
		t.Fatalf("NACK for the unsent SN %d would resend original %d", missing, info.Original)
	}
	late, _ := f.GetTranslation(mk(nackHistory + 11))
	if late.SequenceNumber != missing {
		t.Fatalf("late packet forwarded as %d, want %d", late.SequenceNumber, missing)
	}
	if info, ok := f.RetransmitInfo(missing, 2e9, 0); !ok || info.Original != nackHistory+11 {
		t.Fatalf("recovered packet: resend %+v %v", info, ok)
	}
	// At most 3 resends per packet.
	for i := range 3 {
		if _, ok := f.RetransmitInfo(missing, int64(3+i)*1e9, 0); ok != (i < 2) {
			t.Fatalf("resend %d: ok=%v", i+2, ok)
		}
	}
}

func BenchmarkForwarderGetTranslation(b *testing.B) {
	f := NewForwarder("video/H264")
	buf := buffer.New(buffer.Config{Slots: 500, MimeType: "video/H264"})
	p := rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: 0}, Payload: []byte{0x65, 1, 2, 3}}
	raw, _ := p.Marshal()
	e, _ := buf.Write(raw, 0)
	_, _ = f.GetTranslation(e)
	b.ReportAllocs()
	for b.Loop() {
		e.ExtSequenceNumber++
		e.KeyFrame = false
		if tp, _ := f.GetTranslation(e); tp.Drop {
			b.Fatal("drop")
		}
	}
}
