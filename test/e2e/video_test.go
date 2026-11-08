package e2e

import (
	"testing"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

// videoStream generates one H.264 stream (no simulcast, ADR-0012): 2 packets per
// frame, a keyframe (SPS/PPS, then IDR) every 25 frames, and delta frames between.
type videoStream struct {
	track *webrtc.TrackLocalStaticRTP
	sn    uint16
	ts    uint32
	frame int
	pad   int // extra payload bytes per packet
}

var h264Cap = webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000,
	SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f"}

func newVideoStream(t *testing.T, id string, pad int) *videoStream {
	t.Helper()
	tr, err := webrtc.NewTrackLocalStaticRTP(h264Cap, id, "stream-"+id)
	if err != nil {
		t.Fatal(err)
	}
	return &videoStream{track: tr, sn: 1000, ts: 5000, pad: pad}
}

// writeFrame writes one frame of 2 packets.
func (v *videoStream) writeFrame() {
	first, second := []byte{0x41, 0xab}, []byte{0x41, 0xcd} // non-IDR slices
	if v.frame%25 == 0 {
		first = []byte{0x78, 0, 2, 0x67, 1, 0, 1, 0x68} // STAP-A: SPS, PPS
		second = []byte{0x65, 0xab}                     // IDR slice
	}
	for i, p := range [][]byte{first, second} {
		payload := append(append([]byte(nil), p...), make([]byte, v.pad)...)
		_ = v.track.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: v.sn, Timestamp: v.ts, Marker: i == 1}, Payload: payload})
		v.sn++
	}
	v.frame++
	v.ts += 3000
}
