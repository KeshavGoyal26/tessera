package recorder

import (
	"net/http"
	"strings"

	"github.com/KeshavGoyal26/tessera/pkg/config"
)

// InProcessConfig is the worker configuration for recording inside the media server
// (recording.in_process): it has no listener or server URL of its own.
func InProcessConfig(rc config.RecordingConfig, development bool) *Config {
	c := DefaultConfig()
	c.Development = development
	c.WorkerID = "in-process"
	if rc.Capacity > 0 {
		c.Capacity = rc.Capacity
	}
	if rc.TempDir != "" {
		c.TempDir = rc.TempDir
	}
	if rc.FFmpegPath != "" {
		c.FFmpegPath = rc.FFmpegPath
	}
	if rc.Storage.Kind != "" {
		c.Storage = rc.Storage
	}
	c.FilenamePrefix = rc.FilenamePrefix
	if rc.HLS.SegmentDuration > 0 {
		c.HLS.SegmentDuration = rc.HLS.SegmentDuration
	}
	if rc.HLS.AudioCodec != "" {
		c.HLS.AudioCodec = rc.HLS.AudioCodec
	}
	c.Metrics = config.MetricsConfig{}
	return c
}

// RecordingsHandler serves the local recordings directory read-only, without directory
// listings, with CORS so the dashboard (another origin) can fetch playlists and segments.
func RecordingsHandler(root string) http.Handler {
	files := http.StripPrefix("/recordings/", http.FileServer(http.Dir(root)))
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(rw, r)
			return
		}
		rw.Header().Set("Access-Control-Allow-Origin", "*")
		switch {
		case strings.HasSuffix(r.URL.Path, ".m3u8"):
			rw.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			rw.Header().Set("Cache-Control", "no-cache")
		case strings.HasSuffix(r.URL.Path, ".m4s"):
			rw.Header().Set("Content-Type", "video/iso.segment")
		}
		files.ServeHTTP(rw, r)
	})
}
