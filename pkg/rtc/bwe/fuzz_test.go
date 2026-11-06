package bwe

import (
	"testing"
	"time"

	"github.com/pion/rtcp"
)

// Transport-wide feedback comes from subscribers and is untrusted: any RTCP the
// parser accepts must be safe for the estimator.
func FuzzEstimatorFeedback(f *testing.F) {
	seed := &rtcp.TransportLayerCC{SenderSSRC: 1, MediaSSRC: 2, BaseSequenceNumber: 10, PacketStatusCount: 3, FbPktCount: 1,
		PacketChunks: []rtcp.PacketStatusChunk{&rtcp.RunLengthChunk{PacketStatusSymbol: rtcp.TypeTCCPacketReceivedSmallDelta, RunLength: 3}},
		RecvDeltas:   []*rtcp.RecvDelta{{Type: rtcp.TypeTCCPacketReceivedSmallDelta, Delta: 1000}, {Type: rtcp.TypeTCCPacketReceivedSmallDelta, Delta: 1000}, {Type: rtcp.TypeTCCPacketReceivedSmallDelta, Delta: 1000}}}
	if b, err := seed.Marshal(); err == nil {
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		pkts, err := rtcp.Unmarshal(data)
		if err != nil {
			return
		}
		e := NewEstimator(Config{}).(*estimator)
		for s := range uint16(64) {
			e.OnPacketSent(s, 1200, int64(s)*1e6, s%7 == 0)
		}
		e.StartProbe()
		now := time.Unix(1000, 0)
		for _, p := range pkts {
			if fb, ok := p.(*rtcp.TransportLayerCC); ok {
				now = now.Add(50 * time.Millisecond)
				e.OnTransportFeedback(fb, now)
			}
		}
		e.FinishProbe(now)
		_, _ = e.ProbeResult()
		if est := e.Estimate(); est < e.cfg.MinBitrate || est > e.cfg.MaxBitrate {
			t.Fatalf("estimate %d outside [%d, %d]", est, e.cfg.MinBitrate, e.cfg.MaxBitrate)
		}
	})
}
