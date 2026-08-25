// Package blackbox exercises the product through its public surfaces only:
// HTTP, the Unix socket, and the CLI. It never reaches into internal state to
// make an assertion that a user could not make.
package blackbox

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sss/sss/internal/app"
	"github.com/sss/sss/internal/auth"
	"github.com/sss/sss/internal/clock"
	"github.com/sss/sss/internal/config"
)

// testPassword is the shared base password used by the harness.
const testPassword = "harness-password-1234"

// server is a running daemon under test.
type server struct {
	t        *testing.T
	cfg      config.Server
	srv      *app.Server
	baseURL  string
	socket   string
	dataDir  string
	dbPath   string
	cancel   context.CancelFunc
	done     chan struct{}
	fakeTime *clock.Fixed
}

// options tunes a harness server.
type options struct {
	maxFiles         int
	maxTransferBytes int64
	watermarkPercent int
	localGraceMin    int
	fixedClock       *clock.Fixed
	dataDir          string
	dbPath           string
	socket           string
}

// startServer boots a daemon on loopback with its own storage.
func startServer(t *testing.T, opt options) *server {
	t.Helper()
	dir := t.TempDir()
	if opt.dataDir == "" {
		opt.dataDir = filepath.Join(dir, "srv")
	}
	if opt.dbPath == "" {
		opt.dbPath = filepath.Join(dir, "var", "sss.db")
	}
	if opt.socket == "" {
		// Unix socket paths are length limited, so keep this short.
		sockDir, err := os.MkdirTemp("", "sss")
		if err != nil {
			t.Fatalf("socket dir: %v", err)
		}
		t.Cleanup(func() { os.RemoveAll(sockDir) })
		opt.socket = filepath.Join(sockDir, "s.sock")
	}
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}

	cfg := config.DefaultServer()
	cfg.Server.Listen = "127.0.0.1:0"
	cfg.Server.UnixSocket = opt.socket
	cfg.Server.PublicURL = "https://drop.example.test"
	cfg.Auth.PasswordHash = hash
	cfg.Storage.DataDir = opt.dataDir
	cfg.Storage.DBPath = opt.dbPath
	if opt.localGraceMin > 0 {
		cfg.Storage.LocalClaimGraceMinutes = opt.localGraceMin
	}
	if opt.maxFiles > 0 {
		cfg.Limits.MaxFiles = opt.maxFiles
	}
	cfg.Limits.MaxTransferBytes = opt.maxTransferBytes
	if opt.watermarkPercent > 0 {
		cfg.Limits.DiskHighWatermarkPercent = opt.watermarkPercent
	} else {
		cfg.Limits.DiskHighWatermarkPercent = 99
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("harness config invalid: %v", err)
	}

	var clk clock.Clock = clock.Real{}
	if opt.fixedClock != nil {
		clk = opt.fixedClock
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv, err := app.NewServer(cfg, log, app.Options{Clock: clk})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	if err := srv.Listen(); err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &server{
		t:        t,
		cfg:      cfg,
		srv:      srv,
		baseURL:  "http://" + srv.Addr(),
		socket:   opt.socket,
		dataDir:  opt.dataDir,
		dbPath:   opt.dbPath,
		cancel:   cancel,
		done:     make(chan struct{}),
		fakeTime: opt.fixedClock,
	}
	go func() {
		defer close(s.done)
		// The janitor is driven explicitly in tests that need it.
		_ = srv.Run(ctx, app.Options{Clock: clk, DisableJanitor: true})
	}()
	s.waitReady()
	t.Cleanup(s.stop)
	return s
}

func (s *server) waitReady() {
	s.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(s.baseURL + "/readyz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	s.t.Fatal("server did not become ready")
}

// stop shuts the daemon down and waits for it to exit.
func (s *server) stop() {
	s.cancel()
	select {
	case <-s.done:
	case <-time.After(30 * time.Second):
		s.t.Fatal("server did not shut down")
	}
}

// restart stops the daemon and starts a new one over the same storage, which is
// what a service restart looks like to a user.
func (s *server) restart() *server {
	s.t.Helper()
	s.stop()
	return startServer(s.t, options{
		dataDir:          s.dataDir,
		dbPath:           s.dbPath,
		socket:           s.socket,
		watermarkPercent: s.cfg.Limits.DiskHighWatermarkPercent,
		maxFiles:         s.cfg.Limits.MaxFiles,
		maxTransferBytes: s.cfg.Limits.MaxTransferBytes,
		fixedClock:       s.fakeTime,
	})
}
