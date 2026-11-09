package dashboard

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/auth"
	"github.com/KeshavGoyal26/tessera/pkg/recorder"
	"github.com/KeshavGoyal26/tessera/pkg/room"
	"github.com/KeshavGoyal26/tessera/pkg/stats"
)

type fakeRooms struct {
	room.Admin
	deleted, removed, muted atomic.Value
}

func (f *fakeRooms) DeleteRoom(_ context.Context, r string) error { f.deleted.Store(r); return nil }
func (f *fakeRooms) RemoveParticipant(_ context.Context, r, id string) error {
	f.removed.Store(r + "/" + id)
	return nil
}
func (f *fakeRooms) MutePublishedTrack(_ context.Context, r, id, sid string, m bool) (*tesserav1.TrackInfo, error) {
	f.muted.Store(r + "/" + id + "/" + sid)
	return &tesserav1.TrackInfo{}, nil
}

type fakeRecs struct {
	recorder.Service
	started *tesserav1.StartRecordingRequest
	stopped string
	list    []*tesserav1.RecordingInfo // nil: one recording RC_1
}

func (f *fakeRecs) StartRecording(_ context.Context, r *tesserav1.StartRecordingRequest) (*tesserav1.RecordingInfo, error) {
	f.started = r
	return &tesserav1.RecordingInfo{RecordingId: "RC_1"}, nil
}
func (f *fakeRecs) StopRecording(_ context.Context, id string) (*tesserav1.RecordingInfo, error) {
	f.stopped = id
	return &tesserav1.RecordingInfo{}, nil
}
func (f *fakeRecs) ListRecordings(context.Context, *tesserav1.ListRecordingsRequest) ([]*tesserav1.RecordingInfo, error) {
	if f.list != nil {
		return f.list, nil
	}
	return []*tesserav1.RecordingInfo{{RecordingId: "RC_1", Room: "r"}}, nil
}

func snap() *stats.Snapshot {
	return &stats.Snapshot{
		Time: time.Now(),
		Node: stats.NodeStats{CPUPercent: 390, NumCPU: 4, Rooms: 1, Participants: 1},
		Rooms: []stats.RoomStats{{Name: "r", Participants: []stats.ParticipantStats{{
			SID: "PA_1", Identity: "alice", RTTMs: 450,
			Subscribed: []stats.SubscribedTrackStats{{TrackSID: "TR_1", Kind: "video", LossPercent: 9}},
		}}}},
	}
}

type env struct {
	h     *Handler
	srv   *httptest.Server
	rooms *fakeRooms
	recs  *fakeRecs
}

