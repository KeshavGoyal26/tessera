package webhook

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/auth"
	"github.com/KeshavGoyal26/tessera/pkg/config"
	"github.com/KeshavGoyal26/tessera/pkg/errs"
)

// Delivery policy (PROTOCOL.md 4).
const (
	MaxAttempts    = 10
	InitialBackoff = time.Second
	MaxBackoff     = 5 * time.Minute
)

// EndpointStats are delivery counters of one endpoint, exposed to the admin
// dashboard. Sent counts events acknowledged with 2xx, Failed counts events
// given up after MaxAttempts, Retried counts re-attempts, Dropped counts events
// evicted by queue overflow.
type EndpointStats struct {
	URL       string `json:"url"`
	Sent      uint64 `json:"sent"`
	Failed    uint64 `json:"failed"`
	Retried   uint64 `json:"retried"`
	Dropped   uint64 `json:"dropped"`
	Queued    int    `json:"queued"`
	LastError string `json:"last_error,omitempty"`
}

// StatsProvider is implemented by the HTTP notifier.
type StatsProvider interface {
	Stats() []EndpointStats
}

// NewNotifier creates the HTTP notifier for cfg.Endpoints, signing with the
// secret of cfg.APIKey. Without endpoints it returns Nop.
func NewNotifier(cfg config.WebhookConfig, keys auth.KeyProvider, reg prometheus.Registerer, logger *slog.Logger) (Notifier, error) {
	if len(cfg.Endpoints) == 0 {
		return Nop{}, nil
	}
	secret, ok := keys.Secret(cfg.APIKey)
	if !ok {
		return nil, errs.New(errs.CodeInvalidArgument, "webhook.api_key %q is not a configured API key", cfg.APIKey)
	}
	return newNotifier(cfg, secret, reg, logger, options{})
}

type options struct {
	client *http.Client
	// backoff returns the delay before retry number n (1-based).
	backoff func(n int) time.Duration
	// sleep waits d or until ctx is done.
	sleep func(ctx context.Context, d time.Duration)
	now   func() time.Time
}

func defaultBackoff(n int) time.Duration {
	d := InitialBackoff
	for i := 1; i < n; i++ {
		d *= 2
		if d >= MaxBackoff {
			return MaxBackoff
		}
	}
	return d
}

func realSleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

type httpNotifier struct {
	cfg    config.WebhookConfig
	secret string
	opt    options
	log    *slog.Logger
	eps    []*endpoint

	ctx    context.Context // cancelled to abort in-flight deliveries
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mSent, mFailed, mRetried, mDropped *prometheus.CounterVec
	mQueued                            *prometheus.GaugeVec
}

type endpoint struct {
	n      *httpNotifier
	cfg    config.WebhookEndpoint
	proto  bool
	cap    int
	mu     sync.Mutex
	queue  []*tesserav1.WebhookEvent
	dropN  uint32 // dropped since last delivered event
	flight bool   // queue[0] is being delivered and must not be evicted
	closed bool
	wake   chan struct{}
	done   chan struct{}
	st     EndpointStats
}

