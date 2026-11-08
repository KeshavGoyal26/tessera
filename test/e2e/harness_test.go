// Package e2e runs the real server on loopback with real Pion clients.
package e2e

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"

	"github.com/KeshavGoyal26/tessera/pkg/auth"
	"github.com/KeshavGoyal26/tessera/pkg/client"
	"github.com/KeshavGoyal26/tessera/pkg/config"
	"github.com/KeshavGoyal26/tessera/pkg/room"
	"github.com/KeshavGoyal26/tessera/pkg/rtc"
	"github.com/KeshavGoyal26/tessera/pkg/signal"
	"github.com/KeshavGoyal26/tessera/pkg/webhook"
)

const (
	apiKey    = "e2ekey"
	apiSecret = "e2esecret-e2esecret-e2esecret-0000"
)

type server struct {
	URL     string
	Manager room.Manager
	cfg     *config.Config
	// crash stops the server abruptly (tests of node failures); safe to call twice.
	crash func()
}

func testLogger() *slog.Logger {
	if os.Getenv("E2E_LOG") != "" {
		return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func startServer(t *testing.T, mutate ...func(*config.Config)) *server {
	t.Helper()
	cfg := config.Default()
	cfg.Development = true
	cfg.Keys = map[string]string{apiKey: apiSecret}
	cfg.RTC.UDPPort = 0
	cfg.RTC.IPs = []string{"127.0.0.1"}
	cfg.Signal.ResumeWindow = 3 * time.Second
	for _, m := range mutate {
		m(cfg)
	}
	logger := testLogger()
	eng, err := rtc.NewEngine(cfg.RTC, cfg.TURN, nil, logger)
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := room.NewManager(room.Params{Config: cfg, Engine: eng, Notifier: webhook.Nop{}, Keys: auth.StaticKeys(cfg.Keys), Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(signal.NewServer(cfg.Signal, auth.NewVerifier(auth.StaticKeys(cfg.Keys)), mgr, logger))
	var once sync.Once
	stop := func() {
		once.Do(func() {
			srv.CloseClientConnections()
			mgr.Close()
			srv.Close()
			_ = eng.Close()
		})
	}
	t.Cleanup(stop)
	return &server{URL: "ws://" + strings.TrimPrefix(srv.URL, "http://"), Manager: mgr, cfg: cfg, crash: stop}
}

func token(t *testing.T, roomName, identity string, mut ...func(*auth.RoomGrant)) string {
	t.Helper()
	g := &auth.RoomGrant{Room: roomName, RoomJoin: true}
	for _, m := range mut {
		m(g)
	}
	tok, err := auth.NewAccessToken(apiKey, apiSecret).SetIdentity(identity).SetGrant(g).ToJWT()
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func connect(t *testing.T, s *server, roomName, identity string, mut ...func(*auth.RoomGrant)) client.Client {
	t.Helper()
	c := client.New(client.Options{AutoSubscribe: true, SettingEngine: client.LoopbackSettingEngine(), Logger: testLogger()})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Connect(ctx, s.URL, token(t, roomName, identity, mut...)); err != nil {
		t.Fatalf("connect %s: %v", identity, err)
	}
	t.Cleanup(func() { _ = c.Disconnect(context.Background()) })
	return c
}

// synth publishes synthetic RTP on a TrackLocalStaticRTP.
type synth struct {
	track   *webrtc.TrackLocalStaticRTP
	video   bool
	sn      uint16
	ts      uint32
	n       uint32
	stop    chan struct{}
	stopped chan struct{}
}

// payload layout: [H.264 NAL header 1B, video only][counter 4B][pattern 32B]
var pattern = []byte("tessera-e2e-payload-integrity-ok")

func newSynth(t *testing.T, kind string, id string) *synth {
	t.Helper()
	cap := webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}
	if kind == "video" {
		cap = h264Cap
	}
	tr, err := webrtc.NewTrackLocalStaticRTP(cap, id, "stream-"+id)
	if err != nil {
		t.Fatal(err)
	}
	return &synth{track: tr, video: kind == "video", stop: make(chan struct{}), stopped: make(chan struct{})}
}

func (s *synth) packet() *rtp.Packet {
	s.n++
	p := &rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: s.sn, Timestamp: s.ts, Marker: true}}
	s.sn++
	body := make([]byte, 0, 64)
	if s.video {
		nal := byte(0x41) // non-IDR slice
		if s.n%30 == 1 {
			nal = 0x65 // IDR slice: a keyframe (no in-band SPS in this stream)
		}
		body = append(body, nal)
		s.ts += 3000
	} else {
		s.ts += 960
	}
	body = append(body, byte(s.n>>24), byte(s.n>>16), byte(s.n>>8), byte(s.n))
	body = append(body, pattern...)
	p.Payload = body
	return p
}

func (s *synth) run() {
	go func() {
		defer close(s.stopped)
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-tick.C:
				_ = s.track.WriteRTP(s.packet())
			}
		}
	}()
}

func (s *synth) Stop() {
	close(s.stop)
	<-s.stopped
}

// checkPayload validates the synthetic payload and returns its counter.
func checkPayload(t *testing.T, video bool, payload []byte) (uint32, bool) {
	off := 0
	if video {
		off = 1
	}
	if len(payload) != off+4+len(pattern) || string(payload[off+4:]) != string(pattern) {
		return 0, false
	}
	b := payload[off:]
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3]), true
}
