package hls

import (
	"encoding/binary"
	"errors"
)

// ISO-BMFF box writer (ISO/IEC 14496-12, CMAF 23000-19). Only what fMP4 HLS
// needs: init segment (ftyp+moov) and fragments (moof+mdat).

type buf struct{ b []byte }

func (w *buf) u8(v byte)    { w.b = append(w.b, v) }
func (w *buf) u16(v uint16) { w.b = binary.BigEndian.AppendUint16(w.b, v) }
func (w *buf) u32(v uint32) { w.b = binary.BigEndian.AppendUint32(w.b, v) }
func (w *buf) u64(v uint64) { w.b = binary.BigEndian.AppendUint64(w.b, v) }
func (w *buf) raw(v ...byte) {
	w.b = append(w.b, v...)
}
func (w *buf) zero(n int) { w.b = append(w.b, make([]byte, n)...) }

// box wraps payload parts in a box header.
func box(typ string, parts ...[]byte) []byte {
	n := 8
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 0, n)
	out = binary.BigEndian.AppendUint32(out, uint32(n))
	out = append(out, typ...)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func fullBox(typ string, version byte, flags uint32, parts ...[]byte) []byte {
	hdr := []byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}
	return box(typ, append([][]byte{hdr}, parts...)...)
}

func be32(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }

// InitSegment builds ftyp+moov for one track.
func InitSegment(spec TrackSpec) ([]byte, error) {
	if spec.Timescale == 0 {
		return nil, errors.New("hls: zero timescale")
	}
	var stsd []byte
	var err error
	isVideo := spec.Codec == CodecH264
	switch spec.Codec {
	case CodecH264:
		stsd, err = avc1Entry(spec)
	case CodecOpus:
		stsd = opusEntry(spec)
	case CodecAAC:
		stsd, err = aacEntry(spec)
	default:
		err = errors.New("hls: unsupported codec " + string(spec.Codec))
	}
	if err != nil {
		return nil, err
	}
	trackID := spec.ID
	if trackID == 0 {
		trackID = 1
	}

	ftyp := box("ftyp", []byte("iso6"), be32(0), []byte("iso6"), []byte("cmfc"), []byte("isom"), []byte("mp41"))

	matrix := []byte{0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x40, 0, 0, 0}
	var mvhd buf
	mvhd.u32(0)
	mvhd.u32(0)
	mvhd.u32(1000)
	mvhd.u32(0)
	mvhd.u32(0x00010000)
	mvhd.u16(0x0100)
	mvhd.zero(10)
	mvhd.raw(matrix...)
	mvhd.zero(24)
	mvhd.u32(trackID + 1)

	var tkhd buf
	tkhd.u32(0)
	tkhd.u32(0)
	tkhd.u32(trackID)
	tkhd.u32(0)
	tkhd.u32(0)
	tkhd.zero(8)
	tkhd.u16(0)
	tkhd.u16(0)
	if isVideo {
		tkhd.u16(0)
	} else {
		tkhd.u16(0x0100)
	}
	tkhd.u16(0)
	tkhd.raw(matrix...)
	if isVideo {
		tkhd.u32(spec.Width << 16)
		tkhd.u32(spec.Height << 16)
	} else {
		tkhd.u32(0)
		tkhd.u32(0)
	}

	var mdhd buf
	mdhd.u32(0)
	mdhd.u32(0)
	mdhd.u32(spec.Timescale)
	mdhd.u32(0)
	mdhd.u16(0x55c4) // 'und'
	mdhd.u16(0)

	handler, name := "soun", "SoundHandler"
	var mediaHdr []byte
	if isVideo {
		handler, name = "vide", "VideoHandler"
		mediaHdr = fullBox("vmhd", 0, 1, make([]byte, 8))
	} else {
		mediaHdr = fullBox("smhd", 0, 0, make([]byte, 4))
	}
	var hdlr buf
	hdlr.u32(0)
	hdlr.raw([]byte(handler)...)
	hdlr.zero(12)
	hdlr.raw([]byte(name)...)
	hdlr.u8(0)

	dinf := box("dinf", fullBox("dref", 0, 0, be32(1), fullBox("url ", 0, 1)))
	empty := func(t string) []byte { return fullBox(t, 0, 0, be32(0)) }
	stbl := box("stbl",
		fullBox("stsd", 0, 0, be32(1), stsd),
		empty("stts"), empty("stsc"),
		fullBox("stsz", 0, 0, be32(0), be32(0)),
		empty("stco"))
	minf := box("minf", mediaHdr, dinf, stbl)
	mdia := box("mdia", fullBox("mdhd", 0, 0, mdhd.b), fullBox("hdlr", 0, 0, hdlr.b), minf)
	trak := box("trak", fullBox("tkhd", 0, 7, tkhd.b), mdia)

	var trex buf
	trex.u32(trackID)
	trex.u32(1)
	trex.u32(0)
	trex.u32(0)
	trex.u32(0)
	mvex := box("mvex", fullBox("trex", 0, 0, trex.b))
	moov := box("moov", fullBox("mvhd", 0, 0, mvhd.b), trak, mvex)
	return append(ftyp, moov...), nil
}

