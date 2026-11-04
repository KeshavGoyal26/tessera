package stats

import (
	"bytes"
	"os"
	"runtime"
	"runtime/metrics"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/KeshavGoyal26/tessera/pkg/version"
)

// Global media socket meters. The rtc engine wraps its mux sockets and calls
// UDPIn.Add / UDPOut.Add per datagram (lock-free).
var (
	UDPIn  Meter
	UDPOut Meter
)

// NodeSampler computes process statistics (CPU, memory, GC, goroutines,
// socket rates). It is safe for concurrent use; samples closer than
// MinRateWindow reuse the previous CPU figure.
type NodeSampler struct {
	nodeID  string
	started time.Time

	mu       sync.Mutex
	lastAt   time.Time
	lastCPU  time.Duration
	cpuPct   float64
	samples  []metrics.Sample
	pageSize uint64
}

var metricNames = []string{
	"/sched/pauses/total/gc:seconds",
	"/cpu/classes/gc/total:cpu-seconds",
	"/cpu/classes/total:cpu-seconds",
	"/gc/cycles/total:gc-cycles",
	"/memory/classes/heap/objects:bytes",
	"/sched/goroutines:goroutines",
	"/memory/classes/total:bytes",
}

// NewNodeSampler creates a sampler for this process.
func NewNodeSampler(nodeID string) *NodeSampler {
	s := &NodeSampler{nodeID: nodeID, started: time.Now(), pageSize: uint64(os.Getpagesize())}
	s.samples = make([]metrics.Sample, len(metricNames))
	for i, n := range metricNames {
		s.samples[i].Name = n
	}
	return s
}

// Sample fills the process fields of NodeStats (not the room counters).
func (s *NodeSampler) Sample(now time.Time) NodeStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	ns := NodeStats{
		NodeID:    s.nodeID,
		Version:   version.Version,
		StartedAt: s.started,
		NumCPU:    runtime.GOMAXPROCS(0),
	}
	if cpu, ok := processCPU(); ok {
		if !s.lastAt.IsZero() && now.Sub(s.lastAt) >= MinRateWindow {
			s.cpuPct = float64(cpu-s.lastCPU) * 100 / float64(now.Sub(s.lastAt))
			s.lastAt, s.lastCPU = now, cpu
		} else if s.lastAt.IsZero() {
			s.lastAt, s.lastCPU = now, cpu
		}
		ns.CPUPercent = s.cpuPct
	}
	metrics.Read(s.samples)
	if h := s.samples[0]; h.Value.Kind() == metrics.KindFloat64Histogram {
		ns.GCPauseP99Ns = uint64(histQuantile(h.Value.Float64Histogram(), 0.99) * 1e9)
	}
	gcCPU, totalCPU := floatVal(s.samples[1]), floatVal(s.samples[2])
	if totalCPU > 0 {
		ns.GCCPUPercent = gcCPU * 100 / totalCPU
	}
	ns.NumGC = uintVal(s.samples[3])
	ns.HeapBytes = uintVal(s.samples[4])
	ns.Goroutines = int(uintVal(s.samples[5]))
	ns.RSSBytes = s.rss()
	if ns.RSSBytes == 0 {
		ns.RSSBytes = uintVal(s.samples[6])
	}
	ns.UDPPacketsInPerSec, ns.UDPBytesInPerSec = UDPIn.Rates(now)
	ns.UDPBytesInPerSec /= 8
	ns.UDPPacketsOutPerSec, ns.UDPBytesOutPerSec = UDPOut.Rates(now)
	ns.UDPBytesOutPerSec /= 8
	return ns
}

func floatVal(s metrics.Sample) float64 {
	if s.Value.Kind() == metrics.KindFloat64 {
		return s.Value.Float64()
	}
	return 0
}

func uintVal(s metrics.Sample) uint64 {
	if s.Value.Kind() == metrics.KindUint64 {
		return s.Value.Uint64()
	}
	return 0
}

func histQuantile(h *metrics.Float64Histogram, q float64) float64 {
	var total uint64
	for _, c := range h.Counts {
		total += c
	}
	if total == 0 {
		return 0
	}
	target := uint64(float64(total) * q)
	var acc uint64
	for i, c := range h.Counts {
		acc += c
		if acc >= target && c > 0 {
			// Upper bound of bucket i is Buckets[i+1].
			ub := h.Buckets[i+1]
			if ub > 1e9 { // +Inf
				return h.Buckets[i]
			}
			return ub
		}
	}
	return 0
}

func processCPU() (time.Duration, bool) {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0, false
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano()), true
}

func (s *NodeSampler) rss() uint64 {
	b, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	f := bytes.Fields(b)
	if len(f) < 2 {
		return 0
	}
	pages, err := strconv.ParseUint(string(f[1]), 10, 64)
	if err != nil {
		return 0
	}
	return pages * s.pageSize
}
