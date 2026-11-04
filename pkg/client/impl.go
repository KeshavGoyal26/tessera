package client

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/pion/ice/v4"
	"github.com/pion/interceptor"
	"github.com/pion/logging"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
	"google.golang.org/protobuf/proto"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/errs"
	"github.com/KeshavGoyal26/tessera/pkg/rtc"
	"github.com/KeshavGoyal26/tessera/pkg/version"
)

// RemoteTrack is a subscribed track. It satisfies recorder.RTPSource.
type RemoteTrack struct {
	SID                 string
	ParticipantIdentity string
	ParticipantSID      string
	Info                *tesserav1.TrackInfo
	track               *webrtc.TrackRemote
	pc                  *webrtc.PeerConnection
}

// Read reads one RTP packet into b.
func (t *RemoteTrack) Read(b []byte) (int, error) {
	if t.track == nil {
		return 0, errs.New(errs.CodeFailedPrecondition, "track not bound")
	}
	n, _, err := t.track.Read(b)
	return n, err
}

// Codec returns the negotiated codec.
func (t *RemoteTrack) Codec() webrtc.RTPCodecParameters {
	if t.track == nil {
		return webrtc.RTPCodecParameters{}
	}
	return t.track.Codec()
}

// RequestKeyFrame sends a PLI for this track.
func (t *RemoteTrack) RequestKeyFrame() error {
	if t.track == nil || t.pc == nil {
		return errs.New(errs.CodeFailedPrecondition, "track not bound")
	}
	return t.pc.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: uint32(t.track.SSRC())}})
}

// TrackRemote exposes the underlying Pion track.
func (t *RemoteTrack) TrackRemote() *webrtc.TrackRemote { return t.track }

// LoopbackSettingEngine returns a SettingEngine restricted to 127.0.0.1 UDP,
// for tests and local load tests against a server configured with
// rtc.ips: [127.0.0.1].
func LoopbackSettingEngine() *webrtc.SettingEngine {
	se := &webrtc.SettingEngine{}
	se.SetIncludeLoopbackCandidate(true)
	se.SetIPFilter(func(ip net.IP) bool { return ip.IsLoopback() && ip.To4() != nil })
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	se.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	return se
}

type client struct {
	opts Options
	api  *webrtc.API

	ws    *websocket.Conn
	wsMu  sync.Mutex
	ctx   context.Context
	stop  context.CancelFunc
	reqID atomic.Uint32

	mu        sync.Mutex
	cb        Callbacks
	room      *tesserav1.Room
	local     *tesserav1.ParticipantInfo
	remotes   map[string]*tesserav1.ParticipantInfo // by sid
	pub, sub  *webrtc.PeerConnection
	pubRemote bool
	subRemote bool
	pubCands  []webrtc.ICECandidateInit
	subCands  []webrtc.ICECandidateInit
	pending   map[string]chan *tesserav1.TrackInfo // by cid
	answerCh  chan *tesserav1.SessionDescription
	offerID   uint32
	tracks    map[string]*RemoteTrack // by track sid
	waiting   map[string][]chan *RemoteTrack
	pubMu     sync.Mutex // serialises publisher negotiation
	rtt       atomic.Int64
	closed    atomic.Bool
	done      chan struct{}
}

// New creates a client.
func New(opts Options) Client {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.SDK == tesserav1.ClientInfo_SDK_UNSPECIFIED {
		opts.SDK = tesserav1.ClientInfo_SDK_GO
	}
	return &client{opts: opts, remotes: map[string]*tesserav1.ParticipantInfo{}, pending: map[string]chan *tesserav1.TrackInfo{},
		tracks: map[string]*RemoteTrack{}, waiting: map[string][]chan *RemoteTrack{}, answerCh: make(chan *tesserav1.SessionDescription, 4), done: make(chan struct{})}
}

