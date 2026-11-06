package simulcast

import (
	"testing"

	"github.com/pion/rtp"

	"github.com/KeshavGoyal26/tessera/pkg/rtc/buffer"
	"github.com/KeshavGoyal26/tessera/pkg/rtc/types"
)

// RewriteVP8 sees publisher-controlled payloads: it must never panic or write past
// the destination the DownTrack provides (MaxPacketSize+8).
func FuzzRewriteVP8(f *testing.F) {
	f.Add([]byte{0x90, 0xe0, 0x80, 0x07, 0x01, 0x40, 0x00, 0xaa}, uint16(5), uint8(1), uint8(2))
	f.Add([]byte{0x90, 0x80, 0x07, 0x00}, uint16(0x7fff), uint8(0), uint8(31))
	f.Fuzz(func(t *testing.T, payload []byte, pid uint16, tl0, key uint8) {
		if len(payload) > buffer.MaxPacketSize {
			return
		}
		var d buffer.VP8Descriptor
		if buffer.ParseVP8(payload, &d) != nil {
			return
		}
		dst := make([]byte, buffer.MaxPacketSize+8)
		n := RewriteVP8(dst, payload, &d, &VP8Translation{PictureID: pid & 0x7fff, TL0PICIDX: tl0, KEYIDX: key & 0x1f})
		if n < 0 || n > len(dst) || n > len(payload)+1 {
			t.Fatalf("rewrote %d bytes from %d", n, len(payload))
		}
	})
}

// The forwarder must survive any packet order, gaps, layer flags, probes and NACKs
// without panicking, and never emit the same outgoing sequence number twice for
// different incoming packets.
func FuzzForwarderSequence(f *testing.F) {
	f.Add([]byte{0x01, 0x12, 0x83, 0x24, 0x05, 0xf6, 0x17, 0x08, 0x99, 0x2a})
	f.Fuzz(func(t *testing.T, ops []byte) {
		if len(ops) > 400 {
			return
		}
		fw := NewForwarder("video/VP8")
		fw.SetAvailableLayers(0)
		buf := buffer.New(buffer.Config{Slots: 256, MimeType: "video/VP8"})
		sn, pid, tl0 := uint16(1000), uint16(7), uint8(0)
		out := map[uint16]uint16{} // outgoing SN -> incoming SN
		for i, op := range ops {
			switch op >> 6 {
			case 0, 1: // a packet: low bits pick key / tid / layer sync / SN jump
				key := op&0x01 != 0 && op&0x20 != 0
				tid := (op >> 1) & 0x03
				y := op&0x08 != 0
				jump := int16(op>>4&0x03) - 1 // -1, 0, +1, +2: reorder, duplicate, in order, gap
				sn += uint16(jump)
				first := byte(0x01)
				if key {
					first = 0x00
				}
				tidByte := tid << 6
				if y {
					tidByte |= 0x20
				}
				p := rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: sn, Timestamp: uint32(i) * 3000, SSRC: 1},
					Payload: []byte{0x90, 0xe0, 0x80 | byte(pid>>8), byte(pid), tl0, tidByte, first, 0xaa}}
				raw, _ := p.Marshal()
				e, err := buf.Write(raw, int64(i)*33e6)
				if err != nil || e == nil {
					continue
				}
				pid = (pid + 1) & 0x7fff
				tl0++
				tp, err := fw.GetTranslation(e, 0)
				if err != nil || tp.Drop {
					continue
				}
				if prev, ok := out[tp.SequenceNumber]; ok && prev != e.SequenceNumber {
					t.Fatalf("op %d: outgoing sn %d reused: in %d and in %d", i, tp.SequenceNumber, prev, e.SequenceNumber)
				}
				out[tp.SequenceNumber] = e.SequenceNumber
			case 2: // a probe, or a temporal target change
				if op&0x01 != 0 {
					if s, _, err := fw.PaddingParams(int(op>>1&0x07) + 1); err == nil {
						for k := range int(op>>1&0x07) + 1 {
							out[s+uint16(k)] = 0xffff // reserved for padding
						}
					}
				} else {
					fw.SetMaxLayer(types.VideoLayer{Spatial: 0, Temporal: int32(op >> 1 & 0x03)})
				}
			case 3: // a NACK for a recent outgoing SN
				_, _, _ = fw.TranslateNACK(uint16(1000 + int(op&0x3f)))
				_, _ = fw.RetransmitInfo(uint16(1000+int(op&0x3f)), int64(i)*1e6, 0)
			}
		}
	})
}
