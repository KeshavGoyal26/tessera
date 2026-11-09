package hls

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestOpusFrameSamples(t *testing.T) {
	cases := map[byte]uint32{0xfc: 960, 0xf8: 960, 0xe8: 240, 0x08: 960, 0x48: 960, 0x70: 480}
	for toc, want := range cases {
		if got := OpusFrameSamples([]byte{toc}); got != want {
			t.Errorf("toc %#x: %d want %d", toc, got, want)
		}
	}
	if OpusFrameSamples([]byte{0xfc, 0x01}) != 960 || OpusFrameSamples([]byte{0xfd, 0, 0}) != 1920 || OpusFrameSamples(nil) != 0 {
		t.Fatal("bad durations")
	}
}

func TestWallClock(t *testing.T) {
	c := WallClock{Rate: 48000}
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c.ObserveArrival(1000, t0)
	if !c.At(1000 + 48000).Equal(t0.Add(time.Second)) {
		t.Fatal("arrival fallback")
	}
	ntp := uint64(t0.Unix()+2208988800)<<32 | 1<<31 // +0.5 s
	c.ObserveSR(ntp, 5000)
	if d := c.At(5000 + 24000).Sub(t0); d != time.Second {
		t.Fatalf("%v", d)
	}
}

func TestOpusAndVideoAlignedHLS(t *testing.T) {
	aus := genH264Cached(t)
	frames := genOpus(t, 12)
	dir := t.TempDir()
	origin := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	// Video starts at origin, audio 500 ms later.
	muxH264(t, aus, filepath.Join(dir, "video"), MuxerConfig{StartTime: origin, Origin: origin})

	am, err := NewMuxer(MuxerConfig{StartTime: origin.Add(500 * time.Millisecond), Origin: origin, TargetSegmentDuration: 4 * time.Second},
		TrackSpec{ID: 1, Codec: CodecOpus, Timescale: 48000, Channels: 2}, &DirSink{Dir: filepath.Join(dir, "audio")})
	if err != nil {
		t.Fatal(err)
	}
	var u Unwrapper
	for i, f := range frames {
		ts := uint32(i*960) + 0xffff0000 // wraps
		if err := am.WriteSample(context.Background(), Sample{DTS: u.Unwrap(ts), Data: f}); err != nil {
			t.Fatal(err)
		}
	}
	if err := am.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	ap, _ := os.ReadFile(filepath.Join(dir, "audio", "index.m3u8"))
	// The audio started 500 ms after the origin: its first frame is held from the origin
	// (players need every rendition from 0), the next ones keep their times.
	if !strings.Contains(string(ap), "2026-01-02T03:04:05.000Z") {
		t.Fatalf("%s", ap)
	}
	pf := ffprobe(t, "-show_streams", "-show_packets", "-select_streams", "a:0", filepath.Join(dir, "audio", "index.m3u8"))
	if pf.Streams[0].CodecName != "opus" {
		t.Fatalf("%+v", pf.Streams)
	}
	if len(pf.Packets) != len(frames) {
		t.Fatalf("%d packets want %d", len(pf.Packets), len(frames))
	}
	var first, second float64
	if _, err := fmtSscan(pf.Packets[0].PTSTime, &first); err != nil {
		t.Fatal(err)
	}
	if _, err := fmtSscan(pf.Packets[1].PTSTime, &second); err != nil {
		t.Fatal(err)
	}
	if first > 0.01 || second < 0.51 || second > 0.53 {
		t.Fatalf("audio packets at %v, %v; want 0 (held from the origin) then 0.52 (A/V offset kept)", first, second)
	}
	// Audio-only decode has no errors.
	if b, err := execFFmpegDecode(filepath.Join(dir, "audio", "index.m3u8")); err != nil || len(b) != 0 {
		t.Fatalf("%v %s", err, b)
	}

	master, _ := MasterPlaylist([]Rendition{
		{URI: "video/index.m3u8", Codecs: "avc1.42c01e", Bandwidth: 500000, Width: 320, Height: 240},
		{URI: "audio/index.m3u8", Codecs: "opus", Bandwidth: 32000, IsAudio: true},
	})
	if err := os.WriteFile(filepath.Join(dir, "master.m3u8"), master, 0o644); err != nil {
		t.Fatal(err)
	}
	if b, err := execFFmpegDecode(filepath.Join(dir, "master.m3u8")); err != nil || len(b) != 0 {
		t.Fatalf("master decode: %v %s", err, b)
	}
}

