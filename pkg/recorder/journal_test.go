package recorder

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/config"
	"github.com/KeshavGoyal26/tessera/pkg/hls/hlstest"
)

// A server killed mid-recording (kill -9, OOM) leaves live playlists (no ENDLIST) and no
// master. The next start must close them from the crash journal, so the recording plays
// to its last uploaded segment, and report it with a recording_ended webhook.
func TestRecoverAfterCrash(t *testing.T) {
	hlstest.NeedFFmpeg(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	coord := NewCoordinator(CoordinatorParams{Config: config.RecordingConfig{HeartbeatTimeout: time.Minute},
		APIKey: "k", Secret: "secret-secret-secret-secret-1234"})
	defer coord.Close()
	v, a := NewMemSource("video/H264", ""), NewMemSource("audio/opus", "")
	conn := &fakeConnector{joined: make(chan struct{}), tracks: []Track{
		{SID: "TR_v", ParticipantIdentity: "alice", Kind: "video", Source: "camera", RTP: v},
		{SID: "TR_a", ParticipantIdentity: "alice", Kind: "audio", Source: "microphone", RTP: a},
	}}
	root, tmp := t.TempDir(), t.TempDir()
	cfg := DefaultConfig()
	cfg.WorkerID, cfg.Capacity, cfg.Heartbeat, cfg.TempDir = "w1", 10, 50*time.Millisecond, tmp
	cfg.Storage.LocalRoot = root
	w, err := NewWorker(cfg, nil, nil, WithJobAPI(coord), WithConnector(conn))
	if err != nil {
		t.Fatal(err)
	}
	runDone := make(chan struct{})
	go func() { defer close(runDone); _ = w.Run(ctx) }()
	info, err := coord.StartRecording(ctx, &tesserav1.StartRecordingRequest{Room: "demo",
		Target: &tesserav1.StartRecordingRequest_Participant{Participant: &tesserav1.ParticipantRecordingTarget{Identity: "alice"}}})
	if err != nil {
		t.Fatal(err)
	}
	<-conn.joined
	feedVideo(t, v, hlstest.H264(t, 12))
	feedAudio(t, a, hlstest.Opus(t, 12))
	waitFor(t, "segments", func() bool { return w.Stats().SegmentsWritten >= 4 })

	// "Crash": take what storage and temp_dir hold now, mid-recording.
	crashRoot, crashTmp := t.TempDir(), t.TempDir()
	if err := os.CopyFS(crashRoot, os.DirFS(root)); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(crashTmp, os.DirFS(tmp)); err != nil {
		t.Fatal(err)
	}
	cancel()
	<-runDone
	if e, _ := os.ReadDir(filepath.Join(tmp, "journal")); len(e) != 0 {
		t.Fatalf("a recording that ended normally left a journal entry: %v", e)
	}
	pl, _ := os.ReadFile(filepath.Join(crashRoot, "demo", info.RecordingId, "TR_v", "index.m3u8"))
	if strings.Contains(string(pl), "ENDLIST") {
		t.Fatalf("snapshot should hold a live playlist:\n%s", pl)
	}

	// Next start on the crashed state.
	notifier := &fakeNotifier{}
	coord2 := NewCoordinator(CoordinatorParams{Notifier: notifier, APIKey: "k", Secret: "secret-secret-secret-secret-1234"})
	defer coord2.Close()
	cfg2 := *cfg
	cfg2.TempDir, cfg2.Storage.LocalRoot = crashTmp, crashRoot
	w2, err := NewWorker(&cfg2, nil, nil, WithJobAPI(coord2), WithConnector(&fakeConnector{joined: make(chan struct{})}))
	if err != nil {
		t.Fatal(err)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := make(chan struct{})
	go func() { defer close(done2); _ = w2.Run(ctx2) }()
	waitFor(t, "recording_ended for the crashed recording", func() bool { return len(notifier.types()) == 1 })
	cancel2()
	<-done2

	got, err := coord2.GetRecording(context.Background(), info.RecordingId)
	if err != nil || got.Status != tesserav1.RecordingStatus_RECORDING_STATUS_FAILED || got.Error != errCrashed || len(got.Outputs) != 2 {
		t.Fatalf("%v %v", got, err)
	}
	master := filepath.Join(crashRoot, "demo", info.RecordingId, "master.m3u8")
	if got.MasterPlaylistLocation != master {
		t.Fatalf("master %q, want %q", got.MasterPlaylistLocation, master)
	}
	for _, o := range got.Outputs {
		if b, _ := os.ReadFile(o.PlaylistLocation); !strings.Contains(string(b), "#EXT-X-PLAYLIST-TYPE:VOD") || !strings.Contains(string(b), "#EXT-X-ENDLIST") {
			t.Fatalf("not closed:\n%s", b)
		}
	}
	if b, err := exec.Command("ffmpeg", "-v", "error", "-i", master, "-f", "null", "-").CombinedOutput(); err != nil || len(b) != 0 {
		t.Fatalf("decode: %v %s", err, b)
	}
	if e, _ := os.ReadDir(filepath.Join(crashTmp, "journal")); len(e) != 0 {
		t.Fatalf("journal entry not removed after recovery: %v", e)
	}
}

// Another server process on the host (same temp_dir) still recording: its entries are not touched.
func TestRecoverSkipsLiveOwner(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "journal", "RC_other")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "job.json"), []byte(`{"recordingId":"RC_other"}`), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(os.Getppid())), 0o600)
	cfg := DefaultConfig()
	cfg.TempDir, cfg.Storage.LocalRoot = tmp, t.TempDir()
	w, err := NewWorker(cfg, nil, nil, WithConnector(&fakeConnector{joined: make(chan struct{})}))
	if err != nil {
		t.Fatal(err)
	}
	w.recoverJournal(context.Background())
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("entry of a live process was removed: %v", err)
	}
}

// The periodic recovery runs while recordings are in progress: it must not close this
// process's own recordings (it did on GCP: every recording was sealed and reported FAILED
// about 30 s after it started, and lost its crash protection). A copy of the entry, as
// left by a crashed run with the same pid, is still an orphan.
func TestFileJournalSkipsOwnLiveRecordings(t *testing.T) {
	tmp := t.TempDir()
	j := FileJournal{Dir: filepath.Join(tmp, "live")}
	h, err := j.Begin("RC_live", []byte(`{"recordingId":"RC_live"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Put(h, "TR_v", []byte(`{"sid":"TR_v"}`)); err != nil {
		t.Fatal(err)
	}
	if o, _ := j.Orphans(context.Background()); len(o) != 0 {
		t.Fatalf("a recording of this process was taken as an orphan: %v", o[0].ID)
	}
	crashed := FileJournal{Dir: filepath.Join(tmp, "crashed")}
	if err := os.CopyFS(crashed.Dir, os.DirFS(j.Dir)); err != nil {
		t.Fatal(err)
	}
	if o, _ := crashed.Orphans(context.Background()); len(o) != 1 || o[0].ID != "RC_live" || len(o[0].Tracks) != 1 {
		t.Fatalf("an entry of a previous run with the same pid must be an orphan, got %v", o)
	}
	j.End(h)
	if _, err := os.Stat(h); !os.IsNotExist(err) {
		t.Fatalf("End left the entry: %v", err)
	}
}
