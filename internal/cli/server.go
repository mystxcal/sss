package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sss/sss/internal/app"
	"github.com/sss/sss/internal/auth"
	"github.com/sss/sss/internal/client"
	"github.com/sss/sss/internal/config"
	"github.com/sss/sss/internal/observability"
	"github.com/sss/sss/internal/platform"
	"github.com/sss/sss/internal/protocol"
)

// runServe starts the daemon.
func runServe(args []string, env Env) int {
	fs := newFlagSet("serve", env)
	configPath := fs.String("config", "/etc/sss/config.toml", "path to the server configuration")
	logLevel := fs.String("log-level", "info", "log level: debug, info, warn, error")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := config.LoadServer(*configPath)
	if err != nil {
		fmt.Fprintf(env.Stderr, "sss: %v\n", err)
		return 2
	}
	log := observability.New(*logLevel)
	srv, err := app.NewServer(cfg, log, app.Options{})
	if err != nil {
		fmt.Fprintf(env.Stderr, "sss: %v\n", err)
		return 8
	}
	if err := srv.Run(context.Background(), app.Options{}); err != nil {
		fmt.Fprintf(env.Stderr, "sss: %v\n", err)
		return 8
	}
	return 0
}

// runHashPassword generates the argon2id hash for the server configuration.
func runHashPassword(args []string, env Env) int {
	fs := newFlagSet("hash-password", env)
	fromStdin := fs.Bool("password-stdin", false, "read the password from stdin")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	password, perr := resolvePassword(env, *fromStdin)
	if perr != nil {
		return fail(env, false, perr)
	}
	if len(password) < 12 {
		fmt.Fprintln(env.Stderr, "sss: warning: a base password shorter than 12 characters is weak for a public service")
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return fail(env, false, protocol.Errorf(protocol.ErrInvalidRequest, "%v", err))
	}
	fmt.Fprintln(env.Stdout, hash)
	return 0
}

// runAdmin exposes operational commands over the local socket.
func runAdmin(ctx context.Context, args []string, env Env) int {
	if len(args) == 0 {
		fmt.Fprintln(env.Stderr, "usage: sss admin <status|cleanup> [--socket PATH] [--json]")
		return 2
	}
	sub := args[0]
	fs := newFlagSet("admin "+sub, env)
	socketFlag := fs.String("socket", "", "VPS-local daemon socket path")
	jsonMode := fs.Bool("json", false, "print one JSON object to stdout")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	saved, _ := config.LoadClient()
	socket := config.ResolveSocket(*socketFlag, saved)
	c := client.NewLocal(socket)

	switch sub {
	case "status":
		status, perr := c.LocalStatus(ctx)
		if perr != nil {
			return fail(env, *jsonMode, perr)
		}
		if *jsonMode {
			writeJSONObject(env, status)
			return 0
		}
		fmt.Fprintf(env.Stdout, "version:        %s (protocol %s)\n", status.Version, status.ProtocolVersion)
		fmt.Fprintf(env.Stdout, "ready:          %t\n", status.Ready)
		fmt.Fprintf(env.Stdout, "uptime:         %ds\n", status.UptimeSeconds)
		fmt.Fprintf(env.Stdout, "committed:      %d\n", status.Committed)
		fmt.Fprintf(env.Stdout, "staging:        %d\n", status.Staging)
		fmt.Fprintf(env.Stdout, "active claims:  %d\n", status.ActiveClaims)
		fmt.Fprintf(env.Stdout, "storage:        %s (%d%% used, watermark %d%%)\n",
			status.StorageDir, status.DiskUsedPercent, status.HighWatermarkPct)
		fmt.Fprintf(env.Stdout, "disk free:      %s\n", protocol.HumanBytes(int64(status.DiskFreeBytes)))
		fmt.Fprintf(env.Stdout, "reserved:       %s\n", protocol.HumanBytes(status.ReservedBytes))
		fmt.Fprintf(env.Stdout, "admitting:      %t\n", status.AdmissionOK)
		return 0
	case "cleanup":
		result, perr := c.LocalCleanup(ctx)
		if perr != nil {
			return fail(env, *jsonMode, perr)
		}
		if *jsonMode {
			writeJSONObject(env, result)
			return 0
		}
		fmt.Fprintf(env.Stdout, "expired: %d\ndeleted: %d\nstaging cleaned: %d\ntrash emptied: %d\n",
			result.Expired, result.Deleted, result.StagingCleaned, result.TrashEmptied)
		return 0
	default:
		fmt.Fprintf(env.Stderr, "unknown admin command %q\n", sub)
		return 2
	}
}

// copyLocalPayload gives a VPS-local receiver a writable copy of the payload.
func copyLocalPayload(payload, dest string) (string, *protocol.Error) {
	clean := filepath.Clean(dest)
	if _, err := os.Lstat(clean); err == nil {
		return "", protocol.Errorf(protocol.ErrDestinationExists, "%s already exists", clean)
	}
	parent := filepath.Dir(clean)
	if info, err := os.Stat(parent); err != nil || !info.IsDir() {
		return "", protocol.Errorf(protocol.ErrDestinationExists, "%s is not an existing directory", parent)
	}
	partial := filepath.Join(parent, "."+filepath.Base(clean)+".sss-partial")
	if err := platform.RemoveAllForce(partial); err != nil {
		return "", protocol.Errorf(protocol.ErrDestinationExists, "cannot clear working directory: %v", err)
	}
	if err := os.MkdirAll(partial, 0o755); err != nil {
		return "", protocol.Errorf(protocol.ErrDestinationExists, "cannot create working directory: %v", err)
	}
	if err := platform.CopyTree(payload, partial); err != nil {
		_ = platform.RemoveAllForce(partial)
		return "", protocol.Errorf(protocol.ErrDestinationExists, "cannot copy payload: %v", err)
	}
	if err := os.Rename(partial, clean); err != nil {
		_ = platform.RemoveAllForce(partial)
		return "", protocol.Errorf(protocol.ErrDestinationExists, "cannot publish destination: %v", err)
	}
	abs, err := filepath.Abs(clean)
	if err != nil {
		return clean, nil
	}
	return abs, nil
}
