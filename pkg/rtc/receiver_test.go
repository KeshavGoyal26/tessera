package rtc

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"

	"github.com/KeshavGoyal26/tessera/pkg/rtc/buffer"
	"github.com/KeshavGoyal26/tessera/pkg/rtc/types"
)

type closeCountingDown struct {
	id     string
	closed atomic.Bool
}

func (d *closeCountingDown) Bind(webrtc.TrackLocalContext) (webrtc.RTPCodecParameters, error) {
	return webrtc.RTPCodecParameters{}, nil
}
func (d *closeCountingDown) Unbind(webrtc.TrackLocalContext) error   { return nil }
func (d *closeCountingDown) ID() string                              { return "TR_x" }
func (d *closeCountingDown) RID() string                             { return "" }
func (d *closeCountingDown) StreamID() string                        { return "PA_x" }
func (d *closeCountingDown) Kind() webrtc.RTPCodecType               { return webrtc.RTPCodecTypeVideo }
func (d *closeCountingDown) SubscriberID() string                    { return d.id }
func (d *closeCountingDown) WriteRTP(*buffer.ExtPacket, int32) error { return nil }
func (d *closeCountingDown) SetMaxLayer(types.VideoLayer)            {}
func (d *closeCountingDown) SetTargetLayer(types.VideoLayer)         {}
func (d *closeCountingDown) CurrentLayer() types.VideoLayer          { return types.VideoLayer{} }
func (d *closeCountingDown) Mute(bool)                               {}
func (d *closeCountingDown) HandleRTCP([]rtcp.Packet)                {}
func (d *closeCountingDown) Close()                                  { d.closed.Store(true) }

// A Close that runs between AddDownTrack's fast check and taking dtMu must
// still close the DownTrack (regression: the check only ran outside dtMu).
func TestReceiverCloseDuringAddDownTrack(t *testing.T) {
	r := NewReceiver(ReceiverParams{TrackID: "TR_x", StreamID: "PA_x", Kind: webrtc.RTPCodecTypeVideo,
		Codec: webrtc.RTPCodecParameters{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000}}})
	testHookAddDownTrack = r.Close
	t.Cleanup(func() { testHookAddDownTrack = func() {} })
	d := &closeCountingDown{id: "a"}
	if err := r.AddDownTrack(d); err == nil && !d.closed.Load() {
		t.Fatal("AddDownTrack accepted a DownTrack after Close and it was never closed")
	}
}

// Every DownTrack that AddDownTrack accepts must be closed by a concurrent
// Receiver.Close (regression: the closed check ran outside dtMu).
func TestReceiverCloseClosesConcurrentlyAddedDownTracks(t *testing.T) {
	for range 200 {
		r := NewReceiver(ReceiverParams{TrackID: "TR_x", StreamID: "PA_x", Kind: webrtc.RTPCodecTypeVideo,
			Codec: webrtc.RTPCodecParameters{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000}}})
		var accepted []*closeCountingDown
		var mu sync.Mutex
		var wg sync.WaitGroup
		for i := range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				d := &closeCountingDown{id: string(rune('a' + i))}
				if r.AddDownTrack(d) == nil {
					mu.Lock()
					accepted = append(accepted, d)
					mu.Unlock()
				}
			}()
		}
		r.Close()
		wg.Wait()
		for _, d := range accepted {
			if !d.closed.Load() {
				t.Fatalf("DownTrack %s accepted but never closed", d.id)
			}
		}
	}
}
