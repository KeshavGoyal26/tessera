package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const goodSecret = "0123456789abcdef0123456789abcdef"

func TestDefaultsAreValidWithKey(t *testing.T) {
	cfg := Default()
	cfg.Keys = map[string]string{"APIkey": goodSecret}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejectsShortSecretOutsideDevelopment(t *testing.T) {
	cfg := Default()
	cfg.Keys = map[string]string{"k": "short"}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "shorter") {
		t.Fatalf("want short secret error, got %v", err)
	}
	cfg.Development = true
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadYAMLWithEnvExpansion(t *testing.T) {
	t.Setenv("TEST_SECRET", goodSecret)
	path := filepath.Join(t.TempDir(), "tessera.yaml")
	yaml := "port: 9000\nkeys:\n  k1: ${TEST_SECRET}\nsignal:\n  ping_interval: 2s\nrtc:\n  node_ip: 10.0.0.1\n"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 9000 || cfg.Keys["k1"] != goodSecret || cfg.Signal.PingInterval != 2*time.Second {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if cfg.RTC.UDPPort != 7882 {
		t.Fatal("defaults must survive partial files")
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadYAMLRejectsUnknownKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(path, []byte("prot: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for unknown key")
	}
}

func TestEnvKeys(t *testing.T) {
	t.Setenv("TESSERA_KEYS", "a: "+goodSecret+", b: "+goodSecret)
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Keys) != 2 || cfg.Keys["b"] != goodSecret {
		t.Fatalf("keys: %v", cfg.Keys)
	}
}

func TestValidateClusterRoles(t *testing.T) {
	base := func() *Config {
		cfg := Default()
		cfg.Keys = map[string]string{"APIkey": goodSecret}
		return cfg
	}
	cfg := base()
	cfg.Cluster.Role = RoleMedia
	if cfg.Validate() == nil {
		t.Fatal("a media node without redis, node_id and private_url must be refused")
	}
	cfg.Cluster.Redis.Addr, cfg.NodeID, cfg.Cluster.PrivateURL = "127.0.0.1:6379", "m1", "http://10.0.0.5:7880"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg = base()
	cfg.Cluster.Role = "router"
	if cfg.Validate() == nil {
		t.Fatal("unknown role accepted")
	}
	cfg = base()
	cfg.RTC.TURNRelay.URLs = []string{"turn:edge.example.com:3478"}
	if cfg.Validate() == nil {
		t.Fatal("turn_relay urls without a secret accepted")
	}
}
