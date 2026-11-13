package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KeshavGoyal26/tessera/pkg/auth"
	"github.com/KeshavGoyal26/tessera/pkg/cluster"
	"github.com/KeshavGoyal26/tessera/pkg/config"
	"github.com/KeshavGoyal26/tessera/pkg/stats"
)

// registry is a fake cluster registry: the nodes' entries, with their live summaries.
type registry struct {
	mu    sync.Mutex
	nodes []cluster.NodeInfo
	sum   map[string]func() []byte
}

func (r *registry) Nodes(context.Context) ([]cluster.NodeInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := append([]cluster.NodeInfo(nil), r.nodes...)
	for i := range out {
		out[i].Seen = time.Now()
		if f := r.sum[out[i].ID]; f != nil {
			out[i].Stats = f()
		}
	}
	return out, nil
}

func (r *registry) remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, n := range r.nodes {
		if n.ID == id {
			r.nodes = append(r.nodes[:i], r.nodes[i+1:]...)
			return
		}
	}
}

func participant(id string, published bool, kind string) stats.ParticipantStats {
	p := stats.ParticipantStats{SID: "PA_" + id, Identity: id, Kind: kind, ICECandidateType: "host"}
	if published {
		p.Published = []stats.PublishedTrackStats{{SID: "TR_" + id, Kind: "video", Active: true}}
	}
	return p
}

type clusterEnv struct {
	reg   *registry
	edge  *env
	hits  map[string]*atomic.Int64 // node API requests per node
	nodes map[string]*Handler
}

// newClusterEnv starts media nodes m1 (room g: presenter alice and e1's relay) and m2
// (room h: presenter bob), and edge e1 (room g: alice relayed, viewers v1 and v2),
// each with its own dashboard; the test talks to e1's.
func newClusterEnv(t *testing.T) *clusterEnv {
	t.Helper()
	keys := auth.StaticKeys{"key": "s3cret"}
	ce := &clusterEnv{reg: &registry{sum: map[string]func() []byte{}}, hits: map[string]*atomic.Int64{}, nodes: map[string]*Handler{}}
	snaps := map[string]*stats.Snapshot{
		"m1": {Node: stats.NodeStats{NumCPU: 4, CPUPercent: 100}, Rooms: []stats.RoomStats{{Name: "g", Participants: []stats.ParticipantStats{
			participant("alice", true, "standard"), participant("relay-e1", false, "relay")}}}},
		"m2": {Node: stats.NodeStats{NumCPU: 4}, Rooms: []stats.RoomStats{{Name: "h", Participants: []stats.ParticipantStats{
			participant("bob", true, "standard")}}}},
		"e1": {Node: stats.NodeStats{NumCPU: 4}, Rooms: []stats.RoomStats{{Name: "g", Participants: []stats.ParticipantStats{
			participant("alice", true, "standard"), participant("v1", false, "standard"), participant("v2", false, "standard")}}}},
	}
	for _, id := range []string{"m1", "m2", "e1"} {
		role := config.RoleMedia
		if id == "e1" {
			role = config.RoleEdge
		}
		s := snaps[id]
		h, err := New(Params{
			Stats: stats.ProviderFunc(func() *stats.Snapshot { c := *s; c.Time = time.Now(); return &c }), Keys: keys, Interval: 20 * time.Millisecond,
			Cluster: &ClusterParams{Nodes: ce.reg, Self: id, Role: role, APIKey: "key", Secret: "s3cret", Verifier: auth.NewVerifier(keys)},
		})
		if err != nil {
			t.Fatal(err)
		}
		hits := &atomic.Int64{}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/api/node") {
				hits.Add(1)
			}
			h.ServeHTTP(w, r)
		}))
		t.Cleanup(func() { srv.Close(); h.Close() })
		ce.hits[id], ce.nodes[id] = hits, h
		ce.reg.nodes = append(ce.reg.nodes, cluster.NodeInfo{ID: id, Role: role, PrivateURL: srv.URL})
		ce.reg.sum[id] = h.Summary
		if id == "e1" {
			ce.edge = &env{h: h, srv: httptest.NewServer(h)}
			t.Cleanup(ce.edge.srv.Close)
		}
	}
	return ce
}

func getJSON(t *testing.T, e *env, c *http.Cookie, path string, v any) int {
	t.Helper()
	resp, body := e.do(t, "GET", path, "", c, nil)
	if resp.StatusCode == 200 {
		if err := json.Unmarshal([]byte(body), v); err != nil {
			t.Fatalf("%s: %v: %s", path, err, body)
		}
	}
	return resp.StatusCode
}

