package recorder

import (
	"context"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
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

func (w *Worker) journalDir() string { return filepath.Join(w.cfg.TempDir, "journal") }

// writeFileAtomic replaces name with data (rename, so a crash never leaves half a file).
func writeFileAtomic(name string, data []byte) error {
	tmp := name + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, name)
}

// startJournal records the job; it returns the entry's directory, "" when not journaled.
func (r *jobRun) startJournal() string {
	if r.req.GetStorage().GetS3() != nil {
		return ""
	}
	dir := filepath.Join(r.w.journalDir(), r.id)
	b, err := protojson.Marshal(r.job.GetRecording())
	if err == nil {
		err = os.MkdirAll(dir, 0o700)
	}
	if err == nil {
		err = writeFileAtomic(filepath.Join(dir, "job.json"), b)
	}
	if err == nil {
		err = writeFileAtomic(filepath.Join(dir, "pid"), []byte(strconv.Itoa(os.Getpid())))
	}
	if err != nil {
		r.w.logger.Warn("recording not journaled: a crash would leave it unclosed", "recording", r.id, "err", err)
		_ = os.RemoveAll(dir)
		return ""
	}
	return dir
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

// recoverJournal closes the recordings a previous run left open. Called once at start.
func (w *Worker) recoverJournal(ctx context.Context) {
	entries, _ := os.ReadDir(w.journalDir())
	for _, e := range entries {
		dir := filepath.Join(w.journalDir(), e.Name())
		if ownerAlive(dir) {
			continue // another server process on this host shares temp_dir and is recording
		}
		info, err := w.sealJournaled(ctx, dir)
		if err != nil {
			w.logger.Warn("could not close a recording left open by a previous run", "dir", dir, "err", err)
		} else {
			w.logger.Warn("closed a recording left open by a previous run", "recording", info.GetRecordingId(), "tracks", len(info.GetOutputs()))
			if rep, ok := w.api.(interface {
				RecordingRecovered(*tesserav1.RecordingInfo)
			}); ok {
				rep.RecordingRecovered(info)
			}
		}
		_ = os.RemoveAll(dir)
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

func (w *Worker) sealJournaled(ctx context.Context, dir string) (*tesserav1.RecordingInfo, error) {
	b, err := os.ReadFile(filepath.Join(dir, "job.json"))
	if err != nil {
		return nil, err
	}
	info := &tesserav1.RecordingInfo{}
	if err := protojson.Unmarshal(b, info); err != nil {
		return nil, err
	}
	st, err := w.storageFor(ctx, info.GetRequest())
	if err != nil {
		return nil, err
	}
	files, _ := filepath.Glob(filepath.Join(dir, "TR_*.json"))
	var rends []hls.Rendition
	prefix := ""
	for _, f := range files {
		var t journalTrack
		b, err := os.ReadFile(f)
		if err == nil {
			err = json.Unmarshal(b, &t)
		}
		if err != nil {
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
