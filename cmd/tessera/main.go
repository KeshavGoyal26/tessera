// Command tessera is the Tessera SFU server.
//
//	tessera --config tessera.yaml
//	tessera --dev                                  # dev keys, text logs
//	tessera create-token --room r --identity alice  # mint a join token
//
// Ownership: WP1. Wiring of WP3/WP4 components is fixed here in Phase 0 so
// those work packages never edit this file (ADR-0011).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/KeshavGoyal26/tessera/pkg/admin"
	"github.com/KeshavGoyal26/tessera/pkg/auth"
	"github.com/KeshavGoyal26/tessera/pkg/config"
	"github.com/KeshavGoyal26/tessera/pkg/dashboard"
	"github.com/KeshavGoyal26/tessera/pkg/metrics"
	"github.com/KeshavGoyal26/tessera/pkg/recorder"
	"github.com/KeshavGoyal26/tessera/pkg/room"
	"github.com/KeshavGoyal26/tessera/pkg/rtc"
	sig "github.com/KeshavGoyal26/tessera/pkg/signal"
	"github.com/KeshavGoyal26/tessera/pkg/stats"
	"github.com/KeshavGoyal26/tessera/pkg/version"
	"github.com/KeshavGoyal26/tessera/pkg/webhook"
)

// Development credentials used by --dev. Never use them in production.
const (
	devAPIKey = "devkey"
	devSecret = "devsecret-devsecret-devsecret-0000"
)

func main() { os.Exit(realMain()) }

func realMain() int {
	if len(os.Args) > 1 && os.Args[1] == "create-token" {
		if err := createToken(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		return 0
	}
	configPath := flag.String("config", "", "path to tessera.yaml")
	dev := flag.Bool("dev", false, "development mode: dev API key, text logs")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version.Version, version.Commit)
		return 0
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if *dev {
		cfg.Development = true
		if len(cfg.Keys) == 0 {
			cfg.Keys = map[string]string{devAPIKey: devSecret}
		}
	}
	logger := cfg.NewLogger()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		stop() // a second Ctrl-C or SIGTERM now ends the process at once
		time.AfterFunc(shutdownLimit, func() {
			logger.Error("shutdown did not finish in time, exiting", "limit", shutdownLimit)
			os.Exit(1)
		})
	}()
	if err := run(ctx, cfg, logger, nil, withDevToken(*dev)); err != nil {
		logger.Error("tessera exited", "err", err)
		return 1
	}
	return 0
}

// shutdownLimit bounds a graceful shutdown (30 s drain plus closing); past it the process exits.
const shutdownLimit = 60 * time.Second

// listeners lets tests inject pre-bound listeners (port 0).
type listeners struct {
	http, metrics net.Listener
}

type runOptions struct{ devToken bool }

type runOption func(*runOptions)

// withDevToken serves GET /dev/token; set only by the --dev flag.
func withDevToken(on bool) runOption { return func(o *runOptions) { o.devToken = on } }

