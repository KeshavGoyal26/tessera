package buffer

import (
	"errors"
	"testing"

	"github.com/pion/rtp"

	"github.com/KeshavGoyal26/tessera/pkg/errs"
)

func TestPoolReuse(t *testing.T) {
	b := Pool.Get()
	if len(*b) != MaxPacketSize {
		t.Fatalf("len %d", len(*b))
	}
	Pool.Put(b)
	allocs := testing.AllocsPerRun(100, func() { Pool.Put(Pool.Get()) })
	if allocs > 0 {
		t.Fatalf("pool allocates: %v", allocs)
	}
}

type ext struct {
	id      uint8
	payload []byte
}

func mkPkt(t testing.TB, sn uint16, ts uint32, payload []byte, exts ...ext) []byte {
	h := rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: sn, Timestamp: ts, SSRC: 1234}
	for _, e := range exts {
		if err := h.SetExtension(e.id, e.payload); err != nil {
			t.Fatal(err)
		}
	}
	p := rtp.Packet{Header: h, Payload: payload}
	b, err := p.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestWriteWrapAndGet(t *testing.T) {
	b := New(Config{Slots: 16, ClockRate: 90000, MimeType: "video/VP8"})
	defer b.Release()
	var lastExt uint64
	for i := range 10 {
		sn := uint16(65530 + i)
		e, err := b.Write(mkPkt(t, sn, uint32(i*3000), []byte{0x10, 0x00, byte(i)}), int64(i)*1e6)
		if err != nil {
			t.Fatal(err)
		}
		if e.SequenceNumber != sn || (i > 0 && e.ExtSequenceNumber != lastExt+1) {
			t.Fatalf("sn %d ext %d last %d", sn, e.ExtSequenceNumber, lastExt)
		}
		lastExt = e.ExtSequenceNumber
	}
	if _, err := b.Write(mkPkt(t, 2, 0, []byte{0x10, 0, 0}), 0); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("dup: %v", err)
	}
	dst := make([]byte, MaxPacketSize)
	n, err := b.GetPacket(dst, 65535)
	if err != nil {
		t.Fatal(err)
	}
	var p rtp.Packet
	if err := p.Unmarshal(dst[:n]); err != nil || p.SequenceNumber != 65535 || p.Payload[2] != 5 {
		t.Fatalf("%v %+v", err, p.Header)
	}
	if _, err := b.GetPacket(dst, 100); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	s := b.Stats()
	if s.PacketsReceived != 10 || s.PacketsLost != 0 || s.PacketsDuplicate != 1 {
		t.Fatalf("%+v", s)
	}
}

func TestLossOutOfOrderAndNACK(t *testing.T) {
	b := New(Config{Slots: 64, ClockRate: 90000})
	defer b.Release()
	for _, sn := range []uint16{1, 2, 5, 6} {
		if _, err := b.Write(mkPkt(t, sn, 0, []byte{1}), 0); err != nil {
			t.Fatal(err)
		}
	}
	got := b.MissingSequenceNumbers(nil, 1e6)
	if len(got) != 2 || got[0] != 3 || got[1] != 4 {
		t.Fatalf("nack %v", got)
	}
	// Throttled within 20 ms.
	if got := b.MissingSequenceNumbers(nil, 5e6); len(got) != 0 {
		t.Fatalf("throttle %v", got)
	}
	if _, err := b.Write(mkPkt(t, 3, 0, []byte{1}), 0); err != nil {
		t.Fatal(err)
	}
	if got := b.MissingSequenceNumbers(nil, 30e6); len(got) != 1 || got[0] != 4 {
		t.Fatalf("after recovery %v", got)
	}
	if got := b.MissingSequenceNumbers(nil, 400e6); len(got) != 0 {
		t.Fatalf("give up %v", got)
	}
	s := b.Stats()
	if s.PacketsLost != 1 || s.PacketsOutOfOrder != 1 || s.Expected != 6 {
		t.Fatalf("%+v", s)
	}
}

