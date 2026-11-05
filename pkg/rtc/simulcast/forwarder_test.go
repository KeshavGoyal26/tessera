package simulcast

import (
	"testing"

	"github.com/pion/rtp"

	"github.com/KeshavGoyal26/tessera/pkg/rtc/buffer"
	"github.com/KeshavGoyal26/tessera/pkg/rtc/types"
)

// layerSrc generates VP8 packets for one simulcast layer through a real buffer.
type layerSrc struct {
	t     *testing.T
	buf   *buffer.Buffer
	sn    uint16
	ts    uint32
	pid   uint16
	tl0   uint8
	m     bool // 15-bit picture id
	y     bool // VP8 layer-sync bit on the next packets
	clock int64
}

func newSrc(t *testing.T, spatial int32, sn uint16, ts uint32, pid uint16) *layerSrc {
	return &layerSrc{t: t, buf: buffer.New(buffer.Config{Slots: 64, MimeType: "video/VP8", Spatial: spatial}), sn: sn, ts: ts, pid: pid, m: true}
}

// next returns the next packet; key marks a keyframe; tid the temporal layer.
func (s *layerSrc) next(key bool, tid uint8, advanceFrame bool) *buffer.ExtPacket {
	s.t.Helper()
	var desc []byte
	if s.m {
		desc = []byte{0x90, 0xE0, 0x80 | byte(s.pid>>8), byte(s.pid), s.tl0, tid << 6}
	} else {
		desc = []byte{0x90, 0xE0, byte(s.pid & 0x7f), s.tl0, tid << 6}
	}
	if s.y {
		desc[len(desc)-1] |= 0x20
	}
	first := byte(0x01)
	if key {
		first = 0x00
	}
	p := rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: s.sn, Timestamp: s.ts, SSRC: 1}, Payload: append(desc, first, 0xaa)}
	raw, _ := p.Marshal()
	s.clock += 33e6
	e, err := s.buf.Write(raw, s.clock)
	if err != nil {
		s.t.Fatal(err)
	}
	s.sn++
	if advanceFrame {
		s.ts += 3000
		s.pid = (s.pid + 1) & 0x7fff
		if !s.m {
			s.pid &= 0x7f
		}
		if tid == 0 {
			s.tl0++
		}
	}
	return e
}

type outChecker struct {
	t         *testing.T
	started   bool
	lastSN    uint16
	lastTS    uint32
	lastPID   uint16
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
		if tp.HasVP8 && tp.VP8.PictureID != c.lastPID && tp.VP8.PictureID != (c.lastPID+1)&0x7fff {
			c.t.Fatalf("picture id jump %d -> %d", c.lastPID, tp.VP8.PictureID)
		}
	}
	c.started = true
	c.lastSN, c.lastTS = tp.SequenceNumber, tp.Timestamp
	if tp.HasVP8 {
		c.lastPID = tp.VP8.PictureID
	}
	c.forwarded++
}

func TestSwitchUpDownContinuity(t *testing.T) {
	f := NewForwarder("video/VP8")
	f.SetAvailableLayers(2)
	f.SetMaxLayer(types.VideoLayer{Spatial: 0, Temporal: 3})
	layers := []*layerSrc{newSrc(t, 0, 65500, 1000, 100), newSrc(t, 1, 20, 4_000_000_000, 32760), newSrc(t, 2, 40000, 7, 5)}
	c := &outChecker{t: t}

	// Not started: drop and request keyframe until a key arrives on layer 0.
	tp, _ := f.GetTranslation(layers[0].next(false, 0, true), 0)
	if !tp.Drop || !tp.RequestKeyFrame || tp.KeyFrameLayer != 0 {
		t.Fatalf("%+v", tp)
	}
	run := func(frames int, keyAt int) {
		for i := range frames {
			for l, s := range layers {
				// two packets per frame
				c.check(f.GetTranslation(s.next(i == keyAt, uint8(i%2), false), int32(l)))
				c.check(f.GetTranslation(s.next(false, uint8(i%2), true), int32(l)))
			}
		}
	}
	run(50, 0) // wraps layer 0 SN past 65535
	if f.CurrentLayer().Spatial != 0 {
		t.Fatal(f.CurrentLayer())
	}
	f.SetMaxLayer(types.VideoLayer{Spatial: 2, Temporal: 3})
	run(5, -1) // no keyframe: stays on 0
	if f.CurrentLayer().Spatial != 0 {
		t.Fatal("switched without keyframe")
	}
	run(10, 3)
	if f.CurrentLayer().Spatial != 2 {
		t.Fatalf("expected layer 2, got %v", f.CurrentLayer())
	}
	f.SetTargetLayer(types.VideoLayer{Spatial: 1, Temporal: 3})
	run(10, 2)
	if f.CurrentLayer().Spatial != 1 || f.Switches() != 3 {
		t.Fatalf("layer %v switches %d", f.CurrentLayer(), f.Switches())
	}
	// Pause and resume.
	f.SetTargetLayer(types.InvalidVideoLayer)
	run(2, 0)
	if f.CurrentLayer().IsValid() {
		t.Fatal("not paused")
	}
	f.SetTargetLayer(types.VideoLayer{Spatial: 1, Temporal: 3})
	run(4, 1)
	if f.CurrentLayer().Spatial != 1 {
		t.Fatal("not resumed")
	}
}

