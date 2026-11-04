package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRouting(t *testing.T) {
	s := NewServer(Params{})
	cases := []struct {
		method, path string
		status       int
		code         string
	}{
		{http.MethodPost, "/twirp/tessera.v1.RoomService/CreateRoom", 501, "unimplemented"},
		{http.MethodPost, "/twirp/tessera.v1.RecordingService/StartRecording", 501, "unimplemented"},
		{http.MethodPost, "/twirp/tessera.v1.Nope/X", 404, "bad_route"},
		{http.MethodGet, "/twirp/tessera.v1.RoomService/ListRooms", 400, "malformed"},
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
