package unit

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/enimatek/lattice/internal/config"
)

func TestDefaultConfigIsValid(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config must be valid: %v", err)
	}
	if cfg.Registration.Open {
		t.Errorf("registration must be closed by default (security first)")
	}
	if cfg.Registration.SharedSecret != "" {
		t.Errorf("no shared secret may be set by default")
	}
	if cfg.HTTP.MaxBodyBytes <= 0 {
		t.Errorf("body limit must be set")
	}
}

func TestLoadFromFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "lattice.json")
	contents := `{
  "server_name": "example.org",
  "data_dir": "` + dir + `/data",
  "http": {"client_addr": "127.0.0.1:9008", "federation_addr": "127.0.0.1:9448", "request_timeout": "30s"},
  "registration": {"open": true, "shared_secret": "hunter2", "min_password_length": 8},
  "well_known": {"client_base_url": "https://matrix.example.org"},
  "log": {"level": "debug", "format": "text"}
}`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("cannot write config: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.ServerName != "example.org" {
		t.Errorf("server_name = %q", cfg.ServerName)
	}
	if cfg.HTTP.ClientAddr != "127.0.0.1:9008" {
		t.Errorf("client_addr = %q", cfg.HTTP.ClientAddr)
	}
	if cfg.HTTP.RequestTimeout.Get() != 30*time.Second {
		t.Errorf("request_timeout = %v", cfg.HTTP.RequestTimeout.Get())
	}
	if !cfg.Registration.Open || cfg.Registration.SharedSecret != "hunter2" {
		t.Errorf("registration = %+v", cfg.Registration)
	}
	if cfg.Store.Path != filepath.Join(dir, "data/lattice.db") {
		t.Errorf("derived db path = %q", cfg.Store.Path)
	}
	if cfg.WellKnown.ClientBaseURL != "https://matrix.example.org" {
		t.Errorf("well_known = %+v", cfg.WellKnown)
	}
}

func TestEnvOverrides(t *testing.T) {
	t.Setenv("LATTICE_SERVER_NAME", "env.example")
	t.Setenv("LATTICE_CLIENT_ADDR", "127.0.0.1:18008")
	t.Setenv("LATTICE_REGISTRATION_OPEN", "true")
	t.Setenv("LATTICE_REGISTRATION_SHARED_SECRET", "s3cr3t")
	t.Setenv("LATTICE_MAX_BODY_BYTES", "2048")

	cfg := config.Default()
	cfg.ApplyEnv()
	if cfg.ServerName != "env.example" {
		t.Errorf("server_name = %q", cfg.ServerName)
	}
	if cfg.HTTP.ClientAddr != "127.0.0.1:18008" {
		t.Errorf("client_addr = %q", cfg.HTTP.ClientAddr)
	}
	if !cfg.Registration.Open {
		t.Errorf("registration open not applied")
	}
	if cfg.Registration.SharedSecret != "s3cr3t" {
		t.Errorf("shared secret not applied")
	}
	if cfg.HTTP.MaxBodyBytes != 2048 {
		t.Errorf("max body = %d", cfg.HTTP.MaxBodyBytes)
	}
	if cfg.Store.Path != filepath.Join("data", "lattice.db") {
		t.Errorf("db path should follow data_dir: %q", cfg.Store.Path)
	}
}

func TestConfigValidationRejectsBadInput(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		mutate func(*config.Config)
	}{
		{"invalid server name", func(c *config.Config) { c.ServerName = "not a server name" }},
		{"empty server name", func(c *config.Config) { c.ServerName = "" }},
		{"empty data dir", func(c *config.Config) { c.DataDir = "" }},
		{"empty client addr", func(c *config.Config) { c.HTTP.ClientAddr = "" }},
		{"empty federation addr", func(c *config.Config) { c.HTTP.FederationAddr = "" }},
		{"bcrypt cost too low", func(c *config.Config) { c.Registration.BcryptCost = 1 }},
		{"bcrypt cost too high", func(c *config.Config) { c.Registration.BcryptCost = 30 }},
		{"zero password length", func(c *config.Config) { c.Registration.MinPasswordLength = 0 }},
		{"zero body limit", func(c *config.Config) { c.HTTP.MaxBodyBytes = 0 }},
		{"bad log format", func(c *config.Config) { c.Log.Format = "yaml" }},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := config.Default()
			tc.mutate(cfg)
			if err := cfg.Validate(); err == nil {
				t.Errorf("expected validation failure for %s", tc.name)
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	t.Parallel()
	if _, err := config.Load(filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Errorf("expected an error for a missing config file")
	}
}

func TestLoadMalformedFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("cannot write: %v", err)
	}
	if _, err := config.Load(path); err == nil {
		t.Errorf("expected a parse error")
	}
}
