package room

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
	"google.golang.org/protobuf/proto"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/auth"
	"github.com/KeshavGoyal26/tessera/pkg/errs"
	"github.com/KeshavGoyal26/tessera/pkg/rtc"
	"github.com/KeshavGoyal26/tessera/pkg/rtc/bwe"
	"github.com/KeshavGoyal26/tessera/pkg/rtc/types"
	"github.com/KeshavGoyal26/tessera/pkg/signal"
	"github.com/KeshavGoyal26/tessera/pkg/stats"
	"github.com/KeshavGoyal26/tessera/pkg/version"
)

type pubTrack struct {
	cid  string
	info *tesserav1.TrackInfo
	recv *rtc.ReceiverImpl
	// Dynacast state (RTCP ticker goroutine only).
	lastNeeded [types.MaxSpatialLayers]time.Time
	enabled    int32 // highest enabled layer last signalled; -2 = never sent
}

// DynacastGrace is how long an unused layer stays enabled before the
// publisher is told to stop sending it (ARCHITECTURE.md 4.3).
var DynacastGrace = 10 * time.Second

// dynacast computes the highest layer any subscriber wants and returns an
// update for the publisher when the enabled set changes.
func (t *pubTrack) dynacast(now time.Time) *tesserav1.SubscribedQualityUpdate {
	if !t.info.GetSimulcast() || t.info.GetType() != tesserav1.TrackType_TRACK_TYPE_VIDEO {
		return nil
	}
	maxNeeded := int32(-1)
	for _, d := range t.recv.DownTracks() {
		dt, ok := d.(*rtc.DownTrackImpl)
		if !ok {
			continue
		}
		if m := dt.MaxLayer(); m.IsValid() && m.Spatial > maxNeeded {
			maxNeeded = m.Spatial
		}
	}
	for s := int32(0); s <= maxNeeded && s < types.MaxSpatialLayers; s++ {
		t.lastNeeded[s] = now
	}
	enabled := int32(-1)
	for s := int32(0); s < types.MaxSpatialLayers; s++ {
		if !t.lastNeeded[s].IsZero() && now.Sub(t.lastNeeded[s]) < DynacastGrace {
			enabled = s
		}
	}
	if t.enabled == -2 && enabled == types.MaxSpatialLayers-1 {
		t.enabled = enabled // publisher starts with all layers on
		return nil
	}
	if enabled == t.enabled {
		return nil
	}
	t.enabled = enabled
	upd := &tesserav1.SubscribedQualityUpdate{TrackSid: t.info.GetSid()}
	for s := int32(0); s < types.MaxSpatialLayers; s++ {
		upd.SubscribedQualities = append(upd.SubscribedQualities, &tesserav1.SubscribedQuality{Quality: types.QualityForSpatial(s), Enabled: s <= enabled})
	}
	return upd
}

type subscription struct {
	dt       *rtc.DownTrackImpl
	pub      *Participant
	disabled bool
}

// Participant is one session in a room.
type Participant struct {
	room          *Room
	claims        auth.Claims // of the joining token, re-signed for token refresh
	sid, identity string
	kind          tesserav1.ParticipantInfo_Kind
	joinedAt      time.Time
	autoSubscribe bool
	client        *tesserav1.ClientInfo

	connMu   sync.Mutex
	conn     signal.Conn
	connGen  uint64
	lastSeen atomic.Int64
	rttMs    atomic.Int64
	// joined is set once JoinResponse is queued; earlier room broadcasts are
	// dropped because JoinResponse must be the first message (its content
	// already reflects the room state).
	joined atomic.Bool

	mu         sync.Mutex
	name       string
	metadata   string
	attributes map[string]string
	perm       *tesserav1.ParticipantPermission
	autoRecord bool
	state      tesserav1.ParticipantInfo_State
	pub, sub   rtc.Transport
	pending    map[string]*pubTrack // by cid, before media arrives
	tracks     map[string]*pubTrack // by sid, media flowing
	subs       map[string]*subscription
	explicit   map[string]bool // explicit subscriptions (auto_subscribe=0)
	closed     bool
	resumeT    *time.Timer
	twcc       *rtc.TWCCSender
	alloc      bwe.Allocator
}

func newParticipant(r *Room, c *auth.Claims, cp signal.ConnectParams, conn signal.Conn) *Participant {
	perm := c.Grants.ToPermission()
	kind := tesserav1.ParticipantInfo_KIND_STANDARD
	switch {
	case perm.Recorder:
		kind = tesserav1.ParticipantInfo_KIND_RECORDER
	case cp.Client.GetSdk() == tesserav1.ClientInfo_SDK_LOADTEST:
		kind = tesserav1.ParticipantInfo_KIND_LOADTEST
	}
	p := &Participant{
		room: r, claims: *c, sid: newSID("PA_"), identity: c.Identity(), kind: kind, joinedAt: time.Now(),
		autoSubscribe: cp.AutoSubscribe, client: cp.Client, conn: conn,
		name: c.Name, metadata: c.Metadata, attributes: c.Attributes, perm: perm,
		autoRecord: c.Grants != nil && c.Grants.AutoRecord,
		state:      tesserav1.ParticipantInfo_STATE_JOINING,
		pending:    map[string]*pubTrack{}, tracks: map[string]*pubTrack{}, subs: map[string]*subscription{}, explicit: map[string]bool{},
	}
	p.lastSeen.Store(time.Now().UnixNano())
	return p
}

// SID returns the participant SID.
func (p *Participant) SID() string { return p.sid }

// Identity returns the participant identity.
func (p *Participant) Identity() string { return p.identity }

func (p *Participant) permission() *tesserav1.ParticipantPermission {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.perm
}

