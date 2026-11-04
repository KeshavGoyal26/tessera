package webhook

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/config"
)

const secret = "0123456789abcdef0123456789abcdef"

type recv struct {
	mu     sync.Mutex
	events []*tesserav1.WebhookEvent
	sigOK  bool
	fail   atomic.Int32 // number of initial requests to reject
	hits   atomic.Int32
}

func (r *recv) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	r.hits.Add(1)
	if r.fail.Add(-1) >= 0 {
		http.Error(w, "nope", 500)
		return
	}
	ev := &tesserav1.WebhookEvent{}
	if req.Header.Get("Content-Type") == "application/protobuf" {
		_ = proto.Unmarshal(body, ev)
	} else {
		_ = protojson.Unmarshal(body, ev)
	}
	err := Verify(req.Header.Get(HeaderSignature), body, secret, time.Now(), DefaultTolerance)
	r.mu.Lock()
	r.sigOK = err == nil && req.Header.Get(HeaderKey) == "k" && req.Header.Get(HeaderEventID) == ev.Id
	r.events = append(r.events, ev)
	r.mu.Unlock()
}

func (r *recv) got() []*tesserav1.WebhookEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*tesserav1.WebhookEvent(nil), r.events...)
}

func fast() options {
	return options{
		backoff: func(int) time.Duration { return time.Millisecond },
		sleep:   func(context.Context, time.Duration) {},
	}
}

func ev(name string) *tesserav1.WebhookEvent {
	return &tesserav1.WebhookEvent{Type: tesserav1.WebhookEventType_WEBHOOK_EVENT_TYPE_ROOM_STARTED, Room: &tesserav1.Room{Name: name}}
}

func closeN(t *testing.T, n Notifier) {
	t.Helper()
	ctx, c := context.WithTimeout(context.Background(), 5*time.Second)
	defer c()
	if err := n.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestDeliveryOrderSignatureAndEncodings(t *testing.T) {
	for _, enc := range []string{"json", "protobuf", ""} {
		r := &recv{}
		srv := httptest.NewServer(r)
		n, err := newNotifier(config.WebhookConfig{APIKey: "k", Endpoints: []config.WebhookEndpoint{{URL: srv.URL, Encoding: enc}}}, secret, nil, nil, fast())
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 20; i++ {
			n.Notify(context.Background(), ev(string(rune('a'+i))))
		}
		closeN(t, n)
		srv.Close()
		got := r.got()
		if len(got) != 20 {
			t.Fatalf("%s: got %d events", enc, len(got))
		}
		for i, e := range got {
			if e.Room.Name != string(rune('a'+i)) {
				t.Fatalf("%s: out of order at %d: %s", enc, i, e.Room.Name)
			}
			if e.Id == "" || e.CreatedAt == 0 {
				t.Fatalf("id/created_at not filled: %v", e)
			}
		}
		if !r.sigOK {
			t.Fatalf("%s: signature/headers invalid", enc)
		}
		if s := n.Stats()[0]; s.Sent != 20 || s.Failed != 0 || s.Retried != 0 || s.Queued != 0 {
			t.Fatalf("stats: %+v", s)
		}
	}
}

func TestRetriesThenSucceeds(t *testing.T) {
	r := &recv{}
	r.fail.Store(3)
	srv := httptest.NewServer(r)
	defer srv.Close()
	n, _ := newNotifier(config.WebhookConfig{APIKey: "k", Endpoints: []config.WebhookEndpoint{{URL: srv.URL}}}, secret, nil, nil, fast())
	n.Notify(context.Background(), ev("x"))
	closeN(t, n)
	s := n.Stats()[0]
	if s.Sent != 1 || s.Retried != 3 || s.Failed != 0 || len(r.got()) != 1 {
		t.Fatalf("stats %+v events %d", s, len(r.got()))
	}
}

func TestGivesUpAfterMaxAttemptsAndContinues(t *testing.T) {
	r := &recv{}
	r.fail.Store(MaxAttempts) // first event exhausts all attempts
	srv := httptest.NewServer(r)
	defer srv.Close()
	n, _ := newNotifier(config.WebhookConfig{APIKey: "k", Endpoints: []config.WebhookEndpoint{{URL: srv.URL}}}, secret, nil, nil, fast())
	n.Notify(context.Background(), ev("lost"))
	n.Notify(context.Background(), ev("kept"))
	closeN(t, n)
	s := n.Stats()[0]
	if s.Failed != 1 || s.Sent != 1 || s.Retried != MaxAttempts-1 || s.LastError != "" {
		t.Fatalf("stats %+v", s)
	}
	if g := r.got(); len(g) != 1 || g[0].Room.Name != "kept" {
		t.Fatalf("got %v", g)
	}
}

func TestBackoffSchedule(t *testing.T) {
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second,
		32 * time.Second, 64 * time.Second, 128 * time.Second, 256 * time.Second, MaxBackoff, MaxBackoff}
	for i, w := range want {
		if got := defaultBackoff(i + 1); got != w {
			t.Errorf("backoff(%d)=%v want %v", i+1, got, w)
		}
	}
}

