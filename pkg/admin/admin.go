// Package admin serves the Twirp-style admin API (ADR-0008, PROTOCOL.md 3):
// tessera.v1.RoomService and tessera.v1.RecordingService, with JWT grant checks
// and Twirp error mapping. It also provides a small Twirp client used by Go tools.
//
// Ownership: WP3. Consumes room.Admin (WP1), recorder.Service (WP4) and
// auth.Verifier (WP1).
package admin

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/KeshavGoyal26/tessera/pkg/auth"
	"github.com/KeshavGoyal26/tessera/pkg/errs"
	"github.com/KeshavGoyal26/tessera/pkg/recorder"
	"github.com/KeshavGoyal26/tessera/pkg/room"
)

// PathPrefix is the Twirp route prefix.
const PathPrefix = "/twirp/"

// Service names served.
const (
	RoomServiceName      = "tessera.v1.RoomService"
	RecordingServiceName = "tessera.v1.RecordingService"
)

// MaxBodyBytes limits request bodies.
const MaxBodyBytes = 4 << 20

// Params are the dependencies of the admin server.
type Params struct {
	Rooms      room.Admin
	Recordings recorder.Service
	Verifier   auth.Verifier
	Registry   prometheus.Registerer
	Logger     *slog.Logger
}

// Server is the admin API http.Handler, mounted at PathPrefix.
type Server struct {
	p      Params
	routes map[string]*route
	log    *slog.Logger
	reqs   *prometheus.CounterVec
	dur    *prometheus.HistogramVec
}

// route is one RPC: required grant check plus a typed handler.
type route struct {
	// authorize returns nil when the grants allow the call for this request.
	authorize func(g *auth.RoomGrant, req proto.Message) error
	newReq    func() proto.Message
	handle    func(ctx *callCtx, req proto.Message) (proto.Message, error)
}

// NewServer creates the admin server.
func NewServer(p Params) *Server {
	s := &Server{p: p, log: p.Logger}
	if s.log == nil {
		s.log = slog.Default()
	}
	s.reqs = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "tessera_admin_requests_total", Help: "Admin API requests."}, []string{"rpc", "code"})
	s.dur = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "tessera_admin_request_seconds", Help: "Admin API latency.",
		Buckets: prometheus.DefBuckets}, []string{"rpc"})
	if p.Registry != nil {
		for _, c := range []prometheus.Collector{s.reqs, s.dur} {
			if err := p.Registry.Register(c); err != nil {
				var are prometheus.AlreadyRegisteredError
				if !errors.As(err, &are) {
					s.log.Warn("admin metrics registration failed", "err", err)
				}
			}
		}
	}
	s.routes = buildRoutes(p)
	return s
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	service, method, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, PathPrefix), "/")
	if !ok || method == "" || !strings.HasPrefix(r.URL.Path, PathPrefix) {
		writeBadRoute(w, r.URL.Path)
		return
	}
	rpc := service + "/" + method
	rt := s.routes[rpc]
	if rt == nil {
		writeBadRoute(w, r.URL.Path)
		return
	}
	if r.Method != http.MethodPost {
		s.finish(w, rpc, start, errs.New(errs.CodeMalformed, "method %s not allowed, use POST", r.Method))
		return
	}
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if ct != "application/json" && ct != "application/protobuf" {
		s.finish(w, rpc, start, errs.New(errs.CodeMalformed, "unsupported content type %q", ct))
		return
	}
	claims, err := s.authenticate(r)
	if err != nil {
		s.finish(w, rpc, start, err)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	if err != nil {
		s.finish(w, rpc, start, errs.New(errs.CodeMalformed, "cannot read request body: %v", err))
		return
	}
	req := rt.newReq()
	if ct == "application/json" {
		if len(body) == 0 {
			body = []byte("{}")
		}
		err = protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(body, req)
	} else {
		err = proto.Unmarshal(body, req)
	}
	if err != nil {
		s.finish(w, rpc, start, errs.New(errs.CodeMalformed, "cannot decode request: %v", err))
		return
	}
	grants := claims.Grants
	if grants == nil {
		grants = &auth.RoomGrant{}
	}
	if err := rt.authorize(grants, req); err != nil {
		s.finish(w, rpc, start, err)
		return
	}
	resp, err := rt.handle(&callCtx{r: r, grants: grants}, req)
	if err != nil {
		s.finish(w, rpc, start, err)
		return
	}
	var out []byte
	if ct == "application/json" {
		out, err = protojson.Marshal(resp)
		w.Header().Set("Content-Type", "application/json")
	} else {
		out, err = proto.Marshal(resp)
		w.Header().Set("Content-Type", "application/protobuf")
	}
	if err != nil {
		s.finish(w, rpc, start, errs.Wrap(errs.CodeInternal, err, "cannot encode response"))
		return
	}
	s.reqs.WithLabelValues(rpc, "ok").Inc()
	s.dur.WithLabelValues(rpc).Observe(time.Since(start).Seconds())
	_, _ = w.Write(out)
}

func (s *Server) finish(w http.ResponseWriter, rpc string, start time.Time, err error) {
	code := errs.CodeOf(err)
	s.reqs.WithLabelValues(rpc, string(code)).Inc()
	s.dur.WithLabelValues(rpc).Observe(time.Since(start).Seconds())
	if code == errs.CodeInternal {
		s.log.Error("admin rpc failed", "rpc", rpc, "err", err)
	}
	WriteError(w, err)
}

func (s *Server) authenticate(r *http.Request) (*auth.Claims, error) {
	h := r.Header.Get("Authorization")
	tok, ok := strings.CutPrefix(h, "Bearer ")
	if !ok || strings.TrimSpace(tok) == "" {
		return nil, errs.New(errs.CodeUnauthenticated, "missing bearer token")
	}
	if s.p.Verifier == nil {
		return nil, errs.New(errs.CodeUnauthenticated, "no verifier configured")
	}
	c, err := s.p.Verifier.Verify(strings.TrimSpace(tok))
	if err != nil {
		return nil, errs.New(errs.CodeUnauthenticated, "%s", err.Error())
	}
	return c, nil
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
	msg := err.Error()
	var e *errs.Error
	if errors.As(err, &e) && code == errs.CodeInternal {
		msg = "internal error"
	}
	writeTwirp(w, errs.HTTPStatus(code), twirpError{Code: string(code), Msg: msg})
}

func writeBadRoute(w http.ResponseWriter, path string) {
	writeTwirp(w, http.StatusNotFound, twirpError{Code: "bad_route", Msg: "no handler for path " + path})
}

func writeTwirp(w http.ResponseWriter, status int, e twirpError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(e)
}
