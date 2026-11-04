package stats

import (
	"math"
	"sync"
	"sync/atomic"
	"time"
)

// MinRateWindow is the minimum interval over which rates are computed.
const MinRateWindow = 900 * time.Millisecond

// Meter counts packets and bytes lock-free and derives per-second rates on
// demand. Add is safe for the packet path; Rates is for control goroutines.
// The zero value is ready to use.
type Meter struct {
	packets atomic.Uint64
	bytes   atomic.Uint64

	mu       sync.Mutex
	lastAt   int64
	lastPkts uint64
	lastByte uint64
	pps, bps float64
}

// Add records one packet of n bytes. No allocation, no lock.
func (m *Meter) Add(n int) {
	m.packets.Add(1)
	m.bytes.Add(uint64(n))
}

// AddN records pkts packets totalling n bytes.
func (m *Meter) AddN(pkts, n int) {
	m.packets.Add(uint64(pkts))
	m.bytes.Add(uint64(n))
}

// Packets returns the cumulative packet count.
func (m *Meter) Packets() uint64 { return m.packets.Load() }

// Bytes returns the cumulative byte count.
func (m *Meter) Bytes() uint64 { return m.bytes.Load() }

// Rates returns packets/s and bits/s over the last window ending at now.
// Calls closer together than MinRateWindow return the cached values.
func (m *Meter) Rates(now time.Time) (pps, bps float64) {
	ns := now.UnixNano()
	m.mu.Lock()
	defer m.mu.Unlock()
	p, b := m.packets.Load(), m.bytes.Load()
	if m.lastAt == 0 {
		m.lastAt, m.lastPkts, m.lastByte = ns, p, b
		return 0, 0
	}
	dt := time.Duration(ns - m.lastAt)
	if dt >= MinRateWindow {
		sec := dt.Seconds()
		m.pps = float64(p-m.lastPkts) / sec
		m.bps = float64(b-m.lastByte) * 8 / sec
		m.lastAt, m.lastPkts, m.lastByte = ns, p, b
	}
	return m.pps, m.bps
}

// Counter is a lock-free monotonically increasing counter.
type Counter struct{ v atomic.Uint64 }

// Inc adds one.
func (c *Counter) Inc() { c.v.Add(1) }

// Add adds n.
func (c *Counter) Add(n uint64) { c.v.Add(n) }

// Load returns the value.
func (c *Counter) Load() uint64 { return c.v.Load() }

// Gauge is a lock-free float64 gauge.
type Gauge struct{ v atomic.Uint64 }

// Set stores f.
func (g *Gauge) Set(f float64) { g.v.Store(math.Float64bits(f)) }

// Load returns the value.
func (g *Gauge) Load() float64 { return math.Float64frombits(g.v.Load()) }

// LossTracker derives a loss percentage over a window from cumulative
// expected/lost counters (e.g. from a receive buffer or receiver reports).
type LossTracker struct {
	mu                   sync.Mutex
	lastAt               int64
	lastExpected, lastLo uint64
	pct                  float64
}

// Update feeds cumulative counters and returns the loss percent of the last window.
func (l *LossTracker) Update(now time.Time, expected, lost uint64) float64 {
	ns := now.UnixNano()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.lastAt == 0 || time.Duration(ns-l.lastAt) >= MinRateWindow {
		de, dl := expected-l.lastExpected, lost-l.lastLo
		if l.lastAt != 0 && expected >= l.lastExpected && lost >= l.lastLo {
			if de > 0 {
				l.pct = math.Min(100, float64(dl)*100/float64(de))
			} else {
				l.pct = 0
			}
		}
		l.lastAt, l.lastExpected, l.lastLo = ns, expected, lost
	}
	return l.pct
}
