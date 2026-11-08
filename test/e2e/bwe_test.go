package e2e

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/ice/v4"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/client"
)

// policedConn drops inbound datagrams above rate (token bucket), emulating
// a constrained downlink.
type policedConn struct {
	*net.UDPConn
	mu     sync.Mutex
	rate   atomic.Int64 // bits per second
	tokens float64
	last   time.Time
}

func (c *policedConn) ReadFrom(b []byte) (int, net.Addr, error) {
	for {
		n, a, err := c.UDPConn.ReadFrom(b)
		if err != nil || n < 200 { // never drop small control packets (STUN, DTLS, RTCP)
			return n, a, err
		}
		c.mu.Lock()
		now := time.Now()
		rate := float64(c.rate.Load())
		c.tokens += now.Sub(c.last).Seconds() * rate
		c.last = now
		if burst := rate / 10; c.tokens > burst {
			c.tokens = burst
		}
		ok := c.tokens >= float64(n*8)
		if ok {
			c.tokens -= float64(n * 8)
		}
		c.mu.Unlock()
		if ok {
			return n, a, err
		}
	}
}

// A subscriber whose downlink cannot carry the video stream gets it paused (and
// told so); when the link recovers the stream resumes. One stream per track: there
// is no lower layer to fall back to (ADR-0012).
func TestBWEPauseAndResume(t *testing.T) {
	s := startServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pubC := connect(t, s, "bwe", "pub")
	v := newVideoStream(t, "cam", 550) // ~450 kbit/s, about 360p
	info, err := pubC.PublishTrack(ctx, v.track, client.PublishOptions{Name: "cam", Width: 640, Height: 360})
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		tk := time.NewTicker(20 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tk.C:
				v.writeFrame()
			}
		}
	}()
	t.Cleanup(func() { close(stop); wg.Wait() })

	raw, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	pc := &policedConn{UDPConn: raw, last: time.Now()}
	pc.rate.Store(5_000_000)
	se := client.LoopbackSettingEngine()
	mux := ice.NewUDPMuxDefault(ice.UDPMuxParams{UDPConn: pc})
	t.Cleanup(func() { _ = mux.Close() })
	se.SetICEUDPMux(mux)
	sub := client.New(client.Options{AutoSubscribe: true, SettingEngine: se, Logger: testLogger()})
	states := make(chan tesserav1.StreamState, 16)
	sub.SetCallbacks(client.Callbacks{OnStreamStateUpdate: func(u *tesserav1.StreamStateUpdate) {
		for _, st := range u.StreamStates {
			states <- st.State
		}
	}})
	if err := sub.Connect(ctx, s.URL, token(t, "bwe", "sub")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sub.Disconnect(context.Background()) })
	rt, err := client.WaitTrack(ctx, sub, info.Sid)
	if err != nil {
		t.Fatal(err)
	}
	var received atomic.Int64
	go func() {
		b := make([]byte, 1500)
		for {
			if _, err := rt.Read(b); err != nil {
				return
			}
			received.Add(1)
		}
	}()
	estimate := func() int64 {
		for _, p := range s.Manager.Snapshot().Rooms[0].Participants {
			if p.Identity == "sub" {
				return p.BandwidthEstimateBps
			}
		}
		return 0
	}
	waitState := func(want tesserav1.StreamState, within time.Duration) {
		t.Helper()
		deadline := time.After(within)
		for {
			select {
			case st := <-states:
				if st == want {
					return
				}
			case <-deadline:
				t.Fatalf("no %v stream state within %v (estimate %d)", want, within, estimate())
			}
		}
	}

	// 200 kbit/s cannot carry the 450 kbit/s stream: it must be paused and signalled.
	pc.rate.Store(200_000)
	waitState(tesserav1.StreamState_STREAM_STATE_PAUSED, 30*time.Second)
	// The link recovers: the stream resumes and media flows again.
	pc.rate.Store(5_000_000)
	waitState(tesserav1.StreamState_STREAM_STATE_ACTIVE, 40*time.Second)
	before := received.Load()
	time.Sleep(2 * time.Second)
	if received.Load()-before < 50 {
		t.Fatalf("resumed but only %d packets in 2 s", received.Load()-before)
	}
}
