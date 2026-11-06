package buffer

// NACK generation parameters (ARCHITECTURE.md 4.2). Retries follow the RTT: a
// retransmission cannot arrive sooner than one round trip, so asking again
// earlier only duplicates requests, and giving up earlier (the old fixed 20 ms
// and 300 ms) left a single recovery attempt on links above ~300 ms RTT.
const (
	nackMaxTries   = 10
	nackMinRetry   = 20e6   // ns: retry interval floor (low-RTT links)
	nackMinGiveUp  = 300e6  // ns after the gap was detected
	nackMaxGiveUp  = 2000e6 // ns: past this a keyframe is the better recovery
	nackMaxPending = 256
)

// nackTiming returns the retry interval and give-up age for a round-trip time.
func nackTiming(rtt int64) (retry, giveUp int64) {
	retry = max(nackMinRetry, rtt+rtt/10)
	giveUp = min(max(nackMinGiveUp, 4*rtt), nackMaxGiveUp)
	return retry, giveUp
}

type nackEntry struct {
	extSN    uint64
	detected int64
	lastSent int64
	tries    uint8
}

// nackQueue holds missing sequence numbers, writer goroutine only. It is a
// fixed-size array; when full, the oldest entries are dropped.
type nackQueue struct {
	entries [nackMaxPending]nackEntry
	n       int
}

func (q *nackQueue) addRange(from, to uint64, now int64) {
	if to-from > nackMaxPending {
		from = to - nackMaxPending
	}
	for sn := from; sn < to; sn++ {
		if q.n == nackMaxPending {
			copy(q.entries[:], q.entries[1:])
			q.n--
		}
		q.entries[q.n] = nackEntry{extSN: sn, detected: now}
		q.n++
	}
}

func (q *nackQueue) remove(extSN uint64) {
	for i := 0; i < q.n; i++ {
		if q.entries[i].extSN == extSN {
			copy(q.entries[i:q.n], q.entries[i+1:q.n])
			q.n--
			return
		}
	}
}

func (q *nackQueue) collect(dst []uint16, now int64, highest, slots uint64, rtt int64) []uint16 {
	retry, giveUp := nackTiming(rtt)
	j := 0
	for i := 0; i < q.n; i++ {
		e := q.entries[i]
		if e.tries >= nackMaxTries || now-e.detected > giveUp || e.extSN+slots <= highest {
			continue // drop
		}
		if e.tries == 0 || now-e.lastSent >= retry {
			e.lastSent = now
			e.tries++
			dst = append(dst, uint16(e.extSN))
		}
		q.entries[j] = e
		j++
	}
	q.n = j
	return dst
}
