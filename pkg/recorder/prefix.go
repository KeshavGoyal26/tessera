package recorder

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/KeshavGoyal26/tessera/pkg/storage"
)

const (
	defaultPrefix = "{room}/{recording_id}"
	indexFile     = "index.json"
)

// expandPrefix fills in an output prefix template and returns the prefix and, when
// the template names the presenter ({pid}), the key of their index.json ("" = none).
//
// Variables: {room}, {recording_id}, {time} (UTC 20060102T150405Z), {start_ms} (Unix
// milliseconds), {pid} (the recorded participant's identity) and {shard} (pid mod
// 1000 as three digits, spreading consecutive numeric pids over S3 prefixes; a hash
// for non-numeric ones). The index sits where the template's {pid} segment ends:
// "recordings/{shard}/{pid}/{start_ms}" lists recordings in
// "recordings/<shard>/<pid>/index.json". A template that needs a pid falls back to
// the default for recordings that have none (track recordings). A template without
// {recording_id}, {start_ms} or {time} gets "/{recording_id}" appended, so recordings
// never overwrite each other.
func expandPrefix(tmpl, room, id, pid string, now time.Time) (prefix, index string) {
	if tmpl == "" {
		tmpl = defaultPrefix
	}
	pid = safePID(pid)
	usesPID := strings.Contains(tmpl, "{pid}") || strings.Contains(tmpl, "{shard}")
	if usesPID && pid == "" {
		tmpl = defaultPrefix
	}
	if !strings.Contains(tmpl, "{recording_id}") && !strings.Contains(tmpl, "{start_ms}") && !strings.Contains(tmpl, "{time}") {
		// Nothing tells this recording from the next one of the same room or pid: they
		// would overwrite each other.
		tmpl += "/{recording_id}"
	}
	rep := strings.NewReplacer("{room}", room, "{recording_id}", id, "{time}", now.UTC().Format("20060102T150405Z"),
		"{start_ms}", fmt.Sprint(now.UnixMilli()), "{pid}", pid, "{shard}", shard(pid))
	clean := func(p string) string { return strings.Trim(path.Clean("/"+p), "/") }
	prefix = clean(rep.Replace(tmpl))
	if i := strings.Index(tmpl, "{pid}"); i >= 0 && pid != "" {
		index = path.Join(clean(rep.Replace(tmpl[:i+len("{pid}")])), indexFile)
	}
	return prefix, index
}

// safePID keeps a pid to one path segment: characters other than letters, digits,
// '-', '_' and '.' become '_', and "." / ".." are rejected.
func safePID(pid string) string {
	if len(pid) > 128 {
		pid = pid[:128]
	}
	b := []byte(pid)
	for i, c := range b {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.'
		if !ok {
			b[i] = '_'
		}
	}
	if s := string(b); s != "." && s != ".." {
		return s
	}
	return ""
}

// shard is pid mod 1000 as three digits ("1048576" -> "576"). Non-numeric pids use
// a hash, so every pid has a stable shard.
func shard(pid string) string {
	if pid == "" {
		return ""
	}
	numeric := true
	for i := 0; i < len(pid); i++ {
		numeric = numeric && pid[i] >= '0' && pid[i] <= '9'
	}
	if numeric {
		s := "00" + pid
		return s[len(s)-3:]
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(pid))
	return fmt.Sprintf("%03d", h.Sum32()%1000)
}

// recordingIndex is the content of a presenter's index.json: the master playlist of
// each of their recordings, oldest first.
type recordingIndex struct {
	Recordings []string `json:"recordings"`
}

// addToIndex lists master (a master playlist location) in the index.json at key.
// A presenter has one recording at a time and each recording adds itself once, so
// a plain read-modify-write is enough; the mutex covers this worker.
func (w *Worker) addToIndex(ctx context.Context, st storage.Storage, key, master string) error {
	w.indexMu.Lock()
	defer w.indexMu.Unlock()
	var idx recordingIndex
	b, err := st.Get(ctx, key)
	switch {
	case errors.Is(err, storage.ErrNotFound):
	case err != nil:
		return err
	default:
		if err := json.Unmarshal(b, &idx); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
	}
	if slices.Contains(idx.Recordings, master) {
		return nil
	}
	idx.Recordings = append(idx.Recordings, master)
	out, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	_, err = st.Put(ctx, key, bytes.NewReader(out), int64(len(out)), storage.ContentTypeJSON)
	return err
}
