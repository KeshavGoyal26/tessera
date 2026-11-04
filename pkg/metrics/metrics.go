// Package metrics provides the Prometheus registry and the operational HTTP
// listener: /metrics, /healthz, /readyz and /debug/pprof (ADR-0010).
//
// Packages register their own collectors on the Registerer passed to their
// constructors; nothing uses the global default registry.
package metrics

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/KeshavGoyal26/tessera/pkg/config"
	"github.com/KeshavGoyal26/tessera/pkg/version"
)

// Namespace prefixes every metric name.
const Namespace = "tessera"

// NewRegistry returns a registry with Go runtime, process and build info collectors.
func NewRegistry(component string) *prometheus.Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace:   Namespace,
			Name:        "build_info",
			Help:        "Build information.",
			ConstLabels: prometheus.Labels{"version": version.Version, "commit": version.Commit, "component": component},
		}, func() float64 { return 1 }),
	)
	return reg
}

// ReadyFunc returns nil when the process is ready to serve traffic.
type ReadyFunc func() error

// Handler returns the operational HTTP handler.
func Handler(cfg config.MetricsConfig, reg *prometheus.Registry, ready ReadyFunc) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{Registry: reg}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if ready != nil {
			if err := ready(); err != nil {
				http.Error(w, "not ready: "+err.Error(), http.StatusServiceUnavailable)
				return
			}
		}
		_, _ = w.Write([]byte("ready\n"))
	})
	if cfg.PProf {
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	}
	return mux
}

// Serve runs the operational listener until ctx is cancelled. It returns
// immediately when cfg.Addr is empty.
func Serve(ctx context.Context, cfg config.MetricsConfig, reg *prometheus.Registry, ready ReadyFunc, logger *slog.Logger) error {
	if cfg.Addr == "" {
		return nil
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", cfg.Addr)
	if err != nil {
		return err
	}
	return ServeListener(ctx, ln, cfg, reg, ready, logger)
}

// ServeListener is Serve on an existing listener (tests use port 0).
func ServeListener(ctx context.Context, ln net.Listener, cfg config.MetricsConfig, reg *prometheus.Registry, ready ReadyFunc, logger *slog.Logger) error {
	srv := &http.Server{Handler: Handler(cfg, reg, ready), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	if logger != nil {
		logger.Info("metrics listening", "addr", ln.Addr().String())
	}
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
