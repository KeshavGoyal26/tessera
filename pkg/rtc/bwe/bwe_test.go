package bwe

import (
	"testing"
	"time"

	"github.com/pion/rtcp"

	"github.com/KeshavGoyal26/tessera/pkg/rtc/types"
)

type fakeEst struct{ bps int64 }

func (f *fakeEst) OnPacketSent(uint16, int, int64, bool)                 {}
func (f *fakeEst) OnTransportFeedback(*rtcp.TransportLayerCC, time.Time) {}
func (f *fakeEst) OnRTT(time.Duration)                                   {}
func (f *fakeEst) Estimate() int64                                       { return f.bps }
func (f *fakeEst) State() CongestionState                                { return StateNormal }

type fakeTrack struct {
	id     string
	video  bool
	prio   uint32
	max    types.VideoLayer
	rates  types.Bitrates
	target types.VideoLayer
}

func (t *fakeTrack) ID() string                        { return t.id }
func (t *fakeTrack) IsVideo() bool                     { return t.video }
func (t *fakeTrack) Priority() uint32                  { return t.prio }
func (t *fakeTrack) MaxLayer() types.VideoLayer        { return t.max }
func (t *fakeTrack) Bitrates() types.Bitrates          { return t.rates }
func (t *fakeTrack) SetTargetLayer(l types.VideoLayer) { t.target = l }

func video(id string, prio uint32, q, h, f int64) *fakeTrack {
	t := &fakeTrack{id: id, video: true, prio: prio, max: types.VideoLayer{Spatial: 2, Temporal: 3}}
	for i, r := range []int64{q, h, f} {
		for tl := range types.MaxTemporalLayers {
			t.rates[i][tl] = r
		}
	}
	return t
}

func TestAllocatorDecisions(t *testing.T) {
	est := &fakeEst{bps: 3_000_000}
	a := NewAllocator(Config{Enabled: true, AllowPause: true, UpgradeHold: 3 * time.Second}, est).(*allocator)
	clock := time.Unix(1000, 0)
	a.now = func() time.Time { return clock }
	var paused []Allocation
	a.OnPausedChanged(func(c []Allocation) { paused = append(paused, c...) })
	audio := &fakeTrack{id: "a", rates: types.Bitrates{{50_000, 50_000, 50_000, 50_000}}}
	cam := video("cam", 1, 150_000, 500_000, 1_500_000)
	screen := video("screen", 2, 200_000, 600_000, 1_200_000)
	for _, tr := range []AllocatableTrack{audio, cam, screen} {
		a.AddTrack(tr)
	}
	a.Allocate()
	// 3M*0.95 - 50k = 2.8M: screen high (1.2M) + cam medium (500k) after low passes... check budget greedily.
	if screen.target.Spatial != 2 || cam.target.Spatial < 1 {
		t.Fatalf("plenty: screen %v cam %v", screen.target, cam.target)
	}
	// Congestion: immediate downgrade, lower priority suffers first.
	est.bps = 900_000
	a.Allocate()
	if screen.target.Spatial != 1 || cam.target.Spatial != 0 {
		t.Fatalf("900k: screen %v cam %v", screen.target, cam.target)
	}
	// Not even both low layers fit: lowest priority video paused.
	est.bps = 300_000
	a.Allocate()
	if screen.target.Spatial != 0 || cam.target.IsValid() || len(paused) != 1 || paused[0].TrackID != "cam" || !paused[0].Paused {
		t.Fatalf("300k: screen %v cam %v paused %+v", screen.target, cam.target, paused)
	}
	// Recovery is held for UpgradeHold.
	est.bps = 3_000_000
	a.Allocate()
	if screen.target.Spatial != 0 {
		t.Fatalf("upgrade not held: %v", screen.target)
	}
	clock = clock.Add(4 * time.Second)
	a.Allocate()
	if screen.target.Spatial != 2 || !cam.target.IsValid() {
		t.Fatalf("after hold: screen %v cam %v", screen.target, cam.target)
	}
	if paused[len(paused)-1].Paused {
		t.Fatal("resume not reported")
	}
	// Subscriber preference caps the layer.
	screen.max = types.VideoLayer{Spatial: 0, Temporal: 3}
	a.Allocate()
	if screen.target.Spatial != 0 {
		t.Fatal(screen.target)
	}
}

