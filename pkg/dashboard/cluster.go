package dashboard

// Cluster view (ADR-0012). The registry in Redis already lists the nodes; each node adds
// its Summary (a few hundred bytes, computed from the sample the dashboard takes anyway)
// to the heartbeat it already sends. Everything else is fetched only while an operator
// looks at it: one node's state, one room across the nodes, or the room index, from the
// nodes' own dashboards over the private network (GET /admin/api/node, signed with the
// cluster's API key), with short caches so several open dashboards cost the same as one.

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/KeshavGoyal26/tessera/pkg/auth"
	"github.com/KeshavGoyal26/tessera/pkg/cluster"
	"github.com/KeshavGoyal26/tessera/pkg/config"
	"github.com/KeshavGoyal26/tessera/pkg/stats"
)

// ClusterParams turn on the cluster view. Nil in single-node mode.
type ClusterParams struct {
	Nodes interface {
		Nodes(ctx context.Context) ([]cluster.NodeInfo, error)
	}
	Self, Role string
	// APIKey/Secret sign this dashboard's requests to other nodes; Verifier checks theirs.
	APIKey, Secret string
	Verifier       auth.Verifier
	// Sessions stores ended sessions cluster-wide (Redis) for support lookups by identity;
	// nil keeps them only in this node's journal.
	Sessions interface {
		SaveSession(ctx context.Context, identity, sid string, data []byte) error
		UserSessions(ctx context.Context, identity string) ([][]byte, error)
	}
	// KeepHistory samples the cluster every clusterEvery even when nobody looks (edges,
	// where operators open the dashboard); other nodes sample for an hour after a visit.
	KeepHistory bool
}

const (
	clusterEvery   = 5 * time.Second
	clusterHistory = 720             // one hour at clusterEvery
	nodeLate       = 4 * time.Second // two missed 2 s heartbeats
	downMemory     = time.Hour       // how long a node that left is listed as down
	fetchTimeout   = 2 * time.Second
	maxIndexRooms  = 5000
)

// cache lifetimes per request kind: the browser polls a node or a room every 2 s and
// the room index every 5 s; a second dashboard within that window costs nothing.
var cacheFor = map[string]time.Duration{"node": time.Second, "room": 2 * time.Second, "rooms": 4 * time.Second}

// Summary is what a node publishes about itself with each heartbeat.
type Summary struct {
	Version   string  `json:"version"`
	StartedAt int64   `json:"started_at"` // unix ms
	CPU       float64 `json:"cpu"`        // percent of all cores
	Cores     int     `json:"cores"`
	RSS       uint64  `json:"rss"`
	BpsIn     float64 `json:"bps_in"`
	BpsOut    float64 `json:"bps_out"`
	Rooms     int     `json:"rooms"`
	// Presenters publish on this node (relayed copies on an edge are not counted); viewers only watch.
	Presenters int     `json:"presenters"`
	Viewers    int     `json:"viewers"`
	Relays     int     `json:"relays"`
	Published  int     `json:"published"`
	Subscribed int     `json:"subscribed"`
	Recordings int     `json:"recordings"`
	RecUsed    int     `json:"rec_used"`
	RecCap     int     `json:"rec_cap"`
	LossMax    float64 `json:"loss_max"`
	RTTAvg     float64 `json:"rtt_avg"`
	Critical   int     `json:"critical"`
	Warnings   int     `json:"warnings"`
}

// roles counts participants the way the dashboard shows them.
func roles(s *stats.Snapshot) (presenters, viewers, relays int) {
	for _, r := range s.Rooms {
		p, v, rl := roomRoles(&r)
		presenters, viewers, relays = presenters+p, viewers+v, relays+rl
	}
	return
}

func roomRoles(r *stats.RoomStats) (presenters, viewers, relays int) {
	for _, p := range r.Participants {
		switch {
		case p.Kind == "relay":
			relays++
		case p.Kind == "recorder":
		case len(p.Published) > 0 && p.ICECandidateType == "":
			// a presenter relayed to this edge from its media node: counted there
		case len(p.Published) > 0:
			presenters++
		default:
			viewers++
		}
	}
	return
}

