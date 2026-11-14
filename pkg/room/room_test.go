package room

import (
	"context"
	"errors"
	"testing"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/config"
	"github.com/KeshavGoyal26/tessera/pkg/errs"
)

func TestAdminRoomLifecycle(t *testing.T) {
	m, err := NewManager(Params{Config: config.Default()})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	ctx := context.Background()
	r, err := m.CreateRoom(ctx, &tesserav1.CreateRoomRequest{Name: "a", Metadata: "x"})
	if err != nil || r.Name != "a" || r.Metadata != "x" || r.Sid == "" {
		t.Fatalf("%v %+v", err, r)
	}
	if r2, _ := m.CreateRoom(ctx, &tesserav1.CreateRoomRequest{Name: "a"}); r2.Sid != r.Sid {
		t.Fatal("create must be idempotent")
	}
	rooms, _ := m.ListRooms(ctx, nil)
	if len(rooms) != 1 {
		t.Fatal(rooms)
	}
	if _, err := m.CreateRoom(ctx, &tesserav1.CreateRoomRequest{}); !errors.Is(err, errs.ErrInvalidArgument) {
		t.Fatal(err)
	}
	if _, err := m.GetParticipant(ctx, "a", "nobody"); !errors.Is(err, errs.ErrNotFound) {
		t.Fatal(err)
	}
	if err := m.DeleteRoom(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteRoom(ctx, "a"); !errors.Is(err, errs.ErrNotFound) {
		t.Fatal(err)
	}
	if s := m.Snapshot(); len(s.Rooms) != 0 || s.Node.Goroutines == 0 {
		t.Fatalf("%+v", s)
	}
}

// TestRemotePublisherQuality: a presenter relayed from a media node shows the connection
// quality that node measured, not the one this node would compute for the relay.
func TestRemotePublisherQuality(t *testing.T) {
	m, err := NewManager(Params{Config: config.Default()})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	rp, err := m.(RemotePublishers).AddRemotePublisher("g", "alice", "Alice", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	quality := func() string {
		for _, r := range m.Snapshot().Rooms {
			for _, p := range r.Participants {
				if p.Identity == "alice" {
					return p.ConnectionQuality
				}
			}
		}
		return ""
	}
	if q := quality(); q != "unknown" {
		t.Fatalf("before any update: %q", q)
	}
	rp.SetQuality(tesserav1.ConnectionQuality_CONNECTION_QUALITY_POOR, 2)
	if q := quality(); q != "poor" {
		t.Fatalf("after the media node's update: %q", q)
	}
}

// TestMergeAttributes: updates merge into the participant's attributes and an empty value
// deletes a key (admin UpdateParticipant and the client's own update).
func TestMergeAttributes(t *testing.T) {
	cur := map[string]string{"role": "presenter", "hls_url": "/x", "k": "v"}
	got := mergeAttributes(cur, map[string]string{"k": "", "new": "1"})
	if len(got) != 3 || got["role"] != "presenter" || got["hls_url"] != "/x" || got["new"] != "1" || got["k"] != "" {
		t.Fatalf("%v", got)
	}
	if _, ok := got["k"]; ok {
		t.Fatal("an empty value must delete the key")
	}
	if cur["k"] != "v" {
		t.Fatal("the current map must not be modified (it is shared with snapshots)")
	}
	if mergeAttributes(nil, nil) != nil {
		t.Fatal("no update keeps nil")
	}
}

// TestCreateRoomRecordingEnabled: CreateRoom keeps recording_enabled, so the room records
// every participant who joins it.
func TestCreateRoomRecordingEnabled(t *testing.T) {
	m, err := NewManager(Params{Config: config.Default()})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	ctx := context.Background()
	r, err := m.CreateRoom(ctx, &tesserav1.CreateRoomRequest{Name: "rec", RecordingEnabled: true})
	if err != nil || !r.RecordingEnabled {
		t.Fatalf("%v %+v", err, r)
	}
}
