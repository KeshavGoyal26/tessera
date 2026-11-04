package bwe

import (
	"math"
	"sync"
	"time"

	"github.com/pion/rtcp"
)

const (
	historySize   = 4096 // sent packets remembered (power of two)
	burstInterval = 5 * time.Millisecond
	trendWindow   = 20
)

type sentRecord struct {
	seq    uint16
	size   uint16
	sentAt int64 // monotonic ns
	valid  bool
}

// estimator is a send-side bandwidth estimator: a GCC-style trendline
// overuse detector on inter-group delay variation, an AIMD rate controller
// bounded by the acknowledged receive rate, and a loss-based bound.
type estimator struct {
	cfg Config

	sentMu sync.Mutex // OnPacketSent vs feedback (short critical sections)
	sent   [historySize]sentRecord

	mu        sync.Mutex
	estimate  float64
	state     CongestionState
	rtt       time.Duration
	lastIncr  time.Time
	lastDecr  time.Time
	threshold float64
	// trendline state
	groups        []group
	accDelay      float64
	smoothedDelay float64
	trend         []trendPoint
	firstArrival  int64
	lossFrac      float64
	ackRate       float64
	feedbacks     uint64
}

type group struct {
	firstSent, lastSent int64
	lastArrival         int64
	size                int
}

type trendPoint struct{ x, y float64 }

// NewEstimator returns the TWCC estimator.
func NewEstimator(cfg Config) Estimator {
	if cfg.MinBitrate == 0 {
		cfg.MinBitrate = 50_000
	}
	if cfg.MaxBitrate == 0 {
		cfg.MaxBitrate = 20_000_000
	}
	start := float64(cfg.StartBitrate)
	if start == 0 {
		start = 1_500_000
	}
	return &estimator{cfg: cfg, estimate: start, threshold: 12.5}
}

// OnPacketSent implements Estimator. No allocation.
func (e *estimator) OnPacketSent(seq uint16, size int, sentAt int64, _ bool) {
	e.sentMu.Lock()
	e.sent[seq%historySize] = sentRecord{seq: seq, size: uint16(min(size, math.MaxUint16)), sentAt: sentAt, valid: true}
	e.sentMu.Unlock()
}

// OnRTT implements Estimator.
func (e *estimator) OnRTT(rtt time.Duration) {
	e.mu.Lock()
	e.rtt = rtt
	e.mu.Unlock()
}

// Estimate implements Estimator.
func (e *estimator) Estimate() int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return int64(e.estimate)
}

// State implements Estimator.
func (e *estimator) State() CongestionState {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state
}

// Feedbacks returns the number of feedback packets processed.
func (e *estimator) Feedbacks() uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.feedbacks
}

type ack struct {
	sentAt, arrival int64
	size            int
}

// OnTransportFeedback implements Estimator.
func (e *estimator) OnTransportFeedback(fb *rtcp.TransportLayerCC, receivedAt time.Time) {
	// Decode arrival times.
	acks := make([]ack, 0, fb.PacketStatusCount)
	var lost, total int
	refTime := int64(fb.ReferenceTime) * 64e6
	arrival := refTime
	seq := fb.BaseSequenceNumber
	deltaIdx := 0
	e.sentMu.Lock()
	for _, chunk := range fb.PacketChunks {
		var symbols []uint16
		switch c := chunk.(type) {
		case *rtcp.RunLengthChunk:
			for range c.RunLength {
				symbols = append(symbols, c.PacketStatusSymbol)
			}
		case *rtcp.StatusVectorChunk:
			symbols = c.SymbolList
		}
		for _, sym := range symbols {
			if total >= int(fb.PacketStatusCount) {
				break
			}
			total++
			rec := e.sent[seq%historySize]
			if sym == rtcp.TypeTCCPacketNotReceived {
				lost++
			} else if deltaIdx < len(fb.RecvDeltas) {
				arrival += fb.RecvDeltas[deltaIdx].Delta * 1000 // us -> ns
				deltaIdx++
				if rec.valid && rec.seq == seq {
					acks = append(acks, ack{sentAt: rec.sentAt, arrival: arrival, size: int(rec.size)})
				}
			}
			seq++
		}
	}
	e.sentMu.Unlock()

	e.mu.Lock()
	defer e.mu.Unlock()
	e.feedbacks++
	if total > 0 {
		f := float64(lost) / float64(total)
		e.lossFrac = 0.7*e.lossFrac + 0.3*f
	}
	if len(acks) >= 2 {
		var bytes int
		for _, a := range acks {
			bytes += a.size
		}
		span := float64(acks[len(acks)-1].arrival-acks[0].arrival) / 1e9
		if span > 0.02 {
			rate := float64(bytes*8) / span
			if e.ackRate == 0 {
				e.ackRate = rate
			} else {
				e.ackRate = 0.8*e.ackRate + 0.2*rate
			}
		}
	}
	for _, a := range acks {
		e.addAck(a)
	}
	e.update(receivedAt)
}

