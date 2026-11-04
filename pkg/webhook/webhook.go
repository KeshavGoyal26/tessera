// Package webhook delivers signed WebhookEvents to configured endpoints
// (ADR-0008, PROTOCOL.md 4) and provides signature helpers for receivers.
//
// Ownership: WP3. Consumers: pkg/room and pkg/recorder (Notifier).
package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/auth"
	"github.com/KeshavGoyal26/tessera/pkg/config"
	"github.com/KeshavGoyal26/tessera/pkg/errs"
)

// HTTP headers of webhook requests.
const (
	HeaderEventID   = "Tessera-Event-Id"
	HeaderEventType = "Tessera-Event-Type"
	HeaderKey       = "Tessera-Key"
	HeaderSignature = "Tessera-Signature"
)

// DefaultTolerance is the accepted clock difference for signatures.
const DefaultTolerance = 300 * time.Second

// Notifier queues events for delivery. Notify never blocks; the notifier
// fills in Id and CreatedAt when empty.
type Notifier interface {
	Notify(ctx context.Context, ev *tesserav1.WebhookEvent)
	// Close flushes queues until ctx is done.
	Close(ctx context.Context) error
}

// Nop is a Notifier that drops events.
type Nop struct{}

// Notify implements Notifier.
func (Nop) Notify(context.Context, *tesserav1.WebhookEvent) {}

// Close implements Notifier.
func (Nop) Close(context.Context) error { return nil }

// NewNotifier creates the HTTP notifier for cfg.Endpoints, signing with
// cfg.APIKey's secret. Phase 0 stub: returns Nop (logging a warning when
// endpoints are configured).
func NewNotifier(cfg config.WebhookConfig, keys auth.KeyProvider, reg prometheus.Registerer, logger *slog.Logger) (Notifier, error) {
	if len(cfg.Endpoints) > 0 && logger != nil {
		logger.Warn("webhook delivery not implemented yet; events are dropped")
	}
	return Nop{}, nil
}

// Sign returns the Tessera-Signature header value for body at time t.
func Sign(secret string, t time.Time, body []byte) string {
	ts := strconv.FormatInt(t.Unix(), 10)
	return "t=" + ts + ",v1=" + mac(secret, ts, body)
}

func mac(secret, ts string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(ts))
	m.Write([]byte{'.'})
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

// Verify checks a Tessera-Signature header against body. It accepts any of
// several v1 entries (secret rotation) and rejects timestamps outside tolerance.
func Verify(header string, body []byte, secret string, now time.Time, tolerance time.Duration) error {
	var ts string
	var sigs []string
	for _, part := range strings.Split(header, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch k {
		case "t":
			ts = v
		case "v1":
			sigs = append(sigs, v)
		}
	}
	if ts == "" || len(sigs) == 0 {
		return errs.New(errs.CodeUnauthenticated, "malformed signature header")
	}
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return errs.New(errs.CodeUnauthenticated, "malformed signature timestamp")
	}
	if d := now.Sub(time.Unix(sec, 0)); d > tolerance || d < -tolerance {
		return errs.New(errs.CodeUnauthenticated, "signature timestamp outside tolerance")
	}
	want := []byte(mac(secret, ts, body))
	for _, s := range sigs {
		if subtle.ConstantTimeCompare(want, []byte(s)) == 1 {
			return nil
		}
	}
	return errs.New(errs.CodeUnauthenticated, "signature mismatch")
}
