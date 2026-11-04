// Package admin serves the Twirp-style admin API (ADR-0008, PROTOCOL.md 3):
// tessera.v1.RoomService, tessera.v1.RecordingService and the internal
// tessera.v1.RecorderWorkerService, with JWT grant checks and Twirp error
// mapping. It also provides a small Twirp client used by Go tools.
//
// Ownership: WP3. Consumes room.Admin (WP1), recorder.Service and
// recorder.WorkerAPI (WP4), auth.Verifier (WP1).
package admin

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/KeshavGoyal26/tessera/pkg/auth"
	"github.com/KeshavGoyal26/tessera/pkg/errs"
	"github.com/KeshavGoyal26/tessera/pkg/recorder"
	"github.com/KeshavGoyal26/tessera/pkg/room"
)

// PathPrefix is the Twirp route prefix.
const PathPrefix = "/twirp/"

// Service names served.
const (
	RoomServiceName           = "tessera.v1.RoomService"
	RecordingServiceName      = "tessera.v1.RecordingService"
	RecorderWorkerServiceName = "tessera.v1.RecorderWorkerService"
)

// Params are the dependencies of the admin server.
type Params struct {
	Rooms      room.Admin
	Recordings recorder.Service
	Workers    recorder.WorkerAPI
	Verifier   auth.Verifier
	Registry   prometheus.Registerer
	Logger     *slog.Logger
}

// Server is the admin API http.Handler, mounted at PathPrefix.
type Server struct {
	p Params
}

// NewServer creates the admin server.
func NewServer(p Params) *Server { return &Server{p: p} }

// ServeHTTP implements http.Handler. Phase 0 stub: validates the route shape
// and answers every known method with a Twirp "unimplemented" error.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, errs.New(errs.CodeMalformed, "method %s not allowed, use POST", r.Method))
		return
	}
	service, method, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, PathPrefix), "/")
	if !ok || method == "" {
		writeBadRoute(w, r.URL.Path)
		return
	}
	switch service {
	case RoomServiceName, RecordingServiceName, RecorderWorkerServiceName:
		WriteError(w, errs.NotImplemented(service+"/"+method))
	default:
		writeBadRoute(w, r.URL.Path)
	}
}

// twirpError is the Twirp error JSON body.
type twirpError struct {
	Code string            `json:"code"`
	Msg  string            `json:"msg"`
	Meta map[string]string `json:"meta,omitempty"`
}

// WriteError writes err as a Twirp error response.
func WriteError(w http.ResponseWriter, err error) {
	code := errs.CodeOf(err)
	writeTwirp(w, errs.HTTPStatus(code), twirpError{Code: string(code), Msg: err.Error()})
}

func writeBadRoute(w http.ResponseWriter, path string) {
	writeTwirp(w, http.StatusNotFound, twirpError{Code: "bad_route", Msg: "no handler for path " + path})
}

func writeTwirp(w http.ResponseWriter, status int, e twirpError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(e)
}