// addAck groups packets into 5 ms send bursts and feeds inter-group delay
// variation into the trendline filter.
func (e *estimator) addAck(a ack) {
	n := len(e.groups)
	if n > 0 && a.sentAt-e.groups[n-1].firstSent < int64(burstInterval) && a.sentAt >= e.groups[n-1].firstSent {
		g := &e.groups[n-1]
		g.lastSent = max(g.lastSent, a.sentAt)
		g.lastArrival = max(g.lastArrival, a.arrival)
		g.size += a.size
		return
	}
	if n > 0 {
		prev := e.groups[n-1]
		if n > 1 {
			pp := e.groups[n-2]
			sendDelta := float64(prev.lastSent-pp.lastSent) / 1e6
			arrDelta := float64(prev.lastArrival-pp.lastArrival) / 1e6
			if sendDelta >= 0 {
				e.accDelay += arrDelta - sendDelta
				e.smoothedDelay = 0.9*e.smoothedDelay + 0.1*e.accDelay
				if e.firstArrival == 0 {
					e.firstArrival = prev.lastArrival
				}
				e.trend = append(e.trend, trendPoint{x: float64(prev.lastArrival-e.firstArrival) / 1e6, y: e.smoothedDelay})
				if len(e.trend) > trendWindow {
					e.trend = e.trend[1:]
				}
			}
		}
		if n > 2 {
			e.groups = e.groups[n-2:]
		}
	}
	e.groups = append(e.groups, group{firstSent: a.sentAt, lastSent: a.sentAt, lastArrival: a.arrival, size: a.size})
}

func (e *estimator) slope() float64 {
	n := float64(len(e.trend))
	if n < 5 {
		return 0
	}
	var sx, sy float64
	for _, p := range e.trend {
		sx += p.x
		sy += p.y
	}
	mx, my := sx/n, sy/n
	var num, den float64
	for _, p := range e.trend {
		num += (p.x - mx) * (p.y - my)
		den += (p.x - mx) * (p.x - mx)
	}
	if den == 0 {
		return 0
	}
	return num / den
}

func (e *estimator) update(now time.Time) {
	// Delay-based detector: modified trend compared with an adaptive threshold.
	modified := e.slope() * float64(len(e.trend)) * 4
	switch {
	case modified > e.threshold:
		e.state = StateOveruse
	case modified < -e.threshold:
		e.state = StateUnderuse
	default:
		e.state = StateNormal
	}
	k := 0.039
	if math.Abs(modified) < e.threshold {
		k = 0.0087
	}
	e.threshold += k * (math.Abs(modified) - e.threshold) * 0.05
	e.threshold = math.Min(math.Max(e.threshold, 6), 600)

	est := e.estimate
	switch {
	case e.state == StateOveruse || e.lossFrac > 0.1:
		if now.Sub(e.lastDecr) > 200*time.Millisecond {
			base := est
			if e.ackRate > 0 && e.ackRate < base {
				base = e.ackRate
			}
			factor := 0.85
			if e.lossFrac > 0.1 {
				factor = math.Min(factor, 1-0.5*e.lossFrac)
			}
			est = base * factor
			e.lastDecr = now
			e.state = StateOveruse
		}
	case e.state == StateNormal && e.lossFrac < 0.02:
		if !e.lastIncr.IsZero() {
			dt := now.Sub(e.lastIncr).Seconds()
			est *= math.Pow(1.08, math.Min(dt, 1))
		}
		// Never run far ahead of what the receiver actually acknowledges.
		if e.ackRate > 0 && est > 1.5*e.ackRate+100_000 {
			est = 1.5*e.ackRate + 100_000
		}
	}
	e.lastIncr = now
	e.estimate = math.Min(math.Max(est, float64(e.cfg.MinBitrate)), float64(e.cfg.MaxBitrate))
}
