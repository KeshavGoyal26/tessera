package bwe

import (
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/rtcp"
)

const (
	historySize   = 4096 // sent packets remembered (power of two)
	burstInterval = 5 * time.Millisecond
	trendWindow   = 20
)

type sentRecord struct {
	seq     uint16
	size    uint16
	sentAt  int64 // monotonic ns
	valid   bool
	cluster uint8 // probe cluster the packet was sent in (0 = none)
}

// probeCluster measures one probe: every packet sent on the connection while it
// runs (media and padding) is counted on the send side and, from TWCC feedback,
// on the receive side. The receive rate is what the path delivered.
type probeCluster struct {
	id                  uint8
	sentBytes, sentPkts int
	firstSent, lastSent int64
	ackBytes, ackPkts   int
	lostPkts            int
	firstArr, lastArr   int64
	firstAckSize        int
	finishedAt          time.Time // sending stopped; zero while sending
	done                bool
	result              int64 // estimate after the probe (0 = probe failed)
}

const (
	minProbeSpan   = 100 * time.Millisecond // shorter clusters say nothing reliable
	probeFeedbackT = time.Second            // give up waiting for the last feedback after this
)

// estimator is a send-side bandwidth estimator: a GCC-style trendline
// overuse detector on inter-group delay variation, an AIMD rate controller
// bounded by the acknowledged receive rate, and a loss-based bound.
type estimator struct {
	cfg Config

	sentMu sync.Mutex // OnPacketSent vs feedback (short critical sections)
	sent   [historySize]sentRecord
	// probing: the active cluster id (0 = none) is read on the hot path; the
	// cluster's counters are updated under sentMu.
	probeID atomic.Uint32
	probe   probeCluster

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
	cl := uint8(e.probeID.Load())
	e.sentMu.Lock()
	e.sent[seq%historySize] = sentRecord{seq: seq, size: uint16(min(size, math.MaxUint16)), sentAt: sentAt, valid: true, cluster: cl}
	if cl != 0 && cl == e.probe.id {
		pc := &e.probe
		if pc.sentPkts == 0 {
			pc.firstSent = sentAt
		}
		pc.sentPkts++
		pc.sentBytes += size
		pc.lastSent = sentAt
	}
	e.sentMu.Unlock()
}

// StartProbe begins a probe cluster: packets sent from now on are measured until
// FinishProbe. A running probe is replaced.
func (e *estimator) StartProbe() {
	e.sentMu.Lock()
	id := e.probe.id + 1
	if id == 0 {
		id = 1
	}
	e.probe = probeCluster{id: id}
	e.sentMu.Unlock()
	e.probeID.Store(uint32(id))
}

// FinishProbe ends sending for the current probe; the result follows from feedback.
func (e *estimator) FinishProbe(now time.Time) {
	e.probeID.Store(0)
	e.sentMu.Lock()
	if e.probe.id != 0 && e.probe.finishedAt.IsZero() {
		e.probe.finishedAt = now
	}
	e.sentMu.Unlock()
}

// ProbeResult reports the outcome of the last probe: done once it was evaluated,
// and the estimate it established (0 if it failed).
func (e *estimator) ProbeResult() (done bool, estimate int64) {
	e.sentMu.Lock()
	defer e.sentMu.Unlock()
	return e.probe.done, e.probe.result
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
			inProbe := rec.valid && rec.seq == seq && rec.cluster != 0 && rec.cluster == e.probe.id
			if sym == rtcp.TypeTCCPacketNotReceived {
				lost++
				if inProbe {
					e.probe.lostPkts++
				}
			} else if deltaIdx < len(fb.RecvDeltas) {
				arrival += fb.RecvDeltas[deltaIdx].Delta * 1000 // us -> ns
				deltaIdx++
				if rec.valid && rec.seq == seq {
					acks = append(acks, ack{sentAt: rec.sentAt, arrival: arrival, size: int(rec.size)})
					if inProbe {
						e.probe.addAck(arrival, int(rec.size))
					}
				}
			}
			seq++
		}
	}
	probeEst := e.evalProbeLocked(receivedAt)
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
	if probeEst > 0 && e.state != StateOveruse && float64(probeEst) > e.estimate {
		// The path delivered the probe: jump to what it carried. This is the only way
		// past the "1.5x acknowledged" growth bound, which otherwise keeps a
		// subscriber on a lower layer whose rate is far below the next one.
		e.estimate = math.Min(float64(probeEst), float64(e.cfg.MaxBitrate))
	}
}

func (pc *probeCluster) addAck(arrival int64, size int) {
	if pc.ackPkts == 0 || arrival < pc.firstArr {
		pc.firstArr, pc.firstAckSize = arrival, size
	}
	pc.lastArr = max(pc.lastArr, arrival)
	pc.ackBytes += size
	pc.ackPkts++
}

// evalProbeLocked evaluates a finished probe once its feedback is in (or late).
// Returns the estimate the probe supports, or 0. Caller holds sentMu.
func (e *estimator) evalProbeLocked(now time.Time) int64 {
	pc := &e.probe
	if pc.id == 0 || pc.done || pc.finishedAt.IsZero() {
		return 0
	}
	if pc.ackPkts+pc.lostPkts < pc.sentPkts*9/10 && now.Sub(pc.finishedAt) < probeFeedbackT {
		return 0 // feedback still on its way
	}
	pc.done = true
	sendSpan := time.Duration(pc.lastSent - pc.firstSent)
	recvSpan := time.Duration(pc.lastArr - pc.firstArr)
	if pc.ackPkts < 5 || sendSpan < minProbeSpan || recvSpan <= 0 {
		return 0
	}
	if float64(pc.lostPkts) > 0.05*float64(pc.sentPkts) {
		return 0 // the probe itself caused loss: no headroom
	}
	sendRate := float64(pc.sentBytes*8) / sendSpan.Seconds()
	recvRate := float64((pc.ackBytes-pc.firstAckSize)*8) / recvSpan.Seconds()
	// A path that cannot carry the probe delivers it slower than it was sent.
	if recvRate < 0.8*sendRate {
		return 0
	}
	pc.result = int64(0.95 * math.Min(sendRate, recvRate))
	return pc.result
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
		// Do not grow far beyond what the receiver actually acknowledges. The
		// bound only stops increases: an application-limited subscriber (audio
		// only, or all video paused) acknowledges little, and pulling the
		// estimate down to that would pause its video for good, because the
		// acknowledged rate can then never rise again (no probing yet).
		if limit := 1.5*e.ackRate + 100_000; e.ackRate > 0 && est > limit {
			est = math.Max(e.estimate, limit)
		}
	}
	e.lastIncr = now
	e.estimate = math.Min(math.Max(est, float64(e.cfg.MinBitrate)), float64(e.cfg.MaxBitrate))
}
