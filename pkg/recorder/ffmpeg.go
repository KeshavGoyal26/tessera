package recorder

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"

	"github.com/KeshavGoyal26/tessera/pkg/hls"
)

// FFmpeg is only used for the two compatibility fallbacks (ADR-0007): Opus to
// AAC-LC and VP8 to H.264. The default H.264 + Opus path never starts it.

// transcoded is one output unit of an FFmpeg pipeline.
type transcoded struct {
	dts  int64
	data []byte          // AAC frame
	au   *hls.AccessUnit // H.264 access unit
}

// ffproc supervises one FFmpeg subprocess with stdin/stdout pipes.
type ffproc struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr bytes.Buffer
	mu     sync.Mutex
}

func startFFmpeg(ctx context.Context, path string, args ...string) (*ffproc, error) {
	if path == "" {
		path = "ffmpeg"
	}
	cmd := exec.CommandContext(ctx, path, append([]string{"-hide_banner", "-v", "error", "-nostdin"}, args...)...)
	p := &ffproc{cmd: cmd}
	cmd.Stderr = &lockedWriter{p}
	var err error
	if p.stdin, err = cmd.StdinPipe(); err != nil {
		return nil, err
	}
	if p.stdout, err = cmd.StdoutPipe(); err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("ffmpeg: %w", err)
	}
	return p, nil
}

type lockedWriter struct{ p *ffproc }

func (w *lockedWriter) Write(b []byte) (int, error) {
	w.p.mu.Lock()
	defer w.p.mu.Unlock()
	if w.p.stderr.Len() < 4096 {
		w.p.stderr.Write(b)
	}
	return len(b), nil
}

// killAfter kills the process if it is still running after d.
func (p *ffproc) killAfter(d time.Duration) {
	time.AfterFunc(d, func() {
		if p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
		}
	})
}

func (p *ffproc) wait() error {
	err := p.cmd.Wait()
	p.mu.Lock()
	defer p.mu.Unlock()
	if err != nil && p.stderr.Len() > 0 {
		return fmt.Errorf("%w: %s", err, bytes.TrimSpace(p.stderr.Bytes()))
	}
	return err
}

// ---- Opus -> AAC -----------------------------------------------------------

// aacTranscoder converts Opus packets to AAC-LC frames via FFmpeg.
type aacTranscoder struct {
	p       *ffproc
	ogg     *oggOpusWriter
	out     chan transcoded
	baseDTS int64
	err     error
	done    chan struct{}
}

// aacConfig48kStereo is the AudioSpecificConfig for AAC-LC, 48 kHz, 2 channels.
var aacConfig48kStereo = []byte{0x11, 0x90}

func newAACTranscoder(ctx context.Context, ffmpeg string, baseDTS int64) (*aacTranscoder, error) {
	p, err := startFFmpeg(ctx, ffmpeg, "-f", "ogg", "-i", "pipe:0", "-c:a", "aac", "-b:a", "96k", "-ar", "48000", "-ac", "2", "-f", "adts", "pipe:1")
	if err != nil {
		return nil, err
	}
	t := &aacTranscoder{p: p, ogg: newOggOpusWriter(p.stdin), out: make(chan transcoded, 256), baseDTS: baseDTS, done: make(chan struct{})}
	if err := t.ogg.writeHeaders(); err != nil {
		return nil, err
	}
	go t.readLoop()
	return t, nil
}

func (t *aacTranscoder) readLoop() {
	defer close(t.done)
	defer close(t.out)
	r := bufio.NewReaderSize(t.p.stdout, 64<<10)
	var n int64
	for {
		hdr, err := r.Peek(7)
		if err != nil {
			break
		}
		if hdr[0] != 0xff || hdr[1]&0xf0 != 0xf0 {
			_, _ = r.Discard(1)
			continue
		}
		flen := int(hdr[3]&3)<<11 | int(hdr[4])<<3 | int(hdr[5]>>5)
		hlen := 7
		if hdr[1]&1 == 0 {
			hlen = 9
		}
		if flen < hlen {
			_, _ = r.Discard(1)
			continue
		}
		frame := make([]byte, flen)
		if _, err := io.ReadFull(r, frame); err != nil {
			break
		}
		t.out <- transcoded{dts: t.baseDTS + n*1024, data: frame[hlen:]}
		n++
	}
	t.err = t.p.wait()
}

func (t *aacTranscoder) write(pkt []byte, samples uint32) error {
	return t.ogg.writePacket(pkt, samples)
}

// closeInput finishes the input; the output channel closes after the last
// frame. result must be called after the channel is drained.
func (t *aacTranscoder) closeInput() {
	_ = t.ogg.finish()
	_ = t.p.stdin.Close()
	t.p.killAfter(10 * time.Second)
}

func (t *aacTranscoder) result() error { <-t.done; return t.err }

// oggOpusWriter writes a minimal Ogg Opus stream (RFC 7845), one packet per page.
type oggOpusWriter struct {
	w      io.Writer
	serial uint32
	seq    uint32
	gran   uint64
}

