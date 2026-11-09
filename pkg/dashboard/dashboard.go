// Package dashboard is the admin dashboard backend (WP6): login with an API
// key and secret, an HttpOnly session cookie, CSRF-protected actions, a JSON
// snapshot, a one-second Server-Sent Events stream, a one-hour in-memory
// history and the embedded single-page app (web/admin).
//
// Ownership: WP6. See docs/DASHBOARD.md.
package dashboard

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/auth"
	"github.com/KeshavGoyal26/tessera/pkg/errs"
	"github.com/KeshavGoyal26/tessera/pkg/recorder"
	"github.com/KeshavGoyal26/tessera/pkg/room"
	"github.com/KeshavGoyal26/tessera/pkg/stats"
)

//go:embed all:dist
var distFS embed.FS

// PathPrefix is where the dashboard is mounted.
const PathPrefix = "/admin"

// CookieName of the session cookie.
const CookieName = "tessera_admin"

// Params are the dependencies of the dashboard.
type Params struct {
	Stats stats.Provider
	Keys  auth.KeyProvider
	// Rooms and Recordings enable actions; nil disables them (501).
	Rooms      room.Admin
	Recordings recorder.Service
	// RecorderURLs are base URLs of tessera-recorder ops listeners (".../stats" is appended).
	RecorderURLs []string
	Logger       *slog.Logger

	// SessionTTL defaults to 12 h; HistorySize to 3600 points (one hour at 1 Hz).
	SessionTTL  time.Duration
	HistorySize int
	// Interval between samples (default 1 s).
	Interval time.Duration
	// Now overrides the clock in tests.
	Now func() time.Time
	// Thresholds for alerts (defaults: loss 5 %, RTT 300 ms, CPU 80 %).
	LossPercent, RTTMs, CPUPercent float64
	// Cluster turns on the cluster view (nil: a single node).
	Cluster *ClusterParams
}

// Handler is the dashboard http.Handler (mounted at PathPrefix).
type Handler struct {
	p      Params
	log    *slog.Logger
	static http.Handler
	client *http.Client

	mu       sync.Mutex
	sessions map[string]*session
	fails    map[string][]time.Time

	hist   *ring
	latest *Update
	// latestJSON is latest marshalled (nil until someone needs it); summary is this
	// node's Summary for the cluster heartbeat.
	latestJSON []byte
	summary    []byte
	cl         clusterData
	subs       map[chan []byte]struct{}
	alertAt    map[string]time.Time
	rec        []RecorderStatus
	recAt      time.Time

	stop chan struct{}
	done chan struct{}
}

type session struct {
	apiKey  string
	csrf    string
	expires time.Time
}

// Update is what the sampler publishes each tick (snapshot endpoint and SSE).
type Update struct {
	Snapshot  *stats.Snapshot  `json:"snapshot"`
	Recorders []RecorderStatus `json:"recorders"`
	Alerts    []Alert          `json:"alerts"`
	Point     Point            `json:"point"`
}

// RecorderStatus is one recorder worker's /stats (or the error fetching it).
type RecorderStatus struct {
	URL   string          `json:"url"`
	OK    bool            `json:"ok"`
	Error string          `json:"error,omitempty"`
	Stats *recorder.Stats `json:"stats,omitempty"`
}

// Alert is an active alert.
type Alert struct {
	ID       string    `json:"id"`
	Severity string    `json:"severity"` // warning, critical
	Kind     string    `json:"kind"`     // loss, rtt, cpu, recording
	Message  string    `json:"message"`
	Since    time.Time `json:"since"`
}

