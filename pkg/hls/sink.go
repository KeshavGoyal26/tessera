package hls

import (
	"context"
	"os"
	"path/filepath"
	"sync"
)

// DirSink writes muxer output to a local directory (used by tests and tools).
type DirSink struct {
	Dir string
	mu  sync.Mutex
}

func (d *DirSink) write(name string, data []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := os.MkdirAll(d.Dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(d.Dir, name), data, 0o644)
}

// WriteInit implements Sink.
func (d *DirSink) WriteInit(_ context.Context, name string, data []byte) error {
	return d.write(name, data)
}

// WriteSegment implements Sink.
func (d *DirSink) WriteSegment(_ context.Context, s Segment) error { return d.write(s.Name, s.Data) }

// WritePlaylist implements Sink.
func (d *DirSink) WritePlaylist(_ context.Context, name string, data []byte) error {
	return d.write(name, data)
}
