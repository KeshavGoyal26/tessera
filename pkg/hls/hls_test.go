package hls

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pion/rtp"
)

func TestParseSPS(t *testing.T) {
	// 320x240 baseline from x264.
	aus := genH264Cached(t)
	var sps []byte
	for _, n := range aus[0] {
		if n[0]&0x1f == nalSPS {
			sps = n
		}
	}
	info, err := ParseSPS(sps)
	if err != nil {
		t.Fatal(err)
	}
	if info.Width != 320 || info.Height != 240 || info.Profile != 66 {
		t.Fatalf("%+v", info)
	}
	if !strings.HasPrefix(info.CodecString(), "avc1.42") {
		t.Fatal(info.CodecString())
	}
	// Cropped 1080p high profile (hand-made vector from x264: 1920x1080).
	hi := []byte{0x67, 0x64, 0x00, 0x28, 0xac, 0xd9, 0x40, 0x78, 0x02, 0x27, 0xe5, 0xc0, 0x44, 0x00, 0x00, 0x03, 0x00, 0x04, 0x00, 0x00, 0x03, 0x00, 0xf0, 0x3c, 0x60, 0xc6, 0x58}
	info, err = ParseSPS(hi)
	if err != nil || info.Width != 1920 || info.Height != 1080 {
		t.Fatalf("%+v %v", info, err)
	}
}

var cachedAUs [][][]byte

func genH264Cached(t *testing.T) [][][]byte {
	needFFmpeg(t)
	if cachedAUs == nil {
		cachedAUs = genH264(t, 12)
	}
	return cachedAUs
}

func TestDepacketizerRoundTrip(t *testing.T) {
	aus := genH264Cached(t)
	p := &packetizer{mtu: 400, ssrc: 1}
	d := &H264Depacketizer{}
	n := 0
	for i, au := range aus {
		for _, pkt := range p.packetize(au, uint32(i*3600)) {
			out, err := d.Push(pkt)
			if err != nil {
				t.Fatal(err)
			}
			for _, a := range out {
				if a.KeyFrame != (n%50 == 0) {
					t.Fatalf("AU %d keyframe=%v", n, a.KeyFrame)
				}
				n++
			}
		}
	}
	if n != len(aus) {
		t.Fatalf("got %d AUs want %d", n, len(aus))
	}
}

func TestDepacketizerLossWaitsForKeyframe(t *testing.T) {
	aus := genH264Cached(t)
	p := &packetizer{mtu: 400, ssrc: 1}
	d := &H264Depacketizer{}
	var got []bool
	lossSeen := false
	for i, au := range aus[:120] {
		for j, pkt := range p.packetize(au, uint32(i*3600)) {
			if i == 10 && j == 0 {
				continue // drop one fragment of frame 10
			}
			out, err := d.Push(pkt)
			if errors.Is(err, ErrPacketLoss) {
				lossSeen = true
			}
			for _, a := range out {
				got = append(got, a.KeyFrame)
			}
		}
	}
	if !lossSeen {
		t.Fatal("expected loss error")
	}
	// Frames 10..49 dropped; resumes at keyframe 50.
	if len(got) != 10+70 {
		t.Fatalf("got %d AUs", len(got))
	}
	if !got[10] {
		t.Fatal("must resume on a keyframe")
	}
}

