// Package turn runs the embedded TURN server (pion/turn) with ephemeral
// REST-style credentials (ADR-0005). The relay only permits peers that are
// this node's own media addresses, so it is never an open relay.
package turn

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"time"

	"github.com/pion/logging"
	pturn "github.com/pion/turn/v5"

	"github.com/KeshavGoyal26/tessera/pkg/config"
	"github.com/KeshavGoyal26/tessera/pkg/errs"
	"github.com/KeshavGoyal26/tessera/pkg/stats"
)

// Server is the embedded TURN server.
type Server struct {
	cfg     config.TURNConfig
	srv     *pturn.Server
	secret  string
	host    string
	udpPort int
	tcpPort int
	tlsPort int
	closers []func() error
	// in and out meter all TURN traffic; act tracks the relay sockets' activity (meter.go).
	in, out stats.Meter
	act     activity
}

// NewServer starts the TURN listeners. It returns (nil, nil) when TURN is
// disabled. relayIP is the address relayed traffic uses (the node's media
// IP); allowed lists the peer IPs clients may relay to, peers extra networks
// (an edge's media-server subnet, ADR-0012).
func NewServer(cfg config.TURNConfig, relayIP string, allowed []net.IP, peers []*net.IPNet, logger *slog.Logger) (*Server, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	if cfg.TLSPort > 0 && (cfg.CertFile == "" || cfg.KeyFile == "") {
		return nil, errs.New(errs.CodeInvalidArgument, "turn: tls_port requires cert_file and key_file")
	}
	s := &Server{cfg: cfg, secret: cfg.Secret, host: relayIP}
	if s.secret == "" {
		b := make([]byte, 32)
		_, _ = rand.Read(b)
		s.secret = hex.EncodeToString(b)
	}
	if cfg.Domain != "" {
		s.host = cfg.Domain
	}
	ip := net.ParseIP(relayIP)
	if ip == nil {
		return nil, errs.New(errs.CodeInvalidArgument, "turn: bad relay ip %q", relayIP)
	}
	permit := func(_ net.Addr, peer net.IP) bool {
		for _, a := range allowed {
			if a.Equal(peer) {
				return true
			}
		}
		for _, n := range peers {
			if n.Contains(peer) {
				return true
			}
		}
		return false
	}
	// Listen on an address only when this machine has it, else on all interfaces: in a
	// container or behind cloud 1:1 NAT the advertised address (node_ip) is not a local one.
	bindOf := func(a net.IP) string {
		if isLocal(a) {
			return a.String()
		}
		return "0.0.0.0"
	}
	host := bindOf(ip)
	rip := ip // relay sockets: towards the relay peers, cfg.RelayIP when set
	if cfg.RelayIP != "" {
		if rip = net.ParseIP(cfg.RelayIP); rip == nil {
			return nil, errs.New(errs.CodeInvalidArgument, "turn: bad relay_ip %q", cfg.RelayIP)
		}
	}
	gen := func() pturn.RelayAddressGenerator {
		if cfg.RelayRangeStart > 0 && cfg.RelayRangeEnd >= cfg.RelayRangeStart {
			return &pturn.RelayAddressGeneratorPortRange{RelayAddress: rip, Address: bindOf(rip),
				MinPort: uint16(cfg.RelayRangeStart), MaxPort: uint16(cfg.RelayRangeEnd)}
		}
		return &pturn.RelayAddressGeneratorStatic{RelayAddress: rip, Address: bindOf(rip)}
	}
	metered := func() pturn.RelayAddressGenerator {
		return meteredGenerator{RelayAddressGenerator: gen(), in: &s.in, out: &s.out, act: &s.act}
	}
	lf := logging.NewDefaultLoggerFactory()
	lf.DefaultLogLevel = logging.LogLevelError
	sc := pturn.ServerConfig{Realm: "tessera", LoggerFactory: lf, AuthHandler: pturn.LongTermTURNRESTAuthHandler(s.secret, nil)}
	bind := net.JoinHostPort(host, strconv.Itoa(cfg.UDPPort))
	if cfg.UDPPort >= 0 {
		var lc net.ListenConfig
		pc, err := lc.ListenPacket(context.Background(), "udp4", bind)
		if err != nil {
			return nil, fmt.Errorf("turn: listen udp: %w", err)
		}
		s.udpPort = pc.LocalAddr().(*net.UDPAddr).Port
		s.closers = append(s.closers, pc.Close)
		sc.PacketConnConfigs = append(sc.PacketConnConfigs, pturn.PacketConnConfig{PacketConn: meteredPacketConn{PacketConn: pc, in: &s.in, out: &s.out},
			RelayAddressGenerator: metered(), PermissionHandler: permit})
	}
	if cfg.TCPPort >= 0 {
		var lc net.ListenConfig
		ln, err := lc.Listen(context.Background(), "tcp4", net.JoinHostPort(host, strconv.Itoa(cfg.TCPPort)))
		if err != nil {
			s.close()
			return nil, fmt.Errorf("turn: listen tcp: %w", err)
		}
		s.tcpPort = ln.Addr().(*net.TCPAddr).Port
		s.closers = append(s.closers, ln.Close)
		sc.ListenerConfigs = append(sc.ListenerConfigs, pturn.ListenerConfig{Listener: meteredListener{Listener: ln, in: &s.in, out: &s.out},
			RelayAddressGenerator: metered(), PermissionHandler: permit})
	}
	if cfg.TLSPort > 0 {
		// TURN over TLS (TURNS, RFC 7350) for networks that only let TCP/443 through.
		cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			s.close()
			return nil, fmt.Errorf("turn: load tls certificate: %w", err)
		}
		var lc net.ListenConfig
		raw, err := lc.Listen(context.Background(), "tcp4", net.JoinHostPort(host, strconv.Itoa(cfg.TLSPort)))
		if err != nil {
			s.close()
			return nil, fmt.Errorf("turn: listen tls: %w", err)
		}
		ln := tls.NewListener(meteredListener{Listener: raw, in: &s.in, out: &s.out}, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12, NextProtos: []string{"stun.turn"}})
		s.tlsPort = raw.Addr().(*net.TCPAddr).Port
		s.closers = append(s.closers, ln.Close)
		sc.ListenerConfigs = append(sc.ListenerConfigs, pturn.ListenerConfig{Listener: ln, RelayAddressGenerator: metered(), PermissionHandler: permit})
	}
	srv, err := pturn.NewServer(sc)
	if err != nil {
		s.close()
		return nil, fmt.Errorf("turn: %w", err)
	}
	s.srv = srv
	if logger != nil {
		logger.Info("turn listening", "udp", s.udpPort, "tcp", s.tcpPort, "tls", s.tlsPort, "relay", relayIP)
	}
	return s, nil
}

