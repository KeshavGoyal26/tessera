package recorder

import (
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"

	"github.com/KeshavGoyal26/tessera/pkg/rtc"
)

// The tap is written on the forwarding path: a recorder that stops reading must cost
// dropped packets, never a blocked publisher.
func TestTapNeverBlocksForwarding(t *testing.T) {
	tp := &tap{ch: make(chan *rtp.Packet, 4), done: make(chan struct{})}
	finished := make(chan struct{})
	go func() {
		for i := range 100 {
			_, _ = tp.WriteRTP(&rtp.Header{Version: 2, SequenceNumber: uint16(i)}, []byte{1, 2, 3})
		}
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("WriteRTP blocked with a full queue")
	}
	if d := tp.dropped.Load(); d != 96 {
		t.Fatalf("dropped %d, want 96", d)
	}
	// Queued packets are still delivered after the stream ends, then EOF.
	tp.end()
	n := 0
	for {
		if _, err := tp.ReadPacket(); errors.Is(err, io.EOF) {
			break
		}
		n++
	}
	if n != 4 {
		t.Fatalf("read %d packets after end, want 4", n)
	}
}

// chanSource feeds a receiver layer from a channel (a simulcast layer of a publisher).
type chanSource struct {
	ssrc uint32
	ch   chan []byte
}

func (s chanSource) Read(b []byte) (int, error) {
	p, ok := <-s.ch
	if !ok {
		return 0, io.EOF
	}
	return copy(b, p), nil
}
func (s chanSource) SSRC() uint32 { return s.ssrc }
func (chanSource) RID() string    { return "" }

// vp8 returns a VP8 packet of layer `layer` (its first payload byte after the
// descriptor names the layer, so the test can tell which layer the tap forwards).
func vp8(ssrc uint32, sn uint16, key bool, layer byte) []byte {
	first := byte(0x01) // inter frame
	if key {
		first = 0x00
	}
	payload := append([]byte{0x90, 0x80, 0x80, byte(sn), first, layer}, make([]byte, 600)...)
	p := rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: sn, Timestamp: uint32(sn) * 3000, SSRC: ssrc, Marker: true}, Payload: payload}
	b, _ := p.Marshal()
	return b
}

// When the publisher pauses its top simulcast layer (short uplink), the recorder must
// move to the best layer still arriving instead of waiting for the top one: a live
// subscriber's allocator does this, the recorder has none (regression: minutes of
// recordings without video under a throttled uplink, while viewers saw video).
func TestTapFollowsAvailableLayers(t *testing.T) {
	srcs := [2]chanSource{{ssrc: 100, ch: make(chan []byte, 64)}, {ssrc: 200, ch: make(chan []byte, 64)}}
	var keyWanted [2]atomic.Bool
	recv := rtc.NewReceiver(rtc.ReceiverParams{TrackID: "TR_v", StreamID: "PA_p", Kind: webrtc.RTPCodecTypeVideo,
		Codec: webrtc.RTPCodecParameters{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000}, PayloadType: 96},
		WriteRTCP: func(pkts []rtcp.Packet) error {
			for _, p := range pkts {
				if pli, ok := p.(*rtcp.PictureLossIndication); ok {
					for i, s := range srcs {
						if s.ssrc == pli.MediaSSRC {
							keyWanted[i].Store(true) // the publisher answers a PLI with a keyframe
						}
					}
				}
			}
			return nil
		}})
	defer recv.Close()
	for i, s := range srcs {
		if err := recv.AddLayer(int32(i), s, nil); err != nil {
			t.Fatal(err)
		}
	}
	tp, err := newTap(recv, "recorder", "TR_v", "p")
	if err != nil {
		t.Fatal(err)
	}
	defer tp.close()

	var sn [2]uint16
	send := func(layers ...int) {
		for _, l := range layers {
			sn[l]++
			srcs[l].ch <- vp8(srcs[l].ssrc, sn[l], sn[l] == 1 || keyWanted[l].Swap(false), byte(l))
		}
	}
	var got [2]int
	drain := func() {
		for {
			select {
			case p := <-tp.ch:
				if len(p.Payload) > 5 {
					got[p.Payload[5]]++
				}
			default:
				return
			}
		}
	}
	run := func(d time.Duration, layers ...int) {
		for end := time.Now().Add(d); time.Now().Before(end); {
			send(layers...)
			time.Sleep(20 * time.Millisecond)
			drain()
		}
	}

	run(2*time.Second, 0, 1) // both layers: the top one is recorded
	if got[1] == 0 || got[0] != 0 {
		t.Fatalf("both layers sent: recorded %v packets per layer, want only layer 1", got)
	}
	got = [2]int{}
	run(3*time.Second, 0) // top layer paused by the publisher
	if got[0] == 0 {
		t.Fatalf("top layer paused: recorded %v packets per layer, want layer 0 to be recorded", got)
	}
	got = [2]int{}
	run(8*time.Second, 0, 1) // top layer back (after upgradeHold): the recorder returns to it
	if got[1] == 0 {
		t.Fatalf("top layer resumed: recorded %v packets per layer, want layer 1 again", got)
	}
}
