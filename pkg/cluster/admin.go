package cluster

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/proto"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/admin"
	"github.com/KeshavGoyal26/tessera/pkg/auth"
	"github.com/KeshavGoyal26/tessera/pkg/config"
	"github.com/KeshavGoyal26/tessera/pkg/errs"
	"github.com/KeshavGoyal26/tessera/pkg/recorder"
	"github.com/KeshavGoyal26/tessera/pkg/room"
)

// recordingTTL is how long an edge remembers which media node runs a recording.
const recordingTTL = 7 * 24 * time.Hour

// AdminForwarder serves the admin API on an edge (room.Admin and recorder.Service):
// presenter calls go to the presenter's media node, room calls to every media node
// hosting the room plus the edge's own room (its viewers), lists are merged, and
// recordings are found through rec:<id> (ADR-0012). Calls to media nodes use the
// internal API key over the private network.
type AdminForwarder struct {
	C      *Client
	Local  room.Admin // this edge's rooms (viewers and relayed copies of presenters)
	APIKey string
	Secret string
	// Self is this edge's node id: viewers on the other edges are reached through them.
	Self string
}

var (
	_ room.Admin       = (*AdminForwarder)(nil)
	_ recorder.Service = (*AdminForwarder)(nil)
)

func recKey(id string) string { return keyPrefix + "rec:" + id }

func (f *AdminForwarder) client(n NodeInfo) *admin.Client {
	c := admin.NewClient(n.PrivateURL, f.APIKey, f.Secret,
		auth.RoomGrant{RoomCreate: true, RoomList: true, RoomAdmin: true, RoomRecord: true})
	c.Forwarded = true
	return c
}

// otherEdges lists the edges a viewer call can go to: none for a call that another node
// forwarded (it answers from its own rooms, so calls never bounce between edges).
func (f *AdminForwarder) otherEdges(ctx context.Context) []NodeInfo {
	if admin.Forwarded(ctx) {
		return nil
	}
	nodes, err := f.C.Nodes(ctx)
	if err != nil {
		return nil
	}
	var out []NodeInfo
	for _, n := range nodes {
		if n.Role == config.RoleEdge && n.ID != f.Self && n.PrivateURL != "" {
			out = append(out, n)
		}
	}
	return out
}

func (f *AdminForwarder) media(ctx context.Context) ([]NodeInfo, error) {
	nodes, err := f.C.Nodes(ctx)
	if err != nil {
		return nil, err
	}
	out := nodes[:0]
	for _, n := range nodes {
		if n.Role == config.RoleMedia {
			out = append(out, n)
		}
	}
	return out, nil
}

// hosting returns the live media nodes hosting room.
func (f *AdminForwarder) hosting(ctx context.Context, room string) ([]NodeInfo, error) {
	ids, err := f.C.RoomNodes(ctx, room)
	if err != nil {
		return nil, err
	}
	media, err := f.media(ctx)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var out []NodeInfo
	for _, n := range media {
		if want[n.ID] {
			out = append(out, n)
		}
	}
	return out, nil
}

// presenterNode returns the media node of a presenter (ok false when it has none).
func (f *AdminForwarder) presenterNode(ctx context.Context, room, identity string) (NodeInfo, bool, error) {
	id, err := f.C.rdb.Get(ctx, presKey(room, identity)).Result()
	if errors.Is(err, redis.Nil) {
		return NodeInfo{}, false, nil
	}
	if err != nil {
		return NodeInfo{}, false, err
	}
	media, err := f.media(ctx)
	if err != nil {
		return NodeInfo{}, false, err
	}
	for _, n := range media {
		if n.ID == id {
			return n, true, nil
		}
	}
	return NodeInfo{}, false, nil
}

// CreateRoom creates the room on the least-loaded media node (presenters join it there).
func (f *AdminForwarder) CreateRoom(ctx context.Context, req *tesserav1.CreateRoomRequest) (*tesserav1.Room, error) {
	media, err := f.media(ctx)
	if err != nil {
		return nil, err
	}
	var best *NodeInfo
	for i := range media {
		if !media[i].Draining && (best == nil || media[i].Sessions < best.Sessions) {
			best = &media[i]
		}
	}
	if best == nil {
		return nil, ErrNoMediaNode
	}
	return f.client(*best).CreateRoom(ctx, req)
}