func summaryOf(s *stats.Snapshot, pt Point, recs []RecorderStatus, alerts []Alert) Summary {
	n := s.Node
	sum := Summary{Version: n.Version, StartedAt: n.StartedAt.UnixMilli(), CPU: n.CPUPercent / float64(max(n.NumCPU, 1)),
		Cores: n.NumCPU, RSS: n.RSSBytes, BpsIn: pt.BpsIn, BpsOut: pt.BpsOut, Rooms: n.Rooms,
		Published: n.PublishedTracks, Subscribed: n.SubscribedTracks, Recordings: pt.Recording, LossMax: pt.LossMax, RTTAvg: pt.RTTAvg}
	sum.Presenters, sum.Viewers, sum.Relays = roles(s)
	for _, r := range recs {
		if r.OK && r.Stats != nil {
			sum.RecUsed += r.Stats.CapacityUsed
			sum.RecCap += r.Stats.Capacity
		}
	}
	for _, a := range alerts {
		if a.Severity == "critical" {
			sum.Critical++
		} else {
			sum.Warnings++
		}
	}
	return sum
}

// Summary returns this node's summary (JSON) for the cluster heartbeat.
func (h *Handler) Summary() []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.summary
}

// latestBytes is the latest Update as JSON, marshalled at most once per sample.
func (h *Handler) latestBytes() ([]byte, error) {
	h.mu.Lock()
	u, b := h.latest, h.latestJSON
	h.mu.Unlock()
	if b != nil {
		return b, nil
	}
	b, err := json.Marshal(u)
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	if h.latest == u {
		h.latestJSON = b
	}
	h.mu.Unlock()
	return b, nil
}

// liveBytes is the latest Live frame as JSON (cluster/node: another node's live view).
func (h *Handler) liveBytes() ([]byte, error) {
	h.mu.Lock()
	b := h.liveJSON
	h.mu.Unlock()
	if b == nil {
		return nil, errors.New("no sample yet")
	}
	return b, nil
}

// ---- served to other nodes ---------------------------------------------------

// RoomBrief is one room in a node's room index.
type RoomBrief struct {
	Name       string `json:"name"`
	Presenters int    `json:"presenters"`
	Viewers    int    `json:"viewers"`
	Relays     int    `json:"relays"`
	Recording  bool   `json:"recording,omitempty"`
}

// NodeRoom is one node's part of a room.
type NodeRoom struct {
	Node  string           `json:"node"`
	Role  string           `json:"role"`
	Room  *stats.RoomStats `json:"room,omitempty"`
	Error string           `json:"error,omitempty"`
}