func TestDepacketizerSingleAndSTAP(t *testing.T) {
	sps := []byte{0x67, 0x42, 0xc0, 0x1f, 0xda, 0x05, 0x07, 0xe8}
	pps := []byte{0x68, 0xce, 0x3c, 0x80}
	idr := []byte{0x65, 1, 2, 3}
	stap := []byte{24, 0, byte(len(sps))}
	stap = append(stap, sps...)
	stap = append(stap, 0, byte(len(pps)))
	stap = append(stap, pps...)
	stap = append(stap, 0, byte(len(idr)))
	stap = append(stap, idr...)
	d := &H264Depacketizer{}
	out, err := d.Push(&rtp.Packet{Header: rtp.Header{SequenceNumber: 1, Timestamp: 90, Marker: true}, Payload: stap})
	if err != nil || len(out) != 1 || !out[0].KeyFrame || len(out[0].NALUs) != 1 || out[0].SPS == nil || out[0].PPS == nil {
		t.Fatalf("%v %+v", err, out)
	}
	// A keyframe keeps its parameter sets in-band: SPS, PPS, then the slice.
	want := []byte{0, 0, 0, 8}
	want = append(append(want, sps...), 0, 0, 0, 4)
	want = append(append(want, pps...), 0, 0, 0, 4)
	want = append(want, idr...)
	if got := out[0].AVCC(); !bytes.Equal(got, want) {
		t.Fatalf("avcc %x, want %x", got, want)
	}
	// Other frames are the slices only.
	if got := (&AccessUnit{NALUs: [][]byte{{0x41, 9}}, SPS: sps, PPS: pps}).AVCC(); !bytes.Equal(got, []byte{0, 0, 0, 2, 0x41, 9}) {
		t.Fatalf("non-key avcc %x", got)
	}
}

func TestUnwrapper(t *testing.T) {
	var u Unwrapper
	vals := []uint32{0xfffffff0, 0xfffffff8, 4, 2, 0x20}
	want := []int64{0xfffffff0, 0xfffffff8, 1<<32 + 4, 1<<32 + 2, 1<<32 + 0x20}
	for i, v := range vals {
		if g := u.Unwrap(v); g != want[i] {
			t.Fatalf("%d: %x want %x", i, g, want[i])
		}
	}
}

func TestReorderBuffer(t *testing.T) {
	r := NewReorderBuffer()
	now := time.Now()
	mk := func(s uint16) *rtp.Packet { return &rtp.Packet{Header: rtp.Header{SequenceNumber: s}} }
	var order []uint16
	push := func(s uint16, dt time.Duration) {
		for _, p := range r.Push(mk(s), now.Add(dt)) {
			order = append(order, p.SequenceNumber)
		}
	}
	push(65534, 0)
	push(0, 0)
	push(65535, 0)
	push(1, 0)
	push(3, time.Millisecond) // 2 missing
	for _, p := range r.Poll(now.Add(300 * time.Millisecond)) {
		order = append(order, p.SequenceNumber)
	}
	want := []uint16{65534, 65535, 0, 1, 3}
	if len(order) != len(want) {
		t.Fatalf("%v", order)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("%v", order)
		}
	}
	if r.Lost != 1 {
		t.Fatalf("lost=%d", r.Lost)
	}
}

type probeFormat struct {
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
	Streams []struct {
		CodecName string `json:"codec_name"`
		Width     int    `json:"width"`
		Height    int    `json:"height"`
	} `json:"streams"`
	Packets []struct {
		PTSTime string `json:"pts_time"`
		Flags   string `json:"flags"`
	} `json:"packets"`
}

