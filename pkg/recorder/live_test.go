package recorder

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/auth"
	"github.com/KeshavGoyal26/tessera/pkg/client"
	"github.com/KeshavGoyal26/tessera/pkg/config"
	"github.com/KeshavGoyal26/tessera/pkg/hls/hlstest"
	"github.com/KeshavGoyal26/tessera/pkg/room"
	"github.com/KeshavGoyal26/tessera/pkg/rtc"
	"github.com/KeshavGoyal26/tessera/pkg/signal"
	"github.com/KeshavGoyal26/tessera/pkg/webhook"
)

// TestLiveRecordingThroughRealServer records a participant (H.264 + Opus) that
// publishes through a real SFU on loopback, with the production ClientConnector.
func TestLiveRecordingThroughRealServer(t *testing.T) {
	hlstest.NeedFFmpeg(t)
	const key, secret = "livekey", "livesecret-livesecret-livesecret-00"
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	cfg := config.Default()
	cfg.Development = true
	cfg.Keys = map[string]string{key: secret}
	cfg.RTC.UDPPort = 0
	cfg.RTC.IPs = []string{"127.0.0.1"}
	eng, err := rtc.NewEngine(cfg.RTC, cfg.TURN, nil, logger)
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := room.NewManager(room.Params{Config: cfg, Engine: eng, Notifier: webhook.Nop{}, Keys: auth.StaticKeys(cfg.Keys), Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(signal.NewServer(cfg.Signal, auth.NewVerifier(auth.StaticKeys(cfg.Keys)), mgr, logger))
	t.Cleanup(func() { mgr.Close(); srv.Close(); _ = eng.Close() })
	wsURL := "ws://" + strings.TrimPrefix(srv.URL, "http://")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Publisher: real Opus frames and a real H.264 stream, paced in real time.
	tok, _ := auth.NewAccessToken(key, secret).SetIdentity("alice").SetGrant(&auth.RoomGrant{Room: "live", RoomJoin: true}).ToJWT()
	pub := client.New(client.Options{SettingEngine: client.LoopbackSettingEngine(), Logger: logger})
	cctx, ccancel := context.WithTimeout(ctx, 15*time.Second)
	defer ccancel()
	if err := pub.Connect(cctx, wsURL, tok); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pub.Disconnect(context.Background()) }()
	vtrack, _ := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000,
		SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f"}, "video", "s")
	atrack, _ := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, "audio", "s")
	if _, err := pub.PublishTrack(cctx, vtrack, client.PublishOptions{Name: "cam", Source: tesserav1.TrackSource_TRACK_SOURCE_CAMERA, Width: 320, Height: 240}); err != nil {
		t.Fatal(err)
	}
	if _, err := pub.PublishTrack(cctx, atrack, client.PublishOptions{Name: "mic", Source: tesserav1.TrackSource_TRACK_SOURCE_MICROPHONE}); err != nil {
		t.Fatal(err)
	}
	aus := hlstest.H264(t, 12)
	opus := hlstest.Opus(t, 12)
	go func() {
		p := &hlstest.Packetizer{SSRC: 1}
		vt := time.NewTicker(40 * time.Millisecond)
		at := time.NewTicker(20 * time.Millisecond)
		defer vt.Stop()
		defer at.Stop()
		vi, ai := 0, 0
		for ctx.Err() == nil && (vi < len(aus) || ai < len(opus)) {
			select {
			case <-ctx.Done():
				return
			case <-vt.C:
				if vi < len(aus) {
					for _, pkt := range p.Packetize(aus[vi], uint32(vi*3600)) {
						_ = vtrack.WriteRTP(pkt)
					}
					vi++
				}
			case <-at.C:
				if ai < len(opus) {
					_ = atrack.WriteRTP(hlstest.OpusPacket(uint16(ai), uint32(ai*960), 2, opus[ai]))
					ai++
				}
			}
		}
	}()

	// Control plane and worker, as deployed: Coordinator in the server, Worker pulling jobs.
	coord := NewCoordinator(CoordinatorParams{Config: config.RecordingConfig{WSURL: wsURL, HeartbeatTimeout: time.Minute},
		Rooms: mgr, APIKey: key, Secret: secret, Logger: logger})
	defer coord.Close()
	root := t.TempDir()
	wcfg := DefaultConfig()
	wcfg.WorkerID, wcfg.Capacity, wcfg.Heartbeat = "live-worker", 10, 100*time.Millisecond
	wcfg.Storage.LocalRoot = root
	w, err := NewWorker(wcfg, nil, logger, WithJobAPI(coord), WithConnector(ClientConnector{Logger: logger, SettingEngine: client.LoopbackSettingEngine()}))
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = w.Run(ctx) }()

	info, err := coord.StartRecording(ctx, &tesserav1.StartRecordingRequest{Room: "live",
		Target: &tesserav1.StartRecordingRequest_Participant{Participant: &tesserav1.ParticipantRecordingTarget{Identity: "alice"}}})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "segments from the live stream", func() bool { return w.Stats().SegmentsWritten >= 2 })
	if _, err := coord.StopRecording(ctx, info.RecordingId); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "COMPLETE", func() bool {
		r, _ := coord.GetRecording(ctx, info.RecordingId)
		return r.Status == tesserav1.RecordingStatus_RECORDING_STATUS_COMPLETE || r.Status == tesserav1.RecordingStatus_RECORDING_STATUS_FAILED
	})
	got, _ := coord.GetRecording(ctx, info.RecordingId)
	if got.Status != tesserav1.RecordingStatus_RECORDING_STATUS_COMPLETE || got.Error != "" || len(got.Outputs) != 2 {
		t.Fatalf("%v", got)
	}
	master := filepath.Join(root, "live", info.RecordingId, "master.m3u8")
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "stream=codec_name,width,height", "-of", "csv=p=0", master).CombinedOutput()
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if !strings.Contains(string(out), "h264") || !strings.Contains(string(out), "opus") {
		t.Fatalf("%s", out)
	}
	if b, err := exec.Command("ffmpeg", "-v", "error", "-i", master, "-f", "null", "-").CombinedOutput(); err != nil || len(b) != 0 {
		t.Fatalf("decode: %v %s", err, b)
	}
	for _, o := range got.Outputs {
		if o.DurationMs < 3000 || o.SegmentCount < 2 {
			t.Fatalf("output too short: %v", o)
		}
	}
}
