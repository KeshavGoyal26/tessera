// Command netem is a TURN relay that impairs the link between a WebRTC client and the media server.
//
// It is a test tool, not part of Tessera. Clients forced to relay through it (iceTransportPolicy
// "relay" with only this server in iceServers) see the configured loss, delay, jitter and
// bandwidth cap in both directions, whatever media server is on the other side. That makes A/B
// runs against other servers fair: the network is the same for both.
//
//	go run ./test/netem -listen 127.0.0.1:3478 -ctl 127.0.0.1:3479 -loss 5 -delay 50ms -kbps 1500
//	curl '127.0.0.1:3479/set?loss=0&kbps=500'   # change the profile while running
//	curl '127.0.0.1:3479/stats'                 # packets passed and dropped per direction
//
// TURN credentials: user "netem", password "netem".
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	pturn "github.com/pion/turn/v5"
)

// Profile is one direction's impairment. Both directions use the same profile.
type Profile struct {
	LossPct float64       `json:"loss"`  // packet loss in percent
	Burst   float64       `json:"burst"` // mean loss burst length in packets (1 = independent losses)
	Delay   time.Duration `json:"delay"` // one-way base delay
	Jitter  time.Duration `json:"jitter"`
	Kbps    int           `json:"kbps"`  // bandwidth cap, 0 = none
	Queue   time.Duration `json:"queue"` // max queueing delay at the cap before tail drop
}

type counters struct {
	Passed, Lost, QueueDrops uint64
	// MaxLateMs is the worst delay of a delivery past its scheduled time since the
	// last /stats read: when it grows, the tool itself (scheduling) adds jitter.
	MaxLateMs float64
}

// link delivers packets in order after the profile's queueing, delay and loss.
type link struct {
	mu       sync.Mutex
	p        *Profile
	pmu      *sync.RWMutex
	nextFree time.Time // when the bottleneck finishes serialising the last accepted packet
	lastOut  time.Time // departure of the previous packet, keeps delivery in order under jitter
	inBurst  bool
	c        counters
	q        chan pkt
}

type pkt struct {
	at   time.Time
	data []byte
	send func([]byte)
}

func newLink(p *Profile, pmu *sync.RWMutex) *link {
	l := &link{p: p, pmu: pmu, q: make(chan pkt, 1<<16)}
	go l.run()
	return l
}

func (l *link) run() {
	for k := range l.q {
		if d := time.Until(k.at); d > 0 {
			time.Sleep(d)
		}
		k.send(k.data)
		if late := float64(time.Since(k.at)) / 1e6; late > 0 {
			l.mu.Lock()
			l.c.MaxLateMs = max(l.c.MaxLateMs, late)
			l.mu.Unlock()
		}
	}
}

// lose decides loss with a two-state (Gilbert) model whose mean burst length is p.Burst.
func (l *link) lose(p Profile) bool {
	if p.LossPct <= 0 {
		return false
	}
	pl := p.LossPct / 100
	burst := max(p.Burst, 1)
	if l.inBurst {
		l.inBurst = rand.Float64() >= 1/burst // stay in the burst with probability 1-1/burst
	} else {
		// Enter a burst so that the long-run loss rate is pl.
		l.inBurst = rand.Float64() < pl/(burst*(1-pl))
	}
	return l.inBurst
}

func (l *link) send(b []byte, send func([]byte)) {
	l.pmu.RLock()
	p := *l.p
	l.pmu.RUnlock()

	l.mu.Lock()
	now := time.Now()
	depart := now
	if p.Kbps > 0 {
		start := now
		if l.nextFree.After(start) {
			start = l.nextFree
		}
		if start.Sub(now) > p.Queue {
			l.c.QueueDrops++
			l.mu.Unlock()
			return
		}
		depart = start.Add(time.Duration(len(b)*8) * time.Second / time.Duration(p.Kbps*1000))
		l.nextFree = depart
	}
	if l.lose(p) {
		l.c.Lost++
		l.mu.Unlock()
		return
	}
	at := depart.Add(p.Delay)
	if p.Jitter > 0 {
		at = at.Add(time.Duration(rand.Int64N(int64(p.Jitter))))
	}
	if at.Before(l.lastOut) {
		at = l.lastOut
	}
	l.lastOut = at
	l.c.Passed++
	l.mu.Unlock()

	data := append([]byte(nil), b...)
	select {
	case l.q <- pkt{at: at, data: data, send: send}:
	default: // the tool itself is overloaded; count as a queue drop
		l.mu.Lock()
		l.c.QueueDrops++
		l.c.Passed--
		l.mu.Unlock()
	}
}

