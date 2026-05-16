package session

// Failing tests for issue #610: `agent-deck list --json` and
// `agent-deck session show <id> --json` report wrong status (idle/waiting)
// for sessions that the TUI and web API /api/menu correctly report as
// running.
//
// Root cause trace (see .planning/fix-issue-610/PLAN.md for the full
// data-flow analysis):
//
//   TUI   backgroundStatusUpdate → tmux.RefreshPaneInfoCache()
//                                → hookWatcher.GetHookStatus + UpdateHookStatus
//                                → inst.UpdateStatus()           ← title fast-path hits
//   Web   SessionDataService.refreshStatuses → tmux.RefreshPaneInfoCache()
//                                → defaultLoadHookStatuses + UpdateHookStatus
//                                → inst.UpdateStatus()           ← title fast-path hits
//   CLI   handleList / handleShow → inst.UpdateStatus()          ← cache cold, hook stale
//
// When Claude is mid-tool-execution the only reliable running-state signal
// is the braille spinner embedded in the pane title (set by Claude Code via
// OSC sequences). The TUI/web populate the pane-title cache before each
// UpdateStatus tick; the CLI never does. As a result the CLI's GetStatus
// falls through the title fast path, the hook fast path is stale (>2min,
// because Claude only emits "running" on UserPromptSubmit), and content-scan
// on the bottom of the pane misses the busy indicator for long tool calls.
//
// Fix surface: introduce session.RefreshInstancesForCLIStatus(instances)
// — the CLI analogue of SessionDataService.refreshStatuses — and call it
// from handleList and the session-show JSON emitter before the UpdateStatus
// loop. Until that helper exists this file fails to compile, which is the
// intended red state for TDD.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// writeHookFile writes a hook status file under the test-scoped HOME so
// readHookStatusFile picks it up via GetHooksDir().
func writeHookFile(t *testing.T, instanceID, status string, tsSecondsAgo int) {
	t.Helper()
	hooksDir := GetHooksDir()
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	ts := time.Now().Add(-time.Duration(tsSecondsAgo) * time.Second).Unix()
	body := fmt.Sprintf(
		`{"status":%q,"session_id":"sess-610","event":"UserPromptSubmit","ts":%d}`,
		status, ts,
	)
	path := filepath.Join(hooksDir, instanceID+".json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write hook: %v", err)
	}
}

// setPaneTitle injects a pane title via `tmux select-pane -T`. Claude Code
// normally does this with OSC escape sequences while it is actively working;
// the tests fake the same state.
func setPaneTitle(t *testing.T, tmuxSession, title string) {
	t.Helper()
	cmd := exec.Command("tmux", "select-pane", "-t", tmuxSession, "-T", title)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("select-pane -T %q: %v\n%s", title, err, out)
	}
}