func avc1Entry(spec TrackSpec) ([]byte, error) {
	info, err := ParseSPS(spec.SPS)
	if err != nil {
		return nil, err
	}
	if len(spec.PPS) == 0 {
		return nil, errors.New("hls: missing PPS")
	}
	var c buf
	c.u8(1)
	c.u8(info.Profile)
	c.u8(info.Compat)
	c.u8(info.Level)
	c.u8(0xff)
	c.u8(0xe1)
	c.u16(uint16(len(spec.SPS)))
	c.raw(spec.SPS...)
	c.u8(1)
	c.u16(uint16(len(spec.PPS)))
	c.raw(spec.PPS...)
	switch info.Profile {
	case 100, 110, 122, 144:
		c.u8(0xfc | info.ChromaFormat)
		c.u8(0xf8 | (info.BitDepthLuma - 8))
		c.u8(0xf8 | (info.BitDepthChroma - 8))
		c.u8(0)
	}
	var e buf
	e.zero(6)
	e.u16(1)
	e.zero(16)
	e.u16(uint16(info.Width))
	e.u16(uint16(info.Height))
	e.u32(0x00480000)
	e.u32(0x00480000)
	e.u32(0)
	e.u16(1)
	e.zero(32)
	e.u16(0x0018)
	e.u16(0xffff)
	e.raw(box("avcC", c.b)...)
	return box("avc1", e.b), nil
}

func audioEntryHeader(e *buf, channels uint16, rate uint32) {
	e.zero(6)
	e.u16(1)
	e.zero(8)
	e.u16(channels)
	e.u16(16)
	e.u16(0)
	e.u16(0)
	e.u32(rate << 16)
}

func opusEntry(spec TrackSpec) []byte {
	ch := spec.Channels
	if ch == 0 {
		ch = 2
	}
	var e buf
	audioEntryHeader(&e, ch, 48000)
	var d buf
	d.u8(0)
	d.u8(byte(ch))
	d.u16(spec.PreSkip)
	d.u32(48000)
	d.u16(0)
	d.u8(0)
	e.raw(box("dOps", d.b)...)
	return box("Opus", e.b)
}

func descr(tag byte, body []byte) []byte {
	out := []byte{tag}
	n := len(body)
	// 4-byte expandable length, always valid.
	out = append(out, byte(n>>21)|0x80, byte(n>>14)|0x80, byte(n>>7)|0x80, byte(n)&0x7f)
	return append(out, body...)
}

func aacEntry(spec TrackSpec) ([]byte, error) {
	if len(spec.AudioSpecificConfig) == 0 {
		return nil, errors.New("hls: missing AudioSpecificConfig")
	}
	ch := spec.Channels
	if ch == 0 {
		ch = 2
	}
	rate := spec.SampleRate
	if rate == 0 {
		rate = 48000
	}
	var dc buf
	dc.u8(0x40)
	dc.u8(0x15)
	dc.raw(0, 0, 0)
	dc.u32(0)
	dc.u32(0)
	dc.raw(descr(5, spec.AudioSpecificConfig)...)
	var es buf
	es.u16(1)
	es.u8(0)
	es.raw(descr(4, dc.b)...)
	es.raw(descr(6, []byte{2})...)
	var e buf
	audioEntryHeader(&e, ch, rate)
	e.raw(fullBox("esds", 0, 0, descr(3, es.b))...)
	return box("mp4a", e.b), nil
}

const (
	sampleFlagSync    = 0x02000000
	sampleFlagNonSync = 0x01010000
)

// Fragment builds moof+mdat for consecutive samples starting at baseDecodeTime.
func Fragment(trackID uint32, seq uint32, baseDecodeTime uint64, samples []Sample) []byte {
	if trackID == 0 {
		trackID = 1
	}
	var tfhd buf
	tfhd.u32(trackID)
	var tfdt buf
	tfdt.u64(baseDecodeTime)

	var trun buf
	trun.u32(uint32(len(samples)))
	trun.u32(0) // data offset, patched below
	for _, s := range samples {
		trun.u32(s.Duration)
		trun.u32(uint32(len(s.Data)))
		if s.KeyFrame {
			trun.u32(sampleFlagSync)
		} else {
			trun.u32(sampleFlagNonSync)
		}
		trun.u32(uint32(s.CTSOffset))
	}
	trunBox := fullBox("trun", 1, 0x000f01, trun.b)
	traf := box("traf",
		fullBox("tfhd", 0, 0x020000, tfhd.b),
		fullBox("tfdt", 1, 0, tfdt.b),
		trunBox)
	moof := box("moof", fullBox("mfhd", 0, 0, be32(seq)), traf)
	// data_offset is relative to moof start: moof size + mdat header.
	// trun payload starts after: box hdr(8) + ver/flags(4) + sample_count(4).
	off := len(moof) - len(trunBox) + 8 + 4 + 4
	binary.BigEndian.PutUint32(moof[off:], uint32(len(moof)+8))

	total := 0
	for _, s := range samples {
		total += len(s.Data)
	}
	mdat := make([]byte, 0, 8+total)
	mdat = binary.BigEndian.AppendUint32(mdat, uint32(8+total))
	mdat = append(mdat, "mdat"...)
	for _, s := range samples {
		mdat = append(mdat, s.Data...)
	}
	return append(moof, mdat...)
}
