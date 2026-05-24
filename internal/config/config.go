package config

import (
	"encoding/json"
	"os"
)

type Config struct {
	// Path to nginx access log file
	LogPath string `json:"log_path"`
	// Log format: "combined" or "common"
	LogFormat string `json:"log_format"`
	// HTTP server listen address
	ListenAddr string `json:"listen_addr"`
	// Database path (SQLite)
	DBPath string `json:"db_path"`
	// Whether to watch log file for new entries
	Watch bool `json:"watch"`
	// Keywords to track in request paths/query strings
	Keywords []string `json:"keywords"`
	// Input mode: "file", "syslog", or "both"
	InputMode string `json:"input_mode"`
	// Syslog listen address (e.g. ":514" or "127.0.0.1:1514")
	SyslogAddr string `json:"syslog_addr"`
	// Syslog protocol: "udp", "tcp", or "both"
	SyslogProto string `json:"syslog_proto"`
}

func DefaultConfig() *Config {
	return &Config{
		LogPath:     "/var/log/nginx/access.log",
		LogFormat:   "combined",
		ListenAddr:  ":8080",
		DBPath:      "./data/stats.db",
		Watch:       true,
		Keywords:    []string{},
		InputMode:   "file",
		SyslogAddr:  ":1514",
		SyslogProto: "udp",
	}
}

func Load(path string) (*Config, error) {
	cfg := DefaultConfig()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) Save(path string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}
