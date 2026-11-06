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
)

// FFmpeg is only used for the opt-in Opus to AAC-LC audio fallback (ADR-0007).
// Video is never transcoded: recordings need H.264. The default H.264 + Opus
// path never starts FFmpeg.

// transcoded is one output unit of an FFmpeg pipeline.
type transcoded struct {
	dts  int64
	data []byte // AAC frame
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
