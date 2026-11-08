package bwe

import (
	"testing"
	"time"

	"github.com/pion/rtcp"
)

// After an outage, while only audio is acknowledged, the acked-rate cap used to pull
// the estimate below the video stream: video paused, and with nothing forwarding to
// carry probe padding it stayed paused for good. It must regain the stream.
func TestVideoNotStuckPausedAfterOutage(t *testing.T) {
	e := NewEstimator(Config{}).(*estimator)
	a := NewAllocator(Config{Enabled: true, AllowPause: true, Probe: true}, e).(*allocator)
	clock := time.Now()
	a.now = func() time.Time { return clock }
	audio := &fakeTrack{id: "a", rate: 40_000}
	cam := video("cam", 1, 450_000) // 360p
	a.AddTrack(audio)
	a.AddTrack(cam)
	clock = feedLoss(e, clock, 0, 60, func(int) bool { return false })
	a.Allocate()
	clock = feedLoss(e, clock, 60, 12, func(int) bool { return true }) // outage
	a.Allocate()
	seq := uint16(60000)
	for i := 0; i < 200; i++ { // 60 s of audio-only feedback
		fb := &rtcp.TransportLayerCC{BaseSequenceNumber: seq, PacketStatusCount: 5, ReferenceTime: uint32(i * 5)}
		for s := 0; s < 5; s++ {
			e.OnPacketSent(seq, 120, int64(i*300+s*20)*1e6+1e12, false)
			seq++
			fb.RecvDeltas = append(fb.RecvDeltas, &rtcp.RecvDelta{Type: rtcp.TypeTCCPacketReceivedSmallDelta, Delta: 20_000})
		}
		fb.PacketChunks = []rtcp.PacketStatusChunk{&rtcp.RunLengthChunk{PacketStatusSymbol: rtcp.TypeTCCPacketReceivedSmallDelta, RunLength: 5}}
		clock = clock.Add(300 * time.Millisecond)
		e.OnTransportFeedback(fb, clock)
		a.Allocate()
	}
	if cam.paused {
		t.Fatalf("video paused after 60 s of audio-only feedback, estimate %d", e.Estimate())
	}
}