func TestTemporalDropAndNACK(t *testing.T) {
	f := NewForwarder("video/VP8")
	f.SetAvailableLayers(0)
	f.SetMaxLayer(types.VideoLayer{Spatial: 0, Temporal: 0})
	s := newSrc(t, 0, 10, 0, 0)
	c := &outChecker{t: t}
	c.check(f.GetTranslation(s.next(true, 0, true), 0))
	var dropped int
	for i := range 20 {
		tp, err := f.GetTranslation(s.next(false, uint8(i%2), true), 0)
		if tp.Drop {
			dropped++
		}
		c.check(tp, err)
	}
	if dropped != 10 {
		t.Fatalf("dropped %d", dropped)
	}
	// NACK translation of the last forwarded packet.
	layer, orig, ok := f.TranslateNACK(c.lastSN)
	if !ok || layer != 0 || orig != s.sn-2 {
		t.Fatalf("nack %v %d %v (want %d)", layer, orig, ok, s.sn-2)
	}
	if _, _, ok := f.TranslateNACK(c.lastSN + 5); ok {
		t.Fatal("future SN must not translate")
	}
}

func TestOutOfOrderAndLossPreserveGaps(t *testing.T) {
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
		tp, _ := f.GetTranslation(mk(sn), 0)
		got = append(got, tp.SequenceNumber)
	}
	want := []uint16{100, 101, 103, 102, 104}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v", got)
		}
	}
	// Mute/unmute keeps continuity.
	f.Mute(true)
	if tp, _ := f.GetTranslation(mk(105), 0); !tp.Drop {
		t.Fatal("muted must drop")
	}
	f.Mute(false)
	tp, _ := f.GetTranslation(mk(200), 0)
	if tp.SequenceNumber != 105 || !tp.IsSwitch {
		t.Fatalf("%+v", tp)
	}
}

func TestRewriteVP8SevenTo15Bit(t *testing.T) {
	src := newSrc(t, 0, 1, 0, 120)
	src.m = false
	f := NewForwarder("video/VP8")
	f.SetAvailableLayers(0)
	e := src.next(true, 0, true)
	tp, _ := f.GetTranslation(e, 0)
	if tp.Drop || tp.VP8.HeaderSizeDelta != 1 {
		t.Fatalf("%+v", tp)
	}
	dst := make([]byte, 64)
	n := RewriteVP8(dst, e.Payload, &e.VP8, &tp.VP8)
	if n != len(e.Payload)+1 {
		t.Fatalf("n %d", n)
	}
	var d buffer.VP8Descriptor
	if err := buffer.ParseVP8(dst[:n], &d); err != nil || !d.MBit || d.PictureID != tp.VP8.PictureID || !d.IsKeyFrame || d.TL0PICIDX != e.VP8.TL0PICIDX {
		t.Fatalf("%v %+v", err, d)
	}
}

func BenchmarkForwarderGetTranslation(b *testing.B) {
	f := NewForwarder("video/VP8")
	f.SetAvailableLayers(0)
	buf := buffer.New(buffer.Config{Slots: 500, MimeType: "video/VP8"})
	p := rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: 0}, Payload: []byte{0x90, 0x80, 0x80, 0x01, 0x00, 1, 2, 3}}
	raw, _ := p.Marshal()
	e, _ := buf.Write(raw, 0)
	_, _ = f.GetTranslation(e, 0)
	b.ReportAllocs()
	for b.Loop() {
		e.ExtSequenceNumber++
		e.KeyFrame = false
		if tp, _ := f.GetTranslation(e, 0); tp.Drop {
			b.Fatal("drop")
		}
	}
}

