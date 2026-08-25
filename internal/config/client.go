package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// DefaultUnixSocket is where a VPS-local client looks for the daemon.
const DefaultUnixSocket = "/run/sss/sssd.sock"

// Client is the saved client configuration.
type Client struct {
	URL        string `json:"url,omitempty"`
	UnixSocket string `json:"unix_socket,omitempty"`
}

// ClientConfigPath returns the platform-appropriate client configuration file.
func ClientConfigPath() (string, error) {
	if p := os.Getenv("SSS_CONFIG"); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user config directory: %w", err)
	}
	return filepath.Join(dir, "sss", "config.json"), nil
}

// StateDir returns the directory used for resumable client session state.
func StateDir() (string, error) {
	if p := os.Getenv("SSS_STATE_DIR"); p != "" {
		return p, nil
	}
	if runtime.GOOS == "windows" {
		if p := os.Getenv("LOCALAPPDATA"); p != "" {
			return filepath.Join(p, "sss", "state"), nil
		}
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locate user cache directory: %w", err)
	}
	return filepath.Join(dir, "sss", "state"), nil
}

// LoadClient reads the saved client configuration, returning an empty value
// when no file exists yet.
func LoadClient() (Client, error) {
	path, err := ClientConfigPath()
	if err != nil {
		return Client{}, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Client{}, nil
	}
	if err != nil {
		return Client{}, fmt.Errorf("read client config: %w", err)
	}
	var c Client
	if err := json.Unmarshal(data, &c); err != nil {
		return Client{}, fmt.Errorf("parse client config %s: %w", path, err)
	}
	return c, nil
}

// SaveClient writes the client configuration with owner-only permissions.
func SaveClient(c Client) (string, error) {
	path, err := ClientConfigPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create client config directory: %w", err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return "", err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return "", fmt.Errorf("write client config: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", fmt.Errorf("install client config: %w", err)
	}
	return path, nil
}

// ResolveURL applies the documented precedence: explicit flag, SSS_URL, then
// saved configuration.
func ResolveURL(explicit string, saved Client) (string, error) {
	candidate := explicit
	if candidate == "" {
		candidate = os.Getenv("SSS_URL")
	}
	if candidate == "" {
		candidate = saved.URL
	}
	if candidate == "" {
		return "", fmt.Errorf("no server URL configured; run: sss configure --url https://drop.example.com")
	}
	candidate = strings.TrimRight(candidate, "/")
	u, err := url.Parse(candidate)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid server URL %q", candidate)
	}
	if u.Scheme != "https" && u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1" {
		return "", fmt.Errorf("server URL must use https (got %q)", u.Scheme)
	}
	return candidate, nil
}

// ResolveSocket applies the precedence for the VPS-local socket path.
func ResolveSocket(explicit string, saved Client) string {
	if explicit != "" {
		return explicit
	}
	if p := os.Getenv("SSS_SOCKET"); p != "" {
		return p
	}
	if saved.UnixSocket != "" {
		return saved.UnixSocket
	}
	return DefaultUnixSocket
}