func (p *Participant) isClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

func (p *Participant) isPublisher() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.tracks) > 0
}

// ToProto returns the participant info.
func (p *Participant) ToProto() *tesserav1.ParticipantInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	info := &tesserav1.ParticipantInfo{
		Sid: p.sid, Identity: p.identity, State: p.state, Metadata: p.metadata, JoinedAtMs: p.joinedAt.UnixMilli(),
		Name: p.name, Permission: p.perm, IsPublisher: len(p.tracks) > 0, Kind: p.kind, Attributes: p.attributes,
	}
	for _, t := range p.tracks {
		info.Tracks = append(info.Tracks, proto.Clone(t.info).(*tesserav1.TrackInfo))
	}
	return info
}

func (p *Participant) send(msg *tesserav1.SignalResponse) {
	p.connMu.Lock()
	c := p.conn
	p.connMu.Unlock()
	if c == nil {
		return
	}
	if _, isJoin := msg.Message.(*tesserav1.SignalResponse_Join); isJoin {
		defer p.joined.Store(true)
	} else if !p.joined.Load() {
		return
	}
	if err := c.WriteResponse(msg); err != nil && errs.CodeOf(err) == errs.CodeResourceExhausted {
		_ = c.Close(signal.CloseProtocolError, "slow consumer")
	}
}

type tHandler struct {
	p      *Participant
	target tesserav1.SignalTarget
}

func (h tHandler) OnICECandidate(c *webrtc.ICECandidateInit, target tesserav1.SignalTarget) {
	h.p.send(&tesserav1.SignalResponse{Message: &tesserav1.SignalResponse_Trickle{Trickle: &tesserav1.TrickleRequest{
		CandidateInit: rtc.MarshalCandidate(c), Target: target}}})
}

func (h tHandler) OnOffer(sd *tesserav1.SessionDescription) {
	h.p.send(&tesserav1.SignalResponse{Message: &tesserav1.SignalResponse_Offer{Offer: sd}})
}

func (h tHandler) OnTrack(t *webrtc.TrackRemote, r *webrtc.RTPReceiver) { h.p.onTrack(t, r) }

func (h tHandler) OnDataPacket(tesserav1.DataPacket_Kind, []byte) {}

func (h tHandler) OnDataChannel(dc *webrtc.DataChannel) { h.p.onDataChannel(dc) }

func (h tHandler) OnConnectionStateChange(s webrtc.PeerConnectionState) {
	if s == webrtc.PeerConnectionStateConnected {
		h.p.mu.Lock()
		if h.p.state == tesserav1.ParticipantInfo_STATE_JOINED {
			h.p.state = tesserav1.ParticipantInfo_STATE_ACTIVE
		}
		h.p.mu.Unlock()
	}
}

func (p *Participant) newTransport(target tesserav1.SignalTarget) (rtc.Transport, error) {
	return p.room.m.engine.NewTransport(rtc.TransportParams{
		ParticipantSID: p.sid, Target: target, Handler: tHandler{p, target},
		Logger: p.room.m.logger.With("participant", p.identity, "room", p.room.Name()),
	})
}

// start creates the subscriber PC and sends JoinResponse.
func (p *Participant) start() error {
	m := p.room.m
	perm := p.permission()
	if perm.CanSubscribe {
		t, err := p.newTransport(tesserav1.SignalTarget_SIGNAL_TARGET_SUBSCRIBER)
		if err != nil {
			return err
		}
		cc := m.cfg.RTC.CongestionControl
		if p.kind == tesserav1.ParticipantInfo_KIND_RECORDER {
			// Recorders run next to the server and must never be downgraded or
			// paused; they also may not read every subscribed track, which
			// would starve TWCC feedback.
			cc.Enabled = false
		}
		bcfg := bwe.Config{Enabled: cc.Enabled, AllowPause: cc.AllowPause, Probe: cc.Probe}
		est := bwe.NewEstimator(bcfg)
		alloc := bwe.NewAllocator(bcfg, est)
		alloc.OnPausedChanged(p.onStreamStates)
		_ = t.CreateDataChannels()
		p.mu.Lock()
		p.sub = t
		if cc.Enabled {
			p.twcc = &rtc.TWCCSender{Est: est}
		}
		p.alloc = alloc
		p.mu.Unlock()
		alloc.Start()
	}
	var others []*tesserav1.ParticipantInfo
	for _, o := range p.room.Participants() {
		if o != p && !o.permission().GetHidden() {
			others = append(others, o.ToProto())
		}
	}
	var codecs []*tesserav1.Codec
	for _, c := range m.cfg.RTC.EnabledCodecs {
		if !strings.EqualFold(c, "audio/red") {
			codecs = append(codecs, &tesserav1.Codec{Mime: c})
		}
	}
	p.mu.Lock()
	p.state = tesserav1.ParticipantInfo_STATE_JOINED
	p.mu.Unlock()
	go p.rtcpLoop()
	p.send(&tesserav1.SignalResponse{Message: &tesserav1.SignalResponse_Join{Join: &tesserav1.JoinResponse{
		Room: p.room.ToProto(), Participant: p.ToProto(), OtherParticipants: others,
		ServerInfo:   &tesserav1.ServerInfo{Version: version.Version, Protocol: version.ProtocolVersion, MinProtocol: version.MinProtocolVersion, NodeId: m.cfg.NodeID, Region: m.cfg.Region},
		IceServers:   m.engine.ICEServers(p.sid),
		PingInterval: uint32(m.cfg.Signal.PingInterval / time.Second), PingTimeout: uint32(m.cfg.Signal.PingTimeout / time.Second),
		EnabledPublishCodecs: codecs, ResumeWindow: uint32(m.cfg.Signal.ResumeWindow / time.Second),
	}}})
	// JoinResponse must be the first message: negotiate the subscriber PC
	// (data channels) only after it is queued.
	p.mu.Lock()
	sub := p.sub
	p.mu.Unlock()
	if sub != nil {
		sub.Negotiate(false)
	}
	return nil
}

