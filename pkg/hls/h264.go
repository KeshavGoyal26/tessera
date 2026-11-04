package hls

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/pion/rtp"
)

// ErrPacketLoss is returned by Push when a sequence gap was detected. The
// affected frame is dropped and the depacketizer discards everything until
// the next IDR, so the caller should request a keyframe.
var ErrPacketLoss = errors.New("hls: rtp packet loss, waiting for keyframe")

// H.264 NAL unit types used here.
const (
	nalSlice   = 1
	nalIDR     = 5
	nalSEI     = 6
	nalSPS     = 7
	nalPPS     = 8
	nalAUD     = 9
	nalFiller  = 12
	nalSTAPA   = 24
	nalFUA     = 28
	maxAUBytes = 8 << 20
	maxAUNALUs = 4096
)

// AccessUnit is a depacketized H.264 frame.
type AccessUnit struct {
	RTPTimestamp uint32
	// NALUs holds slice/SEI NAL units without start codes. SPS, PPS, AUD and
	// filler NALs are removed (parameter sets are reported in SPS/PPS).
	NALUs    [][]byte
	KeyFrame bool
	// SPS/PPS seen in this AU (for init segment updates), nil if none.
	SPS, PPS []byte
}

// AVCC returns the NAL units as 4-byte length-prefixed data (ISO-BMFF form).
func (a *AccessUnit) AVCC() []byte {
	n := 0
	for _, u := range a.NALUs {
		n += 4 + len(u)
	}
	out := make([]byte, 0, n)
	for _, u := range a.NALUs {
		out = binary.BigEndian.AppendUint32(out, uint32(len(u)))
		out = append(out, u...)
	}
	return out
}

// H264Depacketizer assembles access units from RTP (RFC 6184, packetization-mode 1).
// It is not safe for concurrent use.
type H264Depacketizer struct {
	started  bool
	lastSeq  uint16
	curTS    uint32
	have     bool // current AU has data
	cur      AccessUnit
	size     int
	damaged  bool
	waitKey  bool
	fu       []byte
	fuActive bool
	// Dropped counts access units discarded because of loss or waiting for a keyframe.
	Dropped int
}

// Push adds a packet in sequence order and returns the access units completed
// by it (marker bit or timestamp change). On a sequence gap it returns
// ErrPacketLoss (possibly together with units completed before the gap) and
// resynchronizes on the next IDR.
func (d *H264Depacketizer) Push(pkt *rtp.Packet) ([]*AccessUnit, error) {
	var out []*AccessUnit
	var retErr error
	if d.started && pkt.SequenceNumber != d.lastSeq+1 {
		// Gap or duplicate/old packet. Old/duplicate packets are ignored.
		if int16(pkt.SequenceNumber-d.lastSeq) <= 0 {
			return nil, nil
		}
		d.damaged = true
		d.waitKey = true
		d.fuActive = false
		retErr = ErrPacketLoss
	}
	d.started = true
	d.lastSeq = pkt.SequenceNumber

	if d.have && pkt.Timestamp != d.curTS {
		// Marker packet of the previous frame was lost or never sent.
		if au := d.finish(); au != nil {
			out = append(out, au)
		}
	}
	if !d.have {
		d.curTS = pkt.Timestamp
		d.have = true
		d.cur = AccessUnit{RTPTimestamp: pkt.Timestamp}
		d.size = 0
		if retErr != nil {
			d.damaged = true
		}
	}
	d.addPayload(pkt.Payload)
	if pkt.Marker {
		if au := d.finish(); au != nil {
			out = append(out, au)
		}
	}
	return out, retErr
}

// Flush returns the pending access unit, if complete enough to use.
func (d *H264Depacketizer) Flush() *AccessUnit { return d.finish() }

func (d *H264Depacketizer) finish() *AccessUnit {
	if !d.have {
		return nil
	}
	d.have = false
	au := d.cur
	damaged := d.damaged || d.fuActive
	d.damaged = false
	d.fuActive = false
	d.fu = d.fu[:0]
	if damaged {
		d.waitKey = true
		d.Dropped++
		return nil
	}
	if len(au.NALUs) == 0 && au.SPS == nil {
		return nil
	}
	if d.waitKey {
		if !au.KeyFrame {
			d.Dropped++
			return nil
		}
		d.waitKey = false
	}
	if len(au.NALUs) == 0 {
		return nil
	}
	return &au
}

func (d *H264Depacketizer) addPayload(p []byte) {
	if len(p) < 1 {
		return
	}
	switch t := p[0] & 0x1f; {
	case t >= 1 && t <= 23:
		d.addNAL(p)
	case t == nalSTAPA:
		p = p[1:]
		for len(p) >= 2 {
			n := int(binary.BigEndian.Uint16(p))
			p = p[2:]
			if n == 0 || n > len(p) {
				d.damaged = true
				return
			}
			d.addNAL(p[:n])
			p = p[n:]
		}
	case t == nalFUA:
		if len(p) < 3 {
			d.damaged = true
			return
		}
		start, end := p[1]&0x80 != 0, p[1]&0x40 != 0
		if start {
			d.fu = append(d.fu[:0], p[1]&0x1f|p[0]&0xe0)
			d.fuActive = true
		} else if !d.fuActive {
			d.damaged = true
			return
		}
		if len(d.fu)+len(p) > maxAUBytes {
			d.damaged = true
			d.fuActive = false
			return
		}
		d.fu = append(d.fu, p[2:]...)
		if end {
			d.addNAL(append([]byte(nil), d.fu...))
			d.fuActive = false
			d.fu = d.fu[:0]
		}
	default:
		// STAP-B, MTAP, FU-B are not used by WebRTC; ignore.
	}
}