func TestEstimatorReactsToLoss(t *testing.T) {
	e := NewEstimator(Config{}).(*estimator)
	start := e.Estimate()
	now := time.Now()
	for i := range 20 {
		base := uint16(i * 10)
		for s := range uint16(10) {
			e.OnPacketSent(base+s, 1000, int64(i*10+int(s))*1e6, false)
		}
		fb := &rtcp.TransportLayerCC{BaseSequenceNumber: base, PacketStatusCount: 10,
			PacketChunks: []rtcp.PacketStatusChunk{&rtcp.StatusVectorChunk{SymbolSize: rtcp.TypeTCCSymbolSizeOneBit,
				SymbolList: []uint16{1, 0, 1, 0, 1, 0, 1, 0, 1, 1}}}}
		for range 6 {
			fb.RecvDeltas = append(fb.RecvDeltas, &rtcp.RecvDelta{Type: rtcp.TypeTCCPacketReceivedSmallDelta, Delta: 1000})
		}
		now = now.Add(300 * time.Millisecond)
		e.OnTransportFeedback(fb, now)
	}
	if e.Estimate() >= start/2 {
		t.Fatalf("estimate %d did not drop from %d under 40%% loss", e.Estimate(), start)
	}
}

// An application-limited subscriber (here: audio only, ~50 kbit/s acknowledged) must
// not drag the estimate below its starting value: the allocator would otherwise pause
// all video, and with video paused the acknowledged rate could never rise again.
func TestEstimatorHoldsWhenApplicationLimited(t *testing.T) {
	e := NewEstimator(Config{}).(*estimator)
	start := e.Estimate()
	now := time.Now()
	seq := uint16(0)
	for i := range 60 {
		base := seq
		fb := &rtcp.TransportLayerCC{BaseSequenceNumber: base, PacketStatusCount: 5, ReferenceTime: uint32(i * 5)}
		for s := range 5 {
			e.OnPacketSent(seq, 120, int64(i*300+s*20)*1e6, false)
			seq++
			fb.RecvDeltas = append(fb.RecvDeltas, &rtcp.RecvDelta{Type: rtcp.TypeTCCPacketReceivedSmallDelta, Delta: 20_000})
		}
		fb.PacketChunks = []rtcp.PacketStatusChunk{&rtcp.RunLengthChunk{PacketStatusSymbol: rtcp.TypeTCCPacketReceivedSmallDelta, RunLength: 5}}
		now = now.Add(300 * time.Millisecond)
		e.OnTransportFeedback(fb, now)
	}
	if got := e.Estimate(); got < start {
		t.Fatalf("estimate fell from %d to %d with no congestion signal", start, got)
	}
}

// feedLoss simulates n feedback reports, 128 ms apart, each covering 32 packets of
// 1000 bytes sent every 4 ms (2 Mbit/s) and arriving with the same spacing (no
// queueing), with packet seq lost when lost(seq). Returns the time after the last.
func feedLoss(e *estimator, now time.Time, from, n int, lost func(seq int) bool) time.Time {
	const per, gap = 32, 4 * time.Millisecond
	for i := from; i < from+n; i++ {
		base := i * per
		fb := &rtcp.TransportLayerCC{BaseSequenceNumber: uint16(base), PacketStatusCount: per, ReferenceTime: uint32(2 * i)}
		var syms []uint16
		lastRecv := -1
		for k := range per {
			seq := base + k
			e.OnPacketSent(uint16(seq), 1000, int64(seq)*int64(gap), false)
			if lost(seq) {
				syms = append(syms, rtcp.TypeTCCPacketNotReceived)
				continue
			}
			syms = append(syms, rtcp.TypeTCCPacketReceivedSmallDelta)
			d := time.Duration(k-lastRecv) * gap
			if lastRecv < 0 {
				d = 0
			}
			fb.RecvDeltas = append(fb.RecvDeltas, &rtcp.RecvDelta{Type: rtcp.TypeTCCPacketReceivedSmallDelta, Delta: d.Microseconds()})
			lastRecv = k
		}
		for len(syms) > 0 {
			c := syms[:min(14, len(syms))]
			syms = syms[len(c):]
			fb.PacketChunks = append(fb.PacketChunks, &rtcp.StatusVectorChunk{SymbolSize: rtcp.TypeTCCSymbolSizeOneBit, SymbolList: c})
		}
		now = now.Add(128 * time.Millisecond)
		e.OnTransportFeedback(fb, now)
	}
	return now
}

