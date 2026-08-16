package conductor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Regression test for https://github.com/asheshgoplani/agent-deck/issues/1854.
//
// conductor/setup.sh reported "Could not register <title> (add manually from
// TUI)" for a session that was already registered and needed nothing done — an
// instruction that would itself fail.
//
// The python existence check above the `add` exits non-zero both when the
// session genuinely does not exist AND when the probe itself could not run, so
// `add` is reached in two very different situations. Before #1850 a duplicate
// exited 0 and printed the ok line (wrong but harmless); #1850 made it exit
// non-zero with an ALREADY_EXISTS payload, which took the warn branch, and
// `2>/dev/null` discarded the message that would have said so.
//
// These tests drive the real register_session function out of setup.sh with a
// fake `agent-deck` on PATH.

// extractShellFunc pulls one top-level function out of a shell script so the
// test can source it without executing the whole setup.
func extractShellFunc(t *testing.T, script, name string) string {
	t.Helper()
	raw, err := os.ReadFile(script)
	if err != nil {
		t.Fatalf("read %s: %v", script, err)
	}
	lines := strings.Split(string(raw), "\n")
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(l, name+"() {") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("%s does not define %s()", script, name)
	}
	for i := start; i < len(lines); i++ {
		if lines[i] == "}" {
			return strings.Join(lines[start:i+1], "\n")
		}
	}
	t.Fatalf("unterminated %s() in %s", name, script)
	return ""
}

// runRegisterSession sources register_session with a fake `agent-deck` that
// exits with exitCode after printing stdoutText, and returns the combined
// output.
func runRegisterSession(t *testing.T, exitCode int, stdoutText string) string {
	t.Helper()

	binDir := t.TempDir()
	fake := "#!/usr/bin/env bash\ncat <<'JSON'\n" + stdoutText + "\nJSON\nexit " + itoa(exitCode) + "\n"
	if err := os.WriteFile(filepath.Join(binDir, "agent-deck"), []byte(fake), 0o755); err != nil {
		t.Fatalf("write fake agent-deck: %v", err)
	}

	body := extractShellFunc(t, "setup.sh", "register_session")
	script := `#!/usr/bin/env bash
set -euo pipefail
GREEN=''; YELLOW=''; NC=''
ok()   { echo "[ok] $*"; }
warn() { echo "[warn] $*"; }
` + body + `
register_session "testprofile" "/tmp/profile-dir" "conductor-title"
`
	path := filepath.Join(binDir, "drive.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write driver: %v", err)
	}

	cmd := exec.Command("bash", path)
	cmd.Env = append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("register_session driver failed (it must never fail the script): %v\n%s", err, out)
	}
	return string(out)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

// TestRegisterSession_AlreadyRegisteredIsNotAFailure is the reported bug.
func TestRegisterSession_AlreadyRegisteredIsNotAFailure(t *testing.T) {
	out := runRegisterSession(t, 1, `{"success":false,"error":"session already exists: \"conductor-title\" at /tmp/profile-dir (id: abc123)","code":"ALREADY_EXISTS"}`)

	if strings.Contains(out, "Could not register") {
		t.Fatalf("an already-registered session was reported as a registration failure, telling the user to do something that would fail:\n%s", out)
	}
	if !strings.Contains(out, "[ok]") {
		t.Fatalf("an already-registered session should report ok:\n%s", out)
	}
	if !strings.Contains(out, "already registered") {
		t.Errorf("the ok line should distinguish 'already registered' from 'registered':\n%s", out)
	}
}

// TestRegisterSession_FreshRegistrationReportsOk guards the success path.
func TestRegisterSession_FreshRegistrationReportsOk(t *testing.T) {
	out := runRegisterSession(t, 0, `{"success":true,"id":"abc123"}`)

	if !strings.Contains(out, "[ok]") || strings.Contains(out, "Could not register") {
		t.Fatalf("a fresh registration was not reported as success:\n%s", out)
	}
	if strings.Contains(out, "already registered") {
		t.Errorf("a fresh registration was reported as already registered:\n%s", out)
	}
}

// TestRegisterSession_RealFailureStillWarnsAndShowsTheError keeps the branch
// honest for genuine failures, and surfaces the error text that `2>/dev/null`
// used to discard on the one path whose whole job is reporting a failure.
func TestRegisterSession_RealFailureStillWarnsAndShowsTheError(t *testing.T) {
	out := runRegisterSession(t, 1, `{"success":false,"error":"path does not exist: /tmp/profile-dir","code":"INVALID_OPERATION"}`)

	if !strings.Contains(out, "Could not register") {
		t.Fatalf("a genuine failure was reported as success:\n%s", out)
	}
	if !strings.Contains(out, "path does not exist") {
		t.Errorf("the real error text was discarded, leaving the user nothing to act on:\n%s", out)
	}
}

// TestRegisterSession_NonJSONFailureStillWarns: a crash that prints no JSON at
// all must not be mistaken for ALREADY_EXISTS.
func TestRegisterSession_NonJSONFailureStillWarns(t *testing.T) {
	out := runRegisterSession(t, 2, `panic: something went very wrong`)

	if !strings.Contains(out, "Could not register") {
		t.Fatalf("a non-JSON crash was not reported as a failure:\n%s", out)
	}
}
