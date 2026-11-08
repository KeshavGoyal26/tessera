package dashboard

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/KeshavGoyal26/tessera/pkg/recorder"
	"github.com/KeshavGoyal26/tessera/pkg/stats"
)

var jsonpb = protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: false}

// Point is one history sample (about 150 bytes, one hour = 3600 points).
type Point struct {
	T          int64   `json:"t"` // unix ms
	CPU        float64 `json:"cpu"`
	RSS        uint64  `json:"rss"`
	Heap       uint64  `json:"heap"`
	Goroutines int     `json:"goroutines"`
	GCPauseNs  uint64  `json:"gc_pause_ns"`
	GCCPU      float64 `json:"gc_cpu"`
	PktsIn     float64 `json:"pkts_in"`
	PktsOut    float64 `json:"pkts_out"`
	BpsIn      float64 `json:"bps_in"`
	BpsOut     float64 `json:"bps_out"`
	Rooms      int     `json:"rooms"`
	Particips  int     `json:"participants"`
	Published  int     `json:"published"`
	Subscribed int     `json:"subscribed"`
	LossAvg    float64 `json:"loss_avg"`
	LossMax    float64 `json:"loss_max"`
	RTTAvg     float64 `json:"rtt_avg"`
	RTTMax     float64 `json:"rtt_max"`
	Recording  int     `json:"recordings"`
}

type ring struct {
	buf  []Point
	next int
	full bool
}

func newRing(n int) *ring { return &ring{buf: make([]Point, n)} }

func (r *ring) add(p Point) {
	r.buf[r.next] = p
	r.next++
	if r.next == len(r.buf) {
		r.next, r.full = 0, true
	}
}

// all returns the points oldest first.
func (r *ring) all() []Point {
	if !r.full {
		return append([]Point(nil), r.buf[:r.next]...)
	}
	return append(append([]Point(nil), r.buf[r.next:]...), r.buf[:r.next]...)
}

func pointOf(s *stats.Snapshot, recordings int) Point {
	n := s.Node
	p := Point{
		T: s.Time.UnixMilli(), CPU: n.CPUPercent, RSS: n.RSSBytes, Heap: n.HeapBytes, Goroutines: n.Goroutines,
		GCPauseNs: n.GCPauseP99Ns, GCCPU: n.GCCPUPercent, PktsIn: n.UDPPacketsInPerSec, PktsOut: n.UDPPacketsOutPerSec,
		BpsIn: n.UDPBytesInPerSec * 8, BpsOut: n.UDPBytesOutPerSec * 8,
		Rooms: n.Rooms, Particips: n.Participants, Published: n.PublishedTracks, Subscribed: n.SubscribedTracks, Recording: recordings,
	}
	var lossSum float64
	var lossN int
	var rttSum float64
	var rttN int
	for _, r := range s.Rooms {
		for _, pt := range r.Participants {
			if pt.RTTMs > 0 {
				rttSum += pt.RTTMs
				rttN++
				p.RTTMax = max(p.RTTMax, pt.RTTMs)
			}
			for _, t := range pt.Subscribed {
				if t.Kind == "video" || t.Kind == "audio" {
					lossSum += t.LossPercent
					lossN++
					p.LossMax = max(p.LossMax, t.LossPercent)
				}
			}
		}
	}
	if lossN > 0 {
		p.LossAvg = lossSum / float64(lossN)
	}
	if rttN > 0 {
		p.RTTAvg = rttSum / float64(rttN)
	}
	return p
}

