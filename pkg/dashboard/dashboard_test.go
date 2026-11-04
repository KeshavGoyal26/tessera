package dashboard

import (
	"bufio"
	"context"
	"encoding/json"
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