// rtcpLoop sends SRs for subscribed tracks and RRs for published tracks
// every second (ARCHITECTURE.md 4.2).
func (p *Participant) rtcpLoop() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for range t.C {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return
		}
		pub, sub := p.pub, p.sub
		dts := make([]*rtc.DownTrackImpl, 0, len(p.subs))
		for _, s := range p.subs {
			dts = append(dts, s.dt)
		}
		recvs := make([]*rtc.ReceiverImpl, 0, len(p.tracks))
		ptracks := make([]*pubTrack, 0, len(p.tracks))
		for _, tr := range p.tracks {
			recvs = append(recvs, tr.recv)
			ptracks = append(ptracks, tr)
		}
		p.mu.Unlock()
		// Round trips measured on the media paths (ICE) pace NACK retries towards
		// the publisher and the subscriber's congestion controller.
		if pub != nil {
			if rtt := pub.Stats().RTT; rtt > 0 {
				for _, r := range recvs {
					r.SetRTT(rtt)
				}
			}
		}
		if sub != nil {
			if rtt := sub.Stats().RTT; rtt > 0 {
				p.mu.Lock()
				if p.twcc != nil {
					p.twcc.Est.OnRTT(rtt)
				}
				p.mu.Unlock()
			}
		}
		now := time.Now()
		for _, tr := range ptracks {
			if upd := tr.dynacast(now); upd != nil {
				p.send(&tesserav1.SignalResponse{Message: &tesserav1.SignalResponse_SubscribedQualityUpdate{SubscribedQualityUpdate: upd}})
			}
		}
		if sub != nil && len(dts) > 0 {
			var pkts []rtcp.Packet
			for _, dt := range dts {
				if sr := dt.SenderReport(); sr != nil {
					pkts = append(pkts, sr)
				}
			}
			// 28-byte SRs: 30 per compound packet keeps it well under the MTU.
			for len(pkts) > 0 {
				n := min(len(pkts), 30)
				_ = sub.WriteRTCP(pkts[:n])
				pkts = pkts[n:]
			}
		}
		if pub != nil && len(recvs) > 0 {
			rr := &rtcp.ReceiverReport{SSRC: 1}
			for _, r := range recvs {
				rr.Reports = append(rr.Reports, r.ReceptionReports()...)
			}
			for len(rr.Reports) > 0 {
				n := min(len(rr.Reports), 31)
				_ = pub.WriteRTCP([]rtcp.Packet{&rtcp.ReceiverReport{SSRC: 1, Reports: rr.Reports[:n]}})
				rr.Reports = rr.Reports[n:]
			}
		}
	}
}

// run is the session read loop; it returns when the connection ends.
func (p *Participant) run(ctx context.Context) error {
	p.connMu.Lock()
	conn, gen := p.conn, p.connGen
	p.connMu.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go p.pingWatch(ctx, conn)
	for {
		req, err := conn.ReadRequest(ctx)
		if err != nil {
			p.onConnLost(gen)
			return err
		}
		p.lastSeen.Store(time.Now().UnixNano())
		if leave := p.handle(req); leave {
			p.room.removeParticipant(p, tesserav1.DisconnectReason_DISCONNECT_REASON_CLIENT_INITIATED, false)
			_ = conn.Close(signal.CloseNormal, "leave")
			return nil
		}
	}
}

func (p *Participant) pingWatch(ctx context.Context, conn signal.Conn) {
	timeout := p.room.m.cfg.Signal.PingTimeout
	every := p.refreshInterval()
	p.refreshToken()
	nextRefresh := time.Now().Add(every)
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			if timeout > 0 && now.Sub(time.Unix(0, p.lastSeen.Load())) > timeout {
				_ = conn.Close(signal.ClosePingTimeout, "ping timeout")
				return
			}
			if now.After(nextRefresh) {
				p.refreshToken()
				nextRefresh = now.Add(every)
			}
		}
	}
}

// tokenTTL is the lifetime of the joining token (6 h if it had no not-before).
func (p *Participant) tokenTTL() time.Duration {
	if c := p.claims; c.NotBefore > 0 && c.Expiry > c.NotBefore {
		return time.Duration(c.Expiry-c.NotBefore) * time.Second
	}
	return 6 * time.Hour
}

// refreshInterval sends a fresh token well before the last one expires.
func (p *Participant) refreshInterval() time.Duration {
	return max(min(p.tokenTTL()/2, 5*time.Minute), 10*time.Second)
}

// refreshToken sends the client its token re-signed with a new lifetime, so a
// reconnect after the original token expired still succeeds (as LiveKit does).
func (p *Participant) refreshToken() {
	keys := p.room.m.keys
	if keys == nil {
		return
	}
	secret, ok := keys.Secret(p.claims.Issuer)
	if !ok {
		return
	}
	c := p.claims
	now := time.Now()
	c.NotBefore, c.Expiry = now.Unix(), now.Add(p.tokenTTL()).Unix()
	tok, err := auth.Sign(&c, secret)
	if err != nil {
		return
	}
	p.send(&tesserav1.SignalResponse{Message: &tesserav1.SignalResponse_RefreshToken{RefreshToken: tok}})
}

