package recorder

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/protobuf/proto"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/auth"
	"github.com/KeshavGoyal26/tessera/pkg/errs"
	"github.com/KeshavGoyal26/tessera/pkg/webhook"
)

// Coordinator implements Service and WorkerAPI: the in-server recording job
// queue (ADR-0007). State is in memory; the job store is the only part that
// has to move to Redis for multi-node.
type Coordinator struct {
	p   CoordinatorParams
	now func() time.Time

	mu      sync.Mutex
	jobs    map[string]*job
	order   []string // creation order
	queue   []string // unclaimed job ids, FIFO
	workers map[string]*workerState
	wake    chan struct{}

	stop chan struct{}
	done chan struct{}
}

type job struct {
	info       *tesserav1.RecordingInfo
	worker     string
	lastBeat   time.Time
	stopWanted bool
	started    bool // RECORDING_STARTED sent
}

type workerState struct {
	lastSeen time.Time
	capacity uint32
}

var (
	_ Service   = (*Coordinator)(nil)
	_ WorkerAPI = (*Coordinator)(nil)
)

const maxClaimWait = 30 * time.Second

// maxFinishedJobs bounds how many ended recordings are kept for listing; older
// ones are forgotten (their files stay in storage).
const maxFinishedJobs = 1000

// NewCoordinator creates the Coordinator and starts its failure detector.
// Call Close to stop it.
func NewCoordinator(p CoordinatorParams) *Coordinator {
	if p.Notifier == nil {
		p.Notifier = webhook.Nop{}
	}
	if p.Logger == nil {
		p.Logger = slog.Default()
	}
	if p.Config.HeartbeatTimeout <= 0 {
		p.Config.HeartbeatTimeout = 15 * time.Second
	}
	c := &Coordinator{
		p: p, now: time.Now, jobs: map[string]*job{}, workers: map[string]*workerState{},
		wake: make(chan struct{}), stop: make(chan struct{}), done: make(chan struct{}),
	}
	if p.Now != nil {
		c.now = p.Now
	}
	if p.Registry != nil {
		p.Registry.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: "tessera", Name: "recording_jobs_queued", Help: "Recording jobs waiting for a worker.",
		}, func() float64 { c.mu.Lock(); defer c.mu.Unlock(); return float64(len(c.queue)) }))
		p.Registry.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: "tessera", Name: "recording_workers", Help: "Recorder workers seen recently.",
		}, func() float64 { c.mu.Lock(); defer c.mu.Unlock(); return float64(len(c.workers)) }))
	}
	go c.reapLoop()
	return c
}

// Close stops background work.
func (c *Coordinator) Close() {
	select {
	case <-c.stop:
	default:
		close(c.stop)
		<-c.done
	}
}

func (c *Coordinator) reapLoop() {
	defer close(c.done)
	t := time.NewTicker(c.p.Config.HeartbeatTimeout / 3)
	defer t.Stop()
	for {
		select {
		case <-c.stop:
			return
		case <-t.C:
			c.Reap()
		}
	}
}

func newRecordingID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "RC_" + hex.EncodeToString(b[:])
}

func terminal(s tesserav1.RecordingStatus) bool {
	return s == tesserav1.RecordingStatus_RECORDING_STATUS_COMPLETE ||
		s == tesserav1.RecordingStatus_RECORDING_STATUS_FAILED ||
		s == tesserav1.RecordingStatus_RECORDING_STATUS_ABORTED
}

func (c *Coordinator) broadcast() {
	close(c.wake)
	c.wake = make(chan struct{})
}

func (c *Coordinator) notify(t tesserav1.WebhookEventType, info *tesserav1.RecordingInfo) {
	c.p.Notifier.Notify(context.Background(), &tesserav1.WebhookEvent{
		Type: t, Recording: proto.Clone(info).(*tesserav1.RecordingInfo),
	})
}

