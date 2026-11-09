package rtc

import (
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/errs"
	"github.com/KeshavGoyal26/tessera/pkg/stats"
)

// NegotiationDebounce batches subscriber renegotiations (ADR-0002).
var NegotiationDebounce = 50 * time.Millisecond

type transport struct {
	e       *engine
	pc      *webrtc.PeerConnection
	target  tesserav1.SignalTarget
	handler TransportHandler
	logger  *slog.Logger

	mu            sync.Mutex
	remoteSet     bool
	pending       []webrtc.ICECandidateInit
	offerID       uint32
	awaitAnswer   bool
	renegotiate   bool
	restartNext   bool
	timer         *time.Timer
	closed        bool
	senders       map[DownTrack]*webrtc.RTPSender
	dcs           map[string]*webrtc.DataChannel
	created       time.Time
	iceConnected  time.Time
	dtlsConnected time.Time
}

// NewTransport implements Engine.
func (e *engine) NewTransport(p TransportParams) (Transport, error) {
	api := e.subAPI
	if p.Target == tesserav1.SignalTarget_SIGNAL_TARGET_PUBLISHER {
		api = e.pubAPI
	}
	pc, err := api.NewPeerConnection(webrtc.Configuration{SDPSemantics: webrtc.SDPSemanticsUnifiedPlan})
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "new peer connection")
	}
	e.pcCount.Add(1)
	logger := p.Logger
	if logger == nil {
		logger = e.logger
	}
	t := &transport{e: e, pc: pc, target: p.Target, handler: p.Handler, logger: logger, senders: map[DownTrack]*webrtc.RTPSender{}, created: time.Now()}
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		init := c.ToJSON()
		t.handler.OnICECandidate(&init, t.target)
	})
	pc.OnICEConnectionStateChange(func(s webrtc.ICEConnectionState) {
		if s == webrtc.ICEConnectionStateConnected {
			t.mu.Lock()
			if t.iceConnected.IsZero() {
				t.iceConnected = time.Now()
			}
			t.mu.Unlock()
		}
	})
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		if s == webrtc.PeerConnectionStateConnected {
			t.mu.Lock()
			if t.dtlsConnected.IsZero() {
				t.dtlsConnected = time.Now()
			}
			t.mu.Unlock()
		}
		t.handler.OnConnectionStateChange(s)
	})
	pc.OnTrack(func(tr *webrtc.TrackRemote, r *webrtc.RTPReceiver) { t.handler.OnTrack(tr, r) })
	pc.OnDataChannel(func(dc *webrtc.DataChannel) { t.handler.OnDataChannel(dc) })
	return t, nil
}

func (t *transport) PeerConnection() *webrtc.PeerConnection { return t.pc }

// HandleRemoteDescription implements Transport.
func (t *transport) HandleRemoteDescription(sd *tesserav1.SessionDescription) (*tesserav1.SessionDescription, error) {
	typ := webrtc.NewSDPType(sd.GetType())
	desc := webrtc.SessionDescription{Type: typ, SDP: sd.GetSdp()}
	if typ == webrtc.SDPTypeAnswer {
		t.mu.Lock()
		if !t.awaitAnswer || (sd.GetId() != 0 && sd.GetId() < t.offerID) {
			t.mu.Unlock()
			return nil, nil // stale or duplicate answer (an offer may have been resent)
		}
		t.mu.Unlock()
		if err := t.pc.SetRemoteDescription(desc); err != nil {
			return nil, errs.Wrap(errs.CodeInvalidArgument, err, "set answer")
		}
		t.mu.Lock()
		t.remoteSet = true
		t.awaitAnswer = false
		again, restart := t.renegotiate, t.restartNext
		t.renegotiate, t.restartNext = false, false
		t.flushCandidatesLocked()
		t.mu.Unlock()
		if again {
			t.Negotiate(restart)
		}
		return nil, nil
	}
	if typ != webrtc.SDPTypeOffer {
		return nil, errs.New(errs.CodeInvalidArgument, "unexpected sdp type %q", sd.GetType())
	}
	if err := t.pc.SetRemoteDescription(desc); err != nil {
		return nil, errs.Wrap(errs.CodeInvalidArgument, err, "set offer")
	}
	t.mu.Lock()
	t.remoteSet = true
	t.flushCandidatesLocked()
	t.mu.Unlock()
	ans, err := t.pc.CreateAnswer(nil)
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "create answer")
	}
	if err := t.pc.SetLocalDescription(ans); err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "set local answer")
	}
	return &tesserav1.SessionDescription{Type: "answer", Sdp: t.pc.LocalDescription().SDP, Id: sd.GetId()}, nil
}