func newNotifier(cfg config.WebhookConfig, secret string, reg prometheus.Registerer, logger *slog.Logger, o options) (*httpNotifier, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if o.client == nil {
		to := cfg.Timeout
		if to <= 0 {
			to = 10 * time.Second
		}
		o.client = &http.Client{Timeout: to}
	}
	if o.backoff == nil {
		o.backoff = defaultBackoff
	}
	if o.sleep == nil {
		o.sleep = realSleep
	}
	if o.now == nil {
		o.now = time.Now
	}
	n := &httpNotifier{cfg: cfg, secret: secret, opt: o, log: logger}
	n.ctx, n.cancel = context.WithCancel(context.Background())
	mk := func(name, help string) *prometheus.CounterVec {
		return prometheus.NewCounterVec(prometheus.CounterOpts{Name: name, Help: help}, []string{"url"})
	}
	n.mSent = mk("tessera_webhook_sent_total", "Webhook events delivered (2xx).")
	n.mFailed = mk("tessera_webhook_failed_total", "Webhook events given up after all attempts.")
	n.mRetried = mk("tessera_webhook_retried_total", "Webhook delivery retries.")
	n.mDropped = mk("tessera_webhook_dropped_total", "Webhook events dropped by queue overflow.")
	n.mQueued = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "tessera_webhook_queue_length", Help: "Queued webhook events."}, []string{"url"})
	if reg != nil {
		for _, c := range []prometheus.Collector{n.mSent, n.mFailed, n.mRetried, n.mDropped, n.mQueued} {
			if err := reg.Register(c); err != nil {
				logger.Warn("webhook metrics registration failed", "err", err)
			}
		}
	}
	qs := cfg.QueueSize
	if qs <= 0 {
		qs = 1000
	}
	for _, ec := range cfg.Endpoints {
		if ec.URL == "" {
			return nil, errs.New(errs.CodeInvalidArgument, "webhook endpoint url is empty")
		}
		switch ec.Encoding {
		case "", "json", "protobuf":
		default:
			return nil, errs.New(errs.CodeInvalidArgument, "webhook endpoint %s: unknown encoding %q", ec.URL, ec.Encoding)
		}
		e := &endpoint{n: n, cfg: ec, proto: ec.Encoding == "protobuf", cap: qs,
			wake: make(chan struct{}, 1), done: make(chan struct{}), st: EndpointStats{URL: ec.URL}}
		n.eps = append(n.eps, e)
		n.wg.Add(1)
		go e.run()
	}
	return n, nil
}

// Notify implements Notifier. It never blocks: the event is cloned and
// appended to each endpoint queue, evicting the oldest event when full.
func (n *httpNotifier) Notify(_ context.Context, ev *tesserav1.WebhookEvent) {
	if ev == nil {
		return
	}
	ev = proto.Clone(ev).(*tesserav1.WebhookEvent)
	if ev.Id == "" {
		ev.Id = newID()
	}
	if ev.CreatedAt == 0 {
		ev.CreatedAt = n.opt.now().Unix()
	}
	for _, e := range n.eps {
		e.enqueue(ev)
	}
}

func (e *endpoint) enqueue(ev *tesserav1.WebhookEvent) {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	if len(e.queue) >= e.cap {
		i := 0
		if e.flight && len(e.queue) > 1 {
			i = 1
		}
		e.queue = append(e.queue[:i], e.queue[i+1:]...)
		e.dropN++
		e.st.Dropped++
		e.n.mDropped.WithLabelValues(e.cfg.URL).Inc()
	}
	e.queue = append(e.queue, ev)
	e.n.mQueued.WithLabelValues(e.cfg.URL).Set(float64(len(e.queue)))
	e.mu.Unlock()
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// next returns the oldest event without removing it (it stays queued while
// being delivered so Stats shows it and order is preserved).
// closed is read under the same lock as the queue: checking them separately let an event
// queued just before Close be dropped (the loop saw an empty queue, then closed).
func (e *endpoint) next() (ev *tesserav1.WebhookEvent, dropped uint32, ok, closed bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.queue) == 0 {
		return nil, 0, false, e.closed
	}
	e.flight = true
	return e.queue[0], e.dropN, true, e.closed
}

// pop removes the head if it is still ev (overflow may have evicted it).
func (e *endpoint) pop(ev *tesserav1.WebhookEvent, resetDropped uint32) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.flight = false
	if len(e.queue) > 0 && e.queue[0] == ev {
		e.queue = e.queue[1:]
	}
	e.dropN -= min(e.dropN, resetDropped)
	e.n.mQueued.WithLabelValues(e.cfg.URL).Set(float64(len(e.queue)))
}