// New creates the handler and starts the sampler. Call Close to stop it.
func New(p Params) (*Handler, error) {
	if p.Stats == nil || p.Keys == nil {
		return nil, errors.New("dashboard: Stats and Keys are required")
	}
	if p.SessionTTL <= 0 {
		p.SessionTTL = 12 * time.Hour
	}
	if p.HistorySize <= 0 {
		p.HistorySize = 3600
	}
	if p.Interval <= 0 {
		p.Interval = time.Second
	}
	if p.Now == nil {
		p.Now = time.Now
	}
	if p.LossPercent <= 0 {
		p.LossPercent = 5
	}
	if p.RTTMs <= 0 {
		p.RTTMs = 300
	}
	if p.CPUPercent <= 0 {
		p.CPUPercent = 80
	}
	if p.Logger == nil {
		p.Logger = slog.Default()
	}
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return nil, err
	}
	h := &Handler{
		p: p, log: p.Logger, static: http.FileServerFS(sub), client: &http.Client{Timeout: 1500 * time.Millisecond},
		sessions: map[string]*session{}, fails: map[string][]time.Time{}, hist: newRing(p.HistorySize),
		subs: map[chan []byte]struct{}{}, alertAt: map[string]time.Time{},
		stop: make(chan struct{}), done: make(chan struct{}),
	}
	h.cl = clusterData{last: map[string]NodeView{}, alertAt: map[string]time.Time{}, cache: map[string]cached{}}
	h.tick() // initial snapshot so the first request has data
	go h.loop()
	if p.Cluster != nil {
		go h.clusterLoop()
	}
	return h, nil
}

// Close stops the sampler.
func (h *Handler) Close() {
	select {
	case <-h.stop:
	default:
		close(h.stop)
		<-h.done
	}
}

func (h *Handler) loop() {
	defer close(h.done)
	t := time.NewTicker(h.p.Interval)
	defer t.Stop()
	for {
		select {
		case <-h.stop:
			return
		case <-t.C:
			h.tick()
		}
	}
}

// ServeHTTP implements http.Handler. Requests may arrive with or without the
// /admin prefix.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, PathPrefix)
	switch {
	case path == "" && !strings.HasSuffix(r.URL.Path, "/"):
		http.Redirect(w, r, PathPrefix+"/", http.StatusMovedPermanently)
	case strings.HasPrefix(path, "/api/"):
		h.api(w, r, strings.TrimPrefix(path, "/api/"))
	default:
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; media-src 'self' blob: https: http:; connect-src 'self' https: http:; style-src 'self' 'unsafe-inline'; worker-src 'self' blob:")
		r2 := r.Clone(r.Context())
		r2.URL.Path = path
		if path == "" {
			r2.URL.Path = "/"
		}
		h.static.ServeHTTP(w, r2)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func jsonErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (h *Handler) api(w http.ResponseWriter, r *http.Request, route string) {
	switch route {
	case "login":
		h.login(w, r)
		return
	case "node": // other nodes' dashboards, with a cluster token instead of a session
		h.nodeAPI(w, r)
		return
	case "session":
		if s := h.session(r); s != nil {
			writeJSON(w, 200, map[string]any{"authenticated": true, "csrf": s.csrf, "api_key": s.apiKey, "cluster": h.clusterInfo()})
		} else {
			writeJSON(w, 200, map[string]any{"authenticated": false})
		}
		return
	}
	s := h.session(r)
	if s == nil {
		jsonErr(w, http.StatusUnauthorized, "not logged in")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		if !sameOrigin(r) {
			jsonErr(w, http.StatusForbidden, "cross-origin request refused")
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(s.csrf)) != 1 {
			jsonErr(w, http.StatusForbidden, "missing or invalid CSRF token")
			return
		}
	}
	switch {
	case route == "logout" && r.Method == http.MethodPost:
		h.mu.Lock()
		if c, err := r.Cookie(CookieName); err == nil {
			delete(h.sessions, c.Value)
		}
		h.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: PathPrefix, MaxAge: -1, HttpOnly: true})
		writeJSON(w, 200, map[string]bool{"ok": true})
	case route == "snapshot" && r.Method == http.MethodGet:
		h.mu.Lock()
		u := h.latest
		h.mu.Unlock()
		writeJSON(w, 200, u)
	case route == "history" && r.Method == http.MethodGet:
		h.mu.Lock()
		pts := h.hist.all()
		h.mu.Unlock()
		writeJSON(w, 200, map[string]any{"interval_ms": h.p.Interval.Milliseconds(), "points": pts})
	case route == "events" && r.Method == http.MethodGet:
		h.events(w, r)
	case route == "recordings" && r.Method == http.MethodGet:
		h.listRecordings(w, r)
	case (route == "cluster" || strings.HasPrefix(route, "cluster/")) && r.Method == http.MethodGet:
		h.clusterAPI(w, r, route)
	case strings.HasPrefix(route, "actions/") && r.Method == http.MethodPost:
		h.action(w, r, strings.TrimPrefix(route, "actions/"))
	default:
		jsonErr(w, http.StatusNotFound, "unknown endpoint")
	}
}