// nodeAPI serves GET /admin/api/node to the other nodes' dashboards (bearer token signed
// with a cluster API key, room_admin grant): the latest update by default, ?room=<name>
// for one room, ?rooms for the room index, ?history for the last hour.
func (h *Handler) nodeAPI(w http.ResponseWriter, r *http.Request) {
	c := h.p.Cluster
	if c == nil || c.Verifier == nil {
		jsonErr(w, http.StatusNotFound, "unknown endpoint")
		return
	}
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	claims, err := c.Verifier.Verify(tok)
	if !ok || err != nil || claims.Grants == nil || !claims.Grants.RoomAdmin {
		jsonErr(w, http.StatusUnauthorized, "a cluster token is required")
		return
	}
	if r.Method != http.MethodGet {
		jsonErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	q := r.URL.Query()
	if route := q.Get("api"); route != "" { // a query forwarded by another node's dashboard (node=)
		q.Del("api")
		b, err := h.queryBytes(route, q)
		switch {
		case errors.Is(err, errGone), errors.Is(err, errUnknown):
			jsonErr(w, http.StatusNotFound, err.Error())
		case err != nil:
			jsonErr(w, 500, err.Error())
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(b)
		}
		return
	}
	switch {
	case q.Has("room"):
		writeJSON(w, 200, h.localRoom(q.Get("room")))
	case q.Has("rooms"):
		writeJSON(w, 200, h.localIndex())
	case q.Has("history"):
		h.mu.Lock()
		pts := h.hist.all()
		h.mu.Unlock()
		writeJSON(w, 200, map[string]any{"interval_ms": h.p.Interval.Milliseconds(), "points": pts})
	default:
		b, err := h.liveBytes()
		if err != nil {
			jsonErr(w, 500, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(b)
	}
}

func (h *Handler) snap() *stats.Snapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.latest == nil {
		return &stats.Snapshot{}
	}
	return h.latest.Snapshot
}

func (h *Handler) localRoom(name string) NodeRoom {
	out := NodeRoom{Node: h.p.Cluster.Self, Role: h.p.Cluster.Role}
	rooms := h.snap().Rooms
	for i := range rooms {
		if rooms[i].Name == name {
			out.Room = &rooms[i]
		}
	}
	return out
}

func (h *Handler) localIndex() map[string]any {
	rooms := h.snap().Rooms
	out := make([]RoomBrief, 0, min(len(rooms), maxIndexRooms))
	for i := range rooms {
		if len(out) == maxIndexRooms {
			break
		}
		p, v, rl := roomRoles(&rooms[i])
		out = append(out, RoomBrief{Name: rooms[i].Name, Presenters: p, Viewers: v, Relays: rl, Recording: rooms[i].ActiveRecording})
	}
	return map[string]any{"node": h.p.Cluster.Self, "role": h.p.Cluster.Role, "rooms": out, "total": len(rooms)}
}

// ---- the cluster view (this node's dashboard) --------------------------------

// ClusterPoint is one cluster history sample.
type ClusterPoint struct {
	T          int64   `json:"t"` // unix ms
	Media      int     `json:"media"`
	Edges      int     `json:"edges"`
	Down       int     `json:"down"`
	Presenters int     `json:"presenters"`
	Viewers    int     `json:"viewers"`
	BpsIn      float64 `json:"bps_in"`
	BpsOut     float64 `json:"bps_out"`
	CPUMax     float64 `json:"cpu_max"`
	CPUAvg     float64 `json:"cpu_avg"`
	Recordings int     `json:"recordings"`
	RecUsed    int     `json:"rec_used"`
	RecCap     int     `json:"rec_cap"`
	LossMax    float64 `json:"loss_max"`
}

// NodeView is one node in the cluster view.
type NodeView struct {
	ID        string   `json:"id"`
	Role      string   `json:"role"`
	Self      bool     `json:"self,omitempty"`
	Draining  bool     `json:"draining,omitempty"`
	Down      bool     `json:"down,omitempty"`
	Reachable bool     `json:"reachable"` // its dashboard can be fetched (has a private URL)
	Seen      int64    `json:"seen"`      // unix ms of the last heartbeat
	Summary   *Summary `json:"summary,omitempty"`
}

// ClusterState is GET /admin/api/cluster.
type ClusterState struct {
	Capacity map[string]Capacity `json:"capacity,omitempty"` // per role: media, edge
	Self     string              `json:"self"`
	Time     int64               `json:"time"`
	Error    string              `json:"error,omitempty"`
	Nodes    []NodeView          `json:"nodes"`
	Alerts   []Alert             `json:"alerts"`
	Point    *ClusterPoint       `json:"point,omitempty"`
}

type clusterData struct {
	mu      sync.Mutex
	urls    map[string]string    // node id -> private URL (never sent to browsers)
	last    map[string]NodeView  // nodes seen within downMemory
	view    *ClusterState        // latest sample
	hist    []ClusterPoint       // ring, oldest first after all()
	next    int                  // ring position
	alertAt map[string]time.Time // first-seen times of cluster alerts
	wanted  time.Time            // last request: keep sampling for an hour after it
	cache   map[string]cached
	tok     string
	tokAt   time.Time
}

type cached struct {
	at   time.Time
	body []byte
}

func (h *Handler) clusterLoop() {
	t := time.NewTicker(clusterEvery)
	defer t.Stop()
	for {
		select {
		case <-h.stop:
			return
		case <-t.C:
			c := &h.cl
			c.mu.Lock()
			want := h.p.Cluster.KeepHistory || h.p.Now().Sub(c.wanted) < time.Hour
			c.mu.Unlock()
			if want {
				h.sampleCluster()
			}
		}
	}
}

// sampleCluster reads the registry once and derives the node list, totals and alerts.
func (h *Handler) sampleCluster() *ClusterState {
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()
	nodes, err := h.p.Cluster.Nodes.Nodes(ctx)
	now := h.p.Now()
	c := &h.cl
	c.mu.Lock()
	defer c.mu.Unlock()
	st := &ClusterState{Self: h.p.Cluster.Self, Time: now.UnixMilli()}
	cur := map[string]Alert{}
	add := func(id, sev, kind, msg string) { cur[id] = Alert{ID: id, Severity: sev, Kind: kind, Message: msg} }
	if err != nil {
		st.Error = "node registry unreachable: " + err.Error()
		add("registry", "critical", "cluster", st.Error)
	} else {
		live := map[string]bool{}
		urls := map[string]string{}
		for _, n := range nodes {
			v := NodeView{ID: n.ID, Role: n.Role, Self: n.ID == h.p.Cluster.Self, Draining: n.Draining,
				Reachable: n.PrivateURL != "" || n.ID == h.p.Cluster.Self, Seen: n.Seen.UnixMilli()}
			var sum Summary
			if json.Unmarshal(n.Stats, &sum) == nil {
				v.Summary = &sum
			}
			live[n.ID], urls[n.ID] = true, n.PrivateURL
			c.last[n.ID] = v
		}
		c.urls = urls
		for id, v := range c.last {
			if live[id] {
				continue
			}
			if now.Sub(time.UnixMilli(v.Seen)) > downMemory {
				delete(c.last, id)
				continue
			}
			v.Down, v.Summary = true, nil
			c.last[id] = v
		}
	}
	pt := ClusterPoint{T: now.UnixMilli()}
	var cpuSum float64
	var cpuN int
	media := 0
	for _, v := range c.last {
		st.Nodes = append(st.Nodes, v)
		if v.Down {
			pt.Down++
			add("down:"+v.ID, "critical", "node", fmt.Sprintf("%s (%s) is gone: last heartbeat %s", v.ID, v.Role, time.UnixMilli(v.Seen).Format("15:04:05")))
			continue
		}
		switch v.Role {
		case config.RoleEdge:
			pt.Edges++
		default:
			pt.Media++
			media++
		}
		if v.Draining {
			add("draining:"+v.ID, "warning", "node", v.ID+" is draining")
		}
		if late := now.Sub(time.UnixMilli(v.Seen)); late > nodeLate {
			add("late:"+v.ID, "warning", "node", fmt.Sprintf("%s: last heartbeat %.0f s ago", v.ID, late.Seconds()))
		}
		s := v.Summary
		if s == nil {
			continue
		}
		if v.Role != config.RoleEdge { // an edge's presenters are relayed copies
			pt.Presenters += s.Presenters
		}
		pt.Viewers += s.Viewers
		pt.BpsIn += s.BpsIn
		pt.BpsOut += s.BpsOut
		pt.CPUMax = max(pt.CPUMax, s.CPU)
		cpuSum += s.CPU
		cpuN++
		pt.Recordings += s.Recordings
		pt.RecUsed += s.RecUsed
		pt.RecCap += s.RecCap
		pt.LossMax = max(pt.LossMax, s.LossMax)
		if s.Critical+s.Warnings > 0 {
			sev := "warning"
			if s.Critical > 0 {
				sev = "critical"
			}
			add("node:"+v.ID, sev, "node", fmt.Sprintf("%s: %d critical, %d warning alerts", v.ID, s.Critical, s.Warnings))
		}
	}
	if cpuN > 0 {
		pt.CPUAvg = cpuSum / float64(cpuN)
	}
	if err == nil && media == 0 {
		add("no-media", "critical", "cluster", "no media node is up: presenters cannot join")
	}
	sort.Slice(st.Nodes, func(i, j int) bool {
		if st.Nodes[i].Role != st.Nodes[j].Role {
			return st.Nodes[i].Role > st.Nodes[j].Role // media before edge
		}
		return st.Nodes[i].ID < st.Nodes[j].ID
	})
	st.Alerts = stamp(cur, c.alertAt, now)
	st.Point = &pt
	st.Capacity = capacityOf(st.Nodes)
	if len(c.hist) < clusterHistory {
		c.hist = append(c.hist, pt)
	} else {
		c.hist[c.next] = pt
		c.next = (c.next + 1) % clusterHistory
	}
	c.view = st
	return st
}

// state returns the latest sample, taking one when it is older than clusterEvery.
func (h *Handler) clusterState() *ClusterState {
	c := &h.cl
	c.mu.Lock()
	c.wanted = h.p.Now()
	st := c.view
	c.mu.Unlock()
	if st != nil && h.p.Now().Sub(time.UnixMilli(st.Time)) < clusterEvery {
		return st
	}
	return h.sampleCluster()
}

func (h *Handler) clusterPoints() []ClusterPoint {
	c := &h.cl
	c.mu.Lock()
	defer c.mu.Unlock()
	return append(append([]ClusterPoint(nil), c.hist[c.next:]...), c.hist[:c.next]...)
}

// token signs requests to other nodes (re-minted every 5 minutes, valid for 10).
func (h *Handler) token() (string, error) {
	c := &h.cl
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tok != "" && h.p.Now().Sub(c.tokAt) < 5*time.Minute {
		return c.tok, nil
	}
	t, err := auth.NewAccessToken(h.p.Cluster.APIKey, h.p.Cluster.Secret).SetIdentity("dashboard-" + h.p.Cluster.Self).
		SetGrant(&auth.RoomGrant{RoomAdmin: true}).SetValidFor(10 * time.Minute).ToJWT()
	if err != nil {
		return "", err
	}
	c.tok, c.tokAt = t, h.p.Now()
	return t, nil
}

// fetch GETs a node's /admin/api/node?query (or answers locally for this node).
func (h *Handler) fetch(ctx context.Context, id, query string) ([]byte, error) {
	if id == h.p.Cluster.Self {
		q, _ := url.ParseQuery(query)
		if route := q.Get("api"); route != "" {
			q.Del("api")
			return h.queryBytes(route, q)
		}
		switch {
		case q.Has("room"):
			return json.Marshal(h.localRoom(q.Get("room")))
		case q.Has("rooms"):
			return json.Marshal(h.localIndex())
		case q.Has("history"):
			h.mu.Lock()
			pts := h.hist.all()
			h.mu.Unlock()
			return json.Marshal(map[string]any{"interval_ms": h.p.Interval.Milliseconds(), "points": pts})
		}
		return h.liveBytes()
	}
	h.cl.mu.Lock()
	base := h.cl.urls[id]
	h.cl.mu.Unlock()
	if base == "" {
		return nil, fmt.Errorf("%s has no private_url: its dashboard cannot be reached", id)
	}
	tok, err := h.token()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+PathPrefix+"/api/node?"+query, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", id, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}

// cachedFetch serves kind requests from the cache while fresh, else runs get.
func (h *Handler) cachedFetch(kind, key string, get func() ([]byte, error)) ([]byte, error) {
	c := &h.cl
	now := h.p.Now()
	c.mu.Lock()
	if e, ok := c.cache[key]; ok && now.Sub(e.at) < cacheFor[kind] {
		c.mu.Unlock()
		return e.body, nil
	}
	c.mu.Unlock()
	b, err := get()
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	for k, e := range c.cache { // keys are the pages being viewed: few
		if now.Sub(e.at) > time.Minute {
			delete(c.cache, k)
		}
	}
	c.cache[key] = cached{at: now, body: b}
	c.mu.Unlock()
	return b, nil
}

// reachable lists the live nodes this dashboard can fetch from.
func (h *Handler) reachable() []NodeView {
	var out []NodeView
	for _, n := range h.clusterState().Nodes {
		if !n.Down && n.Reachable {
			out = append(out, n)
		}
	}
	return out
}

// fanOut runs fetch on every reachable node in parallel.
func (h *Handler) fanOut(ctx context.Context, query string, each func(n NodeView, b []byte, err error)) {
	var wg sync.WaitGroup
	var mu sync.Mutex
	slots := make(chan struct{}, fanOutMax)
	for _, n := range h.reachable() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			b, err := h.fetch(ctx, n.ID, query)
			mu.Lock()
			each(n, b, err)
			mu.Unlock()
		}()
	}
	wg.Wait()
}

// Capacity is the headroom of one role's nodes: load is the busier of CPU (share of all
// cores) and recording capacity used; N+1 holds when the others can take the busiest node's
// load and stay under capacityLimit.
type Capacity struct {
	Nodes   int     `json:"nodes"`
	AvgLoad float64 `json:"avg_load"`
	MaxLoad float64 `json:"max_load"`
	Busiest string  `json:"busiest,omitempty"`
	N1      bool    `json:"n1"`
	Note    string  `json:"note"`
}

const capacityLimit = 0.8

func capacityOf(nodes []NodeView) map[string]Capacity {
	out := map[string]Capacity{}
	sum := map[string]float64{}
	for _, n := range nodes {
		if n.Down || n.Draining || n.Summary == nil {
			continue
		}
		load := n.Summary.CPU / 100
		if n.Summary.RecCap > 0 {
			load = max(load, float64(n.Summary.RecUsed)/float64(n.Summary.RecCap))
		}
		c := out[n.Role]
		c.Nodes++
		sum[n.Role] += load
		if load >= c.MaxLoad {
			c.MaxLoad, c.Busiest = load, n.ID
		}
		out[n.Role] = c
	}
	for role, c := range out {
		c.AvgLoad = sum[role] / float64(c.Nodes)
		switch {
		case c.Nodes < 2:
			c.Note = "a single node: losing it stops this role"
		default:
			after := sum[role] / float64(c.Nodes-1) // the busiest node's load spread over the others
			c.N1 = after <= capacityLimit
			c.Note = fmt.Sprintf("without one node the others run at %.0f%% (limit %.0f%%)", 100*after, 100*capacityLimit)
		}
		out[role] = c
	}
	return out
}

// userSessions returns the saved sessions of ?identity= (cluster session store), newest first.
func (h *Handler) userSessions(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("identity"))
	st := h.p.Cluster.Sessions
	switch {
	case id == "":
		jsonErr(w, http.StatusBadRequest, "identity required")
		return
	case st == nil:
		jsonErr(w, http.StatusNotImplemented, "no cluster session store")
		return
	}
	raw, err := st.UserSessions(r.Context(), id)
	if err != nil {
		jsonErr(w, http.StatusBadGateway, err.Error())
		return
	}
	items := []json.RawMessage{}
	for _, b := range raw {
		items = append(items, b)
	}
	b, _ := json.Marshal(map[string]any{"identity": id, "items": items})
	writeBody(w, r, b)
}

