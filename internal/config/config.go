package config

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/moehoshio/web-request-attribution/internal/parser"
)

// SourceType identifies the kind of log source.
type SourceType string

const (
	SourceFile   SourceType = "file"
	SourceSyslog SourceType = "syslog"
)

// Source describes one place to ingest log lines from.
type Source struct {
	// Name is an optional human-readable identifier.
	Name string `json:"name,omitempty"`
	// Type is "file" or "syslog".
	Type SourceType `json:"type"`
	// Format describes how to parse lines from this source.
	Format parser.FormatConfig `json:"format"`

	// File-source fields.
	Path string `json:"path,omitempty"`
	// ReadCompressed enables reading rotated/archived `.gz` files when first
	// opening the source. Live tailing of compressed files is not supported.
	// Support for `.bz2`/`.xz` is tracked in docs/TODO.md.
	ReadCompressed bool `json:"read_compressed,omitempty"`

	// Syslog-source fields.
	Addr  string `json:"addr,omitempty"`
	Proto string `json:"proto,omitempty"` // "udp", "tcp", or "both"
}

// Config is the top-level application configuration.
type Config struct {
	// HTTP server listen address.
	ListenAddr string `json:"listen_addr"`
	// SQLite database path.
	DBPath string `json:"db_path"`
	// Whether to start watchers on launch. When false the server only serves
	// the dashboard / API and does not ingest new lines.
	Watch bool `json:"watch"`
	// Keywords to track in request paths and query strings.
	Keywords []string `json:"keywords"`
	// Sources is the list of log inputs to ingest from.
	Sources []Source `json:"sources"`
	// Auth contains bootstrap settings for the account system. See
	// docs/ROADMAP.md (Phase 2).
	Auth AuthConfig `json:"auth"`
}

// AuthConfig holds settings consumed at startup by the auth package.
type AuthConfig struct {
	// BootstrapAdmin creates the named admin user on first launch when
	// the users table is empty. Both fields are required to trigger
	// the bootstrap; otherwise the operator must create the first user
	// out-of-band (e.g. by inserting into SQLite directly).
	BootstrapAdmin *BootstrapAdmin `json:"bootstrap_admin,omitempty"`
	// BcryptCost overrides the bcrypt cost parameter. 0 → default (10).
	BcryptCost int `json:"bcrypt_cost,omitempty"`
	// SessionTTLHours overrides the session lifetime. 0 → 24 hours.
	SessionTTLHours int `json:"session_ttl_hours,omitempty"`
	// CookieSecure issues cookies with the Secure attribute (HTTPS only).
	CookieSecure bool `json:"cookie_secure,omitempty"`
}

// BootstrapAdmin describes the initial admin account created on first
// launch.
type BootstrapAdmin struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// DefaultConfig returns a Config populated with sensible defaults and a single
// Nginx file source.
func DefaultConfig() *Config {
	return &Config{
		ListenAddr: ":8080",
		DBPath:     "./data/stats.db",
		Watch:      true,
		Keywords:   []string{},
		Sources: []Source{{
			Name: "nginx",
			Type: SourceFile,
			Path: "/var/log/nginx/access.log",
			Format: parser.FormatConfig{
				Engine: "nginx",
				Preset: "combined",
			},
		}},
	}
}

// Load reads configuration from disk. A missing file is treated as a request
// for defaults.
func Load(path string) (*Config, error) {
	cfg := DefaultConfig()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, err
	}
	// Replace defaults entirely when a file is provided so unset fields are
	// explicit rather than silently merged.
	cfg = &Config{}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Validate checks the configuration for obvious mistakes.
func (c *Config) Validate() error {
	if c.ListenAddr == "" {
		c.ListenAddr = ":8080"
	}
	if c.DBPath == "" {
		c.DBPath = "./data/stats.db"
	}
	for i, s := range c.Sources {
		switch s.Type {
		case SourceFile:
			if s.Path == "" {
				return fmt.Errorf("sources[%d]: file source requires \"path\"", i)
			}
		case SourceSyslog:
			if s.Addr == "" {
				return fmt.Errorf("sources[%d]: syslog source requires \"addr\"", i)
			}
			if s.Proto == "" {
				c.Sources[i].Proto = "udp"
			}
		case "":
			return fmt.Errorf("sources[%d]: missing \"type\"", i)
		default:
			return fmt.Errorf("sources[%d]: unknown type %q", i, s.Type)
		}
	}
	return nil
}

// Save writes the configuration to disk as indented JSON.
func (c *Config) Save(path string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}
