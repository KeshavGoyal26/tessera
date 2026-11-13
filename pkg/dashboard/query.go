package dashboard

import (
	"cmp"
	"compress/gzip"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/KeshavGoyal26/tessera/pkg/stats"
)

// Scale-safe views of the latest snapshot: the live frame stays a few KB at any size, and
// lists are filtered, sorted and paged here, never shipped whole to the browser. The index
// is built at most once per sample, by the first query that needs it; answers are cached
// per query until the next sample.

// Live is what the SSE stream sends every sample.
type Live struct {
	Version     uint64            `json:"version"`
	Point       Point             `json:"point"`
	Node        stats.NodeStats   `json:"node"`
	Counts      Counts            `json:"counts"`
	Quality     map[string]int    `json:"quality"`
	ICE         map[string]int    `json:"ice"`
	Alerts      []Alert           `json:"alerts"`
	AlertsTotal int               `json:"alerts_total"`
	Recorders   []RecorderSummary `json:"recorders"`
}

// Counts are the participants of this node by role.
type Counts struct {
	Rooms      int `json:"rooms"`
	Presenters int `json:"presenters"`
	Viewers    int `json:"viewers"`
	Relays     int `json:"relays"`
	Recorders  int `json:"recorders"`
}

// RecorderSummary is a recorder's totals, without its per-recording lists.
type RecorderSummary struct {
	URL               string `json:"url"`
	OK                bool   `json:"ok"`
	Error             string `json:"error,omitempty"`
	Capacity          int    `json:"capacity"`
	CapacityUsed      int    `json:"capacity_used"`
	ActiveRecordings  int    `json:"active_recordings"`
	TracksRecording   int    `json:"tracks_recording"`
	SegmentsWritten   uint64 `json:"segments_written"`
	BytesUploaded     uint64 `json:"bytes_uploaded"`
	UploadBacklog     int64  `json:"upload_backlog"`
	UploadErrors      uint64 `json:"upload_errors"`
	TrackErrors       uint64 `json:"track_errors"`
	SegmentDelayMs    int64  `json:"segment_delay_ms"`
	SegmentDelayMaxMs int64  `json:"segment_delay_max_ms"`
}

// RoomRow is one row of the rooms list.
type RoomRow struct {
	Name         string    `json:"name"`
	SID          string    `json:"sid"`
	CreatedAt    time.Time `json:"created_at"`
	Participants int       `json:"participants"`
	Presenters   int       `json:"presenters"`
	Viewers      int       `json:"viewers"`
	Relays       int       `json:"relays"`
	Recording    bool      `json:"recording"`
	Speakers     []string  `json:"speakers"`
	WorstQuality string    `json:"worst_quality"`
}

// ParticipantBrief is one row of the participants list.
type ParticipantBrief struct {
	Room        string    `json:"room"`
	SID         string    `json:"sid"`
	Identity    string    `json:"identity"`
	Name        string    `json:"name,omitempty"`
	Role        string    `json:"role"`
	Kind        string    `json:"kind"`
	Hidden      bool      `json:"hidden,omitempty"`
	State       string    `json:"state"`
	Quality     string    `json:"quality"`
	Score       float64   `json:"score"`
	RTTMs       float64   `json:"rtt_ms"`
	LossPercent float64   `json:"loss_percent"`
	BitrateBps  float64   `json:"bitrate_bps"`
	BWEBps      int64     `json:"bwe_bps"`
	ICEType     string    `json:"ice_type"`
	ICESetupMs  int64     `json:"ice_setup_ms"`
	DTLSSetupMs int64     `json:"dtls_setup_ms"`
	Published   int       `json:"published"`
	Subscribed  int       `json:"subscribed"`
	Paused      int       `json:"paused"`
	NoVideo     bool      `json:"no_video"`
	ClientSDK   string    `json:"client_sdk,omitempty"`
	JoinedAt    time.Time `json:"joined_at"`
}

// TrackBrief is one row of the published-tracks list.
type TrackBrief struct {
	Room           string  `json:"room"`
	Participant    string  `json:"participant"`
	ParticipantSID string  `json:"participant_sid"`
	SID            string  `json:"sid"`
	Kind           string  `json:"kind"`
	Source         string  `json:"source"`
	Codec          string  `json:"codec"`
	Width          uint32  `json:"width,omitempty"`
	Height         uint32  `json:"height,omitempty"`
	BitrateBps     float64 `json:"bitrate_bps"`
	LossPercent    float64 `json:"loss_percent"`
	JitterMs       float64 `json:"jitter_ms"`
	Subscribers    int     `json:"subscribers"`
	Active         bool    `json:"active"`
	Muted          bool    `json:"muted"`
	NACKs          uint64  `json:"nacks"`
	PLIs           uint64  `json:"plis"`
}

