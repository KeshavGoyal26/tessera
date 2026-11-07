package rtc

import (
	"testing"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"

	"github.com/KeshavGoyal26/tessera/pkg/rtc/buffer"
	"github.com/KeshavGoyal26/tessera/pkg/rtc/bwe"
)

// testBindContext is fakeLocalCtx (red_test.go) with transport-wide CC, which probe
// padding needs.
type testBindContext struct {
	codec webrtc.RTPCodecParameters
	w     *captureWriter
}

func (c testBindContext) CodecParameters() []webrtc.RTPCodecParameters {
	return []webrtc.RTPCodecParameters{c.codec}
}
func (testBindContext) HeaderExtensions() []webrtc.RTPHeaderExtensionParameter {
	return []webrtc.RTPHeaderExtensionParameter{{URI: extTWCC, ID: 3}}
}
func (testBindContext) SSRC() webrtc.SSRC                       { return 7 }
func (testBindContext) SSRCRetransmission() webrtc.SSRC         { return 0 }
func (testBindContext) SSRCForwardErrorCorrection() webrtc.SSRC { return 0 }
func (c testBindContext) WriteStream() webrtc.TrackLocalWriter  { return c.w }
func (testBindContext) ID() string                              { return "sub" }
func (testBindContext) RTCPReader() interceptor.RTCPReader      { return nil }

// Probe padding may only go between frames. A recovered (late) packet that ended an
// older frame must not open that gap: padding inserted while the newest frame is half
// sent splits it, the subscriber can never assemble it nor any frame after it, and the
// video freezes until the next keyframe (regression: seconds of frozen video at 5%
// uplink loss whenever the server probed).
func TestPaddingNotInsertedAfterLateMarker(t *testing.T) {
	codec := webrtc.RTPCodecParameters{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000}, PayloadType: 102}
	r := NewReceiver(ReceiverParams{TrackID: "TR_v", StreamID: "PA_p", Kind: webrtc.RTPCodecTypeVideo, Codec: codec})
	defer r.Close()
	buf := buffer.New(buffer.Config{Slots: 64, ClockRate: 90000, MimeType: webrtc.MimeTypeH264})
	src := blockingSource{done: make(chan struct{})}
	defer close(src.done)
	if err := r.AddLayer(0, src, buf); err != nil {
		t.Fatal(err)
	}
	dt := NewDownTrack(DownTrackParams{SubscriberID: "PA_s", Receiver: r, TrackID: "TR_v", StreamID: "PA_p",
		TWCC: &TWCCSender{Est: bwe.NewEstimator(bwe.Config{})}})
	w := &captureWriter{}
	if _, err := dt.Bind(testBindContext{codec: codec, w: w}); err != nil {
		t.Fatal(err)
	}
	forward := func(sn uint16, ts uint32, marker bool, payload []byte) {
		p := rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 102, SequenceNumber: sn, Timestamp: ts, SSRC: 42, Marker: marker}, Payload: payload}
		raw, _ := p.Marshal()
		e, err := buf.Write(raw, int64(sn)*1e6)
		if err != nil {
			t.Fatal(err)
		}
		if err := dt.WriteRTP(e, 0); err != nil {
			t.Fatal(err)
		}
	}
	sps := []byte{0x78, 0, 2, 0x67, 1, 0, 1, 0x68}
	forward(10, 3000, false, sps)                   // keyframe: SPS+PPS
	forward(11, 3000, false, []byte{0x7c, 0x85, 1}) // IDR start; 12 (IDR end, marker) is lost
	forward(13, 6000, false, []byte{0x7c, 0x81, 1}) // next frame starts
	forward(12, 3000, true, []byte{0x7c, 0x45, 1})  // 12 recovered late: it ends the older frame
	if n := dt.WritePadding(2); n != 0 {
		t.Fatalf("padded %d packets in the middle of a frame", n)
	}
	forward(14, 6000, true, []byte{0x7c, 0x41, 1}) // the newest frame ends
	if n := dt.WritePadding(2); n != 2 {
		t.Fatalf("padded %d packets between frames, want 2", n)
	}
}
