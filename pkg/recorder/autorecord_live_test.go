package recorder

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
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

// lateTrigger lets the room manager reach a Coordinator created after it.
type lateTrigger struct{ c atomic.Pointer[Coordinator] }

func (l *lateTrigger) ParticipantJoined(r, id string) {
	if c := l.c.Load(); c != nil {
		c.ParticipantJoined(r, id)
	}
}

func (l *lateTrigger) ParticipantLeft(r, id string) {
	if c := l.c.Load(); c != nil {
		c.ParticipantLeft(r, id)
	}
}

// TestLiveAutoRecordOnJoin: a participant whose token has auto_record is recorded from
// join to leave without any API call, and the result plays as one master playlist.
func TestLiveAutoRecordOnJoin(t *testing.T) {
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
	trig := &lateTrigger{}
	mgr, err := room.NewManager(room.Params{Config: cfg, Engine: eng, Notifier: webhook.Nop{}, Keys: auth.StaticKeys(cfg.Keys),
		Logger: logger, Recording: trig})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(signal.NewServer(cfg.Signal, auth.NewVerifier(auth.StaticKeys(cfg.Keys)), mgr, logger))
	t.Cleanup(func() { mgr.Close(); srv.Close(); _ = eng.Close() })
	wsURL := "ws://" + strings.TrimPrefix(srv.URL, "http://")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	coord := NewCoordinator(CoordinatorParams{Config: config.RecordingConfig{HeartbeatTimeout: time.Minute},
		Rooms: mgr, APIKey: key, Secret: secret, Logger: logger})
	defer coord.Close()
	trig.c.Store(coord)
	root := t.TempDir()
	wcfg := DefaultConfig()
	wcfg.WorkerID, wcfg.Capacity, wcfg.Heartbeat = "auto-worker", 10, 100*time.Millisecond
	wcfg.Storage.LocalRoot = root
	wcfg.Storage.LocalBaseURL = "http://rec.test"
	conn := LocalConnector{Rooms: mgr, Verifier: auth.NewVerifier(auth.StaticKeys(cfg.Keys))}
	w, err := NewWorker(wcfg, nil, logger, WithJobAPI(coord), WithConnector(conn))
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = w.Run(ctx) }()

	// A participant without auto_record is not recorded.
	plain, _ := auth.NewAccessToken(key, secret).SetIdentity("bob").SetGrant(&auth.RoomGrant{Room: "auto", RoomJoin: true}).ToJWT()
	bob := client.New(client.Options{SettingEngine: client.LoopbackSettingEngine(), Logger: logger})
	cctx, ccancel := context.WithTimeout(ctx, 15*time.Second)
	defer ccancel()
	if err := bob.Connect(cctx, wsURL, plain); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bob.Disconnect(context.Background()) }()

	tok, _ := auth.NewAccessToken(key, secret).SetIdentity("alice").
		SetGrant(&auth.RoomGrant{Room: "auto", RoomJoin: true, AutoRecord: true}).ToJWT()
	pub := client.New(client.Options{SettingEngine: client.LoopbackSettingEngine(), Logger: logger})
	if err := pub.Connect(cctx, wsURL, tok); err != nil {
		t.Fatal(err)
	}
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
	sendDone := make(chan struct{})
	go func() {
		defer close(sendDone)
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

	recs := func() []*tesserav1.RecordingInfo {
		l, _ := coord.ListRecordings(ctx, &tesserav1.ListRecordingsRequest{Room: "auto"})
		return l
	}
	waitFor(t, "an auto-started recording", func() bool { return len(recs()) > 0 })
	waitFor(t, "segments from the live stream", func() bool { return w.Stats().SegmentsWritten >= 2 })

	// While alice is live, her recording has a master playlist (camera + mic) that
	// viewers find in her attributes.
	live := recs()[0]
	liveMaster := filepath.Join(root, "auto", live.RecordingId, "master.m3u8")
	waitFor(t, "a live master playlist with video and audio", func() bool {
		b, err := os.ReadFile(liveMaster)
		return err == nil && strings.Contains(string(b), "TYPE=AUDIO") && strings.Contains(string(b), "#EXT-X-STREAM-INF")
	})
	wantURL := "http://rec.test/auto/" + live.RecordingId + "/master.m3u8"
	waitFor(t, "the playlist URL in alice's attributes", func() bool {
		p, err := mgr.GetParticipant(ctx, "auto", "alice")
		return err == nil && p.GetAttributes()[PlaylistAttribute] == wantURL
	})
	if r, _ := coord.GetRecording(ctx, live.RecordingId); r.GetMasterPlaylistUrl() != wantURL ||
		r.Status != tesserav1.RecordingStatus_RECORDING_STATUS_ACTIVE {
		t.Fatalf("live recording: %v", r)
	}
	<-sendDone

	// Leaving ends the recording; nobody calls StopRecording.
	if err := pub.Disconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	l := recs()
	if len(l) != 1 || l[0].GetRequest().GetParticipant().GetIdentity() != "alice" {
		t.Fatalf("want exactly one recording, of alice: %v", l)
	}
	id := l[0].RecordingId
	waitFor(t, "COMPLETE after alice left", func() bool {
		r, _ := coord.GetRecording(ctx, id)
		return r.Status == tesserav1.RecordingStatus_RECORDING_STATUS_COMPLETE || r.Status == tesserav1.RecordingStatus_RECORDING_STATUS_FAILED
	})
	got, _ := coord.GetRecording(ctx, id)
	if got.Status != tesserav1.RecordingStatus_RECORDING_STATUS_COMPLETE || got.Error != "" || len(got.Outputs) != 2 {
		t.Fatalf("%v", got)
	}
	master := filepath.Join(root, "auto", id, "master.m3u8")
	if b, err := exec.Command("ffmpeg", "-v", "error", "-i", master, "-f", "null", "-").CombinedOutput(); err != nil || len(b) != 0 {
		t.Fatalf("decode: %v %s", err, b)
	}
}