// SubscriberBrief is one subscriber of a track.
type SubscriberBrief struct {
	SID         string  `json:"sid"`
	Identity    string  `json:"identity"`
	BitrateBps  float64 `json:"bitrate_bps"`
	LossPercent float64 `json:"loss_percent"`
	JitterMs    float64 `json:"jitter_ms"`
	RTTMs       float64 `json:"rtt_ms"`
	BWEBps      int64   `json:"bwe_bps"`
	Paused      bool    `json:"paused"`
	Muted       bool    `json:"muted"`
	Restarts    uint64  `json:"restarts"`
}

// Stat is an average, 95th percentile and maximum.
type Stat struct {
	Avg float64 `json:"avg"`
	P95 float64 `json:"p95"`
	Max float64 `json:"max"`
}

const (
	pageDefault   = 100
	pageMax       = 500
	detailSubsMax = 500 // a participant's subscribed tracks in its detail
	trackSubsMax  = 100 // a track's subscribers in its detail (worst first)
	liveAlerts    = 50  // alerts in the live frame (most severe first); alerts_total counts all
)

// qualityRank orders connection qualities worst first.
var qualityRank = map[string]int{stats.QualityLost: 0, stats.QualityPoor: 1, stats.QualityUnknown: 2, stats.QualityGood: 3, stats.QualityExcellent: 4}

func rankOf(q string) int {
	if r, ok := qualityRank[q]; ok {
		return r
	}
	return qualityRank[stats.QualityUnknown]
}

// roleOf is the dashboard's role of a participant (web/admin role()).
func roleOf(p *stats.ParticipantStats) string {
	switch {
	case p.Kind == "relay":
		return "relay"
	case p.Kind == "recorder":
		return "recorder"
	case len(p.Published) > 0:
		return "presenter"
	}
	return "viewer"
}

// index is the latest snapshot flattened for queries.
type index struct {
	version uint64
	snap    *stats.Snapshot
	rooms   []RoomRow
	parts   []ParticipantBrief
	tracks  []TrackBrief
}

func buildIndex(version uint64, s *stats.Snapshot) *index {
	ix := &index{version: version, snap: s}
	for ri := range s.Rooms {
		r := &s.Rooms[ri]
		rb := RoomRow{Name: r.Name, SID: r.SID, CreatedAt: r.CreatedAt, Participants: len(r.Participants),
			Recording: r.ActiveRecording, WorstQuality: stats.QualityExcellent}
		rb.Presenters, rb.Viewers, rb.Relays = roomRoles(r)
		for i, sp := range r.ActiveSpeakers {
			if i == 3 {
				break
			}
			rb.Speakers = append(rb.Speakers, sp.Identity)
		}
		for pi := range r.Participants {
			p := &r.Participants[pi]
			b := briefOf(r.Name, p)
			ix.parts = append(ix.parts, b)
			if b.Role != "relay" && b.Role != "recorder" && rankOf(b.Quality) < rankOf(rb.WorstQuality) {
				rb.WorstQuality = b.Quality
			}
			for _, t := range p.Published {
				ix.tracks = append(ix.tracks, TrackBrief{Room: r.Name, Participant: p.Identity, ParticipantSID: p.SID, SID: t.SID,
					Kind: t.Kind, Source: t.Source, Codec: t.Codec, Width: t.Width, Height: t.Height, BitrateBps: t.BitrateBps,
					LossPercent: t.LossPercent, JitterMs: t.JitterMs, Subscribers: t.Subscribers, Active: t.Active, Muted: t.Muted,
					NACKs: t.NACKs, PLIs: t.PLIs})
			}
		}
		if len(r.Participants) == 0 {
			rb.WorstQuality = ""
		}
		ix.rooms = append(ix.rooms, rb)
	}
	return ix
}