// onConnLost keeps the session for resume_window, then removes it.
func (p *Participant) onConnLost(gen uint64) {
	p.connMu.Lock()
	if p.connGen != gen {
		p.connMu.Unlock()
		return // already resumed on a newer connection
	}
	p.conn = nil
	p.connMu.Unlock()
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	window := p.room.m.cfg.Signal.ResumeWindow
	if p.resumeT != nil {
		p.resumeT.Stop()
	}
	p.resumeT = time.AfterFunc(window, func() {
		p.connMu.Lock()
		resumed := p.conn != nil
		p.connMu.Unlock()
		if !resumed {
			p.room.removeParticipant(p, tesserav1.DisconnectReason_DISCONNECT_REASON_SIGNAL_TIMEOUT, false)
		}
	})
	p.mu.Unlock()
}

// resume attaches a new signaling connection (PROTOCOL.md 2.6).
func (p *Participant) resume(ctx context.Context, conn signal.Conn) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		_ = conn.Close(signal.CloseProtocolError, "resume failed")
		return errs.New(errs.CodeNotFound, "participant closed")
	}
	if p.resumeT != nil {
		p.resumeT.Stop()
		p.resumeT = nil
	}
	p.mu.Unlock()
	p.connMu.Lock()
	old := p.conn
	// Swap the connection and its generation together: the old read loop's
	// onConnLost (gen mismatch) must not clear the new connection.
	p.conn = conn
	p.connGen++
	p.connMu.Unlock()
	if old != nil {
		_ = old.Close(signal.CloseGoingAway, "resumed elsewhere")
	}
	p.lastSeen.Store(time.Now().UnixNano())
	p.send(&tesserav1.SignalResponse{Message: &tesserav1.SignalResponse_Reconnect{Reconnect: &tesserav1.ReconnectResponse{
		IceServers: p.room.m.engine.ICEServers(p.sid), Participant: p.ToProto()}}})
	return p.run(ctx)
}

// close tears the participant down; it returns false if already closed.
func (p *Participant) close(reason tesserav1.DisconnectReason, sendLeave bool) bool {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return false
	}
	p.closed = true
	p.state = tesserav1.ParticipantInfo_STATE_DISCONNECTED
	if p.resumeT != nil {
		p.resumeT.Stop()
	}
	pub, sub := p.pub, p.sub
	if p.alloc != nil {
		p.alloc.Stop()
	}
	tracks := make([]*pubTrack, 0, len(p.tracks))
	for _, t := range p.tracks {
		tracks = append(tracks, t)
	}
	subs := p.subs
	p.subs = map[string]*subscription{}
	p.mu.Unlock()
	for _, s := range subs {
		s.dt.Close()
	}
	for _, t := range tracks {
		t.recv.Close()
	}
	if pub != nil {
		_ = pub.Close()
	}
	if sub != nil {
		_ = sub.Close()
	}
	if sendLeave {
		// On shutdown (restart, rolling deploy) clients should rejoin, not give up.
		action := tesserav1.LeaveRequest_ACTION_DISCONNECT
		if reason == tesserav1.DisconnectReason_DISCONNECT_REASON_SERVER_SHUTDOWN {
			action = tesserav1.LeaveRequest_ACTION_RECONNECT
		}
		p.send(&tesserav1.SignalResponse{Message: &tesserav1.SignalResponse_Leave{Leave: &tesserav1.LeaveRequest{
			Reason: reason, Action: action}}})
	}
	p.connMu.Lock()
	c := p.conn
	p.connMu.Unlock()
	if c != nil {
		code := signal.CloseNormal
		if reason == tesserav1.DisconnectReason_DISCONNECT_REASON_DUPLICATE_IDENTITY {
			code = signal.CloseDuplicateIdentity
		}
		_ = c.Close(code, reason.String())
	}
	return true
}

func (p *Participant) respond(reqID uint32, reason tesserav1.RequestResponse_Reason, msg string) {
	if reqID == 0 {
		return
	}
	p.send(&tesserav1.SignalResponse{Message: &tesserav1.SignalResponse_RequestResponse{RequestResponse: &tesserav1.RequestResponse{
		RequestId: reqID, Reason: reason, Message: msg}}})
}