func (s *Server) close() {
	for _, c := range s.closers {
		_ = c()
	}
}

// Credentials returns an ephemeral username/password for a participant:
// username "<expiry>:<participant sid>", password HMAC-SHA1 (RFC 7635 style).
func (s *Server) Credentials(participantSID string, ttl time.Duration) (username, password string) {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return RESTCredentials(s.secret, participantSID, ttl)
}

// RESTCredentials returns TURN REST API credentials (username "expiry:user", HMAC
// password) that any TURN server sharing secret accepts until ttl elapses.
func RESTCredentials(secret, user string, ttl time.Duration) (username, password string) {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	u, p, err := pturn.GenerateLongTermTURNRESTCredentials(secret, user, ttl)
	if err != nil {
		return "", ""
	}
	return u, p
}

// URLs returns the ICE server URLs clients should use (turn:, turns:).
func (s *Server) URLs() []string {
	var out []string
	if s.udpPort > 0 {
		out = append(out, fmt.Sprintf("turn:%s:%d?transport=udp", s.host, s.udpPort))
	}
	if s.tcpPort > 0 {
		out = append(out, fmt.Sprintf("turn:%s:%d?transport=tcp", s.host, s.tcpPort))
	}
	if s.tlsPort > 0 {
		out = append(out, fmt.Sprintf("turns:%s:%d?transport=tcp", s.host, s.tlsPort))
	}
	return out
}

// isLocal reports whether ip is assigned to one of this machine's interfaces.
func isLocal(ip net.IP) bool {
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.Equal(ip) {
			return true
		}
	}
	return false
}

// relayActiveWindow: a relay that carried traffic this recently is in use (media
// flows continuously; RTCP alone keeps an idle but connected relay active).
const relayActiveWindow = 5 * time.Second

// Stats returns the number of allocations, the relays among them in use, and the TURN
// traffic in bits per second (both legs, see meter.go) over the last window ending at now.
func (s *Server) Stats(now time.Time) (allocations, active int, bpsIn, bpsOut float64) {
	if s == nil || s.srv == nil {
		return 0, 0, 0, 0
	}
	_, bpsIn = s.in.Rates(now)
	_, bpsOut = s.out.Rates(now)
	return s.srv.AllocationCount(), s.act.active(now, relayActiveWindow), bpsIn, bpsOut
}

// Close stops all listeners.
func (s *Server) Close() error {
	if s == nil || s.srv == nil {
		return nil
	}
	return s.srv.Close()
}