func briefOf(room string, p *stats.ParticipantStats) ParticipantBrief {
	b := ParticipantBrief{Room: room, SID: p.SID, Identity: p.Identity, Name: p.Name, Role: roleOf(p), Kind: p.Kind, Hidden: p.Hidden,
		State: p.State, Quality: cmp.Or(p.ConnectionQuality, stats.QualityUnknown), Score: p.QualityScore, RTTMs: p.RTTMs,
		BWEBps: p.BandwidthEstimateBps, ICEType: p.ICECandidateType, ICESetupMs: p.ICESetupMs, DTLSSetupMs: p.DTLSSetupMs,
		Published: len(p.Published), Subscribed: len(p.Subscribed), ClientSDK: p.ClientSDK, JoinedAt: p.JoinedAt}
	for _, t := range p.Published {
		b.LossPercent = max(b.LossPercent, t.LossPercent)
		b.BitrateBps += t.BitrateBps
		// A presenter relayed from another node (no connection here) is paused upstream while unwatched.
		if t.Kind == "video" && !t.Active && !t.Muted && p.ICECandidateType != "" {
			b.NoVideo = true
		}
	}
	for _, t := range p.Subscribed {
		b.LossPercent = max(b.LossPercent, t.LossPercent)
		b.BitrateBps += t.BitrateBps
		if t.Paused {
			b.Paused++
		}
	}
	return b
}

// connectedHere: a participant with its own connection to this node, not a relay session, a
// recorder or a presenter relayed to this edge from its media node (no ICE here).
func (b *ParticipantBrief) connectedHere() bool {
	return b.Role != "relay" && b.Role != "recorder" && !(b.Published > 0 && b.ICEType == "")
}

// live builds the frame for one sample.
func liveOf(version uint64, s *stats.Snapshot, pt Point, alerts []Alert, total int, recs []RecorderStatus) *Live {
	if len(alerts) > liveAlerts {
		alerts = alerts[:liveAlerts]
	}
	l := &Live{Version: version, Point: pt, Node: s.Node, Alerts: alerts, AlertsTotal: total,
		Quality: map[string]int{}, ICE: map[string]int{}, Recorders: make([]RecorderSummary, 0, len(recs))}
	l.Counts.Rooms = len(s.Rooms)
	for ri := range s.Rooms {
		r := &s.Rooms[ri]
		p, v, rl := roomRoles(r)
		l.Counts.Presenters += p
		l.Counts.Viewers += v
		l.Counts.Relays += rl
		for pi := range r.Participants {
			pp := &r.Participants[pi]
			switch roleOf(pp) {
			case "relay":
				continue
			case "recorder":
				l.Counts.Recorders++
				continue
			}
			l.Quality[cmp.Or(pp.ConnectionQuality, stats.QualityUnknown)]++
			l.ICE[pp.ICECandidateType]++
		}
	}
	for _, r := range recs {
		rs := RecorderSummary{URL: r.URL, OK: r.OK, Error: r.Error}
		if st := r.Stats; st != nil {
			rs.Capacity, rs.CapacityUsed, rs.ActiveRecordings, rs.TracksRecording = st.Capacity, st.CapacityUsed, st.ActiveRecordings, st.TracksRecording
			rs.SegmentsWritten, rs.BytesUploaded, rs.UploadBacklog = st.SegmentsWritten, st.BytesUploaded, st.UploadBacklog
			rs.UploadErrors, rs.TrackErrors, rs.SegmentDelayMs, rs.SegmentDelayMaxMs = st.UploadErrors, st.TrackErrors, st.SegmentDelayMs, st.SegmentDelayMax
		}
		l.Recorders = append(l.Recorders, rs)
	}
	return l
}

// ---- paging -------------------------------------------------------------------

type pageReq struct {
	q             string
	sort          string
	desc          bool
	offset, limit int
}

func pageOf(v url.Values) pageReq {
	p := pageReq{q: strings.ToLower(strings.TrimSpace(v.Get("q"))), sort: v.Get("sort"), desc: v.Get("desc") == "1", limit: pageDefault}
	if n, err := strconv.Atoi(v.Get("offset")); err == nil && n > 0 {
		p.offset = n
	}
	if n, err := strconv.Atoi(v.Get("limit")); err == nil && n > 0 {
		p.limit = min(n, pageMax)
	}
	return p
}

func (p pageReq) match(fields ...string) bool {
	if p.q == "" {
		return true
	}
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f), p.q) {
			return true
		}
	}
	return false
}