func (h *Handler) tick() {
	snap := h.p.Stats.Snapshot()
	if snap == nil {
		return
	}
	if snap.Time.IsZero() {
		snap.Time = h.p.Now()
	}
	recs := h.recorders()
	active := 0
	for _, r := range recs {
		if r.OK && r.Stats != nil {
			active += r.Stats.ActiveRecordings
		}
	}
	pt := pointOf(snap, active)
	alerts := h.alerts(snap, recs)
	u := &Update{Snapshot: snap, Recorders: recs, Alerts: alerts, Point: pt}
	var sum []byte
	if h.p.Cluster != nil {
		sum, _ = json.Marshal(summaryOf(snap, pt, recs, alerts))
	}
	h.mu.Lock()
	viewers := len(h.subs)
	h.mu.Unlock()
	var b []byte
	err := errNoViewers
	if viewers > 0 { // the frame grows with participants x subscriptions; skip it when nobody watches
		b, err = json.Marshal(u)
	}
	h.mu.Lock()
	h.latest, h.latestJSON, h.summary = u, b, sum
	h.hist.add(pt)
	if err == nil {
		for ch := range h.subs {
			select {
			case ch <- b:
			default: // slow viewer: drop this frame
			}
		}
	}
	h.mu.Unlock()
}

var errNoViewers = errors.New("no viewers")

// recorders fetches recorder /stats at most every 2 s, in parallel.
func (h *Handler) recorders() []RecorderStatus {
	if len(h.p.RecorderURLs) == 0 {
		return []RecorderStatus{}
	}
	h.mu.Lock()
	if h.rec != nil && h.p.Now().Sub(h.recAt) < 2*time.Second {
		out := h.rec
		h.mu.Unlock()
		return out
	}
	h.mu.Unlock()
	out := make([]RecorderStatus, len(h.p.RecorderURLs))
	var wg sync.WaitGroup
	for i, base := range h.p.RecorderURLs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = h.fetchRecorder(base)
		}()
	}
	wg.Wait()
	h.mu.Lock()
	h.rec, h.recAt = out, h.p.Now()
	h.mu.Unlock()
	return out
}

func (h *Handler) fetchRecorder(base string) RecorderStatus {
	st := RecorderStatus{URL: base}
	u := strings.TrimRight(base, "/") + "/stats"
	resp, err := h.client.Get(u) //nolint:noctx // bounded by client timeout
	if err != nil {
		st.Error = err.Error()
		return st
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		st.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		return st
	}
	var rs recorder.Stats
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&rs); err != nil {
		st.Error = err.Error()
		return st
	}
	st.OK, st.Stats = true, &rs
	return st
}

// Alert bounds: one alert per participant for downlink loss (not one per
// subscribed track), at most maxAlerts in an update (the rest are counted in a
// summary alert), and a presenter's video only counts as stalled once the
// presenter has been in the room for stallGrace.
const (
	maxAlerts  = 100
	stallGrace = 10 * time.Second
)

