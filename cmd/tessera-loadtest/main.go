// Command tessera-loadtest simulates publishers and subscribers with
// pkg/client and synthetic RTP (no encoder), reporting join latency, packet
// loss, forwarding latency and throughput (ARCHITECTURE.md 9).
//
//	tessera-loadtest --url ws://localhost:7880 --api-key devkey --secret ... \
//	  --rooms 10 --publishers 10 --subscribers 0 --duration 2m --video-layers 3
//
// Ownership: WP5.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/KeshavGoyal26/tessera/pkg/errs"
)

// Params of a load test run.
type Params struct {
	URL          string
	APIKey       string
	Secret       string
	Rooms        int
	Publishers   int
	Subscribers  int
	AudioOnly    bool
	VideoLayers  int
	Duration     time.Duration
	RampUp       time.Duration
	ReportPeriod time.Duration
}

func main() {
	var p Params
	flag.StringVar(&p.URL, "url", "ws://localhost:7880", "server URL")
	flag.StringVar(&p.APIKey, "api-key", "devkey", "API key")
	flag.StringVar(&p.Secret, "secret", "", "API secret")
	flag.IntVar(&p.Rooms, "rooms", 1, "number of rooms")
	flag.IntVar(&p.Publishers, "publishers", 1, "publishers per room")
	flag.IntVar(&p.Subscribers, "subscribers", 0, "listen-only subscribers per room")
	flag.BoolVar(&p.AudioOnly, "audio-only", false, "publish audio only")
	flag.IntVar(&p.VideoLayers, "video-layers", 3, "simulcast layers per video track")
	flag.DurationVar(&p.Duration, "duration", time.Minute, "test duration")
	flag.DurationVar(&p.RampUp, "ramp-up", 10*time.Second, "time to connect all clients")
	flag.DurationVar(&p.ReportPeriod, "report", 5*time.Second, "stats report period")
	flag.Parse()

	if err := run(p); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(p Params) error {
	if p.Secret == "" {
		return errs.New(errs.CodeInvalidArgument, "--secret is required")
	}
	return errs.NotImplemented("tessera-loadtest")
}