// TestUpdateStatus_CLIParity_SpinnerTitle_StaleHook reproduces the core
// symptom of issue #610: when the hook fast path is stale and the pane
// title carries a braille spinner (Claude "working" signal), the CLI cold
// path must still report StatusRunning.
//
// Required behavior after fix:
//
//	RefreshInstancesForCLIStatus(instances) warms the title cache (and loads
//	hook files from disk) so the subsequent UpdateStatus sees the spinner
//	via the title fast-path — identical to what the TUI and web already do.
func TestUpdateStatus_CLIParity_SpinnerTitle_StaleHook(t *testing.T) {
	// Requires only a live tmux server; TestMain bootstraps one, so skip
	// only when tmux is entirely missing. This was the F3 silent-skip trap.
	skipIfNoTmuxBinary(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	inst := NewInstanceWithTool("issue610-spinner", tmpHome, "claude")
	if err := inst.tmuxSession.Start("sleep 3600"); err != nil {
		t.Fatalf("tmux start: %v", err)
	}
	defer func() { _ = inst.tmuxSession.Kill() }()

	// Stale running hook (3 minutes old, past the 2-minute fast-path window).
	// Matches real-world behavior: Claude only emits "running" on
	// UserPromptSubmit, so a long-running tool call leaves the hook stale.
	writeHookFile(t, inst.ID, "running", 180)

	// Simulate Claude's OSC title sequence while working.
	setPaneTitle(t, inst.tmuxSession.Name, "⠋ Working on refactor")

	// Past the 1.5-second grace period inside UpdateStatus.
	time.Sleep(2 * time.Second)

	// CLI entry point: the fix must expose a helper that parallels
	// SessionDataService.refreshStatuses and Home.backgroundStatusUpdate,
	// and handleList / session-show must call it before the UpdateStatus
	// loop. Until then this symbol does not exist and the file will not
	// compile — the intended TDD red.
	RefreshInstancesForCLIStatus([]*Instance{inst})

	if err := inst.UpdateStatus(); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	if got := inst.GetStatusThreadSafe(); got != StatusRunning {
		t.Errorf(
			"issue #610: CLI cold path reported %q, want %q. "+
				"Pane title carries a braille spinner; TUI and web API report "+
				"\"running\" for this exact state.",
			got, StatusRunning,
		)
	}
}

// TestUpdateStatus_CLIvsTUIParity_SameTmuxState verifies that the CLI and
// TUI paths produce the same Status for a session in the same underlying
// tmux state. Direct parity assertion: scripts that consume `list --json`
// must see the same answer the TUI shows on screen.
//
// Both entry points run against a single tmux session:
//   - TUI path:   tmux.RefreshPaneInfoCache + UpdateHookStatus + UpdateStatus
//   - CLI path:   RefreshInstancesForCLIStatus + UpdateStatus
//
// On main the CLI path has no equivalent to RefreshPaneInfoCache, so even
// once the helper exists the two must produce identical output for the
// same pane state. The fix lands green only when CLI output == TUI output.
func TestUpdateStatus_CLIvsTUIParity_SameTmuxState(t *testing.T) {
	// See TestUpdateStatus_CLIParity_SpinnerTitle_StaleHook for rationale.
	skipIfNoTmuxBinary(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// Shared tmux state: create the session once. Two Instance wrappers
	// point at the same tmux session name, mirroring the real world where
	// the TUI and CLI are separate processes loading the same sessions.json.
	base := NewInstanceWithTool("issue610-parity", tmpHome, "claude")
	if err := base.tmuxSession.Start("sleep 3600"); err != nil {
		t.Fatalf("tmux start: %v", err)
	}
	defer func() { _ = base.tmuxSession.Kill() }()

	writeHookFile(t, base.ID, "running", 180)
	setPaneTitle(t, base.tmuxSession.Name, "⠴ Running tool")
	time.Sleep(2 * time.Second)

	tuiInst := reloadInstanceForParityTest(base)
	cliInst := reloadInstanceForParityTest(base)

	// Run CLI path FIRST so the tmux pane-info cache is cold — mirrors a
	// real agent-deck list invocation where the CLI is a fresh OS process
	// with its own (empty) tmux package globals. If the TUI path ran first
	// in this binary it would warm the package-level cache and mask the
	// parity gap.
	//
	// --- CLI path (handleList / session show --json) ---
	RefreshInstancesForCLIStatus([]*Instance{cliInst})
	if err := cliInst.UpdateStatus(); err != nil {
		t.Fatalf("CLI UpdateStatus: %v", err)
	}
	cliStatus := cliInst.GetStatusThreadSafe()

	// --- TUI path (internal/ui/home.go:backgroundStatusUpdate) ---
	tmux.RefreshPaneInfoCache()
	if hs := readHookStatusFile(tuiInst.ID); hs != nil {
		tuiInst.UpdateHookStatus(hs)
	}
	if err := tuiInst.UpdateStatus(); err != nil {
		t.Fatalf("TUI UpdateStatus: %v", err)
	}
	tuiStatus := tuiInst.GetStatusThreadSafe()

	if tuiStatus != StatusRunning {
		// Sanity: if the TUI path itself does not report Running on this
		// setup, the test oracle is wrong — bail before trusting the parity
		// check.
		t.Fatalf(
			"test oracle broken: TUI path did not report running for a "+
				"spinner-title session; got %q. Check tmux.RefreshPaneInfoCache "+
				"and the AnalyzePaneTitle contract before blaming CLI parity.",
			tuiStatus,
		)
	}

	if cliStatus != tuiStatus {
		t.Errorf(
			"issue #610 parity break: TUI path=%q, CLI path=%q for the same "+
				"tmux state. list --json must match /api/menu.",
			tuiStatus, cliStatus,
		)
	}
}

// reloadInstanceForParityTest constructs a second Instance wrapper pointing
// at the same underlying tmux session as base — simulates what
// ReconnectSessionLazy does across process boundaries (TUI vs CLI as
// separate OS processes reading the same sessions.json).
func reloadInstanceForParityTest(base *Instance) *Instance {
	return reloadInstanceForParityTestWithPrev(base, "idle")
}

// reloadInstanceForParityTestWithPrev mirrors reloadInstanceForParityTest
// but lets the caller specify the persisted-status string passed to
// tmux.ReconnectSessionLazy. The previousStatus governs the initial
// acknowledged flag (see internal/tmux/tmux.go:1396): "idle" → ack=true,
// "waiting"/"active" → ack=false. Production callers persist the last seen
// Status into sessions.json and feed it back here on next process start;
// without parameterizing this the parity tests for Waiting state would
// silently degrade to Idle because the reloaded session would be marked
// already-acknowledged.
func reloadInstanceForParityTestWithPrev(base *Instance, previousStatus string) *Instance {
	reloaded := &Instance{
		ID:          base.ID,
		Title:       base.Title,
		ProjectPath: base.ProjectPath,
		GroupPath:   base.GroupPath,
		Tool:        base.Tool,
		Status:      StatusIdle,
		CreatedAt:   time.Now().Add(-10 * time.Second), // past grace window
	}
	reloaded.tmuxSession = tmux.ReconnectSessionLazy(
		base.tmuxSession.Name,
		base.Title,
		base.ProjectPath,
		base.Tool,
		previousStatus,
	)
	reloaded.tmuxSession.InstanceID = base.ID
	return reloaded
}

// --- v1.9 T4: Waiting / Error / Idle parity tests --------------------------
//
// The existing Running-state parity tests above cover the issue #610 fix.
// The plan T4 (V1.9-PRIORITY-PLAN.md) flagged that the parity matrix has
// zero meaningful coverage for the other three derived states the user
// surface depends on:
//
//   StatusWaiting — the user's "needs my attention" signal
//   StatusError   — the user's "session is dead, restart it" signal
//   StatusIdle    — the user's "nothing happening, can be detached" signal
//
// Each of these has its own derivation path (hook fast-path with
// non-acknowledged tmuxSession, dead-session detection, shell-tool tmux
// fallthrough). Without parity tests, the CLI/TUI/web split that caused
// #610 can re-emerge silently in any of these directions.
//
// These tests mirror the Running-state test pattern (CLI path runs first
// against a cold tmux package state, then TUI path verifies same state).

// runParitySetup returns a fresh tmux session under a test HOME, suitable
// for the three parity tests below. It returns the base instance and a
// cleanup func.
func runParitySetup(t *testing.T, idSuffix, tool string) (*Instance, func()) {
	t.Helper()
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	inst := NewInstanceWithTool("issue876-parity-"+idSuffix, tmpHome, tool)
	if err := inst.tmuxSession.Start("sleep 3600"); err != nil {
		t.Fatalf("tmux start: %v", err)
	}
	cleanup := func() { _ = inst.tmuxSession.Kill() }
	// Past the 1.5-second grace window inside UpdateStatus, so subsequent
	// status checks aren't short-circuited to Starting.
	time.Sleep(2 * time.Second)
	return inst, cleanup
}

// runParityProbe drives one Instance through the CLI cold-load path and a
// second Instance (sharing the same tmux state) through the TUI path. It
// returns the Status each path produced. The CLI path runs FIRST so the
// tmux package-level cache is genuinely cold on entry — without that, the
// TUI's cache warm-up would leak into the CLI run and mask parity gaps.
//
// previousStatus is fed to ReconnectSessionLazy and controls the initial
// acknowledged flag on each reloaded tmux session — see
// reloadInstanceForParityTestWithPrev.
func runParityProbe(t *testing.T, base *Instance, previousStatus string) (cli, tui Status) {
	t.Helper()
	cliInst := reloadInstanceForParityTestWithPrev(base, previousStatus)
	tuiInst := reloadInstanceForParityTestWithPrev(base, previousStatus)

	// --- CLI path (handleList / session show --json) ---
	RefreshInstancesForCLIStatus([]*Instance{cliInst})
	if err := cliInst.UpdateStatus(); err != nil {
		// inactive tmux returns nil from GetStatus, so any error here is
		// a test infra failure not a state assertion failure.
		t.Fatalf("CLI UpdateStatus: %v", err)
	}
	cli = cliInst.GetStatusThreadSafe()

	// --- TUI path (internal/ui/home.go:backgroundStatusUpdate) ---
	tmux.RefreshPaneInfoCache()
	if hs := readHookStatusFile(tuiInst.ID); hs != nil {
		tuiInst.UpdateHookStatus(hs)
	}
	if err := tuiInst.UpdateStatus(); err != nil {
		t.Fatalf("TUI UpdateStatus: %v", err)
	}
	tui = tuiInst.GetStatusThreadSafe()
	return cli, tui
}

// TestUpdateStatus_CLIvsTUIParity_Waiting locks in the parity contract for
// the StatusWaiting derivation: a fresh "waiting" hook from a Claude Stop
// event with no acknowledgment must surface as StatusWaiting on BOTH the
// CLI cold-load path and the TUI inotify-fed path.
//
// Drift surface: if the CLI's RefreshInstancesForCLIStatus stops calling
// UpdateHookStatus, or if UpdateHookStatus's "Stop" handling diverges from
// the cold-load block in UpdateStatus (instance.go:2854 vs the dispatch at
// :2876), one path will start reporting Idle while the other reports
// Waiting — exactly the issue-class behind #876 and #610.
func TestUpdateStatus_CLIvsTUIParity_Waiting(t *testing.T) {
	skipIfNoTmuxBinary(t)

	base, cleanup := runParitySetup(t, "waiting", "claude")
	defer cleanup()

	// Fresh "waiting" hook (well within the 2-min fast-path window) from a
	// Stop event. Stop is the canonical "task complete, awaiting next
	// prompt" signal and must surface as StatusWaiting until the user
	// acknowledges. tsSecondsAgo=10 (NOT 0) keeps the timestamp strictly
	// in the past so isNewEvent comparisons are well-defined.
	writeHookFile(t, base.ID, "waiting", 10)
	// Crucial: the writeHookFile helper hardcodes event="UserPromptSubmit".
	// For Waiting state we want event="Stop" — rewrite the file to set
	// the event correctly, otherwise UpdateHookStatus's Permission/Notification
	// branch will reset acknowledged and we'd be testing the wrong path.
	overrideHookEvent(t, base.ID, "waiting", "Stop", 10)

	// previousStatus="waiting" mirrors the production case the test is
	// gating: sessions.json said the session was last seen Waiting, the
	// process restarted, ReconnectSessionLazy initializes acknowledged=false,
	// and the fresh waiting hook should keep it in StatusWaiting. With the
	// default "idle" the reloaded session would be acknowledged=true and
	// the fast-path would (correctly for that scenario) report Idle —
	// but that's a different test.
	cli, tui := runParityProbe(t, base, "waiting")

	if tui != StatusWaiting {
		t.Fatalf(
			"test oracle broken: TUI path reported %q, want %q. The hook fast "+
				"path (instance.go:2885) maps fresh waiting+Stop hooks to Waiting "+
				"when not acknowledged — investigate before blaming CLI parity.",
			tui, StatusWaiting,
		)
	}
	if cli != tui {
		t.Errorf(
			"v1.9 T4 parity break (Waiting state): TUI=%q, CLI=%q for the "+
				"same hook+tmux state. list --json must match the TUI's view "+
				"or scripts that gate on Waiting will see flickering disagreement.",
			tui, cli,
		)
	}
}

// TestUpdateStatus_CLIvsTUIParity_Idle locks in the parity contract for
// the StatusIdle derivation. Drift surface: the cold-load block in
// UpdateStatus (instance.go:2863) calls ResetAcknowledged for "waiting"
// hooks, while UpdateHookStatus's Stop branch preserves acknowledgment.
// If the CLI ever bypasses the explicit UpdateHookStatus call in
// RefreshInstancesForCLIStatus, the cold-load reset would fire and the
// CLI would report Waiting where the TUI reports Idle.
//
// We model "Idle" as: Claude session, fresh waiting+Stop hook, user has
// already acknowledged via Acknowledge(). Both surfaces must report Idle.
func TestUpdateStatus_CLIvsTUIParity_Idle(t *testing.T) {
	skipIfNoTmuxBinary(t)

	base, cleanup := runParitySetup(t, "idle", "claude")
	defer cleanup()

	// Fresh waiting+Stop hook (same shape as the Waiting test, but the
	// session is acknowledged by the user — so the fast-path must produce
	// Idle, not Waiting).
	writeHookFile(t, base.ID, "waiting", 10)
	overrideHookEvent(t, base.ID, "waiting", "Stop", 10)

	// previousStatus="idle" mirrors the production "user already
	// acknowledged this Stop, persisted as Idle" case: ReconnectSessionLazy
	// initializes acknowledged=true, the fresh waiting+Stop hook does NOT
	// reset it (UpdateHookStatus's reset path only fires for
	// PermissionRequest/Notification), and the fast-path correctly produces
	// Idle on both surfaces.
	cli, tui := runParityProbe(t, base, "idle")

	if tui != StatusIdle {
		t.Fatalf(
			"test oracle broken: TUI path reported %q, want %q. Acknowledged "+
				"Claude session with fresh waiting hook must hit Idle branch "+
				"of the hook fast-path (instance.go:2899).",
			tui, StatusIdle,
		)
	}
	if cli != tui {
		t.Errorf(
			"v1.9 T4 parity break (Idle state): TUI=%q, CLI=%q for the same "+
				"acknowledged hook state. Likely cause: cold-load ResetAcknowledged "+
				"(instance.go:2863) is firing on the CLI path because hookStatus "+
				"was empty when UpdateStatus ran — RefreshInstancesForCLIStatus "+
				"must call UpdateHookStatus before UpdateStatus.",
			tui, cli,
		)
	}
}

// TestUpdateStatus_CLIvsTUIParity_Error locks in the parity contract for
// dead-session detection. Drift surface: UpdateStatus's tmux.Exists() check
// (instance.go:2824) is shared by both paths, so this test catches future
// short-circuits that bypass the existence check on one path (e.g., a
// cache that returns last-known status without rechecking).
func TestUpdateStatus_CLIvsTUIParity_Error(t *testing.T) {
	skipIfNoTmuxBinary(t)

	base, cleanup := runParitySetup(t, "error", "claude")
	// Kill the underlying tmux session — cleanup is still called for
	// idempotence but the Kill below is what this test depends on.
	defer cleanup()

	// Construct the parity wrappers BEFORE killing the tmux session so
	// both wrappers can resolve the (now-defunct) name through the same
	// tmux package globals — mirrors the real-world race where a session
	// dies between sessions.json load and status refresh. previousStatus
	// is irrelevant to the Error test: dead-session detection short-circuits
	// before any acknowledgment logic runs.
	cliInst := reloadInstanceForParityTestWithPrev(base, "idle")
	tuiInst := reloadInstanceForParityTestWithPrev(base, "idle")

	if err := base.tmuxSession.Kill(); err != nil {
		t.Fatalf("tmux kill: %v", err)
	}
	// Give tmux a moment to actually tear down the session record.
	time.Sleep(200 * time.Millisecond)

	// CLI path
	RefreshInstancesForCLIStatus([]*Instance{cliInst})
	if err := cliInst.UpdateStatus(); err != nil {
		t.Fatalf("CLI UpdateStatus: %v", err)
	}
	cliStatus := cliInst.GetStatusThreadSafe()

	// TUI path
	tmux.RefreshPaneInfoCache()
	if hs := readHookStatusFile(tuiInst.ID); hs != nil {
		tuiInst.UpdateHookStatus(hs)
	}
	if err := tuiInst.UpdateStatus(); err != nil {
		t.Fatalf("TUI UpdateStatus: %v", err)
	}
	tuiStatus := tuiInst.GetStatusThreadSafe()

	if tuiStatus != StatusError {
		t.Fatalf(
			"test oracle broken: TUI path reported %q for a killed tmux "+
				"session, want %q. Check tmux.Session.Exists() before blaming "+
				"CLI parity.",
			tuiStatus, StatusError,
		)
	}
	if cliStatus != tuiStatus {
		t.Errorf(
			"v1.9 T4 parity break (Error state): TUI=%q, CLI=%q for the "+
				"same dead tmux session. /api/menu, list --json, and the TUI "+
				"row colour must agree on Error or users will see ghost rows.",
			tuiStatus, cliStatus,
		)
	}
}

// overrideHookEvent rewrites the hook status file with a custom event field.
// writeHookFile hardcodes event="UserPromptSubmit"; some parity tests need
// "Stop" or other events to exercise specific branches of UpdateHookStatus.
func overrideHookEvent(t *testing.T, instanceID, status, event string, tsSecondsAgo int) {
	t.Helper()
	hooksDir := GetHooksDir()
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	ts := time.Now().Add(-time.Duration(tsSecondsAgo) * time.Second).Unix()
	body := fmt.Sprintf(
		`{"status":%q,"session_id":"sess-876-parity","event":%q,"ts":%d}`,
		status, event, ts,
	)
	path := filepath.Join(hooksDir, instanceID+".json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write hook: %v", err)
	}
}