func (e *endpoint) run() {
	defer e.n.wg.Done()
	defer close(e.done)
	for {
		ev, dropped, ok, closed := e.next()
		if !ok {
			if closed {
				return
			}
			select {
			case <-e.wake:
			case <-e.n.ctx.Done():
				return
			}
			continue
		}
		if e.n.ctx.Err() != nil {
			return
		}
		e.deliver(ev, dropped)
	}
}

func (e *endpoint) deliver(ev *tesserav1.WebhookEvent, dropped uint32) {
	out := proto.Clone(ev).(*tesserav1.WebhookEvent)
	out.NumDropped = dropped
	var body []byte
	var err error
	ct := "application/json"
	if e.proto {
		ct = "application/protobuf"
		body, err = proto.Marshal(out)
	} else {
		body, err = protojson.Marshal(out)
	}
	if err != nil {
		e.n.log.Error("webhook encode failed", "err", err)
		e.pop(ev, dropped)
		return
	}
	var lastErr error
	for attempt := 1; attempt <= MaxAttempts; attempt++ {
		if attempt > 1 {
			e.count(func(s *EndpointStats) { s.Retried++ })
			e.n.mRetried.WithLabelValues(e.cfg.URL).Inc()
			e.n.opt.sleep(e.n.ctx, e.n.opt.backoff(attempt-1))
			if e.n.ctx.Err() != nil {
				return
			}
		}
		if lastErr = e.post(out, body, ct); lastErr == nil {
			e.count(func(s *EndpointStats) { s.Sent++; s.LastError = "" })
			e.n.mSent.WithLabelValues(e.cfg.URL).Inc()
			e.pop(ev, dropped)
			return
		}
		e.count(func(s *EndpointStats) { s.LastError = lastErr.Error() })
	}
	e.n.log.Warn("webhook delivery failed permanently", "url", e.cfg.URL, "event", out.Id, "err", lastErr)
	e.count(func(s *EndpointStats) { s.Failed++ })
	e.n.mFailed.WithLabelValues(e.cfg.URL).Inc()
	e.pop(ev, dropped)
}

func (e *endpoint) count(f func(*EndpointStats)) {
	e.mu.Lock()
	f(&e.st)
	e.mu.Unlock()
}

func (e *endpoint) post(ev *tesserav1.WebhookEvent, body []byte, ct string) error {
	n := e.n
	req, err := http.NewRequestWithContext(n.ctx, http.MethodPost, e.cfg.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", ct)
	req.Header.Set(HeaderEventID, ev.Id)
	req.Header.Set(HeaderEventType, ev.Type.String())
	req.Header.Set(HeaderKey, n.cfg.APIKey)
	req.Header.Set(HeaderSignature, Sign(n.secret, n.opt.now(), body))
	req.Header.Set("User-Agent", "tessera-webhook/1")
	resp, err := n.opt.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("endpoint answered HTTP %d", resp.StatusCode)
	}
	return nil
}

// Stats returns a snapshot of per-endpoint delivery counters.
func (n *httpNotifier) Stats() []EndpointStats {
	out := make([]EndpointStats, 0, len(n.eps))
	for _, e := range n.eps {
		e.mu.Lock()
		s := e.st
		s.Queued = len(e.queue)
		e.mu.Unlock()
		out = append(out, s)
	}
	return out
}

// Close stops accepting events and delivers what is queued until ctx is done,
// then aborts in-flight work.
func (n *httpNotifier) Close(ctx context.Context) error {
	for _, e := range n.eps {
		e.mu.Lock()
		e.closed = true
		e.mu.Unlock()
		select {
		case e.wake <- struct{}{}:
		default:
		}
	}
	flushed := make(chan struct{})
	go func() { n.wg.Wait(); close(flushed) }()
	select {
	case <-flushed:
		n.cancel()
		return nil
	case <-ctx.Done():
		n.cancel()
		<-flushed
		return ctx.Err()
	}
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
