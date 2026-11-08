package e2e

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
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

// publishVideo publishes a synthetic H.264 stream until the test ends.
func publishVideo(t *testing.T, ctx context.Context, c client.Client, id string) {
	t.Helper()
	v := newVideoStream(t, id, 100)
	if _, err := c.PublishTrack(ctx, v.track, client.PublishOptions{Name: "cam", Width: 640, Height: 360}); err != nil {
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
}

// newEdge starts an edge: presenters are relayed to the media nodes, viewers join the
// edge's own rooms where Relays brings the presenters.
func newEdge(t *testing.T, mr *miniredis.Miniredis) (edge *server, httpURL string) {
	t.Helper()
	local := startServer(t)
	ecc, err := cluster.New(context.Background(), config.RedisConfig{Addr: mr.Addr()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ecc.Close() })
	verifier := auth.NewVerifier(auth.StaticKeys(local.cfg.Keys))
	relays := &cluster.Relays{Cluster: ecc, Rooms: local.Manager, NodeID: "e1", APIKey: apiKey, APISecret: apiSecret,
		SettingEngine: client.LoopbackSettingEngine(), Logger: testLogger()}
	t.Cleanup(relays.Close)
	h := httptest.NewServer(&cluster.Edge{Cluster: ecc, Verifier: verifier, Relays: relays, Logger: testLogger(),
		Local: signal.NewServer(local.cfg.Signal, verifier, local.Manager, testLogger())})
	t.Cleanup(h.Close)
	return &server{URL: "ws://" + strings.TrimPrefix(h.URL, "http://"), Manager: local.Manager, cfg: local.cfg}, h.URL
}

// TestEdgeRelaysPresentersFromSeveralMediaNodes: a group spans two media nodes (the
// first is past the spill threshold when the second presenter joins); a viewer on the
// edge receives both presenters (ADR-0012 phase 2).
func TestEdgeRelaysPresentersFromSeveralMediaNodes(t *testing.T) {
	old := cluster.SpillSessions
	cluster.SpillSessions = 1
	t.Cleanup(func() { cluster.SpillSessions = old })
	mr := miniredis.RunT(t)
	m1, _ := startMedia(t, mr, "m1")
	m2, _ := startMedia(t, mr, "m2")
	edge, httpURL := newEdge(t, mr)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	resp, err := http.Get(httpURL + "/rtc?access_token=nope")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad token: status %d", resp.StatusCode)
	}

	publishVideo(t, ctx, connect(t, edge, "g1", "a"), "cam-a")
	time.Sleep(2500 * time.Millisecond) // a heartbeat reports the first node's session
	publishVideo(t, ctx, connect(t, edge, "g1", "b"), "cam-b")
	if !hosts(m1, "g1") || !hosts(m2, "g1") {
		t.Fatal("the group should span both media nodes")
	}

	no := false
	viewer := client.New(client.Options{AutoSubscribe: true, SettingEngine: client.LoopbackSettingEngine(), Logger: testLogger()})
	got := make(chan string, 4)
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
	if err := viewer.Connect(ctx, edge.URL, token(t, "g1", "viewer", func(g *auth.RoomGrant) { g.CanPublish = &no })); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = viewer.Disconnect(context.Background()) })
	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case id := <-got:
			seen[id] = true
		case <-ctx.Done():
			t.Fatalf("the viewer received media from %v, want a and b", seen)
		}
	}
	if !hosts(edge, "g1") {
		t.Fatal("the viewer must be on the edge")
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

	// Info set on the media node later (e.g. a recording's playlist URL) reaches the edge's viewers.
	if _, err := media.Manager.UpdateParticipant(ctx, &tesserav1.UpdateParticipantRequest{Room: "g1", Identity: "pres",
		Attributes: map[string]string{"hls_url": "/recordings/g1/RC_x/master.m3u8"}}); err != nil {
		t.Fatal(err)
	}
	for {
		var seen string
		for _, p := range viewer.RemoteParticipants() {
			if p.GetIdentity() == "pres" {
				seen = p.GetAttributes()["hls_url"]
			}
		}
		if seen == "/recordings/g1/RC_x/master.m3u8" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("the attribute set on the media node never reached the viewer on the edge")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// relayPaused reports whether the relay's subscription on a media node is paused.
func relayPaused(s *server) (paused, found bool) {
	for _, r := range s.Manager.Snapshot().Rooms {
		for _, p := range r.Participants {
			if p.Kind != "relay" {
				continue
			}
			for _, st := range p.Subscribed {
				if st.Kind == "video" {
					return st.Paused, true
				}
			}
		}
	}
	return false, false
}

// The relay pauses upstream what no viewer on the edge watches, and resumes it when a
// viewer subscribes (ADR-0012: an edge pulls only the watched presenters).
func TestEdgeRelaysOnlyWatchedPresenters(t *testing.T) {
	old := cluster.RelayUnwatchedGrace
	cluster.RelayUnwatchedGrace = time.Second
	t.Cleanup(func() { cluster.RelayUnwatchedGrace = old })
	mr := miniredis.RunT(t)
	media, _ := startMedia(t, mr, "m1")
	edge, _ := newEdge(t, mr)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	publishVideo(t, ctx, connect(t, edge, "g1", "pres"), "cam")

	no := false
	viewer := client.New(client.Options{AutoSubscribe: false, SettingEngine: client.LoopbackSettingEngine(), Logger: testLogger()})
	got := make(chan struct{}, 1)
	viewer.SetCallbacks(client.Callbacks{OnTrackSubscribed: func(rt *client.RemoteTrack) {
		go func() {
			b := make([]byte, 1500)
			for n := 0; n < 30; n++ {
				if _, err := rt.Read(b); err != nil {
					return
				}
			}
			got <- struct{}{}
		}()
	}})
	if err := viewer.Connect(ctx, edge.URL, token(t, "g1", "viewer", func(g *auth.RoomGrant) { g.CanPublish = &no })); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = viewer.Disconnect(context.Background()) })
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		for !cond() {
			select {
			case <-ctx.Done():
				t.Fatal(what)
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	waitFor("the unwatched presenter was never paused upstream", func() bool { p, ok := relayPaused(media); return ok && p })

	var video string
	waitFor("the presenter never appeared on the edge", func() bool {
		for _, p := range viewer.RemoteParticipants() {
			for _, tr := range p.GetTracks() {
				if tr.GetType() == tesserav1.TrackType_TRACK_TYPE_VIDEO {
					video = tr.GetSid()
				}
			}
		}
		return video != ""
	})
	if err := viewer.Subscribe(ctx, video); err != nil {
		t.Fatal(err)
	}
	select {
	case <-got:
	case <-ctx.Done():
		t.Fatal("no media after subscribing to a paused presenter")
	}
	if p, _ := relayPaused(media); p {
		t.Fatal("a watched presenter is still paused upstream")
	}
}

// A media node crashes: its presenter rejoins (as the browser SDK does after a failed
// resume) and is placed on the remaining node; the edge's relay follows it there and the
// viewer on the edge gets the presenter back (ADR-0012 phase 3 drill).
func TestMediaNodeCrashPresenterComesBack(t *testing.T) {
	mr := miniredis.RunT(t)
	m1, stopM1 := startMedia(t, mr, "m1")
	m2, _ := startMedia(t, mr, "m2")
	edge, _ := newEdge(t, mr)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// The presenter app: (re)join and publish; on a lost session, join again.
	var join func()
	join = func() {
		c := client.New(client.Options{SettingEngine: client.LoopbackSettingEngine(), Logger: testLogger()})
		c.SetCallbacks(client.Callbacks{OnDisconnected: func(tesserav1.DisconnectReason) {
			if ctx.Err() == nil {
				go join()
			}
		}})
		for ctx.Err() == nil {
			if err := c.Connect(ctx, edge.URL, token(t, "g1", "pres")); err == nil {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		t.Cleanup(func() { _ = c.Disconnect(context.Background()) })
		publishVideo(t, ctx, c, "cam")
	}
	join()

	no := false
	viewer := client.New(client.Options{AutoSubscribe: true, SettingEngine: client.LoopbackSettingEngine(), Logger: testLogger()})
	got := make(chan struct{}, 8)
	viewer.SetCallbacks(client.Callbacks{OnTrackSubscribed: func(rt *client.RemoteTrack) {
		go func() {
			b := make([]byte, 1500)
			for n := 0; n < 30; n++ {
				if _, err := rt.Read(b); err != nil {
					return
				}
			}
			got <- struct{}{}
		}()
	}})
	if err := viewer.Connect(ctx, edge.URL, token(t, "g1", "viewer", func(g *auth.RoomGrant) { g.CanPublish = &no })); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = viewer.Disconnect(context.Background()) })
	select {
	case <-got:
	case <-ctx.Done():
		t.Fatal("no media before the crash")
	}
	first, survivor := m1, m2
	if !hosts(m1, "g1") {
		first, survivor = m2, m1
	}
	if first != m1 {
		t.Skip("the presenter landed on m2; the drill crashes m1") // ties go to m1
	}
	stopM1()   // its registry entry goes (as after the 10 s expiry)
	m1.crash() // sessions drop
	select {
	case <-got:
	case <-ctx.Done():
		t.Fatal("the viewer never got the presenter back after the media node crashed")
	}
	if !hosts(survivor, "g1") {
		t.Fatal("the presenter should have rejoined on the remaining media node")
	}
}

// Admin calls to an edge reach the media node of the presenter, and lists merge the
// media nodes with the edge's viewers (ADR-0012 phase 3).
func TestEdgeForwardsAdminCalls(t *testing.T) {
	mr := miniredis.RunT(t)
	media, _ := startMedia(t, mr, "m1")
	edge, _ := newEdge(t, mr)
	ecc, err := cluster.New(context.Background(), config.RedisConfig{Addr: mr.Addr()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ecc.Close() })
	fwd := &cluster.AdminForwarder{C: ecc, Local: edge.Manager, APIKey: apiKey, Secret: apiSecret}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	connect(t, edge, "g1", "pres")
	no := false
	connect(t, edge, "g1", "viewer", func(g *auth.RoomGrant) { g.CanPublish = &no })
	time.Sleep(5500 * time.Millisecond) // the media node records that it hosts g1
	ps, err := fwd.ListParticipants(ctx, "g1")
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, p := range ps {
		ids[p.GetIdentity()] = true
	}
	if len(ps) != 2 || !ids["pres"] || !ids["viewer"] {
		t.Fatalf("participants %v, want the presenter (media node) and the viewer (edge)", ids)
	}
	if p, err := fwd.GetParticipant(ctx, "g1", "pres"); err != nil || p.GetIdentity() != "pres" {
		t.Fatalf("presenter lookup: %v %v", p, err)
	}
	if err := fwd.RemoveParticipant(ctx, "g1", "pres"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	for _, r := range media.Manager.Snapshot().Rooms {
		for _, p := range r.Participants {
			if p.Identity == "pres" {
				t.Fatal("the presenter was not removed from its media node")
			}
		}
	}
	rooms, err := fwd.ListRooms(ctx, nil)
	if err != nil || len(rooms) != 1 || rooms[0].GetName() != "g1" {
		t.Fatalf("rooms %v err %v", rooms, err)
	}
}