func (c *client) buildAPI() error {
	codecs := c.opts.Codecs
	if codecs == nil {
		codecs = rtc.Codecs(nil)
	}
	me, err := rtc.NewMediaEngine(codecs, true)
	if err != nil {
		return err
	}
	ir := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(me, ir); err != nil {
		return err
	}
	se := webrtc.SettingEngine{}
	if c.opts.SettingEngine != nil {
		se = *c.opts.SettingEngine
	}
	if se.LoggerFactory == nil {
		lf := logging.NewDefaultLoggerFactory()
		lf.DefaultLogLevel = logging.LogLevelError
		se.LoggerFactory = lf
	}
	c.api = webrtc.NewAPI(webrtc.WithMediaEngine(me), webrtc.WithSettingEngine(se), webrtc.WithInterceptorRegistry(ir))
	return nil
}

func sdkName(s tesserav1.ClientInfo_SDK) string {
	return strings.ToLower(strings.TrimPrefix(s.String(), "SDK_"))
}

// Connect implements Client.
func (c *client) Connect(ctx context.Context, rawURL, token string) error {
	if err := c.buildAPI(); err != nil {
		return err
	}
	u, err := url.Parse(strings.TrimSuffix(rawURL, "/") + "/rtc")
	if err != nil {
		return err
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	}
	q := u.Query()
	q.Set("access_token", token)
	q.Set("protocol", fmt.Sprint(version.ProtocolVersion))
	q.Set("sdk", sdkName(c.opts.SDK))
	q.Set("version", version.Version)
	if c.opts.AutoSubscribe {
		q.Set("auto_subscribe", "1")
	} else {
		q.Set("auto_subscribe", "0")
	}
	u.RawQuery = q.Encode()
	ws, resp, err := websocket.Dial(ctx, u.String(), nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		if resp != nil {
			return fmt.Errorf("client: connect: HTTP %d: %w", resp.StatusCode, err)
		}
		return fmt.Errorf("client: connect: %w", err)
	}
	ws.SetReadLimit(1 << 20)
	c.ws = ws
	c.ctx, c.stop = context.WithCancel(context.Background())

	first, err := c.read(ctx)
	if err != nil {
		_ = ws.CloseNow()
		return err
	}
	join := first.GetJoin()
	if join == nil {
		_ = ws.CloseNow()
		return errs.New(errs.CodeInternal, "first message was not join")
	}
	c.mu.Lock()
	c.room, c.local = join.Room, join.Participant
	for _, p := range join.OtherParticipants {
		c.remotes[p.Sid] = p
	}
	c.mu.Unlock()
	var iceServers []webrtc.ICEServer
	for _, s := range join.IceServers {
		iceServers = append(iceServers, webrtc.ICEServer{URLs: s.Urls, Username: s.Username, Credential: s.Credential})
	}
	cfg := webrtc.Configuration{ICEServers: iceServers}
	if c.sub, err = c.newPC(cfg, tesserav1.SignalTarget_SIGNAL_TARGET_SUBSCRIBER); err != nil {
		return err
	}
	if c.pub, err = c.newPC(cfg, tesserav1.SignalTarget_SIGNAL_TARGET_PUBLISHER); err != nil {
		return err
	}
	c.sub.OnTrack(c.onTrack)
	go c.readLoop()
	go c.pingLoop(time.Duration(max(join.PingInterval, 1)) * time.Second)
	return nil
}

func (c *client) newPC(cfg webrtc.Configuration, target tesserav1.SignalTarget) (*webrtc.PeerConnection, error) {
	pc, err := c.api.NewPeerConnection(cfg)
	if err != nil {
		return nil, err
	}
	pc.OnICECandidate(func(cand *webrtc.ICECandidate) {
		if cand == nil {
			return
		}
		init := cand.ToJSON()
		_ = c.send(&tesserav1.SignalRequest{Message: &tesserav1.SignalRequest_Trickle{Trickle: &tesserav1.TrickleRequest{
			CandidateInit: rtc.MarshalCandidate(&init), Target: target}}})
	})
	return pc, nil
}

