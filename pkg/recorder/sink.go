package recorder

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path"
	"sync"
	"sync/atomic"
	"time"

	"github.com/KeshavGoyal26/tessera/pkg/hls"
	"github.com/KeshavGoyal26/tessera/pkg/storage"
)

// storageSink implements hls.Sink on top of a storage.Storage. Uploads run in
// order on one goroutine, so a playlist is never visible before the segments
// it lists (ADR-0007). The queue is bounded: when storage is slower than the
// media, WriteX blocks and the track pipeline applies back-pressure.
type storageSink struct {
	st     storage.Storage
	prefix string // key prefix of this track, e.g. "room/RC_x/TR_y"

	ch      chan upload
	wg      sync.WaitGroup
	backlog atomic.Int64
	errors  atomic.Int64
	bytes   atomic.Int64
	segs    atomic.Int64
	durNs   atomic.Int64
	lastErr atomic.Value // string

	// onUploaded is called with the delay between segment end and upload completion.
	onSegmentDelay func(time.Duration)

	mu            sync.Mutex
	playlistKey   string
	playlistWhere string

	// Crash journal (journal.go): after each playlist upload, the playlist and meta()
	// are written to the file journal. Both unset = not journaled.
	journal string
	meta    func() journalTrack
}

type upload struct {
	key, ctype string
	data       []byte
	// segment bookkeeping
	isSegment bool
	endWall   time.Time
	dur       time.Duration
	isPlaylst bool
	meta      journalTrack
}

func newStorageSink(ctx context.Context, st storage.Storage, prefix string) *storageSink {
	s := &storageSink{st: st, prefix: prefix, ch: make(chan upload, 64)}
	s.wg.Add(1)
	go s.loop(ctx)
	return s
}

func (s *storageSink) loop(ctx context.Context) {
	defer s.wg.Done()
	for u := range s.ch {
		// Uploads continue after the job context is cancelled so finalization can drain.
		where, err := s.st.Put(context.WithoutCancel(ctx), u.key, bytes.NewReader(u.data), int64(len(u.data)), u.ctype)
		s.backlog.Add(-1)
		if err != nil {
			s.errors.Add(1)
			s.lastErr.Store(err.Error())
			continue
		}
		s.bytes.Add(int64(len(u.data)))
		if u.isSegment {
			s.segs.Add(1)
			s.durNs.Add(int64(u.dur))
			if s.onSegmentDelay != nil {
				s.onSegmentDelay(time.Since(u.endWall))
			}
		}
		if u.isPlaylst {
			s.mu.Lock()
			s.playlistKey, s.playlistWhere = u.key, where
			s.mu.Unlock()
			if s.journal != "" {
				m := u.meta
				m.Key, m.Playlist = u.key, string(u.data)
				m.Bytes, m.Segments, m.DurMs = s.bytes.Load(), s.segs.Load(), time.Duration(s.durNs.Load()).Milliseconds()
				if b, err := json.Marshal(m); err == nil {
					_ = writeFileAtomic(s.journal, b)
				}
			}
		}
	}
}

func (s *storageSink) enqueue(ctx context.Context, u upload) error {
	s.backlog.Add(1)
	select {
	case s.ch <- u:
		return nil
	case <-ctx.Done():
		s.backlog.Add(-1)
		return ctx.Err()
	}
}

// WriteInit implements hls.Sink.
func (s *storageSink) WriteInit(ctx context.Context, name string, data []byte) error {
	return s.enqueue(ctx, upload{key: path.Join(s.prefix, name), ctype: storage.ContentTypeInit, data: data})
}

// WriteSegment implements hls.Sink.
func (s *storageSink) WriteSegment(ctx context.Context, seg hls.Segment) error {
	return s.enqueue(ctx, upload{
		key: path.Join(s.prefix, seg.Name), ctype: storage.ContentTypeSegment, data: seg.Data,
		isSegment: true, endWall: seg.ProgramDateTime.Add(seg.Duration), dur: seg.Duration,
	})
}

// WritePlaylist implements hls.Sink.
func (s *storageSink) WritePlaylist(ctx context.Context, name string, data []byte) error {
	u := upload{key: path.Join(s.prefix, name), ctype: storage.ContentTypePlaylist, data: data, isPlaylst: true}
	if s.meta != nil {
		u.meta = s.meta()
	}
	return s.enqueue(ctx, u)
}

// Drain waits for all queued uploads and stops the uploader.
func (s *storageSink) Drain(ctx context.Context) error {
	close(s.ch)
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	if n := s.errors.Load(); n > 0 {
		msg, _ := s.lastErr.Load().(string)
		return errors.New(itoa(n) + " uploads failed: " + msg)
	}
	return nil
}

func (s *storageSink) playlist() (key, where string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.playlistKey, s.playlistWhere
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for ; n > 0; n /= 10 {
		i--
		b[i] = byte('0' + n%10)
	}
	return string(b[i:])
}
