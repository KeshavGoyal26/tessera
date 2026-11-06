package recorder

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/config"
	"github.com/KeshavGoyal26/tessera/pkg/hls/hlstest"
)

type fakeConnector struct {
	tracks []Track
	joined chan struct{}
	closed func(error)
	left   bool
}

func (f *fakeConnector) Join(_ context.Context, _, _ string, _ func(Track) bool, onTrack func(Track), onClosed func(error)) (Room, error) {
	f.closed = onClosed
	for _, t := range f.tracks {
		onTrack(t)
	}
	close(f.joined)
	return fakeRoom{f}, nil
}

type fakeRoom struct{ f *fakeConnector }

func (r fakeRoom) Close(context.Context) error { r.f.left = true; return nil }

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func feedVideo(t *testing.T, src *MemSource, aus [][][]byte) {
	p := &hlstest.Packetizer{SSRC: 11, MTU: 1200, Seq: 65500} // sequence wraps
	for i, au := range aus {
		for _, pkt := range p.Packetize(au, uint32(i*3600)+0xfffff000) { // timestamp wraps
			if err := src.Push(pkt); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func feedAudio(t *testing.T, src *MemSource, frames [][]byte) {
	for i, f := range frames {
		if err := src.Push(hlstest.OpusPacket(uint16(i), uint32(i*960), 22, f)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWorkerParticipantRecordingEndToEnd(t *testing.T) {
	hlstest.NeedFFmpeg(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	notifier := &fakeNotifier{}
	coord := NewCoordinator(CoordinatorParams{
		Config: config.RecordingConfig{WSURL: "ws://x", HeartbeatTimeout: time.Minute}, Notifier: notifier,
		APIKey: "k", Secret: "secret-secret-secret-secret-1234",
	})
	defer coord.Close()

	v := NewMemSource("video/H264", "")
	a := NewMemSource("audio/opus", "")
	conn := &fakeConnector{joined: make(chan struct{}), tracks: []Track{
		{SID: "TR_v", ParticipantIdentity: "alice", Kind: "video", Source: "camera", RTP: v},
		{SID: "TR_a", ParticipantIdentity: "alice", Kind: "audio", Source: "microphone", RTP: a},
		{SID: "TR_other", ParticipantIdentity: "bob", Kind: "audio", Source: "microphone", RTP: NewMemSource("audio/opus", "")},
	}}

	root := t.TempDir()
	if d := os.Getenv("TESSERA_TEST_OUTDIR"); d != "" { // keep the output, e.g. for hls.js playback checks
		root = d
	}
	cfg := DefaultConfig()
	cfg.WorkerID, cfg.Capacity, cfg.Heartbeat = "w1", 10, 50*time.Millisecond
	cfg.Storage.LocalRoot = root
	w, err := NewWorker(cfg, nil, nil, WithJobAPI(coord), WithConnector(conn))
	if err != nil {
		t.Fatal(err)
	}
	runDone := make(chan struct{})
	go func() { defer close(runDone); _ = w.Run(ctx) }()

	info, err := coord.StartRecording(ctx, &tesserav1.StartRecordingRequest{
		Room: "demo", Target: &tesserav1.StartRecordingRequest_Participant{Participant: &tesserav1.ParticipantRecordingTarget{Identity: "alice"}},
		Hls: &tesserav1.HLSOptions{SegmentDurationSeconds: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	<-conn.joined
	feedVideo(t, v, hlstest.H264(t, 12))
	feedAudio(t, a, hlstest.Opus(t, 12))
	v.Close()
	a.Close()

	waitFor(t, "ACTIVE", func() bool {
		r, _ := coord.GetRecording(ctx, info.RecordingId)
		return r.Status == tesserav1.RecordingStatus_RECORDING_STATUS_ACTIVE
	})
	if st := w.Stats(); st.ActiveRecordings != 1 || st.CapacityUsed != 2 {
		t.Fatalf("%+v", st)
	}
	waitFor(t, "segments", func() bool { return w.Stats().SegmentsWritten >= 4 })
	if _, err := coord.StopRecording(ctx, info.RecordingId); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "COMPLETE", func() bool {
		r, _ := coord.GetRecording(ctx, info.RecordingId)
		return r.Status == tesserav1.RecordingStatus_RECORDING_STATUS_COMPLETE
	})
	got, _ := coord.GetRecording(ctx, info.RecordingId)
	if got.Error != "" || len(got.Outputs) != 2 || got.MasterPlaylistLocation == "" {
		t.Fatalf("%v", got)
	}
	if !conn.left {
		t.Fatal("room not left")
	}
	for _, o := range got.Outputs {
		if o.SegmentCount < 2 || o.DurationMs < 11000 || o.DurationMs > 13000 {
			t.Fatalf("%v", o)
		}
	}
	// Output plays: master decodes without errors, 12 s of video and audio.
	master := filepath.Join(root, "demo", info.RecordingId, "master.m3u8")
	if got.MasterPlaylistLocation != master {
		t.Fatalf("%s vs %s", got.MasterPlaylistLocation, master)
	}
	if b, err := exec.Command("ffmpeg", "-v", "error", "-i", master, "-f", "null", "-").CombinedOutput(); err != nil || len(b) != 0 {
		t.Fatalf("decode: %v %s", err, b)
	}
	b, _ := exec.Command("ffprobe", "-v", "error", "-show_entries", "stream=codec_name:format=duration", "-of", "json", master).Output()
	if !strings.Contains(string(b), `"h264"`) || !strings.Contains(string(b), `"opus"`) {
		t.Fatalf("%s", b)
	}
	if strings.Contains(string(b), "TR_other") {
		t.Fatal("bob must not be recorded")
	}
	mp, _ := os.ReadFile(master)
	if !strings.Contains(string(mp), "EXT-X-MEDIA:TYPE=AUDIO") || !strings.Contains(string(mp), "RESOLUTION=320x240") {
		t.Fatalf("%s", mp)
	}
	if v.KeyFrames.Load() == 0 {
		t.Fatal("recorder must request a keyframe when starting")
	}

	types := notifier.types()
	if len(types) != 2 || types[0] != tesserav1.WebhookEventType_WEBHOOK_EVENT_TYPE_RECORDING_STARTED || types[1] != tesserav1.WebhookEventType_WEBHOOK_EVENT_TYPE_RECORDING_ENDED {
		t.Fatalf("%v", types)
	}

	// Stats endpoint and Prometheus.
	rec := httptest.NewRecorder()
	w.StatsHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/stats", nil))
	var st Stats
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.FinishedTotal != 1 || len(st.Finished) != 1 || st.Finished[0].Status != "RECORDING_STATUS_COMPLETE" || len(st.Finished[0].Outputs) != 2 || st.SegmentsWritten < 4 {
		t.Fatalf("%+v", st)
	}
	cancel()
	<-runDone
}

func TestWorkerTrackRecordingEndsWithTrackAndReportsLoss(t *testing.T) {
	hlstest.NeedFFmpeg(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	coord := NewCoordinator(CoordinatorParams{Config: config.RecordingConfig{HeartbeatTimeout: time.Minute}, APIKey: "k", Secret: "secret-secret-secret-secret-1234"})
	defer coord.Close()
	v := NewMemSource("video/H264", "")
	conn := &fakeConnector{joined: make(chan struct{}), tracks: []Track{{SID: "TR_1", ParticipantIdentity: "alice", Kind: "video", Source: "camera", RTP: v}}}
	cfg := DefaultConfig()
	cfg.WorkerID, cfg.Capacity, cfg.Heartbeat = "w1", 4, 50*time.Millisecond
	cfg.Storage.LocalRoot = t.TempDir()
	w, _ := NewWorker(cfg, nil, nil, WithJobAPI(coord), WithConnector(conn))
	go func() { _ = w.Run(ctx) }()

	info, _ := coord.StartRecording(ctx, trackReq("demo"))
	<-conn.joined
	aus := hlstest.H264(t, 12)
	// Drop one packet of frame 60 (second GOP): the recorder must resync at the next keyframe.
	p := &hlstest.Packetizer{SSRC: 1}
	for i, au := range aus {
		for j, pkt := range p.Packetize(au, uint32(i*3600)) {
			if i == 60 && j == 0 {
				continue
			}
			_ = v.Push(pkt)
		}
	}
	v.Close()
	waitFor(t, "COMPLETE", func() bool {
		r, _ := coord.GetRecording(ctx, info.RecordingId)
		return r.Status == tesserav1.RecordingStatus_RECORDING_STATUS_COMPLETE
	})
	got, _ := coord.GetRecording(ctx, info.RecordingId)
	if len(got.Outputs) != 1 || got.MasterPlaylistLocation != "" {
		t.Fatalf("%v", got)
	}
	// Frames 60..99 (rest of the GOP) are dropped but the timeline stays continuous.
	if d := got.Outputs[0].DurationMs; d < 11500 || d > 12500 {
		t.Fatalf("duration %d", d)
	}
	b, _ := exec.Command("ffprobe", "-v", "error", "-count_packets", "-show_entries", "stream=nb_read_packets", "-of", "csv=p=0", got.Outputs[0].PlaylistLocation).Output()
	if n := strings.Fields(string(b)); len(n) == 0 || n[0] != "260" {
		t.Fatalf("packets %q, want 260", b)
	}
	if v.KeyFrames.Load() < 1 {
		t.Fatalf("keyframe requests: %d", v.KeyFrames.Load())
	}
}
