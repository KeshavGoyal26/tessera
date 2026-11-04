package buffer

import "encoding/binary"

// Header is the parsed fixed RTP header plus the location of the payload.
type Header struct {
	PayloadType    uint8
	Marker         bool
	Padding        bool
	SequenceNumber uint16
	Timestamp      uint32
	SSRC           uint32
	// ExtProfile is the extension profile (0xBEDE one-byte, 0x1000 two-byte); 0 = none.
	ExtProfile uint16
	// ExtStart/ExtEnd delimit the extension body inside the packet.
	ExtStart, ExtEnd int
	// HeaderSize is the offset of the payload.
	HeaderSize int
	// PaddingSize is the number of padding bytes at the end.
	PaddingSize int
}

// ParseHeader parses an RTP header without allocating.
func ParseHeader(b []byte, h *Header) error {
	if len(b) < 12 {
		return errShort
	}
	if b[0]>>6 != 2 {
		return errVersion
	}
	cc := int(b[0] & 0x0f)
	h.Padding = b[0]&0x20 != 0
	ext := b[0]&0x10 != 0
	h.Marker = b[1]&0x80 != 0
	h.PayloadType = b[1] & 0x7f
	h.SequenceNumber = binary.BigEndian.Uint16(b[2:])
	h.Timestamp = binary.BigEndian.Uint32(b[4:])
	h.SSRC = binary.BigEndian.Uint32(b[8:])
	off := 12 + 4*cc
	if len(b) < off {
		return errShort
	}
	h.ExtProfile, h.ExtStart, h.ExtEnd = 0, 0, 0
	if ext {
		if len(b) < off+4 {
			return errShort
		}
		h.ExtProfile = binary.BigEndian.Uint16(b[off:])
		l := int(binary.BigEndian.Uint16(b[off+2:])) * 4
		h.ExtStart = off + 4
		off += 4 + l
		h.ExtEnd = off
		if len(b) < off {
			return errShort
		}
	}
	h.HeaderSize = off
	h.PaddingSize = 0
	if h.Padding {
		if len(b) == off {
			return errShort
		}
		p := int(b[len(b)-1])
		if p == 0 || off+p > len(b) {
			return errPadding
		}
		h.PaddingSize = p
	}
	return nil
}

// FindExtension returns the body of header extension id, or nil.
func FindExtension(b []byte, h *Header, id uint8) []byte {
	if h.ExtProfile == 0 || id == 0 {
		return nil
	}
	body := b[h.ExtStart:h.ExtEnd]
	switch {
	case h.ExtProfile == 0xBEDE:
		for i := 0; i < len(body); {
			if body[i] == 0 { // padding
				i++
				continue
			}
			eid := body[i] >> 4
			l := int(body[i]&0x0f) + 1
			if eid == 15 {
				return nil
			}
			if i+1+l > len(body) {
				return nil
			}
			if eid == id {
				return body[i+1 : i+1+l]
			}
			i += 1 + l
		}
	case h.ExtProfile&0xfff0 == 0x1000:
		for i := 0; i+1 < len(body); {
			if body[i] == 0 {
				i++
				continue
			}
			eid, l := body[i], int(body[i+1])
			if i+2+l > len(body) {
				return nil
			}
			if eid == id {
				return body[i+2 : i+2+l]
			}
			i += 2 + l
		}
	}
	return nil
}

type parseError string

func (e parseError) Error() string { return string(e) }

const (
	errShort   = parseError("rtp: packet too short")
	errVersion = parseError("rtp: bad version")
	errPadding = parseError("rtp: bad padding")
	errVP8     = parseError("vp8: bad descriptor")
)

// ParseVP8 parses the VP8 payload descriptor (RFC 7741) of payload into d.
func ParseVP8(payload []byte, d *VP8Descriptor) error {
	*d = VP8Descriptor{}
	if len(payload) < 1 {
		return errVP8
	}
	d.FirstByte = payload[0]
	x := payload[0]&0x80 != 0
	s := payload[0]&0x10 != 0
	pid := payload[0] & 0x07
	i := 1
	if x {
		if len(payload) < 2 {
			return errVP8
		}
		ext := payload[1]
		i = 2
		if ext&0x80 != 0 { // I
			if len(payload) < i+1 {
				return errVP8
			}
			d.PictureIDPresent = true
			if payload[i]&0x80 != 0 {
				if len(payload) < i+2 {
					return errVP8
				}
				d.MBit = true
				d.PictureID = uint16(payload[i]&0x7f)<<8 | uint16(payload[i+1])
				i += 2
			} else {
				d.PictureID = uint16(payload[i])
				i++
			}
		}
		if ext&0x40 != 0 { // L
			if len(payload) < i+1 {
				return errVP8
			}
			d.TL0PICIDXPresent = true
			d.TL0PICIDX = payload[i]
			i++
		}
		if ext&0x20 != 0 || ext&0x10 != 0 { // T or K
			if len(payload) < i+1 {
				return errVP8
			}
			if ext&0x20 != 0 {
				d.TIDPresent = true
				d.TID = payload[i] >> 6
				d.Y = payload[i]&0x20 != 0
			}
			if ext&0x10 != 0 {
				d.KEYIDXPresent = true
				d.KEYIDX = payload[i] & 0x1f
			}
			i++
		}
	}
	d.HeaderSize = i
	if s && pid == 0 && len(payload) > i {
		d.IsKeyFrame = payload[i]&0x01 == 0
	}
	return nil
}

// IsH264KeyFrame reports whether an H.264 RTP payload (RFC 6184) starts or
// contains a keyframe: an SPS (7), or an IDR slice (5) in a single NAL unit,
// a STAP-A, or the first fragment of an FU-A.
func IsH264KeyFrame(payload []byte) bool {
	if len(payload) < 1 {
		return false
	}
	switch payload[0] & 0x1f {
	case 5, 7:
		return true
	case 24: // STAP-A
		for i := 1; i+2 < len(payload); {
			size := int(binary.BigEndian.Uint16(payload[i:]))
			i += 2
			if i >= len(payload) {
				return false
			}
			if t := payload[i] & 0x1f; t == 5 || t == 7 {
				return true
			}
			i += size
		}
	case 28: // FU-A
		if len(payload) < 2 {
			return false
		}
		return payload[1]&0x80 != 0 && payload[1]&0x1f == 5
	}
	return false
}
