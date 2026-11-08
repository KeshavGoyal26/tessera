package rtc

import (
	"fmt"
	"testing"

	"github.com/pion/rtp"
	"github.com/pion/srtp/v3"
	"github.com/pion/webrtc/v4"

	"github.com/KeshavGoyal26/tessera/pkg/rtc/buffer"
)

// srtpWriter emulates Pion's write path: marshal the header, then SRTP
// encrypt into a reused buffer (the socket write itself is not included).
type srtpWriter struct {
	ctx   *srtp.Context
	plain []byte
	out   []byte
	n     int
}

// benchProfile is the SRTP profile used by the benchmarks.
var benchProfile = srtp.ProtectionProfileAes128CmHmacSha1_80

func newSRTPWriter(tb testing.TB) *srtpWriter {
	tb.Helper()
	keyLen, _ := benchProfile.KeyLen()
	saltLen, _ := benchProfile.SaltLen()
	ctx, err := srtp.CreateContext(make([]byte, keyLen), make([]byte, saltLen), benchProfile)
	if err != nil {
		tb.Fatal(err)
	}
	return &srtpWriter{ctx: ctx, plain: make([]byte, 1600), out: make([]byte, 1700)}
}

func (w *srtpWriter) WriteRTP(h *rtp.Header, payload []byte) (int, error) {
	n, err := h.MarshalTo(w.plain)
	if err != nil {
		return 0, err
	}
	n += copy(w.plain[n:], payload)
	out, err := w.ctx.EncryptRTP(w.out[:0], w.plain[:n], h)
	if err != nil {
		return 0, err
	}
	w.n += len(out)
	return len(out), nil
}

func (w *srtpWriter) Write(b []byte) (int, error) { return len(b), nil }

func benchReceiver(tb testing.TB) (*ReceiverImpl, *buffer.Buffer) {
	r := NewReceiver(ReceiverParams{TrackID: "TR_x", StreamID: "PA_x", Kind: webrtc.RTPCodecTypeVideo,
		Codec: webrtc.RTPCodecParameters{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000}}})
	buf := buffer.New(buffer.Config{Slots: 500, ClockRate: 90000, MimeType: webrtc.MimeTypeH264})
	tb.Cleanup(buf.Release)
	return r, buf
}

func boundDownTrack(tb testing.TB, r *ReceiverImpl, id string) *DownTrackImpl {
	d := NewDownTrack(DownTrackParams{SubscriberID: id, Receiver: r, TrackID: "TR_x", StreamID: "PA_x"})
	d.writer = newSRTPWriter(tb)
	d.ssrc, d.pt = 1234, 96
	d.bound.Store(true)
	d.firstPkt.Store(true)
	return d
}

// h264Packet returns a ~1000 byte H.264 packet: an IDR slice (a keyframe start for a
// stream without in-band SPS) or a non-IDR slice.
func h264Packet(sn uint16, key bool) []byte {
	nal := byte(0x41)
	if key {
		nal = 0x65
	}
	payload := append([]byte{nal}, make([]byte, 1000)...)
	p := rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 102, SequenceNumber: sn, Timestamp: uint32(sn) * 3000, SSRC: 1}, Payload: payload}
	b, _ := p.Marshal()
	return b
}

// BenchmarkDownTrackWriteRTP measures munging + header
// marshal + SRTP encryption for one subscriber.
func BenchmarkDownTrackWriteRTP(b *testing.B) {
	r, buf := benchReceiver(b)
	d := boundDownTrack(b, r, "s")
	raw := h264Packet(0, true)
	ext, err := buf.Write(raw, 0)
	if err != nil {
		b.Fatal(err)
	}
	if err := d.WriteRTP(ext); err != nil {
		b.Fatal(err)
	}
	ext.KeyFrame = false
	b.ReportAllocs()
	b.SetBytes(int64(len(raw)))
	for b.Loop() {
		ext.ExtSequenceNumber++
		ext.ExtTimestamp += 3000
		if err := d.WriteRTP(ext); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "pkts/s")
}

// BenchmarkDownTrackWriteRTPGCM is BenchmarkDownTrackWriteRTP with the
// AEAD_AES_128_GCM SRTP profile.
func BenchmarkDownTrackWriteRTPGCM(b *testing.B) {
	old := benchProfile
	benchProfile = srtp.ProtectionProfileAeadAes128Gcm
	defer func() { benchProfile = old }()
	BenchmarkDownTrackWriteRTP(b)
}

// BenchmarkReceiverFanout measures buffer write + fan-out to N DownTracks
// per received packet.
func BenchmarkReceiverFanout(b *testing.B) {
	for _, n := range []int{1, 10, 100, 1000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			r, buf := benchReceiver(b)
			for i := range n {
				if err := r.AddDownTrack(boundDownTrack(b, r, fmt.Sprint(i))); err != nil {
					b.Fatal(err)
				}
			}
			pkts := [2][]byte{h264Packet(0, true), h264Packet(0, false)}
			sn := uint16(0)
			write := func(key bool) {
				p := pkts[1]
				if key {
					p = pkts[0]
				}
				p[2], p[3] = byte(sn>>8), byte(sn)
				sn++
				ext, err := buf.Write(p, int64(sn)*1e6)
				if err != nil {
					b.Fatal(err)
				}
				r.fanout(ext)
			}
			write(true)
			b.ReportAllocs()
			for b.Loop() {
				write(false)
			}
			b.ReportMetric(float64(b.N*n)/b.Elapsed().Seconds(), "egress-pkts/s")
		})
	}
}

func TestForwardingDoesNotAllocate(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector allocates; run without -race")
	}
	r, buf := benchReceiver(t)
	for i := range 3 {
		if err := r.AddDownTrack(boundDownTrack(t, r, fmt.Sprint(i))); err != nil {
			t.Fatal(err)
		}
	}
	sn := uint16(0)
	ext, _ := buf.Write(h264Packet(sn, true), 0)
	r.fanout(ext)
	p := h264Packet(0, false)
	allocs := testing.AllocsPerRun(500, func() {
		sn++
		p[2], p[3] = byte(sn>>8), byte(sn)
		e, err := buf.Write(p, int64(sn)*1e6)
		if err != nil {
			t.Fatal(err)
		}
		r.fanout(e)
	})
	if allocs > 0 {
		t.Fatalf("forwarding allocates %v per packet", allocs)
	}
}