func (c *client) read(ctx context.Context) (*tesserav1.SignalResponse, error) {
	_, data, err := c.ws.Read(ctx)
	if err != nil {
		return nil, err
	}
	msg := &tesserav1.SignalResponse{}
	if err := proto.Unmarshal(data, msg); err != nil {
		return nil, err
	}
	return msg, nil
}

func (c *client) send(req *tesserav1.SignalRequest) error {
	data, err := proto.Marshal(req)
	if err != nil {
		return err
	}
	c.wsMu.Lock()
	defer c.wsMu.Unlock()
	if c.closed.Load() {
		return errs.New(errs.CodeUnavailable, "client closed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return c.ws.Write(ctx, websocket.MessageBinary, data)
}

func (c *client) pingLoop(every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-t.C:
			_ = c.send(&tesserav1.SignalRequest{Message: &tesserav1.SignalRequest_Ping{Ping: &tesserav1.Ping{
				Timestamp: time.Now().UnixMilli(), Rtt: c.rtt.Load()}}})
		}
	}
}

func (c *client) readLoop() {
	defer close(c.done)
	for {
		msg, err := c.read(c.ctx)
		if err != nil {
			if !c.closed.Load() {
				c.mu.Lock()
				cb := c.cb.OnDisconnected
				c.mu.Unlock()
				if cb != nil {
					cb(tesserav1.DisconnectReason_DISCONNECT_REASON_SIGNAL_TIMEOUT)
				}
			}
			return
		}
		c.handle(msg)
	}
}

func (c *client) handle(msg *tesserav1.SignalResponse) {
	c.mu.Lock()
	cb := c.cb
	c.mu.Unlock()
	switch m := msg.Message.(type) {
	case *tesserav1.SignalResponse_Offer:
		c.handleOffer(m.Offer)
	case *tesserav1.SignalResponse_Answer:
		select {
		case c.answerCh <- m.Answer:
		default:
		}
	case *tesserav1.SignalResponse_Trickle:
		cand, err := rtc.ParseCandidate(m.Trickle.GetCandidateInit())
		if err != nil {
			return
		}
		c.mu.Lock()
		if m.Trickle.GetTarget() == tesserav1.SignalTarget_SIGNAL_TARGET_PUBLISHER {
			if c.pubRemote {
				_ = c.pub.AddICECandidate(cand)
			} else {
				c.pubCands = append(c.pubCands, cand)
			}
		} else {
			if c.subRemote {
				_ = c.sub.AddICECandidate(cand)
			} else {
				c.subCands = append(c.subCands, cand)
			}
		}
		c.mu.Unlock()
	case *tesserav1.SignalResponse_Update:
		c.mu.Lock()
		for _, p := range m.Update.Participants {
			if c.local != nil && p.Sid == c.local.Sid {
				c.local = p
				continue
			}
			if p.State == tesserav1.ParticipantInfo_STATE_DISCONNECTED {
				delete(c.remotes, p.Sid)
			} else {
				c.remotes[p.Sid] = p
			}
		}
		c.mu.Unlock()
		if cb.OnParticipantUpdate != nil {
			cb.OnParticipantUpdate(m.Update.Participants)
		}
	case *tesserav1.SignalResponse_TrackPublished:
		c.mu.Lock()
		ch := c.pending[m.TrackPublished.GetCid()]
		delete(c.pending, m.TrackPublished.GetCid())
		c.mu.Unlock()
		if ch != nil {
			ch <- m.TrackPublished.GetTrack()
		}
	case *tesserav1.SignalResponse_Pong:
		if ts := m.Pong.GetLastPingTimestamp(); ts > 0 {
			c.rtt.Store(time.Now().UnixMilli() - ts)
		}
	case *tesserav1.SignalResponse_SpeakersChanged:
		if cb.OnSpeakersChanged != nil {
			cb.OnSpeakersChanged(m.SpeakersChanged.Speakers)
		}
	case *tesserav1.SignalResponse_StreamStateUpdate:
		if cb.OnStreamStateUpdate != nil {
			cb.OnStreamStateUpdate(m.StreamStateUpdate)
		}
	case *tesserav1.SignalResponse_SubscribedQualityUpdate:
		if cb.OnSubscribedQuality != nil {
			cb.OnSubscribedQuality(m.SubscribedQualityUpdate)
		}
	case *tesserav1.SignalResponse_ConnectionQuality:
		if cb.OnConnectionQuality != nil {
			cb.OnConnectionQuality(m.ConnectionQuality)
		}
	case *tesserav1.SignalResponse_Leave:
		c.closed.Store(true)
		if cb.OnDisconnected != nil {
			cb.OnDisconnected(m.Leave.GetReason())
		}
	case *tesserav1.SignalResponse_TrackUnpublished:
	}
}