// page filters, sorts (cmpFn ascending; desc reverses) and slices items.
func page[T any](p pageReq, items []T, keep func(*T) bool, cmpFn func(a, b *T) int) (total int, out []T) {
	sel := make([]T, 0, len(items))
	for i := range items {
		if keep(&items[i]) {
			sel = append(sel, items[i])
		}
	}
	if cmpFn != nil {
		slices.SortStableFunc(sel, func(a, b T) int {
			if p.desc {
				return cmpFn(&b, &a)
			}
			return cmpFn(&a, &b)
		})
	}
	total = len(sel)
	if p.offset >= total {
		return total, []T{}
	}
	return total, sel[p.offset:min(p.offset+p.limit, total)]
}

// ---- queries --------------------------------------------------------------------

var errGone = errors.New("not found")

// queryLocal answers one query route on this node.
func (h *Handler) queryLocal(route string, v url.Values) (any, error) {
	ix := h.indexNow()
	p := pageOf(v)
	switch route {
	case "rooms":
		total, items := page(p, ix.rooms, func(r *RoomRow) bool { return p.match(r.Name) }, roomCmp(p.sort))
		return map[string]any{"version": ix.version, "total": total, "offset": p.offset, "items": items}, nil
	case "participants":
		room, role, connected := v.Get("room"), v.Get("role"), v.Get("connected") == "1"
		keep := func(b *ParticipantBrief) bool {
			return (room == "" || b.Room == room) && (role == "" || b.Role == role) && (!connected || b.connectedHere()) &&
				p.match(b.Identity, b.Name, b.SID, b.Room)
		}
		var sel []ParticipantBrief
		for i := range ix.parts {
			if keep(&ix.parts[i]) {
				sel = append(sel, ix.parts[i])
			}
		}
		total, items := page(p, sel, func(*ParticipantBrief) bool { return true }, partCmp(p.sort))
		res := map[string]any{"version": ix.version, "total": total, "offset": p.offset, "items": items}
		for k, val := range partAggregates(sel) {
			res[k] = val
		}
		return res, nil
	case "tracks":
		kind := v.Get("kind")
		sort := cmp.Or(p.sort, "loss")
		if p.sort == "" {
			p.desc = true
		}
		total, items := page(p, ix.tracks, func(t *TrackBrief) bool {
			return (kind == "" || t.Kind == kind) && p.match(t.Room, t.Participant, t.SID, t.Codec)
		}, trackCmp(sort))
		return map[string]any{"version": ix.version, "total": total, "offset": p.offset, "items": items}, nil
	case "participant":
		return participantDetail(ix, v.Get("room"), v.Get("sid"))
	case "track":
		return trackDetail(ix, v.Get("sid"))
	case "recorders":
		return map[string]any{"version": ix.version, "recorders": h.recorders()}, nil
	case "live":
		h.mu.Lock()
		l := h.live
		h.mu.Unlock()
		return l, nil
	}
	return nil, errUnknown
}

var errUnknown = errors.New("unknown endpoint")

// queryRoutes are the GET routes answered by queryLocal (and forwarded with node=).
var queryRoutes = map[string]bool{"rooms": true, "participants": true, "tracks": true, "participant": true, "track": true, "recorders": true, "live": true}

// serveQuery answers a query route, on this node or forwarded to node=<id>, cached per
// sample (per query string).
func (h *Handler) serveQuery(w http.ResponseWriter, r *http.Request, route string) {
	v := r.URL.Query()
	node := v.Get("node")
	v.Del("node")
	var b []byte
	var err error
	if c := h.p.Cluster; c != nil && node != "" && node != c.Self {
		fwd := url.Values{}
		for k, vals := range v {
			fwd[k] = vals
		}
		fwd.Set("api", route)
		qs := fwd.Encode()
		b, err = h.cachedFetch("node", "q:"+node+":"+qs, func() ([]byte, error) { return h.fetch(r.Context(), node, qs) })
	} else {
		b, err = h.queryBytes(route, v)
	}
	switch {
	case errors.Is(err, errGone):
		jsonErr(w, http.StatusNotFound, "not found (it may have ended)")
	case errors.Is(err, errUnknown):
		jsonErr(w, http.StatusNotFound, "unknown endpoint")
	case err != nil:
		jsonErr(w, http.StatusBadGateway, err.Error())
	default:
		writeBody(w, r, b)
	}
}

// gzipOver: JSON bodies above this are compressed when the client accepts it (stats JSON
// shrinks 5-10x).
const gzipOver = 2 << 10

