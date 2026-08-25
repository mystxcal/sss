package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/sss/sss/internal/client"
	"github.com/sss/sss/internal/config"
	"github.com/sss/sss/internal/protocol"
)

// runSend uploads files or directories and prints only the code on stdout.
func runSend(ctx context.Context, args []string, env Env) int {
	fs := newFlagSet("send", env)
	note := fs.String("note", "", "handoff note")
	noteFile := fs.String("note-file", "", "read the handoff note from a file")
	ttl := fs.String("ttl", "", "expiry, e.g. 30m, 2h, or 3000m (default 30m)")
	jsonMode := fs.Bool("json", false, "print one JSON object to stdout")
	quiet := fs.Bool("quiet", false, "suppress progress on stderr")
	passwordStdin := fs.Bool("password-stdin", false, "read the password from stdin")
	urlFlag := fs.String("url", "", "server URL")
	roots, err := parseArgs(fs, args)
	if err != nil {
		return 2
	}
	if len(roots) == 0 {
		return usageError(env, *jsonMode, "send requires at least one file or directory")
	}
	if *note != "" && *noteFile != "" {
		return usageError(env, *jsonMode, "--note and --note-file are mutually exclusive")
	}
	noteText := *note
	if *noteFile != "" {
		data, readErr := os.ReadFile(*noteFile)
		if readErr != nil {
			return fail(env, *jsonMode, protocol.Errorf(protocol.ErrInvalidRequest, "cannot read note file: %v", readErr))
		}
		noteText = strings.TrimRight(string(data), "\r\n")
	}
	minutes := protocol.DefaultTTLMinutes
	if *ttl != "" {
		parsed, perr := protocol.ParseTTL(*ttl)
		if perr != nil {
			return fail(env, *jsonMode, perr)
		}
		minutes = parsed
	}

	c, perr := remoteClient(env, *urlFlag, *passwordStdin)
	if perr != nil {
		return fail(env, *jsonMode, perr)
	}
	result, perr := client.Send(ctx, c, client.SendOptions{
		Roots:      roots,
		Note:       noteText,
		TTLMinutes: minutes,
		StateDir:   stateDir(),
		Progress:   progressFunc(env, *quiet, *jsonMode),
	})
	if perr != nil {
		return fail(env, *jsonMode, perr)
	}
	if *jsonMode {
		writeJSONObject(env, map[string]any{
			"ok":         true,
			"operation":  "send",
			"code":       result.Code,
			"expires_at": result.ExpiresAt.Format(time.RFC3339),
			"files":      result.Files,
			"bytes":      result.Bytes,
		})
		return 0
	}
	if !*quiet {
		fmt.Fprintf(env.Stderr, "Sent %s in %s; expires %s\n",
			protocol.HumanBytes(result.Bytes),
			plural(result.Files, "file", "files"),
			result.ExpiresAt.Local().Format(time.RFC3339))
	}
	fmt.Fprintln(env.Stdout, result.Code)
	return 0
}

// runRecv downloads a handoff and prints only the final path on stdout.
func runRecv(ctx context.Context, args []string, env Env) int {
	fs := newFlagSet("recv", env)
	to := fs.String("to", "", "destination path")
	jsonMode := fs.Bool("json", false, "print one JSON object to stdout")
	quiet := fs.Bool("quiet", false, "suppress progress on stderr")
	passwordStdin := fs.Bool("password-stdin", false, "read the password from stdin")
	urlFlag := fs.String("url", "", "server URL")
	socketFlag := fs.String("socket", "", "VPS-local daemon socket path")
	noLocal := fs.Bool("no-local", false, "ignore the local socket and use HTTPS")
	positional, err := parseArgs(fs, args)
	if err != nil {
		return 2
	}
	if len(positional) != 1 {
		return usageError(env, *jsonMode, "recv requires exactly one code")
	}
	code := positional[0]
	if _, ok := protocol.NormalizeCode(code); !ok {
		return fail(env, *jsonMode, protocol.Errorf(protocol.ErrInvalidCode, "code is not eight valid characters"))
	}
	progress := progressFunc(env, *quiet, *jsonMode)

	// Prefer the VPS-local socket when it exists and speaks a compatible
	// protocol. Locality is never inferred from DNS or hostname.
	saved, _ := config.LoadClient()
	socket := config.ResolveSocket(*socketFlag, saved)
	if !*noLocal && client.LocalReachable(ctx, socket) {
		return recvLocal(ctx, env, socket, code, *to, *jsonMode, *quiet, progress)
	}

	c, perr := remoteClient(env, *urlFlag, *passwordStdin)
	if perr != nil {
		return fail(env, *jsonMode, perr)
	}
	result, perr := client.Recv(ctx, c, client.RecvOptions{
		Code:        code,
		Destination: *to,
		Progress:    progress,
	})
	if perr != nil {
		return fail(env, *jsonMode, perr)
	}
	if *jsonMode {
		writeJSONObject(env, map[string]any{
			"ok":              true,
			"operation":       "recv",
			"code":            protocol.FormatCode(mustNormalize(code)),
			"path":            result.Path,
			"local_zero_copy": false,
			"files":           result.Files,
			"bytes":           result.Bytes,
			"note":            result.Note,
		})
		return 0
	}
	if !*quiet {
		if result.Note != "" {
			fmt.Fprintf(env.Stderr, "Note: %s\n", result.Note)
		}
		fmt.Fprintf(env.Stderr, "Received %s in %s\n",
			protocol.HumanBytes(result.Bytes), plural(result.Files, "file", "files"))
	}
	fmt.Fprintln(env.Stdout, result.Path)
	return 0
}

