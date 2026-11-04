package recorder

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/auth"
	"github.com/KeshavGoyal26/tessera/pkg/config"
	"github.com/KeshavGoyal26/tessera/pkg/errs"
)

type fakeNotifier struct {
	mu     sync.Mutex
	events []*tesserav1.WebhookEvent
}

func (f *fakeNotifier) Notify(_ context.Context, ev *tesserav1.WebhookEvent) {
	f.mu.Lock()
	f.events = append(f.events, ev)
	f.mu.Unlock()
}
func (f *fakeNotifier) Close(context.Context) error { return nil }
func (f *fakeNotifier) types() []tesserav1.WebhookEventType {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []tesserav1.WebhookEventType
	for _, e := range f.events {
		out = append(out, e.Type)
	}
	return out
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newTestCoordinator(t *testing.T) (*Coordinator, *fakeNotifier, *clock) {
	n := &fakeNotifier{}
	clk := &clock{t: time.Unix(1000, 0)}
	c := NewCoordinator(CoordinatorParams{
		Config: config.RecordingConfig{WSURL: "ws://x", HeartbeatTimeout: time.Hour}, Notifier: n,
		APIKey: "key", Secret: "secret-secret-secret-secret-1234", Now: clk.now,
	})
	t.Cleanup(c.Close)
	return c, n, clk
}

func trackReq(room string) *tesserav1.StartRecordingRequest {
	return &tesserav1.StartRecordingRequest{Room: room, Target: &tesserav1.StartRecordingRequest_Track{Track: &tesserav1.TrackRecordingTarget{TrackSid: "TR_1"}}}
}

func TestCoordinatorLifecycle(t *testing.T) {
	c, n, _ := newTestCoordinator(t)
	ctx := context.Background()
	info, err := c.StartRecording(ctx, trackReq("r"))
	if err != nil || info.Status != tesserav1.RecordingStatus_RECORDING_STATUS_STARTING || info.RecordingId[:3] != "RC_" {
		t.Fatalf("%v %v", info, err)
	}
	resp, err := c.ClaimJob(ctx, &tesserav1.ClaimJobRequest{WorkerId: "w1", AvailableCapacity: 4})
	if err != nil || resp.Job == nil {
		t.Fatalf("%v %v", resp, err)
	}
	if resp.Job.WsUrl != "ws://x" {
		t.Fatal(resp.Job.WsUrl)
	}
	claims, err := auth.NewVerifier(auth.StaticKeys{"key": "secret-secret-secret-secret-1234"}).Verify(resp.Job.Token)
	if err != nil || !claims.Grants.Hidden || !claims.Grants.Recorder || claims.Grants.Room != "r" {
		t.Fatalf("%+v %v", claims, err)
	}
	id := info.RecordingId
	if err := c.UpdateJob(ctx, &tesserav1.UpdateJobRequest{WorkerId: "w1", RecordingId: id, Status: tesserav1.RecordingStatus_RECORDING_STATUS_ACTIVE}); err != nil {
		t.Fatal(err)
	}
	// Another worker cannot update it.
	if err := c.UpdateJob(ctx, &tesserav1.UpdateJobRequest{WorkerId: "w2", RecordingId: id}); !errors.Is(err, errs.ErrFailedPrecondition) {
		t.Fatalf("%v", err)
	}
	// Stop: ENDING, and the heartbeat tells the worker to stop.
	st, _ := c.StopRecording(ctx, id)
	if st.Status != tesserav1.RecordingStatus_RECORDING_STATUS_ENDING {
		t.Fatal(st.Status)
	}
	hb, _ := c.Heartbeat(ctx, &tesserav1.HeartbeatRequest{WorkerId: "w1", RecordingIds: []string{id}})
	if len(hb.StopRecordingIds) != 1 {
		t.Fatal("expected stop")
	}
	_ = c.UpdateJob(ctx, &tesserav1.UpdateJobRequest{WorkerId: "w1", RecordingId: id, Status: tesserav1.RecordingStatus_RECORDING_STATUS_COMPLETE,
		MasterPlaylistLocation: "/m.m3u8", Outputs: []*tesserav1.RecordingOutput{{TrackSid: "TR_1", PlaylistLocation: "/p"}}})
	got, _ := c.GetRecording(ctx, id)
	if got.Status != tesserav1.RecordingStatus_RECORDING_STATUS_COMPLETE || got.EndedAtMs == 0 || got.MasterPlaylistLocation != "/m.m3u8" || len(got.Outputs) != 1 {
		t.Fatalf("%v", got)
	}
	want := []tesserav1.WebhookEventType{tesserav1.WebhookEventType_WEBHOOK_EVENT_TYPE_RECORDING_STARTED, tesserav1.WebhookEventType_WEBHOOK_EVENT_TYPE_RECORDING_ENDED}
	if got := n.types(); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("%v", got)
	}
	if l, _ := c.ListRecordings(ctx, &tesserav1.ListRecordingsRequest{ActiveOnly: true}); len(l) != 0 {
		t.Fatal("active filter")
	}
	if l, _ := c.ListRecordings(ctx, &tesserav1.ListRecordingsRequest{Room: "r"}); len(l) != 1 {
		t.Fatal("room filter")
	}
}

