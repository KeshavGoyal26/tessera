package stats

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
)

// Global event counters incremented by the engine (lock-free). They are
// exported as Prometheus counters by Collector and never decrease, unlike
// per-track counters which disappear with their track.
var (
	NACKsReceived    Counter // NACK packets from subscribers
	NACKsSent        Counter // NACK packets sent to publishers
	PLIsReceived     Counter // PLI/FIR from subscribers
	PLIsSent         Counter // PLIs sent to publishers
	FIRsSent         Counter // FIRs sent to publishers
	Retransmits      Counter // packets retransmitted to subscribers
	LayerSwitches    Counter // simulcast layer switches
	ParticipantJoins Counter
	TrackPublishes   Counter
)

const ns = "tessera"

// Collector exports a Provider's snapshots as Prometheus metrics. Labels are
// restricted to small closed sets (kind, codec, quality, layer, direction,
// candidate type), never room, participant or track identifiers, so the
// series count stays bounded regardless of load.
type Collector struct {
	p Provider

	cpu, rss, heap, goroutines, gcPause, gcCPU, pcs        *prometheus.Desc
	udpPkts, udpBytes                                      *prometheus.Desc
	rooms, participants, published, subscribed             *prometheus.Desc
	pubBitrate, subBitrate, layerDist, paused, dynPaused   *prometheus.Desc
	quality, candidates, rtt, iceSetup, dtlsSetup, speaker *prometheus.Desc
	counters                                               []counterDesc
}

type counterDesc struct {
	d *prometheus.Desc
	c *Counter
}

// NewCollector creates a collector; register it on the node registry.
func NewCollector(p Provider) *Collector {
	d := func(name, help string, labels ...string) *prometheus.Desc {
		return prometheus.NewDesc(ns+"_"+name, help, labels, nil)
	}
	c := &Collector{
		p:            p,
		cpu:          d("node_cpu_percent", "Process CPU usage, 100 = one core."),
		rss:          d("node_rss_bytes", "Resident set size."),
		heap:         d("node_heap_bytes", "Go heap object bytes."),
		goroutines:   d("node_goroutines", "Number of goroutines."),
		gcPause:      d("node_gc_pause_p99_seconds", "p99 GC stop-the-world pause since start."),
		gcCPU:        d("node_gc_cpu_percent", "Share of CPU spent in GC since start."),
		pcs:          d("peer_connections", "Open PeerConnections."),
		udpPkts:      d("udp_packets_per_second", "Media socket packets per second.", "direction"),
		udpBytes:     d("udp_bytes_per_second", "Media socket bytes per second.", "direction"),
		rooms:        d("rooms", "Active rooms."),
		participants: d("participants", "Participants by kind.", "kind"),
		published:    d("published_tracks", "Published tracks by kind and codec.", "kind", "codec"),
		subscribed:   d("subscribed_tracks", "Subscribed tracks (DownTracks) by kind.", "kind"),
		pubBitrate:   d("published_bitrate_bps", "Sum of ingress bitrate by kind.", "kind"),
		subBitrate:   d("subscribed_bitrate_bps", "Sum of egress bitrate by kind.", "kind"),
		layerDist:    d("subscribed_video_layer", "Video DownTracks by current spatial layer (none = paused/off).", "layer"),
		paused:       d("subscribed_paused_tracks", "DownTracks paused by the bandwidth allocator."),
		dynPaused:    d("subscribed_dynacast_paused_tracks", "DownTracks whose layer is disabled at the publisher."),
		quality:      d("connection_quality_participants", "Participants by connection quality.", "quality"),
		candidates:   d("ice_candidate_participants", "Participants by selected ICE candidate type.", "type"),
		rtt:          d("participant_rtt_ms_avg", "Average participant RTT."),
		iceSetup:     d("ice_setup_ms_avg", "Average ICE setup time of current participants."),
		dtlsSetup:    d("dtls_setup_ms_avg", "Average DTLS setup time of current participants."),
		speaker:      d("active_speakers", "Active speakers over all rooms."),
	}
	for _, x := range []struct {
		name, help string
		c          *Counter
	}{
		{"nack_received_total", "NACK packets received from subscribers.", &NACKsReceived},
		{"nack_sent_total", "NACK packets sent to publishers.", &NACKsSent},
		{"pli_received_total", "PLI/FIR received from subscribers.", &PLIsReceived},
		{"pli_sent_total", "PLIs sent to publishers.", &PLIsSent},
		{"fir_sent_total", "FIRs sent to publishers.", &FIRsSent},
		{"retransmits_total", "Packets retransmitted to subscribers.", &Retransmits},
		{"layer_switches_total", "Simulcast layer switches.", &LayerSwitches},
		{"participant_joins_total", "Participant joins.", &ParticipantJoins},
		{"track_publishes_total", "Tracks published.", &TrackPublishes},
	} {
		c.counters = append(c.counters, counterDesc{d(x.name, x.help), x.c})
	}
	return c
}

