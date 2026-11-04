// Command tessera-recorder runs recording workers that pull jobs from a
// tessera server and write HLS (ADR-0007).
//
//	tessera-recorder --config recorder.yaml
//
// Ownership: WP4.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/KeshavGoyal26/tessera/pkg/admin"
	"github.com/KeshavGoyal26/tessera/pkg/config"
	"github.com/KeshavGoyal26/tessera/pkg/metrics"
	"github.com/KeshavGoyal26/tessera/pkg/recorder"
	"github.com/KeshavGoyal26/tessera/pkg/version"
)

func main() { os.Exit(realMain()) }

func realMain() int {
	configPath := flag.String("config", "", "path to recorder.yaml")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version.Version, version.Commit)
		return 0
	}

	cfg := recorder.DefaultConfig()
	if *configPath != "" {
		if err := config.LoadYAML(*configPath, cfg); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	logger := config.NewLogger(cfg.Logging, cfg.Development)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	reg := metrics.NewRegistry("recorder")
	api := admin.NewClient(cfg.ServerURL, cfg.APIKey, cfg.Secret, admin.WorkerGrant())
	worker, err := recorder.NewWorker(cfg, reg, logger, recorder.WithJobAPI(api))
	if err != nil {
		logger.Error("init failed", "err", err)
		return 1
	}
	go func() {
		if err := serveOps(ctx, cfg.Metrics, reg, worker, logger); err != nil {
			logger.Error("metrics listener failed", "err", err)
		}
	}()
	logger.Info("starting tessera-recorder", "version", version.Version, "server", cfg.ServerURL)
	if err := worker.Run(ctx); err != nil && ctx.Err() == nil {
		logger.Error("recorder exited", "err", err)
		return 1
	}
	return 0
}

// serveOps serves /metrics, /healthz, /readyz and the recorder's /stats.
func serveOps(ctx context.Context, cfg config.MetricsConfig, reg *prometheus.Registry, w *recorder.Worker, logger *slog.Logger) error {
	if cfg.Addr == "" {
		return nil
	}
	mux := http.NewServeMux()
	mux.Handle("/stats", w.StatsHandler())
	mux.Handle("/", metrics.Handler(cfg, reg, w.Ready))
	srv := &http.Server{Addr: cfg.Addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	logger.Info("recorder ops listening", "addr", cfg.Addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