func (c *client) handleOffer(sd *tesserav1.SessionDescription) {
	if err := c.sub.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: sd.GetSdp()}); err != nil {
		c.opts.Logger.Warn("client: subscriber offer", "err", err)
		return
	}
	c.mu.Lock()
	c.subRemote = true
	for _, cand := range c.subCands {
		_ = c.sub.AddICECandidate(cand)
	}
	c.subCands = nil
	c.mu.Unlock()
	ans, err := c.sub.CreateAnswer(nil)
	if err == nil {
		err = c.sub.SetLocalDescription(ans)
	}
	if err != nil {
		c.opts.Logger.Warn("client: subscriber answer", "err", err)
		return
	}
	_ = c.send(&tesserav1.SignalRequest{Message: &tesserav1.SignalRequest_Answer{Answer: &tesserav1.SessionDescription{
		Type: "answer", Sdp: c.sub.LocalDescription().SDP, Id: sd.GetId()}}})
}

func (c *client) onTrack(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
	c.mu.Lock()
	rt := &RemoteTrack{SID: tr.ID(), ParticipantSID: tr.StreamID(), track: tr, pc: c.sub}
	if p := c.remotes[tr.StreamID()]; p != nil {
		rt.ParticipantIdentity = p.Identity
		for _, t := range p.Tracks {
			if t.Sid == tr.ID() {
				rt.Info = t
			}
		}
	}
	c.tracks[rt.SID] = rt
	waiters := c.waiting[rt.SID]
	delete(c.waiting, rt.SID)
	cb := c.cb.OnTrackSubscribed
	c.mu.Unlock()
	for _, w := range waiters {
		w <- rt
	}
	if cb != nil {
		cb(rt)
	}
}

// WaitTrack waits until the track with sid is subscribed (helper for tests
// and tools; not part of the Client interface).
func WaitTrack(ctx context.Context, cl Client, sid string) (*RemoteTrack, error) {
	c, ok := cl.(*client)
	if !ok {
		return nil, errs.New(errs.CodeInvalidArgument, "unsupported client")
	}
	ch := make(chan *RemoteTrack, 1)
	c.mu.Lock()
	if t := c.tracks[sid]; t != nil {
		c.mu.Unlock()
		return t, nil
	}
	c.waiting[sid] = append(c.waiting[sid], ch)
	c.mu.Unlock()
	select {
	case t := <-ch:
		return t, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Room implements Client.
func (c *client) Room() *tesserav1.Room {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.room
}

// LocalParticipant implements Client.
func (c *client) LocalParticipant() *tesserav1.ParticipantInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.local
}

// RemoteParticipants implements Client.
func (c *client) RemoteParticipants() []*tesserav1.ParticipantInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*tesserav1.ParticipantInfo, 0, len(c.remotes))
	for _, p := range c.remotes {
		out = append(out, p)
	}
	return out
}

