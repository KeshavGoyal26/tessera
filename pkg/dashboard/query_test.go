package dashboard

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/KeshavGoyal26/tessera/pkg/auth"
	"github.com/KeshavGoyal26/tessera/pkg/stats"
)

// bigSnap is a busy node: rooms × (1 presenter with audio+video, viewers watching both).
func bigSnap(rooms, viewers int) *stats.Snapshot {
	s := &stats.Snapshot{Time: time.Now(), Node: stats.NodeStats{NumCPU: 4}}
	qs := []string{stats.QualityExcellent, stats.QualityGood, stats.QualityPoor, stats.QualityLost}
	for r := range rooms {
		name := fmt.Sprintf("room-%03d", r)
		pres := stats.ParticipantStats{SID: name + "-P", Identity: name + "-presenter", ICECandidateType: "host", ConnectionQuality: stats.QualityGood,
			Published: []stats.PublishedTrackStats{
				{SID: name + "-A", Kind: "audio", Active: true, Subscribers: viewers},
				{SID: name + "-V", Kind: "video", Active: true, Subscribers: viewers, LossPercent: float64(r % 7)}}}
		room := stats.RoomStats{Name: name, SID: "RM_" + name, Participants: []stats.ParticipantStats{pres}}
		for v := range viewers {
			room.Participants = append(room.Participants, stats.ParticipantStats{
				SID: fmt.Sprintf("%s-v%04d", name, v), Identity: fmt.Sprintf("viewer-%s-%04d", name, v), ICECandidateType: []string{"host", "srflx", "relay"}[v%3],
				ICESetupMs: int64(10 + v%50), ConnectionQuality: qs[v%4], RTTMs: float64(v % 300),
				Subscribed: []stats.SubscribedTrackStats{
					{TrackSID: name + "-A", PublisherSID: pres.SID, Kind: "audio"},
					{TrackSID: name + "-V", PublisherSID: pres.SID, Kind: "video", LossPercent: float64(v % 11), Paused: v%10 == 0}}})
		}
		s.Rooms = append(s.Rooms, room)
	}
	return s
}