// handle processes one request; it returns true on leave.
func (p *Participant) handle(req *tesserav1.SignalRequest) bool {
	id := req.GetRequestId()
	switch m := req.Message.(type) {
	case *tesserav1.SignalRequest_Ping:
		p.rttMs.Store(m.Ping.GetRtt())
		p.send(&tesserav1.SignalResponse{Message: &tesserav1.SignalResponse_Pong{Pong: &tesserav1.Pong{
			LastPingTimestamp: m.Ping.GetTimestamp(), Timestamp: time.Now().UnixMilli()}}})
	case *tesserav1.SignalRequest_Offer:
		p.handleOffer(m.Offer)
	case *tesserav1.SignalRequest_Answer:
		p.mu.Lock()
		sub := p.sub
		p.mu.Unlock()
		if sub != nil {
			if _, err := sub.HandleRemoteDescription(m.Answer); err != nil {
				p.room.m.logger.Warn("subscriber answer failed", "participant", p.identity, "err", err)
			}
		}
	case *tesserav1.SignalRequest_Trickle:
		c, err := rtc.ParseCandidate(m.Trickle.GetCandidateInit())
		if err != nil || m.Trickle.GetFinal() && c.Candidate == "" {
			return false
		}
		var t rtc.Transport
		if m.Trickle.GetTarget() == tesserav1.SignalTarget_SIGNAL_TARGET_PUBLISHER {
			t, _ = p.publisherTransport()
		} else {
			p.mu.Lock()
			t = p.sub
			p.mu.Unlock()
		}
		if t != nil {
			_ = t.AddICECandidate(c)
		}
	case *tesserav1.SignalRequest_AddTrack:
		p.addTrack(id, m.AddTrack)
	case *tesserav1.SignalRequest_Mute:
		if _, err := p.setTrackMuted(m.Mute.GetSid(), m.Mute.GetMuted()); err != nil {
			p.respond(id, tesserav1.RequestResponse_REASON_NOT_FOUND, err.Error())
			return false
		}
		p.room.broadcastParticipant(p)
	case *tesserav1.SignalRequest_Subscription:
		p.updateSubscription(m.Subscription)
	case *tesserav1.SignalRequest_TrackSetting:
		p.updateTrackSettings(m.TrackSetting)
	case *tesserav1.SignalRequest_Leave:
		return true
	case *tesserav1.SignalRequest_UnpublishTrack:
		p.unpublish(m.UnpublishTrack.GetTrackSid())
	case *tesserav1.SignalRequest_UpdateMetadata:
		if !p.permission().GetCanUpdateMetadata() {
			p.respond(id, tesserav1.RequestResponse_REASON_NOT_ALLOWED, "metadata update not allowed")
			return false
		}
		if !metadataWithinLimits(m.UpdateMetadata) {
			p.respond(id, tesserav1.RequestResponse_REASON_LIMIT_EXCEEDED, "metadata too large")
			return false
		}
		p.mu.Lock()
		if m.UpdateMetadata.GetName() != "" {
			p.name = m.UpdateMetadata.GetName()
		}
		p.metadata = m.UpdateMetadata.GetMetadata()
		if len(m.UpdateMetadata.GetAttributes()) > 0 {
			p.attributes = m.UpdateMetadata.GetAttributes()
		}
		p.mu.Unlock()
		p.room.broadcastParticipant(p)
	case *tesserav1.SignalRequest_SyncState:
		// ice_restart: the client's network path changed, so restart ICE on the
		// subscriber connection (the publisher restarts through a client offer).
		// Without the flag media keeps flowing undisturbed.
		if m.SyncState.GetIceRestart() {
			p.mu.Lock()
			sub := p.sub
			p.mu.Unlock()
			if sub != nil {
				sub.Negotiate(true)
			}
		}
		p.send(&tesserav1.SignalResponse{Message: &tesserav1.SignalResponse_Update{Update: &tesserav1.ParticipantUpdate{Participants: p.visibleOthers()}}})
	}
	p.respond(id, tesserav1.RequestResponse_REASON_OK, "")
	return false
}

func (p *Participant) visibleOthers() []*tesserav1.ParticipantInfo {
	var out []*tesserav1.ParticipantInfo
	for _, o := range p.room.Participants() {
		if o != p && !o.permission().GetHidden() {
			out = append(out, o.ToProto())
		}
	}
	return out
}

func (p *Participant) publisherTransport() (rtc.Transport, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pub != nil {
		return p.pub, nil
	}
	if p.closed {
		return nil, errs.New(errs.CodeUnavailable, "closed")
	}
	t, err := p.newTransport(tesserav1.SignalTarget_SIGNAL_TARGET_PUBLISHER)
	if err != nil {
		return nil, err
	}
	p.pub = t
	return t, nil
}

func (p *Participant) handleOffer(sd *tesserav1.SessionDescription) {
	if !p.permission().GetCanPublish() && !p.permission().GetCanPublishData() {
		return
	}
	t, err := p.publisherTransport()
	if err != nil {
		return
	}
	ans, err := t.HandleRemoteDescription(sd)
	if err != nil {
		p.room.m.logger.Warn("publisher offer failed", "participant", p.identity, "err", err)
		p.send(&tesserav1.SignalResponse{Message: &tesserav1.SignalResponse_Error{Error: &tesserav1.ErrorResponse{
			Code: tesserav1.ErrorCode_ERROR_CODE_NEGOTIATION_FAILED, Message: err.Error()}}})
		return
	}
	p.send(&tesserav1.SignalResponse{Message: &tesserav1.SignalResponse_Answer{Answer: ans}})
}

func (p *Participant) canPublishSource(src tesserav1.TrackSource) bool {
	perm := p.permission()
	if !perm.GetCanPublish() {
		return false
	}
	if len(perm.GetCanPublishSources()) == 0 {
		return true
	}
	for _, s := range perm.GetCanPublishSources() {
		if s == src {
			return true
		}
	}
	return false
}

// Per-participant limits, so one client cannot exhaust the node or flood the room.
const (
	maxTracksPerParticipant = 32       // published plus pending
	maxMetadataBytes        = 64 << 10 // name + metadata + attributes
	maxAttributes           = 100
)

func metadataWithinLimits(u *tesserav1.UpdateParticipantMetadata) bool {
	if len(u.GetAttributes()) > maxAttributes {
		return false
	}
	n := len(u.GetName()) + len(u.GetMetadata())
	for k, v := range u.GetAttributes() {
		n += len(k) + len(v)
	}
	return n <= maxMetadataBytes
}

