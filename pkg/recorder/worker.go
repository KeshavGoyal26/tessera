package recorder

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/hls"
	"github.com/KeshavGoyal26/tessera/pkg/storage"
)

// JobAPI is the worker's view of the Coordinator (which implements it).
type JobAPI interface {
	ClaimJob(ctx context.Context, req *tesserav1.ClaimJobRequest) (*tesserav1.ClaimJobResponse, error)
	UpdateJob(ctx context.Context, req *tesserav1.UpdateJobRequest) error
	Heartbeat(ctx context.Context, req *tesserav1.HeartbeatRequest) (*tesserav1.HeartbeatResponse, error)
}

// Room is a joined room; Close leaves it.
type Room interface {
	Close(ctx context.Context) error
}

// Connector joins a room as a hidden subscriber and delivers subscribed tracks.
// onClosed is called once when the connection ends (nil error = left cleanly).
type Connector interface {
	// Join connects to the room and subscribes to the tracks want accepts; onTrack is
	// called for each subscribed track.
	Join(ctx context.Context, wsURL, token string, want func(Track) bool, onTrack func(Track), onClosed func(error)) (Room, error)
}

// WorkerOption customises a Worker.
type WorkerOption func(*Worker)

// WithJobAPI sets the control-plane client.
func WithJobAPI(a JobAPI) WorkerOption { return func(w *Worker) { w.api = a } }

// WithConnector overrides the media connector (default: pkg/client).
func WithConnector(c Connector) WorkerOption { return func(w *Worker) { w.conn = c } }

// WithStorage overrides the default storage.
func WithStorage(s storage.Storage) WorkerOption { return func(w *Worker) { w.defStorage = s } }

// Worker runs recording jobs inside the media server.
type Worker struct {
	cfg        *Config
	logger     *slog.Logger
	api        JobAPI
	conn       Connector
	defStorage storage.Storage
	stats      *workerStats

	mu     sync.Mutex
	active map[string]*jobRun
	used   int
	wg     sync.WaitGroup
}

// NewWorker creates a worker.
func NewWorker(cfg *Config, reg prometheus.Registerer, logger *slog.Logger, opts ...WorkerOption) (*Worker, error) {
	if logger == nil {
		logger = slog.Default()
	}
	w := &Worker{cfg: cfg, logger: logger, active: map[string]*jobRun{}}
	w.stats = newWorkerStats(w)
	if cfg.WorkerID == "" {
		host, _ := os.Hostname()
		w.cfg.WorkerID = "recorder-" + host
	}
	for _, o := range opts {
		o(w)
	}
	if w.defStorage == nil {
		st, err := w.buildDefaultStorage()
		if err != nil {
			return nil, err
		}
		w.defStorage = st
	}
	if w.conn == nil {
		return nil, errors.New("recorder: no connector (use WithConnector)")
	}
	if reg != nil {
		w.stats.register(reg)
	}
	return w, nil
}

func (w *Worker) buildDefaultStorage() (storage.Storage, error) {
	sc := w.cfg.Storage
	if sc.Kind == "s3" {
		return storage.NewS3FromConfig(context.Background(), storage.S3Config{
			Bucket: sc.S3.Bucket, Region: sc.S3.Region, Endpoint: sc.S3.Endpoint, AccessKey: sc.S3.AccessKey,
			Secret: sc.S3.Secret, Prefix: sc.S3.Prefix, ForcePathStyle: sc.S3.ForcePathStyle,
		})
	}
	return storage.NewLocal(sc.LocalRoot, storage.WithBaseURL(sc.LocalBaseURL))
}

// Ready reports readiness (storage reachable, capacity available).
func (w *Worker) Ready() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := w.defStorage.Check(ctx); err != nil {
		return fmt.Errorf("storage: %w", err)
	}
	return nil
}

func (w *Worker) freeCapacity() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.cfg.Capacity - w.used
}

