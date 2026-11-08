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
)

// relayAudioLevelID is the audio-level extension id a media node uses towards its
// subscribers (pkg/rtc), so the edge's speaker detection reads the relayed levels.
const relayAudioLevelID = 1

const (
	relayCheckEvery = 2 * time.Second
	relayIdleStop   = 30 * time.Second
)

// Relays brings each group's presenters from the media node that hosts them to this
// edge's own room, where the edge's viewers subscribe to them (ADR-0012). One relay
// session per group: a hidden participant of kind RELAY on the media node, joined over
// the private network, subscribed to every track there. It stops when the group has
// had no viewer on this edge for a while.
type Relays struct {
	Cluster *Client
	Rooms   interface {
		room.RemotePublishers
		ListParticipants(ctx context.Context, room string) ([]*tesserav1.ParticipantInfo, error)
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
	g := &groupRelay{r: r, group: group, pubs: map[string]room.RemotePublisher{}, tracks: map[string]localTrack{}}
	r.groups[group] = g
	go g.run()
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

type groupRelay struct {
	r     *Relays
	group string

	mu       sync.Mutex
	stopped  bool
	node     string
	c        client.Client
	pubs     map[string]room.RemotePublisher // by presenter identity
	tracks   map[string]localTrack           // by the media node's track SID
	idleFrom time.Time
	done     chan struct{}
}

func (g *groupRelay) run() {
	g.done = make(chan struct{})
	t := time.NewTicker(relayCheckEvery)
	defer t.Stop()
	for {
		if !g.tick() {
			return
		}
		select {
		case <-g.done:
			return
		case <-t.C:
		}
	}
}

// tick (re)connects to the group's media node and reports false once the relay stops.
func (g *groupRelay) tick() bool {
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
	node, err := g.r.Cluster.RoomNode(ctx, g.group)
	if err != nil || node == "" {
		return true // no presenter yet (or Redis unavailable): check again
	}
	g.mu.Lock()
	same := g.node == node && g.c != nil
	g.mu.Unlock()
	if same {
		return true
	}
	nodes, err := g.r.Cluster.Nodes(ctx)
	if err != nil {
		return true
	}
	var url string
	for _, n := range nodes {
		if n.ID == node {
			url = strings.Replace(strings.TrimSuffix(n.PrivateURL, "/"), "http", "ws", 1)
		}
	}
	if url == "" {
		return true
	}
	g.disconnect()
	if err := g.connect(ctx, node, url); err != nil {
		g.r.Logger.Warn("relay: cannot join the group's media node", "group", g.group, "node", node, "err", err)
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

func (g *groupRelay) connect(ctx context.Context, node, url string) error {
	no, yes := false, true
	tok, err := auth.NewAccessToken(g.r.APIKey, g.r.APISecret).SetIdentity("relay:" + g.r.NodeID).SetValidFor(24 * time.Hour).
		SetGrant(&auth.RoomGrant{Room: g.group, RoomJoin: true, CanPublish: &no, CanPublishData: &no, CanSubscribe: &yes,
			Hidden: true, Relay: true}).ToJWT()
	if err != nil {
		return err
	}
	c := client.New(client.Options{AutoSubscribe: true, SettingEngine: g.r.SettingEngine, Logger: g.r.Logger})
	c.SetCallbacks(client.Callbacks{
		OnTrackSubscribed:   func(t *client.RemoteTrack) { g.onTrack(c, t) },
		OnTrackUnsubscribed: g.onTrackGone,
		OnParticipantUpdate: g.onParticipants,
		OnDisconnected: func(tesserav1.DisconnectReason) {
			g.mu.Lock()
			if g.c == c {
				g.c, g.node = nil, "" // reconnect on the next tick
			}
			g.mu.Unlock()
			g.dropAll()
		},
	})
	if err := c.Connect(ctx, url, tok); err != nil {
		return err
	}
	g.mu.Lock()
	if g.stopped {
		g.mu.Unlock()
		_ = c.Disconnect(context.Background())
		return nil
	}
	g.c, g.node = c, node
	g.mu.Unlock()
	g.r.Logger.Info("relay: following group", "group", g.group, "node", node)
	return nil
}

func (g *groupRelay) publisher(c client.Client, identity string) room.RemotePublisher {
	g.mu.Lock()
	rp := g.pubs[identity]
	g.mu.Unlock()
	if rp != nil {
		return rp
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
	g.pubs[identity] = rp
	g.mu.Unlock()
	return rp
}

func (g *groupRelay) onTrack(c client.Client, t *client.RemoteTrack) {
	rp := g.publisher(c, t.ParticipantIdentity)
	if rp == nil {
		return
	}
	sid, err := rp.PublishTrack(t.Info, t.Codec(), relayAudioLevelID, t, t.WriteRTCP)
	if err != nil {
		g.r.Logger.Warn("relay: cannot publish relayed track", "group", g.group, "track", t.SID, "err", err)
		return
	}
	g.mu.Lock()
	g.tracks[t.SID] = localTrack{identity: t.ParticipantIdentity, sid: sid}
	g.mu.Unlock()
}

func (g *groupRelay) onTrackGone(remoteSID string) {
	g.mu.Lock()
	lt, ok := g.tracks[remoteSID]
	delete(g.tracks, remoteSID)
	rp := g.pubs[lt.identity]
	g.mu.Unlock()
	if ok && rp != nil {
		rp.UnpublishTrack(lt.sid)
	}
}

func (g *groupRelay) onParticipants(ps []*tesserav1.ParticipantInfo) {
	for _, p := range ps {
		if p.GetState() != tesserav1.ParticipantInfo_STATE_DISCONNECTED {
			continue
		}
		g.mu.Lock()
		rp := g.pubs[p.GetIdentity()]
		delete(g.pubs, p.GetIdentity())
		for k, lt := range g.tracks {
			if lt.identity == p.GetIdentity() {
				delete(g.tracks, k)
			}
		}
		g.mu.Unlock()
		if rp != nil {
			rp.Close()
		}
	}
}

// dropAll removes every relayed presenter (the relay session ended).
func (g *groupRelay) dropAll() {
	g.mu.Lock()
	pubs := g.pubs
	g.pubs = map[string]room.RemotePublisher{}
	g.tracks = map[string]localTrack{}
	g.mu.Unlock()
	for _, rp := range pubs {
		rp.Close()
	}
}

func (g *groupRelay) disconnect() {
	g.mu.Lock()
	c := g.c
	g.c, g.node = nil, ""
	g.mu.Unlock()
	if c != nil {
		_ = c.Disconnect(context.Background())
	}
	g.dropAll()
}

func (g *groupRelay) stop() {
	g.mu.Lock()
	if g.stopped {
		g.mu.Unlock()
		return
	}
	g.stopped = true
	done := g.done
	g.mu.Unlock()
	g.disconnect()
	if done != nil {
		select {
		case <-done:
		default:
			close(done)
		}
	}
}
