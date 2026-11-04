package buffer

// NACK generation parameters (ARCHITECTURE.md 4.2).
const (
	nackMaxTries   = 5
	nackInterval   = 20e6  // ns between retries of the same SN
	nackGiveUp     = 300e6 // ns after the gap was detected
	nackMaxPending = 256
)

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

func (q *nackQueue) collect(dst []uint16, now int64, highest, slots uint64) []uint16 {
	j := 0
	for i := 0; i < q.n; i++ {
		e := q.entries[i]
		if e.tries >= nackMaxTries || now-e.detected > nackGiveUp || e.extSN+slots <= highest {
			continue // drop
		}
		if e.lastSent == 0 || now-e.lastSent >= nackInterval {
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
