package cluster

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/auth"
	"github.com/KeshavGoyal26/tessera/pkg/client"
	"github.com/KeshavGoyal26/tessera/pkg/room"
	"github.com/KeshavGoyal26/tessera/pkg/stats"
)

// relayAudioLevelID is the audio-level extension id a media node uses towards its
// subscribers (pkg/rtc), so the edge's speaker detection reads the relayed levels.
const relayAudioLevelID = 1

const (
	relayCheckEvery = 2 * time.Second
	relayIdleStop   = 30 * time.Second
)

// RelayUnwatchedGrace is how long a relayed track with no viewer on this edge keeps
// flowing before the relay pauses it upstream (the media node stops sending it); a
// viewer switching back within it sees the track at once.
var RelayUnwatchedGrace = 10 * time.Second

// Relays brings each group's presenters from the media nodes that host them to this
// edge's own room, where the edge's viewers subscribe to them (ADR-0012). One relay
// session per group and media node: a hidden participant of kind RELAY, joined over
// the private network, subscribed to every track there. A group's relay stops when it
// has had no viewer on this edge for a while.
type Relays struct {
	Cluster *Client
	Rooms   interface {
		room.RemotePublishers
		ListParticipants(ctx context.Context, room string) ([]*tesserav1.ParticipantInfo, error)
		UpdateRoomMetadata(ctx context.Context, room, metadata string) (*tesserav1.Room, error)
		DeleteRoom(ctx context.Context, room string) error
	}
	NodeID    string
	APIKey    string
	APISecret string
	// SettingEngine customises the relay's PeerConnections (tests: loopback only).
	SettingEngine *webrtc.SettingEngine
	Logger        *slog.Logger

	mu     sync.Mutex
	groups map[string]*groupRelay
}

// Ensure starts the relay of group if it is not running.
func (r *Relays) Ensure(group string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.groups == nil {
		r.groups = map[string]*groupRelay{}
	}
	if _, ok := r.groups[group]; ok {
		return
	}
	g := &groupRelay{r: r, group: group, pubs: map[string]remotePres{}, tracks: map[string]localTrack{}}
	r.groups[group] = g
	go g.run()
}

// Sessions is the number of pull sessions this edge holds, by media node id (one per
// group and media node).
func (r *Relays) Sessions() map[string]int {
	r.mu.Lock()
	groups := make([]*groupRelay, 0, len(r.groups))
	for _, g := range r.groups {
		groups = append(groups, g)
	}
	r.mu.Unlock()
	out := map[string]int{}
	for _, g := range groups {
		g.mu.Lock()
		for node := range g.sessions {
			out[node]++
		}
		g.mu.Unlock()
	}
	return out
}

// Close stops every relay.
func (r *Relays) Close() {
	r.mu.Lock()
	groups := r.groups
	r.groups = nil
	r.mu.Unlock()
	for _, g := range groups {
		g.stop()
	}
}

type localTrack struct {
	identity string
	sid      string
}

// trackWatch is whether a relayed track flows upstream (enabled) and the pending pause.
type trackWatch struct {
	enabled bool
	timer   *time.Timer
}

type remotePres struct {
	rp   room.RemotePublisher
	node string
}

// groupRelay follows one group: a relay session on every media node hosting it.
type groupRelay struct {
	r     *Relays
	group string

	mu       sync.Mutex
	stopped  bool
	sessions map[string]client.Client                    // by media node id
	pubs     map[string]remotePres                       // by presenter identity
	quality  map[string]*tesserav1.ConnectionQualityInfo // latest from the media nodes, by node + "/" + identity
	tracks   map[string]localTrack                       // by the media node's track SID
	watch    map[string]*trackWatch                      // by the media node's track SID
	idleFrom time.Time
	done     chan struct{}
}

func (g *groupRelay) run() {
	g.mu.Lock()
	g.done = make(chan struct{})
	g.sessions = map[string]client.Client{}
	done := g.done
	g.mu.Unlock()
	t := time.NewTicker(relayCheckEvery)
	defer t.Stop()
	for {
		if !g.tick() {
			return
		}
		select {
		case <-done:
			return
		case <-t.C:
		}
	}
}

