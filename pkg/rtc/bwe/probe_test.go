package bwe

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/rtcp"
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

// Two cameras of 450 kbit/s on a 900 kbit/s estimate: one is paused, and the other
// carries probe padding for what resuming it needs.
func newProbeSetup(t *testing.T, cfg Config) (*probeEst, *allocator, *padTrack, *padTrack, *time.Time) {
	t.Helper()
	est := &probeEst{fakeEst: fakeEst{bps: 900_000}}
	a := NewAllocator(cfg, est).(*allocator)
	clock := time.Unix(1000, 0)
	a.now = func() time.Time { return clock }
	t.Cleanup(a.Stop)
	cam1 := &padTrack{fakeTrack: video("cam1", 2, 450_000)}
	cam2 := &padTrack{fakeTrack: video("cam2", 1, 450_000)}
	a.AddTrack(&fakeTrack{id: "a", rate: 50_000})
	a.AddTrack(cam1)
	a.AddTrack(cam2)
	a.Allocate()
	if cam1.paused || !cam2.paused {
		t.Fatalf("900 kbit/s should carry one camera: cam1 paused=%v cam2 paused=%v", cam1.paused, cam2.paused)
	}
	return est, a, cam1, cam2, &clock
}

func TestAllocatorProbesToResumePausedTrack(t *testing.T) {
	est, a, cam1, cam2, clock := newProbeSetup(t, Config{Enabled: true, AllowPause: true, Probe: true})
	if s, _ := est.counts(); s != 1 {
		t.Fatalf("a paused track must start a probe, started=%d", s)
	}
	deadline := time.Now().Add(3 * time.Second)
	for _, f := est.counts(); f == 0 && time.Now().Before(deadline); _, f = est.counts() {
		time.Sleep(20 * time.Millisecond)
	}
	// Padding for 20% above resuming: (500k + 450k) * 1.2 - 500k = 640 kbit/s for 0.5 s,
	// on the forwarding camera.
	if p := cam1.padded.Load(); p < 80 || p > 200 {
		t.Fatalf("padded %d packets, want about %d", p, 640_000/2/8/PaddingPacketBytes)
	}
	if cam2.padded.Load() != 0 {
		t.Fatal("padding went to the paused track")
	}
	est.mu.Lock()
	est.done, est.result, est.bps = true, 1_500_000, 1_500_000
	est.mu.Unlock()
	*clock = clock.Add(allocTick)
	a.Allocate() // registers the result
	*clock = clock.Add(allocTick)
	a.Allocate()
	if cam2.paused {
		t.Fatal("after a successful probe the paused camera should resume")
	}
	if started, ok := a.ProbeStats(); started != 1 || ok != 1 {
		t.Fatalf("probe stats %d/%d", started, ok)
	}
	*clock = clock.Add(5 * time.Second)
	a.Allocate()
	if s, _ := est.counts(); s != 1 {
		t.Fatalf("nothing paused must mean no probe, started=%d", s)
	}
}

func TestAllocatorBacksOffAfterFailedProbe(t *testing.T) {
	est, a, _, cam2, clock := newProbeSetup(t, Config{Enabled: true, AllowPause: true, Probe: true})
	est.mu.Lock()
	est.done, est.result = true, 0 // failed
	est.mu.Unlock()
	*clock = clock.Add(allocTick)
	a.Allocate() // failure registered: back off 4 s
	for range 6 {
		*clock = clock.Add(allocTick)
		a.Allocate()
	}
	if s, _ := est.counts(); s != 1 {
		t.Fatalf("probed again within the back-off, started=%d", s)
	}
	*clock = clock.Add(2 * time.Second)
	a.Allocate()
	if s, _ := est.counts(); s != 2 {
		t.Fatalf("expected a retry after the back-off, started=%d", s)
	}
	if !cam2.paused {
		t.Fatal("a failed probe must not resume the track")
	}
}

// A probe whose result falls short of what resuming needs (a capped link) is not a
// success: re-probing every tick kept such a link congested and the track flapping.
func TestAllocatorBacksOffWhenProbeFallsShort(t *testing.T) {
	est, a, _, _, clock := newProbeSetup(t, Config{Enabled: true, AllowPause: true, Probe: true})
	est.mu.Lock()
	est.done, est.result = true, 900_000 // more than now, but not the 950 kbit/s needed
	est.mu.Unlock()
	*clock = clock.Add(allocTick)
	a.Allocate()
	for range 6 {
		*clock = clock.Add(allocTick)
		a.Allocate()
	}
	if s, _ := est.counts(); s != 1 {
		t.Fatalf("re-probed within the back-off after a short result, started=%d", s)
	}
	if _, ok := a.ProbeStats(); ok != 0 {
		t.Fatalf("a short probe counted as a success")
	}
}