func ffprobe(t *testing.T, args ...string) probeFormat {
	t.Helper()
	b, err := exec.Command("ffprobe", append([]string{"-v", "error", "-of", "json"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("ffprobe %v: %v\n%s", args, err, b)
	}
	var pf probeFormat
	if err := json.Unmarshal(b, &pf); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	return pf
}

func muxH264(t *testing.T, aus [][][]byte, dir string, cfg MuxerConfig) *Muxer {
	t.Helper()
	var sps, pps []byte
	for _, n := range aus[0] {
		switch n[0] & 0x1f {
		case nalSPS:
			sps = n
		case nalPPS:
			pps = n
		}
	}
	info, _ := ParseSPS(sps)
	m, err := NewMuxer(cfg, TrackSpec{ID: 1, Codec: CodecH264, Timescale: 90000, Width: info.Width, Height: info.Height, SPS: sps, PPS: pps}, &DirSink{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	p := &packetizer{mtu: 1200, ssrc: 1}
	d := &H264Depacketizer{}
	var u Unwrapper
	ctx := context.Background()
	for i, au := range aus {
		for _, pkt := range p.packetize(au, uint32(i*3600)+0xffffff00) { // timestamps wrap during the clip
			out, err := d.Push(pkt)
			if err != nil {
				t.Fatal(err)
			}
			for _, a := range out {
				if err := m.WriteSample(ctx, Sample{DTS: u.Unwrap(a.RTPTimestamp), KeyFrame: a.KeyFrame, Data: a.AVCC()}); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if err := m.Close(ctx); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestH264HLSWithFFprobe(t *testing.T) {
	aus := genH264Cached(t) // 12 s, 25 fps, GOP 2 s
	dir := t.TempDir()
	muxH264(t, aus, dir, MuxerConfig{TargetSegmentDuration: 4 * time.Second, StartTime: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)})

	pl, _ := os.ReadFile(filepath.Join(dir, "index.m3u8"))
	s := string(pl)
	for _, want := range []string{"#EXT-X-PLAYLIST-TYPE:VOD", "#EXT-X-ENDLIST", "#EXT-X-MAP:URI=\"init.mp4\"", "#EXT-X-TARGETDURATION:4", "#EXT-X-PROGRAM-DATE-TIME:2026-01-02T03:04:05.000Z", "seg_00002.m4s"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in\n%s", want, s)
		}
	}
	if strings.Contains(s, "seg_00003") {
		t.Fatalf("expected 3 segments:\n%s", s)
	}
	// Whole playlist decodes without errors and totals 12 s.
	pf := ffprobe(t, "-show_format", "-show_streams", filepath.Join(dir, "index.m3u8"))
	if pf.Streams[0].CodecName != "h264" || pf.Streams[0].Width != 320 || pf.Streams[0].Height != 240 {
		t.Fatalf("%+v", pf.Streams)
	}
	var dur float64
	if _, err := fmtSscan(pf.Format.Duration, &dur); err != nil || math.Abs(dur-12) > 0.1 {
		t.Fatalf("duration %q", pf.Format.Duration)
	}
	// Each segment starts with a keyframe.
	for _, n := range []string{"seg_00000.m4s", "seg_00001.m4s", "seg_00002.m4s"} {
		// Concatenate init + segment to probe a single segment.
		initB, _ := os.ReadFile(filepath.Join(dir, "init.mp4"))
		segB, _ := os.ReadFile(filepath.Join(dir, n))
		one := filepath.Join(t.TempDir(), "one.mp4")
		if err := os.WriteFile(one, append(initB, segB...), 0o644); err != nil {
			t.Fatal(err)
		}
		pk := ffprobe(t, "-show_packets", "-select_streams", "v:0", one)
		if len(pk.Packets) != 100 || !strings.HasPrefix(pk.Packets[0].Flags, "K") {
			t.Fatalf("%s: %d packets, first flags %q", n, len(pk.Packets), pk.Packets[0].Flags)
		}
	}
	// Decoding finds no errors.
	if b, err := exec.Command("ffmpeg", "-v", "error", "-i", filepath.Join(dir, "index.m3u8"), "-f", "null", "-").CombinedOutput(); err != nil || len(b) != 0 {
		t.Fatalf("decode: %v %s", err, b)
	}
}

func TestLivePlaylistIsEventAndGrows(t *testing.T) {
	aus := genH264Cached(t)
	dir := t.TempDir()
	sink := &DirSink{Dir: dir}
	_ = sink
	m := muxH264Partial(t, aus[:130], dir)
	_ = m
	pl, _ := os.ReadFile(filepath.Join(dir, "index.m3u8"))
	s := string(pl)
	if !strings.Contains(s, "#EXT-X-PLAYLIST-TYPE:EVENT") || strings.Contains(s, "ENDLIST") || !strings.Contains(s, "seg_00000.m4s") {
		t.Fatalf("%s", s)
	}
}

func muxH264Partial(t *testing.T, aus [][][]byte, dir string) *Muxer {
	t.Helper()
	var sps, pps []byte
	for _, n := range aus[0] {
		switch n[0] & 0x1f {
		case nalSPS:
			sps = n
		case nalPPS:
			pps = n
		}
	}
	m, err := NewMuxer(MuxerConfig{Live: true, TargetSegmentDuration: 4 * time.Second}, TrackSpec{Codec: CodecH264, Timescale: 90000, SPS: sps, PPS: pps}, &DirSink{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	d := &H264Depacketizer{}
	p := &packetizer{mtu: 1200}
	for i, au := range aus {
		for _, pkt := range p.packetize(au, uint32(i*3600)) {
			out, _ := d.Push(pkt)
			for _, a := range out {
				if err := m.WriteSample(context.Background(), Sample{DTS: int64(a.RTPTimestamp), KeyFrame: a.KeyFrame, Data: a.AVCC()}); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	return m
}

func TestMasterPlaylist(t *testing.T) {
	b, err := MasterPlaylist([]Rendition{
		{URI: "v/index.m3u8", Codecs: "avc1.42c01f", Bandwidth: 1000000, Width: 640, Height: 480},
		{URI: "a/index.m3u8", Codecs: "opus", Bandwidth: 32000, IsAudio: true, Name: "mic"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `#EXTM3U
#EXT-X-VERSION:7
#EXT-X-INDEPENDENT-SEGMENTS
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio",NAME="mic",DEFAULT=YES,AUTOSELECT=YES,URI="a/index.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=1032000,CODECS="avc1.42c01f,opus",RESOLUTION=640x480,AUDIO="audio"
v/index.m3u8
`
	if string(b) != want {
		t.Fatalf("%s", b)
	}
}

func TestBoxGolden(t *testing.T) {
	frag := Fragment(1, 7, 90000, []Sample{{Duration: 3600, KeyFrame: true, Data: []byte{1, 2, 3}}})
	// moof(mfhd seq=7, traf(tfhd, tfdt=90000, trun 1 sample)) + mdat(1,2,3)
	if string(frag[4:8]) != "moof" || string(frag[len(frag)-7:len(frag)-3]) != "mdat" {
		t.Fatalf("%x", frag)
	}
	moofSize := int(frag[0])<<24 | int(frag[1])<<16 | int(frag[2])<<8 | int(frag[3])
	// data_offset in trun = moof size + 8.
	idx := strings.Index(string(frag), "trun") + 4 + 4 + 4
	off := int(frag[idx])<<24 | int(frag[idx+1])<<16 | int(frag[idx+2])<<8 | int(frag[idx+3])
	if off != moofSize+8 {
		t.Fatalf("data_offset %d moof %d", off, moofSize)
	}
}

// Regression: a backwards jump onto a non-keyframe dropped the pending sample, and the
// next sample dereferenced it (the recorder crashed on Chrome's H.264). Samples with the
// same timestamp are parts of one access unit and are merged, not treated as a jump.
func TestMuxerSurvivesJumpsAndSameTimestamp(t *testing.T) {
	aus := genH264Cached(t)
	var sps, pps []byte
	for _, n := range aus[0] {
		switch n[0] & 0x1f {
		case nalSPS:
			sps = n
		case nalPPS:
			pps = n
		}
	}
	m, err := NewMuxer(MuxerConfig{TargetSegmentDuration: 4 * time.Second}, TrackSpec{Codec: CodecH264, Timescale: 90000, SPS: sps, PPS: pps}, &DirSink{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	write := func(dts int64, key bool) {
		t.Helper()
		if err := m.WriteSample(ctx, Sample{DTS: dts, KeyFrame: key, Data: []byte{0, 0, 0, 1, 0x65}}); err != nil {
			t.Fatal(err)
		}
	}
	write(9000, true)
	write(9000, false) // same timestamp: merged into the keyframe
	write(12000, false)
	write(3000, false) // clock went back onto a non-keyframe: dropped
	write(6000, false) // used to panic (nil pending)
	write(9000, true)  // resumes on the next keyframe
	write(12000, false)
	if err := m.Close(ctx); err != nil {
		t.Fatal(err)
	}
}
