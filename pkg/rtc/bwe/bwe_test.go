package bwe

import (
	"testing"
	"time"

	"github.com/pion/rtcp"
)

type fakeEst struct {
	bps   int64
	state CongestionState
}

func (f *fakeEst) OnPacketSent(uint16, int, int64, bool)                 {}
func (f *fakeEst) OnTransportFeedback(*rtcp.TransportLayerCC, time.Time) {}
func (f *fakeEst) OnRTT(time.Duration)                                   {}
func (f *fakeEst) Estimate() int64                                       { return f.bps }
func (f *fakeEst) State() CongestionState                                { return f.state }

type fakeTrack struct {
	id       string
	video    bool
	prio     uint32
	disabled bool
	rate     int64
	paused   bool
}

func (t *fakeTrack) ID() string                 { return t.id }
func (t *fakeTrack) IsVideo() bool              { return t.video }
func (t *fakeTrack) Priority() uint32           { return t.prio }
func (t *fakeTrack) Wanted() bool               { return !t.disabled }
func (t *fakeTrack) Bitrate() int64             { return t.rate }
func (t *fakeTrack) SetAllocPaused(paused bool) { t.paused = paused }

func video(id string, prio uint32, rate int64) *fakeTrack {
	return &fakeTrack{id: id, video: true, prio: prio, rate: rate}
}

func TestAllocatorDecisions(t *testing.T) {
	est := &fakeEst{bps: 3_000_000}
	a := NewAllocator(Config{Enabled: true, AllowPause: true}, est).(*allocator)
	var paused []Allocation
	a.OnPausedChanged(func(c []Allocation) { paused = append(paused, c...) })
	audio := &fakeTrack{id: "a", rate: 50_000}
	cams := []*fakeTrack{video("cam1", 2, 500_000), video("cam2", 1, 500_000), video("cam3", 1, 500_000)}
	a.AddTrack(audio)
	for _, c := range cams {
		a.AddTrack(c)
	}
	a.Allocate()
	if cams[0].paused || cams[1].paused || cams[2].paused || len(paused) != 0 {
		t.Fatalf("plenty: %+v %+v %+v paused %+v", cams[0], cams[1], cams[2], paused)
	}
	// 1.2M*0.95 - 50k = 1.09M: two fit, the lowest priority (then highest id) pauses.
	est.bps = 1_200_000
	a.Allocate()
	if cams[0].paused || cams[1].paused || !cams[2].paused || len(paused) != 1 || paused[0].TrackID != "cam3" || !paused[0].Paused {
		t.Fatalf("1.2M: %v %v %v paused %+v", cams[0].paused, cams[1].paused, cams[2].paused, paused)
	}
	// A track the subscriber disabled uses no bandwidth and is not "paused".
	cams[1].disabled = true
	a.Allocate()
	if cams[1].paused || cams[2].paused || len(paused) != 2 || paused[1].TrackID != "cam3" || paused[1].Paused {
		t.Fatalf("cam2 disabled: %v %v paused %+v", cams[1].paused, cams[2].paused, paused)
	}
	// Recovery resumes everything and reports it.
	cams[1].disabled = false
	est.bps = 3_000_000
	a.Allocate()
	if cams[0].paused || cams[1].paused || cams[2].paused {
		t.Fatalf("recovered: %v %v %v", cams[0].paused, cams[1].paused, cams[2].paused)
	}
	// Without AllowPause nothing is paused, however low the estimate.
	b := NewAllocator(Config{Enabled: true}, &fakeEst{bps: 100_000}).(*allocator)
	c := video("c", 1, 500_000)
	b.AddTrack(c)
	b.Allocate()
	if c.paused {
		t.Fatal("paused without AllowPause")
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

// With every wanted video paused nothing can carry probe padding: after a back-off
// the first paused track is resumed on trial, kept if the estimate comes to cover
// it, and paused again (with a longer back-off) if not.
func TestAllocatorTrialResume(t *testing.T) {
	est := &fakeEst{bps: 300_000}
	a := NewAllocator(Config{Enabled: true, AllowPause: true}, est).(*allocator)
	clock := time.Unix(1000, 0)
	a.now = func() time.Time { return clock }
	cam := video("cam", 1, 450_000)
	a.AddTrack(cam)
	step := func(d time.Duration) { clock = clock.Add(d); a.Allocate() }
	step(0)
	if !cam.paused {
		t.Fatal("450k does not fit 300k: must pause")
	}
	step(4 * time.Second)
	if !cam.paused {
		t.Fatal("trial before the back-off")
	}
	step(time.Second) // 5 s stalled: trial
	if cam.paused {
		t.Fatal("no trial resume after 5 s")
	}
	step(trialHold / 2)
	if cam.paused {
		t.Fatal("a trial without congestion must go on")
	}
	est.state = StateOveruse // the track congests the link: paused again, back-off 10 s
	step(allocTick)
	if !cam.paused {
		t.Fatal("a congested trial must pause again")
	}
	est.state = StateNormal
	step(9 * time.Second)
	if !cam.paused {
		t.Fatal("trial again before the doubled back-off")
	}
	step(time.Second)
	if cam.paused {
		t.Fatal("no second trial after 10 s")
	}
	step(trialHold) // no congestion but the estimate never covered it: paused again
	if !cam.paused {
		t.Fatal("a trial that never succeeds must end")
	}
	step(20 * time.Second) // back-off 20 s
	if cam.paused {
		t.Fatal("no third trial after 20 s")
	}
	est.bps = 2_000_000 // the trial's traffic raised the estimate
	step(allocTick)
	step(trialHold + time.Second)
	if cam.paused || a.trialID != "" || a.trialBackoff != 0 {
		t.Fatalf("successful trial must keep the track on: paused=%v trial=%q backoff=%v", cam.paused, a.trialID, a.trialBackoff)
	}
}
