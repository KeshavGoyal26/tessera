package recorder

import (
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pion/rtp"

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
	if tr.codecString != "mp4a.40.2" {
		t.Fatal(tr.codecString)
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

func genVP8(t *testing.T, seconds int) [][]byte {
	out := filepath.Join(t.TempDir(), "v.ivf")
	cmd := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc=size=320x240:rate=25", "-t", itoa(int64(seconds)),
		"-c:v", "libvpx", "-g", "50", "-deadline", "realtime", "-f", "ivf", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, b)
	}
	data, _ := os.ReadFile(out)
	data = data[32:]
	var frames [][]byte
	for len(data) >= 12 {
		n := int(binary.LittleEndian.Uint32(data))
		frames = append(frames, data[12:12+n])
		data = data[12+n:]
	}
	return frames
}

func TestVP8ToH264Fallback(t *testing.T) {
	hlstest.NeedFFmpeg(t)
	frames := genVP8(t, 8)
	src := NewMemSource("video/VP8", "")
	var seq uint16
	feed := func() {
		for i, f := range frames {
			for off := 0; off < len(f); {
				end := min(off+1000, len(f))
				desc := byte(0x00)
				if off == 0 {
					desc = 0x10
				}
				_ = src.Push(&rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: seq, Timestamp: uint32(i * 3600), SSRC: 5, Marker: end == len(f)},
					Payload: append([]byte{desc}, f[off:end]...)})
				seq++
				off = end
			}
		}
	}
	dir, tr, _ := runTrack(t, src, trackOptions{}, feed)
	if !strings.HasPrefix(tr.codecString, "avc1.") || tr.stats.width.Load() != 320 {
		t.Fatalf("%q %d", tr.codecString, tr.stats.width.Load())
	}
	out, err := exec.Command("ffprobe", "-v", "error", "-count_packets", "-show_entries", "stream=codec_name,width,height,nb_read_packets", "-of", "default=nw=1", filepath.Join(dir, "index.m3u8")).CombinedOutput()
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
	s := string(out)
	if !strings.Contains(s, "codec_name=h264") || !strings.Contains(s, "width=320") || !strings.Contains(s, "nb_read_packets=200") {
		t.Fatal(s)
	}
	if b, err := exec.Command("ffmpeg", "-v", "error", "-i", filepath.Join(dir, "index.m3u8"), "-f", "null", "-").CombinedOutput(); err != nil || len(b) != 0 {
		t.Fatalf("decode: %v %s", err, b)
	}
}

func TestH264PathNeverSpawnsFFmpeg(t *testing.T) {
	hlstest.NeedFFmpeg(t)
	aus := hlstest.H264(t, 6)
	src := NewMemSource("video/H264", "")
	_, tr, _ := runTrack(t, src, trackOptions{}, func() { feedVideo(t, src, aus) })
	if tr.vp8TC != nil || tr.aacTC != nil {
		t.Fatal("transcoder started on the remux path")
	}
}
