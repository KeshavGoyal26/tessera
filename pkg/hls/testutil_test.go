package hls

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/pion/rtp"
)

func needFFmpeg(t *testing.T) {
	t.Helper()
	for _, b := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(b); err != nil {
			t.Skipf("%s not installed", b)
		}
	}
}

// genH264 encodes a synthetic clip (ffmpeg testsrc, baseline, 25 fps, GOP 50)
// and returns one slice of NAL units (without start codes) per access unit.
func genH264(t *testing.T, seconds int) [][][]byte {
	t.Helper()
	out := filepath.Join(t.TempDir(), "v.h264")
	cmd := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc=size=320x240:rate=25",
		"-t", itoa(seconds), "-c:v", "libx264", "-profile:v", "baseline", "-g", "50", "-bf", "0",
		"-x264-params", "aud=1:keyint=50:min-keyint=50:scenecut=0", "-pix_fmt", "yuv420p", "-f", "h264", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, b)
	}
	data, _ := os.ReadFile(out)
	var aus [][][]byte
	for _, n := range splitAnnexB(data) {
		if n[0]&0x1f == nalAUD {
			aus = append(aus, nil)
		}
		if len(aus) == 0 {
			continue
		}
		aus[len(aus)-1] = append(aus[len(aus)-1], n)
	}
	return aus
}

func itoa(i int) string { return strconv.Itoa(i) }

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

// packetizer builds RTP packets for access units (STAP-A for SPS/PPS, FU-A for large NALs).
type packetizer struct {
	seq  uint16
	ssrc uint32
	mtu  int
}

func (p *packetizer) packetize(au [][]byte, ts uint32) []*rtp.Packet {
	var pkts []*rtp.Packet
	add := func(payload []byte) {
		pkts = append(pkts, &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: p.seq, Timestamp: ts, SSRC: p.ssrc}, Payload: payload})
		p.seq++
	}
	var params [][]byte
	flushParams := func() {
		if len(params) == 0 {
			return
		}
		if len(params) == 1 {
			add(params[0])
		} else {
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
		case nalSPS, nalPPS:
			params = append(params, n)
			continue
		case nalAUD:
			continue
		}
		flushParams()
		if len(n) <= p.mtu {
			add(n)
			continue
		}
		hdr := n[0]&0xe0 | 28
		body := n[1:]
		for off := 0; off < len(body); {
			end := off + p.mtu - 2
			if end > len(body) {
				end = len(body)
			}
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
	flushParams()
	pkts[len(pkts)-1].Marker = true
	return pkts
}

// genOpus encodes 20 ms Opus frames and returns the packets.
func genOpus(t *testing.T, seconds int) [][]byte {
	t.Helper()
	out := filepath.Join(t.TempDir(), "a.opus")
	cmd := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", itoa(seconds), "-c:a", "libopus", "-frame_duration", "20", "-ac", "2", "-f", "opus", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, b)
	}
	data, _ := os.ReadFile(out)
	return oggPackets(data)[2:] // skip OpusHead, OpusTags
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

func fmtSscan(s string, f *float64) (int, error) { return fmt.Sscan(s, f) }