// ListRooms merges the rooms of every media node (participant counts add up).
func (f *AdminForwarder) ListRooms(ctx context.Context, names []string) ([]*tesserav1.Room, error) {
	media, err := f.media(ctx)
	if err != nil {
		return nil, err
	}
	byName := map[string]*tesserav1.Room{}
	var order []string
	for _, n := range media {
		rooms, err := f.client(n).ListRooms(ctx, names...)
		if err != nil {
			continue // a node that is down or unreachable: list the others
		}
		for _, r := range rooms {
			if cur, ok := byName[r.GetName()]; ok {
				cur.NumParticipants += r.GetNumParticipants()
				cur.NumPublishers += r.GetNumPublishers()
				continue
			}
			byName[r.GetName()] = proto.Clone(r).(*tesserav1.Room)
			order = append(order, r.GetName())
		}
	}
	out := make([]*tesserav1.Room, 0, len(order))
	for _, n := range order {
		out = append(out, byName[n])
	}
	return out, nil
}

// DeleteRoom deletes the room on every node hosting it and on this edge.
func (f *AdminForwarder) DeleteRoom(ctx context.Context, room string) error {
	nodes, err := f.hosting(ctx, room)
	if err != nil {
		return err
	}
	found := false
	for _, n := range nodes {
		if err := f.client(n).DeleteRoom(ctx, room); err != nil && errs.CodeOf(err) != errs.CodeNotFound {
			return err
		}
		found = true
	}
	if err := f.Local.DeleteRoom(ctx, room); err == nil {
		found = true
	}
	if !found {
		return errs.New(errs.CodeNotFound, "room not found")
	}
	return nil
}

// UpdateRoomMetadata updates the room everywhere it lives.
func (f *AdminForwarder) UpdateRoomMetadata(ctx context.Context, room, metadata string) (*tesserav1.Room, error) {
	nodes, err := f.hosting(ctx, room)
	if err != nil {
		return nil, err
	}
	var out *tesserav1.Room
	for _, n := range nodes {
		r, err := f.client(n).UpdateRoomMetadata(ctx, room, metadata)
		if err != nil {
			return nil, err
		}
		out = r
	}
	if r, err := f.Local.UpdateRoomMetadata(ctx, room, metadata); err == nil && out == nil {
		out = r
	}
	if out == nil {
		return nil, errs.New(errs.CodeNotFound, "room not found")
	}
	return out, nil
}

// ListParticipants lists the presenters on the media nodes and the viewers on this
// edge (its relayed copies of presenters are left out).
func (f *AdminForwarder) ListParticipants(ctx context.Context, room string) ([]*tesserav1.ParticipantInfo, error) {
	nodes, err := f.hosting(ctx, room)
	if err != nil {
		return nil, err
	}
	var out []*tesserav1.ParticipantInfo
	seen := map[string]bool{}
	for _, n := range nodes {
		ps, err := f.client(n).ListParticipants(ctx, room)
		if err != nil {
			continue // a node that is down or unreachable: list the others
		}
		for _, p := range ps {
			if p.GetKind() == tesserav1.ParticipantInfo_KIND_RELAY {
				continue
			}
			seen[p.GetIdentity()] = true
			out = append(out, p)
		}
	}
	local, err := f.Local.ListParticipants(ctx, room)
	if err == nil {
		for _, p := range local {
			if !seen[p.GetIdentity()] {
				seen[p.GetIdentity()] = true
				out = append(out, p)
			}
		}
	}
	for _, e := range f.otherEdges(ctx) { // their viewers (their presenters are relayed copies)
		ps, perr := f.client(e).ListParticipants(ctx, room)
		if perr != nil {
			continue
		}
		for _, p := range ps {
			if !seen[p.GetIdentity()] && p.GetKind() != tesserav1.ParticipantInfo_KIND_RELAY && len(p.GetTracks()) == 0 {
				seen[p.GetIdentity()] = true
				out = append(out, p)
			}
		}
	}
	if len(nodes) == 0 && err != nil {
		return nil, err
	}
	return out, nil
}

// onParticipant runs call on the presenter's media node, or on this edge (a viewer).
func (f *AdminForwarder) onParticipant(ctx context.Context, room, identity string, remote func(*admin.Client) error, local func() error) error {
	n, ok, err := f.presenterNode(ctx, room, identity)
	if err != nil {
		return err
	}
	if ok {
		return remote(f.client(n))
	}
	err = local()
	if !errors.Is(err, errs.ErrNotFound) {
		return err
	}
	for _, e := range f.otherEdges(ctx) { // a viewer on another edge
		if err = remote(f.client(e)); !errors.Is(err, errs.ErrNotFound) {
			return err
		}
	}
	return err
}

// GetParticipant implements room.Admin.
func (f *AdminForwarder) GetParticipant(ctx context.Context, room, identity string) (out *tesserav1.ParticipantInfo, err error) {
	err = f.onParticipant(ctx, room, identity,
		func(c *admin.Client) (e error) { out, e = c.GetParticipant(ctx, room, identity); return },
		func() (e error) { out, e = f.Local.GetParticipant(ctx, room, identity); return })
	return out, err
}