func TestSleepReceivesBackoff(t *testing.T) {
	r := &recv{}
	r.fail.Store(2)
	srv := httptest.NewServer(r)
	defer srv.Close()
	var mu sync.Mutex
	var slept []time.Duration
	o := options{sleep: func(_ context.Context, d time.Duration) { mu.Lock(); slept = append(slept, d); mu.Unlock() }}
	n, _ := newNotifier(config.WebhookConfig{APIKey: "k", Endpoints: []config.WebhookEndpoint{{URL: srv.URL}}}, secret, nil, nil, o)
	n.Notify(context.Background(), ev("x"))
	closeN(t, n)
	if len(slept) != 2 || slept[0] != time.Second || slept[1] != 2*time.Second {
		t.Fatalf("slept %v", slept)
	}
}

func TestQueueOverflowDropsOldestAndReportsNumDropped(t *testing.T) {
	block := make(chan struct{})
	r := &recv{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		<-block
		r.ServeHTTP(w, q)
	}))
	defer srv.Close()
	n, _ := newNotifier(config.WebhookConfig{APIKey: "k", QueueSize: 3, Endpoints: []config.WebhookEndpoint{{URL: srv.URL}}}, secret, nil, nil, fast())
	n.Notify(context.Background(), ev("e0")) // in flight (blocked)
	time.Sleep(50 * time.Millisecond)
	start := time.Now()
	for i := 1; i <= 8; i++ { // queue holds e0 plus 2 newest of the rest
		n.Notify(context.Background(), ev("e"+string(rune('0'+i))))
	}
	if time.Since(start) > time.Second {
		t.Fatal("Notify blocked")
	}
	close(block)
	closeN(t, n)
	got := r.got()
	if len(got) != 3 || got[0].Room.Name != "e0" || got[1].Room.Name != "e7" || got[2].Room.Name != "e8" {
		names := []string{}
		for _, g := range got {
			names = append(names, g.Room.Name)
		}
		t.Fatalf("got %v", names)
	}
	var total uint32
	for _, g := range got {
		total += g.NumDropped
	}
	if s := n.Stats()[0]; s.Dropped != 6 || total != 6 {
		t.Fatalf("dropped stat %d, num_dropped sum %d", s.Dropped, total)
	}
}

func TestSurvivesReceiverDowntime(t *testing.T) {
	r := &recv{}
	var down atomic.Bool
	down.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if down.Load() {
			http.Error(w, "down", 503)
			return
		}
		r.ServeHTTP(w, q)
	}))
	defer srv.Close()
	n, _ := newNotifier(config.WebhookConfig{APIKey: "k", Endpoints: []config.WebhookEndpoint{{URL: srv.URL}}}, secret, nil, nil,
		options{backoff: func(int) time.Duration { return 5 * time.Millisecond }})
	n.Notify(context.Background(), ev("a"))
	n.Notify(context.Background(), ev("b"))
	time.Sleep(30 * time.Millisecond)
	down.Store(false)
	closeN(t, n)
	if g := r.got(); len(g) != 2 || g[0].Room.Name != "a" {
		t.Fatalf("got %v", g)
	}
}

func TestCloseTimeoutAborts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) { http.Error(w, "x", 500) }))
	defer srv.Close()
	n, _ := newNotifier(config.WebhookConfig{APIKey: "k", Endpoints: []config.WebhookEndpoint{{URL: srv.URL}}}, secret, nil, nil, options{})
	n.Notify(context.Background(), ev("a"))
	ctx, c := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer c()
	if err := n.Close(ctx); err == nil {
		t.Fatal("expected deadline error")
	}
	n.Notify(context.Background(), ev("after")) // no panic, dropped
}

func TestNewNotifierConfig(t *testing.T) {
	n, err := NewNotifier(config.WebhookConfig{}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := n.(Nop); !ok {
		t.Fatal("expected Nop")
	}
	cfg := config.WebhookConfig{APIKey: "k", Endpoints: []config.WebhookEndpoint{{URL: "http://127.0.0.1:1/x"}}}
	if _, err := NewNotifier(cfg, staticKeys{}, nil, nil); err == nil {
		t.Fatal("expected unknown key error")
	}
	cfg.Endpoints[0].Encoding = "xml"
	if _, err := NewNotifier(cfg, staticKeys{"k": secret}, nil, nil); err == nil {
		t.Fatal("expected encoding error")
	}
	cfg.Endpoints[0].Encoding = ""
	n, err = NewNotifier(cfg, staticKeys{"k": secret}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := n.(StatsProvider); !ok {
		t.Fatal("notifier should provide stats")
	}
	ctx, c := context.WithTimeout(context.Background(), time.Second)
	defer c()
	_ = n.Close(ctx)
}

type staticKeys map[string]string

func (s staticKeys) Secret(k string) (string, bool) { v, ok := s[k]; return v, ok }
