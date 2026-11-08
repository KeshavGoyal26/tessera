package recorder

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KeshavGoyal26/tessera/pkg/hls"
	"github.com/KeshavGoyal26/tessera/pkg/hls/hlstest"
	"github.com/KeshavGoyal26/tessera/pkg/storage"
)

// runTrack records src until it ends and returns the track directory.
func runTrack(t *testing.T, src *MemSource, opts trackOptions, feed func()) (dir string, tr *trackRecorder, sink *storageSink) {
	t.Helper()
	root := t.TempDir()
	st, _ := storage.NewLocal(root)
	ctx := context.Background()
	sink = newStorageSink(ctx, st, "trk")
	opts.segment, opts.ffmpeg = 4*time.Second, "ffmpeg"
	tr, err := newTrackRecorder(Track{SID: "TR_x", RTP: src}, sink, opts, nopLog{})
	if err != nil {
		t.Fatal(err)
	}
	go func() { feed(); src.Close() }()
	if err := tr.run(ctx); err != nil {
		t.Fatal(err)
	}
	if err := sink.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "trk"), tr, sink
}

type nopLog struct{}

func (nopLog) Warn(string, ...any) {}

func TestOpusToAACFallback(t *testing.T) {
	hlstest.NeedFFmpeg(t)
	src := NewMemSource("audio/opus", "")
	frames := hlstest.Opus(t, 10)
	dir, tr, _ := runTrack(t, src, trackOptions{aac: true}, func() { feedAudio(t, src, frames) })
	if tr.codecString() != "mp4a.40.2" {
		t.Fatal(tr.codecString())
	}
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "stream=codec_name,sample_rate,channels:format=duration", "-of", "default=nw=1", filepath.Join(dir, "index.m3u8")).CombinedOutput()
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
	s := string(out)
	if !strings.Contains(s, "codec_name=aac") || !strings.Contains(s, "sample_rate=48000") || !strings.Contains(s, "channels=2") {
		t.Fatal(s)
	}
	if b, err := exec.Command("ffmpeg", "-v", "error", "-i", filepath.Join(dir, "index.m3u8"), "-f", "null", "-").CombinedOutput(); err != nil || len(b) != 0 {
		t.Fatalf("decode: %v %s", err, b)
	}
	pl, _ := os.ReadFile(filepath.Join(dir, "index.m3u8"))
	if !strings.Contains(string(pl), "ENDLIST") {
		t.Fatal(string(pl))
	}
}

// Video is never transcoded: a VP8 track is refused with an error that says what to publish.
func TestVP8IsRefused(t *testing.T) {
	sink := newStorageSink(context.Background(), nil, "trk")
	_, err := newTrackRecorder(Track{SID: "TR_x", RTP: NewMemSource("video/VP8", "")}, sink, trackOptions{}, nopLog{})
	if err == nil || !strings.Contains(err.Error(), "H.264") {
		t.Fatalf("err = %v, want a refusal that asks for H.264", err)
	}
}

func TestH264PathNeverSpawnsFFmpeg(t *testing.T) {
	hlstest.NeedFFmpeg(t)
	aus := hlstest.H264(t, 6)
	src := NewMemSource("video/H264", "")
	_, tr, _ := runTrack(t, src, trackOptions{}, func() { feedVideo(t, src, aus) })
	if tr.aacTC != nil {
		t.Fatal("transcoder started on the remux path")
	}
}

// TestKeyframePerSegment: browsers send keyframes only when asked, so the recorder asks
// once the current segment reaches its target (and repeats, rate-limited, until one comes).
func TestKeyframePerSegment(t *testing.T) {
	src := NewMemSource("video/H264", "")
	tr, err := newTrackRecorder(Track{SID: "TR_v", RTP: src}, nil, trackOptions{segment: 4 * time.Second}, nopLog{})
	if err != nil {
		t.Fatal(err)
	}
	tr.mux = &hls.Muxer{} // recording started
	t0 := time.Now()
	tr.segStart, tr.lastKeyReq = t0, t0
	for _, c := range []struct {
		at   time.Duration
		want int64
	}{{3 * time.Second, 0}, {4 * time.Second, 1}, {4500 * time.Millisecond, 1}, {5100 * time.Millisecond, 2}} {
		tr.maybeKeyframe(t0.Add(c.at))
		if got := src.KeyFrames.Load(); got != c.want {
			t.Fatalf("at %v: %d keyframe requests, want %d", c.at, got, c.want)
		}
	}
}
