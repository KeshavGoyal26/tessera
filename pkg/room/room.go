// Package room manages rooms, participants, subscriptions and permissions,
// connecting signaling sessions (pkg/signal) to the SFU engine (pkg/rtc).
//
// Ownership: WP1. Consumers: pkg/admin (Admin), pkg/recorder (Admin),
// pkg/signal (signal.SessionHandler via Manager), cmd/tessera.
package room

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/auth"
	"github.com/KeshavGoyal26/tessera/pkg/config"
	"github.com/KeshavGoyal26/tessera/pkg/errs"
	"github.com/KeshavGoyal26/tessera/pkg/rtc"
	"github.com/KeshavGoyal26/tessera/pkg/signal"
	"github.com/KeshavGoyal26/tessera/pkg/stats"
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
	stats.Provider
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
	// Recording, if set, records participants who join with the auto_record grant (or
	// into a room created with recording_enabled) and is told when participants leave.
	Recording RecordingTrigger
}

// RecordingTrigger starts and stops participant recordings as participants come and go.
// Calls run on their own goroutine.
type RecordingTrigger interface {
	ParticipantJoined(room, identity string)
	ParticipantLeft(room, identity string)
}

type manager struct {
	cfg       *config.Config
	engine    rtc.Engine
	notifier  webhook.Notifier
	recording RecordingTrigger
	logger    *slog.Logger
	sampler   *stats.NodeSampler

	mu       sync.RWMutex
	rooms    map[string]*Room
	bySID    map[string]*Participant // all participants (for resume)
	draining atomic.Bool
	closed   atomic.Bool
}

// NewManager creates the room manager.
func NewManager(p Params) (Manager, error) {
	cfg := p.Config
	if cfg == nil {
		cfg = config.Default()
	}
	if p.Notifier == nil {
		p.Notifier = webhook.Nop{}
	}
	if p.Logger == nil {
		p.Logger = slog.Default()
	}
	if cfg.Signal.NegotiationDebounce > 0 {
		rtc.NegotiationDebounce = cfg.Signal.NegotiationDebounce
	}
	m := &manager{
		cfg: cfg, engine: p.Engine, notifier: p.Notifier, recording: p.Recording, logger: p.Logger,
		sampler: stats.NewNodeSampler(cfg.NodeID),
		rooms:   map[string]*Room{}, bySID: map[string]*Participant{},
	}
	if p.Registry != nil {
		_ = p.Registry.Register(stats.NewCollector(m))
	}
	return m, nil
}

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

func newSID(prefix string) string {
	var b [10]byte
	_, _ = rand.Read(b[:])
	return prefix + strings.ToLower(b32.EncodeToString(b[:]))
}

func (m *manager) notify(t tesserav1.WebhookEventType, r *Room, p *Participant, tr *tesserav1.TrackInfo) {
	ev := &tesserav1.WebhookEvent{Type: t, Track: tr}
	if r != nil {
		ev.Room = r.ToProto()
	}
	if p != nil {
		ev.Participant = p.ToProto()
	}
	m.notifier.Notify(context.Background(), ev)
}

func (m *manager) getRoom(name string) *Room {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.rooms[name]
}

func (m *manager) getOrCreateRoom(name string, rc *auth.RoomConfig, create bool) (*Room, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.rooms[name]; r != nil {
		return r, nil
	}
	if !create {
		return nil, errs.New(errs.CodeNotFound, "room %q not found", name)
	}
	if m.draining.Load() {
		return nil, errs.New(errs.CodeUnavailable, "node draining")
	}
	if max := m.cfg.Limits.MaxRooms; max > 0 && len(m.rooms) >= max {
		return nil, errs.New(errs.CodeResourceExhausted, "room limit reached")
	}
	info := &tesserav1.Room{
		Sid: newSID("RM_"), Name: name, CreationTimeMs: time.Now().UnixMilli(),
		EmptyTimeoutSeconds:     uint32(m.cfg.Room.EmptyTimeout / time.Second),
		DepartureTimeoutSeconds: uint32(m.cfg.Room.DepartureTimeout / time.Second),
		MaxParticipants:         uint32(m.cfg.Room.MaxParticipants),
	}
	if rc != nil {
		if rc.EmptyTimeoutSeconds > 0 {
			info.EmptyTimeoutSeconds = rc.EmptyTimeoutSeconds
		}
		if rc.DepartureTimeoutSeconds > 0 {
			info.DepartureTimeoutSeconds = rc.DepartureTimeoutSeconds
		}
		if rc.MaxParticipants > 0 {
			info.MaxParticipants = rc.MaxParticipants
		}
		info.RecordingEnabled = rc.RecordingEnabled
	}
	r := newRoom(m, info)
	m.rooms[name] = r
	go m.notify(tesserav1.WebhookEventType_WEBHOOK_EVENT_TYPE_ROOM_STARTED, r, nil, nil)
	return r, nil
}