// Steady random loss (lossy Wi-Fi) is not congestion: the estimate must not ratchet
// down. With a per-report loss average, 8% random loss regularly crossed the 10%
// decrease threshold and the rate never recovered (2-10% allowed no increase).
func TestEstimatorToleratesSteadyRandomLoss(t *testing.T) {
	e := NewEstimator(Config{}).(*estimator)
	start := e.Estimate()
	rng := uint32(1)
	random8 := func(int) bool { rng = rng*1664525 + 1013904223; return rng%100 < 8 }
	feedLoss(e, time.Now(), 0, 160, random8) // ~20 s
	if got := e.Estimate(); got < start*9/10 {
		t.Fatalf("estimate %d fell from %d under steady 8%% random loss", got, start)
	}
}

// Loss that rises well above the link's usual level is congestion.
func TestEstimatorBacksOffWhenLossRises(t *testing.T) {
	e := NewEstimator(Config{}).(*estimator)
	rng := uint32(7)
	random := func(pct uint32) func(int) bool {
		return func(int) bool { rng = rng*1664525 + 1013904223; return rng%100 < pct }
	}
	now := feedLoss(e, time.Now(), 0, 80, random(1)) // 10 s at 1%
	before := e.Estimate()
	feedLoss(e, now, 80, 24, random(20)) // 3 s at 20%
	if got := e.Estimate(); got > before*7/10 {
		t.Fatalf("estimate %d, want well below %d after loss rose from 1%% to 20%%", got, before)
	}
}

// feedDelay is feedLoss without loss and with arrival gaps from arrGap(seq).
func feedDelay(e *estimator, now time.Time, from, n int, arrGap func(seq int) time.Duration) time.Time {
	const per, gap = 32, 4 * time.Millisecond
	arr := time.Duration(from*per) * gap // receiver clock: true arrival times across reports
	for i := from; i < from+n; i++ {
		base := i * per
		ref := arr / (64 * time.Millisecond)
		fb := &rtcp.TransportLayerCC{BaseSequenceNumber: uint16(base), PacketStatusCount: per, ReferenceTime: uint32(ref),
			PacketChunks: []rtcp.PacketStatusChunk{&rtcp.RunLengthChunk{PacketStatusSymbol: rtcp.TypeTCCPacketReceivedSmallDelta, RunLength: per}}}
		prev := ref * 64 * time.Millisecond
		for k := range per {
			seq := base + k
			e.OnPacketSent(uint16(seq), 1000, int64(seq)*int64(gap), false)
			fb.RecvDeltas = append(fb.RecvDeltas, &rtcp.RecvDelta{Type: rtcp.TypeTCCPacketReceivedSmallDelta, Delta: (arr - prev).Microseconds()})
			prev = arr
			arr += arrGap(seq)
		}
		now = now.Add(128 * time.Millisecond)
		e.OnTransportFeedback(fb, now)
	}
	return now
}

// Arrival jitter without a growing queue is not congestion.
func TestEstimatorIgnoresJitter(t *testing.T) {
	e := NewEstimator(Config{}).(*estimator)
	start := e.Estimate()
	rng := uint32(3)
	jitter := func(int) time.Duration { // 4 ms +/- 3 ms, mean unchanged
		rng = rng*1664525 + 1013904223
		return time.Duration(1000+int(rng%6000)) * time.Microsecond
	}
	feedDelay(e, time.Now(), 0, 120, jitter)
	if got := e.Estimate(); got < start*9/10 {
		t.Fatalf("estimate %d fell from %d under jitter alone", got, start)
	}
}

// A queue that keeps growing (arrivals slower than sends) is congestion.
func TestEstimatorBacksOffOnGrowingQueue(t *testing.T) {
	e := NewEstimator(Config{}).(*estimator)
	start := e.Estimate()
	feedDelay(e, time.Now(), 0, 40, func(int) time.Duration { return 5 * time.Millisecond }) // 25% over capacity
	if got := e.Estimate(); got > start*8/10 {
		t.Fatalf("estimate %d, want well below %d with a growing queue", got, start)
	}
}