func (d *H264Depacketizer) addNAL(n []byte) {
	if len(n) == 0 {
		return
	}
	switch n[0] & 0x1f {
	case nalSPS:
		d.cur.SPS = append([]byte(nil), n...)
		return
	case nalPPS:
		d.cur.PPS = append([]byte(nil), n...)
		return
	case nalAUD, nalFiller:
		return
	case nalIDR:
		d.cur.KeyFrame = true
	}
	d.size += len(n)
	if d.size > maxAUBytes || len(d.cur.NALUs) >= maxAUNALUs {
		d.damaged = true
		return
	}
	d.cur.NALUs = append(d.cur.NALUs, append([]byte(nil), n...))
}

// SPSInfo is the subset of an H.264 sequence parameter set needed for the init segment.
type SPSInfo struct {
	Profile, Compat, Level uint8
	Width, Height          uint32
	ChromaFormat           uint8
	BitDepthLuma           uint8
	BitDepthChroma         uint8
}

// CodecString returns the RFC 6381 string ("avc1.42e01f").
func (s SPSInfo) CodecString() string {
	return fmt.Sprintf("avc1.%02x%02x%02x", s.Profile, s.Compat, s.Level)
}

type bitReader struct {
	b   []byte
	pos int
	err bool
}

func (r *bitReader) bit() uint32 {
	if r.pos>>3 >= len(r.b) {
		r.err = true
		return 0
	}
	v := (r.b[r.pos>>3] >> (7 - uint(r.pos&7))) & 1
	r.pos++
	return uint32(v)
}

func (r *bitReader) bits(n int) uint32 {
	var v uint32
	for i := 0; i < n; i++ {
		v = v<<1 | r.bit()
	}
	return v
}

func (r *bitReader) ue() uint32 {
	zeros := 0
	for r.bit() == 0 && !r.err {
		zeros++
		if zeros > 31 {
			r.err = true
			return 0
		}
	}
	return (1<<uint(zeros) - 1) + r.bits(zeros)
}

func (r *bitReader) se() int32 {
	v := r.ue()
	if v&1 == 1 {
		return int32((v + 1) / 2)
	}
	return -int32(v / 2)
}

func unescapeRBSP(b []byte) []byte {
	out := make([]byte, 0, len(b))
	zeros := 0
	for _, c := range b {
		if zeros >= 2 && c == 3 {
			zeros = 0
			continue
		}
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
		out = append(out, c)
	}
	return out
}

// ParseSPS parses a SPS NAL unit (including its header byte).
func ParseSPS(nal []byte) (SPSInfo, error) {
	var s SPSInfo
	if len(nal) < 5 || nal[0]&0x1f != nalSPS {
		return s, errors.New("hls: not an SPS")
	}
	s.Profile, s.Compat, s.Level = nal[1], nal[2], nal[3]
	r := &bitReader{b: unescapeRBSP(nal[4:])}
	r.ue() // seq_parameter_set_id
	s.ChromaFormat, s.BitDepthLuma, s.BitDepthChroma = 1, 8, 8
	switch s.Profile {
	case 100, 110, 122, 244, 44, 83, 86, 118, 128, 138, 139, 134, 135:
		s.ChromaFormat = uint8(r.ue())
		if s.ChromaFormat == 3 {
			r.bit()
		}
		s.BitDepthLuma = uint8(r.ue()) + 8
		s.BitDepthChroma = uint8(r.ue()) + 8
		r.bit() // qpprime_y_zero_transform_bypass
		if r.bit() == 1 {
			n := 8
			if s.ChromaFormat == 3 {
				n = 12
			}
			for i := 0; i < n; i++ {
				if r.bit() == 1 {
					size := 16
					if i >= 6 {
						size = 64
					}
					last, next := int32(8), int32(8)
					for j := 0; j < size; j++ {
						if next != 0 {
							next = (last + r.se() + 256) % 256
						}
						if next != 0 {
							last = next
						}
					}
				}
			}
		}
	}
	r.ue() // log2_max_frame_num_minus4
	switch r.ue() {
	case 0:
		r.ue()
	case 1:
		r.bit()
		r.se()
		r.se()
		n := r.ue()
		if n > 255 {
			return s, errors.New("hls: bad SPS")
		}
		for i := uint32(0); i < n; i++ {
			r.se()
		}
	}
	r.ue() // max_num_ref_frames
	r.bit()
	wMbs, hMapUnits := r.ue()+1, r.ue()+1
	frameMbsOnly := r.bit()
	if frameMbsOnly == 0 {
		r.bit()
	}
	r.bit() // direct_8x8_inference
	var cl, cr, ct, cb uint32
	if r.bit() == 1 {
		cl, cr, ct, cb = r.ue(), r.ue(), r.ue(), r.ue()
	}
	if r.err || wMbs > 1024 || hMapUnits > 1024 {
		return s, errors.New("hls: bad SPS")
	}
	cux, cuy := uint32(1), uint32(2-frameMbsOnly)
	if s.ChromaFormat == 1 || s.ChromaFormat == 2 {
		cux = 2
	}
	if s.ChromaFormat == 1 {
		cuy *= 2
	}
	s.Width = wMbs*16 - cux*(cl+cr)
	s.Height = (2-frameMbsOnly)*hMapUnits*16 - cuy*(ct+cb)
	return s, nil
}
