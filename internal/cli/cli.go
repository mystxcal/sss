// Package cli implements the user-facing commands. It owns interaction only:
// every transfer behavior lives in the client and transfer packages.
package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"github.com/sss/sss/internal/client"
	"github.com/sss/sss/internal/config"
	"github.com/sss/sss/internal/protocol"
	"github.com/sss/sss/internal/version"
)

// Env carries the streams a command writes to, so tests can capture output.
type Env struct {
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader
}

// Default returns the process streams.
func Default() Env { return Env{Stdout: os.Stdout, Stderr: os.Stderr, Stdin: os.Stdin} }

const usage = `sss - ephemeral store-and-forward artifact relay

Usage:
  sss <command> [options]

Commands:
  send <paths...>      Upload files or directories and print the handoff code
  recv <code>          Download a handoff and print the final path
  inspect <code>       Show note, expiry, file count, and total bytes
  configure            Save the server URL
  doctor               Check configuration, connectivity, and compatibility
  admin status         Show server status through the local socket
  admin cleanup        Run an immediate cleanup pass through the local socket
  serve                Run the daemon
  hash-password        Generate a password hash for the server configuration
  version              Print version information

Aliases:
  sssend  = sss send
  ssrecv  = sss recv
  sssd    = sss serve

Authentication (in precedence order):
  --password-stdin, then $SSS_PASSWORD, then an interactive prompt.

Server URL (in precedence order):
  --url, then $SSS_URL, then the saved client configuration.
`

// Main dispatches a command line, including the alias entry points, and
// returns the process exit code.
func Main(argv []string, env Env) int {
	if len(argv) == 0 {
		fmt.Fprint(env.Stderr, usage)
		return 2
	}
	program := strings.ToLower(filepath.Base(argv[0]))
	program = strings.TrimSuffix(program, ".exe")
	args := argv[1:]
	switch program {
	case "sssend":
		args = append([]string{"send"}, args...)
	case "ssrecv":
		args = append([]string{"recv"}, args...)
	case "sssd":
		args = append([]string{"serve"}, args...)
	}
	if len(args) == 0 {
		fmt.Fprint(env.Stderr, usage)
		return 2
	}

	ctx := context.Background()
	switch args[0] {
	case "send":
		return runSend(ctx, args[1:], env)
	case "recv", "receive":
		return runRecv(ctx, args[1:], env)
	case "inspect":
		return runInspect(ctx, args[1:], env)
	case "configure":
		return runConfigure(args[1:], env)
	case "doctor":
		return runDoctor(ctx, args[1:], env)
	case "admin":
		return runAdmin(ctx, args[1:], env)
	case "serve":
		return runServe(args[1:], env)
	case "hash-password":
		return runHashPassword(args[1:], env)
	case "version", "--version", "-v":
		fmt.Fprintf(env.Stdout, "sss %s\ncommit %s\nbuilt %s\nprotocol %s\n",
			version.Version, version.Commit, version.BuildDate, version.Protocol)
		return 0
	case "help", "--help", "-h":
		fmt.Fprint(env.Stdout, usage)
		return 0
	default:
		fmt.Fprintf(env.Stderr, "unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}

// fail renders an error according to the output mode and returns the documented
// exit code. The stable error code is always more precise than the exit code.
func fail(env Env, jsonMode bool, err *protocol.Error) int {
	if jsonMode {
		body := map[string]any{"ok": false, "error": err}
		enc := json.NewEncoder(env.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(body)
		return protocol.ExitCode(err.Code)
	}
	fmt.Fprintf(env.Stderr, "sss: %s\n", err.PlainLine())
	if err.RequestID != "" {
		fmt.Fprintf(env.Stderr, "sss: request id %s\n", err.RequestID)
	}
	return protocol.ExitCode(err.Code)
}

// usageError reports invalid command-line usage.
func usageError(env Env, jsonMode bool, format string, args ...any) int {
	return fail(env, jsonMode, protocol.Errorf(protocol.ErrInvalidRequest, format, args...))
}

// resolvePassword applies the documented authentication precedence. A plain
// --password flag is deliberately not offered: it would leak the secret into
// shell history and process listings.
func resolvePassword(env Env, fromStdin bool) (string, *protocol.Error) {
	if fromStdin {
		data, err := io.ReadAll(io.LimitReader(env.Stdin, 4096))
		if err != nil {
			return "", protocol.Errorf(protocol.ErrAuthRequired, "could not read the password from stdin")
		}
		password := strings.TrimRight(string(data), "\r\n")
		if password == "" {
			return "", protocol.Errorf(protocol.ErrAuthRequired, "no password was supplied on stdin")
		}
		return password, nil
	}
	if p := os.Getenv("SSS_PASSWORD"); p != "" {
		return p, nil
	}
	if f, ok := env.Stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(env.Stderr, "Password: ")
		data, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(env.Stderr)
		if err == nil && len(data) > 0 {
			return string(data), nil
		}
	}
	return "", protocol.Errorf(protocol.ErrAuthRequired,
		"no password available; set SSS_PASSWORD or use --password-stdin")
}

// remoteClient builds an authenticated client for the resolved server URL.
func remoteClient(env Env, urlFlag string, passwordStdin bool) (*client.Client, *protocol.Error) {
	saved, err := config.LoadClient()
	if err != nil {
		return nil, protocol.Errorf(protocol.ErrInvalidRequest, "%v", err)
	}
	base, err := config.ResolveURL(urlFlag, saved)
	if err != nil {
		return nil, protocol.Errorf(protocol.ErrInvalidRequest, "%v", err)
	}
	password, perr := resolvePassword(env, passwordStdin)
	if perr != nil {
		return nil, perr
	}
	return client.New(base, password), nil
}

// progressFunc returns a phase reporter honoring the quiet and JSON contracts.
// Progress, notes, and warnings always go to stderr; stdout stays machine-clean.
func progressFunc(env Env, quiet, jsonMode bool) client.Progress {
	if quiet || jsonMode {
		return nil
	}
	return func(phase, detail string) {
		if detail == "" {
			fmt.Fprintf(env.Stderr, "%s...\n", phase)
			return
		}
		fmt.Fprintf(env.Stderr, "%s: %s\n", phase, detail)
	}
}

func newFlagSet(name string, env Env) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	return fs
}

// parseArgs parses options that appear before, between, or after positional
// arguments, so `sssend ./a ./b --ttl 2h` works exactly as documented.
// Everything after a bare "--" is positional.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var tail []string
	for i, a := range args {
		if a == "--" {
			tail = append(tail, args[i+1:]...)
			args = args[:i]
			break
		}
	}
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			break
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
	return append(positional, tail...), nil
}

func stateDir() string {
	dir, err := config.StateDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "sss-state")
	}
	return dir
}
