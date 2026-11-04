// Package signal serves the /rtc WebSocket endpoint (ADR-0003, PROTOCOL.md 2):
// token validation before upgrade, connect parameter parsing, protobuf/JSON
// framing, bounded write queues and ping timeouts. Session logic lives in a
// SessionHandler (pkg/room).
package signal

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"

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

// ServeHTTP implements http.Handler. Phase 0 stub: 501 after parameter checks.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if _, err := ParseConnectParams(r.URL.Query(), r.RemoteAddr); err != nil {
		writeHTTPError(w, err)
		return
	}
	writeHTTPError(w, errs.NotImplemented("signal websocket"))
}

func writeHTTPError(w http.ResponseWriter, err error) {
	code := errs.CodeOf(err)
	status := errs.HTTPStatus(code)
	if code == errs.CodeFailedPrecondition {
		status = http.StatusUpgradeRequired
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"code":` + strconv.Quote(string(code)) + `,"msg":` + strconv.Quote(err.Error()) + "}"))
}