func newEnv(t *testing.T, recURLs ...string) *env {
	t.Helper()
	e := &env{rooms: &fakeRooms{}, recs: &fakeRecs{}}
	h, err := New(Params{
		Stats: stats.ProviderFunc(snap), Keys: auth.StaticKeys{"key": "s3cret"}, Rooms: e.rooms, Recordings: e.recs,
		RecorderURLs: recURLs, Interval: 20 * time.Millisecond, HistorySize: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.h = h
	e.srv = httptest.NewServer(h)
	t.Cleanup(func() { e.srv.Close(); h.Close() })
	return e
}

type result struct {
	StatusCode int
	cookies    []*http.Cookie
}

func (r result) Cookies() []*http.Cookie { return r.cookies }

func (e *env) do(t *testing.T, method, path, body string, cookie *http.Cookie, hdr map[string]string) (result, string) {
	t.Helper()
	req, _ := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
	if cookie != nil {
		req.AddCookie(cookie)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var sb strings.Builder
	_, _ = bufio.NewReader(resp.Body).WriteTo(&sb)
	return result{resp.StatusCode, resp.Cookies()}, sb.String()
}

func (e *env) login(t *testing.T) (*http.Cookie, string) {
	t.Helper()
	resp, body := e.do(t, "POST", "/admin/api/login", `{"api_key":"key","secret":"s3cret"}`, nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	var out struct{ CSRF string }
	_ = json.Unmarshal([]byte(strings.ReplaceAll(body, `"csrf"`, `"CSRF"`)), &out)
	for _, c := range resp.Cookies() {
		if c.Name == CookieName {
			if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/admin" {
				t.Fatalf("cookie flags: %+v", c)
			}
			return c, out.CSRF
		}
	}
	t.Fatal("no session cookie")
	return nil, ""
}

func TestAuthRequiredAndLogin(t *testing.T) {
	e := newEnv(t)
	if resp, _ := e.do(t, "GET", "/admin/api/snapshot", "", nil, nil); resp.StatusCode != 401 {
		t.Fatal(resp.StatusCode)
	}
	if resp, _ := e.do(t, "POST", "/admin/api/login", `{"api_key":"key","secret":"wrong"}`, nil, nil); resp.StatusCode != 401 {
		t.Fatal(resp.StatusCode)
	}
	if resp, _ := e.do(t, "POST", "/admin/api/login", `{"api_key":"nokey","secret":"x"}`, nil, nil); resp.StatusCode != 401 {
		t.Fatal(resp.StatusCode)
	}
	c, _ := e.login(t)
	resp, body := e.do(t, "GET", "/admin/api/snapshot", "", c, nil)
	if resp.StatusCode != 200 || !strings.Contains(body, `"alerts"`) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	// Static app is public (it only shows the login form).
	if resp, body := e.do(t, "GET", "/admin/", "", nil, nil); resp.StatusCode != 200 || !strings.Contains(body, "Tessera") {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
}

func TestLoginRateLimit(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < maxFails; i++ {
		e.do(t, "POST", "/admin/api/login", `{"api_key":"key","secret":"bad"}`, nil, nil)
	}
	if resp, _ := e.do(t, "POST", "/admin/api/login", `{"api_key":"key","secret":"s3cret"}`, nil, nil); resp.StatusCode != 429 {
		t.Fatalf("got %d, want 429", resp.StatusCode)
	}
}

func TestCSRFAndActions(t *testing.T) {
	e := newEnv(t)
	c, csrf := e.login(t)
	body := `{"room":"r","identity":"alice","track_sid":"TR_1","muted":true}`
	if resp, _ := e.do(t, "POST", "/admin/api/actions/end-room", `{"room":"r"}`, c, nil); resp.StatusCode != 403 {
		t.Fatalf("no CSRF token must be refused, got %d", resp.StatusCode)
	}
	if resp, _ := e.do(t, "POST", "/admin/api/actions/end-room", `{"room":"r"}`, c, map[string]string{"X-CSRF-Token": csrf, "Origin": "https://evil.example"}); resp.StatusCode != 403 {
		t.Fatalf("cross-origin must be refused, got %d", resp.StatusCode)
	}
	h := map[string]string{"X-CSRF-Token": csrf}
	for _, tc := range []struct{ path, body string }{
		{"end-room", `{"room":"r"}`}, {"remove-participant", body}, {"mute-track", body},
		{"start-recording", `{"room":"r","track_sid":"TR_1"}`}, {"stop-recording", `{"recording_id":"RC_1"}`},
	} {
		if resp, out := e.do(t, "POST", "/admin/api/actions/"+tc.path, tc.body, c, h); resp.StatusCode != 200 {
			t.Fatalf("%s: %d %s", tc.path, resp.StatusCode, out)
		}
	}
	if e.rooms.deleted.Load() != "r" || e.rooms.removed.Load() != "r/alice" || e.rooms.muted.Load() != "r/alice/TR_1" {
		t.Fatal("room actions not applied")
	}
	if e.recs.started.GetTrack().GetTrackSid() != "TR_1" || e.recs.stopped != "RC_1" {
		t.Fatal("recording actions not applied")
	}
	if resp, _ := e.do(t, "POST", "/admin/api/actions/end-room", `{}`, c, h); resp.StatusCode != 400 {
		t.Fatal("missing field must be 400")
	}
	if resp, body := e.do(t, "GET", "/admin/api/recordings", "", c, nil); resp.StatusCode != 200 || !strings.Contains(body, "RC_1") {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	// Logout invalidates the session.
	e.do(t, "POST", "/admin/api/logout", "{}", c, h)
	if resp, _ := e.do(t, "GET", "/admin/api/snapshot", "", c, nil); resp.StatusCode != 401 {
		t.Fatal("session must be gone after logout")
	}
}

func TestHistoryRingAndAlerts(t *testing.T) {
	e := newEnv(t)
	c, _ := e.login(t)
	time.Sleep(300 * time.Millisecond)
	_, body := e.do(t, "GET", "/admin/api/history", "", c, nil)
	var hist struct{ Points []Point }
	_ = json.Unmarshal([]byte(strings.ReplaceAll(body, `"points"`, `"Points"`)), &hist)
	if len(hist.Points) != 5 { // ring capacity
		t.Fatalf("got %d points", len(hist.Points))
	}
	for i := 1; i < len(hist.Points); i++ {
		if hist.Points[i].T < hist.Points[i-1].T {
			t.Fatal("history must be oldest first")
		}
	}
	if p := hist.Points[0]; p.LossMax != 9 || p.RTTMax != 450 || p.CPU != 390 {
		t.Fatalf("%+v", p)
	}
	_, snapBody := e.do(t, "GET", "/admin/api/snapshot", "", c, nil)
	var u Update
	if err := json.Unmarshal([]byte(snapBody), &u); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, a := range u.Alerts {
		kinds[a.Kind] = true
	}
	if !kinds["cpu"] || !kinds["rtt"] || !kinds["loss"] {
		t.Fatalf("alerts: %+v", u.Alerts)
	}
}

func TestSSEStream(t *testing.T) {
	e := newEnv(t)
	c, _ := e.login(t)
	req, _ := http.NewRequest("GET", e.srv.URL+"/admin/api/events", nil)
	req.AddCookie(c)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatal(ct)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	n := 0
	for sc.Scan() && n < 3 {
		if strings.HasPrefix(sc.Text(), "data: ") {
			var u Update
			if err := json.Unmarshal([]byte(strings.TrimPrefix(sc.Text(), "data: ")), &u); err != nil || u.Snapshot == nil {
				t.Fatalf("%v", err)
			}
			n++
		}
	}
	if n < 3 {
		t.Fatalf("got %d events", n)
	}
}

func TestRecorderAggregation(t *testing.T) {
	rec := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/stats" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(recorder.Stats{ActiveRecordings: 2, UploadErrors: 3})
	}))
	defer rec.Close()
	e := newEnv(t, rec.URL, "http://127.0.0.1:1")
	c, _ := e.login(t)
	time.Sleep(100 * time.Millisecond)
	_, body := e.do(t, "GET", "/admin/api/snapshot", "", c, nil)
	var u Update
	if err := json.Unmarshal([]byte(body), &u); err != nil {
		t.Fatal(err)
	}
	if len(u.Recorders) != 2 || !u.Recorders[0].OK || u.Recorders[0].Stats.ActiveRecordings != 2 || u.Recorders[1].OK {
		t.Fatalf("%+v", u.Recorders)
	}
	if u.Point.Recording != 2 {
		t.Fatalf("recordings in point: %d", u.Point.Recording)
	}
	var rk, down bool
	for _, a := range u.Alerts {
		rk = rk || (a.Kind == "recording" && strings.Contains(a.Message, "upload errors"))
		down = down || (a.Kind == "recording" && strings.Contains(a.Message, "unreachable"))
	}
	if !rk || !down {
		t.Fatalf("%+v", u.Alerts)
	}
}

// Failed-login entries of addresses that never return must expire
// (regression: only the requesting address was pruned).
func TestLoginFailuresExpire(t *testing.T) {
	var now atomic.Int64
	now.Store(time.Now().UnixNano())
	h, err := New(Params{
		Stats: stats.ProviderFunc(snap), Keys: auth.StaticKeys{"key": "s3cret"}, Rooms: &fakeRooms{}, Recordings: &fakeRecs{},
		Interval: time.Hour, Now: func() time.Time { return time.Unix(0, now.Load()) },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Close)
	login := func(addr, secret string) int {
		r := httptest.NewRequest("POST", "/admin/api/login", strings.NewReader(`{"api_key":"key","secret":"`+secret+`"}`))
		r.RemoteAddr = addr
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	for i := range 50 {
		login(fmt.Sprintf("10.0.0.%d:1000", i), "bad")
	}
	now.Add(int64(2 * failWindow))
	if code := login("10.0.1.1:1000", "s3cret"); code != 200 {
		t.Fatalf("login: %d", code)
	}
	h.mu.Lock()
	n := len(h.fails)
	h.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d stale failed-login entries kept", n)
	}
}

// newHandler returns a handler whose sampler does not tick during the test.
func newHandler(t *testing.T, now time.Time, recs *fakeRecs) *Handler {
	t.Helper()
	h, err := New(Params{
		Stats: stats.ProviderFunc(snap), Keys: auth.StaticKeys{"key": "s3cret"}, Rooms: &fakeRooms{}, Recordings: recs,
		Interval: time.Hour, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Close)
	return h
}

func TestAlertsPresenterModel(t *testing.T) {
	now := time.Now()
	h := newHandler(t, now, &fakeRecs{})
	old := now.Add(-time.Minute)
	s := &stats.Snapshot{Node: stats.NodeStats{NumCPU: 4}, Rooms: []stats.RoomStats{{Name: "g", Participants: []stats.ParticipantStats{
		// A presenter connected here whose video stopped: stalled.
		{SID: "PA_bob", Identity: "bob", JoinedAt: old, ICECandidateType: "relay", Published: []stats.PublishedTrackStats{
			{SID: "TR_bv", Kind: "video"}, {SID: "TR_ba", Kind: "audio", Active: true}}},
		// Relayed from a media node (no connection here): paused upstream when unwatched, not stalled.
		{SID: "PA_carol", Identity: "carol", JoinedAt: old, Published: []stats.PublishedTrackStats{{SID: "TR_cv", Kind: "video"}}},
		// Just joined: no media yet is normal.
		{SID: "PA_dave", Identity: "dave", JoinedAt: now.Add(-2 * time.Second), ICECandidateType: "host", Published: []stats.PublishedTrackStats{{SID: "TR_dv", Kind: "video"}}},
		// Admin-muted video is not stalled.
		{SID: "PA_erin", Identity: "erin", JoinedAt: old, ICECandidateType: "host", Published: []stats.PublishedTrackStats{{SID: "TR_ev", Kind: "video", Muted: true}}},
		// A viewer with loss on two of three tracks: one alert naming the worst track's presenter.
		{SID: "PA_alice", Identity: "alice", Subscribed: []stats.SubscribedTrackStats{
			{TrackSID: "TR_ba", Kind: "audio", LossPercent: 7}, {TrackSID: "TR_bv", Kind: "video", LossPercent: 12}, {TrackSID: "TR_cv", Kind: "video", LossPercent: 1}}},
	}}}}
	recs := []RecorderStatus{
		{URL: "a", OK: true, Stats: &recorder.Stats{WorkerID: "w1", Capacity: 100, CapacityUsed: 92}},
		{URL: "b", OK: true, Stats: &recorder.Stats{WorkerID: "w2", Capacity: 100, CapacityUsed: 100}},
		{URL: "c", OK: true, Stats: &recorder.Stats{WorkerID: "w3", Capacity: 100, CapacityUsed: 50}},
	}
	got := map[string]Alert{}
	for _, a := range h.alerts(s, recs) {
		got[a.ID] = a
	}
	if len(got) != 4 {
		t.Fatalf("want 4 alerts, got %+v", got)
	}
	if a, ok := got["stall:PA_bob:TR_bv"]; !ok || a.Kind != "stall" {
		t.Fatalf("stalled presenter: %+v", got)
	}
	if a := got["loss:PA_alice"]; !strings.Contains(a.Message, "12.0% loss on video from bob") || !strings.Contains(a.Message, "2 of 3 tracks") {
		t.Fatalf("loss alert: %q", a.Message)
	}
	if a := got["rec-cap:a"]; a.Severity != "warning" || !strings.Contains(a.Message, "92 of 100") {
		t.Fatalf("capacity warning: %+v", a)
	}
	if a := got["rec-cap:b"]; a.Severity != "critical" {
		t.Fatalf("capacity full: %+v", a)
	}
}

func TestAlertsBounded(t *testing.T) {
	now := time.Now()
	h := newHandler(t, now, &fakeRecs{})
	s := &stats.Snapshot{Node: stats.NodeStats{NumCPU: 1, CPUPercent: 100}, Rooms: []stats.RoomStats{{Name: "g"}}}
	for i := range 150 {
		s.Rooms[0].Participants = append(s.Rooms[0].Participants, stats.ParticipantStats{SID: fmt.Sprintf("PA_%03d", i), Identity: "v", RTTMs: 900})
	}
	out := h.alerts(s, nil)
	if len(out) != maxAlerts {
		t.Fatalf("got %d alerts, want %d", len(out), maxAlerts)
	}
	if out[0].ID != "cpu" { // critical first, never cut
		t.Fatalf("first alert %+v", out[0])
	}
	if last := out[len(out)-1]; last.ID != "more" || last.Message != "52 more alerts not shown" || last.Since.IsZero() {
		t.Fatalf("summary alert %+v", last)
	}
}

func TestRecordingsListBounded(t *testing.T) {
	recs := &fakeRecs{}
	for i := range maxListedEnded + 50 {
		recs.list = append(recs.list, &tesserav1.RecordingInfo{RecordingId: fmt.Sprintf("RC_%d", i), Status: tesserav1.RecordingStatus_RECORDING_STATUS_COMPLETE})
		if i == 3 { // an old recording still running (a presenter recorded since joining)
			recs.list[i].Status = tesserav1.RecordingStatus_RECORDING_STATUS_ACTIVE
		}
	}
	e := &env{rooms: &fakeRooms{}, recs: recs}
	e.h = newHandler(t, time.Now(), recs)
	e.srv = httptest.NewServer(e.h)
	t.Cleanup(e.srv.Close)
	c, _ := e.login(t)
	_, body := e.do(t, "GET", "/admin/api/recordings", "", c, nil)
	var out struct {
		Recordings []struct {
			ID string `json:"recording_id"`
		} `json:"recordings"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if out.Total != maxListedEnded+50 || len(out.Recordings) != maxListedEnded+1 {
		t.Fatalf("total %d, listed %d", out.Total, len(out.Recordings))
	}
	if out.Recordings[0].ID != "RC_3" || out.Recordings[len(out.Recordings)-1].ID != fmt.Sprintf("RC_%d", maxListedEnded+49) {
		t.Fatalf("want the running one and the newest ended ones, oldest first: %v ... %v", out.Recordings[0], out.Recordings[len(out.Recordings)-1])
	}
}

// TestEventsEndWithSession: logging out ends an open event stream.
func TestEventsEndWithSession(t *testing.T) {
	e := newEnv(t)
	c, csrf := e.login(t)
	req, _ := http.NewRequest("GET", e.srv.URL+"/admin/api/events", nil)
	req.AddCookie(c)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, resp.Body) // returns when the server ends the stream
		close(done)
	}()
	if r, _ := e.do(t, "POST", "/admin/api/logout", "{}", c, map[string]string{"X-CSRF-Token": csrf}); r.StatusCode != 200 {
		t.Fatal(r.StatusCode)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the event stream kept going after logout")
	}
}
