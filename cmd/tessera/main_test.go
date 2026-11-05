package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/KeshavGoyal26/tessera/pkg/config"
)

// TestRunServesAndShutsDown is a wiring smoke test: the server starts with
// stub components, serves health, admin and signal routes, and stops cleanly.
func TestRunServesAndShutsDown(t *testing.T) {
	cfg := config.Default()
	cfg.Development = true
	cfg.Keys = map[string]string{devAPIKey: devSecret}
	cfg.RTC.UDPPort = 0
	cfg.RTC.TCPPort = 0
	cfg.RTC.IPs = []string{"127.0.0.1"}

	httpLn, _ := net.Listen("tcp", "127.0.0.1:0")
	metricsLn, _ := net.Listen("tcp", "127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	go func() { done <- run(ctx, cfg, logger, &listeners{http: httpLn, metrics: metricsLn}) }()

	base := "http://" + httpLn.Addr().String()
	ops := "http://" + metricsLn.Addr().String()
	waitFor(t, ops+"/healthz")

	checks := []struct {
		method, url string
		want        int
	}{
		{http.MethodGet, ops + "/readyz", 200},
		{http.MethodGet, ops + "/metrics", 200},
		{http.MethodGet, base + "/", 200},
		{http.MethodPost, base + "/twirp/tessera.v1.RoomService/ListRooms", 0}, // routed (any status but 404)
		{http.MethodGet, base + "/rtc?protocol=1", 401},
		{http.MethodGet, base + "/rtc?protocol=99", 426},
	}
	for _, c := range checks {
		req, _ := http.NewRequest(c.method, c.url, strings.NewReader("{}"))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if c.want == 0 && resp.StatusCode != http.StatusNotFound {
			continue
		}
		if resp.StatusCode != c.want {
			t.Errorf("%s %s: got %d want %d", c.method, c.url, resp.StatusCode, c.want)
		}
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown timed out")
	}
}

func waitFor(t *testing.T, url string) {
	t.Helper()
	for range 100 {
		c := &http.Client{Timeout: time.Second}
		if resp, err := c.Get(url); err == nil {
			_ = resp.Body.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s never came up", url)
}

func TestCreateToken(t *testing.T) {
	if err := createToken([]string{"--room", "r", "--identity", "a"}); err != nil {
		t.Fatal(err)
	}
	if err := createToken([]string{"--room", "r"}); err == nil {
		t.Fatal("identity must be required")
	}
}

func TestDevTokenEndpoint(t *testing.T) {
	for _, dev := range []bool{false, true} {
		cfg := config.Default()
		cfg.Development = true // config-only development must not enable the endpoint
		cfg.Keys = map[string]string{devAPIKey: devSecret}
		cfg.RTC.UDPPort, cfg.RTC.TCPPort = 0, 0
		cfg.RTC.IPs = []string{"127.0.0.1"}
		httpLn, _ := net.Listen("tcp", "127.0.0.1:0")
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		go func() { done <- run(ctx, cfg, logger, &listeners{http: httpLn}, withDevToken(dev)) }()
		base := "http://" + httpLn.Addr().String()
		waitFor(t, base+"/")

		resp, err := http.Get(base + "/dev/token?room=r&identity=alice")
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if !dev {
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("without --dev: got %d want 404", resp.StatusCode)
			}
		} else {
			if resp.StatusCode != 200 || !strings.Contains(string(body), `"token"`) {
				t.Errorf("with --dev: got %d %s", resp.StatusCode, body)
			}
			r2, _ := http.Get(base + "/dev/token?room=r")
			_ = r2.Body.Close()
			if r2.StatusCode != http.StatusBadRequest {
				t.Errorf("missing identity: got %d want 400", r2.StatusCode)
			}
		}
		cancel()
		<-done
	}
}