// writeBody writes a JSON body, gzipped when large and accepted.
func writeBody(w http.ResponseWriter, r *http.Request, b []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Add("Vary", "Accept-Encoding")
	if len(b) < gzipOver || !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		_, _ = w.Write(b)
		return
	}
	w.Header().Set("Content-Encoding", "gzip")
	zw := gzip.NewWriter(w)
	_, _ = zw.Write(b)
	_ = zw.Close()
}

// queryBytes is queryLocal as JSON, cached until the next sample.
func (h *Handler) queryBytes(route string, v url.Values) ([]byte, error) {
	key := route + "?" + v.Encode()
	h.qmu.Lock()
	if h.qcacheVer == h.version.Load() {
		if b, ok := h.qcache[key]; ok {
			h.qmu.Unlock()
			return b, nil
		}
	}
	h.qmu.Unlock()
	res, err := h.queryLocal(route, v)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(res)
	if err != nil {
		return nil, err
	}
	h.qmu.Lock()
	if ver := h.version.Load(); h.qcacheVer != ver {
		h.qcache, h.qcacheVer = map[string][]byte{}, ver
	}
	if len(h.qcache) < 256 { // distinct pages viewed within one sample: few
		h.qcache[key] = b
	}
	h.qmu.Unlock()
	return b, nil
}

// indexNow is the index of the latest sample, built by the first query after it.
func (h *Handler) indexNow() *index {
	h.mu.Lock()
	s, ver := h.snapLatest(), h.version.Load()
	h.mu.Unlock()
	h.qmu.Lock()
	defer h.qmu.Unlock()
	if h.idx == nil || h.idx.version != ver {
		h.idx = buildIndex(ver, s)
	}
	return h.idx
}

// snapLatest is the latest snapshot (never nil). The caller holds h.mu.
func (h *Handler) snapLatest() *stats.Snapshot {
	if h.latest == nil || h.latest.Snapshot == nil {
		return &stats.Snapshot{}
	}
	return h.latest.Snapshot
}

func participantDetail(ix *index, room, sid string) (any, error) {
	for ri := range ix.snap.Rooms {
		r := &ix.snap.Rooms[ri]
		if r.Name != room {
			continue
		}
		names := map[string]string{}
		for pi := range r.Participants {
			names[r.Participants[pi].SID] = r.Participants[pi].Identity
		}
		for pi := range r.Participants {
			p := r.Participants[pi] // a copy: the cap below must not change the snapshot
			if p.SID != sid {
				continue
			}
			total := len(p.Subscribed)
			if total > detailSubsMax {
				p.Subscribed = p.Subscribed[:detailSubsMax]
			}
			used := map[string]string{}
			for _, t := range p.Subscribed {
				used[t.PublisherSID] = names[t.PublisherSID]
			}
			return map[string]any{"version": ix.version, "room": r.Name, "participant": p, "names": used, "subscribed_total": total}, nil
		}
	}
	return nil, errGone
}

func trackDetail(ix *index, sid string) (any, error) {
	var res map[string]any
	var subs []SubscriberBrief
	for ri := range ix.snap.Rooms {
		r := &ix.snap.Rooms[ri]
		for pi := range r.Participants {
			p := &r.Participants[pi]
			for _, t := range p.Published {
				if t.SID == sid {
					res = map[string]any{"version": ix.version, "room": r.Name, "participant": p.Identity, "participant_sid": p.SID, "track": t}
				}
			}
			for _, t := range p.Subscribed {
				if t.TrackSID == sid {
					subs = append(subs, SubscriberBrief{SID: p.SID, Identity: p.Identity, BitrateBps: t.BitrateBps, LossPercent: t.LossPercent,
						JitterMs: t.JitterMs, RTTMs: t.RTTMs, BWEBps: t.BandwidthEstimateBps, Paused: t.Paused, Muted: t.Muted, Restarts: t.Restarts})
				}
			}
		}
	}
	if res == nil {
		return nil, errGone
	}
	slices.SortStableFunc(subs, func(a, b SubscriberBrief) int { return cmp.Compare(b.LossPercent, a.LossPercent) })
	res["subscribers_total"] = len(subs)
	res["subscribers"] = append([]SubscriberBrief{}, subs[:min(len(subs), trackSubsMax)]...)
	return res, nil
}

