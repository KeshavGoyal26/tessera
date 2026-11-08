package recorder

import (
	"context"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/hls"
	"github.com/KeshavGoyal26/tessera/pkg/storage"
)

// Crash journal. While a recording runs, the worker keeps in temp_dir the last playlist
// it uploaded for each track and what the master playlist needs; a recording that ends
// normally removes its entry. After a crash (kill -9, OOM, power loss) the playlists in
// storage stay live (EVENT, no ENDLIST) and there is no master: the next start finds the
// entries left behind, closes the playlists, writes the master and reports the recording
// as ended (FAILED, with its outputs). Storage is never listed, so S3 works the same as
// local disk. Recordings with per-request S3 storage are not journaled: their
// credentials are not written to disk.

// journalRecoverEvery is how often a running worker looks for orphaned recordings.
var journalRecoverEvery = 30 * time.Second

// errCrashed is the error of a recording sealed after a crash.
const errCrashed = "the server stopped while recording; the recording was closed at the next start (up to the last uploaded segment)"

type journalTrack struct {
	SID      string `json:"sid"`
	Ident    string `json:"ident"`
	Codecs   string `json:"codecs"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	Audio    bool   `json:"audio"`
	Bytes    int64  `json:"bytes"`
	DurMs    int64  `json:"dur_ms"`
	Segments int64  `json:"segments"`
	Key      string `json:"key"`
	Playlist string `json:"playlist"`
}

// JournalStore keeps the crash journal. The default (FileJournal) stores it under
// temp_dir/journal and closes the recordings of a previous run of this machine; a
// cluster with shared storage (S3) can store it in Redis instead, so any media node
// closes the recordings of a node that is gone (ADR-0012).
type JournalStore interface {
	// Begin records a recording's job; the handle is passed to Put and End.
	Begin(id string, job []byte) (handle string, err error)
	// Put replaces one track's entry (the JSON of journalTrack).
	Put(handle, track string, data []byte) error
	// End removes the entry of a recording that ended normally.
	End(handle string)
	// Orphans returns entries whose owner is gone, each claimed for this caller.
	Orphans(ctx context.Context) ([]JournalEntry, error)
}

// JournalEntry is an orphaned recording to close.
type JournalEntry struct {
	ID     string
	Job    []byte   // protojson of RecordingInfo
	Tracks [][]byte // JSON of journalTrack
	// Done removes the entry (after it was closed, or could not be).
	Done func()
}

// WithJournal sets the crash journal store (default: files under temp_dir).
func WithJournal(j JournalStore) WorkerOption { return func(w *Worker) { w.journal = j } }

// FileJournal stores the journal under dir, one directory per recording; an entry is
// orphaned when the process that wrote it is no longer running.
type FileJournal struct{ Dir string }

// fileJournalLive holds the entries of recordings running in this process. The periodic
// recovery must skip them, and their pid file cannot tell: a restarted server often has
// the pid of the crashed one (pid 1 in a container), so a matching pid means "previous run".
var fileJournalLive sync.Map // handle (dir) -> struct{}

// Begin implements JournalStore.
func (f FileJournal) Begin(id string, job []byte) (string, error) {
	dir := filepath.Join(f.Dir, id)
	err := os.MkdirAll(dir, 0o700)
	if err == nil {
		err = writeFileAtomic(filepath.Join(dir, "job.json"), job)
	}
	if err == nil {
		err = writeFileAtomic(filepath.Join(dir, "pid"), []byte(strconv.Itoa(os.Getpid())))
	}
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	fileJournalLive.Store(dir, struct{}{})
	return dir, nil
}

// Put implements JournalStore.
func (FileJournal) Put(handle, track string, data []byte) error {
	return writeFileAtomic(filepath.Join(handle, track+".json"), data)
}

// End implements JournalStore.
func (FileJournal) End(handle string) {
	_ = os.RemoveAll(handle)
	fileJournalLive.Delete(handle)
}

// Orphans implements JournalStore.
func (f FileJournal) Orphans(context.Context) ([]JournalEntry, error) {
	entries, _ := os.ReadDir(f.Dir)
	var out []JournalEntry
	for _, e := range entries {
		dir := filepath.Join(f.Dir, e.Name())
		if _, live := fileJournalLive.Load(dir); live {
			continue // recording in this process
		}
		if ownerAlive(dir) {
			continue // another server process on this host shares temp_dir and is recording
		}
		job, _ := os.ReadFile(filepath.Join(dir, "job.json"))
		var tracks [][]byte
		files, _ := filepath.Glob(filepath.Join(dir, "TR_*.json"))
		for _, t := range files {
			if b, err := os.ReadFile(t); err == nil {
				tracks = append(tracks, b)
			}
		}
		out = append(out, JournalEntry{ID: e.Name(), Job: job, Tracks: tracks, Done: func() { _ = os.RemoveAll(dir) }})
	}
	return out, nil
}

// writeFileAtomic replaces name with data (rename, so a crash never leaves half a file).
func writeFileAtomic(name string, data []byte) error {
	tmp := name + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, name)
}

// startJournal records the job; it returns the entry's handle, "" when not journaled.
func (r *jobRun) startJournal() string {
	if r.req.GetStorage().GetS3() != nil {
		return ""
	}
	b, err := protojson.Marshal(r.job.GetRecording())
	var h string
	if err == nil {
		h, err = r.w.journal.Begin(r.id, b)
	}
	if err != nil {
		r.w.logger.Warn("recording not journaled: a crash would leave it unclosed", "recording", r.id, "err", err)
		return ""
	}
	return h
}

// rendition describes a recorded track in the master playlist.
func rendition(sid, ident, codecs string, width, height int, audio bool, bytes int64, dur time.Duration) hls.Rendition {
	bw := 128000
	if dur > 0 {
		bw = int(float64(bytes*8) / dur.Seconds())
	}
	return hls.Rendition{URI: sid + "/index.m3u8", Codecs: codecs, Bandwidth: max(bw, 1), Width: width, Height: height,
		IsAudio: audio, Name: ident + "-" + sid}
}

// sealPlaylist turns a live media playlist into the final one.
func sealPlaylist(p string) string {
	p = strings.Replace(p, "#EXT-X-PLAYLIST-TYPE:EVENT\n", "#EXT-X-PLAYLIST-TYPE:VOD\n", 1)
	if !strings.Contains(p, "#EXT-X-ENDLIST") {
		p += "#EXT-X-ENDLIST\n"
	}
	return p
}

// recoverJournal closes the recordings whose owner is gone (a previous run of this
// machine, or with a shared journal another node). Called at start and periodically.
func (w *Worker) recoverJournal(ctx context.Context) {
	entries, err := w.journal.Orphans(ctx)
	if err != nil {
		w.logger.Warn("crash journal unavailable", "err", err)
		return
	}
	for _, e := range entries {
		info, err := w.sealJournaled(ctx, e.Job, e.Tracks)
		if err != nil {
			w.logger.Warn("could not close a recording left open by a stopped server", "recording", e.ID, "err", err)
		} else {
			w.logger.Warn("closed a recording left open by a stopped server", "recording", info.GetRecordingId(), "tracks", len(info.GetOutputs()))
			if rep, ok := w.api.(interface {
				RecordingRecovered(*tesserav1.RecordingInfo)
			}); ok {
				rep.RecordingRecovered(info)
			}
		}
		e.Done()
	}
}

// ownerAlive reports whether the process that wrote the entry is another live process.
func ownerAlive(dir string) bool {
	b, err := os.ReadFile(filepath.Join(dir, "pid"))
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid == os.Getpid() {
		return false
	}
	p, err := os.FindProcess(pid)
	return err == nil && p.Signal(syscall.Signal(0)) == nil
}

func (w *Worker) sealJournaled(ctx context.Context, job []byte, tracks [][]byte) (*tesserav1.RecordingInfo, error) {
	info := &tesserav1.RecordingInfo{}
	if err := protojson.Unmarshal(job, info); err != nil {
		return nil, err
	}
	st, err := w.storageFor(ctx, info.GetRequest())
	if err != nil {
		return nil, err
	}
	var rends []hls.Rendition
	prefix := ""
	for _, b := range tracks {
		var t journalTrack
		if err := json.Unmarshal(b, &t); err != nil {
			return nil, err
		}
		pl := sealPlaylist(t.Playlist)
		loc, err := st.Put(ctx, t.Key, strings.NewReader(pl), int64(len(pl)), storage.ContentTypePlaylist)
		if err != nil {
			return nil, err
		}
		prefix = path.Dir(path.Dir(t.Key))
		info.Outputs = append(info.Outputs, &tesserav1.RecordingOutput{
			TrackSid: t.SID, ParticipantIdentity: t.Ident, PlaylistLocation: loc, PlaylistUrl: st.URL(t.Key),
			Codecs: t.Codecs, SegmentCount: uint32(t.Segments), DurationMs: t.DurMs, SizeBytes: t.Bytes,
		})
		rends = append(rends, rendition(t.SID, t.Ident, t.Codecs, t.Width, t.Height, t.Audio, t.Bytes, time.Duration(t.DurMs)*time.Millisecond))
	}
	if len(rends) > 1 || len(rends) == 1 && info.GetRequest().GetParticipant() != nil {
		if m, err := hls.MasterPlaylist(rends); err == nil {
			if loc, err := st.Put(ctx, path.Join(prefix, "master.m3u8"), strings.NewReader(string(m)), int64(len(m)), storage.ContentTypePlaylist); err == nil {
				info.MasterPlaylistLocation = loc
			}
		}
	}
	info.Status = tesserav1.RecordingStatus_RECORDING_STATUS_FAILED
	info.Error = errCrashed
	info.EndedAtMs = time.Now().UnixMilli()
	return info, nil
}
