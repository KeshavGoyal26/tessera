package e2e

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/auth"
	"github.com/KeshavGoyal26/tessera/pkg/client"
)

// dropper drops the first arrival of every 10th video sequence number on
// the subscriber path, so the client's NACK generator asks for them.
type dropper struct {
	interceptor.NoOp
	mu      sync.Mutex
	seen    map[uint16]bool
	dropped map[uint16]bool
}

type dropperFactory struct{ d *dropper }

func (f dropperFactory) NewInterceptor(string) (interceptor.Interceptor, error) { return f.d, nil }

func (d *dropper) BindRemoteStream(info *interceptor.StreamInfo, r interceptor.RTPReader) interceptor.RTPReader {
	if info.MimeType != "video/VP8" {
		return r
	}
	return interceptor.RTPReaderFunc(func(b []byte, a interceptor.Attributes) (int, interceptor.Attributes, error) {
		for {
			n, attr, err := r.Read(b, a)
			if err != nil || n < 4 {
				return n, attr, err
			}
			sn := uint16(b[2])<<8 | uint16(b[3])
			d.mu.Lock()
			drop := sn%10 == 5 && !d.seen[sn]
			d.seen[sn] = true
			if drop {
				d.dropped[sn] = true
			}
			d.mu.Unlock()
			if !drop {
				return n, attr, err
			}
		}
	})
}

func publishAV(t *testing.T, ctx context.Context, c client.Client) (audio, video *tesserav1.TrackInfo) {
	t.Helper()
	id := c.LocalParticipant().Identity
	a, v := newSynth(t, "audio", id+"-a"), newSynth(t, "video", id+"-v")
	ai, err := c.PublishTrack(ctx, a.track, client.PublishOptions{Name: "mic"})
	if err != nil {
		t.Fatal(err)
	}
	vi, err := c.PublishTrack(ctx, v.track, client.PublishOptions{Name: "cam"})
	if err != nil {
		t.Fatal(err)
	}
	a.run()
	v.run()
	t.Cleanup(a.Stop)
	t.Cleanup(v.Stop)
	return ai, vi
}

func TestNACKRetransmission(t *testing.T) {
	s := startServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pubC := connect(t, s, "nack", "pub")
	_, vi := publishAV(t, ctx, pubC)

	d := &dropper{seen: map[uint16]bool{}, dropped: map[uint16]bool{}}
	// The shim drops after SRTP decryption; real loss happens before it, so
	// disable replay protection to accept the retransmitted copies.
	se := client.LoopbackSettingEngine()
	se.DisableSRTPReplayProtection(true)
	sub := client.New(client.Options{AutoSubscribe: true, SettingEngine: se, Logger: testLogger(),
		Interceptors: []interceptor.Factory{dropperFactory{d}}})
	if err := sub.Connect(ctx, s.URL, token(t, "nack", "sub")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sub.Disconnect(context.Background()) })
	rt, err := client.WaitTrack(ctx, sub, vi.Sid)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1500)
	got := map[uint16]uint32{} // SN -> payload counter
	var base int64 = -1
	for len(got) < 150 {
		n, err := rt.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		var p rtp.Packet
		if err := p.Unmarshal(buf[:n]); err != nil {
			t.Fatal(err)
		}
		cnt, ok := checkPayload(t, true, p.Payload)
		if !ok {
			t.Fatalf("corrupt payload %x", p.Payload)
		}
		// Munged SN and publisher counter advance together.
		if base == -1 {
			base = int64(cnt) - int64(p.SequenceNumber)
		} else if int64(cnt)-int64(p.SequenceNumber) != base {
			t.Fatalf("SN %d carries counter %d (offset changed)", p.SequenceNumber, cnt)
		}
		got[p.SequenceNumber] = cnt
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var recovered, dropped int
	maxSN := uint16(0)
	for sn := range got {
		if sn > maxSN {
			maxSN = sn
		}
	}
	for sn := range d.dropped {
		if sn+30 > maxSN { // too recent to have been recovered
			continue
		}
		dropped++
		if _, ok := got[sn]; ok {
			recovered++
		}
	}
	if dropped < 5 || recovered < dropped*8/10 {
		t.Fatalf("recovered %d of %d dropped packets", recovered, dropped)
	}
	snap := s.Manager.Snapshot()
	var retx uint64
	for _, p := range snap.Rooms[0].Participants {
		for _, st := range p.Subscribed {
			retx += st.Retransmits
		}
	}
	if retx == 0 {
		t.Fatal("no retransmissions in stats")
	}
}

func TestPLIThrottled(t *testing.T) {
	s := startServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var plis atomic.Int64
	pubC := client.New(client.Options{AutoSubscribe: true, SettingEngine: client.LoopbackSettingEngine(), Logger: testLogger(),
		OnPublisherRTCP: func(pkts []rtcp.Packet) {
			for _, p := range pkts {
				if _, ok := p.(*rtcp.PictureLossIndication); ok {
					plis.Add(1)
				}
			}
		}})
	if err := pubC.Connect(ctx, s.URL, token(t, "pli", "pub")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pubC.Disconnect(context.Background()) })
	_, vi := publishAV(t, ctx, pubC)
	var tracks []*client.RemoteTrack
	for _, id := range []string{"s1", "s2", "s3"} {
		c := connect(t, s, "pli", id)
		rt, err := client.WaitTrack(ctx, c, vi.Sid)
		if err != nil {
			t.Fatal(err)
		}
		tracks = append(tracks, rt)
		go func() {
			b := make([]byte, 1500)
			for {
				if _, err := rt.Read(b); err != nil {
					return
				}
			}
		}()
	}
	time.Sleep(1500 * time.Millisecond) // let join-time keyframe requests settle
	before := plis.Load()
	for range 5 {
		for _, rt := range tracks {
			if err := rt.RequestKeyFrame(); err != nil {
				t.Fatal(err)
			}
		}
	}
	time.Sleep(400 * time.Millisecond) // inside the 500 ms throttle window
	if got := plis.Load() - before; got != 1 {
		t.Fatalf("publisher saw %d PLIs for 15 requests, want exactly 1", got)
	}
}

