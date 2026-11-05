package recorder

import (
	"encoding/json"
	"net/http"
	"path"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
)

// Stats is the JSON served at /stats by tessera-recorder (docs/STATS.md,
// docs/RECORDING.md). Fields are only added, never renamed.
type Stats struct {
	Time             time.Time        `json:"time"`
	WorkerID         string           `json:"worker_id"`
	Capacity         int              `json:"capacity"`
	CapacityUsed     int              `json:"capacity_used"`
	ActiveRecordings int              `json:"active_recordings"`
	TracksRecording  int              `json:"tracks_recording"`
	SegmentsWritten  uint64           `json:"segments_written"`
	BytesUploaded    uint64           `json:"bytes_uploaded"`
	UploadBacklog    int64            `json:"upload_backlog"`
	UploadErrors     uint64           `json:"upload_errors"`
	TrackErrors      uint64           `json:"track_errors"`
	ClaimErrors      uint64           `json:"claim_errors"`
	SegmentDelayMs   int64            `json:"segment_delay_ms"`
	SegmentDelayMax  int64            `json:"segment_delay_max_ms"`
	FinishedTotal    uint64           `json:"finished_total"`
	Recordings       []RecordingStats `json:"recordings"`
	Finished         []FinishedStats  `json:"finished"`
}

// RecordingStats describes an active recording.
type RecordingStats struct {
	RecordingID string       `json:"recording_id"`
	Room        string       `json:"room"`
	Status      string       `json:"status"`
	StartedAt   time.Time    `json:"started_at"`
	DurationMs  int64        `json:"duration_ms"`
	Tracks      []TrackStats `json:"tracks"`
}

// TrackStats describes one track of an active recording.
type TrackStats struct {
	TrackSID        string `json:"track_sid"`
	Participant     string `json:"participant"`
	Kind            string `json:"kind"`
	Codec           string `json:"codec"`
	Width           uint32 `json:"width,omitempty"`
	Height          uint32 `json:"height,omitempty"`
	Packets         uint64 `json:"packets"`
	Bytes           uint64 `json:"bytes"`
	PacketsLost     uint64 `json:"packets_lost"`
	PacketsLate     uint64 `json:"packets_late"`
	DroppedFrames   uint64 `json:"dropped_frames"`
	KeyframeReqs    uint64 `json:"keyframe_requests"`
	Segments        int64  `json:"segments"`
	DurationMs      int64  `json:"duration_ms"`
	UploadBacklog   int64  `json:"upload_backlog"`
	UploadErrors    int64  `json:"upload_errors"`
	LastPacketAgeMs int64  `json:"last_packet_age_ms"`
	Error           string `json:"error,omitempty"`
}

// FinishedStats describes a finished recording and where to play it.
type FinishedStats struct {
	RecordingID    string    `json:"recording_id"`
	Room           string    `json:"room"`
	Status         string    `json:"status"`
	Error          string    `json:"error,omitempty"`
	EndedAt        time.Time `json:"ended_at"`
	DurationMs     int64     `json:"duration_ms"`
	MasterPlaylist string    `json:"master_playlist,omitempty"`
	// MasterURL plays all tracks together (video with its audio), when storage has URLs.
	MasterURL string           `json:"master_url,omitempty"`
	Outputs   []FinishedOutput `json:"outputs"`
}

// FinishedOutput is one playlist of a finished recording.
type FinishedOutput struct {
	TrackSID     string `json:"track_sid"`
	Participant  string `json:"participant"`
	PlaylistURL  string `json:"playlist_url,omitempty"`
	PlaylistPath string `json:"playlist_location"`
	Segments     uint32 `json:"segments"`
	DurationMs   int64  `json:"duration_ms"`
}

const maxFinished = 50

type workerStats struct {
	w           *Worker
	claimErrors atomic.Uint64
	trackErrors atomic.Uint64
	delayLast   atomic.Int64
	delayMax    atomic.Int64
	delayHist   prometheus.Histogram

	mu            sync.Mutex
	finished      []FinishedStats
	finishedTotal uint64
	byStatus      map[string]uint64
	doneSegs      uint64
	doneBytes     uint64
	doneErrors    uint64
}

func newWorkerStats(w *Worker) *workerStats {
	return &workerStats{w: w, byStatus: map[string]uint64{}, delayHist: prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: "tessera", Subsystem: "recorder", Name: "segment_delay_seconds",
		Help:    "Time from the end of a segment's media to its upload completing.",
		Buckets: []float64{0.5, 1, 2, 4, 8, 16, 32, 64},
	})}
}