// alerts evaluates the thresholds and keeps first-seen times stable.
func (h *Handler) alerts(s *stats.Snapshot, recs []RecorderStatus) []Alert {
	now := h.p.Now()
	cur := map[string]Alert{}
	add := func(id, sev, kind, msg string) { cur[id] = Alert{ID: id, Severity: sev, Kind: kind, Message: msg} }
	ncpu := max(s.Node.NumCPU, 1)
	if pct := s.Node.CPUPercent / float64(ncpu); pct > h.p.CPUPercent {
		add("cpu", "critical", "cpu", fmt.Sprintf("CPU at %.0f%% of %d cores (threshold %.0f%%)", pct, ncpu, h.p.CPUPercent))
	}
	owner := map[string]string{} // track sid -> presenter identity, per room
	for _, r := range s.Rooms {
		clear(owner)
		for _, p := range r.Participants {
			for _, t := range p.Published {
				owner[t.SID] = p.Identity
			}
		}
		for _, p := range r.Participants {
			if p.RTTMs > h.p.RTTMs {
				add("rtt:"+p.SID, "warning", "rtt", fmt.Sprintf("%s in %s: RTT %.0f ms", p.Identity, r.Name, p.RTTMs))
			}
			var worst *stats.SubscribedTrackStats
			lossy := 0
			for i, t := range p.Subscribed {
				if t.LossPercent > h.p.LossPercent {
					lossy++
					if worst == nil || t.LossPercent > worst.LossPercent {
						worst = &p.Subscribed[i]
					}
				}
			}
			if worst != nil {
				add("loss:"+p.SID, "warning", "loss", fmt.Sprintf("%s in %s: %.1f%% loss on %s from %s (%d of %d tracks above %.0f%%)",
					p.Identity, r.Name, worst.LossPercent, worst.Kind, cmp.Or(owner[worst.TrackSID], worst.TrackSID), lossy, len(p.Subscribed), h.p.LossPercent))
			}
			for _, t := range p.Published {
				if t.LossPercent > h.p.LossPercent {
					add("ploss:"+p.SID+":"+t.SID, "warning", "loss", fmt.Sprintf("%s in %s: %.1f%% uplink loss on %s", p.Identity, r.Name, t.LossPercent, t.Kind))
				}
				// Presenters are always on (ADR-0012): a video track of a presenter connected to
				// this node that stops receiving packets is stalled. Relayed presenters (no
				// connection here) are paused upstream when nobody watches them, so they are skipped.
				if t.Kind == "video" && !t.Active && !t.Muted && p.ICECandidateType != "" && now.Sub(p.JoinedAt) > stallGrace {
					add("stall:"+p.SID+":"+t.SID, "warning", "stall", fmt.Sprintf("%s in %s: no video received (track %s)", p.Identity, r.Name, t.SID))
				}
			}
		}
	}
	for _, r := range recs {
		if !r.OK {
			add("rec-down:"+r.URL, "warning", "recording", fmt.Sprintf("recorder %s unreachable: %s", r.URL, r.Error))
			continue
		}
		if r.Stats.UploadErrors > 0 || r.Stats.TrackErrors > 0 {
			add("rec-err:"+r.URL, "critical", "recording", fmt.Sprintf("recorder %s: %d upload errors, %d track errors", r.URL, r.Stats.UploadErrors, r.Stats.TrackErrors))
		}
		// A recording beyond capacity waits for a free slot and misses its start.
		if c := r.Stats.Capacity; c > 0 && r.Stats.CapacityUsed*10 >= c*9 {
			sev := "warning"
			if r.Stats.CapacityUsed >= c {
				sev = "critical"
			}
			add("rec-cap:"+r.URL, sev, "recording", fmt.Sprintf("recorder %s: capacity %d of %d units used", cmp.Or(r.Stats.WorkerID, r.URL), r.Stats.CapacityUsed, c))
		}
		for _, f := range r.Stats.Finished {
			if f.Status == "RECORDING_STATUS_FAILED" && now.Sub(f.EndedAt) < 15*time.Minute {
				add("rec-failed:"+f.RecordingID, "critical", "recording", fmt.Sprintf("recording %s failed: %s", f.RecordingID, f.Error))
			}
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return stamp(cur, h.alertAt, now)
}

// stamp keeps each alert's first-seen time in at (dropping ended ones), orders critical
// first and caps the list at maxAlerts with a summary alert. The caller holds h.mu.
func stamp(cur map[string]Alert, at map[string]time.Time, now time.Time) []Alert {
	out := make([]Alert, 0, len(cur))
	for id, a := range cur {
		if _, ok := at[id]; !ok {
			at[id] = now
		}
		a.Since = at[id]
		out = append(out, a)
	}
	for id := range at {
		if _, ok := cur[id]; !ok {
			delete(at, id)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Severity != out[j].Severity {
			return out[i].Severity == "critical"
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > maxAlerts {
		rest := out[maxAlerts-1:]
		since := rest[0].Since
		for _, a := range rest {
			if a.Since.Before(since) {
				since = a.Since
			}
		}
		out = append(out[:maxAlerts-1], Alert{ID: "more", Severity: "warning", Kind: "more", Since: since,
			Message: fmt.Sprintf("%d more alerts not shown", len(rest))})
	}
	return out
}
