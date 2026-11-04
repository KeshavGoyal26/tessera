// Package signal serves the /rtc WebSocket endpoint (ADR-0003, PROTOCOL.md 2):
// token validation before upgrade, connect parameter parsing, protobuf/JSON
// framing, bounded write queues and ping timeouts. Session logic lives in a
// SessionHandler (pkg/room).
package signal

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/coder/websocket"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/auth"
	"github.com/KeshavGoyal26/tessera/pkg/config"
	"github.com/KeshavGoyal26/tessera/pkg/errs"
	"github.com/KeshavGoyal26/tessera/pkg/version"
)

// Path of the signaling endpoint.
const Path = "/rtc"

// WebSocket close codes used by the server (PROTOCOL.md 2.2).
const (
	CloseNormal            = 1000
	CloseGoingAway         = 1001
	CloseAuthExpired       = 4001
	ClosePermissionRevoked = 4003
	ClosePingTimeout       = 4008
	CloseDuplicateIdentity = 4009
	CloseProtocolError     = 4010
)

// Conn is one signaling connection.
type Conn interface {
	// ReadRequest blocks for the next client message.
	ReadRequest(ctx context.Context) (*tesserav1.SignalRequest, error)
	// WriteResponse enqueues a message without blocking. It fails with
	// errs.ErrResourceExhausted when the write queue is full (slow consumer).
	WriteResponse(msg *tesserav1.SignalResponse) error
	Close(code int, reason string) error
	RemoteAddr() string
}

// ConnectParams are the parsed query parameters of /rtc.
type ConnectParams struct {
	Protocol        int
	AutoSubscribe   bool
	Reconnect       bool
	ParticipantSID  string
	ReconnectReason int
	// JSON selects protojson text frames.
	JSON   bool
	Client *tesserav1.ClientInfo
}

// SessionHandler implements sessions on top of connections (pkg/room).
type SessionHandler interface {
	// Admit is called before the WebSocket upgrade so failures become HTTP
	// errors (404 room not found, 409 resume impossible, 503 draining...).
	Admit(ctx context.Context, claims *auth.Claims, p ConnectParams) error
	// HandleSession runs a session until it ends. The handler owns conn.
	HandleSession(ctx context.Context, claims *auth.Claims, p ConnectParams, conn Conn) error
}

// ParseConnectParams parses and validates /rtc query parameters.
func ParseConnectParams(q url.Values, remoteAddr string) (ConnectParams, error) {
	p := ConnectParams{AutoSubscribe: true}
	proto, err := strconv.Atoi(q.Get("protocol"))
	if err != nil {
		return p, errs.New(errs.CodeInvalidArgument, "missing or invalid protocol")
	}
	if proto < version.MinProtocolVersion || proto > version.ProtocolVersion {
		return p, errs.New(errs.CodeFailedPrecondition, "unsupported protocol %d (supported %d..%d)",
			proto, version.MinProtocolVersion, version.ProtocolVersion)
	}
	p.Protocol = proto
	if v := q.Get("auto_subscribe"); v != "" {
		p.AutoSubscribe = v == "1" || v == "true"
	}
	p.Reconnect = q.Get("reconnect") == "1" || q.Get("reconnect") == "true"
	p.ParticipantSID = q.Get("sid")
	if p.Reconnect && p.ParticipantSID == "" {
		return p, errs.New(errs.CodeInvalidArgument, "reconnect requires sid")
	}
	if v := q.Get("reconnect_reason"); v != "" {
		p.ReconnectReason, _ = strconv.Atoi(v)
	}
	p.JSON = q.Get("format") == "json"
	p.Client = &tesserav1.ClientInfo{
		Sdk:            sdkFromString(q.Get("sdk")),
		Version:        q.Get("version"),
		Protocol:       int32(proto),
		Os:             q.Get("os"),
		OsVersion:      q.Get("os_version"),
		Browser:        q.Get("browser"),
		BrowserVersion: q.Get("browser_version"),
		DeviceModel:    q.Get("device_model"),
		Address:        remoteAddr,
	}
	return p, nil
}