func (c *client) addTrack(ctx context.Context, req *tesserav1.AddTrackRequest) (*tesserav1.TrackInfo, error) {
	ch := make(chan *tesserav1.TrackInfo, 1)
	c.mu.Lock()
	c.pending[req.Cid] = ch
	c.mu.Unlock()
	if err := c.send(&tesserav1.SignalRequest{RequestId: c.reqID.Add(1), Message: &tesserav1.SignalRequest_AddTrack{AddTrack: req}}); err != nil {
		return nil, err
	}
	select {
	case info := <-ch:
		return info, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func trackType(k webrtc.RTPCodecType) tesserav1.TrackType {
	if k == webrtc.RTPCodecTypeAudio {
		return tesserav1.TrackType_TRACK_TYPE_AUDIO
	}
	return tesserav1.TrackType_TRACK_TYPE_VIDEO
}

func defaultSource(k webrtc.RTPCodecType, s tesserav1.TrackSource) tesserav1.TrackSource {
	if s != tesserav1.TrackSource_TRACK_SOURCE_UNSPECIFIED {
		return s
	}
	if k == webrtc.RTPCodecTypeAudio {
		return tesserav1.TrackSource_TRACK_SOURCE_MICROPHONE
	}
	return tesserav1.TrackSource_TRACK_SOURCE_CAMERA
}

// PublishTrack implements Client.
func (c *client) PublishTrack(ctx context.Context, track webrtc.TrackLocal, opts PublishOptions) (*tesserav1.TrackInfo, error) {
	return c.PublishSimulcastTrack(ctx, []webrtc.TrackLocal{track}, opts)
}

// PublishSimulcastTrack implements Client.
func (c *client) PublishSimulcastTrack(ctx context.Context, layers []webrtc.TrackLocal, opts PublishOptions) (*tesserav1.TrackInfo, error) {
	if len(layers) == 0 {
		return nil, errs.New(errs.CodeInvalidArgument, "no track")
	}
	first := layers[0]
	req := &tesserav1.AddTrackRequest{Cid: first.ID(), Name: opts.Name, Type: trackType(first.Kind()), Width: opts.Width, Height: opts.Height,
		Source: defaultSource(first.Kind(), opts.Source), DisableDtx: opts.DisableDTX, Stereo: opts.Stereo, Red: opts.RED, Stream: first.StreamID()}
	if len(layers) > 1 {
		for i, l := range layers {
			q := tesserav1.VideoQuality(i + 1)
			div := uint32(1) << (len(layers) - 1 - i)
			req.Layers = append(req.Layers, &tesserav1.VideoLayer{Quality: q, Rid: l.RID(), Width: opts.Width / div, Height: opts.Height / div})
		}
	}
	info, err := c.addTrack(ctx, req)
	if err != nil {
		return nil, err
	}
	tr, err := c.pub.AddTransceiverFromTrack(first, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendonly})
	if err != nil {
		return nil, err
	}
	for _, l := range layers[1:] {
		if err := tr.Sender().AddEncoding(l); err != nil {
			return nil, err
		}
	}
	go func() {
		buf := make([]byte, 1500)
		for {
			if _, _, err := tr.Sender().Read(buf); err != nil {
				return
			}
		}
	}()
	if err := c.negotiatePublisher(ctx); err != nil {
		return nil, err
	}
	return info, nil
}

func (c *client) negotiatePublisher(ctx context.Context) error {
	c.pubMu.Lock()
	defer c.pubMu.Unlock()
	offer, err := c.pub.CreateOffer(nil)
	if err != nil {
		return err
	}
	if err := c.pub.SetLocalDescription(offer); err != nil {
		return err
	}
	c.offerID++
	id := c.offerID
	if err := c.send(&tesserav1.SignalRequest{Message: &tesserav1.SignalRequest_Offer{Offer: &tesserav1.SessionDescription{
		Type: "offer", Sdp: c.pub.LocalDescription().SDP, Id: id}}}); err != nil {
		return err
	}
	for {
		select {
		case ans := <-c.answerCh:
			if ans.GetId() != id {
				continue
			}
			if err := c.pub.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: ans.GetSdp()}); err != nil {
				return err
			}
			c.mu.Lock()
			c.pubRemote = true
			for _, cand := range c.pubCands {
				_ = c.pub.AddICECandidate(cand)
			}
			c.pubCands = nil
			c.mu.Unlock()
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// UnpublishTrack implements Client.
func (c *client) UnpublishTrack(ctx context.Context, trackSID string) error {
	c.mu.Lock()
	var cid string
	for _, t := range c.local.GetTracks() {
		if t.Sid == trackSID {
			cid = t.Sid
		}
	}
	c.mu.Unlock()
	_ = cid
	if err := c.send(&tesserav1.SignalRequest{Message: &tesserav1.SignalRequest_UnpublishTrack{UnpublishTrack: &tesserav1.UnpublishTrackRequest{TrackSid: trackSID}}}); err != nil {
		return err
	}
	return nil
}

// SetTrackMuted implements Client.
func (c *client) SetTrackMuted(_ context.Context, trackSID string, muted bool) error {
	return c.send(&tesserav1.SignalRequest{Message: &tesserav1.SignalRequest_Mute{Mute: &tesserav1.MuteTrackRequest{Sid: trackSID, Muted: muted}}})
}

// Subscribe implements Client.
func (c *client) Subscribe(_ context.Context, sids ...string) error {
	return c.send(&tesserav1.SignalRequest{Message: &tesserav1.SignalRequest_Subscription{Subscription: &tesserav1.UpdateSubscription{TrackSids: sids, Subscribe: true}}})
}

// Unsubscribe implements Client.
func (c *client) Unsubscribe(_ context.Context, sids ...string) error {
	c.mu.Lock()
	for _, s := range sids {
		delete(c.tracks, s)
	}
	c.mu.Unlock()
	return c.send(&tesserav1.SignalRequest{Message: &tesserav1.SignalRequest_Subscription{Subscription: &tesserav1.UpdateSubscription{TrackSids: sids}}})
}

// UpdateTrackSettings implements Client.
func (c *client) UpdateTrackSettings(_ context.Context, s *tesserav1.UpdateTrackSettings) error {
	return c.send(&tesserav1.SignalRequest{Message: &tesserav1.SignalRequest_TrackSetting{TrackSetting: s}})
}

// PublishData implements Client (data channels arrive in a later milestone).
func (c *client) PublishData(context.Context, *tesserav1.DataPacket) error {
	return errs.NotImplemented("client.PublishData")
}

// SetCallbacks implements Client.
func (c *client) SetCallbacks(cb Callbacks) {
	c.mu.Lock()
	c.cb = cb
	c.mu.Unlock()
}

// SignalRTT implements Client.
func (c *client) SignalRTT() int64 { return c.rtt.Load() }

// Disconnect implements Client.
func (c *client) Disconnect(context.Context) error {
	if c.ws == nil {
		return nil
	}
	_ = c.send(&tesserav1.SignalRequest{Message: &tesserav1.SignalRequest_Leave{Leave: &tesserav1.LeaveRequest{
		Reason: tesserav1.DisconnectReason_DISCONNECT_REASON_CLIENT_INITIATED}}})
	c.closed.Store(true)
	c.wsMu.Lock()
	err := c.ws.Close(websocket.StatusNormalClosure, "")
	c.wsMu.Unlock()
	if c.stop != nil {
		c.stop()
	}
	if c.pub != nil {
		_ = c.pub.Close()
	}
	if c.sub != nil {
		_ = c.sub.Close()
	}
	if err != nil && !errors.Is(err, net.ErrClosed) && websocket.CloseStatus(err) == -1 {
		return nil
	}
	return nil
}

// DropConnection closes the WebSocket without leaving (tests resume).
func DropConnection(cl Client) {
	if c, ok := cl.(*client); ok && c.ws != nil {
		_ = c.ws.CloseNow()
	}
}