func (p *Participant) addTrack(reqID uint32, req *tesserav1.AddTrackRequest) {
	if req.GetCid() == "" || req.GetType() == tesserav1.TrackType_TRACK_TYPE_UNSPECIFIED {
		p.respond(reqID, tesserav1.RequestResponse_REASON_INVALID, "cid and type required")
		return
	}
	if !p.canPublishSource(req.GetSource()) {
		p.respond(reqID, tesserav1.RequestResponse_REASON_NOT_ALLOWED, "publishing not allowed")
		return
	}
	p.mu.Lock()
	t := p.pending[req.GetCid()]
	if t == nil && len(p.pending) >= maxTracksPerParticipant {
		p.mu.Unlock()
		p.respond(reqID, tesserav1.RequestResponse_REASON_LIMIT_EXCEEDED, "too many tracks")
		return
	}
	if t == nil {
		info := &tesserav1.TrackInfo{
			Sid: newSID("TR_"), Type: req.GetType(), Name: req.GetName(), Muted: req.GetMuted(),
			Width: req.GetWidth(), Height: req.GetHeight(), Simulcast: len(req.GetLayers()) > 1,
			DisableDtx: req.GetDisableDtx(), Source: req.GetSource(), Layers: req.GetLayers(),
			Stereo: req.GetStereo(), Red: req.GetRed(), Stream: req.GetStream(),
		}
		t = &pubTrack{cid: req.GetCid(), info: info, enabled: -2}
		p.pending[req.GetCid()] = t
	}
	info := proto.Clone(t.info).(*tesserav1.TrackInfo)
	p.mu.Unlock()
	p.send(&tesserav1.SignalResponse{Message: &tesserav1.SignalResponse_TrackPublished{TrackPublished: &tesserav1.TrackPublishedResponse{
		Cid: req.GetCid(), Track: info}}})
}

// onTrack attaches an incoming track (or simulcast layer) to its Receiver.
func (p *Participant) onTrack(tr *webrtc.TrackRemote, rr *webrtc.RTPReceiver) {
	p.room.m.logger.Debug("track received", "participant", p.identity, "codec", tr.Codec().MimeType,
		"payload_type", tr.PayloadType(), "rid", tr.RID())
	kind := tesserav1.TrackType_TRACK_TYPE_AUDIO
	if tr.Kind() == webrtc.RTPCodecTypeVideo {
		kind = tesserav1.TrackType_TRACK_TYPE_VIDEO
	}
	p.mu.Lock()
	t := p.pending[tr.ID()]
	if t == nil {
		for _, c := range p.pending {
			if c.info.Type == kind {
				t = c
				break
			}
		}
	}
	if t == nil {
		p.mu.Unlock()
		p.room.m.logger.Warn("track without add_track", "participant", p.identity, "track", tr.ID())
		return
	}
	created := false
	if t.recv == nil {
		var alID uint8
		for _, e := range rr.GetParameters().HeaderExtensions {
			if e.URI == "urn:ietf:params:rtp-hdrext:ssrc-audio-level" {
				alID = uint8(e.ID)
			}
		}
		pub := p.pub
		t.recv = rtc.NewReceiver(rtc.ReceiverParams{
			TrackID: t.info.Sid, StreamID: p.sid, Kind: tr.Kind(), Codec: tr.Codec(), AudioLevelExtID: alID,
			WriteRTCP:   func(pkts []rtcp.Packet) error { return pub.WriteRTCP(pkts) },
			PLIThrottle: p.room.m.cfg.RTC.PLIThrottle, Logger: p.room.m.logger,
		})
		t.info.MimeType = tr.Codec().MimeType
		if strings.EqualFold(t.info.MimeType, rtc.MimeTypeRED) {
			t.info.MimeType = webrtc.MimeTypeOpus // RED is transport redundancy around Opus
		}
		t.info.Mid = rr.RTPTransceiver().Mid()
		created = true
		recv := t.recv
		sid := t.info.Sid
		recv.OnClosed(func() { p.onReceiverClosed(sid, recv) })
	}
	spatial := types.SpatialFromRID(tr.RID())
	recv := t.recv
	for _, l := range t.info.Layers {
		if l.GetRid() == tr.RID() || (tr.RID() == "" && len(t.info.Layers) == 1) {
			l.Ssrc = uint32(tr.SSRC())
		}
	}
	if created {
		p.tracks[t.info.Sid] = t
		if t.info.Muted {
			// Muted at publish time: downtracks start muted.
			_ = t
		}
	}
	p.mu.Unlock()
	if spatial < 0 {
		spatial = 0
	}
	if err := recv.AddLayer(spatial, rtc.TrackRemoteSource{T: tr}, nil); err != nil {
		return
	}
	go p.readPublisherRTCP(rr, tr.RID(), recv)
	if created {
		stats.TrackPublishes.Inc()
		p.room.onTrackPublished(p, t)
		go p.room.m.notify(tesserav1.WebhookEventType_WEBHOOK_EVENT_TYPE_TRACK_PUBLISHED, p.room, p, t.info)
	}
}

func (p *Participant) readPublisherRTCP(rr *webrtc.RTPReceiver, rid string, recv *rtc.ReceiverImpl) {
	for {
		var pkts []rtcp.Packet
		var err error
		if rid != "" {
			pkts, _, err = rr.ReadSimulcastRTCP(rid)
		} else {
			pkts, _, err = rr.ReadRTCP()
		}
		if err != nil {
			return
		}
		for _, pk := range pkts {
			if sr, ok := pk.(*rtcp.SenderReport); ok {
				recv.HandleSenderReport(sr)
			}
		}
	}
}

func (p *Participant) onReceiverClosed(sid string, recv *rtc.ReceiverImpl) {
	p.mu.Lock()
	t := p.tracks[sid]
	if t == nil || t.recv != recv {
		p.mu.Unlock()
		return
	}
	delete(p.tracks, sid)
	delete(p.pending, t.cid)
	closed := p.closed
	p.mu.Unlock()
	p.room.removeSpeakerSource(sid)
	if closed {
		return
	}
	for _, o := range p.room.Participants() {
		o.unsubscribe(sid)
	}
	p.room.broadcastParticipant(p)
	go p.room.m.notify(tesserav1.WebhookEventType_WEBHOOK_EVENT_TYPE_TRACK_UNPUBLISHED, p.room, p, t.info)
}

