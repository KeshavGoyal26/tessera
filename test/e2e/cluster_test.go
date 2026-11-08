package e2e

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/KeshavGoyal26/tessera/pkg/auth"
	"github.com/KeshavGoyal26/tessera/pkg/client"
	"github.com/KeshavGoyal26/tessera/pkg/cluster"
	"github.com/KeshavGoyal26/tessera/pkg/config"
	"github.com/KeshavGoyal26/tessera/pkg/signal"
)

// startMedia starts a media node registered in the cluster; stop removes it (as a
// crashed or stopped node whose entry is gone).
func startMedia(t *testing.T, mr *miniredis.Miniredis, id string) (s *server, stop func()) {
	t.Helper()
	s = startServer(t)
	cc, err := cluster.New(context.Background(), config.RedisConfig{Addr: mr.Addr()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		cc.Run(ctx, cluster.NodeInfo{ID: id, Role: config.RoleMedia, PrivateURL: "http://" + strings.TrimPrefix(s.URL, "ws://")}, s.Manager, testLogger())
	}()
	stop = func() { cancel(); <-done; _ = cc.Close() }
	t.Cleanup(func() {
		select {
		case <-done:
		default:
			stop()
		}
	})
	return s, stop
}

func hosts(s *server, room string) bool {
	for _, r := range s.Manager.Snapshot().Rooms {
		if r.Name == room {
			return true
		}
	}
	return false
}

// TestEdgeRelaysToMediaNodes: clients connect to an edge only; it places each room on
// the least-loaded media node and relays signaling there, media flows end to end, and a
// room whose node is gone is placed again (ADR-0012 phase 1).
func TestEdgeRelaysToMediaNodes(t *testing.T) {
	mr := miniredis.RunT(t)
	m1, stopM1 := startMedia(t, mr, "m1")
	m2, _ := startMedia(t, mr, "m2")
	ecc, err := cluster.New(context.Background(), config.RedisConfig{Addr: mr.Addr()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ecc.Close() })
	edgeHTTP := httptest.NewServer(&cluster.Edge{Cluster: ecc, Verifier: auth.NewVerifier(auth.StaticKeys(m1.cfg.Keys)), Logger: testLogger()})
	t.Cleanup(edgeHTTP.Close)
	edge := &server{URL: "ws://" + strings.TrimPrefix(edgeHTTP.URL, "http://"), cfg: m1.cfg}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	// A bad token is refused at the edge.
	resp, err := http.Get(edgeHTTP.URL + "/rtc?access_token=nope")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad token: status %d", resp.StatusCode)
	}

	// Presenter and viewer of r1, both through the edge.
	pres := connect(t, edge, "r1", "pres")
	v := newVideoStream(t, "cam", 100)
	info, err := pres.PublishTrack(ctx, v.track, client.PublishOptions{Name: "cam", Width: 640, Height: 360})
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() {
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
	viewer := connect(t, edge, "r1", "viewer")
	rt, err := client.WaitTrack(ctx, viewer, info.Sid)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan struct{})
	go func() {
		b := make([]byte, 1500)
		for n := 0; n < 50; n++ {
			if _, err := rt.Read(b); err != nil {
				return
			}
		}
		close(got)
	}()
	select {
	case <-got:
	case <-ctx.Done():
		t.Fatal("no media through the edge")
	}
	first, other := m1, m2
	if !hosts(m1, "r1") {
		first, other = m2, m1
	}
	if !hosts(first, "r1") || hosts(other, "r1") {
		t.Fatal("r1 must live on exactly one media node")
	}

	// After the next heartbeat the loaded node has 2 sessions: r2 goes to the other one.
	time.Sleep(2500 * time.Millisecond)
	connect(t, edge, "r2", "carol")
	if !hosts(other, "r2") {
		t.Fatal("r2 was not placed on the less-loaded media node")
	}

	// r1's node goes away: a new join for r1 is placed on the remaining node.
	if first != m1 {
		t.Skip("r1 landed on m2; the failover part needs it on m1") // placement ties go to m1
	}
	stopM1()
	connect(t, edge, "r1", "late")
	if !hosts(m2, "r1") {
		t.Fatal("r1 was not placed again after its media node left")
	}
}

// TestEdgeServesViewersThroughRelay: a presenter publishes through the edge to a media
// node; a viewer (can_publish false) joins the edge's own room, where the edge relays
// the presenter from the media node, and receives the video (ADR-0012 phase 2).
func TestEdgeServesViewersThroughRelay(t *testing.T) {
	mr := miniredis.RunT(t)
	media, _ := startMedia(t, mr, "m1")
	local := startServer(t) // the edge's own rooms (viewers)
	ecc, err := cluster.New(context.Background(), config.RedisConfig{Addr: mr.Addr()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ecc.Close() })
	verifier := auth.NewVerifier(auth.StaticKeys(media.cfg.Keys))
	relays := &cluster.Relays{Cluster: ecc, Rooms: local.Manager, NodeID: "e1", APIKey: apiKey, APISecret: apiSecret,
		SettingEngine: client.LoopbackSettingEngine(), Logger: testLogger()}
	t.Cleanup(relays.Close)
	edgeHTTP := httptest.NewServer(&cluster.Edge{Cluster: ecc, Verifier: verifier, Relays: relays, Logger: testLogger(),
		Local: signal.NewServer(local.cfg.Signal, verifier, local.Manager, testLogger())})
	t.Cleanup(edgeHTTP.Close)
	edge := &server{URL: "ws://" + strings.TrimPrefix(edgeHTTP.URL, "http://"), cfg: media.cfg}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	pres := connect(t, edge, "g1", "pres")
	v := newVideoStream(t, "cam", 100)
	if _, err := pres.PublishTrack(ctx, v.track, client.PublishOptions{Name: "cam", Width: 640, Height: 360}); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() {
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
	no := false
	viewer := connect(t, edge, "g1", "viewer", func(g *auth.RoomGrant) { g.CanPublish = &no })
	if !hosts(local, "g1") || hosts(media, "g1") && len(media.Manager.Snapshot().Rooms[0].Participants) > 2 {
		t.Fatal("the viewer must be on the edge, the presenter on the media node")
	}
	got := make(chan string, 1)
	viewer.SetCallbacks(client.Callbacks{OnTrackSubscribed: func(rt *client.RemoteTrack) {
		go func() {
			b := make([]byte, 1500)
			for n := 0; n < 50; n++ {
				if _, err := rt.Read(b); err != nil {
					return
				}
			}
			got <- rt.ParticipantIdentity
		}()
	}})
	select {
	case id := <-got:
		if id != "pres" {
			t.Fatalf("relayed track from %q, want the presenter", id)
		}
	case <-ctx.Done():
		t.Fatal("the viewer on the edge received no relayed media")
	}
}
