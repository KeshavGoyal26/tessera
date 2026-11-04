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