// run wires all components and serves until ctx is cancelled.
func run(ctx context.Context, cfg *config.Config, logger *slog.Logger, ln *listeners, opts ...runOption) error {
	var o runOptions
	for _, f := range opts {
		f(&o)
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	logger.Info("starting tessera", "version", version.Version, "node", cfg.NodeID)

	reg := metrics.NewRegistry("tessera")
	keys := auth.StaticKeys(cfg.Keys)
	verifier := auth.NewVerifier(keys)

	notifier, err := webhook.NewNotifier(cfg.Webhook, keys, reg, logger)
	if err != nil {
		return err
	}
	engine, err := rtc.NewEngine(cfg.RTC, cfg.TURN, reg, logger)
	if err != nil {
		return err
	}
	defer func() { _ = engine.Close() }()

	// The coordinator needs the room manager, and the manager triggers recordings
	// through the coordinator, so the trigger is bound once both exist.
	autoRec := &lateRecordingTrigger{}
	rooms, err := room.NewManager(room.Params{
		Config: cfg, Engine: engine, Notifier: notifier, Keys: keys, Registry: reg, Logger: logger,
		Recording: autoRec,
	})
	if err != nil {
		return err
	}
	defer rooms.Close()

	metrics.HandleExtra(stats.Path, stats.Handler(rooms))

	recKey, recSecret := firstKey(cfg)
	if cfg.Recording.WSURL == "" {
		// Recorders on the same host (the common single-node case) reach signaling over loopback.
		cfg.Recording.WSURL = fmt.Sprintf("ws://127.0.0.1:%d", cfg.Port)
		logger.Info("recording.ws_url not set, recorders will join via loopback", "ws_url", cfg.Recording.WSURL)
	}
	coordinator := recorder.NewCoordinator(recorder.CoordinatorParams{
		Config: cfg.Recording, Rooms: rooms, Notifier: notifier,
		APIKey: recKey, Secret: recSecret, Registry: reg, Logger: logger,
	})
	autoRec.set(coordinator)
	adminSrv := admin.NewServer(admin.Params{
		Rooms: rooms, Recordings: coordinator, Workers: coordinator,
		Verifier: verifier, Registry: reg, Logger: logger,
	})
	signalSrv := sig.NewServer(cfg.Signal, verifier, rooms, logger)

	dash, err := dashboard.New(dashboard.Params{
		Stats: rooms, Keys: keys, Rooms: rooms, Recordings: coordinator,
		RecorderURLs: splitList(os.Getenv("TESSERA_DASHBOARD_RECORDERS")), Logger: logger,
	})
	if err != nil {
		return err
	}
	defer dash.Close()

	mux := http.NewServeMux()
	mux.Handle(sig.Path, signalSrv)
	mux.Handle(admin.PathPrefix, adminSrv)
	mux.Handle(dashboard.PathPrefix+"/", dash)
	mux.Handle(dashboard.PathPrefix, dash)
	if o.devToken {
		mux.Handle(devTokenPath, devTokenHandler(recKey, recSecret))
		logger.Warn("dev token endpoint enabled: anyone who can reach this port can join any room", "path", devTokenPath)
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("tessera " + version.Version + "\n"))
	})

	ready := func() error {
		if err := engine.Ready(); err != nil {
			return err
		}
		return rooms.Ready()
	}

	if ln == nil {
		ln = &listeners{}
	}
	var lc net.ListenConfig
	if ln.http == nil {
		if ln.http, err = lc.Listen(ctx, "tcp", net.JoinHostPort("", strconv.Itoa(cfg.Port))); err != nil {
			return err
		}
	}
	if ln.metrics == nil && cfg.Metrics.Addr != "" {
		if ln.metrics, err = lc.Listen(ctx, "tcp", cfg.Metrics.Addr); err != nil {
			return err
		}
	}

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	wg.Add(1)
	go func() {
		defer wg.Done()
		logger.Info("http listening", "addr", ln.http.Addr().String())
		if err := srv.Serve(ln.http); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	if ln.metrics != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := metrics.ServeListener(ctx, ln.metrics, cfg.Metrics, reg, ready, logger); err != nil {
				errCh <- err
			}
		}()
	}

	select {
	case <-ctx.Done():
	case err = <-errCh:
	}
	logger.Info("shutting down")
	dash.Close()        // ends dashboard event streams, which would otherwise hold Shutdown for its full timeout
	coordinator.Close() // ends recorder long-polls (ClaimJob) for the same reason
	drainCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = rooms.Drain(drainCtx)
	_ = srv.Shutdown(drainCtx)
	_ = notifier.Close(drainCtx)
	if ln.metrics != nil && ctx.Err() == nil {
		_ = ln.metrics.Close()
	}
	wg.Wait()
	return err
}

func firstKey(cfg *config.Config) (string, string) {
	if s, ok := cfg.Keys[cfg.Webhook.APIKey]; ok {
		return cfg.Webhook.APIKey, s
	}
	for k, s := range cfg.Keys {
		return k, s
	}
	return "", ""
}

func createToken(args []string) error {
	fs := flag.NewFlagSet("create-token", flag.ContinueOnError)
	key := fs.String("api-key", devAPIKey, "API key")
	secret := fs.String("secret", devSecret, "API secret")
	roomName := fs.String("room", "", "room name")
	identity := fs.String("identity", "", "participant identity")
	name := fs.String("name", "", "display name")
	ttl := fs.Duration("ttl", 6*time.Hour, "validity")
	adminGrants := fs.Bool("admin", false, "admin token (room_create, room_list, room_admin, room_record)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	g := &auth.RoomGrant{Room: *roomName}
	if *adminGrants {
		g.RoomCreate, g.RoomList, g.RoomAdmin, g.RoomRecord = true, true, true, true
	} else {
		if *roomName == "" || *identity == "" {
			return errors.New("--room and --identity are required (or --admin)")
		}
		g.RoomJoin = true
	}
	tok, err := auth.NewAccessToken(*key, *secret).SetIdentity(*identity).SetName(*name).
		SetGrant(g).SetValidFor(*ttl).ToJWT()
	if err != nil {
		return err
	}
	fmt.Println(tok)
	return nil
}

// splitList splits a comma-separated list, dropping empty entries.
func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// lateRecordingTrigger forwards room.RecordingTrigger calls to a trigger bound after
// the room manager is created. Calls before binding are dropped.
type lateRecordingTrigger struct {
	t atomic.Pointer[room.RecordingTrigger]
}

func (l *lateRecordingTrigger) set(t room.RecordingTrigger) { l.t.Store(&t) }

func (l *lateRecordingTrigger) ParticipantJoined(roomName, identity string) {
	if t := l.t.Load(); t != nil {
		(*t).ParticipantJoined(roomName, identity)
	}
}

func (l *lateRecordingTrigger) ParticipantLeft(roomName, identity string) {
	if t := l.t.Load(); t != nil {
		(*t).ParticipantLeft(roomName, identity)
	}
}
