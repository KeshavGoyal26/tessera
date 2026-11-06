// Package config loads and validates the tessera server configuration.
//
// Configuration comes from a YAML file (see deploy/config/tessera.yaml),
// with ${ENV} expansion, then a few environment variable overrides
// (TESSERA_KEYS, TESSERA_NODE_IP). Defaults are defined in Default.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Config is the tessera server configuration.
type Config struct {
	// Development relaxes checks (short secrets, generated dev keys) and uses text logs.
	Development bool   `yaml:"development"`
	NodeID      string `yaml:"node_id"`
	Region      string `yaml:"region"`
	// Port is the HTTP port for signaling (/rtc) and the admin API (/twirp).
	Port int `yaml:"port"`
	// Keys maps API keys to secrets. Never logged.
	Keys    map[string]string `yaml:"keys"`
	KeyFile string            `yaml:"key_file"`

	RTC       RTCConfig       `yaml:"rtc"`
	TURN      TURNConfig      `yaml:"turn"`
	Room      RoomConfig      `yaml:"room"`
	Signal    SignalConfig    `yaml:"signal"`
	Webhook   WebhookConfig   `yaml:"webhook"`
	Recording RecordingConfig `yaml:"recording"`
	Metrics   MetricsConfig   `yaml:"metrics"`
	Logging   LoggingConfig   `yaml:"logging"`
	Limits    LimitsConfig    `yaml:"limits"`
}

// RTCConfig configures ICE and the forwarding engine.
type RTCConfig struct {
	// UDPPort is the single UDP mux port for all media (ADR-0005).
	UDPPort int `yaml:"udp_port"`
	// TCPPort is the ICE-TCP passive port; 0 disables ICE-TCP.
	TCPPort int `yaml:"tcp_port"`
	// IPs restricts media sockets and host candidates to these local
	// addresses (empty = all interfaces). Tests use ["127.0.0.1"].
	IPs []string `yaml:"ips"`
	// NodeIP is advertised as the host candidate address (NAT 1:1).
	NodeIP string `yaml:"node_ip"`
	// ICEServers are extra STUN/TURN servers handed to clients.
	ICEServers []ICEServerConfig `yaml:"ice_servers"`
	// SocketBufferSize sets SO_RCVBUF/SO_SNDBUF on media sockets, bytes.
	SocketBufferSize int `yaml:"socket_buffer_size"`
	// PLIThrottle is the minimum interval between PLIs per layer.
	PLIThrottle       time.Duration           `yaml:"pli_throttle"`
	CongestionControl CongestionControlConfig `yaml:"congestion_control"`
	// EnabledCodecs are MIME types allowed by default, in preference order.
	EnabledCodecs []string `yaml:"enabled_codecs"`
}

// ICEServerConfig is an ICE server handed to clients.
type ICEServerConfig struct {
	URLs       []string `yaml:"urls"`
	Username   string   `yaml:"username"`
	Credential string   `yaml:"credential"`
}

// CongestionControlConfig configures BWE-driven allocation.
type CongestionControlConfig struct {
	Enabled bool `yaml:"enabled"`
	// AllowPause lets the allocator pause video that does not fit.
	AllowPause bool `yaml:"allow_pause"`
	// Probe enables padding probes to discover extra bandwidth.
	Probe bool `yaml:"probe"`
}

// TURNConfig configures the embedded TURN server.
type TURNConfig struct {
	Enabled bool   `yaml:"enabled"`
	Domain  string `yaml:"domain"`
	UDPPort int    `yaml:"udp_port"`
	TCPPort int    `yaml:"tcp_port"`
	// TLSPort serves TURN over TLS (TURNS); requires CertFile/KeyFile. 0 (the default)
	// disables it. Use 443 for networks that only allow HTTPS; the port needs privileges
	// (CAP_NET_BIND_SERVICE) and its own address when the HTTPS signaling also uses 443.
	TLSPort  int    `yaml:"tls_port"`
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
	// Secret for ephemeral credentials; defaults to a random value per process.
	Secret          string        `yaml:"secret"`
	CredentialTTL   time.Duration `yaml:"credential_ttl"`
	RelayRangeStart int           `yaml:"relay_range_start"`
	RelayRangeEnd   int           `yaml:"relay_range_end"`
}

// RoomConfig sets room defaults.
type RoomConfig struct {
	// AutoCreate creates a room on first join.
	AutoCreate       bool          `yaml:"auto_create"`
	EmptyTimeout     time.Duration `yaml:"empty_timeout"`
	DepartureTimeout time.Duration `yaml:"departure_timeout"`
	MaxParticipants  int           `yaml:"max_participants"`
}

