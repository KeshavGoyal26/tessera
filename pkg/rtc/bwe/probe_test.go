package bwe

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/rtcp"

	"github.com/KeshavGoyal26/tessera/pkg/rtc/types"
)

// runProbe sends n packets of size bytes every sendGap as one probe cluster and
// acknowledges them all in one feedback with arrivals every recvGap.
func runProbe(t *testing.T, e *estimator, n, size int, sendGap, recvGap time.Duration) {
	t.Helper()
	e.StartProbe()
	fb := &rtcp.TransportLayerCC{BaseSequenceNumber: 0, PacketStatusCount: uint16(n),
		PacketChunks: []rtcp.PacketStatusChunk{&rtcp.RunLengthChunk{PacketStatusSymbol: rtcp.TypeTCCPacketReceivedSmallDelta, RunLength: uint16(n)}}}
	for i := range n {
		e.OnPacketSent(uint16(i), size, int64(i)*int64(sendGap), true)
		fb.RecvDeltas = append(fb.RecvDeltas, &rtcp.RecvDelta{Type: rtcp.TypeTCCPacketReceivedSmallDelta, Delta: recvGap.Microseconds()})
	}
	now := time.Now()
	e.FinishProbe(now)
	e.OnTransportFeedback(fb, now.Add(50*time.Millisecond))
}

// A probe the path carries lifts the estimate past the "1.5x acknowledged" bound
// that otherwise holds a subscriber below its next layer.
func TestProbeRaisesEstimate(t *testing.T) {
	e := NewEstimator(Config{}).(*estimator)
	runProbe(t, e, 250, 1000, 2*time.Millisecond, 2*time.Millisecond) // 4 Mbit/s for 0.5 s
	done, res := e.ProbeResult()
	if !done || res < 3_500_000 || res > 4_000_000 {
		t.Fatalf("probe result done=%v %d, want about 0.95 x 4 Mbit/s", done, res)
	}
	if got := e.Estimate(); got != res {
		t.Fatalf("estimate %d, want the probe result %d", got, res)
	}
}

// A path that delivers the probe at half the rate it was sent did not have the
// capacity: no jump.
func TestProbeFailsWhenPathIsSlower(t *testing.T) {
	e := NewEstimator(Config{}).(*estimator)
	start := e.Estimate()
	runProbe(t, e, 250, 1000, 2*time.Millisecond, 4*time.Millisecond)
	done, res := e.ProbeResult()
	if !done || res != 0 {
		t.Fatalf("probe result done=%v %d, want a failed probe", done, res)
	}
	if got := e.Estimate(); got > start {
		t.Fatalf("estimate rose from %d to %d after a failed probe", start, got)
	}
}

type probeEst struct {
	fakeEst
	mu                sync.Mutex
	started, finished int
	done              bool
	result            int64
}

func (p *probeEst) StartProbe() { p.mu.Lock(); p.started++; p.done = false; p.mu.Unlock() }
func (p *probeEst) FinishProbe(time.Time) {
	p.mu.Lock()
	p.finished++
	p.mu.Unlock()
}
func (p *probeEst) ProbeResult() (bool, int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.done, p.result
}
func (p *probeEst) counts() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.started, p.finished
}

type padTrack struct {
	*fakeTrack
	padded atomic.Int64
}

func (p *padTrack) WritePadding(n int) int { p.padded.Add(int64(n)); return n }

func TestAllocatorProbesForNextLayer(t *testing.T) {
	est := &probeEst{fakeEst: fakeEst{bps: 1_500_000}}
	a := NewAllocator(Config{Enabled: true, AllowPause: true, Probe: true, UpgradeHold: 3 * time.Second}, est).(*allocator)
	clock := time.Unix(1000, 0)
	a.now = func() time.Time { return clock }
	defer a.Stop()
	cam := &padTrack{fakeTrack: video("cam", 1, 150_000, 600_000, 2_000_000)}
	a.AddTrack(&fakeTrack{id: "a", rates: types.Bitrates{{50_000, 50_000, 50_000, 50_000}}})
	a.AddTrack(cam)

	a.Allocate()
	if cam.target.Spatial != 1 {
		t.Fatalf("1.5 Mbit/s should give the middle layer, got %v", cam.target)
	}
	if s, _ := est.counts(); s != 1 {
		t.Fatalf("a starved track must start a probe, started=%d", s)
	}
	deadline := time.Now().Add(3 * time.Second)
	for _, f := est.counts(); f == 0 && time.Now().Before(deadline); _, f = est.counts() {
		time.Sleep(20 * time.Millisecond)
	}
	// Padding for 20% above the upgrade: (650k + 1.4M) * 1.2 - 650k = 1.81 Mbit/s for 0.5 s.
	if p := cam.padded.Load(); p < 250 || p > 600 {
		t.Fatalf("padded %d packets, want about %d", p, 1_810_000/2/8/PaddingPacketBytes)
	}

	// The probe succeeded: the estimate is high enough and the upgrade skips the hold.
	est.mu.Lock()
	est.done, est.result, est.bps = true, 3_000_000, 3_000_000
	est.mu.Unlock()
	clock = clock.Add(allocTick)
	a.Allocate() // registers the result
	clock = clock.Add(allocTick)
	a.Allocate()
	if cam.target.Spatial != 2 {
		t.Fatalf("after a successful probe the top layer should follow at once, got %v", cam.target)
	}
	if started, ok := a.ProbeStats(); started != 1 || ok != 1 {
		t.Fatalf("probe stats %d/%d", started, ok)
	}
	// Nothing left to gain: no further probes.
	clock = clock.Add(5 * time.Second)
	a.Allocate()
	if s, _ := est.counts(); s != 1 {
		t.Fatalf("no deficit must mean no probe, started=%d", s)
	}
}

func TestAllocatorBacksOffAfterFailedProbe(t *testing.T) {
	est := &probeEst{fakeEst: fakeEst{bps: 1_500_000}}
	a := NewAllocator(Config{Enabled: true, AllowPause: true, Probe: true}, est).(*allocator)
	clock := time.Unix(1000, 0)
	a.now = func() time.Time { return clock }
	defer a.Stop()
	cam := &padTrack{fakeTrack: video("cam", 1, 150_000, 600_000, 2_000_000)}
	a.AddTrack(cam)
	a.Allocate()
	est.mu.Lock()
	est.done, est.result = true, 0 // failed
	est.mu.Unlock()
	clock = clock.Add(allocTick)
	a.Allocate() // failure registered: back off 4 s
	for range 6 {
		clock = clock.Add(allocTick)
		a.Allocate()
	}
	if s, _ := est.counts(); s != 1 {
		t.Fatalf("probed again within the back-off, started=%d", s)
	}
	clock = clock.Add(2 * time.Second)
	a.Allocate()
	if s, _ := est.counts(); s != 2 {
		t.Fatalf("expected a retry after the back-off, started=%d", s)
	}
	if !cam.target.IsValid() || cam.target.Spatial != 1 {
		t.Fatalf("a failed probe must not change the allocation, got %v", cam.target)
	}
}
