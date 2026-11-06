package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/auth"
	"github.com/KeshavGoyal26/tessera/pkg/errs"
)

const (
	testKey    = "devkey"
	testSecret = "0123456789abcdef0123456789abcdef"
)

// fakeRooms implements room.Admin in memory.
type fakeRooms struct {
	rooms map[string]*tesserav1.Room
	calls []string
}

func newFakeRooms() *fakeRooms { return &fakeRooms{rooms: map[string]*tesserav1.Room{}} }

func (f *fakeRooms) CreateRoom(_ context.Context, r *tesserav1.CreateRoomRequest) (*tesserav1.Room, error) {
	f.calls = append(f.calls, "CreateRoom")
	if x, ok := f.rooms[r.Name]; ok {
		return x, nil
	}
	x := &tesserav1.Room{Name: r.Name, Sid: "RM_" + r.Name, Metadata: r.Metadata, MaxParticipants: r.MaxParticipants}
	f.rooms[r.Name] = x
	return x, nil
}
func (f *fakeRooms) ListRooms(_ context.Context, names []string) ([]*tesserav1.Room, error) {
	var out []*tesserav1.Room
	for _, r := range f.rooms {
		if len(names) == 0 || contains(names, r.Name) {
			out = append(out, r)
		}
	}
	return out, nil
}
func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
func (f *fakeRooms) DeleteRoom(_ context.Context, name string) error {
	if _, ok := f.rooms[name]; !ok {
		return errs.New(errs.CodeNotFound, "room %q not found", name)
	}
	delete(f.rooms, name)
	return nil
}
func (f *fakeRooms) UpdateRoomMetadata(_ context.Context, name, md string) (*tesserav1.Room, error) {
	r, ok := f.rooms[name]
	if !ok {
		return nil, errs.New(errs.CodeNotFound, "room not found")
	}
	r.Metadata = md
	return r, nil
}
func (f *fakeRooms) ListParticipants(_ context.Context, name string) ([]*tesserav1.ParticipantInfo, error) {
	if _, ok := f.rooms[name]; !ok {
		return nil, errs.New(errs.CodeNotFound, "room not found")
	}
	return []*tesserav1.ParticipantInfo{{Identity: "alice", Sid: "PA_1"}}, nil
}
func (f *fakeRooms) GetParticipant(_ context.Context, room, id string) (*tesserav1.ParticipantInfo, error) {
	if id != "alice" {
		return nil, errs.New(errs.CodeNotFound, "participant not found")
	}
	return &tesserav1.ParticipantInfo{Identity: id}, nil
}
func (f *fakeRooms) RemoveParticipant(_ context.Context, room, id string) error {
	f.calls = append(f.calls, "Remove:"+room+"/"+id)
	return nil
}
func (f *fakeRooms) MutePublishedTrack(_ context.Context, room, id, sid string, muted bool) (*tesserav1.TrackInfo, error) {
	return &tesserav1.TrackInfo{Sid: sid, Muted: muted}, nil
}
func (f *fakeRooms) UpdateParticipant(_ context.Context, r *tesserav1.UpdateParticipantRequest) (*tesserav1.ParticipantInfo, error) {
	return &tesserav1.ParticipantInfo{Identity: r.Identity, Metadata: r.GetMetadata()}, nil
}

// fakeRecordings implements recorder.Service and recorder.WorkerAPI.
type fakeRecordings struct {
	recs map[string]*tesserav1.RecordingInfo
}

func (f *fakeRecordings) StartRecording(_ context.Context, r *tesserav1.StartRecordingRequest) (*tesserav1.RecordingInfo, error) {
	i := &tesserav1.RecordingInfo{RecordingId: "REC_1", Room: r.Room, Status: tesserav1.RecordingStatus_RECORDING_STATUS_STARTING, Request: r}
	if f.recs == nil {
		f.recs = map[string]*tesserav1.RecordingInfo{}
	}
	f.recs[i.RecordingId] = i
	return i, nil
}
func (f *fakeRecordings) StopRecording(_ context.Context, id string) (*tesserav1.RecordingInfo, error) {
	i, ok := f.recs[id]
	if !ok {
		return nil, errs.New(errs.CodeNotFound, "recording not found")
	}
	i.Status = tesserav1.RecordingStatus_RECORDING_STATUS_ENDING
	return i, nil
}
func (f *fakeRecordings) ListRecordings(_ context.Context, r *tesserav1.ListRecordingsRequest) ([]*tesserav1.RecordingInfo, error) {
	var out []*tesserav1.RecordingInfo
	for _, i := range f.recs {
		if r.Room == "" || r.Room == i.Room {
			out = append(out, i)
		}
	}
	return out, nil
}
func (f *fakeRecordings) GetRecording(_ context.Context, id string) (*tesserav1.RecordingInfo, error) {
	i, ok := f.recs[id]
	if !ok {
		return nil, errs.New(errs.CodeNotFound, "recording not found")
	}
	return i, nil
}