// SignalConfig configures WebSocket sessions.
type SignalConfig struct {
	PingInterval   time.Duration `yaml:"ping_interval"`
	PingTimeout    time.Duration `yaml:"ping_timeout"`
	ResumeWindow   time.Duration `yaml:"resume_window"`
	WriteQueueSize int           `yaml:"write_queue_size"`
	MaxMessageSize int64         `yaml:"max_message_size"`
	// NegotiationDebounce batches subscriber renegotiations.
	NegotiationDebounce time.Duration `yaml:"negotiation_debounce"`
}

// WebhookConfig configures webhook delivery.
type WebhookConfig struct {
	// APIKey selects the secret used to sign webhooks.
	APIKey    string            `yaml:"api_key"`
	Endpoints []WebhookEndpoint `yaml:"endpoints"`
	Timeout   time.Duration     `yaml:"timeout"`
	QueueSize int               `yaml:"queue_size"`
}

// WebhookEndpoint is one webhook receiver.
type WebhookEndpoint struct {
	URL string `yaml:"url"`
	// Encoding is "json" (default) or "protobuf".
	Encoding string `yaml:"encoding"`
}

// RecordingConfig configures the in-server recording Coordinator.
type RecordingConfig struct {
	// Enabled runs the recorder inside this server (default true). It reads each
	// recorded track straight from the server's receive buffer: no separate process,
	// WebRTC session or loopback hop. Disable on media-only nodes.
	Enabled          bool          `yaml:"enabled"`
	HeartbeatTimeout time.Duration `yaml:"heartbeat_timeout"`

	Capacity   int              `yaml:"capacity"` // units; one remuxed track is one unit
	TempDir    string           `yaml:"temp_dir"`
	FFmpegPath string           `yaml:"ffmpeg_path"` // only for the optional AAC audio mode
	Storage    RecordingStorage `yaml:"storage"`
	HLS        RecordingHLS     `yaml:"hls"`
}

// RecordingStorage selects where recordings are written.
type RecordingStorage struct {
	// Kind: "local" or "s3".
	Kind      string `yaml:"kind"`
	LocalRoot string `yaml:"local_root"`
	// LocalBaseURL, when set, serves LocalRoot read-only under /recordings/ and reports
	// playlist URLs below this prefix, so the dashboard can play local recordings.
	LocalBaseURL string `yaml:"local_base_url"`
	S3           struct {
		Bucket         string `yaml:"bucket"`
		Region         string `yaml:"region"`
		Endpoint       string `yaml:"endpoint"`
		AccessKey      string `yaml:"access_key"`
		Secret         string `yaml:"secret"`
		ForcePathStyle bool   `yaml:"force_path_style"`
		Prefix         string `yaml:"prefix"`
	} `yaml:"s3"`
}

// RecordingHLS are applied when a recording request leaves options unset.
type RecordingHLS struct {
	SegmentDuration time.Duration `yaml:"segment_duration"`
	// AudioCodec: "opus" (default) or "aac".
	AudioCodec string `yaml:"audio_codec"`
}

// MetricsConfig configures the metrics/pprof/health listener.
type MetricsConfig struct {
	// Addr, e.g. ":6789". Empty disables the listener.
	Addr  string `yaml:"addr"`
	PProf bool   `yaml:"pprof"`
}

// LoggingConfig configures log/slog.
type LoggingConfig struct {
	// Level: debug, info, warn, error.
	Level string `yaml:"level"`
	// JSON selects the JSON handler (default true unless Development).
	JSON *bool `yaml:"json"`
}

// LimitsConfig caps resource usage.
type LimitsConfig struct {
	MaxRooms        int `yaml:"max_rooms"`
	MaxParticipants int `yaml:"max_participants"`
}

// Default returns the default configuration.
func Default() *Config {
	return &Config{
		Port: 7880,
		RTC: RTCConfig{
			UDPPort:           7882,
			TCPPort:           7881,
			SocketBufferSize:  25 << 20,
			PLIThrottle:       500 * time.Millisecond,
			CongestionControl: CongestionControlConfig{Enabled: true, AllowPause: true, Probe: true},
			EnabledCodecs:     []string{"audio/opus", "audio/red", "video/VP8", "video/H264"},
		},
		TURN: TURNConfig{UDPPort: 3478, TCPPort: 3478, CredentialTTL: 24 * time.Hour},
		Room: RoomConfig{AutoCreate: true, EmptyTimeout: 5 * time.Minute, DepartureTimeout: 20 * time.Second},
		Signal: SignalConfig{
			PingInterval:        5 * time.Second,
			PingTimeout:         15 * time.Second,
			ResumeWindow:        15 * time.Second,
			WriteQueueSize:      256,
			MaxMessageSize:      1 << 20,
			NegotiationDebounce: 50 * time.Millisecond,
		},
		Webhook: WebhookConfig{Timeout: 10 * time.Second, QueueSize: 1000},
		Recording: RecordingConfig{Enabled: true, HeartbeatTimeout: 15 * time.Second, Capacity: 100, TempDir: os.TempDir() + "/tessera-recorder",
			FFmpegPath: "ffmpeg", Storage: RecordingStorage{Kind: "local", LocalRoot: "./recordings"},
			HLS: RecordingHLS{SegmentDuration: 4 * time.Second, AudioCodec: "opus"}},
		Metrics: MetricsConfig{Addr: "127.0.0.1:6789"}, // unauthenticated: loopback, no pprof unless configured
		Logging: LoggingConfig{Level: "info"},
	}
}

