//go:build eval_smoke

// Behavioral eval for --select CLI flag (issue #709, v1.7.53).
//
// Unit tests in cmd/agent-deck/select_flag_test.go and
// internal/ui/initial_select_test.go already cover flag parsing and the
// Home.applyInitialSelection state machine. What they CAN'T express is
// whether the real binary, after argv parsing and storage load, emits the
// correct stderr warning when `--select` names a session outside the `-g`
// scope — and whether it stays silent on the happy path. That's a
// user-facing disclosure contract; this eval asserts it end-to-end against
// the real binary invoked through the sandbox harness.
package session_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/tests/eval/harness"
)

// TestEval_SelectFlag_GroupScopeWarning asserts that `-g <group> --select <id>`
// prints a stderr warning iff the session is outside the group scope.
//
// The binary never reaches the TUI (the short-lived pre-bubble-tea flow
// reads storage, emits the warning, then continues on to TUI startup),
// so we invoke it with a background spawn and read stderr — the warning
// lands before any interactive prompt so we get it immediately.
func TestEval_SelectFlag_GroupScopeWarning(t *testing.T) {
	sb := harness.NewSandbox(t)
	sb.InstallTmuxShim(t)

	// Seed three sessions in three groups so scope logic has something
	// to reason about: alpha→work, beta→personal, gamma→clients/acme.
	for _, s := range []struct{ title, group string }{
		{"alpha", "work"},
		{"beta", "personal"},
		{"gamma", "clients/acme"},
	} {
		dir := filepath.Join(sb.Home, "proj-"+s.title)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		runBin(t, sb, "add", "-c", "bash", "-t", s.title, "-g", s.group, dir)
	}

	// Scenario A: -g work + --select beta (beta is in 'personal', NOT
	// in scope). Expect the warning on stderr.
	stderrA := runBinStderrShort(t, sb, "-g", "work", "--select", "beta")
	if !strings.Contains(stderrA, `Warning: --select "beta" is not in group "work"`) {
		t.Fatalf("expected out-of-scope warning on stderr; got:\n%s", stderrA)
	}

	// Scenario B: -g work + --select alpha (alpha IS in 'work'). Expect
	// NO warning — silent happy path.
	stderrB := runBinStderrShort(t, sb, "-g", "work", "--select", "alpha")
	if strings.Contains(stderrB, "Warning: --select") {
		t.Fatalf("unexpected warning when session IS in scope; stderr:\n%s", stderrB)
	}
}

// runBinStderrShort launches the binary, lets it run just long enough to
// emit the pre-TUI warning, then kills it. The TUI itself is not reachable
// in this harness (no PTY, no alt-screen), so the process would exit on
// its own with an error — we don't care, we only want the stderr that
// lands before bubbletea initialization.
func runBinStderrShort(t *testing.T, sb *harness.Sandbox, args ...string) string {
	t.Helper()
	cmd := exec.Command(sb.BinPath, args...)
	cmd.Env = sb.Env()
	cmd.Dir = sb.Home
	// CombinedOutput is enough: the warning goes to stderr and the TUI
	// errors (no tty, etc.) come after it, so the Warning line appears
	// early in the combined stream and our substring check is robust.
	out, _ := cmd.CombinedOutput()
	return string(out)
}
