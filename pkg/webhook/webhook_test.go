package webhook

import (
	"errors"
	"testing"
	"time"

	"github.com/KeshavGoyal26/tessera/pkg/errs"
)

func TestSignVerify(t *testing.T) {
	secret := "0123456789abcdef0123456789abcdef"
	body := []byte(`{"id":"e1","type":"WEBHOOK_EVENT_TYPE_ROOM_STARTED"}`)
	now := time.Unix(1_793_800_000, 0)
	h := Sign(secret, now, body)

	if err := Verify(h, body, secret, now.Add(10*time.Second), DefaultTolerance); err != nil {
		t.Fatal(err)
	}
	cases := map[string]error{
		"tampered": Verify(h, append(body, ' '), secret, now, DefaultTolerance),
		"secret":   Verify(h, body, secret+"x", now, DefaultTolerance),
		"stale":    Verify(h, body, secret, now.Add(10*time.Minute), DefaultTolerance),
		"future":   Verify(h, body, secret, now.Add(-10*time.Minute), DefaultTolerance),
		"garbage":  Verify("nope", body, secret, now, DefaultTolerance),
	}
	for name, err := range cases {
		if !errors.Is(err, errs.ErrUnauthenticated) {
			t.Errorf("%s: got %v", name, err)
		}
	}
}

// TestKnownVector pins the signature algorithm; the same vector is used by
// the Java SDK contract tests (WP3: testdata/contract/webhook).
func TestKnownVector(t *testing.T) {
	got := Sign("secret", time.Unix(1700000000, 0), []byte("{}"))
	const want = "t=1700000000,v1=b8569b78799ff9e3cbff0fc2d63a33a2b57f3282abd07c37ae5e8e7d79a5f163"
	if got != want {
		t.Fatalf("unexpected header %q", got)
	}
}