func (s *workerStats) observeSegmentDelay(d time.Duration) {
	ms := d.Milliseconds()
	s.delayLast.Store(ms)
	for {
		cur := s.delayMax.Load()
		if ms <= cur || s.delayMax.CompareAndSwap(cur, ms) {
			break
		}
	}
	s.delayHist.Observe(d.Seconds())
}

func (s *workerStats) jobStarted(*jobRun)  {}
func (s *workerStats) jobFinished(*jobRun) {}

func (s *workerStats) recordFinished(r *jobRun, outs []*tesserav1.RecordingOutput, master string) {
	f := FinishedStats{RecordingID: r.id, Room: r.job.GetRecording().GetRoom(), EndedAt: time.Now(), MasterPlaylist: master,
		DurationMs: time.Since(r.started).Milliseconds()}
	if master != "" && r.st != nil {
		f.MasterURL = r.st.URL(path.Join(r.prefix, "master.m3u8"))
	}
	var segs uint64
	var bytes uint64
	for _, o := range outs {
		f.Outputs = append(f.Outputs, FinishedOutput{TrackSID: o.TrackSid, Participant: o.ParticipantIdentity, PlaylistURL: o.PlaylistUrl,
			PlaylistPath: o.PlaylistLocation, Segments: o.SegmentCount, DurationMs: o.DurationMs})
		segs += uint64(o.SegmentCount)
		bytes += uint64(o.SizeBytes)
	}
	r.mu.Lock()
	var errs uint64
	for _, t := range r.tracks {
		errs += uint64(t.sink.errors.Load())
	}
	r.mu.Unlock()
	s.mu.Lock()
	s.finished = append(s.finished, f)
	if len(s.finished) > maxFinished {
		s.finished = s.finished[1:]
	}
	s.finishedTotal++
	s.doneSegs += segs
	s.doneBytes += bytes
	s.doneErrors += errs
	s.mu.Unlock()
}

// markStatus is called with the final status once known.
func (s *workerStats) markStatus(id string, st tesserav1.RecordingStatus, errMsg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.finished {
		if s.finished[i].RecordingID == id {
			s.finished[i].Status, s.finished[i].Error = st.String(), errMsg
		}
	}
	s.byStatus[st.String()]++
}

// Stats returns a snapshot.
func (w *Worker) Stats() Stats {
	now := time.Now()
	w.mu.Lock()
	runs := make([]*jobRun, 0, len(w.active))
	for _, r := range w.active {
		runs = append(runs, r)
	}
	used := w.used
	w.mu.Unlock()

	s := w.stats
	st := Stats{Time: now, WorkerID: w.cfg.WorkerID, Capacity: w.cfg.Capacity, CapacityUsed: used, ActiveRecordings: len(runs),
		TrackErrors: s.trackErrors.Load(), ClaimErrors: s.claimErrors.Load(), SegmentDelayMs: s.delayLast.Load(), SegmentDelayMax: s.delayMax.Load(),
		Recordings: []RecordingStats{}}
	for _, r := range runs {
		rs := RecordingStats{RecordingID: r.id, Room: r.job.GetRecording().GetRoom(), Status: r.statusString(), StartedAt: r.started,
			DurationMs: now.Sub(r.started).Milliseconds(), Tracks: []TrackStats{}}
		r.mu.Lock()
		for _, sid := range r.order {
			t := r.tracks[sid]
			ts := TrackStats{TrackSID: sid, Participant: t.ident, Kind: t.rec.tr.Kind, Codec: t.rec.codec,
				Width: t.rec.stats.width.Load(), Height: t.rec.stats.height.Load(),
				Packets: t.rec.stats.packets.Load(), Bytes: t.rec.stats.bytes.Load(), PacketsLost: t.rec.stats.lost.Load(),
				PacketsLate: t.rec.stats.late.Load(), DroppedFrames: t.rec.stats.dropped.Load(), KeyframeReqs: t.rec.stats.keyframeReqs.Load(),
				Segments: t.sink.segs.Load(), DurationMs: time.Duration(t.sink.durNs.Load()).Milliseconds(),
				UploadBacklog: t.sink.backlog.Load(), UploadErrors: t.sink.errors.Load()}
			if ns := t.rec.stats.lastPacketUnixNano.Load(); ns > 0 {
				ts.LastPacketAgeMs = now.Sub(time.Unix(0, ns)).Milliseconds()
			}
			if e, ok := t.rec.stats.err.Load().(string); ok {
				ts.Error = e
			}
			st.TracksRecording++
			st.SegmentsWritten += uint64(ts.Segments)
			st.BytesUploaded += uint64(t.sink.bytes.Load())
			st.UploadBacklog += ts.UploadBacklog
			st.UploadErrors += uint64(ts.UploadErrors)
			rs.Tracks = append(rs.Tracks, ts)
		}
		r.mu.Unlock()
		st.Recordings = append(st.Recordings, rs)
	}
	s.mu.Lock()
	st.SegmentsWritten += s.doneSegs
	st.BytesUploaded += s.doneBytes
	st.UploadErrors += s.doneErrors
	st.FinishedTotal = s.finishedTotal
	st.Finished = append([]FinishedStats{}, s.finished...)
	s.mu.Unlock()
	return st
}

