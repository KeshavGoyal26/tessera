package rtc

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync/atomic"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/interceptor"
	"github.com/pion/logging"
	"github.com/pion/webrtc/v4"
	"github.com/prometheus/client_golang/prometheus"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/config"
	"github.com/KeshavGoyal26/tessera/pkg/errs"
	"github.com/KeshavGoyal26/tessera/pkg/rtc/turn"
	"github.com/KeshavGoyal26/tessera/pkg/stats"
)

// RTP header extension URIs (ADR-0006).
const (
	extMID          = "urn:ietf:params:rtp-hdrext:sdes:mid"
	extAudioLevel   = "urn:ietf:params:rtp-hdrext:ssrc-audio-level"
	extTWCC         = "http://www.ietf.org/id/draft-holmer-rmcat-transport-wide-cc-extensions-01"
	audioLevelID    = 1 // fixed id used towards subscribers
	videoRTCPFBNack = "nack"
)

var videoFeedback = []webrtc.RTCPFeedback{
	{Type: "goog-remb"}, {Type: "ccm", Parameter: "fir"}, {Type: videoRTCPFBNack}, {Type: videoRTCPFBNack, Parameter: "pli"}, {Type: "transport-cc"},
}

// MimeTypeRED is RFC 2198 redundant audio; here it always carries Opus.
const MimeTypeRED = "audio/red"

// redCodec lets browsers (Chrome, Safari) publish Opus with the previous frame
// repeated in each packet, so a lost packet is rebuilt from the next one. The
// server forwards it unchanged to subscribers that negotiated it and strips it
// to the primary Opus frame for the others (DownTrack). It is registered by the
// server only: pkg/client and the recorder keep receiving plain Opus.
var redCodec = webrtc.RTPCodecParameters{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: MimeTypeRED, ClockRate: 48000, Channels: 2, SDPFmtpLine: "111/111"}, PayloadType: 63}

// Codecs returns the codec set of ADR-0006 filtered by enabled MIME types
// (nil = all). Exported for pkg/client.
func Codecs(enabled []string) []webrtc.RTPCodecParameters {
	all := []webrtc.RTPCodecParameters{
		{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2, SDPFmtpLine: "minptime=10;useinbandfec=1"}, PayloadType: 111},
		{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f", RTCPFeedback: videoFeedback}, PayloadType: 102},
		{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeRTX, ClockRate: 90000, SDPFmtpLine: "apt=102"}, PayloadType: 103},
		{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f", RTCPFeedback: videoFeedback}, PayloadType: 104},
		{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeRTX, ClockRate: 90000, SDPFmtpLine: "apt=104"}, PayloadType: 105},
	}
	if len(enabled) == 0 {
		return all
	}
	ok := map[string]bool{}
	for _, m := range enabled {
		ok[strings.ToLower(m)] = true
	}
	var out []webrtc.RTPCodecParameters
	for _, c := range all {
		if ok[strings.ToLower(c.MimeType)] || strings.EqualFold(c.MimeType, webrtc.MimeTypeRTX) {
			out = append(out, c)
		}
	}
	return out
}

// NewMediaEngine registers codecs and header extensions. The RID extensions are
// not registered: publishers send one stream per track (ADR-0012), and without
// them a browser cannot negotiate simulcast.
func NewMediaEngine(codecs []webrtc.RTPCodecParameters, publisherSide bool) (*webrtc.MediaEngine, error) {
	m := &webrtc.MediaEngine{}
	for _, c := range codecs {
		typ := webrtc.RTPCodecTypeVideo
		if strings.HasPrefix(strings.ToLower(c.MimeType), "audio/") {
			typ = webrtc.RTPCodecTypeAudio
		}
		if err := m.RegisterCodec(c, typ); err != nil {
			return nil, err
		}
	}
	exts := []struct {
		uri string
		typ webrtc.RTPCodecType
	}{{extAudioLevel, webrtc.RTPCodecTypeAudio}}
	if publisherSide {
		exts = append(exts,
			struct {
				uri string
				typ webrtc.RTPCodecType
			}{extMID, webrtc.RTPCodecTypeVideo},
			struct {
				uri string
				typ webrtc.RTPCodecType
			}{extMID, webrtc.RTPCodecTypeAudio},
		)
	}
	if !publisherSide {
		for _, typ := range []webrtc.RTPCodecType{webrtc.RTPCodecTypeAudio, webrtc.RTPCodecTypeVideo} {
			if err := m.RegisterHeaderExtension(webrtc.RTPHeaderExtensionCapability{URI: extTWCC}, typ); err != nil {
				return nil, err
			}
		}
	}
	for _, e := range exts {
		if err := m.RegisterHeaderExtension(webrtc.RTPHeaderExtensionCapability{URI: e.uri}, e.typ); err != nil {
			return nil, err
		}
	}
	return m, nil
}

type engine struct {
	cfg     config.RTCConfig
	logger  *slog.Logger
	pubAPI  *webrtc.API
	subAPI  *webrtc.API
	udpMux  ice.UDPMux
	conns   []net.PacketConn
	iceSrv  []*tesserav1.ICEServer
	pcCount atomic.Int64
	tcpMux  ice.TCPMux
	turn    *turn.Server
	turnTTL time.Duration
}

