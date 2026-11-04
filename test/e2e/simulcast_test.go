package e2e

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/client"
	"github.com/KeshavGoyal26/tessera/pkg/rtc/buffer"
)

// simLayer generates VP8 packets with picture id, TL0PICIDX and TID for one
// simulcast layer; payload byte after the VP8 header carries the layer.
type simLayer struct {
	track *webrtc.TrackLocalStaticRTP
	layer byte
	sn    uint16
	ts    uint32
	pid   uint16
	tl0   uint8
	frame int
	pad   int // extra payload bytes per packet
}

func newSimLayers(t *testing.T, id string) []*simLayer {
	t.Helper()
	var out []*simLayer
	for i, rid := range []string{"q", "h", "f"} {
		tr, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000}, id, "stream-"+id, webrtc.WithRTPStreamID(rid))
		if err != nil {
			t.Fatal(err)
		}
		// Different SN/TS/picture-id bases per layer exercise the munger.
		out = append(out, &simLayer{track: tr, layer: byte(i), sn: uint16(1000 + 20000*i), ts: uint32(5000 + 1_000_000_000*i), pid: uint16(100 + 10000*i)})
	}
	return out
}

// frame writes one frame of 2 packets.
func (l *simLayer) writeFrame() {
	key := l.frame%25 == 0
	tid := byte(l.frame % 2)
	for i := range 2 {
		s := byte(0)
		if i == 0 {
			s = 0x10
		}
		vp8 := byte(0x01)
		if key {
			vp8 = 0x00
		}
		payload := []byte{0x80 | s, 0xE0, 0x80 | byte(l.pid>>8), byte(l.pid), l.tl0, tid << 6, vp8, l.layer, 0xab, 0xcd}
		payload = append(payload, make([]byte, l.pad)...)
		_ = l.track.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: l.sn, Timestamp: l.ts, Marker: i == 1}, Payload: payload})
		l.sn++
	}
	l.frame++
	l.ts += 3000
	l.pid = (l.pid + 1) & 0x7fff
	if tid == 0 {
		l.tl0++
	}
}

func TestSimulcastSwitching(t *testing.T) {
	s := startServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pubC := connect(t, s, "sim", "pub")
	layers := newSimLayers(t, "cam")
	locals := []webrtc.TrackLocal{layers[0].track, layers[1].track, layers[2].track}
	info, err := pubC.PublishSimulcastTrack(ctx, locals, client.PublishOptions{Name: "cam", Width: 1280, Height: 720})
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		tk := time.NewTicker(20 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tk.C:
				for _, l := range layers {
					l.writeFrame()
				}
			}
		}
	}()
	t.Cleanup(func() { close(stop); wg.Wait() })

	sub := connect(t, s, "sim", "sub")
	rt, err := client.WaitTrack(ctx, sub, info.Sid)
	if err != nil {
		t.Fatal(err)
	}
	type pkt struct {
		sn    uint16
		ts    uint32
		pid   uint16
		layer byte
		start bool
	}
	pkts := make(chan pkt, 4096)
	go func() {
		b := make([]byte, 1500)
		for {
			n, err := rt.Read(b)
			if err != nil {
				close(pkts)
				return
			}
			var p rtp.Packet
			if p.Unmarshal(b[:n]) != nil {
				continue
			}
			var d buffer.VP8Descriptor
			if buffer.ParseVP8(p.Payload, &d) != nil || len(p.Payload) < d.HeaderSize+2 {
				t.Errorf("bad VP8 payload %x", p.Payload)
				continue
			}
			pkts <- pkt{p.SequenceNumber, p.Timestamp, d.PictureID, p.Payload[d.HeaderSize+1], p.Payload[0]&0x10 != 0}
		}
	}()

	var last *pkt
	seenLayers := map[byte]bool{}
	var order []byte
	collect := func(want byte, dur time.Duration) {
		t.Helper()
		deadline := time.After(dur)
		reached := false
		for {
			select {
			case p, ok := <-pkts:
				if !ok {
					t.Fatal("track ended")
				}
				if last != nil {
					if p.sn != last.sn+1 {
						t.Fatalf("SN discontinuity %d -> %d (layer %d -> %d)", last.sn, p.sn, last.layer, p.layer)
					}
					if int32(p.ts-last.ts) < 0 {
						t.Fatalf("TS went back %d -> %d", last.ts, p.ts)
					}
					if p.pid != last.pid && p.pid != (last.pid+1)&0x7fff {
						t.Fatalf("picture id jump %d -> %d (layer %d -> %d)", last.pid, p.pid, last.layer, p.layer)
					}
				}
				if len(order) == 0 || order[len(order)-1] != p.layer {
					order = append(order, p.layer)
				}
				seenLayers[p.layer] = true
				cp := p
				last = &cp
				if p.layer == want {
					reached = true
				}
			case <-deadline:
				if !reached {
					t.Fatalf("layer %d not reached; order %v", want, order)
				}
				return
			}
		}
	}
	// Default: highest layer.
	collect(2, 3*time.Second)
	for _, step := range []struct {
		q    tesserav1.VideoQuality
		want byte
	}{{tesserav1.VideoQuality_VIDEO_QUALITY_LOW, 0}, {tesserav1.VideoQuality_VIDEO_QUALITY_HIGH, 2}, {tesserav1.VideoQuality_VIDEO_QUALITY_MEDIUM, 1}} {
		if err := sub.UpdateTrackSettings(ctx, &tesserav1.UpdateTrackSettings{TrackSids: []string{info.Sid}, Quality: step.q}); err != nil {
			t.Fatal(err)
		}
		collect(step.want, 2*time.Second)
		if last.layer != step.want {
			t.Fatalf("ended on layer %d, want %d (order %v)", last.layer, step.want, order)
		}
	}
	t.Logf("layer order: %v", order)
	snap := s.Manager.Snapshot()
	for _, p := range snap.Rooms[0].Participants {
		if p.Identity == "pub" && len(p.Published[0].Layers) != 3 {
			t.Fatalf("published layers %+v", p.Published[0].Layers)
		}
		if p.Identity == "sub" && p.Subscribed[0].LayerSwitches < 4 {
			t.Fatalf("switches %d", p.Subscribed[0].LayerSwitches)
		}
	}
}