func TestNodeAPIRequiresClusterToken(t *testing.T) {
	ce := newClusterEnv(t)
	c, _ := ce.edge.login(t)
	if resp, _ := ce.edge.do(t, "GET", "/admin/api/node", "", c, nil); resp.StatusCode != 401 {
		t.Fatalf("a browser session must not open the node API: %d", resp.StatusCode)
	}
	weak, _ := auth.NewAccessToken("key", "s3cret").SetGrant(&auth.RoomGrant{RoomJoin: true, Room: "g"}).ToJWT()
	if resp, _ := ce.edge.do(t, "GET", "/admin/api/node", "", nil, map[string]string{"Authorization": "Bearer " + weak}); resp.StatusCode != 401 {
		t.Fatalf("a join token must not open the node API: %d", resp.StatusCode)
	}
	tok, _ := ce.edge.h.token()
	resp, body := ce.edge.do(t, "GET", "/admin/api/node?room=g", "", nil, map[string]string{"Authorization": "Bearer " + tok})
	var nr NodeRoom
	if resp.StatusCode != 200 || json.Unmarshal([]byte(body), &nr) != nil || nr.Node != "e1" || nr.Room == nil || len(nr.Room.Participants) != 3 {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
}

func TestClusterView(t *testing.T) {
	ce := newClusterEnv(t)
	c, _ := ce.edge.login(t)

	var info struct{ Cluster map[string]string }
	if getJSON(t, ce.edge, c, "/admin/api/session", &info); info.Cluster["self"] != "e1" || info.Cluster["role"] != "edge" {
		t.Fatalf("session cluster info %v", info.Cluster)
	}

	// Nodes with their heartbeat summaries; presenters count on media nodes only (an
	// edge's are relayed copies), viewers everywhere.
	var st ClusterState
	if code := getJSON(t, ce.edge, c, "/admin/api/cluster", &st); code != 200 || len(st.Nodes) != 3 || st.Nodes[0].ID != "m1" || st.Nodes[2].ID != "e1" || !st.Nodes[2].Self {
		t.Fatalf("%d %+v", code, st)
	}
	if p := st.Point; p == nil || p.Media != 2 || p.Edges != 1 || p.Presenters != 2 || p.Viewers != 2 || p.CPUMax != 25 {
		t.Fatalf("point %+v", st.Point)
	}
	if s := st.Nodes[0].Summary; s == nil || s.Presenters != 1 || s.Relays != 1 {
		t.Fatalf("m1 summary %+v", s)
	}

	// The room index merges the nodes' indexes.
	var idx struct{ Rooms []ClusterRoom }
	getJSON(t, ce.edge, c, "/admin/api/cluster/rooms", &idx)
	if len(idx.Rooms) != 2 || idx.Rooms[0].Name != "g" || idx.Rooms[0].Presenters != 1 || idx.Rooms[0].Viewers != 2 ||
		strings.Join(idx.Rooms[0].Media, ",") != "m1" || strings.Join(idx.Rooms[0].Edges, ",") != "e1" || idx.Rooms[1].Presenters != 1 {
		t.Fatalf("rooms %+v", idx.Rooms)
	}
	// Within the cache window a second dashboard costs no request to the nodes.
	before := ce.hits["m1"].Load()
	getJSON(t, ce.edge, c, "/admin/api/cluster/rooms", &idx)
	if ce.hits["m1"].Load() != before {
		t.Fatal("the room index was fetched again within its cache window")
	}

	// One room: only the nodes that have it.
	var room struct{ Nodes []NodeRoom }
	getJSON(t, ce.edge, c, "/admin/api/cluster/room?name=g", &room)
	if len(room.Nodes) != 2 || room.Nodes[0].Node != "e1" || room.Nodes[1].Node != "m1" || room.Nodes[1].Room == nil {
		t.Fatalf("room %+v", room.Nodes)
	}

	// One node's live frame, and its rooms paged on that node, through this dashboard.
	var u Live
	if code := getJSON(t, ce.edge, c, "/admin/api/cluster/node?id=m2", &u); code != 200 || u.Version == 0 || u.Counts.Rooms != 1 {
		t.Fatalf("%d %+v", code, u)
	}
	var rooms struct {
		Total int
		Items []RoomRow
	}
	if code := getJSON(t, ce.edge, c, "/admin/api/rooms?node=m2", &rooms); code != 200 || rooms.Total != 1 || rooms.Items[0].Name != "h" {
		t.Fatalf("rooms on m2: %d %+v", code, rooms)
	}
	if code := getJSON(t, ce.edge, c, "/admin/api/cluster/node?id=nope", &u); code != http.StatusBadGateway {
		t.Fatalf("unknown node: %d", code)
	}

	// A node that stops heartbeating is listed as down, with an alert.
	ce.reg.remove("m2")
	st = *ce.edge.h.sampleCluster()
	var down bool
	for _, n := range st.Nodes {
		down = down || (n.ID == "m2" && n.Down)
	}
	if !down || len(st.Alerts) == 0 || st.Alerts[0].ID != "down:m2" || st.Alerts[0].Severity != "critical" || st.Point.Down != 1 {
		t.Fatalf("down node: %+v", st)
	}
	ce.reg.remove("m1")
	if st := ce.edge.h.sampleCluster(); !hasAlert(st.Alerts, "no-media") {
		t.Fatalf("no media node: %+v", st.Alerts)
	}
}

func TestSingleNodeHasNoClusterAPI(t *testing.T) {
	e := newEnv(t)
	c, _ := e.login(t)
	if resp, _ := e.do(t, "GET", "/admin/api/cluster", "", c, nil); resp.StatusCode != 404 {
		t.Fatalf("cluster API on a single node: %d", resp.StatusCode)
	}
	if resp, _ := e.do(t, "GET", "/admin/api/node", "", nil, nil); resp.StatusCode != 404 {
		t.Fatalf("node API on a single node: %d", resp.StatusCode)
	}
	if e.h.Summary() != nil {
		t.Fatal("a single node computes no summary")
	}
}

func hasAlert(as []Alert, id string) bool {
	for _, a := range as {
		if a.ID == id {
			return true
		}
	}
	return false
}
