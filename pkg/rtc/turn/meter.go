package turn

import (
	"net"
	"sync"
	"sync/atomic"
	"time"

	pturn "github.com/pion/turn/v5"

	"github.com/KeshavGoyal26/tessera/pkg/stats"
)

// Traffic of the TURN server, both legs: client side (the TURN listeners) and relay side
// (the sockets towards the peers). "in" is what the server receives, "out" what it sends;
// relayed media is counted once on each leg, as it crosses the machine's network twice.

// activity tracks the relay sockets (one per allocation) and when each last carried
// traffic, so Stats can tell relays in use from allocations a client never used (a
// browser allocates on every TURN server it is given, ICE then picks one).
type activity struct {
	mu   sync.Mutex
	last map[*atomic.Int64]struct{}
}

func (a *activity) add() *atomic.Int64 {
	l := new(atomic.Int64)
	a.mu.Lock()
	if a.last == nil {
		a.last = map[*atomic.Int64]struct{}{}
	}
	a.last[l] = struct{}{}
	a.mu.Unlock()
	return l
}

func (a *activity) remove(l *atomic.Int64) {
	a.mu.Lock()
	delete(a.last, l)
	a.mu.Unlock()
}

// active counts the relays that carried traffic within window before now.
func (a *activity) active(now time.Time, window time.Duration) int {
	since := now.Add(-window).UnixNano()
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for l := range a.last {
		if l.Load() >= since {
			n++
		}
	}
	return n
}

// meteredPacketConn meters a socket; on the relay side it also records activity (last).
type meteredPacketConn struct {
	net.PacketConn
	in, out *stats.Meter
	last    *atomic.Int64 // nil on the client side
	done    func()
}

func (c meteredPacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	n, a, err := c.PacketConn.ReadFrom(b)
	if n > 0 {
		c.in.Add(n)
		c.touch()
	}
	return n, a, err
}

func (c meteredPacketConn) WriteTo(b []byte, a net.Addr) (int, error) {
	n, err := c.PacketConn.WriteTo(b, a)
	if n > 0 {
		c.out.Add(n)
		c.touch()
	}
	return n, err
}

func (c meteredPacketConn) touch() {
	if c.last != nil {
		c.last.Store(time.Now().UnixNano())
	}
}

func (c meteredPacketConn) Close() error {
	if c.done != nil {
		c.done()
	}
	return c.PacketConn.Close()
}

type meteredConn struct {
	net.Conn
	in, out *stats.Meter
	last    *atomic.Int64 // nil on the client side
	done    func()
}

func (c meteredConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		c.in.Add(n)
		c.touch()
	}
	return n, err
}

func (c meteredConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if n > 0 {
		c.out.Add(n)
		c.touch()
	}
	return n, err
}

func (c meteredConn) touch() {
	if c.last != nil {
		c.last.Store(time.Now().UnixNano())
	}
}

func (c meteredConn) Close() error {
	if c.done != nil {
		c.done()
	}
	return c.Conn.Close()
}

type meteredListener struct {
	net.Listener
	in, out *stats.Meter
}

func (l meteredListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return meteredConn{Conn: c, in: l.in, out: l.out}, nil
}

// meteredGenerator meters the relay sockets the wrapped generator allocates and tracks
// their activity.
type meteredGenerator struct {
	pturn.RelayAddressGenerator
	in, out *stats.Meter
	act     *activity
}

func (g meteredGenerator) AllocatePacketConn(c pturn.AllocateListenerConfig) (net.PacketConn, net.Addr, error) {
	pc, a, err := g.RelayAddressGenerator.AllocatePacketConn(c)
	if err != nil {
		return nil, nil, err
	}
	last := g.act.add()
	return meteredPacketConn{PacketConn: pc, in: g.in, out: g.out, last: last, done: func() { g.act.remove(last) }}, a, nil
}

func (g meteredGenerator) AllocateListener(c pturn.AllocateListenerConfig) (net.Listener, net.Addr, error) {
	ln, a, err := g.RelayAddressGenerator.AllocateListener(c)
	if err != nil {
		return nil, nil, err
	}
	return meteredListener{Listener: ln, in: g.in, out: g.out}, a, nil
}

func (g meteredGenerator) AllocateConn(c pturn.AllocateConnConfig) (net.Conn, error) {
	conn, err := g.RelayAddressGenerator.AllocateConn(c)
	if err != nil {
		return nil, err
	}
	last := g.act.add()
	return meteredConn{Conn: conn, in: g.in, out: g.out, last: last, done: func() { g.act.remove(last) }}, nil
}