// Run claims and executes jobs until ctx is cancelled. Running jobs are
// finalized (playlists closed, uploads drained) before it returns.
func (w *Worker) Run(ctx context.Context) error {
	if w.api == nil {
		return errors.New("recorder: no job API configured")
	}
	hb := w.cfg.Heartbeat
	if hb <= 0 {
		hb = 5 * time.Second
	}
	hctx, stopHB := context.WithCancel(context.Background())
	hbDone := make(chan struct{})
	go func() { defer close(hbDone); w.heartbeatLoop(hctx, hb) }()
	defer func() {
		w.wg.Wait()
		stopHB()
		<-hbDone
	}()

	backoff := time.Second
	for ctx.Err() == nil {
		free := w.freeCapacity()
		if free <= 0 {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(500 * time.Millisecond):
			}
			continue
		}
		resp, err := w.api.ClaimJob(ctx, &tesserav1.ClaimJobRequest{
			WorkerId: w.cfg.WorkerID, AvailableCapacity: uint32(free), WaitSeconds: 20,
		})
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			w.logger.Warn("claim failed", "err", err)
			w.stats.claimErrors.Add(1)
			select {
			case <-ctx.Done():
			case <-time.After(backoff):
			}
			if backoff < 15*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		if resp.GetJob() == nil {
			continue
		}
		w.startJob(ctx, resp.Job)
	}
	return nil
}

func (w *Worker) startJob(ctx context.Context, j *tesserav1.RecordingJob) {
	run := newJobRun(w, j)
	w.mu.Lock()
	w.active[run.id] = run
	w.used += int(jobCost(j.GetRecording()))
	w.mu.Unlock()
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		// Shutdown stops the recording gracefully (ENDING → COMPLETE).
		jctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		stop := context.AfterFunc(ctx, cancel)
		defer stop()
		run.cancel = cancel
		run.execute(jctx)
		w.mu.Lock()
		delete(w.active, run.id)
		w.used -= int(jobCost(j.GetRecording()))
		w.mu.Unlock()
	}()
}

func (w *Worker) heartbeatLoop(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		w.mu.Lock()
		ids := make([]string, 0, len(w.active))
		for id := range w.active {
			ids = append(ids, id)
		}
		free := w.cfg.Capacity - w.used
		w.mu.Unlock()
		resp, err := w.api.Heartbeat(ctx, &tesserav1.HeartbeatRequest{WorkerId: w.cfg.WorkerID, RecordingIds: ids, AvailableCapacity: uint32(max(free, 0))})
		if err != nil {
			w.logger.Warn("heartbeat failed", "err", err)
			continue
		}
		w.mu.Lock()
		for _, id := range resp.GetStopRecordingIds() {
			if r := w.active[id]; r != nil {
				r.requestStop()
			}
		}
		w.mu.Unlock()
	}
}

// jobRun is one recording job.
type jobRun struct {
	w      *Worker
	job    *tesserav1.RecordingJob
	id     string
	req    *tesserav1.StartRecordingRequest
	cancel context.CancelFunc

	stopOnce sync.Once
	stopCh   chan struct{}

	mu       sync.Mutex
	tracks   map[string]*recTrack
	order    []string
	rejected []string // tracks that could not be recorded (e.g. VP8): the recording fails with these
	started  time.Time
	status   tesserav1.RecordingStatus
	st       storage.Storage
	prefix   string
	origin   time.Time
}

type recTrack struct {
	rec    *trackRecorder
	sink   *storageSink
	ident  string
	done   chan struct{}
	cancel context.CancelFunc
	err    error
	prefix string
}

func newJobRun(w *Worker, j *tesserav1.RecordingJob) *jobRun {
	return &jobRun{
		w: w, job: j, id: j.GetRecording().GetRecordingId(), req: j.GetRecording().GetRequest(),
		stopCh: make(chan struct{}), tracks: map[string]*recTrack{},
		status: tesserav1.RecordingStatus_RECORDING_STATUS_STARTING, started: time.Now(), origin: time.Now(),
	}
}

func (r *jobRun) requestStop() { r.stopOnce.Do(func() { close(r.stopCh) }) }