func TestH264SwitchOnIDR(t *testing.T) {
	f := NewForwarder("video/H264")
	f.SetAvailableLayers(1)
	f.SetMaxLayer(types.VideoLayer{Spatial: 0, Temporal: 3})
	bufs := []*buffer.Buffer{
		buffer.New(buffer.Config{Slots: 64, MimeType: "video/H264", Spatial: 0}),
		buffer.New(buffer.Config{Slots: 64, MimeType: "video/H264", Spatial: 1}),
	}
	sns := []uint16{65530, 300}
	tss := []uint32{0, 99999}
	mk := func(l int, payload []byte) *buffer.ExtPacket {
		p := rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: sns[l], Timestamp: tss[l]}, Payload: payload}
		raw, _ := p.Marshal()
		sns[l]++
		tss[l] += 3000
		e, err := bufs[l].Write(raw, int64(sns[l])*33e6)
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	sps, idr, nonIDR := []byte{0x67, 1}, []byte{0x65, 1}, []byte{0x41, 1}
	c := &outChecker{t: t}
	c.check(f.GetTranslation(mk(0, sps), 0))
	c.check(f.GetTranslation(mk(0, idr), 0))
	f.SetMaxLayer(types.VideoLayer{Spatial: 1, Temporal: 3})
	for range 5 {
		c.check(f.GetTranslation(mk(0, nonIDR), 0))
		if tp, _ := f.GetTranslation(mk(1, nonIDR), 1); !tp.Drop || !tp.RequestKeyFrame {
			t.Fatal("must wait for IDR on layer 1")
		}
	}
	c.check(f.GetTranslation(mk(1, sps), 1))
	if f.CurrentLayer().Spatial != 1 {
		t.Fatal("no switch on SPS")
	}
	if tp, _ := f.GetTranslation(mk(0, nonIDR), 0); !tp.Drop {
		t.Fatal("old layer must be dropped after switch")
	}
	c.check(f.GetTranslation(mk(1, nonIDR), 1))
	if c.forwarded != 9 {
		t.Fatal(c.forwarded)
	}
}

// Padding reserves sequence numbers: media continues after them, and a late packet
// from before the padding (which would collide with a padding number) is dropped.
func TestPaddingKeepsSequenceContinuous(t *testing.T) {
	f := NewForwarder("video/VP8")
	f.SetAvailableLayers(0)
	f.SetMaxLayer(types.VideoLayer{Spatial: 0, Temporal: 3})
	src := newSrc(t, 0, 1000, 90000, 7)
	if _, _, err := f.PaddingParams(3); err == nil {
		t.Fatal("padding before the forwarder started must fail")
	}
	tp, _ := f.GetTranslation(src.next(true, 0, true), 0) // keyframe starts forwarding
	if tp.Drop {
		t.Fatalf("%+v", tp)
	}
	late := src.next(false, 0, true) // in 1001: held back, arrives after the padding
	tp, _ = f.GetTranslation(src.next(false, 0, true), 0)
	last := tp.SequenceNumber // in 1002 (1001 is a gap for now)
	sn, ts, err := f.PaddingParams(3)
	if err != nil || sn != last+1 || ts != tp.Timestamp {
		t.Fatalf("padding sn=%d ts=%d err=%v, last media sn=%d ts=%d", sn, ts, err, last, tp.Timestamp)
	}
	// A late packet from before the padding fills its own gap: it must not land on a padding SN.
	if tp, _ := f.GetTranslation(late, 0); tp.Drop || tp.SequenceNumber != last-1 {
		t.Fatalf("late packet from before the padding: %+v, want sn %d", tp, last-1)
	}
	tp, _ = f.GetTranslation(src.next(false, 0, true), 0)
	if tp.Drop || tp.SequenceNumber != last+4 {
		t.Fatalf("media after padding: %+v, want sn %d", tp, last+4)
	}
}

// A packet recovered late (upstream NACK) after the forwarder absorbed a dropped
// temporal-layer packet must keep the sequence number and picture id of its own
// position. With a single offset it used to collide with the packet before it,
// leaving a hole the subscriber could never get retransmitted.
func TestLatePacketAfterTemporalDropKeepsItsPosition(t *testing.T) {
	f := NewForwarder("video/VP8")
	f.SetAvailableLayers(0)
	f.SetMaxLayer(types.VideoLayer{Spatial: 0, Temporal: 0})
	src := newSrc(t, 0, 1000, 90000, 7)
	key, _ := f.GetTranslation(src.next(true, 0, true), 0) // in 1000, pid 7
	if key.Drop {
		t.Fatalf("%+v", key)
	}
	late := src.next(false, 0, true) // in 1001, pid 8: lost upstream, recovered later
	if tp, _ := f.GetTranslation(src.next(false, 0, true), 0); tp.Drop || tp.SequenceNumber != key.SequenceNumber+2 {
		t.Fatalf("in 1002: %+v", tp)
	}
	if tp, _ := f.GetTranslation(src.next(false, 1, true), 0); !tp.Drop { // in 1003, TID 1: filtered
		t.Fatalf("TID 1 must be dropped: %+v", tp)
	}
	after, _ := f.GetTranslation(src.next(false, 0, true), 0) // in 1004
	if after.Drop || after.SequenceNumber != key.SequenceNumber+3 {
		t.Fatalf("in 1004: %+v, want sn %d (the filtered packet is absorbed)", after, key.SequenceNumber+3)
	}
	tp, _ := f.GetTranslation(late, 0)
	if tp.Drop || tp.SequenceNumber != key.SequenceNumber+1 {
		t.Fatalf("late in 1001: sn %d drop=%v, want sn %d", tp.SequenceNumber, tp.Drop, key.SequenceNumber+1)
	}
	if tp.VP8.PictureID != key.VP8.PictureID+1 {
		t.Fatalf("late in 1001: picture id %d, want %d", tp.VP8.PictureID, key.VP8.PictureID+1)
	}
	if after.VP8.PictureID != key.VP8.PictureID+3 {
		t.Fatalf("in 1004: picture id %d, want %d (filtered picture hidden)", after.VP8.PictureID, key.VP8.PictureID+3)
	}
	// The late packet is now retransmittable under its munged SN.
	if _, orig, ok := f.TranslateNACK(tp.SequenceNumber); !ok || orig != 1001 {
		t.Fatalf("NACK for the late packet: orig=%d ok=%v", orig, ok)
	}
}

