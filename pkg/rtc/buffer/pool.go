package buffer

import "sync"

// Pool hands out MaxPacketSize byte buffers. Use it for transient packet
// scratch space outside the ring (e.g. read buffers of short-lived readers).
var Pool = &packetPool{p: sync.Pool{New: func() any {
	b := make([]byte, MaxPacketSize)
	return &b
}}}

type packetPool struct{ p sync.Pool }

// Get returns a buffer of length MaxPacketSize.
func (p *packetPool) Get() *[]byte {
	b := p.p.Get().(*[]byte)
	*b = (*b)[:MaxPacketSize]
	return b
}

// Put returns a buffer obtained from Get.
func (p *packetPool) Put(b *[]byte) {
	if b == nil || cap(*b) < MaxPacketSize {
		return
	}
	p.p.Put(b)
}

// Slab pools keyed by slot count (video and audio rings use different sizes).
var slabPools sync.Map // int -> *sync.Pool

func getSlab(slots int) *[]byte {
	p, _ := slabPools.LoadOrStore(slots, &sync.Pool{New: func() any {
		b := make([]byte, slots*MaxPacketSize)
		return &b
	}})
	return p.(*sync.Pool).Get().(*[]byte)
}

func putSlab(b *[]byte) {
	if p, ok := slabPools.Load(len(*b) / MaxPacketSize); ok {
		p.(*sync.Pool).Put(b)
	}
}