const trackWaitTimeout = 30 * time.Second

func (r *jobRun) matches(t Track) bool {
	switch tg := r.req.GetTarget().(type) {
	case *tesserav1.StartRecordingRequest_Track:
		return tg.Track.GetTrackSid() == t.SID
	case *tesserav1.StartRecordingRequest_Participant:
		if tg.Participant.GetIdentity() != t.ParticipantIdentity {
			return false
		}
		srcs := tg.Participant.GetSources()
		if len(srcs) == 0 {
			return t.Source == "camera" || t.Source == "microphone" || t.Source == ""
		}
		for _, s := range srcs {
			if strings.EqualFold(s, t.Source) {
				return true
			}
		}
	}
	return false
}

func expandPrefix(tmpl string, room, id string, now time.Time) string {
	if tmpl == "" {
		tmpl = "{room}/{recording_id}"
	}
	rep := strings.NewReplacer("{room}", room, "{recording_id}", id, "{time}", now.UTC().Format("20060102T150405Z"))
	p := rep.Replace(tmpl)
	return strings.Trim(path.Clean("/"+p), "/")
}

func (r *jobRun) update(ctx context.Context, st tesserav1.RecordingStatus, errMsg string, outs []*tesserav1.RecordingOutput, master string) {
	r.mu.Lock()
	r.status = st
	r.mu.Unlock()
	if terminal(st) {
		r.w.stats.markStatus(r.id, st, errMsg)
	}
	req := &tesserav1.UpdateJobRequest{
		WorkerId: r.w.cfg.WorkerID, RecordingId: r.id, Status: st, Error: errMsg, Outputs: outs, MasterPlaylistLocation: master,
	}
	// The final status is retried: if it never arrives the server marks a finished
	// recording failed ("worker lost") once heartbeats stop.
	attempts := 1
	if terminal(st) {
		attempts = 3
	}
	for i := range attempts {
		if i > 0 {
			time.Sleep(time.Duration(i) * 2 * time.Second)
		}
		uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		err := r.w.api.UpdateJob(uctx, req)
		cancel()
		if err == nil {
			return
		}
		r.w.logger.Warn("update job failed", "recording", r.id, "attempt", i+1, "err", err)
	}
}

func (r *jobRun) execute(ctx context.Context) {
	w := r.w
	rec := r.job.GetRecording()
	w.stats.jobStarted(r)
	defer func() { w.stats.jobFinished(r) }()

	var st storage.Storage
	var err error
	if loc := r.req.GetStorage().GetLocal(); loc == nil {
		st, err = storage.FromOptions(ctx, r.req.GetStorage(), w.defStorage)
	} else {
		// Per-request local directories live below the configured root.
		dir := path.Clean("/" + loc.GetDirectory())
		var opts []storage.LocalOption
		if base := w.cfg.Storage.LocalBaseURL; base != "" {
			opts = append(opts, storage.WithBaseURL(strings.TrimRight(base, "/")+dir))
		}
		st, err = storage.NewLocal(path.Join(w.cfg.Storage.LocalRoot, dir), opts...)
	}
	if err != nil {
		r.update(ctx, tesserav1.RecordingStatus_RECORDING_STATUS_FAILED, "storage: "+err.Error(), nil, "")
		return
	}
	r.st = st
	r.prefix = expandPrefix(r.req.GetFilenamePrefix(), rec.GetRoom(), r.id, r.started)

	closed := make(chan error, 1)
	room, err := w.conn.Join(ctx, r.job.GetWsUrl(), r.job.GetToken(), r.matches, r.onTrack(ctx), func(err error) {
		select {
		case closed <- err:
		default:
		}
	})
	if err != nil {
		r.update(ctx, tesserav1.RecordingStatus_RECORDING_STATUS_FAILED, "join: "+err.Error(), nil, "")
		return
	}

	activeSent := false
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	deadline := time.After(trackWaitTimeout)
	var failure string
wait:
	for {
		select {
		case <-ctx.Done():
			break wait
		case <-r.stopCh:
			break wait
		case err := <-closed:
			if err != nil {
				failure = "disconnected: " + err.Error()
			}
			break wait
		case <-deadline:
			// A participant may join with camera and microphone off and turn them on later;
			// their recording waits until they publish or leave (ParticipantLeft stops it).
			if r.trackCount() == 0 && r.req.GetParticipant() == nil {
				failure = "no matching track appeared within " + trackWaitTimeout.String()
				break wait
			}
		case <-tick.C:
			if !activeSent && r.anyStarted() {
				activeSent = true
				r.update(ctx, tesserav1.RecordingStatus_RECORDING_STATUS_ACTIVE, "", nil, "")
			}
			if r.allTracksEnded() {
				break wait
			}
		}
	}
	r.update(ctx, tesserav1.RecordingStatus_RECORDING_STATUS_ENDING, "", nil, "")
	lctx, lcancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	_ = room.Close(lctx)
	lcancel()
	outs, master, ferr := r.finalize(ctx)
	switch {
	case failure != "":
		r.update(ctx, tesserav1.RecordingStatus_RECORDING_STATUS_FAILED, failure, outs, master)
	case ferr != nil:
		r.update(ctx, tesserav1.RecordingStatus_RECORDING_STATUS_FAILED, ferr.Error(), outs, master)
	case !activeSent && !r.anyStarted():
		r.update(ctx, tesserav1.RecordingStatus_RECORDING_STATUS_ABORTED, "", outs, master)
	default:
		r.update(ctx, tesserav1.RecordingStatus_RECORDING_STATUS_COMPLETE, "", outs, master)
	}
}