func TestCoordinatorValidationAndAbort(t *testing.T) {
	c, n, _ := newTestCoordinator(t)
	ctx := context.Background()
	if _, err := c.StartRecording(ctx, &tesserav1.StartRecordingRequest{Room: "r"}); !errors.Is(err, errs.ErrInvalidArgument) {
		t.Fatal(err)
	}
	if _, err := c.StartRecording(ctx, &tesserav1.StartRecordingRequest{Room: "r", Target: &tesserav1.StartRecordingRequest_RoomComposite{}}); !errors.Is(err, errs.ErrUnimplemented) {
		t.Fatal(err)
	}
	info, _ := c.StartRecording(ctx, trackReq("r"))
	st, _ := c.StopRecording(ctx, info.RecordingId) // never claimed
	if st.Status != tesserav1.RecordingStatus_RECORDING_STATUS_ABORTED {
		t.Fatal(st.Status)
	}
	if got := n.types(); len(got) != 1 || got[0] != tesserav1.WebhookEventType_WEBHOOK_EVENT_TYPE_RECORDING_ENDED {
		t.Fatalf("%v", got)
	}
	if _, err := c.GetRecording(ctx, "RC_nope"); !errors.Is(err, errs.ErrNotFound) {
		t.Fatal(err)
	}
	// An aborted job is not handed out.
	resp, _ := c.ClaimJob(ctx, &tesserav1.ClaimJobRequest{WorkerId: "w", AvailableCapacity: 1})
	if resp.Job != nil {
		t.Fatal("aborted job claimed")
	}
}

func TestCoordinatorCapacityAndLongPoll(t *testing.T) {
	c, _, _ := newTestCoordinator(t)
	ctx := context.Background()
	req := &tesserav1.StartRecordingRequest{Room: "r", Target: &tesserav1.StartRecordingRequest_Participant{Participant: &tesserav1.ParticipantRecordingTarget{Identity: "alice"}}}
	if _, err := c.StartRecording(ctx, req); err != nil {
		t.Fatal(err)
	}
	// A participant job needs 2 units.
	if resp, _ := c.ClaimJob(ctx, &tesserav1.ClaimJobRequest{WorkerId: "w", AvailableCapacity: 1}); resp.Job != nil {
		t.Fatal("capacity not respected")
	}
	// Long poll wakes up when a job arrives.
	got := make(chan *tesserav1.ClaimJobResponse, 1)
	go func() {
		r, _ := c.ClaimJob(ctx, &tesserav1.ClaimJobRequest{WorkerId: "w", AvailableCapacity: 2, WaitSeconds: 10})
		got <- r
	}()
	select {
	case r := <-got:
		if r.Job == nil {
			t.Fatal("expected job")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("long poll did not return")
	}
	// Empty queue: returns after the wait (cancel via ctx to keep the test fast).
	cctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	r, err := c.ClaimJob(cctx, &tesserav1.ClaimJobRequest{WorkerId: "w", AvailableCapacity: 2, WaitSeconds: 30})
	if err != nil || r.Job != nil {
		t.Fatalf("%v %v", r, err)
	}
}

func TestCoordinatorHeartbeatTimeout(t *testing.T) {
	c, n, clk := newTestCoordinator(t)
	ctx := context.Background()
	// Never-started job is re-queued.
	a, _ := c.StartRecording(ctx, trackReq("r"))
	c.ClaimJob(ctx, &tesserav1.ClaimJobRequest{WorkerId: "dead", AvailableCapacity: 1})
	clk.add(2 * time.Hour)
	c.Reap()
	r, _ := c.ClaimJob(ctx, &tesserav1.ClaimJobRequest{WorkerId: "alive", AvailableCapacity: 1})
	if r.Job == nil || r.Job.Recording.RecordingId != a.RecordingId {
		t.Fatal("job must be re-queued")
	}
	// Active job fails.
	c.UpdateJob(ctx, &tesserav1.UpdateJobRequest{WorkerId: "alive", RecordingId: a.RecordingId, Status: tesserav1.RecordingStatus_RECORDING_STATUS_ACTIVE})
	clk.add(2 * time.Hour)
	c.Reap()
	got, _ := c.GetRecording(ctx, a.RecordingId)
	if got.Status != tesserav1.RecordingStatus_RECORDING_STATUS_FAILED || got.Error == "" {
		t.Fatalf("%v", got)
	}
	types := n.types()
	if types[len(types)-1] != tesserav1.WebhookEventType_WEBHOOK_EVENT_TYPE_RECORDING_ENDED {
		t.Fatalf("%v", types)
	}
	// Late heartbeat from the lost worker is told to stop.
	hb, _ := c.Heartbeat(ctx, &tesserav1.HeartbeatRequest{WorkerId: "alive", RecordingIds: []string{a.RecordingId}})
	if len(hb.StopRecordingIds) != 1 {
		t.Fatal("expected stop for failed job")
	}
}