func TestAudioLevel(t *testing.T) {
	b := New(Config{Slots: 8, ClockRate: 48000, MimeType: "audio/opus", AudioLevelExtID: 1})
	defer b.Release()
	e, err := b.Write(mkPkt(t, 1, 0, []byte{1, 2}, ext{1, []byte{0x80 | 30}}), 0)
	if err != nil {
		t.Fatal(err)
	}
	if !e.HasAudioLevel || e.AudioLevel != 30 || !e.VoiceActivity {
		t.Fatalf("%+v", e)
	}
}

func TestVP8Descriptor(t *testing.T) {
	// X=1 S=1 PID=0; I L T K; M=1 picture id 0x1234; TL0PICIDX 7; TID 2 Y KEYIDX 3; then keyframe byte.
	p := []byte{0x90, 0xF0, 0x80 | 0x12, 0x34, 7, 0x80 | 0x20 | 3, 0x00}
	var d VP8Descriptor
	if err := ParseVP8(p, &d); err != nil {
		t.Fatal(err)
	}
	if !d.MBit || d.PictureID != 0x1234 || d.TL0PICIDX != 7 || d.TID != 2 || !d.Y || d.KEYIDX != 3 || d.HeaderSize != 6 || !d.IsKeyFrame {
		t.Fatalf("%+v", d)
	}
	// 7-bit picture id, inter frame.
	if err := ParseVP8([]byte{0x90, 0x80, 0x05, 0x01}, &d); err != nil || d.MBit || d.PictureID != 5 || d.IsKeyFrame {
		t.Fatalf("%v %+v", err, d)
	}
	if ParseVP8([]byte{0x90, 0x80}, &d) == nil {
		t.Fatal("truncated must fail")
	}
}

func TestH264KeyFrame(t *testing.T) {
	cases := []struct {
		p    []byte
		want bool
	}{
		{[]byte{0x65, 1}, true},                         // IDR
		{[]byte{0x67, 1}, true},                         // SPS
		{[]byte{0x41, 1}, false},                        // non-IDR
		{[]byte{0x78, 0, 2, 0x67, 1, 0, 1, 0x68}, true}, // STAP-A with SPS
		{[]byte{0x7c, 0x85, 1}, true},                   // FU-A start IDR
		{[]byte{0x7c, 0x05, 1}, false},                  // FU-A middle
		{[]byte{0x7c, 0x81, 1}, false},                  // FU-A start non-IDR
	}
	for i, c := range cases {
		if got := IsH264KeyFrame(c.p); got != c.want {
			t.Errorf("%d: got %v", i, got)
		}
	}
}

// A keyframe starts at its SPS. When the SPS packet is lost, the IDR fragment after it
// must not count as a keyframe start: a layer switch there forwards an IDR without the
// parameters of the new resolution and the subscriber freezes (regression: seconds of
// frozen video after a switch under 5% uplink loss). Streams without in-band SPS start
// on the IDR.
func TestH264KeyFrameStartsAtSPS(t *testing.T) {
	stap := []byte{0x78, 0, 2, 0x67, 1, 0, 1, 0x68} // STAP-A: SPS, PPS
	idrStart := []byte{0x7c, 0x85, 1}               // FU-A start of an IDR slice
	write := func(b *Buffer, sn uint16, p []byte) bool {
		e, err := b.Write(mkPkt(t, sn, uint32(sn)*3000, p), int64(sn))
		if err != nil {
			t.Fatal(err)
		}
		return e.KeyFrame
	}

	b := New(Config{Slots: 64, ClockRate: 90000, MimeType: "video/H264"})
	defer b.Release()
	if !write(b, 1, stap) {
		t.Fatal("SPS packet: want a keyframe start")
	}
	if write(b, 2, idrStart) {
		t.Fatal("IDR after its SPS: not the start")
	}
	if write(b, 11, idrStart) { // the next keyframe's SPS (sn 10) was lost
		t.Fatal("IDR whose SPS was lost: must not be a keyframe start")
	}
	if !write(b, 10, stap) { // ... and recovered late
		t.Fatal("late SPS packet: still a keyframe start (the forwarder ignores late ones)")
	}

	nosps := New(Config{Slots: 64, ClockRate: 90000, MimeType: "video/H264"})
	defer nosps.Release()
	if !write(nosps, 1, idrStart) {
		t.Fatal("stream without in-band SPS: the IDR starts the keyframe")
	}
}