func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true // non-browser clients; CSRF needs a browser
	}
	u, err := url.Parse(o)
	return err == nil && u.Host == r.Host
}

func newToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

const (
	maxFails   = 5
	failWindow = time.Minute
)

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	if !sameOrigin(r) {
		jsonErr(w, http.StatusForbidden, "cross-origin request refused")
		return
	}
	var body struct {
		APIKey string `json:"api_key"`
		Secret string `json:"secret"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	ip, now := clientIP(r), h.p.Now()
	h.mu.Lock()
	// Prune every address, not just this one, so addresses that failed once
	// and never came back do not accumulate.
	for addr, ts := range h.fails {
		recent := ts[:0]
		for _, t := range ts {
			if now.Sub(t) < failWindow {
				recent = append(recent, t)
			}
		}
		if len(recent) == 0 {
			delete(h.fails, addr)
		} else {
			h.fails[addr] = recent
		}
	}
	limited := len(h.fails[ip]) >= maxFails
	h.mu.Unlock()
	if limited {
		jsonErr(w, http.StatusTooManyRequests, "too many failed attempts, retry in a minute")
		return
	}
	secret, ok := h.p.Keys.Secret(body.APIKey)
	if !ok {
		secret = newToken(8) // keep timing similar for unknown keys
	}
	if subtle.ConstantTimeCompare([]byte(secret), []byte(body.Secret)) != 1 || !ok {
		h.mu.Lock()
		h.fails[ip] = append(h.fails[ip], now)
		h.mu.Unlock()
		jsonErr(w, http.StatusUnauthorized, "invalid API key or secret")
		return
	}
	s := &session{apiKey: body.APIKey, csrf: newToken(24), expires: now.Add(h.p.SessionTTL)}
	id := newToken(32)
	h.mu.Lock()
	h.sessions[id] = s
	delete(h.fails, ip)
	for k, v := range h.sessions { // drop expired
		if now.After(v.expires) {
			delete(h.sessions, k)
		}
	}
	h.mu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: id, Path: PathPrefix, HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Secure:  r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		Expires: s.expires,
	})
	writeJSON(w, 200, map[string]any{"authenticated": true, "csrf": s.csrf, "api_key": s.apiKey, "cluster": h.clusterInfo()})
}

// clusterInfo tells the browser whether this node is part of a cluster (nil if not).
func (h *Handler) clusterInfo() map[string]string {
	if h.p.Cluster == nil {
		return nil
	}
	return map[string]string{"self": h.p.Cluster.Self, "role": h.p.Cluster.Role}
}

func (h *Handler) session(r *http.Request) *session {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.sessions[c.Value]
	if s == nil || h.p.Now().After(s.expires) {
		delete(h.sessions, c.Value)
		return nil
	}
	return s
}

func (h *Handler) events(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		jsonErr(w, 500, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	ch := make(chan []byte, 4)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	first := h.latest
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
	}()
	send := func(b []byte) bool {
		if _, err := fmt.Fprintf(w, "event: update\ndata: %s\n\n", b); err != nil {
			return false
		}
		fl.Flush()
		return true
	}
	if b, err := json.Marshal(first); err == nil && !send(b) {
		return
	}
	keep := time.NewTicker(15 * time.Second)
	defer keep.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-h.stop:
			return
		case b := <-ch:
			if h.session(r) == nil {
				return // logged out or expired: the stream ends with the session
			}
			if !send(b) {
				return
			}
		case <-keep.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			fl.Flush()
		}
	}
}

// ---- actions ----------------------------------------------------------------

// maxListedEnded bounds the ended recordings returned by GET recordings.
const maxListedEnded = 200

func isEnded(s tesserav1.RecordingStatus) bool {
	return s == tesserav1.RecordingStatus_RECORDING_STATUS_COMPLETE ||
		s == tesserav1.RecordingStatus_RECORDING_STATUS_FAILED ||
		s == tesserav1.RecordingStatus_RECORDING_STATUS_ABORTED
}

func (h *Handler) listRecordings(w http.ResponseWriter, r *http.Request) {
	if h.p.Recordings == nil {
		jsonErr(w, http.StatusNotImplemented, "recordings are not enabled")
		return
	}
	list, err := h.p.Recordings.ListRecordings(r.Context(), &tesserav1.ListRecordingsRequest{})
	if err != nil {
		h.fail(w, err)
		return
	}
	// Keep every recording in progress and the newest maxListedEnded ended ones (the
	// coordinator keeps up to 1000): the view refreshes this list every few seconds.
	nEnded := 0
	keep := make([]bool, len(list))
	for i := len(list) - 1; i >= 0; i-- {
		if !isEnded(list[i].GetStatus()) {
			keep[i] = true
		} else if nEnded < maxListedEnded {
			keep[i] = true
			nEnded++
		}
	}
	out := make([]json.RawMessage, 0, len(list))
	for i, rec := range list {
		if !keep[i] {
			continue
		}
		b, err := (jsonpb).Marshal(rec)
		if err == nil {
			out = append(out, b)
		}
	}
	writeJSON(w, 200, map[string]any{"recordings": out, "total": len(list)})
}

func (h *Handler) fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errs.ErrNotFound):
		jsonErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, errs.ErrInvalidArgument):
		jsonErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, errs.ErrUnimplemented):
		jsonErr(w, http.StatusNotImplemented, err.Error())
	default:
		h.log.Warn("dashboard action failed", "err", err)
		jsonErr(w, http.StatusBadGateway, err.Error())
	}
}

func (h *Handler) action(w http.ResponseWriter, r *http.Request, name string) {
	var b struct {
		Room      string `json:"room"`
		Identity  string `json:"identity"`
		TrackSID  string `json:"track_sid"`
		Muted     bool   `json:"muted"`
		Recording string `json:"recording_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&b); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	need := func(vals ...string) bool {
		for _, v := range vals {
			if v == "" {
				jsonErr(w, http.StatusBadRequest, "missing required field")
				return false
			}
		}
		return true
	}
	var err error
	switch name {
	case "end-room", "remove-participant", "mute-track":
		if h.p.Rooms == nil {
			jsonErr(w, http.StatusNotImplemented, "room actions are not enabled")
			return
		}
		switch name {
		case "end-room":
			if !need(b.Room) {
				return
			}
			err = h.p.Rooms.DeleteRoom(ctx, b.Room)
		case "remove-participant":
			if !need(b.Room, b.Identity) {
				return
			}
			err = h.p.Rooms.RemoveParticipant(ctx, b.Room, b.Identity)
		default:
			if !need(b.Room, b.Identity, b.TrackSID) {
				return
			}
			_, err = h.p.Rooms.MutePublishedTrack(ctx, b.Room, b.Identity, b.TrackSID, b.Muted)
		}
	case "start-recording", "stop-recording":
		if h.p.Recordings == nil {
			jsonErr(w, http.StatusNotImplemented, "recordings are not enabled")
			return
		}
		if name == "stop-recording" {
			if !need(b.Recording) {
				return
			}
			_, err = h.p.Recordings.StopRecording(ctx, b.Recording)
			break
		}
		if !need(b.Room) {
			return
		}
		req := &tesserav1.StartRecordingRequest{Room: b.Room}
		switch {
		case b.TrackSID != "":
			req.Target = &tesserav1.StartRecordingRequest_Track{Track: &tesserav1.TrackRecordingTarget{TrackSid: b.TrackSID}}
		case b.Identity != "":
			req.Target = &tesserav1.StartRecordingRequest_Participant{Participant: &tesserav1.ParticipantRecordingTarget{Identity: b.Identity}}
		default:
			jsonErr(w, http.StatusBadRequest, "track_sid or identity required")
			return
		}
		var info *tesserav1.RecordingInfo
		info, err = h.p.Recordings.StartRecording(ctx, req)
		if err == nil {
			h.log.Info("dashboard started recording", "recording", info.GetRecordingId())
		}
	default:
		jsonErr(w, http.StatusNotFound, "unknown action")
		return
	}
	if err != nil {
		h.fail(w, err)
		return
	}
	if s := h.session(r); s != nil {
		h.log.Info("dashboard action", "action", name, "room", b.Room, "identity", b.Identity, "api_key", s.apiKey)
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
