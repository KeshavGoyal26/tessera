package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/KeshavGoyal26/tessera/pkg/auth"
)

func TestDevTokenRoles(t *testing.T) {
	h := devTokenHandler(devAPIKey, devSecret)
	verify := func(query string) (*auth.Claims, int) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dev/token?room=live&identity=a&"+query, nil))
		if rec.Code != http.StatusOK {
			return nil, rec.Code
		}
		var body struct{ Token string }
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		c, err := auth.NewVerifier(auth.StaticKeys{devAPIKey: devSecret}).Verify(body.Token)
		if err != nil {
			t.Fatal(err)
		}
		return c, rec.Code
	}

	c, _ := verify("role=presenter&record=1")
	p := c.Grants.ToPermission()
	if !p.CanPublish || p.CanSubscribe || p.Hidden || !c.Grants.AutoRecord || c.Attributes["role"] != "presenter" || c.Attributes["recorded"] != "true" {
		t.Fatalf("presenter: %+v %+v", p, c)
	}
	c, _ = verify("role=viewer&record=1")
	p = c.Grants.ToPermission()
	if p.CanPublish || p.CanPublishData || !p.CanSubscribe || !p.Hidden || c.Grants.AutoRecord || c.Attributes["recorded"] != "" {
		t.Fatalf("viewer: %+v %+v", p, c)
	}
	c, _ = verify("")
	p = c.Grants.ToPermission()
	if !p.CanPublish || !p.CanSubscribe || p.Hidden || c.Attributes != nil {
		t.Fatalf("participant: %+v %+v", p, c)
	}
	if _, code := verify("role=admin"); code != http.StatusBadRequest {
		t.Fatalf("unknown role: got %d", code)
	}
}