// fanOutMax nodes are asked at the same time.
const fanOutMax = 16

// fanOutPage runs a paged query route (query.go) on every reachable node and merges the
// pages: each node returns its first offset+limit items in the requested order, so the
// merged, re-sorted slice is exact. Reusable for any route whose items are T.
func fanOutPage[T any](ctx context.Context, h *Handler, route string, v url.Values, def, most int,
	cmpFn func(a, b *T) int, setNode func(*T, string)) map[string]any {
	p := pageOf(v)
	if v.Get("limit") == "" {
		p.limit = def
	}
	p.limit = min(p.limit, most)
	q := url.Values{}
	for k, vals := range v {
		q[k] = vals
	}
	q.Set("api", route)
	q.Set("offset", "0")
	q.Set("limit", fmt.Sprint(min(p.offset+p.limit, pageMax)))
	var items []T
	var errs []string
	total := 0
	h.fanOut(ctx, q.Encode(), func(n NodeView, b []byte, err error) {
		var pg struct {
			Total int `json:"total"`
			Items []T `json:"items"`
		}
		if err == nil {
			err = json.Unmarshal(b, &pg)
		}
		if err != nil {
			errs = append(errs, n.ID+": "+err.Error())
			return
		}
		total += pg.Total
		for i := range pg.Items {
			setNode(&pg.Items[i], n.ID)
		}
		items = append(items, pg.Items...)
	})
	_, out := page(p, items, func(*T) bool { return true }, cmpFn)
	sort.Strings(errs)
	return map[string]any{"total": total, "offset": p.offset, "items": out, "errors": errs}
}