func TestWriteDoesNotAllocate(t *testing.T) {
	b := New(Config{Slots: 500, ClockRate: 90000, MimeType: "video/VP8"})
	defer b.Release()
	pkt := mkPkt(t, 0, 0, []byte{0x90, 0x80, 0x05, 0x01, 2, 3, 4})
	sn := uint16(0)
	allocs := testing.AllocsPerRun(1000, func() {
		sn++
		pkt[2], pkt[3] = byte(sn>>8), byte(sn)
		if _, err := b.Write(pkt, 0); err != nil {
			t.Fatal(err)
		}
	})
	if allocs > 0 {
		t.Fatalf("allocs %v", allocs)
	}
}

func BenchmarkBufferWrite(b *testing.B) {
	buf := New(Config{Slots: 500, ClockRate: 90000, MimeType: "video/VP8"})
	defer buf.Release()
	pkt := mkPkt(b, 0, 0, append([]byte{0x90, 0x80, 0x05, 0x01}, make([]byte, 1000)...))
	b.ReportAllocs()
	b.SetBytes(int64(len(pkt)))
	sn := uint16(0)
	for b.Loop() {
		sn++
		pkt[2], pkt[3] = byte(sn>>8), byte(sn)
		if _, err := buf.Write(pkt, 0); err != nil {
			b.Fatal(err)
		}
	}
}

func FuzzBufferWrite(f *testing.F) {
	f.Add([]byte{0x80, 96, 0, 1, 0, 0, 0, 0, 0, 0, 0, 1, 0x90, 0x80, 0x05})
	f.Add([]byte{0x90, 96, 0, 1, 0, 0, 0, 0, 0, 0, 0, 1, 0xBE, 0xDE, 0, 1, 0x10, 0x85, 0, 0, 0x7c, 0x85})
	f.Fuzz(func(t *testing.T, data []byte) {
		for _, mime := range []string{"video/VP8", "video/H264", "audio/opus"} {
			b := New(Config{Slots: 8, MimeType: mime, AudioLevelExtID: 1})
			_, _ = b.Write(data, 0)
			dst := make([]byte, MaxPacketSize)
			_, _ = b.GetPacket(dst, 1)
			b.Release()
		}
	})
}

// On a 350 ms RTT link a NACK is repeated once per round trip and kept for several
// round trips; the old fixed timing (every 20 ms, give up after 300 ms) spent all
// its tries before the first retransmission could arrive.
func TestNACKRetriesFollowRTT(t *testing.T) {
	var q nackQueue
	const ms = int64(1e6)
	rtt := 350 * ms
	q.addRange(100, 101, 0)
	sends := 0
	var at []int64
	for now := int64(0); now <= 3000*ms; now += 10 * ms {
		if out := q.collect(nil, now, 200, 1000, rtt); len(out) > 0 {
			sends++
			at = append(at, now)
		}
	}
	if sends < 3 || sends > 5 {
		t.Fatalf("sent %d NACKs at %v, want one per ~RTT for 4 RTTs", sends, at)
	}
	for i := 1; i < len(at); i++ {
		if at[i]-at[i-1] < rtt {
			t.Fatalf("retries %v closer than the RTT", at)
		}
	}
	// Low RTT keeps the fast 20 ms retry.
	var q2 nackQueue
	q2.addRange(100, 101, 0)
	n := 0
	for now := int64(0); now <= 100*ms; now += 10 * ms {
		n += len(q2.collect(nil, now, 200, 1000, 0))
	}
	if n < 5 {
		t.Fatalf("low RTT: %d NACKs in 100 ms, want ~6", n)
	}
}
