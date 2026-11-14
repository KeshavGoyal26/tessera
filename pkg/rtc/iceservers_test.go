package rtc

import (
	"slices"
	"testing"

	"github.com/KeshavGoyal26/tessera/pkg/config"
)

// TestICEServersPreferEdgeTURN: a session proxied by an edge gets that edge's TURN only
// (matched by host and port), never a URL that is not configured; others get all.
func TestICEServersPreferEdgeTURN(t *testing.T) {
	e := &engine{cfg: config.RTCConfig{TURNRelay: config.TURNRelayConfig{
		URLs: []string{"turn:10.0.0.1:3478?transport=tcp", "turn:10.0.0.2:3479?transport=tcp"}, Secret: "s"}}}
	relay := func(prefer []string) []string { s := e.ICEServers("PA_1", prefer); return s[len(s)-1].GetUrls() }
	if got := relay(nil); len(got) != 2 {
		t.Fatalf("no edge: %v, want both", got)
	}
	if got := relay([]string{"turn:10.0.0.2:3479?transport=udp", "turns:10.0.0.2:443?transport=tcp"}); !slices.Equal(got, []string{"turn:10.0.0.2:3479?transport=tcp"}) {
		t.Fatalf("edge 2: %v", got)
	}
	if got := relay([]string{"turn:evil:3478"}); len(got) != 2 {
		t.Fatalf("unknown edge: %v, want the configured ones", got)
	}
}