func (r *jobRun) statusString() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status.String()
}

// StatsHandler serves Stats as JSON.
func (w *Worker) StatsHandler() http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		rw.Header().Set("Access-Control-Allow-Origin", "*")
		_ = json.NewEncoder(rw).Encode(w.Stats())
	})
}

type collector struct{ w *Worker }

var (
	descActive  = prometheus.NewDesc("tessera_recorder_active_recordings", "Recordings currently running.", nil, nil)
	descTracks  = prometheus.NewDesc("tessera_recorder_tracks_recording", "Tracks currently being recorded.", nil, nil)
	descDur     = prometheus.NewDesc("tessera_recorder_longest_recording_seconds", "Duration of the longest active recording.", nil, nil)
	descSegs    = prometheus.NewDesc("tessera_recorder_segments_total", "Segments written.", nil, nil)
	descBytes   = prometheus.NewDesc("tessera_recorder_uploaded_bytes_total", "Bytes uploaded to storage.", nil, nil)
	descBacklog = prometheus.NewDesc("tessera_recorder_upload_backlog", "Objects queued for upload.", nil, nil)
	descErrors  = prometheus.NewDesc("tessera_recorder_errors_total", "Recorder errors.", []string{"type"}, nil)
	descDone    = prometheus.NewDesc("tessera_recorder_finished_recordings_total", "Finished recordings.", []string{"status"}, nil)
	descCap     = prometheus.NewDesc("tessera_recorder_capacity_used_ratio", "Used capacity units / capacity.", nil, nil)
	descLost    = prometheus.NewDesc("tessera_recorder_rtp_packets_lost", "RTP packets given up on across active tracks.", nil, nil)
)

func (collector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{descActive, descTracks, descDur, descSegs, descBytes, descBacklog, descErrors, descDone, descCap, descLost} {
		ch <- d
	}
}

func (c collector) Collect(ch chan<- prometheus.Metric) {
	st := c.w.Stats()
	g := prometheus.GaugeValue
	cv := prometheus.CounterValue
	var longest int64
	var lost uint64
	for _, r := range st.Recordings {
		longest = max(longest, r.DurationMs)
		for _, t := range r.Tracks {
			lost += t.PacketsLost
		}
	}
	ch <- prometheus.MustNewConstMetric(descActive, g, float64(st.ActiveRecordings))
	ch <- prometheus.MustNewConstMetric(descTracks, g, float64(st.TracksRecording))
	ch <- prometheus.MustNewConstMetric(descDur, g, float64(longest)/1000)
	ch <- prometheus.MustNewConstMetric(descSegs, cv, float64(st.SegmentsWritten))
	ch <- prometheus.MustNewConstMetric(descBytes, cv, float64(st.BytesUploaded))
	ch <- prometheus.MustNewConstMetric(descBacklog, g, float64(st.UploadBacklog))
	ch <- prometheus.MustNewConstMetric(descErrors, cv, float64(st.UploadErrors), "upload")
	ch <- prometheus.MustNewConstMetric(descErrors, cv, float64(st.TrackErrors), "track")
	ch <- prometheus.MustNewConstMetric(descErrors, cv, float64(st.ClaimErrors), "claim")
	ch <- prometheus.MustNewConstMetric(descLost, g, float64(lost))
	if st.Capacity > 0 {
		ch <- prometheus.MustNewConstMetric(descCap, g, float64(st.CapacityUsed)/float64(st.Capacity))
	}
	c.w.stats.mu.Lock()
	for status, n := range c.w.stats.byStatus {
		ch <- prometheus.MustNewConstMetric(descDone, cv, float64(n), status)
	}
	c.w.stats.mu.Unlock()
}

func (s *workerStats) register(reg prometheus.Registerer) {
	reg.MustRegister(collector{s.w}, s.delayHist)
}
