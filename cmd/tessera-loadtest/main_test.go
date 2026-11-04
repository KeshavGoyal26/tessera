package main

import (
	"encoding/binary"
	"testing"
	"time"
)

func TestPercentile(t *testing.T) {
	v := []float64{5, 1, 4, 2, 3}
	if pct(v, 0.5) != 3 || pct(v, 0.99) != 5 || pct(nil, 0.5) != 0 {
		t.Fatal(pct(v, 0.5), pct(v, 0.99))
	}
}

func TestStampAndLoss(t *testing.T) {
	b := make([]byte, 10)
	stamp(b)
	if b[0] != magic[0] || b[1] != magic[1] {
		t.Fatal("magic")
	}
	if d := time.Since(time.Unix(0, int64(binary.BigEndian.Uint64(b[2:])))); d < 0 || d > time.Second {
		t.Fatal(d)
	}
	c := &collector{}
	c.expected.Store(200)
	c.lost.Store(3)
	if lossPct(c) != 1.5 {
		t.Fatal(lossPct(c))
	}
}