// StartRecording implements Service.
func (c *Coordinator) StartRecording(ctx context.Context, req *tesserav1.StartRecordingRequest) (*tesserav1.RecordingInfo, error) {
	if req.GetRoom() == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "room is required")
	}
	switch req.GetTarget().(type) {
	case *tesserav1.StartRecordingRequest_Track:
		if req.GetTrack().GetTrackSid() == "" {
			return nil, errs.New(errs.CodeInvalidArgument, "track_sid is required")
		}
	case *tesserav1.StartRecordingRequest_Participant:
		if req.GetParticipant().GetIdentity() == "" {
			return nil, errs.New(errs.CodeInvalidArgument, "participant identity is required")
		}
	case *tesserav1.StartRecordingRequest_RoomComposite:
		return nil, errs.New(errs.CodeUnimplemented, "room composite recording is not supported yet")
	default:
		return nil, errs.New(errs.CodeInvalidArgument, "a recording target is required")
	}
	var roomSID string
	if c.p.Rooms != nil {
		rooms, err := c.p.Rooms.ListRooms(ctx, []string{req.GetRoom()})
		if err != nil {
			return nil, err
		}
		if len(rooms) == 0 {
			return nil, errs.New(errs.CodeNotFound, "room %q not found", req.GetRoom())
		}
		roomSID = rooms[0].GetSid()
		if t := req.GetTrack().GetTrackSid(); t != "" {
			// Validate the track exists in the room.
			found := false
			parts, err := c.p.Rooms.ListParticipants(ctx, req.GetRoom())
			if err != nil {
				return nil, err
			}
			for _, pi := range parts {
				for _, ti := range pi.GetTracks() {
					found = found || ti.GetSid() == t
				}
			}
			if !found {
				return nil, errs.New(errs.CodeNotFound, "track %q not found in room %q", t, req.GetRoom())
			}
		}
	}
	info := &tesserav1.RecordingInfo{
		RecordingId: newRecordingID(),
		Room:        req.GetRoom(),
		RoomSid:     roomSID,
		Status:      tesserav1.RecordingStatus_RECORDING_STATUS_STARTING,
		StartedAtMs: c.now().UnixMilli(),
		Request:     proto.Clone(req).(*tesserav1.StartRecordingRequest),
	}
	c.mu.Lock()
	c.jobs[info.RecordingId] = &job{info: info}
	c.order = append(c.order, info.RecordingId)
	c.queue = append(c.queue, info.RecordingId)
	c.broadcast()
	out := proto.Clone(info).(*tesserav1.RecordingInfo)
	c.mu.Unlock()
	return out, nil
}

func (c *Coordinator) get(id string) (*job, error) {
	j, ok := c.jobs[id]
	if !ok {
		return nil, errs.New(errs.CodeNotFound, "recording %q not found", id)
	}
	return j, nil
}

// finishLocked moves a job to a terminal state. Returns the event to send after unlock.
func (c *Coordinator) finishLocked(j *job, st tesserav1.RecordingStatus, msg string) *tesserav1.RecordingInfo {
	if terminal(j.info.Status) {
		return nil
	}
	j.info.Status = st
	if msg != "" {
		j.info.Error = msg
	}
	j.info.EndedAtMs = c.now().UnixMilli()
	for i, id := range c.queue {
		if id == j.info.RecordingId {
			c.queue = append(c.queue[:i], c.queue[i+1:]...)
			break
		}
	}
	return proto.Clone(j.info).(*tesserav1.RecordingInfo)
}

// StopRecording implements Service.
func (c *Coordinator) StopRecording(_ context.Context, id string) (*tesserav1.RecordingInfo, error) {
	c.mu.Lock()
	j, err := c.get(id)
	if err != nil {
		c.mu.Unlock()
		return nil, err
	}
	var ended *tesserav1.RecordingInfo
	switch {
	case terminal(j.info.Status):
	case j.worker == "":
		ended = c.finishLocked(j, tesserav1.RecordingStatus_RECORDING_STATUS_ABORTED, "")
	default:
		j.stopWanted = true
		j.info.Status = tesserav1.RecordingStatus_RECORDING_STATUS_ENDING
	}
	out := proto.Clone(j.info).(*tesserav1.RecordingInfo)
	c.mu.Unlock()
	if ended != nil {
		c.notify(tesserav1.WebhookEventType_WEBHOOK_EVENT_TYPE_RECORDING_ENDED, ended)
	}
	return out, nil
}

// ListRecordings implements Service.
func (c *Coordinator) ListRecordings(_ context.Context, req *tesserav1.ListRecordingsRequest) ([]*tesserav1.RecordingInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []*tesserav1.RecordingInfo
	for _, id := range c.order {
		j := c.jobs[id]
		if req.GetRoom() != "" && j.info.Room != req.GetRoom() {
			continue
		}
		if req.GetActiveOnly() && terminal(j.info.Status) {
			continue
		}
		out = append(out, proto.Clone(j.info).(*tesserav1.RecordingInfo))
	}
	return out, nil
}

// GetRecording implements Service.
func (c *Coordinator) GetRecording(_ context.Context, id string) (*tesserav1.RecordingInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	j, err := c.get(id)
	if err != nil {
		return nil, err
	}
	return proto.Clone(j.info).(*tesserav1.RecordingInfo), nil
}

