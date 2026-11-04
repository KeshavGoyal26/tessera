package hls

import (
	"sort"
	"time"

	"github.com/pion/rtp"
)

// Unwrapper extends 32-bit RTP timestamps to 64 bits.
type Unwrapper struct {
	init  bool
	last  uint32
	cycle int64
}

// Unwrap returns the extended timestamp. Small backwards steps (reordering)
// stay close to the previous value.
func (u *Unwrapper) Unwrap(ts uint32) int64 {
	if !u.init {
		u.init, u.last = true, ts
		return int64(ts)
	}
	d := int32(ts - u.last)
	prev := u.cycle<<32 | int64(u.last)
	v := prev + int64(d)
	if d > 0 {
		u.last = ts
		u.cycle = v >> 32
	}
	return v
}

// ReorderBuffer restores RTP sequence order with bounded delay. Packets that
// do not arrive within MaxDelay (or when more than MaxPackets are buffered)
// are skipped and counted as lost.
type ReorderBuffer struct {
	MaxDelay   time.Duration
	MaxPackets int

	started bool
	next    uint16
	buf     []held
	// Lost counts packets given up on; Late counts packets that arrived after being skipped.
	Lost, Late uint64
}

type held struct {
	pkt *rtp.Packet
	at  time.Time
}

// NewReorderBuffer returns a buffer with a 200 ms delay bound.
func NewReorderBuffer() *ReorderBuffer {
	return &ReorderBuffer{MaxDelay: 200 * time.Millisecond, MaxPackets: 128}
}

// Push inserts pkt and returns packets now releasable in order.
func (r *ReorderBuffer) Push(pkt *rtp.Packet, now time.Time) []*rtp.Packet {
	if !r.started {
		r.started, r.next = true, pkt.SequenceNumber
	}
	if int16(pkt.SequenceNumber-r.next) < 0 {
		r.Late++
		return nil
	}
	i := sort.Search(len(r.buf), func(i int) bool { return int16(r.buf[i].pkt.SequenceNumber-pkt.SequenceNumber) >= 0 })
	if i < len(r.buf) && r.buf[i].pkt.SequenceNumber == pkt.SequenceNumber {
		return nil // duplicate
	}
	r.buf = append(r.buf, held{})
	copy(r.buf[i+1:], r.buf[i:])
	r.buf[i] = held{pkt, now}
	return r.drain(now)
}

// Poll releases packets whose wait time expired.
func (r *ReorderBuffer) Poll(now time.Time) []*rtp.Packet { return r.drain(now) }

func (r *ReorderBuffer) drain(now time.Time) []*rtp.Packet {
	var out []*rtp.Packet
	for len(r.buf) > 0 {
		h := r.buf[0]
		if h.pkt.SequenceNumber != r.next {
			if now.Sub(h.at) < r.MaxDelay && len(r.buf) <= r.MaxPackets {
				break
			}
			r.Lost += uint64(h.pkt.SequenceNumber - r.next)
		}
		out = append(out, h.pkt)
		r.next = h.pkt.SequenceNumber + 1
		r.buf = r.buf[1:]
	}
	return out
}