func (t *transport) flushCandidatesLocked() {
	for _, c := range t.pending {
		_ = t.pc.AddICECandidate(c)
	}
	t.pending = nil
}

// Bounds on ICE candidates queued before the remote description is set.
const (
	maxPendingCandidates = 64
	maxCandidateLen      = 1024
)

// AddICECandidate implements Transport; candidates are queued until the
// remote description is set.
func (t *transport) AddICECandidate(c webrtc.ICECandidateInit) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.remoteSet {
		// A client that trickles without ever negotiating must not grow this without bound.
		if len(t.pending) >= maxPendingCandidates || len(c.Candidate) > maxCandidateLen {
			return errs.New(errs.CodeResourceExhausted, "too many or too large ICE candidates before negotiation")
		}
		t.pending = append(t.pending, c)
		return nil
	}
	return t.pc.AddICECandidate(c)
}

// AnswerTimeout is how long an offer waits for its answer before it is sent again. The
// offer or answer can be lost with the signaling connection (a resume); without a resend
// the transport would wait forever and the subscriber would never get new tracks.
var AnswerTimeout = 5 * time.Second

// resendIfUnanswered sends offer id again every AnswerTimeout until it is answered,
// superseded or the transport closes.
func (t *transport) resendIfUnanswered(id uint32) {
	time.AfterFunc(AnswerTimeout, func() {
		t.mu.Lock()
		pending := !t.closed && t.awaitAnswer && t.offerID == id
		t.mu.Unlock()
		if !pending {
			return
		}
		ld := t.pc.LocalDescription()
		if ld == nil {
			return
		}
		t.logger.Debug("subscriber offer unanswered, sending again", "id", id)
		t.handler.OnOffer(&tesserav1.SessionDescription{Type: "offer", Sdp: ld.SDP, Id: id})
		t.resendIfUnanswered(id)
	})
}

// Negotiate implements Transport (subscriber side only).
func (t *transport) Negotiate(iceRestart bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	if iceRestart {
		t.restartNext = true
	}
	if t.awaitAnswer {
		t.renegotiate = true
		return
	}
	if t.timer != nil {
		return
	}
	t.timer = time.AfterFunc(NegotiationDebounce, t.createOffer)
}

func (t *transport) createOffer() {
	t.mu.Lock()
	t.timer = nil
	if t.closed || t.awaitAnswer {
		if t.awaitAnswer {
			t.renegotiate = true
		}
		t.mu.Unlock()
		return
	}
	restart := t.restartNext
	t.restartNext = false
	t.awaitAnswer = true
	t.offerID++
	id := t.offerID
	t.mu.Unlock()

	offer, err := t.pc.CreateOffer(&webrtc.OfferOptions{ICERestart: restart})
	if err == nil {
		err = t.pc.SetLocalDescription(offer)
	}
	if err != nil {
		t.logger.Warn("subscriber offer failed", "err", err)
		t.mu.Lock()
		t.awaitAnswer = false
		t.mu.Unlock()
		return
	}
	t.handler.OnOffer(&tesserav1.SessionDescription{Type: "offer", Sdp: t.pc.LocalDescription().SDP, Id: id})
	t.resendIfUnanswered(id)
}

// AddDownTrack implements Transport: adds a sendonly transceiver and starts
// the RTCP reader of its sender.
func (t *transport) AddDownTrack(dt DownTrack) error {
	tr, err := t.pc.AddTransceiverFromTrack(dt, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendonly})
	if err != nil {
		return errs.Wrap(errs.CodeInternal, err, "add transceiver")
	}
	sender := tr.Sender()
	t.mu.Lock()
	t.senders[dt] = sender
	t.mu.Unlock()
	go func() {
		buf := make([]byte, 1500)
		for {
			n, _, err := sender.Read(buf)
			if err != nil {
				return
			}
			pkts, err := rtcp.Unmarshal(buf[:n])
			if err != nil {
				continue
			}
			dt.HandleRTCP(pkts)
		}
	}()
	return nil
}

// RemoveDownTrack implements Transport.
func (t *transport) RemoveDownTrack(dt DownTrack) error {
	t.mu.Lock()
	sender := t.senders[dt]
	delete(t.senders, dt)
	t.mu.Unlock()
	if sender == nil {
		return nil
	}
	if err := t.pc.RemoveTrack(sender); err != nil && t.pc.ConnectionState() != webrtc.PeerConnectionStateClosed {
		return errs.Wrap(errs.CodeInternal, err, "remove track")
	}
	return nil
}