// countingConn counts media datagrams for node statistics.
type countingConn struct {
	*net.UDPConn
	bw *batchWriter // nil: write each datagram directly
}

func newCountingConn(c *net.UDPConn) countingConn {
	return countingConn{UDPConn: c, bw: newBatchWriter(c)}
}

func (c countingConn) Close() error {
	if c.bw != nil {
		c.bw.close()
	}
	return c.UDPConn.Close()
}

func (c countingConn) ReadFrom(b []byte) (int, net.Addr, error) {
	n, a, err := c.UDPConn.ReadFrom(b)
	if n > 0 {
		stats.UDPIn.Add(n)
	}
	return n, a, err
}

func (c countingConn) WriteTo(b []byte, a net.Addr) (int, error) {
	n, err := c.UDPConn.WriteTo(b, a)
	if n > 0 {
		stats.UDPOut.Add(n)
	}
	return n, err
}

func (c countingConn) ReadFromAddrPort(b []byte) (int, netip.AddrPort, error) {
	n, a, err := c.ReadFromUDPAddrPort(b)
	if n > 0 {
		stats.UDPIn.Add(n)
	}
	return n, a, err
}

func (c countingConn) WriteToAddrPort(b []byte, a netip.AddrPort) (int, error) {
	if c.bw != nil {
		if n, ok := c.bw.write(b, a); ok {
			return n, nil
		}
	}
	n, err := c.WriteToUDPAddrPort(b, a)
	if n > 0 {
		stats.UDPOut.Add(n)
	}
	return n, err
}

// NewEngine creates the engine: UDP mux socket(s), media engines and APIs.
func NewEngine(rtcCfg config.RTCConfig, turnCfg config.TURNConfig, reg prometheus.Registerer, logger *slog.Logger) (Engine, error) {
	if logger == nil {
		logger = slog.Default()
	}
	e := &engine{cfg: rtcCfg, logger: logger}
	se := webrtc.SettingEngine{}
	lf := logging.NewDefaultLoggerFactory()
	lf.DefaultLogLevel = logging.LogLevelError
	se.LoggerFactory = lf
	se.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4, webrtc.NetworkTypeUDP6})

	var ips []string
	seen := map[string]bool{}
	for _, ip := range rtcCfg.IPs { // duplicates would bind the same UDP port twice
		key := ip
		if p := net.ParseIP(ip); p != nil {
			key = p.String()
		}
		if !seen[key] {
			seen[key] = true
			ips = append(ips, ip)
		}
	}
	if len(ips) == 0 {
		ips = []string{"0.0.0.0"}
	}
	var muxes []ice.UDPMux
	for _, ip := range ips {
		addr := &net.UDPAddr{IP: net.ParseIP(ip), Port: rtcCfg.UDPPort}
		c, err := net.ListenUDP("udp", addr)
		if err != nil {
			e.closeConns()
			return nil, fmt.Errorf("rtc: listen udp %s: %w", addr, err)
		}
		if rtcCfg.SocketBufferSize > 0 {
			_ = c.SetReadBuffer(rtcCfg.SocketBufferSize)
			_ = c.SetWriteBuffer(rtcCfg.SocketBufferSize)
		}
		cc := newCountingConn(c)
		e.conns = append(e.conns, cc)
		muxes = append(muxes, ice.NewUDPMuxDefault(ice.UDPMuxParams{UDPConn: cc}))
	}
	if len(muxes) == 1 {
		e.udpMux = muxes[0]
	} else {
		e.udpMux = ice.NewMultiUDPMuxDefault(muxes...)
	}
	se.SetICEUDPMux(e.udpMux)
	if len(rtcCfg.IPs) > 0 {
		allowed := map[string]bool{}
		for _, ip := range rtcCfg.IPs {
			allowed[net.ParseIP(ip).String()] = true
			if net.ParseIP(ip).IsLoopback() {
				se.SetIncludeLoopbackCandidate(true)
			}
		}
		se.SetIPFilter(func(ip net.IP) bool { return allowed[ip.String()] })
	}
	if rtcCfg.NodeIP != "" {
		if err := se.SetICEAddressRewriteRules(webrtc.ICEAddressRewriteRule{
			External: []string{rtcCfg.NodeIP}, AsCandidateType: webrtc.ICECandidateTypeHost, Mode: webrtc.ICEAddressRewriteReplace,
		}); err != nil {
			return nil, err
		}
	}
	se.SetICETimeouts(10*time.Second, 20*time.Second, 2*time.Second)

	// ICE-TCP passive candidates on a single port (ADR-0005).
	if rtcCfg.TCPPort > 0 {
		tcpIP := ips[0]
		var lc net.ListenConfig
		ln, err := lc.Listen(context.Background(), "tcp", net.JoinHostPort(tcpIP, fmt.Sprint(rtcCfg.TCPPort)))
		if err != nil {
			// ICE-TCP is a fallback: run UDP-only rather than refusing to start.
			logger.Warn("ICE-TCP disabled: cannot listen", "port", rtcCfg.TCPPort, "err", err)
		} else {
			e.tcpMux = ice.NewTCPMuxDefault(ice.TCPMuxParams{Listener: ln, ReadBufferSize: 64})
			se.SetICETCPMux(e.tcpMux)
			se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4, webrtc.NetworkTypeUDP6, webrtc.NetworkTypeTCP4, webrtc.NetworkTypeTCP6})
		}
	}

	// Embedded TURN relaying only to this node's media addresses.
	if turnCfg.Enabled {
		relayIP := rtcCfg.NodeIP
		if relayIP == "" && len(rtcCfg.IPs) > 0 {
			relayIP = rtcCfg.IPs[0]
		}
		if relayIP == "" {
			relayIP = firstLocalIP()
		}
		allowed := localIPs()
		for _, s := range append(append([]string{}, rtcCfg.IPs...), rtcCfg.NodeIP) {
			if ip := net.ParseIP(s); ip != nil {
				allowed = append(allowed, ip)
			}
		}
		var peers []*net.IPNet
		for _, c := range turnCfg.RelayPeers {
			_, n, err := net.ParseCIDR(c)
			if err != nil {
				e.closeConns()
				return nil, errs.New(errs.CodeInvalidArgument, "turn.relay_peers: bad CIDR %q", c)
			}
			peers = append(peers, n)
		}
		ts, err := turn.NewServer(turnCfg, relayIP, allowed, peers, logger)
		if err != nil {
			e.closeConns()
			return nil, err
		}
		e.turn, e.turnTTL = ts, turnCfg.CredentialTTL
	}

	codecs := Codecs(rtcCfg.EnabledCodecs)
	for _, c := range codecs {
		if strings.EqualFold(c.MimeType, webrtc.MimeTypeOpus) {
			codecs = append(codecs, redCodec)
			break
		}
	}
	pubME, err := NewMediaEngine(codecs, true)
	if err != nil {
		return nil, err
	}
	pubIR := &interceptor.Registry{}
	if err := webrtc.ConfigureTWCCSender(pubME, pubIR); err != nil {
		return nil, err
	}
	subME, err := NewMediaEngine(codecs, false)
	if err != nil {
		return nil, err
	}
	e.pubAPI = webrtc.NewAPI(webrtc.WithMediaEngine(pubME), webrtc.WithSettingEngine(se), webrtc.WithInterceptorRegistry(pubIR))
	e.subAPI = webrtc.NewAPI(webrtc.WithMediaEngine(subME), webrtc.WithSettingEngine(se), webrtc.WithInterceptorRegistry(&interceptor.Registry{}))
	for _, s := range rtcCfg.ICEServers {
		e.iceSrv = append(e.iceSrv, &tesserav1.ICEServer{Urls: s.URLs, Username: s.Username, Credential: s.Credential})
	}
	_ = turnCfg
	_ = reg
	return e, nil
}