// jobCost is the capacity units a job needs to start (one per track; a
// participant recording is budgeted as two: camera + microphone).
func jobCost(info *tesserav1.RecordingInfo) uint32 {
	if info.GetRequest().GetParticipant() != nil {
		return 2
	}
	return 1
}

// ClaimJob implements WorkerAPI (long poll).
func (c *Coordinator) ClaimJob(ctx context.Context, req *tesserav1.ClaimJobRequest) (*tesserav1.ClaimJobResponse, error) {
	if req.GetWorkerId() == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "worker_id is required")
	}
	wait := time.Duration(req.GetWaitSeconds()) * time.Second
	if wait > maxClaimWait {
		wait = maxClaimWait
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		c.mu.Lock()
		c.workers[req.WorkerId] = &workerState{lastSeen: c.now(), capacity: req.AvailableCapacity}
		for i, id := range c.queue {
			j := c.jobs[id]
			if jobCost(j.info) > req.AvailableCapacity {
				continue
			}
			c.queue = append(c.queue[:i], c.queue[i+1:]...)
			j.worker = req.WorkerId
			j.lastBeat = c.now()
			j.info.WorkerId = req.WorkerId
			token, err := c.token(j.info.Room, j.info.RecordingId)
			if err != nil {
				c.mu.Unlock()
				return nil, err
			}
			out := &tesserav1.RecordingJob{
				Recording: proto.Clone(j.info).(*tesserav1.RecordingInfo),
				WsUrl:     c.p.Config.WSURL, Token: token,
			}
			c.mu.Unlock()
			return &tesserav1.ClaimJobResponse{Job: out}, nil
		}
		wake := c.wake
		c.mu.Unlock()
		select {
		case <-wake:
		case <-timer.C:
			return &tesserav1.ClaimJobResponse{}, nil
		case <-ctx.Done():
			return &tesserav1.ClaimJobResponse{}, nil
		case <-c.stop:
			return &tesserav1.ClaimJobResponse{}, nil
		}
	}
}

func (c *Coordinator) token(room, recordingID string) (string, error) {
	return auth.NewAccessToken(c.p.APIKey, c.p.Secret).
		SetIdentity("recorder-" + recordingID).
		SetName("recorder").
		SetGrant(JoinGrant(room)).
		SetValidFor(6 * time.Hour).
		ToJWT()
}

// UpdateJob implements WorkerAPI.
func (c *Coordinator) UpdateJob(_ context.Context, req *tesserav1.UpdateJobRequest) error {
	c.mu.Lock()
	j, err := c.get(req.GetRecordingId())
	if err != nil {
		c.mu.Unlock()
		return err
	}
	if j.worker != req.GetWorkerId() {
		c.mu.Unlock()
		return errs.New(errs.CodeFailedPrecondition, "recording %q is not assigned to worker %q", req.GetRecordingId(), req.GetWorkerId())
	}
	var events []func()
	st := req.GetStatus()
	if len(req.GetOutputs()) > 0 {
		j.info.Outputs = req.GetOutputs()
	}
	if req.GetMasterPlaylistLocation() != "" {
		j.info.MasterPlaylistLocation = req.GetMasterPlaylistLocation()
	}
	j.lastBeat = c.now()
	switch {
	case terminal(j.info.Status):
		// Late updates for finished jobs are ignored.
	case terminal(st):
		if ended := c.finishLocked(j, st, req.GetError()); ended != nil {
			events = append(events, func() {
				c.notify(tesserav1.WebhookEventType_WEBHOOK_EVENT_TYPE_RECORDING_ENDED, ended)
			})
		}
	case st == tesserav1.RecordingStatus_RECORDING_STATUS_ACTIVE:
		if j.info.Status == tesserav1.RecordingStatus_RECORDING_STATUS_STARTING {
			j.info.Status = st
		}
		if !j.started {
			j.started = true
			info := proto.Clone(j.info).(*tesserav1.RecordingInfo)
			events = append(events, func() {
				c.notify(tesserav1.WebhookEventType_WEBHOOK_EVENT_TYPE_RECORDING_STARTED, info)
			})
		}
	case st == tesserav1.RecordingStatus_RECORDING_STATUS_ENDING:
		j.info.Status = st
	}
	c.mu.Unlock()
	for _, f := range events {
		f()
	}
	return nil
}

// Heartbeat implements WorkerAPI.
func (c *Coordinator) Heartbeat(_ context.Context, req *tesserav1.HeartbeatRequest) (*tesserav1.HeartbeatResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	c.workers[req.GetWorkerId()] = &workerState{lastSeen: now, capacity: req.GetAvailableCapacity()}
	resp := &tesserav1.HeartbeatResponse{}
	for _, id := range req.GetRecordingIds() {
		j, ok := c.jobs[id]
		switch {
		case !ok, j.worker != req.GetWorkerId(), terminal(j.info.Status):
			resp.StopRecordingIds = append(resp.StopRecordingIds, id)
		default:
			j.lastBeat = now
			if j.stopWanted {
				resp.StopRecordingIds = append(resp.StopRecordingIds, id)
			}
		}
	}
	return resp, nil
}

