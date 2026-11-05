package rtc

import (
	"io"
	"log/slog"
	"net"
	"testing"

	"github.com/KeshavGoyal26/tessera/pkg/config"
)

// Duplicate IPs (for example --host 127.0.0.1 plus the loopback default) must not
// bind the same UDP port twice.
func TestNewEngineIgnoresDuplicateIPs(t *testing.T) {
	cfg := config.Default()
	probe, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	port := probe.LocalAddr().(*net.UDPAddr).Port
	_ = probe.Close()
	cfg.RTC.UDPPort, cfg.RTC.TCPPort = port, 0
	cfg.RTC.IPs = []string{"127.0.0.1", "127.0.0.1"}
	eng, err := NewEngine(cfg.RTC, cfg.TURN, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	_ = eng.Close()
}
