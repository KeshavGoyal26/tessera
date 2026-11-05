package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLinkLossAndDelay(t *testing.T) {
	var mu sync.RWMutex
	p := Profile{LossPct: 10, Burst: 1, Delay: 50 * time.Millisecond}
	l := newLink(&p, &mu)
	var got atomic.Int64
	start := time.Now()
	var first atomic.Int64
	for range 2000 {
		l.send(make([]byte, 100), func([]byte) {
			first.CompareAndSwap(0, int64(time.Since(start)))
			got.Add(1)
		})
	}
	time.Sleep(200 * time.Millisecond)
	c := l.stats()
	if c.Lost < 100 || c.Lost > 300 {
		t.Fatalf("lost %d of 2000 at 10%%", c.Lost)
	}
	if d := time.Duration(first.Load()); d < 45*time.Millisecond {
		t.Fatalf("first delivery after %v, want >= 50ms", d)
	}
	if int(got.Load()) != int(c.Passed) {
		t.Fatalf("delivered %d, passed %d", got.Load(), c.Passed)
	}
}