// evictFinishedLocked drops the oldest ended jobs beyond maxFinishedJobs.
func (c *Coordinator) evictFinishedLocked() {
	finished := 0
	for _, id := range c.order {
		if terminal(c.jobs[id].info.Status) {
			finished++
		}
	}
	if finished <= maxFinishedJobs {
		return
	}
	drop := finished - maxFinishedJobs
	kept := c.order[:0]
	for _, id := range c.order {
		if drop > 0 && terminal(c.jobs[id].info.Status) {
			delete(c.jobs, id)
			drop--
			continue
		}
		kept = append(kept, id)
	}
	c.order = kept
}

// Reap fails or re-queues jobs whose worker missed heartbeats, and forgets the
// oldest ended jobs beyond maxFinishedJobs.
func (c *Coordinator) Reap() {
	now := c.now()
	var ended []*tesserav1.RecordingInfo
	c.mu.Lock()
	for id, w := range c.workers {
		if now.Sub(w.lastSeen) > 4*c.p.Config.HeartbeatTimeout {
			delete(c.workers, id)
		}
	}
	for _, id := range c.order {
		j := c.jobs[id]
		if j.worker == "" || terminal(j.info.Status) || now.Sub(j.lastBeat) <= c.p.Config.HeartbeatTimeout {
			continue
		}
		c.p.Logger.Warn("recorder worker lost", "recording", id, "worker", j.worker)
		if j.info.Status == tesserav1.RecordingStatus_RECORDING_STATUS_STARTING && !j.started && !j.stopWanted {
			// Never started: hand the job to another worker.
			j.worker, j.info.WorkerId = "", ""
			c.queue = append(c.queue, id)
			c.broadcast()
			continue
		}
		if e := c.finishLocked(j, tesserav1.RecordingStatus_RECORDING_STATUS_FAILED, "recorder worker lost (missed heartbeats)"); e != nil {
			ended = append(ended, e)
		}
	}
	c.evictFinishedLocked()
	c.mu.Unlock()
	for _, e := range ended {
		c.notify(tesserav1.WebhookEventType_WEBHOOK_EVENT_TYPE_RECORDING_ENDED, e)
	}
}

// ParticipantJoined starts recording identity in room, unless a recording of that
// participant is already in progress. It implements room.RecordingTrigger for
// participants who join with the auto_record grant or into a recording_enabled room.
func (c *Coordinator) ParticipantJoined(roomName, identity string) {
	c.mu.Lock()
	for _, j := range c.jobs {
		// A recording that is already ending (the participant rejoined) does not count.
		if !terminal(j.info.Status) && !j.stopWanted && j.info.Status != tesserav1.RecordingStatus_RECORDING_STATUS_ENDING && j.info.Room == roomName && j.info.GetRequest().GetParticipant().GetIdentity() == identity {
			c.mu.Unlock()
			return
		}
	}
	c.mu.Unlock()
	info, err := c.StartRecording(context.Background(), &tesserav1.StartRecordingRequest{
		Room:   roomName,
		Target: &tesserav1.StartRecordingRequest_Participant{Participant: &tesserav1.ParticipantRecordingTarget{Identity: identity}},
	})
	if err != nil {
		c.p.Logger.Warn("auto-record failed to start", "room", roomName, "identity", identity, "err", err)
		return
	}
	c.p.Logger.Info("auto-record started", "room", roomName, "identity", identity, "recording", info.GetRecordingId())
}

// ParticipantLeft stops the in-progress recordings of identity in room: a participant
// recording ends when its participant leaves.
func (c *Coordinator) ParticipantLeft(roomName, identity string) {
	c.mu.Lock()
	var ids []string
	for id, j := range c.jobs {
		if !terminal(j.info.Status) && j.info.Room == roomName && j.info.GetRequest().GetParticipant().GetIdentity() == identity {
			ids = append(ids, id)
		}
	}
	c.mu.Unlock()
	for _, id := range ids {
		if _, err := c.StopRecording(context.Background(), id); err != nil {
			c.p.Logger.Warn("stopping recording of departed participant failed", "recording", id, "err", err)
			continue
		}
		c.p.Logger.Info("recording stopped: participant left", "room", roomName, "identity", identity, "recording", id)
	}
}