func newQueryHandler(t *testing.T, s *stats.Snapshot) *Handler {
	t.Helper()
	h, err := New(Params{Stats: stats.ProviderFunc(func() *stats.Snapshot { return s }), Keys: auth.StaticKeys{"key": "s3cret"}, Interval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Close)
	return h
}

func query[T any](t *testing.T, h *Handler, route, qs string) (T, int) {
	t.Helper()
	v, _ := url.ParseQuery(qs)
	b, err := h.queryBytes(route, v)
	if err != nil {
		t.Fatalf("%s?%s: %v", route, qs, err)
	}
	var out T
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out, len(b)
}

type pageOfT[T any] struct {
	Version uint64
	Total   int
	Offset  int
	Items   []T
}

// 2,000 participants on one node: the live frame and every page stay small.
func TestQueriesStaySmallAtScale(t *testing.T) {
	h := newQueryHandler(t, bigSnap(20, 99))
	h.mu.Lock()
	live := h.liveJSON
	h.mu.Unlock()
	if len(live) > 16<<10 { // 50 alerts at most, the rest counted
		t.Fatalf("live frame %d bytes", len(live))
	}
	var l Live
	_ = json.Unmarshal(live, &l)
	if l.Counts.Rooms != 20 || l.Counts.Presenters != 20 || l.Counts.Viewers != 1980 {
		t.Fatalf("counts %+v", l.Counts)
	}
	if n := l.Quality[stats.QualityLost] + l.Quality[stats.QualityPoor] + l.Quality[stats.QualityGood] + l.Quality[stats.QualityExcellent]; n != 2000 {
		t.Fatalf("quality %v", l.Quality)
	}

	parts, size := query[pageOfT[ParticipantBrief]](t, h, "participants", "")
	if parts.Total != 2000 || len(parts.Items) != pageDefault || size > 64<<10 {
		t.Fatalf("participants page: total %d items %d, %d bytes", parts.Total, len(parts.Items), size)
	}
	big, _ := query[pageOfT[ParticipantBrief]](t, h, "participants", "limit=100000")
	if len(big.Items) != pageMax {
		t.Fatalf("limit not capped: %d", len(big.Items))
	}
}

func TestRoomsPagingFilterSort(t *testing.T) {
	h := newQueryHandler(t, bigSnap(20, 3))
	p, _ := query[pageOfT[RoomRow]](t, h, "rooms", "limit=5&offset=5")
	if p.Total != 20 || len(p.Items) != 5 || p.Items[0].Name != "room-005" {
		t.Fatalf("%+v", p)
	}
	if r := p.Items[0]; r.Participants != 4 || r.Presenters != 1 || r.Viewers != 3 || r.WorstQuality != stats.QualityPoor {
		t.Fatalf("brief %+v", r)
	}
	p, _ = query[pageOfT[RoomRow]](t, h, "rooms", "q=ROOM-01&sort=name&desc=1")
	if p.Total != 10 || p.Items[0].Name != "room-019" {
		t.Fatalf("filtered desc %+v", p)
	}
	p, _ = query[pageOfT[RoomRow]](t, h, "rooms", "offset=500")
	if p.Total != 20 || len(p.Items) != 0 {
		t.Fatalf("past the end %+v", p)
	}
}

func TestParticipantsFiltersAndAggregates(t *testing.T) {
	h := newQueryHandler(t, bigSnap(3, 8))
	type page struct {
		Total      int
		Items      []ParticipantBrief
		Quality    map[string]int
		ICE        map[string]int
		ICESetupMs Stat `json:"ice_setup_ms"`
		Relay      int
	}
	p, _ := query[page](t, h, "participants", "room=room-001&sort=quality")
	if p.Total != 9 || p.Items[0].Quality != stats.QualityLost || p.Items[len(p.Items)-1].Quality != stats.QualityExcellent {
		t.Fatalf("worst first %+v", p.Items)
	}
	if p.Quality[stats.QualityLost] != 2 || p.Relay != 2 || p.ICESetupMs.Max != 17 {
		t.Fatalf("aggregates %+v %+v %d", p.Quality, p.ICESetupMs, p.Relay)
	}
	p, _ = query[page](t, h, "participants", "role=presenter")
	if p.Total != 3 || p.Items[0].Role != "presenter" || p.Items[0].Published != 2 {
		t.Fatalf("presenters %+v", p)
	}
	p, _ = query[page](t, h, "participants", "q=viewer-room-002-0003")
	if p.Total != 1 || p.Items[0].Paused != 0 || p.Items[0].LossPercent != 3 {
		t.Fatalf("search %+v", p.Items)
	}
}

func TestTrackAndParticipantDetail(t *testing.T) {
	h := newQueryHandler(t, bigSnap(2, 150))
	tracks, _ := query[pageOfT[TrackBrief]](t, h, "tracks", "kind=video")
	if tracks.Total != 2 || tracks.Items[0].SID != "room-001-V" { // loss 1 % before 0 %: worst first
		t.Fatalf("tracks %+v", tracks)
	}
	type trackRes struct {
		Room             string
		Track            stats.PublishedTrackStats
		Subscribers      []SubscriberBrief
		SubscribersTotal int `json:"subscribers_total"`
	}
	tr, _ := query[trackRes](t, h, "track", "sid=room-000-V")
	if tr.Room != "room-000" || tr.SubscribersTotal != 150 || len(tr.Subscribers) != trackSubsMax || tr.Subscribers[0].LossPercent != 10 {
		t.Fatalf("track detail: total %d len %d first %+v", tr.SubscribersTotal, len(tr.Subscribers), tr.Subscribers[0])
	}
	type partRes struct {
		Participant stats.ParticipantStats
		Names       map[string]string
	}
	pr, _ := query[partRes](t, h, "participant", "room=room-000&sid=room-000-v0001")
	if pr.Participant.Identity != "viewer-room-000-0001" || pr.Names["room-000-P"] != "room-000-presenter" {
		t.Fatalf("participant detail %+v", pr)
	}
	v, _ := url.ParseQuery("room=room-000&sid=gone")
	if _, err := h.queryBytes("participant", v); !errors.Is(err, errGone) {
		t.Fatalf("a participant that left: %v", err)
	}
}

// One index per sample, and the same query is answered from the cache until the next one.
func TestQueryCachePerSample(t *testing.T) {
	h := newQueryHandler(t, bigSnap(2, 2))
	v := url.Values{"sort": {"rtt"}}
	a, _ := h.queryBytes("participants", v)
	b, _ := h.queryBytes("participants", v)
	if &a[0] != &b[0] {
		t.Fatal("the same query within a sample was answered again")
	}
	ix := h.indexNow()
	h.tick()
	if c, _ := h.queryBytes("participants", v); &c[0] == &a[0] || h.indexNow() == ix {
		t.Fatal("a new sample must give a new index and fresh answers")
	}
}

// Hundreds of participants with loss are one alert, not hundreds.
func TestAlertsGroupedAtScale(t *testing.T) {
	h := newQueryHandler(t, bigSnap(20, 99))
	h.mu.Lock()
	l := h.live
	h.mu.Unlock()
	var group *Alert
	for i, a := range l.Alerts {
		if strings.HasPrefix(a.ID, "loss:") {
			t.Fatalf("ungrouped alert %s", a.ID)
		}
		if a.ID == "group:loss" {
			group = &l.Alerts[i]
		}
	}
	if group == nil || !strings.HasPrefix(group.Message, "902 loss alerts") || l.AlertsTotal > 5 {
		t.Fatalf("grouped: %+v total %d", group, l.AlertsTotal)
	}
}

func TestJournalSessions(t *testing.T) {
	j := newJournal()
	t0 := time.Unix(1000, 0)
	snapAt := func(q string, present bool) *stats.Snapshot {
		s := &stats.Snapshot{Rooms: []stats.RoomStats{{Name: "r"}}}
		if present {
			s.Rooms[0].Participants = []stats.ParticipantStats{{SID: "PA_1", Identity: "alice", JoinedAt: t0, ICECandidateType: "relay", ConnectionQuality: q, RTTMs: 80}}
		}
		return s
	}
	j.Observe(snapAt(stats.QualityGood, true), t0)
	j.Observe(snapAt(stats.QualityGood, true), t0.Add(30*time.Second))
	j.Observe(snapAt(stats.QualityPoor, true), t0.Add(40*time.Second)) // 40 s good, then poor
	j.Observe(snapAt("", false), t0.Add(50*time.Second))               // gone: ended at 40 s
	rows := j.rows(t0.Add(time.Minute))
	if len(rows) != 1 || rows[0].State != "ended" || rows[0].DurationMs != 40000 || rows[0].Share.Good != 1 || rows[0].MaxRTTMs != 80 {
		t.Fatalf("row %+v", rows)
	}
	j.Record("PA_1", SessionEvent{T: t0, Kind: "leave_reason", Detail: "client closed"}) // another source
	b, ev, ok := j.detail("PA_1", t0.Add(time.Minute))
	kinds := []string{}
	for _, e := range ev {
		kinds = append(kinds, e.Kind)
	}
	if !ok || b.Events != 4 || strings.Join(kinds, ",") != "leave_reason,left,quality,joined" {
		t.Fatalf("events %v (%d)", kinds, b.Events)
	}
}

func TestCapacityN1(t *testing.T) {
	n := func(id, role string, cpu float64) NodeView {
		return NodeView{ID: id, Role: role, Summary: &Summary{CPU: cpu}}
	}
	c := capacityOf([]NodeView{n("m1", "media", 60), n("m2", "media", 40), n("m3", "media", 50), n("e1", "edge", 70)})
	if m := c["media"]; m.Nodes != 3 || !m.N1 || m.Busiest != "m1" || m.MaxLoad != 0.6 { // 150% over two nodes = 75 %
		t.Fatalf("media %+v", m)
	}
	if e := c["edge"]; e.N1 || e.Nodes != 1 {
		t.Fatalf("a single edge cannot be lost: %+v", e)
	}
	if m := capacityOf([]NodeView{n("m1", "media", 80), n("m2", "media", 70)})["media"]; m.N1 { // 150% on one node
		t.Fatalf("overloaded after a loss: %+v", m)
	}
}
