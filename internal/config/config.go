// Package config loads Lattice configuration from a JSON file, the environment
// and command line flags, in that order of increasing precedence.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/enimatek/lattice/internal/matrixid"
)

// Duration is a time.Duration that decodes from a Go duration string ("30s") or
// a number of seconds.
type Duration time.Duration

// MarshalJSON writes the duration as a Go duration string.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// UnmarshalJSON accepts `"90s"` (Go duration syntax) or `90` (seconds).
func (d *Duration) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		parsed, cerr := time.ParseDuration(s)
		if cerr != nil {
			return fmt.Errorf("config: invalid duration %q: %w", s, cerr)
		}
		*d = Duration(parsed)
		return nil
	}
	var secs float64
	if err := json.Unmarshal(data, &secs); err != nil {
		return fmt.Errorf("config: invalid duration %s", data)
	}
	*d = Duration(time.Duration(secs * float64(time.Second)))
	return nil
}

// Get returns the duration as a time.Duration.
func (d Duration) Get() time.Duration { return time.Duration(d) }

// StoreConfig configures persistence.
type StoreConfig struct {
	// Path of the SQLite database file. Defaults to <data_dir>/lattice.db.
	Path string `json:"path"`
}

// SigningConfig configures the server signing key.
type SigningConfig struct {
	// Path of the ed25519 signing key file. Defaults to <data_dir>/signing.key.
	Path string `json:"path"`
}

// TLSConfig configures the federation (and optionally client) TLS listener.
type TLSConfig struct {
	CertFile string `json:"cert_file"`
	KeyFile  string `json:"key_file"`
	// AutoGenerate creates a self-signed certificate in the data directory when
	// CertFile/KeyFile are missing. Never used when explicit files are given.
	AutoGenerate bool `json:"auto_generate"`
	// CACertFile and CAKeyFile, when both are set and the certificate is missing,
	// produce a CA-signed certificate instead of a self-signed one. This is how
	// the Complement test harness supplies its own CA.
	CACertFile string `json:"ca_cert_file"`
	CAKeyFile  string `json:"ca_key_file"`
}

// HTTPConfig configures the HTTP listeners.
type HTTPConfig struct {
	ClientAddr     string `json:"client_addr"`
	FederationAddr string `json:"federation_addr"`

	ReadHeaderTimeout Duration `json:"read_header_timeout"`
	WriteTimeout      Duration `json:"write_timeout"`
	IdleTimeout       Duration `json:"idle_timeout"`
	RequestTimeout    Duration `json:"request_timeout"`
	ShutdownTimeout   Duration `json:"shutdown_timeout"`

	MaxBodyBytes           int64 `json:"max_body_bytes"`
	FederationMaxBodyBytes int64 `json:"federation_max_body_bytes"`
}

// RegistrationConfig configures account creation policy.
type RegistrationConfig struct {
	// Open enables unauthenticated registration through the m.login.dummy flow.
	Open bool `json:"open"`
	// SharedSecret enables the Synapse-compatible /_synapse/admin/v1/register
	// endpoint. Disabled when empty.
	SharedSecret      string `json:"shared_secret"`
	MinPasswordLength int    `json:"min_password_length"`
	BcryptCost        int    `json:"bcrypt_cost"`
}

// WellKnownConfig configures the discovery documents.
type WellKnownConfig struct {
	ClientBaseURL string `json:"client_base_url"`
	ServerBaseURL string `json:"server_base_url"`
}

// LogConfig configures logging.
type LogConfig struct {
	Level  string `json:"level"`
	Format string `json:"format"`
}

// Config is the full server configuration.
type Config struct {
	ServerName   string             `json:"server_name"`
	DataDir      string             `json:"data_dir"`
	Store        StoreConfig        `json:"store"`
	Signing      SigningConfig      `json:"signing"`
	TLS          TLSConfig          `json:"tls"`
	HTTP         HTTPConfig         `json:"http"`
	Registration RegistrationConfig `json:"registration"`
	WellKnown    WellKnownConfig    `json:"well_known"`
	Log          LogConfig          `json:"log"`
}

// Default returns a configuration with safe defaults: closed registration, no
// discovery documents, localhost listeners.
func Default() *Config {
	c := &Config{
		ServerName: "localhost",
		DataDir:    "data",
		HTTP: HTTPConfig{
			ClientAddr:             "127.0.0.1:8008",
			FederationAddr:         "127.0.0.1:8448",
			ReadHeaderTimeout:      Duration(10 * time.Second),
			WriteTimeout:           Duration(60 * time.Second),
			IdleTimeout:            Duration(120 * time.Second),
			RequestTimeout:         Duration(45 * time.Second),
			ShutdownTimeout:        Duration(15 * time.Second),
			MaxBodyBytes:           1 << 20,
			FederationMaxBodyBytes: 20 << 20,
		},
		TLS: TLSConfig{AutoGenerate: true},
		Registration: RegistrationConfig{
			Open:              false,
			MinPasswordLength: 6,
			BcryptCost:        10,
		},
		Log: LogConfig{Level: "info", Format: "json"},
	}
	c.Store.Path = filepath.Join(c.DataDir, "lattice.db")
	c.Signing.Path = filepath.Join(c.DataDir, "signing.key")
	return c
}