func partAggregates(sel []ParticipantBrief) map[string]any {
	q, ice := map[string]int{}, map[string]int{}
	var iceMs, dtlsMs []float64
	relay := 0
	for i := range sel {
		b := &sel[i]
		if b.Role == "relay" || b.Role == "recorder" {
			continue
		}
		q[b.Quality]++
		ice[b.ICEType]++
		if b.ICEType == "relay" {
			relay++
		}
		if b.ICESetupMs > 0 {
			iceMs = append(iceMs, float64(b.ICESetupMs))
		}
		if b.DTLSSetupMs > 0 {
			dtlsMs = append(dtlsMs, float64(b.DTLSSetupMs))
		}
	}
	return map[string]any{"quality": q, "ice": ice, "ice_setup_ms": statOf(iceMs), "dtls_setup_ms": statOf(dtlsMs), "relay": relay}
}

func statOf(v []float64) Stat {
	if len(v) == 0 {
		return Stat{}
	}
	slices.Sort(v)
	var sum float64
	for _, x := range v {
		sum += x
	}
	return Stat{Avg: sum / float64(len(v)), P95: v[min(len(v)-1, len(v)*95/100)], Max: v[len(v)-1]}
}

func roomCmp(sort string) func(a, b *RoomRow) int {
	switch sort {
	case "participants":
		return func(a, b *RoomRow) int {
			return cmp.Or(cmp.Compare(a.Participants, b.Participants), cmp.Compare(a.Name, b.Name))
		}
	case "presenters":
		return func(a, b *RoomRow) int {
			return cmp.Or(cmp.Compare(a.Presenters, b.Presenters), cmp.Compare(a.Name, b.Name))
		}
	case "viewers":
		return func(a, b *RoomRow) int {
			return cmp.Or(cmp.Compare(a.Viewers, b.Viewers), cmp.Compare(a.Name, b.Name))
		}
	case "created":
		return func(a, b *RoomRow) int {
			return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), cmp.Compare(a.Name, b.Name))
		}
	}
	return func(a, b *RoomRow) int { return cmp.Compare(a.Name, b.Name) }
}

func partCmp(sort string) func(a, b *ParticipantBrief) int {
	byID := func(a, b *ParticipantBrief) int {
		return cmp.Or(cmp.Compare(a.Identity, b.Identity), cmp.Compare(a.SID, b.SID))
	}
	switch sort {
	case "quality":
		return func(a, b *ParticipantBrief) int {
			return cmp.Or(cmp.Compare(rankOf(a.Quality), rankOf(b.Quality)), cmp.Compare(b.RTTMs, a.RTTMs), byID(a, b))
		}
	case "rtt":
		return func(a, b *ParticipantBrief) int { return cmp.Or(cmp.Compare(a.RTTMs, b.RTTMs), byID(a, b)) }
	case "loss":
		return func(a, b *ParticipantBrief) int { return cmp.Or(cmp.Compare(a.LossPercent, b.LossPercent), byID(a, b)) }
	case "bitrate":
		return func(a, b *ParticipantBrief) int { return cmp.Or(cmp.Compare(a.BitrateBps, b.BitrateBps), byID(a, b)) }
	case "joined":
		return func(a, b *ParticipantBrief) int { return cmp.Or(a.JoinedAt.Compare(b.JoinedAt), byID(a, b)) }
	case "room":
		return func(a, b *ParticipantBrief) int { return cmp.Or(cmp.Compare(a.Room, b.Room), byID(a, b)) }
	}
	return byID
}

func trackCmp(sort string) func(a, b *TrackBrief) int {
	bySID := func(a, b *TrackBrief) int { return cmp.Compare(a.SID, b.SID) }
	switch sort {
	case "bitrate":
		return func(a, b *TrackBrief) int { return cmp.Or(cmp.Compare(a.BitrateBps, b.BitrateBps), bySID(a, b)) }
	case "jitter":
		return func(a, b *TrackBrief) int { return cmp.Or(cmp.Compare(a.JitterMs, b.JitterMs), bySID(a, b)) }
	case "subscribers":
		return func(a, b *TrackBrief) int { return cmp.Or(cmp.Compare(a.Subscribers, b.Subscribers), bySID(a, b)) }
	case "room":
		return func(a, b *TrackBrief) int { return cmp.Or(cmp.Compare(a.Room, b.Room), bySID(a, b)) }
	case "participant":
		return func(a, b *TrackBrief) int { return cmp.Or(cmp.Compare(a.Participant, b.Participant), bySID(a, b)) }
	}
	return func(a, b *TrackBrief) int { return cmp.Or(cmp.Compare(a.LossPercent, b.LossPercent), bySID(a, b)) }
}