// RemoveParticipant implements room.Admin.
func (f *AdminForwarder) RemoveParticipant(ctx context.Context, room, identity string) error {
	return f.onParticipant(ctx, room, identity,
		func(c *admin.Client) error { return c.RemoveParticipant(ctx, room, identity) },
		func() error { return f.Local.RemoveParticipant(ctx, room, identity) })
}

// MutePublishedTrack implements room.Admin (the track SID is the media node's).
func (f *AdminForwarder) MutePublishedTrack(ctx context.Context, room, identity, trackSID string, muted bool) (out *tesserav1.TrackInfo, err error) {
	err = f.onParticipant(ctx, room, identity,
		func(c *admin.Client) (e error) {
			out, e = c.MutePublishedTrack(ctx, room, identity, trackSID, muted)
			return
		},
		func() (e error) { out, e = f.Local.MutePublishedTrack(ctx, room, identity, trackSID, muted); return })
	return out, err
}

// UpdateParticipant implements room.Admin.
func (f *AdminForwarder) UpdateParticipant(ctx context.Context, req *tesserav1.UpdateParticipantRequest) (out *tesserav1.ParticipantInfo, err error) {
	err = f.onParticipant(ctx, req.GetRoom(), req.GetIdentity(),
		func(c *admin.Client) (e error) { out, e = c.UpdateParticipant(ctx, req); return },
		func() (e error) { out, e = f.Local.UpdateParticipant(ctx, req); return })
	return out, err
}

// StartRecording records on the media node of the participant (or of the track).
func (f *AdminForwarder) StartRecording(ctx context.Context, req *tesserav1.StartRecordingRequest) (*tesserav1.RecordingInfo, error) {
	var node *NodeInfo
	if id := req.GetParticipant().GetIdentity(); id != "" {
		n, ok, err := f.presenterNode(ctx, req.GetRoom(), id)
		if err != nil {
			return nil, err
		}
		if ok {
			node = &n
		}
	} else if sid := req.GetTrack().GetTrackSid(); sid != "" {
		nodes, err := f.hosting(ctx, req.GetRoom())
		if err != nil {
			return nil, err
		}
	find:
		for i := range nodes {
			ps, err := f.client(nodes[i]).ListParticipants(ctx, req.GetRoom())
			if err != nil {
				continue
			}
			for _, p := range ps {
				for _, t := range p.GetTracks() {
					if t.GetSid() == sid {
						node = &nodes[i]
						break find
					}
				}
			}
		}
	}
	if node == nil {
		return nil, errs.New(errs.CodeNotFound, "no media server has that participant or track")
	}
	info, err := f.client(*node).StartRecording(ctx, req)
	if err != nil {
		return nil, err
	}
	_ = f.C.rdb.Set(ctx, recKey(info.GetRecordingId()), node.ID, recordingTTL).Err()
	return info, nil
}

// recordingCall runs call on the node that runs recording id (remembered, else tried in turn).
func (f *AdminForwarder) recordingCall(ctx context.Context, id string, call func(*admin.Client) (*tesserav1.RecordingInfo, error)) (*tesserav1.RecordingInfo, error) {
	media, err := f.media(ctx)
	if err != nil {
		return nil, err
	}
	if want, err := f.C.rdb.Get(ctx, recKey(id)).Result(); err == nil {
		for _, n := range media {
			if n.ID == want {
				return call(f.client(n))
			}
		}
	}
	for _, n := range media {
		if info, err := call(f.client(n)); err == nil {
			return info, nil
		}
	}
	return nil, errs.New(errs.CodeNotFound, "recording not found")
}

// StopRecording implements recorder.Service.
func (f *AdminForwarder) StopRecording(ctx context.Context, id string) (*tesserav1.RecordingInfo, error) {
	return f.recordingCall(ctx, id, func(c *admin.Client) (*tesserav1.RecordingInfo, error) { return c.StopRecording(ctx, id) })
}

// GetRecording implements recorder.Service.
func (f *AdminForwarder) GetRecording(ctx context.Context, id string) (*tesserav1.RecordingInfo, error) {
	return f.recordingCall(ctx, id, func(c *admin.Client) (*tesserav1.RecordingInfo, error) { return c.GetRecording(ctx, id) })
}

// ListRecordings merges the recordings of every media node.
func (f *AdminForwarder) ListRecordings(ctx context.Context, req *tesserav1.ListRecordingsRequest) ([]*tesserav1.RecordingInfo, error) {
	media, err := f.media(ctx)
	if err != nil {
		return nil, err
	}
	var out []*tesserav1.RecordingInfo
	for _, n := range media {
		list, err := f.client(n).ListRecordings(ctx, req)
		if err != nil {
			continue // a node that is down or unreachable: list the others
		}
		out = append(out, list...)
	}
	return out, nil
}