// recvLocal returns the already-materialized VPS path instead of downloading.
// With --to, the caller wants a writable copy, so the payload is copied out.
func recvLocal(ctx context.Context, env Env, socket, code, to string, jsonMode, quiet bool, progress client.Progress) int {
	c := client.NewLocal(socket)
	claim, perr := c.LocalPath(ctx, code)
	if perr != nil {
		return fail(env, jsonMode, perr)
	}
	path := claim.Path
	zeroCopy := true
	if to != "" {
		if progress != nil {
			progress(client.PhaseFinalizing, "copying payload to "+to)
		}
		dest, perr := copyLocalPayload(claim.Path, to)
		if perr != nil {
			return fail(env, jsonMode, perr)
		}
		path = dest
		zeroCopy = false
	}
	if jsonMode {
		writeJSONObject(env, map[string]any{
			"ok":              true,
			"operation":       "recv",
			"code":            claim.Code,
			"path":            path,
			"local_zero_copy": zeroCopy,
			"read_only":       zeroCopy,
			"lease_until":     claim.LeaseUntil.Format(time.RFC3339),
		})
		return 0
	}
	if !quiet && zeroCopy {
		fmt.Fprintf(env.Stderr, "VPS-local receipt: existing read-only payload, no copy made\n")
	}
	fmt.Fprintln(env.Stdout, path)
	return 0
}

// runInspect shows the note, expiry, and content summary for a code.
func runInspect(ctx context.Context, args []string, env Env) int {
	fs := newFlagSet("inspect", env)
	jsonMode := fs.Bool("json", false, "print one JSON object to stdout")
	passwordStdin := fs.Bool("password-stdin", false, "read the password from stdin")
	urlFlag := fs.String("url", "", "server URL")
	positional, err := parseArgs(fs, args)
	if err != nil {
		return 2
	}
	if len(positional) != 1 {
		return usageError(env, *jsonMode, "inspect requires exactly one code")
	}
	c, perr := remoteClient(env, *urlFlag, *passwordStdin)
	if perr != nil {
		return fail(env, *jsonMode, perr)
	}
	meta, perr := c.Metadata(ctx, positional[0])
	if perr != nil {
		return fail(env, *jsonMode, perr)
	}
	if *jsonMode {
		writeJSONObject(env, meta)
		return 0
	}
	fmt.Fprintf(env.Stdout, "Code:      %s\n", meta.Code)
	if meta.Note != "" {
		fmt.Fprintf(env.Stdout, "Note:      %s\n", meta.Note)
	}
	fmt.Fprintf(env.Stdout, "Committed: %s\n", meta.CommittedAt.Local().Format(time.RFC3339))
	fmt.Fprintf(env.Stdout, "Expires:   %s (in %s)\n",
		meta.ExpiresAt.Local().Format(time.RFC3339), time.Until(meta.ExpiresAt).Round(time.Minute))
	fmt.Fprintf(env.Stdout, "Files:     %d\n", meta.FileCount)
	fmt.Fprintf(env.Stdout, "Size:      %s\n", protocol.HumanBytes(meta.TotalBytes))
	shown := 0
	for _, e := range meta.Entries {
		if e.Type != protocol.EntryFile {
			continue
		}
		if shown == 20 {
			fmt.Fprintf(env.Stdout, "           ... and %d more\n", meta.FileCount-shown)
			break
		}
		fmt.Fprintf(env.Stdout, "           %s (%s)\n", e.Path, protocol.HumanBytes(e.Size))
		shown++
	}
	return 0
}