// tick joins the media nodes that host the group and leaves those that no longer
// do; it reports false once the relay stops.
func (g *groupRelay) tick() bool {
	g.mu.Lock()
	stopped := g.stopped
	g.mu.Unlock()
	if stopped {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if g.idle(ctx) {
		g.r.mu.Lock()
		if g.r.groups[g.group] == g {
			delete(g.r.groups, g.group)
		}
		g.r.mu.Unlock()
		g.stop()
		return false
	}
	hosting, err := g.r.Cluster.RoomNodes(ctx, g.group)
	if err != nil {
		return true
	}
	nodes, err := g.r.Cluster.Nodes(ctx)
	if err != nil {
		return true
	}
	urls := map[string]string{}
	for _, n := range nodes {
		urls[n.ID] = strings.Replace(strings.TrimSuffix(n.PrivateURL, "/"), "http", "ws", 1)
	}
	want := map[string]bool{}
	for _, id := range hosting {
		if urls[id] != "" {
			want[id] = true
		}
	}
	g.mu.Lock()
	var gone []string
	for id := range g.sessions {
		// Only a registered node that no longer hosts the group: right after a Redis restart
		// the registry is empty for a moment, and a dead node's session ends on its own.
		if urls[id] != "" && !want[id] {
			gone = append(gone, id)
		}
	}
	var missing []string
	for id := range want {
		if _, ok := g.sessions[id]; !ok {
			missing = append(missing, id)
		}
	}
	g.mu.Unlock()
	for _, id := range gone {
		g.leave(id)
	}
	for _, id := range missing {
		if err := g.join(ctx, id, urls[id]); err != nil {
			g.r.Logger.Warn("relay: cannot join a media node of the group", "group", g.group, "node", id, "err", err)
		}
	}
	return true
}

// idle reports whether the group has had no local viewer for relayIdleStop.
func (g *groupRelay) idle(ctx context.Context) bool {
	ps, _ := g.r.Rooms.ListParticipants(ctx, g.group)
	g.mu.Lock()
	defer g.mu.Unlock()
	viewers := 0
	for _, p := range ps {
		if _, remote := g.pubs[p.GetIdentity()]; !remote {
			viewers++
		}
	}
	if viewers > 0 {
		g.idleFrom = time.Time{}
		return false
	}
	if g.idleFrom.IsZero() {
		g.idleFrom = time.Now()
	}
	return time.Since(g.idleFrom) >= relayIdleStop
}

func (g *groupRelay) join(ctx context.Context, node, url string) error {
	no, yes := false, true
	tok, err := auth.NewAccessToken(g.r.APIKey, g.r.APISecret).SetIdentity("relay:" + g.r.NodeID).
		SetValidFor(24 * time.Hour).SetGrant(&auth.RoomGrant{Room: g.group, RoomJoin: true, CanPublish: &no,
		CanPublishData: &no, CanSubscribe: &yes, Hidden: true, Relay: true}).ToJWT()
	if err != nil {
		return err
	}
	c := client.New(client.Options{AutoSubscribe: true, Direct: true, SettingEngine: g.r.SettingEngine, Logger: g.r.Logger})
	c.SetCallbacks(client.Callbacks{
		OnTrackSubscribed:   func(t *client.RemoteTrack) { g.onTrack(node, c, t) },
		OnTrackUnsubscribed: g.onTrackGone,
		OnParticipantUpdate: func(ps []*tesserav1.ParticipantInfo) { g.onParticipants(node, ps) },
		OnConnectionQuality: func(u *tesserav1.ConnectionQualityUpdate) { g.onQuality(node, c, u) },
		OnRoomUpdate:        func(r *tesserav1.Room) { g.syncMetadata(r) },
		OnDisconnected: func(reason tesserav1.DisconnectReason) {
			if reason == tesserav1.DisconnectReason_DISCONNECT_REASON_ROOM_DELETED {
				go g.roomDeleted() // ended by an admin: end it here too, do not rejoin
				return
			}
			g.mu.Lock()
			if g.sessions[node] == c {
				delete(g.sessions, node) // joined again on a later tick if the node still hosts the group
			}
			g.mu.Unlock()
			g.dropNode(node)
		},
	})
	if err := c.Connect(ctx, url, tok); err != nil {
		return err
	}
	g.syncMetadata(c.Room())
	g.mu.Lock()
	if g.stopped {
		g.mu.Unlock()
		_ = c.Disconnect(context.Background())
		return nil
	}
	g.sessions[node] = c
	g.mu.Unlock()
	g.r.Logger.Info("relay: following group", "group", g.group, "node", node)
	return nil
}

func (g *groupRelay) leave(node string) {
	g.mu.Lock()
	c := g.sessions[node]
	delete(g.sessions, node)
	g.mu.Unlock()
	if c != nil {
		_ = c.Disconnect(context.Background())
	}
	g.dropNode(node)
}

// publisher returns the remote publisher of identity, creating it for node. A
// presenter that moved to another node replaces its earlier publisher.
func (g *groupRelay) publisher(node string, c client.Client, identity string) room.RemotePublisher {
	g.mu.Lock()
	cur, ok := g.pubs[identity]
	g.mu.Unlock()
	if ok && cur.node == node {
		return cur.rp
	}
	var name, metadata string
	var attrs map[string]string
	for _, p := range c.RemoteParticipants() {
		if p.GetIdentity() == identity {
			name, metadata, attrs = p.GetName(), p.GetMetadata(), p.GetAttributes()
		}
	}
	rp, err := g.r.Rooms.AddRemotePublisher(g.group, identity, name, metadata, attrs)
	if err != nil {
		g.r.Logger.Warn("relay: cannot add presenter", "group", g.group, "identity", identity, "err", err)
		return nil
	}
	g.mu.Lock()
	g.pubs[identity] = remotePres{rp: rp, node: node}
	q := g.quality[node+"/"+identity]
	g.mu.Unlock()
	if q != nil {
		rp.SetQuality(q.GetQuality(), q.GetScore())
	}
	return rp
}

// pulledSource counts pulled media in the node's incoming media traffic: the pull
// sessions' PeerConnections have their own sockets, outside the engine's metered ones.
type pulledSource struct{ *client.RemoteTrack }

func (s pulledSource) Read(b []byte) (int, error) {
	n, err := s.RemoteTrack.Read(b)
	if n > 0 {
		stats.UDPIn.Add(n)
	}
	return n, err
}

func (g *groupRelay) onTrack(node string, c client.Client, t *client.RemoteTrack) {
	rp := g.publisher(node, c, t.ParticipantIdentity)
	if rp == nil {
		return
	}
	remoteSID := t.SID
	sid, err := rp.PublishTrack(t.Info, t.Codec(), relayAudioLevelID, pulledSource{t}, t.WriteRTCP,
		func(w bool) { g.setWatched(c, remoteSID, w) })
	if err != nil {
		g.r.Logger.Warn("relay: cannot publish relayed track", "group", g.group, "track", t.SID, "err", err)
		return
	}
	g.mu.Lock()
	g.tracks[t.SID] = localTrack{identity: t.ParticipantIdentity, sid: sid}
	g.mu.Unlock()
	g.setWatched(c, remoteSID, false) // nobody watches yet: pause after the grace period
}

// setWatched resumes a relayed track upstream as soon as a viewer on this edge
// subscribes, and pauses it RelayUnwatchedGrace after the last one leaves: the relay
// stays subscribed (no renegotiation, the media node keeps the stream continuous) but
// the media node stops sending what nobody here watches.
func (g *groupRelay) setWatched(c client.Client, remoteSID string, watched bool) {
	g.mu.Lock()
	if g.watch == nil {
		g.watch = map[string]*trackWatch{}
	}
	tw := g.watch[remoteSID]
	if tw == nil {
		tw = &trackWatch{enabled: true}
		g.watch[remoteSID] = tw
	}
	if watched {
		if tw.timer != nil {
			tw.timer.Stop()
			tw.timer = nil
		}
		resume := !tw.enabled
		tw.enabled = true
		g.mu.Unlock()
		if resume {
			g.setUpstream(c, remoteSID, true)
		}
		return
	}
	if tw.enabled && tw.timer == nil {
		tw.timer = time.AfterFunc(RelayUnwatchedGrace, func() {
			g.mu.Lock()
			if g.watch[remoteSID] != tw || tw.timer == nil {
				g.mu.Unlock()
				return
			}
			tw.timer, tw.enabled = nil, false
			g.mu.Unlock()
			g.setUpstream(c, remoteSID, false)
		})
	}
	g.mu.Unlock()
}

func (g *groupRelay) setUpstream(c client.Client, remoteSID string, on bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.UpdateTrackSettings(ctx, &tesserav1.UpdateTrackSettings{TrackSids: []string{remoteSID}, Disabled: !on}); err != nil {
		g.r.Logger.Debug("relay: track setting not sent", "group", g.group, "track", remoteSID, "err", err)
	}
}

func (g *groupRelay) onTrackGone(remoteSID string) {
	g.mu.Lock()
	lt, ok := g.tracks[remoteSID]
	delete(g.tracks, remoteSID)
	if tw := g.watch[remoteSID]; tw != nil && tw.timer != nil {
		tw.timer.Stop()
	}
	delete(g.watch, remoteSID)
	cur := g.pubs[lt.identity]
	g.mu.Unlock()
	if ok && cur.rp != nil {
		cur.rp.UnpublishTrack(lt.sid)
	}
}

// roomDeleted ends the group on this edge after a media node deleted the room (an admin
// DeleteRoom through any edge): the relay stops and this edge's viewers are disconnected.
func (g *groupRelay) roomDeleted() {
	g.r.mu.Lock()
	if g.r.groups[g.group] == g {
		delete(g.r.groups, g.group)
	}
	g.r.mu.Unlock()
	g.stop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = g.r.Rooms.DeleteRoom(ctx, g.group)
}

// syncMetadata copies the media node's room metadata to this edge's room (set through
// any edge or the media node, viewers here see it).
func (g *groupRelay) syncMetadata(r *tesserav1.Room) {
	if r == nil {
		return
	}
	if _, err := g.r.Rooms.UpdateRoomMetadata(context.Background(), g.group, r.GetMetadata()); err != nil {
		g.r.Logger.Debug("relay: room metadata not mirrored", "group", g.group, "err", err)
	}
}

// onQuality mirrors the media node's connection quality of its presenters onto their
// copies here, so this edge's viewers see the presenter's own connection.
func (g *groupRelay) onQuality(node string, c client.Client, u *tesserav1.ConnectionQualityUpdate) {
	ids := map[string]string{}
	for _, p := range c.RemoteParticipants() {
		ids[p.GetSid()] = p.GetIdentity()
	}
	for _, q := range u.GetUpdates() {
		id := ids[q.GetParticipantSid()]
		if id == "" {
			continue
		}
		g.mu.Lock()
		if g.quality == nil {
			g.quality = map[string]*tesserav1.ConnectionQualityInfo{}
		}
		g.quality[node+"/"+id] = q
		cur, ok := g.pubs[id]
		g.mu.Unlock()
		if ok && cur.node == node {
			cur.rp.SetQuality(q.GetQuality(), q.GetScore())
		}
	}
}

// onParticipants removes presenters that left node (not ones that moved elsewhere) and
// mirrors info changes of the others (e.g. a recording's playlist URL in the attributes).
func (g *groupRelay) onParticipants(node string, ps []*tesserav1.ParticipantInfo) {
	for _, p := range ps {
		if p.GetState() == tesserav1.ParticipantInfo_STATE_DISCONNECTED {
			g.drop(func(identity string, rp remotePres) bool { return identity == p.GetIdentity() && rp.node == node })
			g.mu.Lock()
			delete(g.quality, node+"/"+p.GetIdentity())
			g.mu.Unlock()
			continue
		}
		g.mu.Lock()
		cur, ok := g.pubs[p.GetIdentity()]
		g.mu.Unlock()
		if ok && cur.node == node {
			cur.rp.SetInfo(p.GetName(), p.GetMetadata(), p.GetAttributes())
			for _, t := range p.GetTracks() { // mute changes on the media node
				g.mu.Lock()
				lt, found := g.tracks[t.GetSid()]
				g.mu.Unlock()
				if found {
					cur.rp.SetTrackMuted(lt.sid, t.GetMuted())
				}
			}
		}
	}
}

// dropNode removes the presenters relayed from node (its session ended).
func (g *groupRelay) dropNode(node string) {
	g.drop(func(_ string, rp remotePres) bool { return rp.node == node })
}

func (g *groupRelay) drop(match func(identity string, rp remotePres) bool) {
	g.mu.Lock()
	var closing []room.RemotePublisher
	for id, rp := range g.pubs {
		if !match(id, rp) {
			continue
		}
		closing = append(closing, rp.rp)
		delete(g.pubs, id)
		delete(g.quality, rp.node+"/"+id)
		for k, lt := range g.tracks {
			if lt.identity == id {
				delete(g.tracks, k)
				if tw := g.watch[k]; tw != nil && tw.timer != nil {
					tw.timer.Stop()
				}
				delete(g.watch, k)
			}
		}
	}
	g.mu.Unlock()
	for _, rp := range closing {
		rp.Close()
	}
}

func (g *groupRelay) stop() {
	g.mu.Lock()
	if g.stopped {
		g.mu.Unlock()
		return
	}
	g.stopped = true
	sessions := g.sessions
	g.sessions = map[string]client.Client{}
	done := g.done
	g.mu.Unlock()
	for _, c := range sessions {
		_ = c.Disconnect(context.Background())
	}
	g.drop(func(string, remotePres) bool { return true })
	if done != nil {
		close(done)
	}
}