func newOggOpusWriter(w io.Writer) *oggOpusWriter { return &oggOpusWriter{w: w, serial: 0x7e55e12a} }

var oggCRCTable = func() (t [256]uint32) {
	for i := range t {
		r := uint32(i) << 24
		for j := 0; j < 8; j++ {
			if r&0x80000000 != 0 {
				r = r<<1 ^ 0x04c11db7
			} else {
				r <<= 1
			}
		}
		t[i] = r
	}
	return
}()

func (o *oggOpusWriter) page(kind byte, gran uint64, data []byte) error {
	var lacing []byte
	for n := len(data); ; n -= 255 {
		if n >= 255 {
			lacing = append(lacing, 255)
			continue
		}
		lacing = append(lacing, byte(n))
		break
	}
	h := make([]byte, 27, 27+len(lacing)+len(data))
	copy(h, "OggS")
	h[5] = kind
	binary.LittleEndian.PutUint64(h[6:], gran)
	binary.LittleEndian.PutUint32(h[14:], o.serial)
	binary.LittleEndian.PutUint32(h[18:], o.seq)
	h[26] = byte(len(lacing))
	h = append(h, lacing...)
	h = append(h, data...)
	var crc uint32
	for _, b := range h {
		crc = crc<<8 ^ oggCRCTable[byte(crc>>24)^b]
	}
	binary.LittleEndian.PutUint32(h[22:], crc)
	o.seq++
	_, err := o.w.Write(h)
	return err
}

func (o *oggOpusWriter) writeHeaders() error {
	head := []byte("OpusHead")
	head = append(head, 1, 2)
	head = binary.LittleEndian.AppendUint16(head, 312)
	head = binary.LittleEndian.AppendUint32(head, 48000)
	head = binary.LittleEndian.AppendUint16(head, 0)
	head = append(head, 0)
	if err := o.page(0x02, 0, head); err != nil {
		return err
	}
	tags := []byte("OpusTags")
	tags = binary.LittleEndian.AppendUint32(tags, 7)
	tags = append(tags, "tessera"...)
	tags = binary.LittleEndian.AppendUint32(tags, 0)
	return o.page(0, 0, tags)
}

func (o *oggOpusWriter) writePacket(p []byte, samples uint32) error {
	if samples == 0 {
		samples = 960
	}
	o.gran += uint64(samples)
	return o.page(0, o.gran+312, p)
}

func (o *oggOpusWriter) finish() error { return o.page(0x04, o.gran+312, nil) }

// ---- VP8 -> H.264 ----------------------------------------------------------

// vp8Transcoder assembles VP8 frames from RTP and converts them to H.264.
type vp8Transcoder struct {
	p    *ffproc
	out  chan transcoded
	done chan struct{}
	err  error

	mu      sync.Mutex
	pending []int64 // DTS of frames in flight, FIFO

	// depacketizer state
	started, wantKey bool
	lastSeq          uint16
	frame            []byte
	ts               uint32
	damaged          bool
	headerWritten    bool
	frames           uint32
}

func newVP8Transcoder(ctx context.Context, ffmpeg string) (*vp8Transcoder, error) {
	p, err := startFFmpeg(ctx, ffmpeg, "-f", "ivf", "-i", "pipe:0", "-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency",
		"-profile:v", "baseline", "-bf", "0", "-g", "50", "-x264-params", "aud=1:keyint=50:min-keyint=50:scenecut=0", "-pix_fmt", "yuv420p", "-f", "h264", "pipe:1")
	if err != nil {
		return nil, err
	}
	t := &vp8Transcoder{p: p, out: make(chan transcoded, 256), done: make(chan struct{}), wantKey: true}
	go t.readLoop()
	return t, nil
}

