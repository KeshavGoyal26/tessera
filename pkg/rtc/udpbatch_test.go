package rtc

import (
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestBatchWriterDeliversInOrder(t *testing.T) {
	t.Setenv("TESSERA_UDP_NOBATCH", "")
	rx, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rx.Close() }()
	tx, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	c := newCountingConn(tx)
	if c.bw == nil {
		t.Fatal("batch writer not enabled for an IPv4 socket")
	}
	to := rx.LocalAddr().(*net.UDPAddr).AddrPort()
	const total = 5000
	go func() {
		for i := range total {
			b := []byte{byte(i >> 8), byte(i), 1, 2, 3}
			if _, err := c.WriteToAddrPort(b, netip.AddrPortFrom(to.Addr(), to.Port())); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	_ = rx.SetReadBuffer(4 << 20)
	buf := make([]byte, 64)
	next := 0
	for next < total {
		_ = rx.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, err := rx.Read(buf)
		if err != nil {
			t.Fatalf("after %d datagrams: %v", next, err)
		}
		if got := int(buf[0])<<8 | int(buf[1]); n != 5 || got != next {
			t.Fatalf("datagram %d arrived as %d (len %d)", next, got, n)
		}
		next++
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	// Writes after Close fall back to the direct path and fail without hanging.
	_, _ = c.WriteToAddrPort([]byte{1}, to)
}

func BenchmarkUDPWrite(b *testing.B) {
	for _, mode := range []string{"direct", "batch"} {
		b.Run(mode, func(b *testing.B) {
			if mode == "direct" {
				b.Setenv("TESSERA_UDP_NOBATCH", "1")
			} else {
				b.Setenv("TESSERA_UDP_NOBATCH", "")
			}
			rx, _ := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			defer func() { _ = rx.Close() }()
			go func() {
				buf := make([]byte, 2048)
				for {
					if _, err := rx.Read(buf); err != nil {
						return
					}
				}
			}()
			tx, _ := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			c := newCountingConn(tx)
			defer func() { _ = c.Close() }()
			to := rx.LocalAddr().(*net.UDPAddr).AddrPort()
			pkt := make([]byte, 1000)
			b.SetBytes(int64(len(pkt)))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				_, _ = c.WriteToAddrPort(pkt, to)
			}
		})
	}
}