func (p *Participant) unpublish(sid string) {
	p.mu.Lock()
	t := p.tracks[sid]
	p.mu.Unlock()
	if t != nil {
		t.recv.Close() // triggers onReceiverClosed
	}
}

func (p *Participant) publishedTracks() []*pubTrack {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]*pubTrack, 0, len(p.tracks))
	for _, t := range p.tracks {
		out = append(out, t)
	}
	return out
}

func (p *Participant) getTrack(sid string) *pubTrack {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.tracks[sid]
}

func (p *Participant) setTrackMuted(sid string, muted bool) (*tesserav1.TrackInfo, error) {
	p.mu.Lock()
	t := p.tracks[sid]
	if t == nil {
		for _, pt := range p.pending {
			if pt.info.Sid == sid {
				pt.info.Muted = muted
				info := proto.Clone(pt.info).(*tesserav1.TrackInfo)
				p.mu.Unlock()
				return info, nil
			}
		}
		p.mu.Unlock()
		return nil, errs.New(errs.CodeNotFound, "track %s not found", sid)
	}
	t.info.Muted = muted
	info := proto.Clone(t.info).(*tesserav1.TrackInfo)
	p.mu.Unlock()
	for _, o := range p.room.Participants() {
		o.applyMute(sid)
	}
	return info, nil
}

// applyMute recomputes the mute state of a subscription.
func (p *Participant) applyMute(trackSID string) {
	p.mu.Lock()
	s := p.subs[trackSID]
	p.mu.Unlock()
	if s == nil {
		return
	}
	t := s.pub.getTrack(trackSID)
	pubMuted := t != nil && s.pub.trackMuted(trackSID)
	s.dt.Mute(pubMuted || s.disabled)
}

func (p *Participant) trackMuted(sid string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if t := p.tracks[sid]; t != nil {
		return t.info.Muted
	}
	return false
}

func (p *Participant) wantsTrack(sid string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || !p.perm.GetCanSubscribe() {
		return false
	}
	return p.autoSubscribe || p.explicit[sid]
}

func (p *Participant) subscribe(pub *Participant, t *pubTrack) {
	p.mu.Lock()
	if p.closed || p.sub == nil || !p.perm.GetCanSubscribe() || p.subs[t.info.Sid] != nil || t.recv == nil {
		p.mu.Unlock()
		return
	}
	prio := uint32(1)
	if t.info.Source == tesserav1.TrackSource_TRACK_SOURCE_SCREEN_SHARE {
		prio = 2
	}
	dt := rtc.NewDownTrack(rtc.DownTrackParams{SubscriberID: p.sid, Receiver: t.recv, TrackID: t.info.Sid, StreamID: pub.sid, TWCC: p.twcc, Priority: prio})
	s := &subscription{dt: dt, pub: pub}
	p.subs[t.info.Sid] = s
	sub := p.sub
	alloc := p.alloc
	p.mu.Unlock()
	if alloc != nil {
		alloc.AddTrack(dt)
	}
	sid := t.info.Sid
	dt.OnFirstPacket(func() {
		p.send(&tesserav1.SignalResponse{Message: &tesserav1.SignalResponse_TrackSubscribed{TrackSubscribed: &tesserav1.TrackSubscribed{TrackSid: sid}}})
	})
	if pub.trackMuted(sid) {
		dt.Mute(true)
	}
	if err := sub.AddDownTrack(dt); err != nil {
		p.mu.Lock()
		delete(p.subs, sid)
		p.mu.Unlock()
		return
	}
	if err := t.recv.AddDownTrack(dt); err != nil {
		p.unsubscribe(sid)
		return
	}
	sub.Negotiate(false)
}

func (p *Participant) unsubscribe(sid string) {
	p.mu.Lock()
	s := p.subs[sid]
	delete(p.subs, sid)
	sub := p.sub
	closed := p.closed
	alloc := p.alloc
	p.mu.Unlock()
	if s == nil {
		return
	}
	if alloc != nil {
		alloc.RemoveTrack(sid)
	}
	s.dt.Close()
	if sub != nil && !closed {
		_ = sub.RemoveDownTrack(s.dt)
		sub.Negotiate(false)
	}
}

func (p *Participant) updateSubscription(u *tesserav1.UpdateSubscription) {
	sids := append([]string{}, u.GetTrackSids()...)
	for _, pt := range u.GetParticipantTracks() {
		sids = append(sids, pt.GetTrackSids()...)
	}
	for _, sid := range sids {
		if !u.GetSubscribe() {
			p.mu.Lock()
			delete(p.explicit, sid)
			p.mu.Unlock()
			p.unsubscribe(sid)
			continue
		}
		pub, t := p.room.findTrack(sid)
		if t == nil {
			p.send(&tesserav1.SignalResponse{Message: &tesserav1.SignalResponse_SubscriptionResponse{SubscriptionResponse: &tesserav1.SubscriptionResponse{
				TrackSid: sid, Err: tesserav1.SubscriptionError_SUBSCRIPTION_ERROR_TRACK_NOT_FOUND}}})
			continue
		}
		if !p.permission().GetCanSubscribe() {
			p.send(&tesserav1.SignalResponse{Message: &tesserav1.SignalResponse_SubscriptionResponse{SubscriptionResponse: &tesserav1.SubscriptionResponse{
				TrackSid: sid, Err: tesserav1.SubscriptionError_SUBSCRIPTION_ERROR_PERMISSION_DENIED}}})
			continue
		}
		p.mu.Lock()
		p.explicit[sid] = true
		p.mu.Unlock()
		if pub != p {
			p.subscribe(pub, t)
		}
	}
}

