// Package config loads and validates server and client configuration.
package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/sss/sss/internal/protocol"
)

// Server is the on-disk server configuration, matching contracts/config.schema.json.
type Server struct {
	Server  ServerSection  `toml:"server"`
	Auth    AuthSection    `toml:"auth"`
	Storage StorageSection `toml:"storage"`
	Limits  LimitsSection  `toml:"limits"`
}

// ServerSection configures the listeners.
type ServerSection struct {
	Listen               string `toml:"listen"`
	PublicURL            string `toml:"public_url"`
	UnixSocket           string `toml:"unix_socket"`
	ShutdownGraceSeconds int    `toml:"shutdown_grace_seconds"`
}

// AuthSection configures the single base password.
type AuthSection struct {
	Username                string `toml:"username"`
	PasswordHash            string `toml:"password_hash"`
	FailedAttemptsPerMinute int    `toml:"failed_attempts_per_minute"`
}

// StorageSection configures where bytes and metadata live.
type StorageSection struct {
	DataDir                string `toml:"data_dir"`
	DBPath                 string `toml:"db_path"`
	StagingTTLMinutes      int    `toml:"staging_ttl_minutes"`
	LocalClaimGraceMinutes int    `toml:"local_claim_grace_minutes"`
}

// LimitsSection configures lifecycle bounds and resource protection.
type LimitsSection struct {
	DefaultTTLMinutes        int   `toml:"default_ttl_minutes"`
	MaxTTLMinutes            int   `toml:"max_ttl_minutes"`
	MaxTransferBytes         int64 `toml:"max_transfer_bytes"`
	MaxFiles                 int   `toml:"max_files"`
	MaxNoteBytes             int   `toml:"max_note_bytes"`
	DiskHighWatermarkPercent int   `toml:"disk_high_watermark_percent"`
	MaxConcurrentUploads     int   `toml:"max_concurrent_uploads"`
	MaxConcurrentDownloads   int   `toml:"max_concurrent_downloads"`
	MaxMaterializeWorkers    int   `toml:"max_materialize_workers"`
}

// DefaultServer returns a configuration with every optional value populated.
func DefaultServer() Server {
	return Server{
		Server: ServerSection{
			Listen:               "127.0.0.1:7070",
			UnixSocket:           "/run/sss/sssd.sock",
			ShutdownGraceSeconds: 30,
		},
		Auth: AuthSection{
			Username:                "sss",
			FailedAttemptsPerMinute: 20,
		},
		Storage: StorageSection{
			DataDir:                "/srv/sss",
			DBPath:                 "/var/lib/sss/sss.db",
			StagingTTLMinutes:      360,
			LocalClaimGraceMinutes: 120,
		},
		Limits: LimitsSection{
			DefaultTTLMinutes:        protocol.DefaultTTLMinutes,
			MaxTTLMinutes:            protocol.MaxTTLMinutes,
			MaxTransferBytes:         0,
			MaxFiles:                 250000,
			MaxNoteBytes:             protocol.MaxNoteBytesHard,
			DiskHighWatermarkPercent: 85,
			MaxConcurrentUploads:     8,
			MaxConcurrentDownloads:   16,
			MaxMaterializeWorkers:    4,
		},
	}
}

// LoadServer reads, merges, and validates a server configuration file.
func LoadServer(path string) (Server, error) {
	cfg := DefaultServer()
	data, err := os.ReadFile(path)
	if err != nil {
		return Server{}, fmt.Errorf("read config: %w", err)
	}
	md, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return Server{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, k := range undecoded {
			keys = append(keys, k.String())
		}
		return Server{}, fmt.Errorf("unknown configuration keys: %s", strings.Join(keys, ", "))
	}
	if err := cfg.Validate(); err != nil {
		return Server{}, err
	}
	return cfg, nil
}

// Validate enforces the configuration contract.
func (c *Server) Validate() error {
	if c.Server.Listen == "" {
		return fmt.Errorf("server.listen is required")
	}
	if c.Server.PublicURL != "" {
		u, err := url.Parse(c.Server.PublicURL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("server.public_url must be an absolute URL")
		}
	}
	if c.Server.UnixSocket == "" {
		return fmt.Errorf("server.unix_socket is required")
	}
	if c.Server.ShutdownGraceSeconds < 1 || c.Server.ShutdownGraceSeconds > 600 {
		return fmt.Errorf("server.shutdown_grace_seconds must be between 1 and 600")
	}
	if c.Auth.Username != "sss" {
		return fmt.Errorf("auth.username must be \"sss\"")
	}
	if len(c.Auth.PasswordHash) < 20 || strings.Contains(c.Auth.PasswordHash, "REPLACE_ME") {
		return fmt.Errorf("auth.password_hash is not set; generate one with: sss hash-password")
	}
	if c.Auth.FailedAttemptsPerMinute < 1 {
		return fmt.Errorf("auth.failed_attempts_per_minute must be at least 1")
	}
	if c.Storage.DataDir == "" || !filepath.IsAbs(c.Storage.DataDir) {
		return fmt.Errorf("storage.data_dir must be an absolute path")
	}
	if c.Storage.DBPath == "" || !filepath.IsAbs(c.Storage.DBPath) {
		return fmt.Errorf("storage.db_path must be an absolute path")
	}
	if c.Storage.StagingTTLMinutes < 1 {
		return fmt.Errorf("storage.staging_ttl_minutes must be at least 1")
	}
	if c.Storage.LocalClaimGraceMinutes < 1 {
		return fmt.Errorf("storage.local_claim_grace_minutes must be at least 1")
	}
	if c.Limits.DefaultTTLMinutes != protocol.DefaultTTLMinutes {
		return fmt.Errorf("limits.default_ttl_minutes must be %d", protocol.DefaultTTLMinutes)
	}
	if c.Limits.MaxTTLMinutes != protocol.MaxTTLMinutes {
		return fmt.Errorf("limits.max_ttl_minutes must be %d", protocol.MaxTTLMinutes)
	}
	if c.Limits.MaxTransferBytes < 0 {
		return fmt.Errorf("limits.max_transfer_bytes must not be negative")
	}
	if c.Limits.MaxFiles < 1 {
		return fmt.Errorf("limits.max_files must be at least 1")
	}
	if c.Limits.MaxNoteBytes < 1 || c.Limits.MaxNoteBytes > protocol.MaxNoteBytesHard {
		return fmt.Errorf("limits.max_note_bytes must be between 1 and %d", protocol.MaxNoteBytesHard)
	}
	if c.Limits.DiskHighWatermarkPercent < 50 || c.Limits.DiskHighWatermarkPercent > 99 {
		return fmt.Errorf("limits.disk_high_watermark_percent must be between 50 and 99")
	}
	if c.Limits.MaxConcurrentUploads < 1 || c.Limits.MaxConcurrentDownloads < 1 || c.Limits.MaxMaterializeWorkers < 1 {
		return fmt.Errorf("limits concurrency values must be at least 1")
	}
	return nil
}

// StagingDir is where in-progress transfers are written.
func (c *Server) StagingDir() string { return filepath.Join(c.Storage.DataDir, "staging") }

// LiveDir holds committed, immutable transfers.
func (c *Server) LiveDir() string { return filepath.Join(c.Storage.DataDir, "live") }

// TrashDir holds transfers awaiting recursive deletion.
func (c *Server) TrashDir() string { return filepath.Join(c.Storage.DataDir, "trash") }

// Redacted returns a diagnostic copy with secrets removed.
func (c Server) Redacted() Server {
	c.Auth.PasswordHash = "[redacted]"
	return c
}
