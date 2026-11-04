package e2e

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/webrtc/v4"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/client"
	"github.com/KeshavGoyal26/tessera/pkg/room"
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

func TestBWEDowngradeAndPause(t *testing.T) {
	s := startServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pubC := connect(t, s, "bwe", "pub")
	layers := newSimLayers(t, "cam")
	for i, pad := range []int{140, 480, 1080} { // ~120k, ~390k, ~880k bit/s
		layers[i].pad = pad
	}
	info, err := pubC.PublishSimulcastTrack(ctx, []webrtc.TrackLocal{layers[0].track, layers[1].track, layers[2].track},
		client.PublishOptions{Name: "cam", Width: 1280, Height: 720})
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
				for _, l := range layers {
					l.writeFrame()
				}
			}
		}
	}()
	t.Cleanup(func() { close(stop); wg.Wait() })

	raw, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	pc := &policedConn{UDPConn: raw, last: time.Now()}
	pc.rate.Store(600_000)
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
	go func() {
		b := make([]byte, 1500)
		for {
			if _, err := rt.Read(b); err != nil {
				return
			}
		}
	}()
	subStats := func() (int32, int64) {
		for _, p := range s.Manager.Snapshot().Rooms[0].Participants {
			if p.Identity == "sub" && len(p.Subscribed) > 0 {
				return p.Subscribed[0].CurrentSpatial, p.BandwidthEstimateBps
			}
		}
		return -1, 0
	}
	// 600 kbit/s cannot carry HIGH (~880 kbit/s): expect a lower layer within 5 s
	// of the estimator converging.
	deadline := time.Now().Add(15 * time.Second)
	var cur int32
	var est int64
	for time.Now().Before(deadline) {
		cur, est = subStats()
		if cur >= 0 && cur < 2 && est > 0 && est < 900_000 {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if cur < 0 || cur >= 2 {
		t.Fatalf("no downgrade under 600 kbit/s cap: layer %d estimate %d", cur, est)
	}
	t.Logf("downgraded to layer %d, estimate %d bit/s", cur, est)

	// 50 kbit/s fits not even LOW: the stream must be paused and signalled.
	pc.rate.Store(50_000)
	select {
	case st := <-states:
		if st != tesserav1.StreamState_STREAM_STATE_PAUSED {
			t.Fatalf("state %v", st)
		}
	case <-time.After(20 * time.Second):
		cur, est = subStats()
		t.Fatalf("no PAUSED stream state (layer %d estimate %d)", cur, est)
	}
}

func TestDynacast(t *testing.T) {
	old := room.DynacastGrace
	room.DynacastGrace = 500 * time.Millisecond
	t.Cleanup(func() { room.DynacastGrace = old })
	s := startServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pubC := connect(t, s, "dyn", "pub")
	updates := make(chan *tesserav1.SubscribedQualityUpdate, 16)
	pubC.SetCallbacks(client.Callbacks{OnSubscribedQuality: func(u *tesserav1.SubscribedQualityUpdate) { updates <- u }})
	layers := newSimLayers(t, "cam")
	info, err := pubC.PublishSimulcastTrack(ctx, []webrtc.TrackLocal{layers[0].track, layers[1].track, layers[2].track},
		client.PublishOptions{Name: "cam", Width: 1280, Height: 720})
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		tk := time.NewTicker(20 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tk.C:
				for _, l := range layers {
					l.writeFrame()
				}
			}
		}
	}()
	t.Cleanup(func() { close(stop); <-done })
	sub := connect(t, s, "dyn", "sub")
	if _, err := client.WaitTrack(ctx, sub, info.Sid); err != nil {
		t.Fatal(err)
	}
	if err := sub.UpdateTrackSettings(ctx, &tesserav1.UpdateTrackSettings{TrackSids: []string{info.Sid}, Quality: tesserav1.VideoQuality_VIDEO_QUALITY_LOW}); err != nil {
		t.Fatal(err)
	}
	enabled := func(u *tesserav1.SubscribedQualityUpdate) map[tesserav1.VideoQuality]bool {
		m := map[tesserav1.VideoQuality]bool{}
		for _, q := range u.SubscribedQualities {
			m[q.Quality] = q.Enabled
		}
		return m
	}
	select {
	case u := <-updates:
		m := enabled(u)
		if u.TrackSid != info.Sid || !m[tesserav1.VideoQuality_VIDEO_QUALITY_LOW] || m[tesserav1.VideoQuality_VIDEO_QUALITY_MEDIUM] || m[tesserav1.VideoQuality_VIDEO_QUALITY_HIGH] {
			t.Fatalf("update %v", u)
		}
	case <-ctx.Done():
		t.Fatal("no dynacast update")
	}
	if err := sub.UpdateTrackSettings(ctx, &tesserav1.UpdateTrackSettings{TrackSids: []string{info.Sid}, Quality: tesserav1.VideoQuality_VIDEO_QUALITY_HIGH}); err != nil {
		t.Fatal(err)
	}
	select {
	case u := <-updates:
		if !enabled(u)[tesserav1.VideoQuality_VIDEO_QUALITY_HIGH] {
			t.Fatalf("update %v", u)
		}
	case <-ctx.Done():
		t.Fatal("layer not re-enabled")
	}
}
