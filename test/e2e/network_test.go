package e2e

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/KeshavGoyal26/tessera/pkg/client"
	"github.com/KeshavGoyal26/tessera/pkg/config"
	"github.com/KeshavGoyal26/tessera/pkg/stats"
)

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port
}

// mediaVia publishes from a normal client and subscribes with opts; it
// returns the subscriber's ICE candidate type from server stats.
func mediaVia(t *testing.T, s *server, opts client.Options) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pubC := connect(t, s, "net", "pub")
	syn := newSynth(t, "video", "cam")
	info, err := pubC.PublishTrack(ctx, syn.track, client.PublishOptions{Name: "cam"})
	if err != nil {
		t.Fatal(err)
	}
	syn.run()
	t.Cleanup(syn.Stop)
	opts.AutoSubscribe, opts.Logger = true, testLogger()
	sub := client.New(opts)
	if err := sub.Connect(ctx, s.URL, token(t, "net", "sub")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sub.Disconnect(context.Background()) })
	rt, err := client.WaitTrack(ctx, sub, info.Sid)
	if err != nil {
		t.Fatal(err)
	}
	receive(t, rt, true, 20)
	for _, p := range s.Manager.Snapshot().Rooms[0].Participants {
		if p.Identity == "sub" {
			return p.ICECandidateType
		}
	}
	return ""
}

func TestEmbeddedTURNRelay(t *testing.T) {
	s := startServer(t, func(c *config.Config) {
		c.TURN = config.TURNConfig{Enabled: true, UDPPort: 0, TCPPort: 0, CredentialTTL: time.Hour}
	})
	typ := mediaVia(t, s, client.Options{SettingEngine: client.LoopbackSettingEngine(), ForceRelay: true})
	if typ != stats.CandidateRelay {
		t.Fatalf("candidate type %q, want relay", typ)
	}
}

func TestICETCP(t *testing.T) {
	port := freePort(t)
	s := startServer(t, func(c *config.Config) { c.RTC.TCPPort = port })
	se := client.LoopbackSettingEngine()
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeTCP4})
	typ := mediaVia(t, s, client.Options{SettingEngine: se})
	if typ != stats.CandidateTCP {
		t.Fatalf("candidate type %q, want tcp", typ)
	}
}

func TestNAT1To1Advertised(t *testing.T) {
	s := startServer(t, func(c *config.Config) { c.RTC.NodeIP = "127.0.0.1" })
	// Media must still flow with the rewritten host candidate.
	if typ := mediaVia(t, s, client.Options{SettingEngine: client.LoopbackSettingEngine()}); typ != stats.CandidateHost {
		t.Fatalf("candidate type %q", typ)
	}
}