func TestResumeKeepsTracks(t *testing.T) {
	s := startServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pubC := connect(t, s, "resume", "pub")
	_, vi := publishAV(t, ctx, pubC)
	sub := connect(t, s, "resume", "sub")
	var reconnected atomic.Bool
	sub.SetCallbacks(client.Callbacks{OnReconnected: func() { reconnected.Store(true) }})
	rt, err := client.WaitTrack(ctx, sub, vi.Sid)
	if err != nil {
		t.Fatal(err)
	}
	sid := sub.LocalParticipant().Sid
	client.DropConnection(sub)
	client.DropConnection(pubC)
	deadline := time.Now().Add(10 * time.Second)
	for !reconnected.Load() && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if !reconnected.Load() {
		t.Fatal("did not resume")
	}
	receive(t, rt, true, 20) // media still flows on the same track
	p, err := s.Manager.GetParticipant(ctx, "resume", "sub")
	if err != nil || p.Sid != sid {
		t.Fatalf("participant changed: %v %v", p, err)
	}
	pp, err := s.Manager.GetParticipant(ctx, "resume", "pub")
	if err != nil || len(pp.Tracks) != 2 || (pp.Tracks[0].Sid != vi.Sid && pp.Tracks[1].Sid != vi.Sid) {
		t.Fatalf("publisher tracks changed: %v %v", pp, err)
	}
}

func TestDuplicateIdentityKicksFirst(t *testing.T) {
	s := startServer(t)
	first := client.New(client.Options{AutoSubscribe: true, SettingEngine: client.LoopbackSettingEngine(), Logger: testLogger(), DisableResume: true})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	reason := make(chan tesserav1.DisconnectReason, 2)
	first.SetCallbacks(client.Callbacks{OnDisconnected: func(r tesserav1.DisconnectReason) { reason <- r }})
	if err := first.Connect(ctx, s.URL, token(t, "dup", "alice")); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Disconnect(context.Background()) }()
	connect(t, s, "dup", "alice")
	select {
	case r := <-reason:
		if r != tesserav1.DisconnectReason_DISCONNECT_REASON_DUPLICATE_IDENTITY {
			t.Fatalf("reason %v", r)
		}
	case <-ctx.Done():
		t.Fatal("first session not kicked")
	}
	ps, _ := s.Manager.ListParticipants(ctx, "dup")
	if len(ps) != 1 {
		t.Fatalf("participants %d", len(ps))
	}
}

func TestPermissions(t *testing.T) {
	s := startServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	viewer := connect(t, s, "perm", "viewer", func(g *auth.RoomGrant) { g.CanPublish = auth.Bool(false) })
	tr := newSynth(t, "audio", "x")
	pctx, pcancel := context.WithTimeout(ctx, 2*time.Second)
	defer pcancel()
	if _, err := viewer.PublishTrack(pctx, tr.track, client.PublishOptions{}); err == nil {
		t.Fatal("publish without can_publish succeeded")
	}
	connect(t, s, "perm", "rec", func(g *auth.RoomGrant) { g.Hidden = true; g.Recorder = true })
	other := connect(t, s, "perm", "other")
	time.Sleep(200 * time.Millisecond)
	for _, p := range other.RemoteParticipants() {
		if p.Identity == "rec" {
			t.Fatal("hidden participant visible")
		}
	}
	ps, _ := s.Manager.ListParticipants(ctx, "perm")
	if len(ps) != 3 {
		t.Fatalf("admin must list hidden participants, got %d", len(ps))
	}
	if r, _ := s.Manager.ListRooms(ctx, []string{"perm"}); r[0].NumParticipants != 2 {
		t.Fatalf("hidden counted: %d", r[0].NumParticipants)
	}
}

func TestSenderReportsReachSubscriber(t *testing.T) {
	s := startServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pubC := connect(t, s, "sr", "pub")
	_, vi := publishAV(t, ctx, pubC)
	sub := connect(t, s, "sr", "sub")
	rt, err := client.WaitTrack(ctx, sub, vi.Sid)
	if err != nil {
		t.Fatal(err)
	}
	srs := make(chan uint32, 4)
	rt.OnSenderReport(func(_ uint64, ts uint32) {
		select {
		case srs <- ts:
		default:
		}
	})
	go func() {
		b := make([]byte, 1500)
		for {
			if _, err := rt.Read(b); err != nil {
				return
			}
		}
	}()
	select {
	case <-srs:
	case <-time.After(10 * time.Second):
		t.Fatal("no sender report forwarded to the subscriber")
	}
}
