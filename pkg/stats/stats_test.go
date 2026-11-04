package stats

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestMeterRates(t *testing.T) {
	var m Meter
	t0 := time.Unix(100, 0)
	if pps, _ := m.Rates(t0); pps != 0 {
		t.Fatal(pps)
	}
	for range 100 {
		m.Add(125)
	}
	// Within the window: cached (zero).
	if pps, _ := m.Rates(t0.Add(100 * time.Millisecond)); pps != 0 {
		t.Fatal(pps)
	}
	pps, bps := m.Rates(t0.Add(time.Second))
	if pps != 100 || bps != 100_000 {
		t.Fatalf("pps %v bps %v", pps, bps)
	}
	if m.Packets() != 100 || m.Bytes() != 12500 {
		t.Fatal(m.Packets(), m.Bytes())
	}
}

func TestMeterAddDoesNotAllocate(t *testing.T) {
	var m Meter
	if a := testing.AllocsPerRun(1000, func() { m.Add(1200) }); a != 0 {
		t.Fatal(a)
	}
}

func TestLossTracker(t *testing.T) {
	var l LossTracker
	t0 := time.Unix(100, 0)
	l.Update(t0, 100, 0)
	if p := l.Update(t0.Add(time.Second), 200, 10); p != 10 {
		t.Fatal(p)
	}
}

func TestNodeSampler(t *testing.T) {
	s := NewNodeSampler("n1")
	n := s.Sample(time.Now())
	if n.Goroutines == 0 || n.HeapBytes == 0 || n.NodeID != "n1" || n.NumCPU == 0 {
		t.Fatalf("%+v", n)
	}
}

func sample() *Snapshot {
	return &Snapshot{
		Time: time.Now(),
		Node: NodeStats{PeerConnections: 4},
		Rooms: []RoomStats{{
			Name: "r1",
			Participants: []ParticipantStats{{
				Kind: "standard", ConnectionQuality: QualityGood, ICECandidateType: CandidateHost, RTTMs: 10,
				Published:  []PublishedTrackStats{{Kind: "video", Codec: "video/VP8", BitrateBps: 1e6}},
				Subscribed: []SubscribedTrackStats{{Kind: "video", CurrentSpatial: 1, Paused: false}},
			}},
			ActiveSpeakers: []SpeakerStats{{ParticipantSID: "PA_1", Level: 0.5}},
		}, {Name: "r2"}},
	}
}

func TestCollector(t *testing.T) {
	reg := prometheus.NewRegistry()
	reg.MustRegister(NewCollector(ProviderFunc(sample)))
	LayerSwitches.Inc()
	exp := `
# HELP tessera_subscribed_video_layer Video DownTracks by current spatial layer (none = paused/off).
# TYPE tessera_subscribed_video_layer gauge
tessera_subscribed_video_layer{layer="0"} 0
tessera_subscribed_video_layer{layer="1"} 1
tessera_subscribed_video_layer{layer="2"} 0
tessera_subscribed_video_layer{layer="none"} 0
# HELP tessera_rooms Active rooms.
# TYPE tessera_rooms gauge
tessera_rooms 2
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(exp), "tessera_subscribed_video_layer", "tessera_rooms"); err != nil {
		t.Fatal(err)
	}
	if n, err := testutil.GatherAndCount(reg, "tessera_layer_switches_total"); err != nil || n != 1 {
		t.Fatal(n, err)
	}
}

func TestHandler(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler(ProviderFunc(sample)).ServeHTTP(rec, httptest.NewRequest("GET", "/stats?room=r1", nil))
	var s Snapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	if len(s.Rooms) != 1 || s.Rooms[0].Name != "r1" || s.Rooms[0].Participants[0].Published[0].BitrateBps != 1e6 {
		t.Fatalf("%+v", s)
	}
}