func (l *link) stats() counters {
	l.mu.Lock()
	defer l.mu.Unlock()
	c := l.c
	l.c.MaxLateMs = 0
	return c
}

// impairedConn delays, drops and paces writes through a link.
type impairedConn struct {
	net.PacketConn
	l *link
}

func (c *impairedConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	c.l.send(b, func(d []byte) { _, _ = c.PacketConn.WriteTo(d, addr) })
	return len(b), nil
}

// relayGen wraps every relay socket so traffic towards the media server is impaired too.
type relayGen struct {
	pturn.RelayAddressGeneratorStatic
	up *link
}

func (g *relayGen) AllocatePacketConn(c pturn.AllocateListenerConfig) (net.PacketConn, net.Addr, error) {
	pc, addr, err := g.RelayAddressGeneratorStatic.AllocatePacketConn(c)
	if err != nil {
		return nil, nil, err
	}
	return &impairedConn{PacketConn: pc, l: g.up}, addr, nil
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	listen := flag.String("listen", "127.0.0.1:3478", "TURN UDP listen address")
	ctl := flag.String("ctl", "127.0.0.1:3479", "HTTP control address (GET /set, /stats)")
	p := Profile{}
	flag.Float64Var(&p.LossPct, "loss", 0, "packet loss percent, each direction")
	flag.Float64Var(&p.Burst, "burst", 1, "mean loss burst length in packets")
	flag.DurationVar(&p.Delay, "delay", 0, "one-way delay")
	flag.DurationVar(&p.Jitter, "jitter", 0, "extra random delay, 0..jitter (order is kept)")
	flag.IntVar(&p.Kbps, "kbps", 0, "bandwidth cap per direction in kbit/s, 0 = none")
	flag.DurationVar(&p.Queue, "queue", 300*time.Millisecond, "max queueing delay at the cap")
	flag.Parse()

	var pmu sync.RWMutex
	down, up := newLink(&p, &pmu), newLink(&p, &pmu)

	var lc net.ListenConfig
	pc, err := lc.ListenPacket(context.Background(), "udp4", *listen)
	if err != nil {
		return err
	}
	host, _, _ := net.SplitHostPort(*listen)
	key := pturn.GenerateAuthKey("netem", "netem", "netem")
	srv, err := pturn.NewServer(pturn.ServerConfig{
		Realm: "netem",
		AuthHandler: func(ra *pturn.RequestAttributes) (string, []byte, bool) {
			return ra.Username, key, ra.Username == "netem"
		},
		PacketConnConfigs: []pturn.PacketConnConfig{{
			PacketConn:            &impairedConn{PacketConn: pc, l: down},
			RelayAddressGenerator: &relayGen{RelayAddressGeneratorStatic: pturn.RelayAddressGeneratorStatic{RelayAddress: net.ParseIP(host), Address: host}, up: up},
			PermissionHandler:     pturn.DefaultPermissionHandler,
		}},
	})
	if err != nil {
		return err
	}
	defer func() { _ = srv.Close() }()

	http.HandleFunc("/set", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		pmu.Lock()
		defer pmu.Unlock()
		f := func(k string, set func(string) error) {
			if v := q.Get(k); v != "" {
				if err := set(v); err != nil {
					http.Error(w, fmt.Sprintf("%s: %v", k, err), http.StatusBadRequest)
				}
			}
		}
		f("loss", func(v string) (err error) { p.LossPct, err = strconv.ParseFloat(v, 64); return })
		f("burst", func(v string) (err error) { p.Burst, err = strconv.ParseFloat(v, 64); return })
		f("delay", func(v string) (err error) { p.Delay, err = time.ParseDuration(v); return })
		f("jitter", func(v string) (err error) { p.Jitter, err = time.ParseDuration(v); return })
		f("kbps", func(v string) (err error) { p.Kbps, err = strconv.Atoi(v); return })
		f("queue", func(v string) (err error) { p.Queue, err = time.ParseDuration(v); return })
		_ = json.NewEncoder(w).Encode(p)
	})
	http.HandleFunc("/stats", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]counters{"to_client": down.stats(), "to_server": up.stats()})
	})
	log.Printf("netem TURN on %s (user netem/netem), control on http://%s, profile %+v", *listen, *ctl, p)
	srv2 := &http.Server{Addr: *ctl, ReadHeaderTimeout: 5 * time.Second}
	return srv2.ListenAndServe()
}