// push feeds one RTP packet; dtsOf maps the frame's RTP timestamp to the output timeline.
// It returns ErrPacketLoss when a keyframe should be requested.
func (t *vp8Transcoder) push(p *rtp.Packet, dtsOf func(uint32) int64) error {
	var ret error
	if t.started && p.SequenceNumber != t.lastSeq+1 {
		t.damaged, t.wantKey, ret = true, true, hls.ErrPacketLoss
	}
	t.started, t.lastSeq = true, p.SequenceNumber
	var vp codecs.VP8Packet
	payload, err := vp.Unmarshal(p.Payload)
	if err != nil {
		t.damaged = true
		return ret
	}
	if vp.S == 1 && vp.PID == 0 {
		t.frame = t.frame[:0]
		t.ts = p.Timestamp
		t.damaged = ret != nil
	} else if len(t.frame) == 0 && !t.damaged {
		t.damaged = true // continuation without a start
	}
	t.frame = append(t.frame, payload...)
	if !p.Marker {
		return ret
	}
	frame := t.frame
	t.frame = nil
	if t.damaged || len(frame) < 10 {
		t.damaged = false
		t.wantKey = true
		return hls.ErrPacketLoss
	}
	key := frame[0]&1 == 0
	if t.wantKey {
		if !key {
			return ret
		}
		t.wantKey = false
	}
	if !t.headerWritten {
		if !key {
			return ret
		}
		w, h := binary.LittleEndian.Uint16(frame[6:])&0x3fff, binary.LittleEndian.Uint16(frame[8:])&0x3fff
		hdr := make([]byte, 32)
		copy(hdr, "DKIF")
		binary.LittleEndian.PutUint16(hdr[6:], 32)
		copy(hdr[8:], "VP80")
		binary.LittleEndian.PutUint16(hdr[12:], w)
		binary.LittleEndian.PutUint16(hdr[14:], h)
		binary.LittleEndian.PutUint32(hdr[16:], 90000)
		binary.LittleEndian.PutUint32(hdr[20:], 1)
		if _, err := t.p.stdin.Write(hdr); err != nil {
			return err
		}
		t.headerWritten = true
	}
	fh := make([]byte, 12)
	binary.LittleEndian.PutUint32(fh, uint32(len(frame)))
	binary.LittleEndian.PutUint64(fh[4:], uint64(t.frames))
	t.frames++
	t.mu.Lock()
	t.pending = append(t.pending, dtsOf(t.ts))
	t.mu.Unlock()
	if _, err := t.p.stdin.Write(append(fh, frame...)); err != nil {
		return err
	}
	return ret
}

func (t *vp8Transcoder) readLoop() {
	defer close(t.done)
	defer close(t.out)
	r := bufio.NewReaderSize(t.p.stdout, 256<<10)
	var cur *hls.AccessUnit
	emit := func() {
		if cur == nil || len(cur.NALUs) == 0 {
			return
		}
		t.mu.Lock()
		var dts int64
		if len(t.pending) > 0 {
			dts, t.pending = t.pending[0], t.pending[1:]
		}
		t.mu.Unlock()
		t.out <- transcoded{dts: dts, au: cur}
	}
	sc := newAnnexBScanner(r)
	for {
		nal, err := sc.next()
		if err != nil {
			break
		}
		switch nal[0] & 0x1f {
		case 9: // AUD starts a new access unit
			emit()
			cur = &hls.AccessUnit{}
		case 7:
			if cur != nil {
				cur.SPS = nal
			}
		case 8:
			if cur != nil {
				cur.PPS = nal
			}
		default:
			if cur == nil {
				continue
			}
			if nal[0]&0x1f == 5 {
				cur.KeyFrame = true
			}
			cur.NALUs = append(cur.NALUs, nal)
		}
	}
	emit()
	t.err = t.p.wait()
}

func (t *vp8Transcoder) closeInput() {
	_ = t.p.stdin.Close()
	t.p.killAfter(10 * time.Second)
}

func (t *vp8Transcoder) result() error { <-t.done; return t.err }

// annexBScanner yields NAL units (without start codes) from an Annex B stream.
type annexBScanner struct {
	r    *bufio.Reader
	buf  []byte
	init bool
}

func newAnnexBScanner(r *bufio.Reader) *annexBScanner { return &annexBScanner{r: r} }

func (s *annexBScanner) next() ([]byte, error) {
	for {
		// find the next start code after position 0 of buf
		if i := nextStartCode(s.buf, 0); i >= 0 && s.init {
			nal := trimZeros(s.buf[:i])
			s.buf = s.buf[i:]
			s.skipStart()
			if len(nal) > 0 {
				return append([]byte(nil), nal...), nil
			}
			continue
		}
		chunk := make([]byte, 64<<10)
		n, err := s.r.Read(chunk)
		s.buf = append(s.buf, chunk[:n]...)
		if !s.init {
			if i := nextStartCode(s.buf, 0); i >= 0 {
				s.buf = s.buf[i:]
				s.skipStart()
				s.init = true
			} else if len(s.buf) > 4 {
				s.buf = s.buf[len(s.buf)-3:]
			}
		}
		if err != nil {
			if err == io.EOF && s.init && len(s.buf) > 0 {
				nal := trimZeros(s.buf)
				s.buf = nil
				if len(nal) > 0 {
					return append([]byte(nil), nal...), nil
				}
			}
			return nil, err
		}
	}
}

// skipStart drops a leading 00 00 01 (or 00 00 00 01).
func (s *annexBScanner) skipStart() {
	for len(s.buf) > 0 && s.buf[0] == 0 {
		s.buf = s.buf[1:]
	}
	if len(s.buf) > 0 && s.buf[0] == 1 {
		s.buf = s.buf[1:]
	}
}

func nextStartCode(b []byte, from int) int {
	for i := from; i+3 <= len(b); i++ {
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
			return i
		}
	}
	return -1
}

func trimZeros(b []byte) []byte {
	for len(b) > 0 && b[len(b)-1] == 0 {
		b = b[:len(b)-1]
	}
	return b
}