// runConfigure saves the server URL for later commands.
func runConfigure(args []string, env Env) int {
	fs := newFlagSet("configure", env)
	urlFlag := fs.String("url", "", "server URL, e.g. https://drop.example.com")
	socketFlag := fs.String("socket", "", "VPS-local daemon socket path")
	jsonMode := fs.Bool("json", false, "print one JSON object to stdout")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	saved, err := config.LoadClient()
	if err != nil {
		return fail(env, *jsonMode, protocol.Errorf(protocol.ErrInvalidRequest, "%v", err))
	}
	if *urlFlag == "" && *socketFlag == "" {
		return usageError(env, *jsonMode, "configure requires --url or --socket")
	}
	if *urlFlag != "" {
		resolved, err := config.ResolveURL(*urlFlag, config.Client{})
		if err != nil {
			return fail(env, *jsonMode, protocol.Errorf(protocol.ErrInvalidRequest, "%v", err))
		}
		saved.URL = resolved
	}
	if *socketFlag != "" {
		saved.UnixSocket = *socketFlag
	}
	path, err := config.SaveClient(saved)
	if err != nil {
		return fail(env, *jsonMode, protocol.Errorf(protocol.ErrInvalidRequest, "%v", err))
	}
	if *jsonMode {
		writeJSONObject(env, map[string]any{"ok": true, "operation": "configure", "config": path, "url": saved.URL})
		return 0
	}
	fmt.Fprintf(env.Stderr, "Saved configuration to %s\n", path)
	fmt.Fprintln(env.Stdout, saved.URL)
	return 0
}

// runDoctor checks everything a working setup needs.
func runDoctor(ctx context.Context, args []string, env Env) int {
	fs := newFlagSet("doctor", env)
	jsonMode := fs.Bool("json", false, "print one JSON object to stdout")
	passwordStdin := fs.Bool("password-stdin", false, "read the password from stdin")
	urlFlag := fs.String("url", "", "server URL")
	socketFlag := fs.String("socket", "", "VPS-local daemon socket path")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	type check struct {
		Name   string `json:"name"`
		OK     bool   `json:"ok"`
		Detail string `json:"detail"`
	}
	var checks []check
	add := func(name string, ok bool, detail string) { checks = append(checks, check{name, ok, detail}) }

	saved, err := config.LoadClient()
	if err != nil {
		add("configuration", false, err.Error())
	} else {
		path, _ := config.ClientConfigPath()
		add("configuration", true, path)
	}
	base, urlErr := config.ResolveURL(*urlFlag, saved)
	if urlErr != nil {
		add("server url", false, urlErr.Error())
	} else {
		add("server url", true, base)
		switch {
		case strings.HasPrefix(base, "https://"):
			add("tls", true, "https")
		case isLoopbackURL(base):
			// A loopback daemon is reached without a proxy; plaintext there
			// never leaves the host.
			add("tls", true, "plaintext http to loopback (no TLS required)")
		default:
			add("tls", false, "plaintext http to a remote host")
		}
	}

	socket := config.ResolveSocket(*socketFlag, saved)
	localOK := client.LocalReachable(ctx, socket)
	add("local socket", localOK, socket)

	allOK := urlErr == nil
	if urlErr == nil {
		password, perr := resolvePassword(env, *passwordStdin)
		if perr != nil {
			add("authentication", false, perr.Message)
			allOK = false
		} else {
			c := client.New(base, password)
			info, perr := c.Info(ctx)
			switch {
			case perr == nil:
				add("authentication", true, "accepted")
				add("protocol", true, info.ProtocolVersion+" (server "+info.ApplicationVersion+")")
				accepting, _ := info.Limits["accepting_transfers"].(bool)
				add("server admission", accepting, map[bool]string{
					true: "accepting new transfers", false: "not accepting new transfers (disk pressure)"}[accepting])
				if !accepting {
					allOK = false
				}
			case perr.Code == protocol.ErrProtocolMismatch:
				add("protocol", false, perr.Message)
				allOK = false
			case perr.Code == protocol.ErrAuthInvalid || perr.Code == protocol.ErrAuthRequired:
				add("authentication", false, perr.Message)
				allOK = false
			default:
				add("reachability", false, perr.Message)
				allOK = false
			}
		}
	}

	if *jsonMode {
		writeJSONObject(env, map[string]any{"ok": allOK, "operation": "doctor", "checks": checks})
	} else {
		for _, c := range checks {
			mark := "ok  "
			if !c.OK {
				mark = "FAIL"
			}
			fmt.Fprintf(env.Stdout, "[%s] %-18s %s\n", mark, c.Name, c.Detail)
		}
	}
	if !allOK {
		return protocol.ExitCode(protocol.ErrNetwork)
	}
	return 0
}

func writeJSONObject(env Env, body any) {
	enc := json.NewEncoder(env.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(body)
}

func mustNormalize(code string) string {
	canonical, _ := protocol.NormalizeCode(code)
	return canonical
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// isLoopbackURL reports whether a base URL points at this host.
func isLoopbackURL(base string) bool {
	u, err := url.Parse(base)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
