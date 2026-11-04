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
