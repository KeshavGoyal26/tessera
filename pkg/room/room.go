// Package room manages rooms, participants, subscriptions and permissions,
// connecting signaling sessions (pkg/signal) to the SFU engine (pkg/rtc).
//
// Ownership: WP1. Consumers: pkg/admin (Admin), pkg/recorder (Admin),
// pkg/signal (signal.SessionHandler via Manager), cmd/tessera.
package room

import (
	"context"
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/auth"
	"github.com/KeshavGoyal26/tessera/pkg/config"
	"github.com/KeshavGoyal26/tessera/pkg/errs"
	"github.com/KeshavGoyal26/tessera/pkg/rtc"
	"github.com/KeshavGoyal26/tessera/pkg/signal"
	"github.com/KeshavGoyal26/tessera/pkg/webhook"
)

// Admin is the room management API consumed by the admin API (WP3) and the
// recording Coordinator (WP4). Errors use pkg/errs classes:
// ErrNotFound (room/participant/track), ErrInvalidArgument,
// ErrResourceExhausted (limits), ErrUnavailable (draining).
type Admin interface {
	// CreateRoom creates a room or returns the existing one with the same name.
	CreateRoom(ctx context.Context, req *tesserav1.CreateRoomRequest) (*tesserav1.Room, error)
	// ListRooms lists rooms; names filters when non-empty.
	ListRooms(ctx context.Context, names []string) ([]*tesserav1.Room, error)
	DeleteRoom(ctx context.Context, room string) error
	UpdateRoomMetadata(ctx context.Context, room, metadata string) (*tesserav1.Room, error)
	// ListParticipants includes hidden participants.
	ListParticipants(ctx context.Context, room string) ([]*tesserav1.ParticipantInfo, error)
	GetParticipant(ctx context.Context, room, identity string) (*tesserav1.ParticipantInfo, error)
	RemoveParticipant(ctx context.Context, room, identity string) error
	MutePublishedTrack(ctx context.Context, room, identity, trackSID string, muted bool) (*tesserav1.TrackInfo, error)
	UpdateParticipant(ctx context.Context, req *tesserav1.UpdateParticipantRequest) (*tesserav1.ParticipantInfo, error)
}

// Manager owns all rooms of the node.
type Manager interface {
	Admin
	signal.SessionHandler
	// Drain refuses new joins and asks clients to reconnect elsewhere.
	Drain(ctx context.Context) error
	// Ready reports readiness (false while draining).
	Ready() error
	Close()
}

// Params are the dependencies of the Manager.
type Params struct {
	Config   *config.Config
	Engine   rtc.Engine
	Notifier webhook.Notifier
	Keys     auth.KeyProvider
	Registry prometheus.Registerer
	Logger   *slog.Logger
}

// NewManager creates the room manager. Phase 0 stub: every operation returns
// errs.ErrUnimplemented.
func NewManager(p Params) (Manager, error) {
	return &stubManager{}, nil
}

type stubManager struct{}

var errNI = errs.NotImplemented("room.Manager")

func (*stubManager) CreateRoom(context.Context, *tesserav1.CreateRoomRequest) (*tesserav1.Room, error) {
	return nil, errNI
}
func (*stubManager) ListRooms(context.Context, []string) ([]*tesserav1.Room, error) {
	return nil, errNI
}
func (*stubManager) DeleteRoom(context.Context, string) error { return errNI }
func (*stubManager) UpdateRoomMetadata(context.Context, string, string) (*tesserav1.Room, error) {
	return nil, errNI
}
func (*stubManager) ListParticipants(context.Context, string) ([]*tesserav1.ParticipantInfo, error) {
	return nil, errNI
}
func (*stubManager) GetParticipant(context.Context, string, string) (*tesserav1.ParticipantInfo, error) {
	return nil, errNI
}
func (*stubManager) RemoveParticipant(context.Context, string, string) error { return errNI }
func (*stubManager) MutePublishedTrack(context.Context, string, string, string, bool) (*tesserav1.TrackInfo, error) {
	return nil, errNI
}
func (*stubManager) UpdateParticipant(context.Context, *tesserav1.UpdateParticipantRequest) (*tesserav1.ParticipantInfo, error) {
	return nil, errNI
}
func (*stubManager) Admit(context.Context, *auth.Claims, signal.ConnectParams) error { return errNI }
func (*stubManager) HandleSession(context.Context, *auth.Claims, signal.ConnectParams, signal.Conn) error {
	return errNI
}
func (*stubManager) Drain(context.Context) error { return nil }
func (*stubManager) Ready() error                { return nil }
func (*stubManager) Close()                      {}
