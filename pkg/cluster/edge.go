package cluster

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/KeshavGoyal26/tessera/pkg/auth"
	"github.com/KeshavGoyal26/tessera/pkg/errs"
	"github.com/KeshavGoyal26/tessera/pkg/signal"
)

// Edge serves /rtc on an edge node (ADR-0012): it checks the token, finds or places
// the room's media node and relays the WebSocket to that node's private address.
// Clients connect to the edge exactly as to a single server; a reconnect carries the
// same token, so it reaches the same media node and the session resumes there.
type Edge struct {
	Cluster  *Client
	Verifier auth.Verifier
	// MaxMessageSize bounds one signaling message (default 1 MiB).
	MaxMessageSize int64
	Logger         *slog.Logger
}

func (e *Edge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("access_token")
	if token == "" {
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			token = strings.TrimPrefix(h, "Bearer ")
		}
	}
	if token == "" {
		signal.WriteHTTPError(w, errs.New(errs.CodeUnauthenticated, "missing access token"))
		return
	}
	claims, err := e.Verifier.Verify(token)
	if err != nil {
		signal.WriteHTTPError(w, err)
		return
	}
	if claims.Grants == nil || !claims.Grants.RoomJoin || claims.Grants.Room == "" {
		signal.WriteHTTPError(w, errs.New(errs.CodePermissionDenied, "token lacks room_join grant"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	node, err := e.Cluster.PlaceRoom(ctx, claims.Grants.Room)
	cancel()
	if err != nil {
		e.Logger.Warn("edge: no media node for room", "room", claims.Grants.Room, "err", err)
		if errs.CodeOf(err) != errs.CodeUnavailable {
			err = errs.Wrap(errs.CodeUnavailable, err, "cluster unavailable")
		}
		signal.WriteHTTPError(w, err)
		return
	}
	target := strings.Replace(strings.TrimSuffix(node.PrivateURL, "/"), "http", "ws", 1) + signal.Path
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	hdr := http.Header{}
	if a := r.Header.Get("Authorization"); a != "" {
		hdr.Set("Authorization", a)
	}
	hdr.Set("X-Forwarded-For", r.RemoteAddr)
	dctx, dcancel := context.WithTimeout(r.Context(), 10*time.Second)
	up, resp, err := websocket.Dial(dctx, target, &websocket.DialOptions{HTTPHeader: hdr, Subprotocols: subprotocols(r)})
	dcancel()
	if resp != nil && resp.Body != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if err != nil {
		if resp != nil && resp.StatusCode != http.StatusSwitchingProtocols {
			// The media node refused the session (bad token, room full, resume
			// impossible, ...): pass its answer on unchanged.
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
			w.WriteHeader(resp.StatusCode)
			_, _ = w.Write(body)
			return
		}
		e.Logger.Warn("edge: media node unreachable", "node", node.ID, "err", err)
		signal.WriteHTTPError(w, errs.New(errs.CodeUnavailable, "media server unreachable"))
		return
	}
	var accept *websocket.AcceptOptions
	if sp := up.Subprotocol(); sp != "" {
		accept = &websocket.AcceptOptions{Subprotocols: []string{sp}, InsecureSkipVerify: true}
	} else {
		accept = &websocket.AcceptOptions{InsecureSkipVerify: true} // authenticated by token
	}
	down, err := websocket.Accept(w, r, accept)
	if err != nil {
		_ = up.Close(websocket.StatusGoingAway, "client upgrade failed")
		return
	}
	limit := e.MaxMessageSize
	if limit <= 0 {
		limit = 1 << 20
	}
	up.SetReadLimit(limit)
	down.SetReadLimit(limit)
	relay(context.Background(), down, up)
}

func subprotocols(r *http.Request) []string {
	var out []string
	for _, h := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, p := range strings.Split(h, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// relay copies messages both ways until either side closes, then closes the other
// side with the same status (session close codes such as 4009 reach the client).
func relay(ctx context.Context, a, b *websocket.Conn) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{}, 2)
	pipe := func(from, to *websocket.Conn) {
		defer func() { done <- struct{}{} }()
		for {
			typ, msg, err := from.Read(ctx)
			if err != nil {
				code, reason := websocket.CloseStatus(err), ""
				var ce websocket.CloseError
				if errors.As(err, &ce) {
					reason = ce.Reason
				}
				if code == -1 {
					code = websocket.StatusGoingAway
				}
				_ = to.Close(code, reason)
				cancel()
				return
			}
			if err := to.Write(ctx, typ, msg); err != nil {
				_ = from.Close(websocket.StatusGoingAway, "peer gone")
				cancel()
				return
			}
		}
	}
	go pipe(a, b)
	go pipe(b, a)
	<-done
	<-done
}