// NodeParticipant is a participant row of a cluster-wide search.
type NodeParticipant struct {
	ParticipantBrief
	Node string `json:"node"`
}

// ClusterRoom is one room in the cluster room index.
type ClusterRoom struct {
	Name       string   `json:"name"`
	Presenters int      `json:"presenters"`
	Viewers    int      `json:"viewers"`
	Recording  bool     `json:"recording,omitempty"`
	Media      []string `json:"media"` // nodes with its presenters
	Edges      []string `json:"edges"` // edges relaying it to their viewers
}

// clusterAPI serves the cluster routes of the browser API (logged-in session).
func (h *Handler) clusterAPI(w http.ResponseWriter, r *http.Request, route string) {
	if h.p.Cluster == nil {
		jsonErr(w, http.StatusNotFound, "not a cluster node")
		return
	}
	q := r.URL.Query()
	switch route {
	case "cluster":
		writeJSON(w, 200, h.clusterState())
	case "cluster/history":
		writeJSON(w, 200, map[string]any{"interval_ms": clusterEvery.Milliseconds(), "points": h.clusterPoints()})
	case "cluster/node":
		id, query := q.Get("id"), ""
		if q.Has("history") {
			query = "history"
		}
		b, err := h.cachedFetch("node", "node:"+id+":"+query, func() ([]byte, error) { return h.fetch(r.Context(), id, query) })
		if err != nil {
			jsonErr(w, http.StatusBadGateway, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(b)
	case "cluster/rooms":
		h.clusterRoomsPage(w, r)
	case "cluster/user": // past sessions of one identity, from the cluster's session store
		h.userSessions(w, r)
	case "cluster/participants":
		v := r.URL.Query()
		if v.Get("sort") == "" {
			v.Set("sort", "quality") // worst first
		}
		cmpP := partCmp(v.Get("sort"))
		res := fanOutPage(r.Context(), h, "participants", v, 50, 200,
			func(a, b *NodeParticipant) int { return cmpP(&a.ParticipantBrief, &b.ParticipantBrief) },
			func(x *NodeParticipant, node string) { x.Node = node })
		b, _ := json.Marshal(res)
		writeBody(w, r, b)
	case "cluster/room":
		name := q.Get("name")
		h.writeCached(w, "room", "room:"+name, func() (any, error) { return h.clusterRoom(r.Context(), name), nil })
	default:
		jsonErr(w, http.StatusNotFound, "unknown endpoint")
	}
}

func (h *Handler) writeCached(w http.ResponseWriter, kind, key string, get func() (any, error)) {
	b, err := h.cachedFetch(kind, key, func() ([]byte, error) {
		v, err := get()
		if err != nil {
			return nil, err
		}
		return json.Marshal(v)
	})
	if err != nil {
		jsonErr(w, http.StatusBadGateway, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

// clusterRoomsPage searches, sorts and pages the cluster's rooms. Each node returns only its
// first offset+limit rooms for the query (searched and sorted there); the rooms are merged by
// name across nodes. Exact for the name order; for presenter/viewer order the counts of a room
// that one node did not return in its slice are missing (close enough to find big rooms). The
// cost is O(nodes x page), not O(rooms), and the answer is cached a few seconds per query.
func (h *Handler) clusterRoomsPage(w http.ResponseWriter, r *http.Request) {
	v := r.URL.Query()
	p := pageOf(v)
	key := "rooms?" + v.Encode()
	b, err := h.cachedFetch("rooms", key, func() ([]byte, error) {
		q := url.Values{"api": {"rooms"}, "q": {v.Get("q")}, "offset": {"0"}, "limit": {fmt.Sprint(min(p.offset+p.limit, pageMax))}}
		if p.sort != "" {
			q.Set("sort", p.sort)
		}
		if p.desc {
			q.Set("desc", "1")
		}
		byName := map[string]*ClusterRoom{}
		var errs []string
		total := 0
		h.fanOut(r.Context(), q.Encode(), func(n NodeView, b []byte, err error) {
			var pg struct {
				Total int       `json:"total"`
				Items []RoomRow `json:"items"`
			}
			if err == nil {
				err = json.Unmarshal(b, &pg)
			}
			if err != nil {
				errs = append(errs, n.ID+": "+err.Error())
				return
			}
			total = max(total, pg.Total) // rooms span nodes: the largest node count is a lower bound
			for _, rr := range pg.Items {
				cr := byName[rr.Name]
				if cr == nil {
					cr = &ClusterRoom{Name: rr.Name, Media: []string{}, Edges: []string{}}
					byName[rr.Name] = cr
				}
				cr.Viewers += rr.Viewers
				cr.Recording = cr.Recording || rr.Recording
				if n.Role == config.RoleEdge {
					cr.Edges = append(cr.Edges, n.ID)
				} else {
					cr.Presenters += rr.Presenters
					cr.Media = append(cr.Media, n.ID)
				}
			}
		})
		all := make([]ClusterRoom, 0, len(byName))
		for _, cr := range byName {
			sort.Strings(cr.Media)
			sort.Strings(cr.Edges)
			all = append(all, *cr)
		}
		var cmpFn func(a, b *ClusterRoom) int
		switch p.sort {
		case "presenters":
			cmpFn = func(a, b *ClusterRoom) int {
				return cmp.Or(cmp.Compare(a.Presenters, b.Presenters), strings.Compare(a.Name, b.Name))
			}
		case "viewers":
			cmpFn = func(a, b *ClusterRoom) int {
				return cmp.Or(cmp.Compare(a.Viewers, b.Viewers), strings.Compare(a.Name, b.Name))
			}
		default:
			cmpFn = func(a, b *ClusterRoom) int { return strings.Compare(a.Name, b.Name) }
		}
		_, items := page(p, all, func(*ClusterRoom) bool { return true }, cmpFn)
		sort.Strings(errs)
		return json.Marshal(map[string]any{"total": max(total, len(all)), "offset": p.offset, "items": items, "errors": errs, "truncated": false})
	})
	if err != nil {
		jsonErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeBody(w, r, b)
}

// clusterRoom returns every node's part of a room (nodes without it are left out).
func (h *Handler) clusterRoom(ctx context.Context, name string) map[string]any {
	parts := []NodeRoom{}
	h.fanOut(ctx, "room="+url.QueryEscape(name), func(n NodeView, b []byte, err error) {
		nr := NodeRoom{Node: n.ID, Role: n.Role}
		if err == nil {
			err = json.Unmarshal(b, &nr)
		}
		if err != nil {
			nr.Error = err.Error()
		}
		if nr.Room != nil || nr.Error != "" {
			parts = append(parts, nr)
		}
	})
	sort.Slice(parts, func(i, j int) bool { return parts[i].Node < parts[j].Node })
	return map[string]any{"name": name, "nodes": parts}
}