// Data channel labels (PROTOCOL.md 2.5).
const (
	LabelReliable = "_reliable"
	LabelLossy    = "_lossy"
)

// CreateDataChannels creates the server-side `_reliable` and `_lossy`
// channels (subscriber PC); the next offer carries them.
func (t *transport) CreateDataChannels() error {
	ordered := false
	var zero uint16
	rel, err := t.pc.CreateDataChannel(LabelReliable, nil)
	if err != nil {
		return err
	}
	lossy, err := t.pc.CreateDataChannel(LabelLossy, &webrtc.DataChannelInit{Ordered: &ordered, MaxRetransmits: &zero})
	if err != nil {
		return err
	}
	t.mu.Lock()
	t.dcs = map[string]*webrtc.DataChannel{LabelReliable: rel, LabelLossy: lossy}
	t.mu.Unlock()
	return nil
}

// SendDataPacket implements Transport; data channels are looked up by label.
func (t *transport) SendDataPacket(kind tesserav1.DataPacket_Kind, data []byte) error {
	label := LabelReliable
	if kind == tesserav1.DataPacket_KIND_LOSSY {
		label = LabelLossy
	}
	t.mu.Lock()
	dc := t.dcs[label]
	t.mu.Unlock()
	if dc == nil || dc.ReadyState() != webrtc.DataChannelStateOpen {
		return errs.New(errs.CodeUnavailable, "data channel %s not open", label)
	}
	return dc.Send(data)
}

// WriteRTCP implements Transport.
func (t *transport) WriteRTCP(pkts []rtcp.Packet) error { return t.pc.WriteRTCP(pkts) }

// Stats implements Transport.
func (t *transport) Stats() TransportStats {
	t.mu.Lock()
	s := TransportStats{Connected: !t.dtlsConnected.IsZero()}
	if !t.iceConnected.IsZero() {
		s.ICESetup = t.iceConnected.Sub(t.created)
		if !t.dtlsConnected.IsZero() {
			s.DTLSSetup = t.dtlsConnected.Sub(t.iceConnected)
		}
	}
	t.mu.Unlock()
	if s.Connected {
		// Only a live agent: a closed one (session dropped, waiting to resume) makes Pion log
		// "the agent is closed" on every poll.
		if ice := t.iceTransport(); ice != nil && iceUp(ice.State()) {
			if pair, err := ice.GetSelectedCandidatePair(); err == nil && pair != nil {
				s.CandidateType = candidateType(pair.Remote)
			}
			if ps, ok := ice.GetSelectedCandidatePairStats(); ok && ps.CurrentRoundTripTime > 0 {
				s.RTT = time.Duration(ps.CurrentRoundTripTime * float64(time.Second))
			}
		}
	}
	return s
}

func iceUp(st webrtc.ICETransportState) bool {
	return st == webrtc.ICETransportStateConnected || st == webrtc.ICETransportStateCompleted || st == webrtc.ICETransportStateChecking
}

// iceTransport returns the PeerConnection's ICE transport (bundled: one for all).
func (t *transport) iceTransport() *webrtc.ICETransport {
	if sctp := t.pc.SCTP(); sctp != nil && sctp.Transport() != nil {
		return sctp.Transport().ICETransport()
	}
	for _, tr := range t.pc.GetTransceivers() {
		if r := tr.Receiver(); r != nil && r.Transport() != nil {
			return r.Transport().ICETransport()
		}
		if s := tr.Sender(); s != nil && s.Transport() != nil {
			return s.Transport().ICETransport()
		}
	}
	return nil
}

func candidateType(c *webrtc.ICECandidate) string {
	if c == nil {
		return ""
	}
	if c.Protocol == webrtc.ICEProtocolTCP {
		return stats.CandidateTCP
	}
	return c.Typ.String()
}

// Close implements Transport.
func (t *transport) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	if t.timer != nil {
		t.timer.Stop()
	}
	t.mu.Unlock()
	t.e.pcCount.Add(-1)
	return t.pc.Close()
}

// ParseCandidate decodes a trickled candidate (JSON RTCIceCandidateInit).
func ParseCandidate(s string) (webrtc.ICECandidateInit, error) {
	var c webrtc.ICECandidateInit
	err := json.Unmarshal([]byte(s), &c)
	return c, err
}

// MarshalCandidate encodes a candidate for TrickleRequest.candidate_init.
func MarshalCandidate(c *webrtc.ICECandidateInit) string {
	b, _ := json.Marshal(c)
	return string(b)
}
