package metrics

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KeshavGoyal26/tessera/pkg/config"
)

func get(t *testing.T, h http.Handler, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	b, _ := io.ReadAll(rec.Body)
	return rec.Code, string(b)
}

func TestHandler(t *testing.T) {
	ready := errors.New("draining")
	h := Handler(config.MetricsConfig{PProf: true}, NewRegistry("test"), func() error { return ready })

	if code, _ := get(t, h, "/healthz"); code != 200 {
		t.Fatalf("healthz %d", code)
	}
	if code, _ := get(t, h, "/readyz"); code != 503 {
		t.Fatalf("readyz while not ready %d", code)
	}
	ready = nil
	if code, _ := get(t, h, "/readyz"); code != 200 {
		t.Fatalf("readyz %d", code)
	}
	code, body := get(t, h, "/metrics")
	if code != 200 || !strings.Contains(body, "tessera_build_info") {
		t.Fatalf("metrics %d", code)
	}
	if code, _ := get(t, h, "/debug/pprof/"); code != 200 {
		t.Fatalf("pprof %d", code)
	}
}

func TestPProfDisabled(t *testing.T) {
	h := Handler(config.MetricsConfig{}, NewRegistry("test"), nil)
	if code, _ := get(t, h, "/debug/pprof/"); code != 404 {
		t.Fatalf("pprof must be off, got %d", code)
	}
}
