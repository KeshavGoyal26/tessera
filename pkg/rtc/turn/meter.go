package turn

import (
	"net"

	pturn "github.com/pion/turn/v5"

	"github.com/KeshavGoyal26/tessera/pkg/stats"
)

// Traffic of the TURN server, both legs: client side (the TURN listeners) and relay side
// (the sockets towards the peers). "in" is what the server receives, "out" what it sends;
// relayed media is counted once on each leg, as it crosses the machine's network twice.

type meteredPacketConn struct {
	net.PacketConn
	in, out *stats.Meter
}

func (c meteredPacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	n, a, err := c.PacketConn.ReadFrom(b)
	if n > 0 {
		c.in.Add(n)
	}
	return n, a, err
}

func (c meteredPacketConn) WriteTo(b []byte, a net.Addr) (int, error) {
	n, err := c.PacketConn.WriteTo(b, a)
	if n > 0 {
		c.out.Add(n)
	}
	return n, err
}

type meteredConn struct {
	net.Conn
	in, out *stats.Meter
}

func (c meteredConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		c.in.Add(n)
	}
	return n, err
}

func (c meteredConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if n > 0 {
		c.out.Add(n)
	}
	return n, err
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

// meteredGenerator meters the relay sockets the wrapped generator allocates.
type meteredGenerator struct {
	pturn.RelayAddressGenerator
	in, out *stats.Meter
}

func (g meteredGenerator) AllocatePacketConn(c pturn.AllocateListenerConfig) (net.PacketConn, net.Addr, error) {
	pc, a, err := g.RelayAddressGenerator.AllocatePacketConn(c)
	if err != nil {
		return nil, nil, err
	}
	return meteredPacketConn{PacketConn: pc, in: g.in, out: g.out}, a, nil
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
	return meteredConn{Conn: conn, in: g.in, out: g.out}, nil
}