func (p *Participant) updateTrackSettings(s *tesserav1.UpdateTrackSettings) {
	for _, sid := range s.GetTrackSids() {
		p.mu.Lock()
		sub := p.subs[sid]
		if sub != nil {
			sub.disabled = s.GetDisabled()
		}
		p.mu.Unlock()
		if sub == nil {
			continue
		}
		p.applyMute(sid)
		if pr := s.GetPriority(); pr > 0 {
			sub.dt.SetPriority(pr)
		}
		if sub.dt.Kind() != webrtc.RTPCodecTypeVideo {
			continue
		}
		spatial := int32(types.MaxSpatialLayers - 1)
		if q := s.GetQuality(); q != tesserav1.VideoQuality_VIDEO_QUALITY_UNSPECIFIED {
			spatial = types.SpatialForQuality(q)
		} else if s.GetWidth() > 0 || s.GetHeight() > 0 {
			spatial = layerForSize(sub.pub.getTrack(sid), s.GetWidth(), s.GetHeight())
		}
		temporal := int32(types.MaxTemporalLayers - 1)
		if fps := s.GetFps(); fps > 0 {
			switch {
			case fps <= 8:
				temporal = 0
			case fps <= 16:
				temporal = 1
			}
		}
		if spatial < 0 { // OFF
			sub.dt.SetMaxLayer(types.InvalidVideoLayer)
			continue
		}
		sub.dt.SetMaxLayer(types.VideoLayer{Spatial: spatial, Temporal: temporal})
	}
}

// layerForSize picks the smallest layer whose size is >= the requested size.
func layerForSize(t *pubTrack, w, h uint32) int32 {
	if t == nil || len(t.info.GetLayers()) == 0 {
		return types.MaxSpatialLayers - 1
	}
	best := int32(-1)
	for _, l := range t.info.GetLayers() {
		s := types.SpatialForQuality(l.GetQuality())
		if s < 0 {
			continue
		}
		if l.GetWidth() >= w && l.GetHeight() >= h && (best < 0 || s < best) {
			best = s
		}
	}
	if best < 0 {
		return types.MaxSpatialLayers - 1
	}
	return best
}

func (p *Participant) update(req *tesserav1.UpdateParticipantRequest) {
	p.mu.Lock()
	if req.Metadata != nil {
		p.metadata = req.GetMetadata()
	}
	if req.Name != nil {
		p.name = req.GetName()
	}
	if len(req.GetAttributes()) > 0 {
		p.attributes = req.GetAttributes()
	}
	var unpublish, unsubscribe bool
	if req.GetPermission() != nil {
		unpublish = p.perm.GetCanPublish() && !req.GetPermission().GetCanPublish()
		unsubscribe = p.perm.GetCanSubscribe() && !req.GetPermission().GetCanSubscribe()
		p.perm = req.GetPermission()
	}
	p.mu.Unlock()
	if unpublish {
		for _, t := range p.publishedTracks() {
			p.unpublish(t.info.Sid)
		}
	}
	if unsubscribe {
		p.mu.Lock()
		sids := make([]string, 0, len(p.subs))
		for sid := range p.subs {
			sids = append(sids, sid)
		}
		p.mu.Unlock()
		for _, sid := range sids {
			p.unsubscribe(sid)
		}
	}
}

// onStreamStates tells the subscriber which video streams the allocator
// paused or resumed.
func (p *Participant) onStreamStates(changed []bwe.Allocation) {
	upd := &tesserav1.StreamStateUpdate{}
	for _, a := range changed {
		st := tesserav1.StreamState_STREAM_STATE_ACTIVE
		if a.Paused {
			st = tesserav1.StreamState_STREAM_STATE_PAUSED
		}
		p.mu.Lock()
		s := p.subs[a.TrackID]
		p.mu.Unlock()
		if s == nil {
			continue
		}
		upd.StreamStates = append(upd.StreamStates, &tesserav1.StreamStateInfo{ParticipantSid: s.pub.sid, TrackSid: a.TrackID, State: st})
	}
	if len(upd.StreamStates) > 0 {
		p.send(&tesserav1.SignalResponse{Message: &tesserav1.SignalResponse_StreamStateUpdate{StreamStateUpdate: upd}})
	}
}

// onDataChannel forwards DataPackets from the client's publisher data
// channels to the other participants' subscriber data channels.
func (p *Participant) onDataChannel(dc *webrtc.DataChannel) {
	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		if !p.permission().GetCanPublishData() {
			return
		}
		pkt := &tesserav1.DataPacket{}
		if err := proto.Unmarshal(msg.Data, pkt); err != nil {
			return
		}
		if pkt.Kind == tesserav1.DataPacket_KIND_UNSPECIFIED {
			pkt.Kind = tesserav1.DataPacket_KIND_RELIABLE
			if dc.Label() == rtc.LabelLossy {
				pkt.Kind = tesserav1.DataPacket_KIND_LOSSY
			}
		}
		pkt.ParticipantIdentity = p.identity
		dest := map[string]bool{}
		for _, d := range pkt.DestinationIdentities {
			dest[d] = true
		}
		pkt.DestinationIdentities = nil
		data, err := proto.Marshal(pkt)
		if err != nil {
			return
		}
		for _, o := range p.room.Participants() {
			if o == p || (len(dest) > 0 && !dest[o.identity]) || !o.permission().GetCanSubscribe() {
				continue
			}
			o.mu.Lock()
			sub := o.sub
			o.mu.Unlock()
			if sub != nil {
				_ = sub.SendDataPacket(pkt.Kind, data)
			}
		}
	})
}