// Describe implements prometheus.Collector (unchecked collector).
func (c *Collector) Describe(chan<- *prometheus.Desc) {}

// Collect implements prometheus.Collector.
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	s := c.p.Snapshot()
	g := func(d *prometheus.Desc, v float64, l ...string) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, l...)
	}
	n := s.Node
	g(c.cpu, n.CPUPercent)
	g(c.rss, float64(n.RSSBytes))
	g(c.heap, float64(n.HeapBytes))
	g(c.goroutines, float64(n.Goroutines))
	g(c.gcPause, float64(n.GCPauseP99Ns)/1e9)
	g(c.gcCPU, n.GCCPUPercent)
	g(c.pcs, float64(n.PeerConnections))
	g(c.udpPkts, n.UDPPacketsInPerSec, "in")
	g(c.udpPkts, n.UDPPacketsOutPerSec, "out")
	g(c.udpBytes, n.UDPBytesInPerSec, "in")
	g(c.udpBytes, n.UDPBytesOutPerSec, "out")
	g(c.rooms, float64(len(s.Rooms)))
	for _, cd := range c.counters {
		ch <- prometheus.MustNewConstMetric(cd.d, prometheus.CounterValue, float64(cd.c.Load()))
	}

	kinds := map[string]float64{}
	pub := map[[2]string]float64{}
	subs := map[string]float64{"audio": 0, "video": 0}
	pubBR := map[string]float64{"audio": 0, "video": 0}
	subBR := map[string]float64{"audio": 0, "video": 0}
	layers := map[string]float64{"none": 0, "0": 0, "1": 0, "2": 0}
	qual := map[string]float64{}
	cands := map[string]float64{}
	var paused, dyn, speakers float64
	var rttSum, iceSum, dtlsSum, rttN, setupN float64
	for i := range s.Rooms {
		r := &s.Rooms[i]
		speakers += float64(len(r.ActiveSpeakers))
		for j := range r.Participants {
			p := &r.Participants[j]
			kinds[p.Kind]++
			qual[p.ConnectionQuality]++
			if p.ICECandidateType != "" {
				cands[p.ICECandidateType]++
			}
			if p.RTTMs > 0 {
				rttSum += p.RTTMs
				rttN++
			}
			if p.ICESetupMs > 0 {
				iceSum += float64(p.ICESetupMs)
				dtlsSum += float64(p.DTLSSetupMs)
				setupN++
			}
			for k := range p.Published {
				t := &p.Published[k]
				pub[[2]string{t.Kind, t.Codec}]++
				pubBR[t.Kind] += t.BitrateBps
			}
			for k := range p.Subscribed {
				t := &p.Subscribed[k]
				subs[t.Kind]++
				subBR[t.Kind] += t.BitrateBps
				if t.Paused {
					paused++
				}
				if t.DynacastPaused {
					dyn++
				}
				if t.Kind == "video" {
					if t.CurrentSpatial < 0 || t.CurrentSpatial > 2 {
						layers["none"]++
					} else {
						layers[strconv.Itoa(int(t.CurrentSpatial))]++
					}
				}
			}
		}
	}
	for k, v := range kinds {
		g(c.participants, v, k)
	}
	for k, v := range pub {
		g(c.published, v, k[0], k[1])
	}
	for k, v := range subs {
		g(c.subscribed, v, k)
	}
	for k, v := range pubBR {
		g(c.pubBitrate, v, k)
	}
	for k, v := range subBR {
		g(c.subBitrate, v, k)
	}
	for k, v := range layers {
		g(c.layerDist, v, k)
	}
	for k, v := range qual {
		g(c.quality, v, k)
	}
	for k, v := range cands {
		g(c.candidates, v, k)
	}
	g(c.paused, paused)
	g(c.dynPaused, dyn)
	g(c.speaker, speakers)
	g(c.rtt, avg(rttSum, rttN))
	g(c.iceSetup, avg(iceSum, setupN))
	g(c.dtlsSetup, avg(dtlsSum, setupN))
}

func avg(sum, n float64) float64 {
	if n == 0 {
		return 0
	}
	return sum / n
}
