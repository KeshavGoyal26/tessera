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
	go func() {
		<-ctx.Done()
		stop() // a second Ctrl-C or SIGTERM ends the process at once
		// Finishing recordings may take up to a minute (flush, upload); never hang past that.
		time.AfterFunc(90*time.Second, func() {
			logger.Error("shutdown did not finish in time, exiting")
			os.Exit(1)
		})
	}()

	reg := metrics.NewRegistry("recorder")
	api := admin.NewClient(cfg.ServerURL, cfg.APIKey, cfg.Secret, admin.WorkerGrant())
	worker, err := recorder.NewWorker(cfg, reg, logger, recorder.WithJobAPI(api))
	if err != nil {
		logger.Error("init failed", "err", err)
		return 1
	}
	go func() {
		if err := serveOps(ctx, cfg, reg, worker, logger); err != nil {
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

// serveOps serves /metrics, /healthz, /readyz and the recorder's /stats, plus local
// recordings under /recordings/ when storage.local_base_url is set.
func serveOps(ctx context.Context, rc *recorder.Config, reg *prometheus.Registry, w *recorder.Worker, logger *slog.Logger) error {
	cfg := rc.Metrics
	if cfg.Addr == "" {
		return nil
	}
	mux := http.NewServeMux()
	mux.Handle("/stats", w.StatsHandler())
	if rc.Storage.Kind != "s3" && rc.Storage.LocalBaseURL != "" {
		mux.Handle("/recordings/", recorder.RecordingsHandler(rc.Storage.LocalRoot))
		logger.Info("serving local recordings", "url", rc.Storage.LocalBaseURL, "dir", rc.Storage.LocalRoot)
	}
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