// Raising the temporal layer must wait for a layer-sync frame: a higher-layer frame
// without it references pictures the subscriber never received.
func TestTemporalUpSwitchWaitsForLayerSync(t *testing.T) {
	f := NewForwarder("video/VP8")
	f.SetAvailableLayers(0)
	f.SetMaxLayer(types.VideoLayer{Spatial: 0, Temporal: 0})
	src := newSrc(t, 0, 1000, 90000, 7)
	if tp, _ := f.GetTranslation(src.next(true, 0, true), 0); tp.Drop {
		t.Fatalf("keyframe: %+v", tp)
	}
	if tp, _ := f.GetTranslation(src.next(false, 2, true), 0); !tp.Drop {
		t.Fatal("TID 2 forwarded while the target is T0")
	}
	f.SetMaxLayer(types.VideoLayer{Spatial: 0, Temporal: 2})
	if tp, _ := f.GetTranslation(src.next(false, 2, true), 0); !tp.Drop {
		t.Fatal("TID 2 without layer sync forwarded right after the target went up")
	}
	if tp, _ := f.GetTranslation(src.next(false, 0, true), 0); tp.Drop {
		t.Fatal("TID 0 dropped")
	}
	src.y = true
	if tp, _ := f.GetTranslation(src.next(false, 1, true), 0); tp.Drop {
		t.Fatal("TID 1 with layer sync must start layer 1")
	}
	src.y = false
	if tp, _ := f.GetTranslation(src.next(false, 2, true), 0); !tp.Drop {
		t.Fatal("TID 2 without layer sync forwarded: only layer 1 is synced")
	}
	if tp, _ := f.GetTranslation(src.next(false, 1, true), 0); tp.Drop {
		t.Fatal("TID 1 dropped after layer 1 was synced")
	}
	src.y = true
	if tp, _ := f.GetTranslation(src.next(false, 2, true), 0); tp.Drop {
		t.Fatal("TID 2 with layer sync must start layer 2")
	}
	if got := f.CurrentLayer().Temporal; got != 2 {
		t.Fatalf("current temporal %d, want 2", got)
	}
	// Going down is immediate.
	f.SetMaxLayer(types.VideoLayer{Spatial: 0, Temporal: 0})
	if tp, _ := f.GetTranslation(src.next(false, 1, true), 0); !tp.Drop {
		t.Fatal("TID 1 forwarded after the target went down to T0")
	}
}

// A keyframe whose first packet arrives late (lost, then recovered) must not start
// a layer: its other packets were already dropped while waiting, so the receiver
// would get a keyframe with holes nothing can fill.
func TestSwitchIgnoresLateKeyframeStart(t *testing.T) {
	f := NewForwarder("video/VP8")
	f.SetAvailableLayers(0)
	src := newSrc(t, 0, 1000, 90000, 7)
	src.sn = 1001
	rest := src.next(false, 0, false) // in 1001: rest of the keyframe arrives first
	if tp, _ := f.GetTranslation(rest, 0); !tp.Drop || !tp.RequestKeyFrame {
		t.Fatalf("before any keyframe: %+v", tp)
	}
	src.sn = 1000
	first := src.next(true, 0, true) // in 1000: the recovered keyframe start, late
	if !first.Late {
		t.Fatal("buffer did not mark the recovered packet late")
	}
	if tp, _ := f.GetTranslation(first, 0); !tp.Drop || !tp.RequestKeyFrame {
		t.Fatalf("switched on a late keyframe start: %+v", tp)
	}
	src.sn = 1002
	if tp, _ := f.GetTranslation(src.next(true, 0, true), 0); tp.Drop { // the next, in-order keyframe
		t.Fatalf("in-order keyframe must start the layer: %+v", tp)
	}
}
