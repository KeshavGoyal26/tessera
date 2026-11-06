package rtc

import (
	"bytes"
	"testing"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"

	"github.com/KeshavGoyal26/tessera/pkg/rtc/buffer"
)

func TestRedPrimary(t *testing.T) {
	prev, cur := []byte{1, 2, 3}, []byte{9, 8, 7, 6}
	// One redundant block (PT 111, ts offset 960, length 3), then the primary.
	red := append([]byte{0x80 | 111, 15, 0, 3, 111}, append(prev, cur...)...) // ts offset 960 = 15<<6
	if p, ok := redPrimary(red); !ok || !bytes.Equal(p, cur) {
		t.Fatalf("one redundant block: %v %v", p, ok)
	}
	// No redundancy (first packet of a stream): only the final header.
	if p, ok := redPrimary(append([]byte{111}, cur...)); !ok || !bytes.Equal(p, cur) {
		t.Fatalf("primary only: %v %v", p, ok)
	}
	for _, bad := range [][]byte{nil, {0x80 | 111, 0, 0}, {0x80 | 111, 0, 0, 50, 111, 1}} {
		if _, ok := redPrimary(bad); ok {
			t.Fatalf("malformed %v accepted", bad)
		}
	}
}

// RED payloads come from publishers: never panic, and the primary is a suffix.
func FuzzRedPrimary(f *testing.F) {
	f.Add([]byte{0x80 | 111, 15, 0, 3, 111, 1, 2, 3, 9, 8})
	f.Fuzz(func(t *testing.T, b []byte) {
		if p, ok := redPrimary(b); ok && !bytes.HasSuffix(b, p) {
			t.Fatalf("primary %v is not a suffix of %v", p, b)
		}
	})
}

type fakeLocalCtx struct {
	codecs []webrtc.RTPCodecParameters
	w      *captureWriter
}

func (c fakeLocalCtx) CodecParameters() []webrtc.RTPCodecParameters         { return c.codecs }
func (fakeLocalCtx) HeaderExtensions() []webrtc.RTPHeaderExtensionParameter { return nil }
func (fakeLocalCtx) SSRC() webrtc.SSRC                                      { return 5 }
func (fakeLocalCtx) SSRCRetransmission() webrtc.SSRC                        { return 0 }
func (fakeLocalCtx) SSRCForwardErrorCorrection() webrtc.SSRC                { return 0 }
func (c fakeLocalCtx) WriteStream() webrtc.TrackLocalWriter                 { return c.w }
func (fakeLocalCtx) ID() string                                             { return "x" }
func (fakeLocalCtx) RTCPReader() interceptor.RTCPReader                     { return nil }

type captureWriter struct {
	pt      uint8
	payload []byte
}

func (w *captureWriter) WriteRTP(h *rtp.Header, p []byte) (int, error) {
	w.pt, w.payload = h.PayloadType, append([]byte(nil), p...)
	return len(p), nil
}
func (w *captureWriter) Write(b []byte) (int, error) { return len(b), nil }

// A RED publisher reaches RED subscribers unchanged and the others as plain Opus.
func TestDownTrackRedPassThroughAndStrip(t *testing.T) {
	opus := webrtc.RTPCodecParameters{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, PayloadType: 111}
	red := redCodec
	primary := []byte{9, 8, 7, 6}
	payload := append([]byte{0x80 | 111, 15, 0, 3, 111, 1, 2, 3}, primary...)
	for _, tc := range []struct {
		name    string
		offered []webrtc.RTPCodecParameters
		pt      uint8
		want    []byte
	}{
		{"red subscriber", []webrtc.RTPCodecParameters{opus, red}, 63, payload},
		{"opus-only subscriber", []webrtc.RTPCodecParameters{opus}, 111, primary},
	} {
		r := NewReceiver(ReceiverParams{TrackID: "TR_a", StreamID: "PA_a", Kind: webrtc.RTPCodecTypeAudio, Codec: red})
		dt := NewDownTrack(DownTrackParams{SubscriberID: "PA_s", Receiver: r, TrackID: "TR_a", StreamID: "PA_a"})
		w := &captureWriter{}
		got, err := dt.Bind(fakeLocalCtx{codecs: tc.offered, w: w})
		if err != nil || got.PayloadType != webrtc.PayloadType(tc.pt) {
			t.Fatalf("%s: bound %v (pt %d), err %v", tc.name, got.MimeType, got.PayloadType, err)
		}
		buf := buffer.New(buffer.Config{Slots: 16, ClockRate: 48000, MimeType: rtcRED})
		raw, _ := (&rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 63, SequenceNumber: 1, Timestamp: 960, SSRC: 9}, Payload: payload}).Marshal()
		e, err := buf.Write(raw, 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := dt.WriteRTP(e, 0); err != nil {
			t.Fatal(err)
		}
		if w.pt != tc.pt || !bytes.Equal(w.payload, tc.want) {
			t.Fatalf("%s: wrote pt %d payload %v, want pt %d payload %v", tc.name, w.pt, w.payload, tc.pt, tc.want)
		}
		r.Close()
	}
}

const rtcRED = MimeTypeRED