// Load reads a JSON configuration file, applies environment overrides and
// validates the result.
func Load(path string) (*Config, error) {
	cfg := Default()
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("config: cannot read %s: %w", path, err)
		}
		dec := json.NewDecoder(strings.NewReader(string(data)))
		if err := dec.Decode(cfg); err != nil {
			return nil, fmt.Errorf("config: cannot parse %s: %w", path, err)
		}
	}
	cfg.ApplyEnv()
	if err := cfg.applyDerivedDefaults(); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// ApplyEnv overrides configuration from LATTICE_* environment variables.
func (c *Config) ApplyEnv() {
	setStr(&c.ServerName, "SERVER_NAME")
	setStr(&c.ServerName, "LATTICE_SERVER_NAME")
	setStr(&c.DataDir, "LATTICE_DATA_DIR")
	setStr(&c.Store.Path, "LATTICE_DB_PATH")
	setStr(&c.Signing.Path, "LATTICE_SIGNING_KEY_PATH")
	setStr(&c.TLS.CertFile, "LATTICE_TLS_CERT_FILE")
	setStr(&c.TLS.KeyFile, "LATTICE_TLS_KEY_FILE")
	setStr(&c.HTTP.ClientAddr, "LATTICE_CLIENT_ADDR")
	setStr(&c.HTTP.FederationAddr, "LATTICE_FEDERATION_ADDR")
	setStr(&c.Registration.SharedSecret, "LATTICE_REGISTRATION_SHARED_SECRET")
	setStr(&c.WellKnown.ClientBaseURL, "LATTICE_WELL_KNOWN_CLIENT_BASE_URL")
	setStr(&c.WellKnown.ServerBaseURL, "LATTICE_WELL_KNOWN_SERVER_BASE_URL")
	setStr(&c.Log.Level, "LATTICE_LOG_LEVEL")
	setStr(&c.Log.Format, "LATTICE_LOG_FORMAT")
	setBool(&c.Registration.Open, "LATTICE_REGISTRATION_OPEN")
	setBool(&c.TLS.AutoGenerate, "LATTICE_TLS_AUTO_GENERATE")
	setStr(&c.TLS.CACertFile, "LATTICE_TLS_CA_CERT_FILE")
	setStr(&c.TLS.CAKeyFile, "LATTICE_TLS_CA_KEY_FILE")
	setInt(&c.HTTP.MaxBodyBytes, "LATTICE_MAX_BODY_BYTES")
	setInt(&c.HTTP.FederationMaxBodyBytes, "LATTICE_FEDERATION_MAX_BODY_BYTES")
	setSmallInt(&c.Registration.MinPasswordLength, "LATTICE_MIN_PASSWORD_LENGTH")
	setSmallInt(&c.Registration.BcryptCost, "LATTICE_BCRYPT_COST")
}

func setStr(target *string, key string) {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		*target = v
	}
}

func setBool(target *bool, key string) {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if parsed, err := strconv.ParseBool(v); err == nil {
			*target = parsed
		}
	}
}

func setSmallInt(target *int, key string) {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if parsed, err := strconv.Atoi(v); err == nil {
			*target = parsed
		}
	}
}

func setInt(target *int64, key string) {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if parsed, err := strconv.ParseInt(v, 10, 64); err == nil {
			*target = parsed
		}
	}
}

// applyDerivedDefaults fills paths that depend on other settings.
func (c *Config) applyDerivedDefaults() error {
	if c.Store.Path == "" || c.Store.Path == filepath.Join("data", "lattice.db") {
		c.Store.Path = filepath.Join(c.DataDir, "lattice.db")
	}
	if c.Signing.Path == "" || c.Signing.Path == filepath.Join("data", "signing.key") {
		c.Signing.Path = filepath.Join(c.DataDir, "signing.key")
	}
	if c.TLS.CertFile == "" && c.TLS.AutoGenerate {
		c.TLS.CertFile = filepath.Join(c.DataDir, "tls", c.ServerName+".crt")
	}
	if c.TLS.KeyFile == "" && c.TLS.AutoGenerate {
		c.TLS.KeyFile = filepath.Join(c.DataDir, "tls", c.ServerName+".key")
	}
	return nil
}

// Validate checks the configuration is usable.
func (c *Config) Validate() error {
	if err := matrixid.ServerNameValid(c.ServerName); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if c.DataDir == "" {
		return fmt.Errorf("config: data_dir must not be empty")
	}
	if c.HTTP.ClientAddr == "" {
		return fmt.Errorf("config: http.client_addr must not be empty")
	}
	if c.HTTP.FederationAddr == "" {
		return fmt.Errorf("config: http.federation_addr must not be empty")
	}
	if c.Registration.BcryptCost < 4 || c.Registration.BcryptCost > 15 {
		return fmt.Errorf("config: registration.bcrypt_cost must be between 4 and 15")
	}
	if c.Registration.MinPasswordLength < 1 {
		return fmt.Errorf("config: registration.min_password_length must be at least 1")
	}
	if c.HTTP.MaxBodyBytes <= 0 || c.HTTP.FederationMaxBodyBytes <= 0 {
		return fmt.Errorf("config: body size limits must be positive")
	}
	switch c.Log.Format {
	case "json", "text":
	default:
		return fmt.Errorf("config: log.format must be \"json\" or \"text\"")
	}
	return nil
}

// DatabasePath returns the configured database path.
func (c *Config) DatabasePath() string { return c.Store.Path }

// SigningKeyPath returns the configured signing key path.
func (c *Config) SigningKeyPath() string { return c.Signing.Path }
