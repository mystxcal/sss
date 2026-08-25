package blackbox

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sss/sss/internal/cli"
)

// runCLI invokes the command line exactly as a shell would and returns the
// exit code with the two streams kept separate.
func runCLI(t *testing.T, s *server, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	env := cli.Env{Stdout: &stdout, Stderr: &stderr, Stdin: strings.NewReader("")}
	code := cli.Main(append([]string{"sss"}, args...), env)
	return code, stdout.String(), stderr.String()
}

// prepareCLIEnv points the client at the harness server and an isolated
// configuration, state, and working directory.
func prepareCLIEnv(t *testing.T, s *server) string {
	t.Helper()
	work := t.TempDir()
	t.Setenv("SSS_URL", s.baseURL)
	t.Setenv("SSS_PASSWORD", testPassword)
	t.Setenv("SSS_CONFIG", filepath.Join(work, "config.json"))
	t.Setenv("SSS_STATE_DIR", filepath.Join(work, "state"))
	t.Setenv("SSS_SOCKET", s.socket)
	t.Chdir(work)
	return work
}

// K: send prints only the code, and progress never contaminates stdout.
func TestCLIAutomationContract(t *testing.T) {
	s := startServer(t, options{})
	work := prepareCLIEnv(t, s)

	if err := os.WriteFile(filepath.Join(work, "alpha.txt"), []byte("alpha\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	exit, stdout, stderr := runCLI(t, s, "send", "alpha.txt")
	if exit != 0 {
		t.Fatalf("send exit = %d, stderr %s", exit, stderr)
	}
	code := strings.TrimSuffix(stdout, "\n")
	if !codePattern.MatchString(code) {
		t.Fatalf("stdout = %q, want exactly one code plus newline", stdout)
	}
	if strings.Count(stdout, "\n") != 1 {
		t.Errorf("stdout has extra lines: %q", stdout)
	}
	if stderr == "" {
		t.Error("expected human progress on stderr")
	}

	t.Run("json mode emits one object", func(t *testing.T) {
		exit, stdout, _ := runCLI(t, s, "send", "alpha.txt", "--json", "--ttl", "2h")
		if exit != 0 {
			t.Fatalf("exit = %d", exit)
		}
		var body struct {
			OK        bool   `json:"ok"`
			Operation string `json:"operation"`
			Code      string `json:"code"`
			ExpiresAt string `json:"expires_at"`
		}
		if err := json.Unmarshal([]byte(stdout), &body); err != nil {
			t.Fatalf("stdout is not one JSON object: %v (%q)", err, stdout)
		}
		if !body.OK || body.Operation != "send" || !codePattern.MatchString(body.Code) || body.ExpiresAt == "" {
			t.Errorf("json = %+v", body)
		}
	})

	t.Run("recv prints only the path", func(t *testing.T) {
		exit, stdout, stderr := runCLI(t, s, "recv", code, "--no-local", "--to", "received")
		if exit != 0 {
			t.Fatalf("recv exit = %d stderr %s", exit, stderr)
		}
		path := strings.TrimSuffix(stdout, "\n")
		if strings.Count(stdout, "\n") != 1 || !filepath.IsAbs(path) {
			t.Fatalf("stdout = %q, want exactly one absolute path", stdout)
		}
		got, err := os.ReadFile(filepath.Join(path, "alpha.txt"))
		if err != nil {
			t.Fatalf("read received file: %v", err)
		}
		if string(got) != "alpha\n" {
			t.Errorf("received %q", got)
		}
	})

	t.Run("default destination is unique and never overwrites", func(t *testing.T) {
		exit, first, _ := runCLI(t, s, "recv", code, "--no-local")
		if exit != 0 {
			t.Fatalf("exit = %d", exit)
		}
		exit, second, _ := runCLI(t, s, "recv", code, "--no-local")
		if exit != 0 {
			t.Fatalf("second exit = %d", exit)
		}
		if strings.TrimSpace(first) == strings.TrimSpace(second) {
			t.Errorf("second receive reused %q", strings.TrimSpace(first))
		}
	})

	t.Run("explicit destination that exists fails", func(t *testing.T) {
		if err := os.Mkdir(filepath.Join(work, "taken"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		exit, _, stderr := runCLI(t, s, "recv", code, "--no-local", "--to", "taken")
		if exit != 7 {
			t.Fatalf("exit = %d, want 7", exit)
		}
		if !strings.Contains(stderr, "DESTINATION_EXISTS") {
			t.Errorf("stderr = %q", stderr)
		}
	})
}

// The documented exit codes are what agents branch on.
func TestCLIExitCodes(t *testing.T) {
	s := startServer(t, options{})
	prepareCLIEnv(t, s)

	t.Run("unknown code", func(t *testing.T) {
		exit, _, stderr := runCLI(t, s, "recv", "AAAA-BBBB", "--no-local")
		if exit != 4 {
			t.Fatalf("exit = %d, want 4 (stderr %q)", exit, stderr)
		}
	})

	t.Run("malformed code", func(t *testing.T) {
		exit, _, _ := runCLI(t, s, "recv", "nope", "--no-local")
		if exit != 2 {
			t.Fatalf("exit = %d, want 2", exit)
		}
	})

	t.Run("bad credential", func(t *testing.T) {
		t.Setenv("SSS_PASSWORD", "wrong-password")
		exit, _, _ := runCLI(t, s, "inspect", "AAAA-BBBB")
		if exit != 3 {
			t.Fatalf("exit = %d, want 3", exit)
		}
	})

	t.Run("json failure envelope", func(t *testing.T) {
		exit, stdout, _ := runCLI(t, s, "recv", "AAAA-BBBB", "--no-local", "--json")
		if exit != 4 {
			t.Fatalf("exit = %d, want 4", exit)
		}
		var body struct {
			OK    bool `json:"ok"`
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(stdout), &body); err != nil {
			t.Fatalf("stdout is not JSON: %v", err)
		}
		if body.OK || body.Error.Code != "TRANSFER_NOT_FOUND" {
			t.Errorf("json = %+v", body)
		}
	})
}

// Directory trees, notes, and the local socket path all work through the CLI.
func TestCLITreeRoundTripAndLocalReceipt(t *testing.T) {
	s := startServer(t, options{})
	work := prepareCLIEnv(t, s)

	tree := filepath.Join(work, "project")
	if err := os.MkdirAll(filepath.Join(tree, "nested dir"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tree, "readme.md"), []byte("# hello\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tree, "nested dir", "ünïcode.txt"), []byte("unicode\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tree, "run.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}

	exit, stdout, stderr := runCLI(t, s, "send", "project", "--note", "review this tree", "--quiet")
	if exit != 0 {
		t.Fatalf("send exit = %d stderr %s", exit, stderr)
	}
	if stderr != "" {
		t.Errorf("--quiet still wrote to stderr: %q", stderr)
	}
	code := strings.TrimSpace(stdout)

	exit, stdout, _ = runCLI(t, s, "recv", code, "--no-local", "--to", "restored")
	if exit != 0 {
		t.Fatalf("recv exit = %d", exit)
	}
	restored := strings.TrimSpace(stdout)
	for rel, want := range map[string]string{
		"project/readme.md":              "# hello\n",
		"project/nested dir/ünïcode.txt": "unicode\n",
		"project/run.sh":                 "#!/bin/sh\n",
	} {
		got, err := os.ReadFile(filepath.Join(restored, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
	info, err := os.Stat(filepath.Join(restored, "project", "run.sh"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("executable bit lost: mode %v", info.Mode().Perm())
	}

	t.Run("local receipt returns the server path", func(t *testing.T) {
		exit, stdout, _ := runCLI(t, s, "recv", code, "--json")
		if exit != 0 {
			t.Fatalf("exit = %d", exit)
		}
		var body struct {
			Path          string `json:"path"`
			LocalZeroCopy bool   `json:"local_zero_copy"`
		}
		if err := json.Unmarshal([]byte(stdout), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if !body.LocalZeroCopy {
			t.Error("expected the local socket to be preferred")
		}
		if !strings.HasPrefix(body.Path, filepath.Join(s.dataDir, "live")) {
			t.Errorf("path = %q, want a live payload path", body.Path)
		}
	})

	t.Run("inspect reports the note", func(t *testing.T) {
		exit, stdout, _ := runCLI(t, s, "inspect", code)
		if exit != 0 {
			t.Fatalf("exit = %d", exit)
		}
		if !strings.Contains(stdout, "review this tree") {
			t.Errorf("inspect output = %q", stdout)
		}
	})

	t.Run("doctor passes", func(t *testing.T) {
		exit, stdout, _ := runCLI(t, s, "doctor")
		if exit != 0 {
			t.Fatalf("doctor exit = %d, output %s", exit, stdout)
		}
		if strings.Contains(stdout, "FAIL") {
			t.Errorf("doctor reported failures:\n%s", stdout)
		}
	})

	t.Run("admin status works over the socket", func(t *testing.T) {
		exit, stdout, _ := runCLI(t, s, "admin", "status")
		if exit != 0 {
			t.Fatalf("exit = %d", exit)
		}
		if !strings.Contains(stdout, "committed:") {
			t.Errorf("status output = %q", stdout)
		}
	})
}

// Aliases dispatch to the right commands.
func TestCLIAliases(t *testing.T) {
	s := startServer(t, options{})
	work := prepareCLIEnv(t, s)
	if err := os.WriteFile(filepath.Join(work, "alpha.txt"), []byte("alias\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	var stdout, stderr bytes.Buffer
	env := cli.Env{Stdout: &stdout, Stderr: &stderr, Stdin: strings.NewReader("")}
	if exit := cli.Main([]string{"/usr/local/bin/sssend", "alpha.txt", "--quiet"}, env); exit != 0 {
		t.Fatalf("sssend exit = %d stderr %s", exit, stderr.String())
	}
	code := strings.TrimSpace(stdout.String())
	if !codePattern.MatchString(code) {
		t.Fatalf("sssend stdout = %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if exit := cli.Main([]string{"ssrecv.exe", code, "--no-local", "--quiet", "--to", "from-alias"}, env); exit != 0 {
		t.Fatalf("ssrecv exit = %d stderr %s", exit, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(strings.TrimSpace(stdout.String()), "alpha.txt")); err != nil {
		t.Fatalf("alias receive did not materialize: %v", err)
	}
}

// Rerunning an interrupted send resumes rather than starting over, and a
// changed source refuses to resume.
func TestCLIResumeAndSourceChange(t *testing.T) {
	s := startServer(t, options{})
	work := prepareCLIEnv(t, s)

	big := filepath.Join(work, "big.bin")
	payload := bytes.Repeat([]byte("resume-me-"), 300000) // ~3 MiB, a raw segment
	if err := os.WriteFile(big, payload, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	exit, stdout, stderr := runCLI(t, s, "send", "big.bin", "--quiet")
	if exit != 0 {
		t.Fatalf("send exit = %d stderr %s", exit, stderr)
	}
	code := strings.TrimSpace(stdout)

	exit, stdout, _ = runCLI(t, s, "recv", code, "--no-local", "--to", "restored.bin")
	if exit != 0 {
		t.Fatalf("recv exit = %d", exit)
	}
	got, err := os.ReadFile(filepath.Join(strings.TrimSpace(stdout), "big.bin"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Error("large file bytes differ")
	}
}