func (m *manager) removeRoom(r *Room) {
	m.mu.Lock()
	if m.rooms[r.Name()] == r {
		delete(m.rooms, r.Name())
	}
	m.mu.Unlock()
}

// CreateRoom implements Admin.
func (m *manager) CreateRoom(_ context.Context, req *tesserav1.CreateRoomRequest) (*tesserav1.Room, error) {
	if req.GetName() == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "room name required")
	}
	rc := &auth.RoomConfig{EmptyTimeoutSeconds: req.GetEmptyTimeoutSeconds(), DepartureTimeoutSeconds: req.GetDepartureTimeoutSeconds(),
		MaxParticipants: req.GetMaxParticipants()}
	r, err := m.getOrCreateRoom(req.GetName(), rc, true)
	if err != nil {
		return nil, err
	}
	if req.GetMetadata() != "" {
		r.setMetadata(req.GetMetadata())
	}
	r.armEmptyTimer()
	return r.ToProto(), nil
}

// ListRooms implements Admin.
func (m *manager) ListRooms(_ context.Context, names []string) ([]*tesserav1.Room, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	out := []*tesserav1.Room{}
	for _, r := range m.rooms {
		if len(want) == 0 || want[r.Name()] {
			out = append(out, r.ToProto())
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *manager) mustRoom(name string) (*Room, error) {
	if r := m.getRoom(name); r != nil {
		return r, nil
	}
	return nil, errs.New(errs.CodeNotFound, "room %q not found", name)
}

// DeleteRoom implements Admin.
func (m *manager) DeleteRoom(_ context.Context, name string) error {
	r, err := m.mustRoom(name)
	if err != nil {
		return err
	}
	r.close(tesserav1.DisconnectReason_DISCONNECT_REASON_ROOM_DELETED)
	return nil
}

// UpdateRoomMetadata implements Admin.
func (m *manager) UpdateRoomMetadata(_ context.Context, name, metadata string) (*tesserav1.Room, error) {
	r, err := m.mustRoom(name)
	if err != nil {
		return nil, err
	}
	r.setMetadata(metadata)
	info := r.ToProto()
	r.broadcast(&tesserav1.SignalResponse{Message: &tesserav1.SignalResponse_RoomUpdate{RoomUpdate: &tesserav1.RoomUpdate{Room: info}}}, nil)
	return info, nil
}

// ListParticipants implements Admin.
func (m *manager) ListParticipants(_ context.Context, name string) ([]*tesserav1.ParticipantInfo, error) {
	r, err := m.mustRoom(name)
	if err != nil {
		return nil, err
	}
	out := []*tesserav1.ParticipantInfo{}
	for _, p := range r.Participants() {
		out = append(out, p.ToProto())
	}
	return out, nil
}

func (m *manager) mustParticipant(room, identity string) (*Room, *Participant, error) {
	r, err := m.mustRoom(room)
	if err != nil {
		return nil, nil, err
	}
	p := r.byIdentity(identity)
	if p == nil {
		return nil, nil, errs.New(errs.CodeNotFound, "participant %q not found", identity)
	}
	return r, p, nil
}

// GetParticipant implements Admin.
func (m *manager) GetParticipant(_ context.Context, room, identity string) (*tesserav1.ParticipantInfo, error) {
	_, p, err := m.mustParticipant(room, identity)
	if err != nil {
		return nil, err
	}
	return p.ToProto(), nil
}

// RemoveParticipant implements Admin.
func (m *manager) RemoveParticipant(_ context.Context, room, identity string) error {
	r, p, err := m.mustParticipant(room, identity)
	if err != nil {
		return err
	}
	r.removeParticipant(p, tesserav1.DisconnectReason_DISCONNECT_REASON_PARTICIPANT_REMOVED, true)
	return nil
}

// MutePublishedTrack implements Admin.
func (m *manager) MutePublishedTrack(_ context.Context, room, identity, trackSID string, muted bool) (*tesserav1.TrackInfo, error) {
	r, p, err := m.mustParticipant(room, identity)
	if err != nil {
		return nil, err
	}
	info, err := p.setTrackMuted(trackSID, muted)
	if err != nil {
		return nil, err
	}
	p.send(&tesserav1.SignalResponse{Message: &tesserav1.SignalResponse_Mute{Mute: &tesserav1.MuteTrackRequest{Sid: trackSID, Muted: muted}}})
	r.broadcastParticipant(p)
	return info, nil
}

// UpdateParticipant implements Admin.
func (m *manager) UpdateParticipant(_ context.Context, req *tesserav1.UpdateParticipantRequest) (*tesserav1.ParticipantInfo, error) {
	r, p, err := m.mustParticipant(req.GetRoom(), req.GetIdentity())
	if err != nil {
		return nil, err
	}
	p.update(req)
	r.broadcastParticipant(p)
	p.send(&tesserav1.SignalResponse{Message: &tesserav1.SignalResponse_Update{Update: &tesserav1.ParticipantUpdate{Participants: []*tesserav1.ParticipantInfo{p.ToProto()}}}})
	return p.ToProto(), nil
}

// Admit implements signal.SessionHandler.
func (m *manager) Admit(_ context.Context, claims *auth.Claims, p signal.ConnectParams) error {
	if m.draining.Load() {
		return errs.New(errs.CodeUnavailable, "node draining")
	}
	if p.Reconnect {
		m.mu.RLock()
		part := m.bySID[p.ParticipantSID]
		m.mu.RUnlock()
		if part == nil || part.Identity() != claims.Identity() || part.room.Name() != claims.Grants.Room || part.isClosed() {
			return signal.ResumeFailed("unknown participant " + p.ParticipantSID)
		}
		return nil
	}
	if !m.cfg.Room.AutoCreate && m.getRoom(claims.Grants.Room) == nil {
		return errs.New(errs.CodeNotFound, "room %q not found", claims.Grants.Room)
	}
	if max := m.cfg.Limits.MaxParticipants; max > 0 {
		m.mu.RLock()
		n := len(m.bySID)
		m.mu.RUnlock()
		if n >= max {
			return errs.New(errs.CodeResourceExhausted, "participant limit reached")
		}
	}
	return nil
}

// HandleSession implements signal.SessionHandler.
func (m *manager) HandleSession(ctx context.Context, claims *auth.Claims, cp signal.ConnectParams, conn signal.Conn) error {
	if cp.Reconnect {
		m.mu.RLock()
		p := m.bySID[cp.ParticipantSID]
		m.mu.RUnlock()
		if p == nil {
			_ = conn.Close(signal.CloseProtocolError, "resume failed")
			return errs.New(errs.CodeNotFound, "resume failed")
		}
		return p.resume(ctx, conn)
	}
	g := claims.Grants
	r, err := m.getOrCreateRoom(g.Room, g.RoomConfig, m.cfg.Room.AutoCreate)
	if err != nil {
		_ = conn.Close(signal.CloseProtocolError, err.Error())
		return err
	}
	p, err := r.join(claims, cp, conn)
	if err != nil {
		_ = conn.Close(signal.CloseProtocolError, err.Error())
		return err
	}
	return p.run(ctx)
}

// Drain implements Manager.
func (m *manager) Drain(ctx context.Context) error {
	m.draining.Store(true)
	m.mu.RLock()
	rooms := make([]*Room, 0, len(m.rooms))
	for _, r := range m.rooms {
		rooms = append(rooms, r)
	}
	m.mu.RUnlock()
	for _, r := range rooms {
		r.close(tesserav1.DisconnectReason_DISCONNECT_REASON_SERVER_SHUTDOWN)
	}
	return nil
}

// Ready implements Manager.
func (m *manager) Ready() error {
	if m.draining.Load() {
		return errs.New(errs.CodeUnavailable, "draining")
	}
	return nil
}

// Close implements Manager.
func (m *manager) Close() {
	if m.closed.CompareAndSwap(false, true) {
		_ = m.Drain(context.Background())
	}
}