func sdkFromString(s string) tesserav1.ClientInfo_SDK {
	switch s {
	case "js":
		return tesserav1.ClientInfo_SDK_JS
	case "go":
		return tesserav1.ClientInfo_SDK_GO
	case "recorder":
		return tesserav1.ClientInfo_SDK_RECORDER
	case "loadtest":
		return tesserav1.ClientInfo_SDK_LOADTEST
	}
	return tesserav1.ClientInfo_SDK_UNSPECIFIED
}

// Server is the /rtc HTTP handler.
type Server struct {
	cfg      config.SignalConfig
	verifier auth.Verifier
	handler  SessionHandler
	logger   *slog.Logger
}

// NewServer creates the signaling server.
func NewServer(cfg config.SignalConfig, verifier auth.Verifier, handler SessionHandler, logger *slog.Logger) *Server {
	return &Server{cfg: cfg, verifier: verifier, handler: handler, logger: logger}
}

// ResumeFailed returns an Admit error that maps to HTTP 409 (the client
// must perform a full reconnect, PROTOCOL.md 2.6).
func ResumeFailed(msg string) error { return &resumeError{msg: msg} }

type resumeError struct{ msg string }

func (e *resumeError) Error() string { return "resume impossible: " + e.msg }

// Subprotocol is the optional WebSocket subprotocol echoed by the server.
const Subprotocol = "tessera.v1"

// ServeHTTP implements http.Handler: token check and Admit before the
// upgrade, then the session runs on the request goroutine.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p, err := ParseConnectParams(r.URL.Query(), r.RemoteAddr)
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	token := r.URL.Query().Get("access_token")
	if token == "" {
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			token = strings.TrimPrefix(h, "Bearer ")
		}
	}
	if token == "" {
		writeHTTPError(w, errs.New(errs.CodeUnauthenticated, "missing access token"))
		return
	}
	if s.verifier == nil || s.handler == nil {
		writeHTTPError(w, errs.New(errs.CodeUnavailable, "signaling not configured"))
		return
	}
	claims, err := s.verifier.Verify(token)
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	if claims.Grants == nil || !claims.Grants.RoomJoin || claims.Grants.Room == "" {
		writeHTTPError(w, errs.New(errs.CodePermissionDenied, "token lacks room_join grant"))
		return
	}
	if claims.Identity() == "" {
		writeHTTPError(w, errs.New(errs.CodeInvalidArgument, "token has no identity"))
		return
	}
	if err := s.handler.Admit(r.Context(), claims, p); err != nil {
		writeHTTPError(w, err)
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols:       []string{Subprotocol},
		InsecureSkipVerify: true, // browsers on other origins are authenticated by token
	})
	if err != nil {
		return
	}
	maxSize := s.cfg.MaxMessageSize
	if maxSize <= 0 {
		maxSize = 1 << 20
	}
	ws.SetReadLimit(maxSize)
	conn := newWSConn(ws, p.JSON, s.cfg.WriteQueueSize, r.RemoteAddr)
	// The session outlives the request context on server shutdown only via
	// the handler; cancel when the handler returns.
	ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
	defer cancel()
	go conn.writeLoop(ctx)
	if err := s.handler.HandleSession(ctx, claims, p, conn); err != nil && s.logger != nil {
		s.logger.Debug("session ended", "identity", claims.Identity(), "err", err)
	}
	_ = conn.Close(CloseNormal, "")
}

func writeHTTPError(w http.ResponseWriter, err error) {
	code := errs.CodeOf(err)
	status := errs.HTTPStatus(code)
	var re *resumeError
	switch {
	case errors.As(err, &re):
		status, code = http.StatusConflict, errs.CodeNotFound
	case code == errs.CodeFailedPrecondition:
		status = http.StatusUpgradeRequired
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"code":` + strconv.Quote(string(code)) + `,"msg":` + strconv.Quote(err.Error()) + "}"))
}