func (r *jobRun) trackCount() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.tracks) }

func (r *jobRun) anyStarted() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range r.tracks {
		select {
		case <-t.rec.started:
			return true
		default:
		}
	}
	return false
}

func (r *jobRun) allTracksEnded() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.tracks) == 0 {
		return false
	}
	for _, t := range r.tracks {
		select {
		case <-t.done:
		default:
			return false
		}
	}
	// A participant recording may still get more tracks; only a single-track target ends with its track.
	return r.req.GetTrack() != nil
}

func (r *jobRun) onTrack(ctx context.Context) func(Track) {
	return func(t Track) {
		if !r.matches(t) {
			return
		}
		r.mu.Lock()
		if _, dup := r.tracks[t.SID]; dup {
			r.mu.Unlock()
			return
		}
		prefix := path.Join(r.prefix, t.SID)
		sink := newStorageSink(ctx, r.st, prefix)
		sink.onSegmentDelay = r.w.stats.observeSegmentDelay
		tr, err := newTrackRecorder(t, sink, trackOptions{
			segment: r.segmentDuration(), live: r.req.GetHls().GetPlaylistType() != tesserav1.PlaylistType_PLAYLIST_TYPE_VOD, origin: r.origin,
			aac: r.wantAAC(), ffmpeg: r.w.cfg.FFmpegPath, reserve: r.w.reserve,
		}, r.w.logger)
		if err != nil {
			r.rejected = append(r.rejected, t.SID+": "+err.Error())
			r.mu.Unlock()
			r.w.logger.Warn("track not recorded", "track", t.SID, "err", err)
			r.w.stats.trackErrors.Add(1)
			go sink.Drain(ctx) //nolint:errcheck
			return
		}
		tctx, tcancel := context.WithCancel(ctx)
		rt := &recTrack{rec: tr, sink: sink, ident: t.ParticipantIdentity, done: make(chan struct{}), prefix: prefix, cancel: tcancel}
		r.tracks[t.SID] = rt
		r.order = append(r.order, t.SID)
		r.mu.Unlock()
		go func() {
			defer close(rt.done)
			defer tcancel()
			rt.err = tr.run(tctx)
			if rt.err != nil {
				r.w.stats.trackErrors.Add(1)
				r.w.logger.Warn("track recording failed", "track", t.SID, "err", rt.err)
			}
		}()
	}
}

