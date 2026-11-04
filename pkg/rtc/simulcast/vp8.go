package simulcast

import "github.com/KeshavGoyal26/tessera/pkg/rtc/buffer"

// RewriteVP8 writes payload with its descriptor rewritten per t into dst and
// returns the new payload length. The picture id is always written in the
// 15-bit form. dst must have room for len(payload)+1 bytes.
func RewriteVP8(dst, payload []byte, d *buffer.VP8Descriptor, t *VP8Translation) int {
	if !d.PictureIDPresent || d.HeaderSize < 3 {
		return copy(dst, payload)
	}
	dst[0] = payload[0]
	dst[1] = payload[1]
	i := 2
	dst[i] = 0x80 | byte(t.PictureID>>8)&0x7f
	dst[i+1] = byte(t.PictureID)
	i += 2
	j := 3 // incoming offset after picture id
	if d.MBit {
		j = 4
	}
	if d.TL0PICIDXPresent {
		dst[i] = t.TL0PICIDX
		i++
		j++
	}
	if d.TIDPresent || d.KEYIDXPresent {
		b := payload[j]
		if d.KEYIDXPresent {
			b = b&0xe0 | t.KEYIDX&0x1f
		}
		dst[i] = b
		i++
		j++
	}
	return i + copy(dst[i:], payload[j:])
}