func (e *engine) closeConns() {
	for _, c := range e.conns {
		_ = c.Close()
	}
}

// ICEServers implements Engine: configured servers plus per-session
// credentials for the shared-secret TURN relays and the embedded TURN server.
func (e *engine) ICEServers(participantSID string) []*tesserav1.ICEServer {
	out := append([]*tesserav1.ICEServer{}, e.iceSrv...)
	if r := e.cfg.TURNRelay; len(r.URLs) > 0 {
		u, p := turn.RESTCredentials(r.Secret, participantSID, r.TTL)
		out = append(out, &tesserav1.ICEServer{Urls: r.URLs, Username: u, Credential: p})
	}
	if e.turn != nil {
		if urls := e.turn.URLs(); len(urls) > 0 {
			u, p := e.turn.Credentials(participantSID, e.turnTTL)
			out = append(out, &tesserav1.ICEServer{Urls: urls, Username: u, Credential: p})
		}
	}
	return out
}

func localIPs() []net.IP {
	var out []net.IP
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok {
			out = append(out, n.IP)
		}
	}
	return out
}

func firstLocalIP() string {
	for _, ip := range localIPs() {
		if ip.To4() != nil && !ip.IsLoopback() {
			return ip.String()
		}
	}
	return "127.0.0.1"
}

// Ready implements Engine.
func (e *engine) Ready() error { return nil }

// Close implements Engine.
func (e *engine) Close() error {
	if e.udpMux != nil {
		_ = e.udpMux.Close()
	}
	if e.tcpMux != nil {
		_ = e.tcpMux.Close()
	}
	if e.turn != nil {
		_ = e.turn.Close()
	}
	e.closeConns()
	return nil
}

// PeerConnections returns the number of open PeerConnections.
func (e *engine) PeerConnections() int { return int(e.pcCount.Load()) }

// PeerConnectionCounter is implemented by the engine.
type PeerConnectionCounter interface{ PeerConnections() int }

// monotonic clock base for arrival times.
var clockBase = time.Now()

// Now returns monotonic nanoseconds since process start.
func Now() int64 { return int64(time.Since(clockBase)) }
