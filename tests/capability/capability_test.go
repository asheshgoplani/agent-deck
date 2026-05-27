//go:build capability_e2e

// Package capability holds capability-level end-to-end tests for agent-deck.
//
// A capability test refuses to mock the thing under test. For each action
// agent-deck promises a user can perform, one test runs the real action
// through the compiled binary against a real tmux server on an isolated
// socket, then asserts on the real effect: registry rows in state.db or the
// live tmux pane content, the same surface a human would see.
//
// The suite is gated behind the `capability_e2e` build tag so it stays out of
// the default `go test ./...` run (mirroring the eval_smoke tier). Run it with:
//
//	go test -tags capability_e2e ./tests/capability/...
//
// or via scripts/capability-e2e.sh, which also emits the manifest and
// regenerates the dashboard. See
// docs/testing/2026-05-26-capability-e2e-strategy.md for the design.
//
// Isolation discipline (non-negotiable, see the 2026-04-17 cascade incident):
// every test goes through harness.Sandbox + Sandbox.InstallTmuxShim, which
// forces every tmux invocation onto a per-test socket via `-S`, and through
// testutil.IsolateTmuxSocket in TestMain, which unsets TMUX so a spawned
// binary can never join the user's real tmux server.
package capability

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
	"github.com/asheshgoplani/agent-deck/tests/eval/harness"
)

func TestMain(m *testing.M) {
	cleanup := testutil.IsolateTmuxSocket()
	code := m.Run()
	cleanup()
	os.Exit(code)
}

// capSandbox wraps harness.Sandbox with a real-tmux shim installed and a
// scratch project directory ready for use.
type capSandbox struct {
	*harness.Sandbox
	WorkDir string
}

// newCapSandbox builds an isolated sandbox, installs the tmux shim so all
// pane spawns land on the per-test socket, and creates a scratch project dir
// inside the sandbox HOME. It deliberately does NOT set [tmux].socket_name,
// because that would make the binary emit its own `-L name` which would win
// over the shim's `-S` and break isolation.
func newCapSandbox(t *testing.T) *capSandbox {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH")
	}
	sb := harness.NewSandbox(t)
	sb.InstallTmuxShim(t)

	work := filepath.Join(sb.Home, "project")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}
	return &capSandbox{Sandbox: sb, WorkDir: work}
}

// run executes the agent-deck binary with the sandbox env and fails the test
// on a non-zero exit.
func (c *capSandbox) run(t *testing.T, args ...string) string {
	t.Helper()
	out, err := c.try(args...)
	if err != nil {
		t.Fatalf("agent-deck %v: %v\n%s", args, err, out)
	}
	return out
}

// try is the best-effort variant: it returns combined output and error
// without failing the test. Used for negative cases and cleanup.
func (c *capSandbox) try(args ...string) (string, error) {
	cmd := exec.Command(c.BinPath, args...)
	cmd.Env = c.Env()
	cmd.Dir = c.Home
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// sessionRow is the subset of `agent-deck list --json` fields the capability
// tests assert on.
type sessionRow struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
	Tool   string `json:"tool"`
	Path   string `json:"path"`
	Group  string `json:"group"`
}

// list returns all registry rows via `list --json`.
func (c *capSandbox) list(t *testing.T) []sessionRow {
	t.Helper()
	out := c.run(t, "list", "--json")
	out = strings.TrimSpace(out)
	// With zero sessions the CLI prints a human "No sessions found" line rather
	// than an empty JSON array, so treat any non-JSON output as no rows.
	if out == "" || out == "null" || (!strings.HasPrefix(out, "[") && !strings.HasPrefix(out, "{")) {
		return nil
	}
	var rows []sessionRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("parse list --json: %v\nraw: %s", err, out)
	}
	return rows
}

// findByTitle returns the registry row with the given title, or ok=false.
func (c *capSandbox) findByTitle(t *testing.T, title string) (sessionRow, bool) {
	t.Helper()
	for _, r := range c.list(t) {
		if r.Title == title {
			return r, true
		}
	}
	return sessionRow{}, false
}

// tmuxSessionNames returns the names of every tmux session on the sandbox
// socket that agent-deck owns (the agentdeck_ prefix).
func (c *capSandbox) tmuxSessionNames(t *testing.T) []string {
	t.Helper()
	out, err := c.TmuxTry("list-sessions", "-F", "#{session_name}")
	if err != nil {
		// No server running yet is not an error for our purposes.
		return nil
	}
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "agentdeck_") {
			names = append(names, line)
		}
	}
	return names
}

// waitForTmuxSession polls until an agentdeck_ tmux session appears (or the
// deadline passes) and returns its name. Empty string means none appeared.
func (c *capSandbox) waitForTmuxSession(t *testing.T, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if names := c.tmuxSessionNames(t); len(names) > 0 {
			return names[0]
		}
		time.Sleep(100 * time.Millisecond)
	}
	return ""
}

// waitForNoTmuxSession polls until zero agentdeck_ tmux sessions remain.
func (c *capSandbox) waitForNoTmuxSession(t *testing.T, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if len(c.tmuxSessionNames(t)) == 0 {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// stopQuietly best-effort stops a session so the shared tmux server exits
// cleanly. Failures are non-fatal; Sandbox.teardown kill-servers as a backstop.
func (c *capSandbox) stopQuietly(ref string) {
	_, _ = c.try("session", "stop", ref)
}

// snapshotDir is where capability tests write their per-capability terminal
// snapshots. tools/capability-report reads "<id>.txt" from here when it
// regenerates the dashboard. It is overridable via CAPABILITY_SNAPSHOT_DIR so
// the gate script can point it at a collected location; the default is the
// committed testdata path (relative to this package's working directory), so a
// bare `go test -tags capability_e2e ./tests/capability/...` also produces the
// snapshots the Verify step expects.
func snapshotDir() string {
	if d := os.Getenv("CAPABILITY_SNAPSHOT_DIR"); d != "" {
		return d
	}
	return filepath.Join("testdata", "snapshots")
}

// snapshot records the real terminal/CLI content visible at a test's
// verification point, keyed by capability id. It is DISPLAY proof only: the
// pass/fail assertion lives on registry/pane state, never on this text. Writes
// are best-effort so a snapshot failure never masks the real assertion result.
func snapshot(t *testing.T, id, content string) {
	t.Helper()
	dir := snapshotDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Logf("snapshot %s: mkdir %s: %v (display-only, ignoring)", id, dir, err)
		return
	}
	if err := os.WriteFile(filepath.Join(dir, id+".txt"), []byte(content), 0o644); err != nil {
		t.Logf("snapshot %s: write: %v (display-only, ignoring)", id, err)
	}
}