func newTestServer(t *testing.T) (*Server, *fakeRooms, *fakeRecordings) {
	t.Helper()
	rooms, recs := newFakeRooms(), &fakeRecordings{}
	s := NewServer(Params{
		Rooms: rooms, Recordings: recs,
		Verifier: auth.NewVerifier(auth.StaticKeys{testKey: testSecret}),
	})
	return s, rooms, recs
}

func token(t *testing.T, g auth.RoomGrant) string {
	t.Helper()
	tok, err := auth.NewAccessToken(testKey, testSecret).SetGrant(&g).ToJWT()
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func post(s *Server, path, tok, ct, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if tok != "" {
		r.Header.Set("Authorization", "Bearer "+tok)
	}
	r.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestRouting(t *testing.T) {
	s, _, _ := newTestServer(t)
	cases := []struct {
		method, path string
		status       int
		code         string
	}{
		{http.MethodPost, "/twirp/tessera.v1.Nope/X", 404, "bad_route"},
		{http.MethodPost, "/twirp/tessera.v1.RoomService/Nope", 404, "bad_route"},
		{http.MethodPost, "/twirp/tessera.v1.RoomService", 404, "bad_route"},
		{http.MethodGet, "/twirp/tessera.v1.RoomService/ListRooms", 400, "malformed"},
		{http.MethodPost, "/twirp/tessera.v1.RoomService/ListRooms", 400, "malformed"}, // no content type
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		var body twirpError
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: %v", c.path, err)
		}
		if rec.Code != c.status || body.Code != c.code {
			t.Errorf("%s %s: got %d %q", c.method, c.path, rec.Code, body.Code)
		}
	}
}

func TestAuthentication(t *testing.T) {
	s, _, _ := newTestServer(t)
	const p = "/twirp/tessera.v1.RoomService/ListRooms"
	for name, tok := range map[string]string{"missing": "", "garbage": "abc", "wrong-secret": func() string {
		g := &auth.RoomGrant{RoomList: true}
		tk, _ := auth.NewAccessToken(testKey, "another-secret-another-secret-123").SetGrant(g).ToJWT()
		return tk
	}()} {
		w := post(s, p, tok, "application/json", "{}")
		if w.Code != 401 {
			t.Errorf("%s: got %d", name, w.Code)
		}
	}
}

// TestGrantTable checks RPC x grant for every RPC.
func TestGrantTable(t *testing.T) {
	s, rooms, recs := newTestServer(t)
	rooms.rooms["r1"] = &tesserav1.Room{Name: "r1"}
	rooms.rooms["tmp"] = &tesserav1.Room{Name: "tmp"}
	recs.recs = map[string]*tesserav1.RecordingInfo{"REC_1": {RecordingId: "REC_1", Room: "r1"}, "REC_2": {RecordingId: "REC_2", Room: "r2"}}
	type rpcCase struct {
		path, body string
		need       string // grant name
		grant      func(*auth.RoomGrant)
	}
	set := map[string]func(*auth.RoomGrant){
		"room_create":     func(g *auth.RoomGrant) { g.RoomCreate = true },
		"room_list":       func(g *auth.RoomGrant) { g.RoomList = true },
		"room_admin":      func(g *auth.RoomGrant) { g.RoomAdmin = true },
		"room_record":     func(g *auth.RoomGrant) { g.RoomRecord = true },
		"recorder_worker": func(g *auth.RoomGrant) { g.RecorderWorker = true },
	}
	rs, rc := "/twirp/"+RoomServiceName+"/", "/twirp/"+RecordingServiceName+"/"
	cases := []rpcCase{
		{rs + "CreateRoom", `{"name":"r1"}`, "room_create", nil},
		{rs + "ListRooms", `{}`, "room_list", nil},
		{rs + "UpdateRoomMetadata", `{"room":"r1","metadata":"m"}`, "room_admin", nil},
		{rs + "ListParticipants", `{"room":"r1"}`, "room_admin", nil},
		{rs + "GetParticipant", `{"room":"r1","identity":"alice"}`, "room_admin", nil},
		{rs + "RemoveParticipant", `{"room":"r1","identity":"alice"}`, "room_admin", nil},
		{rs + "MutePublishedTrack", `{"room":"r1","identity":"alice","trackSid":"TR_1","muted":true}`, "room_admin", nil},
		{rs + "UpdateParticipant", `{"room":"r1","identity":"alice","metadata":"x"}`, "room_admin", nil},
		{rc + "StartRecording", `{"room":"r1","roomComposite":{}}`, "room_record", nil},
		{rc + "StopRecording", `{"recordingId":"REC_1"}`, "room_record", nil},
		{rc + "ListRecordings", `{"room":"r1"}`, "room_record", nil},
		{rc + "GetRecording", `{"recordingId":"REC_1"}`, "room_record", nil},
		{rs + "DeleteRoom", `{"room":"tmp"}`, "room_create", nil},
	}
	for _, c := range cases {
		for name, fn := range set {
			var g auth.RoomGrant
			fn(&g)
			w := post(s, c.path, token(t, g), "application/json", c.body)
			want := 403
			if name == c.need {
				want = 200
			}
			if w.Code != want {
				t.Errorf("%s with %s: got %d (%s), want %d", c.path, name, w.Code, w.Body.String(), want)
			}
		}
		if w := post(s, c.path, token(t, auth.RoomGrant{}), "application/json", c.body); w.Code != 403 {
			t.Errorf("%s without grants: got %d", c.path, w.Code)
		}
	}
	// Room scoping.
	g := auth.RoomGrant{RoomAdmin: true, Room: "other"}
	if w := post(s, rs+"ListParticipants", token(t, g), "application/json", `{"room":"r1"}`); w.Code != 403 {
		t.Errorf("scoped admin on other room: %d", w.Code)
	}
	g = auth.RoomGrant{RoomCreate: true, Room: "other"}
	if w := post(s, rs+"DeleteRoom", token(t, g), "application/json", `{"room":"tmp"}`); w.Code != 403 {
		t.Errorf("scoped room_create deleting another room: %d", w.Code)
	}
	if w := post(s, rs+"CreateRoom", token(t, g), "application/json", `{"name":"tmp"}`); w.Code != 403 {
		t.Errorf("scoped room_create creating another room: %d", w.Code)
	}
	g = auth.RoomGrant{RoomRecord: true, Room: "r1"}
	if w := post(s, rc+"StopRecording", token(t, g), "application/json", `{"recordingId":"REC_2"}`); w.Code != 403 {
		t.Errorf("scoped record stop on foreign recording: %d", w.Code)
	}
	if w := post(s, rc+"GetRecording", token(t, g), "application/json", `{"recordingId":"REC_1"}`); w.Code != 200 {
		t.Errorf("scoped record get own: %d", w.Code)
	}
	if w := post(s, rc+"ListRecordings", token(t, g), "application/json", `{}`); w.Code != 403 {
		t.Errorf("scoped list without room: %d", w.Code)
	}
}

func TestErrorMapping(t *testing.T) {
	s, _, _ := newTestServer(t)
	tok := token(t, FullGrant())
	rs := "/twirp/" + RoomServiceName + "/"
	cases := []struct {
		path, body string
		status     int
		code       string
	}{
		{rs + "CreateRoom", `{}`, 400, "invalid_argument"},
		{rs + "CreateRoom", `{not json`, 400, "malformed"},
		{rs + "DeleteRoom", `{"room":"nope"}`, 404, "not_found"},
	}
	for _, c := range cases {
		w := post(s, c.path, tok, "application/json", c.body)
		var te twirpError
		_ = json.Unmarshal(w.Body.Bytes(), &te)
		if w.Code != c.status || te.Code != c.code {
			t.Errorf("%s %s: got %d %+v", c.path, c.body, w.Code, te)
		}
	}
	// Body too large.
	big := `{"name":"` + strings.Repeat("a", MaxBodyBytes+10) + `"}`
	if w := post(s, rs+"CreateRoom", tok, "application/json", big); w.Code != 400 {
		t.Errorf("oversized body: %d", w.Code)
	}
	// Nil dependencies answer unimplemented.
	empty := NewServer(Params{Verifier: auth.NewVerifier(auth.StaticKeys{testKey: testSecret})})
	if w := post(empty, rs+"ListRooms", tok, "application/json", `{}`); w.Code != 501 {
		t.Errorf("nil rooms: %d", w.Code)
	}
}

func TestJSONAcceptsSnakeAndCamel(t *testing.T) {
	s, rooms, _ := newTestServer(t)
	tok := token(t, FullGrant())
	for _, body := range []string{`{"name":"a","maxParticipants":5}`, `{"name":"b","max_participants":5}`} {
		w := post(s, "/twirp/"+RoomServiceName+"/CreateRoom", tok, "application/json", body)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body)
		}
	}
	if rooms.rooms["a"].MaxParticipants != 5 || rooms.rooms["b"].MaxParticipants != 5 {
		t.Fatal("max participants not parsed")
	}
	w := post(s, "/twirp/"+RoomServiceName+"/CreateRoom", tok, "application/json", `{"name":"c","maxParticipants":5}`)
	if !strings.Contains(w.Body.String(), `"maxParticipants":5`) {
		t.Fatalf("expected camelCase output: %s", w.Body)
	}
}