// Load reads a YAML file over the defaults, expands ${ENV} references and
// applies environment overrides. An empty path loads only defaults and env.
func Load(path string) (*Config, error) {
	cfg := Default()
	if path != "" {
		if err := LoadYAML(path, cfg); err != nil {
			return nil, err
		}
	}
	applyEnv(cfg)
	return cfg, nil
}

// LoadYAML decodes a YAML file into dst after ${ENV} expansion. Unknown keys are errors.
// It is shared by the recorder and load tester configs.
func LoadYAML(path string, dst any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	dec := yaml.NewDecoder(strings.NewReader(os.ExpandEnv(string(raw))))
	dec.KnownFields(true)
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("config %s: %w", path, err)
	}
	return nil
}

func applyEnv(cfg *Config) {
	// TESSERA_KEYS="key1: secret1, key2: secret2"
	if v := os.Getenv("TESSERA_KEYS"); v != "" {
		if cfg.Keys == nil {
			cfg.Keys = map[string]string{}
		}
		for _, pair := range strings.Split(v, ",") {
			k, s, ok := strings.Cut(pair, ":")
			if ok {
				cfg.Keys[strings.TrimSpace(k)] = strings.TrimSpace(s)
			}
		}
	}
	if v := os.Getenv("TESSERA_NODE_IP"); v != "" {
		cfg.RTC.NodeIP = v
	}
}

// MinSecretLength is the minimum API secret length outside development mode.
const MinSecretLength = 32

// Validate checks the configuration for errors.
func (c *Config) Validate() error {
	var errs []error
	if c.Port <= 0 || c.Port > 65535 {
		errs = append(errs, fmt.Errorf("port %d out of range", c.Port))
	}
	if len(c.Keys) == 0 && c.KeyFile == "" {
		errs = append(errs, errors.New("no API keys configured (keys or key_file)"))
	}
	for k, s := range c.Keys {
		if k == "" {
			errs = append(errs, errors.New("empty API key"))
		}
		if len(s) < MinSecretLength && !c.Development {
			errs = append(errs, fmt.Errorf("secret for key %q shorter than %d bytes", k, MinSecretLength))
		}
	}
	if c.RTC.UDPPort < 0 || c.RTC.UDPPort > 65535 {
		errs = append(errs, fmt.Errorf("rtc.udp_port %d out of range (0 = random)", c.RTC.UDPPort))
	}
	if c.TURN.Enabled && c.TURN.TLSPort > 0 && (c.TURN.CertFile == "" || c.TURN.KeyFile == "") {
		errs = append(errs, errors.New("turn.tls_port requires turn.cert_file and turn.key_file"))
	}
	for _, e := range c.Webhook.Endpoints {
		if e.Encoding != "" && e.Encoding != "json" && e.Encoding != "protobuf" {
			errs = append(errs, fmt.Errorf("webhook encoding %q must be json or protobuf", e.Encoding))
		}
	}
	if len(c.Webhook.Endpoints) > 0 {
		if _, ok := c.Keys[c.Webhook.APIKey]; !ok {
			errs = append(errs, errors.New("webhook.api_key must name a configured key"))
		}
	}
	return errors.Join(errs...)
}

// NewLogger builds the process logger from the logging config.
func (c *Config) NewLogger() *slog.Logger {
	return NewLogger(c.Logging, c.Development)
}

// NewLogger builds a slog logger; shared by all binaries.
func NewLogger(lc LoggingConfig, development bool) *slog.Logger {
	var level slog.Level
	if err := level.UnmarshalText([]byte(lc.Level)); err != nil {
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	useJSON := !development
	if lc.JSON != nil {
		useJSON = *lc.JSON
	}
	if useJSON {
		return slog.New(slog.NewJSONHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, opts))
}
