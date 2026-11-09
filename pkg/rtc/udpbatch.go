package rtc

import (
	"errors"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"

	"golang.org/x/net/ipv4"

	"github.com/KeshavGoyal26/tessera/pkg/stats"
)

// Batched UDP egress.
//
// Pion's ICE mux writes every datagram with its own sendto(2); at SFU fan-out
// rates that syscall is the largest single CPU cost (about half of the server's
// CPU under a 50-subscriber load, docs/perf/COMPARISON.md). batchWriter queues
// outgoing datagrams and a single goroutine flushes whatever is queued with one
// sendmmsg(2) (ipv4.PacketConn.WriteBatch). Under light load a batch holds one
// datagram, so latency stays one goroutine hand-off; under heavy load the
// batches grow by themselves. Per-destination order is preserved (one FIFO).

const (
	batchSlots   = 512  // queued datagrams before producers wait
	batchMaxSend = 64   // datagrams per sendmmsg
	batchMaxSize = 1500 // larger datagrams are written directly
)

type batchSlot struct {
	buf  [batchMaxSize]byte
	n    int
	ip   [4]byte
	port uint16
}

type batchWriter struct {
	conn *net.UDPConn
	pc   *ipv4.PacketConn

	mu       sync.Mutex
	notEmpty *sync.Cond
	notFull  *sync.Cond
	slots    [batchSlots]batchSlot
	head     int // next slot to send
	count    int // queued slots
	closed   bool
	done     chan struct{}

	// writer-goroutine state (no locking needed)
	msgs  [batchMaxSend]ipv4.Message
	addrs [batchMaxSend]net.UDPAddr
	bufs  [batchMaxSend][1][]byte
}

// newBatchWriter returns nil when batching does not apply (IPv6 socket, disabled).
func newBatchWriter(c *net.UDPConn) *batchWriter {
	if os.Getenv("TESSERA_UDP_NOBATCH") != "" {
		return nil
	}
	la, ok := c.LocalAddr().(*net.UDPAddr)
	if !ok || la.IP.To4() == nil {
		return nil
	}
	w := &batchWriter{conn: c, pc: ipv4.NewPacketConn(c), done: make(chan struct{})}
	w.notEmpty = sync.NewCond(&w.mu)
	w.notFull = sync.NewCond(&w.mu)
	for i := range w.msgs {
		w.addrs[i].IP = make(net.IP, 4)
		w.msgs[i].Buffers = w.bufs[i][:]
		w.msgs[i].Addr = &w.addrs[i]
	}
	go w.run()
	return w
}

// write queues one datagram. ok is false when the caller must write directly.
func (w *batchWriter) write(b []byte, to netip.AddrPort) (n int, ok bool) {
	if len(b) > batchMaxSize || !to.Addr().Is4() {
		return 0, false
	}
	w.mu.Lock()
	for w.count == batchSlots && !w.closed {
		w.notFull.Wait()
	}
	if w.closed {
		w.mu.Unlock()
		return 0, false
	}
	s := &w.slots[(w.head+w.count)%batchSlots]
	s.n = copy(s.buf[:], b)
	s.ip = to.Addr().As4()
	s.port = to.Port()
	w.count++
	if w.count == 1 {
		w.notEmpty.Signal()
	}
	w.mu.Unlock()
	return len(b), true
}

func (w *batchWriter) close() {
	w.mu.Lock()
	if !w.closed {
		w.closed = true
		w.notEmpty.Broadcast()
		w.notFull.Broadcast()
	}
	w.mu.Unlock()
	<-w.done
}

func (w *batchWriter) run() {
	defer close(w.done)
	for {
		w.mu.Lock()
		for w.count == 0 && !w.closed {
			w.notEmpty.Wait()
		}
		if w.closed {
			w.mu.Unlock()
			return
		}
		n := min(w.count, batchMaxSend)
		head := w.head
		w.mu.Unlock()

		// The slots [head, head+n) are owned by this goroutine until released.
		bytes := 0
		for i := range n {
			s := &w.slots[(head+i)%batchSlots]
			w.bufs[i][0] = s.buf[:s.n]
			copy(w.addrs[i].IP, s.ip[:])
			w.addrs[i].Port = int(s.port)
			bytes += s.n
		}
		for sent := 0; sent < n; {
			m, err := w.pc.WriteBatch(w.msgs[sent:n], 0)
			if err != nil {
				if isClosedErr(err) {
					break
				}
				sent++ // drop the datagram that failed (like an ignored sendto error)
				continue
			}
			sent += m
		}
		stats.UDPOut.AddN(n, bytes) // n datagrams, not one

		w.mu.Lock()
		w.head = (w.head + n) % batchSlots
		w.count -= n
		w.notFull.Broadcast()
		w.mu.Unlock()
	}
}

func isClosedErr(err error) bool {
	return err != nil && (errors.Is(err, net.ErrClosed) || strings.Contains(err.Error(), "closed"))
}