func (r *jobRun) wantAAC() bool {
	switch r.req.GetHls().GetAudioCodec() {
	case tesserav1.AudioCodec_AUDIO_CODEC_AAC:
		return true
	case tesserav1.AudioCodec_AUDIO_CODEC_OPUS:
		return false
	}
	return strings.EqualFold(r.w.cfg.HLS.AudioCodec, "aac")
}

// reserve takes extra capacity units (transcoding costs more than remuxing).
func (w *Worker) reserve(n int) func() {
	w.mu.Lock()
	w.used += n
	w.mu.Unlock()
	return func() {
		w.mu.Lock()
		w.used -= n
		w.mu.Unlock()
	}
}

func (r *jobRun) segmentDuration() time.Duration {
	d := time.Duration(r.req.GetHls().GetSegmentDurationSeconds()) * time.Second
	if d == 0 {
		d = r.w.cfg.HLS.SegmentDuration
	}
	if d == 0 {
		d = 4 * time.Second
	}
	return min(max(d, 2*time.Second), 6*time.Second)
}

// finalize stops all tracks, drains uploads and writes the master playlist.
func (r *jobRun) finalize(ctx context.Context) ([]*tesserav1.RecordingOutput, string, error) {
	r.mu.Lock()
	order := append([]string(nil), r.order...)
	tracks := make(map[string]*recTrack, len(r.tracks))
	for k, v := range r.tracks {
		tracks[k] = v
	}
	errMsgs := append([]string(nil), r.rejected...)
	r.mu.Unlock()

	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 60*time.Second)
	defer cancel()
	var outs []*tesserav1.RecordingOutput
	var rends []hls.Rendition
	for _, sid := range order {
		t := tracks[sid]
		// Ask the track to flush: its source ends when the room is closed;
		// wait briefly, then force it.
		select {
		case <-t.done:
		case <-time.After(time.Second):
			t.cancel()
			select {
			case <-t.done:
			case <-fctx.Done(): // stuck (FFmpeg or storage): report it, do not hang the job
				errMsgs = append(errMsgs, sid+": did not finish in time")
				continue
			}
		}
		if err := t.sink.Drain(fctx); err != nil {
			errMsgs = append(errMsgs, err.Error())
		}
		if t.err != nil {
			errMsgs = append(errMsgs, sid+": "+t.err.Error())
		}
		key, where := t.sink.playlist()
		dur := time.Duration(t.sink.durNs.Load())
		out := &tesserav1.RecordingOutput{
			TrackSid: sid, ParticipantIdentity: t.ident, PlaylistLocation: where, PlaylistUrl: r.st.URL(key),
			Codecs: t.rec.codecString, SegmentCount: uint32(t.sink.segs.Load()), DurationMs: dur.Milliseconds(), SizeBytes: t.sink.bytes.Load(),
		}
		outs = append(outs, out)
		if where == "" {
			continue
		}
		bw := 128000
		if dur > 0 {
			bw = int(float64(t.sink.bytes.Load()*8) / dur.Seconds())
		}
		rends = append(rends, hls.Rendition{
			URI: sid + "/index.m3u8", Codecs: t.rec.codecString, Bandwidth: max(bw, 1), Width: int(t.rec.stats.width.Load()),
			Height: int(t.rec.stats.height.Load()), IsAudio: t.rec.codec == "Opus", Name: t.ident + "-" + sid,
		})
	}
	var master string
	if len(rends) > 1 || r.req.GetParticipant() != nil {
		if b, err := hls.MasterPlaylist(rends); err == nil && len(rends) > 0 {
			loc, err := r.st.Put(fctx, path.Join(r.prefix, "master.m3u8"), strings.NewReader(string(b)), int64(len(b)), storage.ContentTypePlaylist)
			if err != nil {
				errMsgs = append(errMsgs, "master playlist: "+err.Error())
			} else {
				master = loc
			}
		}
	}
	r.w.stats.recordFinished(r, outs, master)
	if len(errMsgs) > 0 {
		return outs, master, errors.New(strings.Join(errMsgs, "; "))
	}
	return outs, master, nil
}
