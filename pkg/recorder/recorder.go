// Package recorder contains both halves of recording (ADR-0007):
//
//   - Coordinator, running inside tessera: job queue, worker registry,
//     recorder tokens, RecordingInfo state and recording webhooks. It
//     implements Service (consumed by the admin API) and WorkerAPI (served
//     by the admin API's Twirp router for RecorderWorkerService).
//   - Worker, running inside tessera-recorder: claims jobs, joins rooms with
//     pkg/client, feeds RTP to pkg/hls muxers and uploads with pkg/storage.
//
// Ownership: WP4.
package recorder

import (
	"context"
	"github.com/prometheus/client_golang/prometheus"
	"log/slog"
	"time"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/auth"
	"github.com/KeshavGoyal26/tessera/pkg/config"
	"github.com/KeshavGoyal26/tessera/pkg/room"
	"github.com/KeshavGoyal26/tessera/pkg/webhook"
)

// Service is the public recording API (RecordingService), consumed by pkg/admin.
type Service interface {
	StartRecording(ctx context.Context, req *tesserav1.StartRecordingRequest) (*tesserav1.RecordingInfo, error)
	StopRecording(ctx context.Context, recordingID string) (*tesserav1.RecordingInfo, error)
	ListRecordings(ctx context.Context, req *tesserav1.ListRecordingsRequest) ([]*tesserav1.RecordingInfo, error)
	GetRecording(ctx context.Context, recordingID string) (*tesserav1.RecordingInfo, error)
}

// WorkerAPI is the server side of RecorderWorkerService, consumed by pkg/admin.
type WorkerAPI interface {
	ClaimJob(ctx context.Context, req *tesserav1.ClaimJobRequest) (*tesserav1.ClaimJobResponse, error)
	UpdateJob(ctx context.Context, req *tesserav1.UpdateJobRequest) error
	Heartbeat(ctx context.Context, req *tesserav1.HeartbeatRequest) (*tesserav1.HeartbeatResponse, error)
}

// CoordinatorParams are the dependencies of the Coordinator.
type CoordinatorParams struct {
	Config   config.RecordingConfig
	Rooms    room.Admin
	Notifier webhook.Notifier
	// APIKey/Secret sign recorder access tokens.
	APIKey, Secret string
	Registry       prometheus.Registerer
	// Now overrides the clock in tests.
	Now    func() time.Time
	Logger *slog.Logger
}

// JoinGrant returns the grant used in recorder join tokens.
func JoinGrant(roomName string) *auth.RoomGrant {
	return &auth.RoomGrant{
		Room: roomName, RoomJoin: true, Hidden: true, Recorder: true,
		CanPublish: auth.Bool(false), CanPublishData: auth.Bool(false), CanSubscribe: auth.Bool(true),
	}
}

// Config is the tessera-recorder configuration (deploy/config/recorder.yaml).
type Config struct {
	Development bool `yaml:"development"`
	// ServerURL is the tessera HTTP base URL, e.g. "http://tessera:7880".
	ServerURL string `yaml:"server_url"`
	APIKey    string `yaml:"api_key"`
	Secret    string `yaml:"secret"`
	WorkerID  string `yaml:"worker_id"`
	// Capacity in units (1 unit = one remuxed track).
	Capacity int `yaml:"capacity"`
	// TempDir holds segments before upload.
	TempDir    string               `yaml:"temp_dir"`
	FFmpegPath string               `yaml:"ffmpeg_path"`
	Storage    StorageConfig        `yaml:"storage"`
	HLS        HLSDefaults          `yaml:"hls"`
	Metrics    config.MetricsConfig `yaml:"metrics"`
	Logging    config.LoggingConfig `yaml:"logging"`
	Heartbeat  time.Duration        `yaml:"heartbeat"`
}

// StorageConfig selects default storage.
type StorageConfig struct {
	// Kind: "local" or "s3".
	Kind      string `yaml:"kind"`
	LocalRoot string `yaml:"local_root"`
	S3        struct {
		Bucket         string `yaml:"bucket"`
		Region         string `yaml:"region"`
		Endpoint       string `yaml:"endpoint"`
		AccessKey      string `yaml:"access_key"`
		Secret         string `yaml:"secret"`
		ForcePathStyle bool   `yaml:"force_path_style"`
		Prefix         string `yaml:"prefix"`
	} `yaml:"s3"`
}

// HLSDefaults are applied when a request leaves options unset.
type HLSDefaults struct {
	SegmentDuration time.Duration `yaml:"segment_duration"`
	// AudioCodec: "opus" (default) or "aac".
	AudioCodec string `yaml:"audio_codec"`
}

// DefaultConfig returns recorder defaults.
func DefaultConfig() *Config {
	return &Config{
		ServerURL:  "http://localhost:7880",
		Capacity:   100,
		TempDir:    "/tmp/tessera-recorder",
		FFmpegPath: "ffmpeg",
		Storage:    StorageConfig{Kind: "local", LocalRoot: "./recordings"},
		HLS:        HLSDefaults{SegmentDuration: 4 * time.Second, AudioCodec: "opus"},
		Metrics:    config.MetricsConfig{Addr: ":6790"},
		Logging:    config.LoggingConfig{Level: "info"},
		Heartbeat:  5 * time.Second,
	}
}
