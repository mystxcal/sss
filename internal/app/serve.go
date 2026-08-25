// Package app wires the daemon together: listeners, lifecycle service,
// reconciliation, cleanup, and graceful shutdown.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/sss/sss/internal/api/local"
	"github.com/sss/sss/internal/api/middleware"
	"github.com/sss/sss/internal/api/public"
	"github.com/sss/sss/internal/auth"
	"github.com/sss/sss/internal/clock"
	"github.com/sss/sss/internal/config"
	"github.com/sss/sss/internal/janitor"
	"github.com/sss/sss/internal/platform"
	"github.com/sss/sss/internal/reconcile"
	"github.com/sss/sss/internal/store/files"
	"github.com/sss/sss/internal/store/sqlite"
	"github.com/sss/sss/internal/transfer"
	"github.com/sss/sss/internal/version"
)

// Server is a running daemon.
type Server struct {
	cfg      config.Server
	log      *slog.Logger
	store    *sqlite.Store
	svc      *transfer.Service
	janitor  *janitor.Janitor
	public   *http.Server
	local    *http.Server
	tcpLn    net.Listener
	unixLn   net.Listener
	stopOnce chan struct{}
}

// Options tunes daemon construction, mainly for tests.
type Options struct {
	Clock          clock.Clock
	SweepInterval  time.Duration
	DisableJanitor bool
}

// NewServer builds a daemon from configuration without starting listeners.
func NewServer(cfg config.Server, log *slog.Logger, opts Options) (*Server, error) {
	if opts.Clock == nil {
		opts.Clock = clock.Real{}
	}
	store, err := sqlite.Open(cfg.Storage.DBPath)
	if err != nil {
		return nil, err
	}
	layout, err := files.New(cfg.Storage.DataDir)
	if err != nil {
		store.Close()
		return nil, err
	}
	svc := transfer.New(cfg, store, layout, opts.Clock, log)
	jan := janitor.New(svc, log)

	verifier, err := auth.NewVerifier(cfg.Auth.PasswordHash)
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("configure authentication: %w", err)
	}
	limiter := auth.NewLimiter(cfg.Auth.FailedAttemptsPerMinute, opts.Clock.Now)

	publicAPI := public.New(svc, log)
	localAPI := local.New(svc, jan, log)

	authed := func(h http.Handler) http.Handler {
		return middleware.Auth(verifier, limiter, log, public.WriteError)(h)
	}
	publicMux := http.NewServeMux()
	publicAPI.Register(publicMux, authed)

	// The local plane authorizes through socket permissions, so the same API is
	// mounted without the password requirement, plus the local-only routes.
	localMux := http.NewServeMux()
	publicAPI.Register(localMux, func(h http.Handler) http.Handler { return h })
	localAPI.Register(localMux)

	common := []func(http.Handler) http.Handler{
		middleware.RequestID,
		middleware.Recover(log, public.WriteError),
		middleware.LogRequests(log),
	}

	s := &Server{
		cfg:      cfg,
		log:      log,
		store:    store,
		svc:      svc,
		janitor:  jan,
		stopOnce: make(chan struct{}),
	}
	s.public = &http.Server{
		Handler:           middleware.Chain(publicMux, common...),
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          nil,
	}
	s.local = &http.Server{
		Handler:           middleware.Chain(localMux, common...),
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	return s, nil
}

// Service exposes the lifecycle service, used by tests and admin commands.
func (s *Server) Service() *transfer.Service { return s.svc }

// Addr reports the bound public address once listening.
func (s *Server) Addr() string {
	if s.tcpLn == nil {
		return ""
	}
	return s.tcpLn.Addr().String()
}

// Listen binds both listeners without serving traffic yet.
func (s *Server) Listen() error {
	ln, err := net.Listen("tcp", s.cfg.Server.Listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.cfg.Server.Listen, err)
	}
	s.tcpLn = ln

	sockPath := s.cfg.Server.UnixSocket
	if sockPath != "" {
		if err := os.MkdirAll(filepath.Dir(sockPath), 0o750); err != nil {
			ln.Close()
			return fmt.Errorf("create socket directory: %w", err)
		}
		// A socket file left by a crash would block binding.
		if err := removeStaleSocket(sockPath); err != nil {
			ln.Close()
			return err
		}
		uln, err := net.Listen("unix", sockPath)
		if err != nil {
			ln.Close()
			return fmt.Errorf("listen on %s: %w", sockPath, err)
		}
		if err := platform.ChmodSocket(sockPath); err != nil {
			ln.Close()
			uln.Close()
			return fmt.Errorf("set socket permissions: %w", err)
		}
		s.unixLn = uln
	}
	return nil
}

func removeStaleSocket(path string) error {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("%s exists and is not a socket", path)
	}
	if conn, err := net.DialTimeout("unix", path, 200*time.Millisecond); err == nil {
		conn.Close()
		return fmt.Errorf("another daemon is already listening on %s", path)
	}
	return os.Remove(path)
}

// Run reconciles storage, serves both listeners, and blocks until the context
// is cancelled or a termination signal arrives.
func (s *Server) Run(ctx context.Context, opts Options) error {
	if s.tcpLn == nil {
		if err := s.Listen(); err != nil {
			return err
		}
	}
	s.log.Info("starting sss",
		"version", version.Version,
		"protocol", version.Protocol,
		"listen", s.Addr(),
		"socket", s.cfg.Server.UnixSocket,
		"data_dir", s.cfg.Storage.DataDir)

	// Readiness is only declared after reconciliation, so a code can never
	// resolve to a half-published transfer after a crash.
	rep, err := reconcile.Run(ctx, s.svc, s.log)
	if err != nil {
		return fmt.Errorf("startup reconciliation: %w", err)
	}
	s.log.Info("reconciliation complete",
		"completed_commits", rep.CompletedCommits,
		"failed_transfers", rep.FailedTransfers,
		"orphan_live", rep.OrphanLive,
		"orphan_staging", rep.OrphanStaging,
		"finished_deletes", rep.FinishedDeletes)
	s.svc.SetReady(true)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, 2)
	go func() {
		if err := s.public.Serve(s.tcpLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	if s.unixLn != nil {
		go func() {
			if err := s.local.Serve(s.unixLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- err
			}
		}()
	}
	if !opts.DisableJanitor {
		go s.janitor.Run(runCtx, opts.SweepInterval)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	select {
	case err := <-errCh:
		return err
	case sig := <-sigCh:
		s.log.Info("shutdown signal received", "signal", sig.String())
	case <-ctx.Done():
	case <-s.stopOnce:
	}
	return s.Shutdown()
}

// Shutdown stops accepting work and lets active requests finish inside the
// configured grace period.
func (s *Server) Shutdown() error {
	grace := time.Duration(s.cfg.Server.ShutdownGraceSeconds) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	s.svc.SetReady(false)
	var firstErr error
	if err := s.public.Shutdown(ctx); err != nil {
		firstErr = err
	}
	if s.local != nil {
		if err := s.local.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if s.cfg.Server.UnixSocket != "" {
		_ = os.Remove(s.cfg.Server.UnixSocket)
	}
	if err := s.store.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	s.log.Info("shutdown complete")
	return firstErr
}

// Stop asks a running server to shut down.
func (s *Server) Stop() {
	select {
	case <-s.stopOnce:
	default:
		close(s.stopOnce)
	}
}