// TestMuxerGapAndBackwardsClock: a stretch without media is listed as EXT-X-GAP entries
// and a backwards clock continues the timeline, so the playlist's timeline is the media
// timeline and no rendition needs a discontinuity.
func TestMuxerGapAndBackwardsClock(t *testing.T) {
	var sink memSink
	m, _ := NewMuxer(MuxerConfig{TargetSegmentDuration: time.Second}, TrackSpec{Codec: CodecOpus, Timescale: 48000, Channels: 2}, &sink)
	ctx := context.Background()
	dts := int64(0)
	step := func(n int) {
		for i := 0; i < n; i++ {
			if err := m.WriteSample(ctx, Sample{DTS: dts, Data: []byte{0xfc, 1}}); err != nil {
				t.Fatal(err)
			}
			dts += 960
		}
	}
	step(150)         // 3 s
	dts += 48000 * 20 // 20 s gap
	step(100)
	dts = 1000 // clock jumps backwards (e.g. SSRC change)
	step(100)
	if err := m.Close(ctx); err != nil {
		t.Fatal(err)
	}
	pl := sink.playlist
	if strings.Contains(pl, "#EXT-X-DISCONTINUITY") || strings.Count(pl, "#EXT-X-GAP\n") != 20 {
		t.Fatalf("want 20 gap entries (20 s at the 1 s target) and no discontinuity:\n%s", pl)
	}
	var total, gap float64
	lines := strings.Split(pl, "\n")
	for i, l := range lines {
		if d, ok := strings.CutPrefix(l, "#EXTINF:"); ok {
			v, _ := strconv.ParseFloat(strings.TrimSuffix(d, ","), 64)
			total += v
			if lines[i+1] == "#EXT-X-GAP" {
				gap += v
			}
		}
	}
	if math.Abs(gap-20) > 0.05 || math.Abs(total-27) > 0.1 { // 3 s, the 20 s gap, 2 s, 2 s after the backwards jump
		t.Fatalf("gap %.2f s, total %.2f s:\n%s", gap, total, pl)
	}
}

type memSink struct{ playlist string }

func (m *memSink) WriteInit(context.Context, string, []byte) error { return nil }
func (m *memSink) WriteSegment(context.Context, Segment) error     { return nil }
func (m *memSink) WritePlaylist(_ context.Context, _ string, d []byte) error {
	m.playlist = string(d)
	return nil
}

// TestMuxerSpecChangeKeepsTimeline: new codec parameters get a new init segment but no
// EXT-X-DISCONTINUITY (the timeline continues); hls.js stalls on a live playlist opened
// mid-recording when only one rendition has discontinuities.
func TestMuxerSpecChangeKeepsTimeline(t *testing.T) {
	var sink memSink
	ctx := context.Background()
	m, _ := NewMuxer(MuxerConfig{TargetSegmentDuration: time.Second, Live: true}, TrackSpec{Codec: CodecOpus, Timescale: 48000, Channels: 2}, &sink)
	dts := int64(0)
	step := func(n int) {
		for i := 0; i < n; i++ {
			if err := m.WriteSample(ctx, Sample{DTS: dts, Data: []byte{0xfc, 1}}); err != nil {
				t.Fatal(err)
			}
			dts += 960
		}
	}
	step(100)
	dts += 48000 * 3 / 2 // a 1.5 s pause, then new parameters (as a browser resuming at another size)
	if err := m.UpdateSpec(ctx, TrackSpec{Codec: CodecOpus, Timescale: 48000, Channels: 1}); err != nil {
		t.Fatal(err)
	}
	step(100)
	if err := m.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sink.playlist, "#EXT-X-DISCONTINUITY") || strings.Contains(sink.playlist, "#EXT-X-GAP") ||
		strings.Count(sink.playlist, "#EXT-X-MAP:") != 2 || !strings.Contains(sink.playlist, `#EXT-X-MAP:URI="init_1.mp4"`) {
		t.Fatalf("%s", sink.playlist)
	}
	// The pause is held by the last sample before it: the playlist covers 2 + 1.5 + 2 s.
	var total float64
	for _, l := range strings.Split(sink.playlist, "\n") {
		if d, ok := strings.CutPrefix(l, "#EXTINF:"); ok {
			v, _ := strconv.ParseFloat(strings.TrimSuffix(d, ","), 64)
			total += v
		}
	}
	if math.Abs(total-5.5) > 0.05 {
		t.Fatalf("playlist covers %.2f s, want 5.5:\n%s", total, sink.playlist)
	}
}
