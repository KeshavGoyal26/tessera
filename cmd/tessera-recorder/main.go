// Command tessera-recorder runs recording workers that pull jobs from a
// tessera server and write HLS (ADR-0007).
//
//	tessera-recorder --config recorder.yaml
//
// Ownership: WP4.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

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
	worker, err := recorder.NewWorker(cfg, reg, logger)
	if err != nil {
		logger.Error("init failed", "err", err)
		return 1
	}
	go func() {
		if err := metrics.Serve(ctx, cfg.Metrics, reg, worker.Ready, logger); err != nil {
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
