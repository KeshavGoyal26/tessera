// Package hlstest generates synthetic RTP for recorder tests and benchmarks:
// a real H.264 clip and an Opus clip encoded with FFmpeg, packetized as
// browsers send them (STAP-A for parameter sets, FU-A for large NAL units).
package hlstest

import (
	"bytes"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/pion/rtp"
)

// NeedFFmpeg skips the test when ffmpeg/ffprobe are missing.
func NeedFFmpeg(t testing.TB) {
	t.Helper()
	for _, b := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(b); err != nil {
			t.Skipf("%s not installed", b)
		}
	}
}

// H264 returns a clip (320x240, 25 fps, GOP 2 s) as NAL units per access unit.
func H264(t testing.TB, seconds int) [][][]byte {
	t.Helper()
	out := filepath.Join(t.TempDir(), "v.h264")
	cmd := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc=size=320x240:rate=25",
		"-t", strconv.Itoa(seconds), "-c:v", "libx264", "-profile:v", "baseline", "-g", "50", "-bf", "0",
		"-x264-params", "aud=1:keyint=50:min-keyint=50:scenecut=0", "-pix_fmt", "yuv420p", "-f", "h264", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, b)
	}
	data, _ := os.ReadFile(out)
	var aus [][][]byte
	for _, n := range splitAnnexB(data) {
		if n[0]&0x1f == 9 {
			aus = append(aus, nil)
		}
		if len(aus) == 0 {
			continue
		}
		aus[len(aus)-1] = append(aus[len(aus)-1], n)
	}
	return aus
}

// Opus returns 20 ms Opus packets of a sine tone.
func Opus(t testing.TB, seconds int) [][]byte {
	t.Helper()
	out := filepath.Join(t.TempDir(), "a.opus")
	cmd := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", strconv.Itoa(seconds), "-c:a", "libopus", "-frame_duration", "20", "-ac", "2", "-f", "opus", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, b)
	}
	data, _ := os.ReadFile(out)
	return oggPackets(data)[2:]
}

func splitAnnexB(b []byte) [][]byte {
	var nals [][]byte
	start := -1
	for i := 0; i+3 <= len(b); i++ {
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
			if start >= 0 {
				end := i
				for end > start && b[end-1] == 0 {
					end--
				}
				nals = append(nals, b[start:end])
			}
			start = i + 3
			i += 2
		}
	}
	if start >= 0 {
		nals = append(nals, b[start:])
	}
	return nals
}

func oggPackets(b []byte) [][]byte {
	var pkts [][]byte
	var cur []byte
	for len(b) >= 27 && bytes.Equal(b[:4], []byte("OggS")) {
		nseg := int(b[26])
		lacing := b[27 : 27+nseg]
		body := b[27+nseg:]
		for _, l := range lacing {
			cur = append(cur, body[:l]...)
			body = body[l:]
			if l < 255 {
				pkts = append(pkts, cur)
				cur = nil
			}
		}
		b = body
	}
	return pkts
}

// Packetizer builds RTP packets for H.264 access units.
type Packetizer struct {
	Seq  uint16
	SSRC uint32
	MTU  int
}

// Packetize returns the packets of one access unit (marker set on the last).
func (p *Packetizer) Packetize(au [][]byte, ts uint32) []*rtp.Packet {
	if p.MTU == 0 {
		p.MTU = 1200
	}
	var pkts []*rtp.Packet
	add := func(payload []byte) {
		pkts = append(pkts, &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: p.Seq, Timestamp: ts, SSRC: p.SSRC}, Payload: payload})
		p.Seq++
	}
	var params [][]byte
	flush := func() {
		switch len(params) {
		case 0:
		case 1:
			add(params[0])
		default:
			pl := []byte{24}
			for _, n := range params {
				pl = binary.BigEndian.AppendUint16(pl, uint16(len(n)))
				pl = append(pl, n...)
			}
			add(pl)
		}
		params = nil
	}
	for _, n := range au {
		switch n[0] & 0x1f {
		case 7, 8:
			params = append(params, n)
			continue
		case 9:
			continue
		}
		flush()
		if len(n) <= p.MTU {
			add(n)
			continue
		}
		hdr := n[0]&0xe0 | 28
		body := n[1:]
		for off := 0; off < len(body); {
			end := min(off+p.MTU-2, len(body))
			fu := n[0] & 0x1f
			if off == 0 {
				fu |= 0x80
			}
			if end == len(body) {
				fu |= 0x40
			}
			add(append([]byte{hdr, fu}, body[off:end]...))
			off = end
		}
	}
	flush()
	pkts[len(pkts)-1].Marker = true
	return pkts
}

// OpusPacket wraps one Opus frame in RTP.
func OpusPacket(seq uint16, ts, ssrc uint32, frame []byte) *rtp.Packet {
	return &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 111, SequenceNumber: seq, Timestamp: ts, SSRC: ssrc, Marker: false}, Payload: frame}
}