// TestClientRoundTrip drives every client method through a real HTTP server.
func TestClientRoundTrip(t *testing.T) {
	s, _, _ := newTestServer(t)
	srv := httptest.NewServer(s)
	defer srv.Close()
	c := NewClient(srv.URL, testKey, testSecret, FullGrant())
	ctx := context.Background()

	room, err := c.CreateRoom(ctx, &tesserav1.CreateRoomRequest{Name: "demo", Metadata: "m"})
	if err != nil || room.Name != "demo" {
		t.Fatalf("create: %v %v", room, err)
	}
	if rs, err := c.ListRooms(ctx); err != nil || len(rs) != 1 {
		t.Fatalf("list: %v %v", rs, err)
	}
	if r, err := c.UpdateRoomMetadata(ctx, "demo", "m2"); err != nil || r.Metadata != "m2" {
		t.Fatalf("update md: %v %v", r, err)
	}
	if ps, err := c.ListParticipants(ctx, "demo"); err != nil || len(ps) != 1 {
		t.Fatalf("participants: %v %v", ps, err)
	}
	if p, err := c.GetParticipant(ctx, "demo", "alice"); err != nil || p.Identity != "alice" {
		t.Fatalf("get: %v %v", p, err)
	}
	if _, err := c.GetParticipant(ctx, "demo", "bob"); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("get missing: %v", err)
	}
	if err := c.RemoveParticipant(ctx, "demo", "alice"); err != nil {
		t.Fatal(err)
	}
	if tr, err := c.MutePublishedTrack(ctx, "demo", "alice", "TR_1", true); err != nil || !tr.Muted {
		t.Fatalf("mute: %v %v", tr, err)
	}
	md := "z"
	if p, err := c.UpdateParticipant(ctx, &tesserav1.UpdateParticipantRequest{Room: "demo", Identity: "alice", Metadata: &md}); err != nil || p.Metadata != "z" {
		t.Fatalf("update participant: %v %v", p, err)
	}
	rec, err := c.StartRecording(ctx, &tesserav1.StartRecordingRequest{Room: "demo"})
	if err != nil || rec.RecordingId == "" {
		t.Fatalf("start rec: %v %v", rec, err)
	}
	if rs, err := c.ListRecordings(ctx, &tesserav1.ListRecordingsRequest{Room: "demo"}); err != nil || len(rs) != 1 {
		t.Fatalf("list rec: %v %v", rs, err)
	}
	if r, err := c.GetRecording(ctx, rec.RecordingId); err != nil || r.Room != "demo" {
		t.Fatalf("get rec: %v %v", r, err)
	}
	if r, err := c.StopRecording(ctx, rec.RecordingId); err != nil || r.Status != tesserav1.RecordingStatus_RECORDING_STATUS_ENDING {
		t.Fatalf("stop rec: %v %v", r, err)
	}
	if err := c.DeleteRoom(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteRoom(ctx, "demo"); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
}

func TestProtobufAndJSONEquivalent(t *testing.T) {
	s, _, _ := newTestServer(t)
	tok := token(t, FullGrant())
	w := post(s, "/twirp/"+RoomServiceName+"/ListRooms", tok, "application/protobuf", "")
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/protobuf" {
		t.Fatalf("%d %s", w.Code, w.Header())
	}
}
