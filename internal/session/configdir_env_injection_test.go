package session

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Host-side CLAUDE_CONFIG_DIR injection (issue #1791).
//
// CLAUDE_CONFIG_DIR was applied to the spawned launch *command* (as an
// inline `CLAUDE_CONFIG_DIR=x claude ...` prefix or, on the custom-command
// path, an `export ...;` prefix baked into the one-shot spawn string) but
// never re-asserted host-side the way AGENTDECK_INSTANCE_ID/AGENTDECK_PROFILE
// are (tmux set-environment). A fleet survey found it missing from the pane
// shell environment in 14/40 sampled panes. Restarting `claude` by hand
// inside the pane (Ctrl-C, then `claude`) then falls back to the ambient
// ~/.claude root: wrong billed account, and a transcript the deck's own
// resume logic can no longer find.
//
// ensureClaudeConfigDirEnv is the tool-agnostic (Claude-compatible-gated)
// safety net (mirroring ensureProfileEnv) that every spawn/respawn success
// path now calls.

// TestEnsureClaudeConfigDirEnv_SetsHostSideEnv pins that ensureClaudeConfigDirEnv
// writes CLAUDE_CONFIG_DIR into the live tmux session environment when the
// instance has an explicit, resolved config dir (here: the CLAUDE_CONFIG_DIR
// env var, priority level 4 of GetClaudeConfigDirForInstance).
//
// Uses a plain shell session (NewInstance, generic /bin/sh — no claude binary
// needed in CI, matching TestEnsureProfileEnv_SetsHostSideEnv's pattern in
// profile_env_injection_test.go) and flips Tool to "claude" post-Start so the
// host-side env write is under test without depending on a live claude pane.
func TestEnsureClaudeConfigDirEnv_SetsHostSideEnv(t *testing.T) {
	skipIfNoTmuxBinary(t)
	isolateUserHomeForShellRestart(t)

	wantDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", wantDir)

	title := uniqueShellTestTitle("EnsureConfigDirEnv")
	inst := NewInstance(title, t.TempDir())
	inst.Command = ""
	if err := inst.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	t.Cleanup(func() { cleanupShellSessions(title) })

	if !waitForTmuxSession(inst.tmuxSession.Name, 1*time.Second) {
		t.Fatalf("tmux session %q never appeared after Start", inst.tmuxSession.Name)
	}
	inst.Tool = "claude"

	// Seed a stale value first so we prove ensureClaudeConfigDirEnv is what
	// sets it, isolating the helper every respawn branch calls.
	if err := inst.tmuxSession.SetEnvironment("CLAUDE_CONFIG_DIR", "stale"); err != nil {
		t.Fatalf("seed SetEnvironment failed: %v", err)
	}

	inst.ensureClaudeConfigDirEnv()

	got, err := inst.tmuxSession.GetEnvironment("CLAUDE_CONFIG_DIR")
	if err != nil {
		t.Fatalf("GetEnvironment(CLAUDE_CONFIG_DIR) failed: %v", err)
	}
	if got != wantDir {
		t.Errorf("CLAUDE_CONFIG_DIR in tmux env = %q, want %q", got, wantDir)
	}
}

// TestEnsureClaudeConfigDirEnv_NilTmuxSession_NoPanic pins the nil guard: a
// respawn branch must never panic if the instance has no tmux session.
func TestEnsureClaudeConfigDirEnv_NilTmuxSession_NoPanic(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/tmp/whatever")
	inst := NewInstanceWithTool("test", "/tmp/test", "claude")
	inst.tmuxSession = nil
	inst.ensureClaudeConfigDirEnv() // must not panic
}

// TestEnsureClaudeConfigDirEnv_NonClaudeTool_DoesNotSet pins the
// IsClaudeCompatible gate: CLAUDE_CONFIG_DIR is Claude-specific, so a
// non-Claude-compatible tool (e.g. gemini) must never get it injected
// host-side even if CLAUDE_CONFIG_DIR happens to be set in the ambient env.
// Uses a plain shell session (NewInstance, generic /bin/sh — no gemini
// binary needed in CI) and flips Tool to "gemini" post-Start so only the
// gate itself is under test, mirroring the shell-session pattern
// profile_env_injection_test.go uses for the tmux-dependent profile tests.
func TestEnsureClaudeConfigDirEnv_NonClaudeTool_DoesNotSet(t *testing.T) {
	skipIfNoTmuxBinary(t)
	isolateUserHomeForShellRestart(t)

	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())

	title := uniqueShellTestTitle("ConfigDirEnvNonClaudeGate")
	inst := NewInstance(title, t.TempDir())
	inst.Command = ""
	if err := inst.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	t.Cleanup(func() { cleanupShellSessions(title) })

	if !waitForTmuxSession(inst.tmuxSession.Name, 1*time.Second) {
		t.Fatalf("tmux session %q never appeared after Start", inst.tmuxSession.Name)
	}

	inst.Tool = "gemini"
	inst.ensureClaudeConfigDirEnv()

	if _, err := inst.tmuxSession.GetEnvironment("CLAUDE_CONFIG_DIR"); err == nil {
		t.Errorf("CLAUDE_CONFIG_DIR should not be set host-side for a non-Claude-compatible tool")
	}
}

// TestEnsureClaudeConfigDirEnv_NoExplicitConfigDir_DoesNotSet pins the other
// gate: when no priority level resolves an explicit config dir for the
// instance (IsClaudeConfigDirExplicitForInstance is false, e.g. ambient
// ~/.claude with no env/TOML override), the host-side var must not be set at
// all — setting CLAUDE_CONFIG_DIR="" would itself be a footgun (breaks the
// "no override, use claude's own default" case some callers rely on).
func TestEnsureClaudeConfigDirEnv_NoExplicitConfigDir_DoesNotSet(t *testing.T) {
	skipIfNoTmuxBinary(t)
	isolateUserHomeForShellRestart(t)
	// Explicitly ensure no CLAUDE_CONFIG_DIR is inherited from the test host.
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	title := uniqueShellTestTitle("ConfigDirEnvGate")
	inst := NewInstance(title, t.TempDir())
	inst.Command = ""
	if err := inst.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	t.Cleanup(func() { cleanupShellSessions(title) })

	if !waitForTmuxSession(inst.tmuxSession.Name, 1*time.Second) {
		t.Fatalf("tmux session %q never appeared after Start", inst.tmuxSession.Name)
	}
	inst.Tool = "claude"

	inst.ensureClaudeConfigDirEnv()

	if _, err := inst.tmuxSession.GetEnvironment("CLAUDE_CONFIG_DIR"); err == nil {
		t.Errorf("CLAUDE_CONFIG_DIR should not be set host-side when no explicit config dir resolves for the instance")
	}
}

// TestEnsureClaudeConfigDirEnv_GateCloses_ClearsStaleValue pins #1822 F7:
// when a previously-open gate closes (tool changes away from Claude, or the
// instance no longer has an explicit config dir resolved), a stale
// CLAUDE_CONFIG_DIR left in the tmux session env from an earlier call must
// be unset — not merely left alone — so a later respawn never inherits an
// account override that no longer applies.
func TestEnsureClaudeConfigDirEnv_GateCloses_ClearsStaleValue(t *testing.T) {
	skipIfNoTmuxBinary(t)
	isolateUserHomeForShellRestart(t)

	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())

	title := uniqueShellTestTitle("ConfigDirGateCloses")
	inst := NewInstance(title, t.TempDir())
	inst.Command = ""
	if err := inst.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	t.Cleanup(func() { cleanupShellSessions(title) })

	if !waitForTmuxSession(inst.tmuxSession.Name, 1*time.Second) {
		t.Fatalf("tmux session %q never appeared after Start", inst.tmuxSession.Name)
	}

	inst.Tool = "claude"
	inst.ensureClaudeConfigDirEnv()
	if _, err := inst.tmuxSession.GetEnvironment("CLAUDE_CONFIG_DIR"); err != nil {
		t.Fatalf("precondition: CLAUDE_CONFIG_DIR should be set before the gate closes, GetEnvironment error: %v", err)
	}

	// Gate closes: tool is no longer Claude-compatible.
	inst.Tool = "gemini"
	inst.ensureClaudeConfigDirEnv()

	if _, err := inst.tmuxSession.GetEnvironment("CLAUDE_CONFIG_DIR"); err == nil {
		t.Error("CLAUDE_CONFIG_DIR should have been cleared from the tmux session env once the tool changed away from Claude")
	}
}

// TestBuildBashExportPrefix_ExportsClaudeConfigDir covers the custom-command /
// conductor wrapper path, which exports CLAUDE_CONFIG_DIR via `export VAR=...;`
// (not a bare command-prefix assignment) so a hand-restart inside the pane
// inherits it too. Locks the existing behavior this fix's host-side net
// complements — a regression here would silently reopen the gap for the
// custom-command spawn path specifically.
func TestBuildBashExportPrefix_ExportsClaudeConfigDir(t *testing.T) {
	wantDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", wantDir)

	inst := NewInstanceWithTool("test", "/tmp/test", "claude")
	prefix := inst.buildBashExportPrefix(false)

	if !strings.Contains(prefix, "export CLAUDE_CONFIG_DIR="+wantDir+";") {
		t.Errorf("bash export prefix should export CLAUDE_CONFIG_DIR=%s, got: %s", wantDir, prefix)
	}
}

// TestClaudeConfigDirExport_VisibleInRunningPaneProcess is the #1822 F6
// pane-PROCESS regression test — every other test in this file (and
// TestSession_SetAndGetEnvironment in internal/tmux) only asserts against
// tmux's SESSION environment table (`tmux show-environment` /
// GetEnvironment), which is not the same thing as what a shell process
// running inside a pane actually sees in its own env. `tmux set-environment`
// only affects panes/processes tmux spawns AFTER the call; it cannot mutate
// an already-running pane's process environment (tmux(1)). Codex flagged
// this gap in review (PR #1822): "the resolved directory must be exported
// in the launch command before the pane starts" — this test proves both
// halves of that claim against a REAL running pane process (send-keys +
// echo + capture-pane, reading $CLAUDE_CONFIG_DIR from the pane's own shell,
// never GetEnvironment):
//
//  1. tmux set-environment (ensureClaudeConfigDirEnv's host-side re-assert,
//     #1791) does NOT reach an already-running pane process — confirming
//     why that mechanism alone cannot fix a pane that already exists.
//  2. `export CLAUDE_CONFIG_DIR=...` executed directly in the pane process
//     — the mechanism buildBashExportPrefix/buildClaudeResumeCommand now
//     use for continue/resume/-r/restart (#1822 F5, replacing the old bare
//     `CLAUDE_CONFIG_DIR=x claude` single-command prefix) — DOES persist in
//     that process's own env, including across `exec "$SHELL"`, which is
//     exactly what wrapExitToShell's fallback (#1161) execs into.
func TestClaudeConfigDirExport_VisibleInRunningPaneProcess(t *testing.T) {
	skipIfNoTmuxBinary(t)
	isolateUserHomeForShellRestart(t)

	title := uniqueShellTestTitle("ConfigDirPaneProcess")
	inst := NewInstance(title, t.TempDir())
	inst.Command = ""
	if err := inst.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	t.Cleanup(func() { cleanupShellSessions(title) })

	if !waitForTmuxSession(inst.tmuxSession.Name, 1*time.Second) {
		t.Fatalf("tmux session %q never appeared after Start", inst.tmuxSession.Name)
	}
	paneName := inst.tmuxSession.Name

	// 1. tmux set-environment (session-level) must NOT reach the already-
	// running pane process.
	if err := inst.tmuxSession.SetEnvironment("CLAUDE_CONFIG_DIR", "session-level-value"); err != nil {
		t.Fatalf("SetEnvironment: %v", err)
	}
	out := sendToPaneAndCapture(t, paneName, "echo PANE1=[$CLAUDE_CONFIG_DIR]")
	if strings.Contains(out, "PANE1=[session-level-value]") {
		t.Fatal("tmux set-environment unexpectedly reached the running pane process — test assumption invalid, or a tmux version quirk")
	}

	// 2. `export` run directly in the pane process DOES persist, including
	// across exec into a replacement shell (the exit-to-shell fallback).
	wantDir := "/tmp/pane-process-config-dir-1822"
	sendToPaneAndCapture(t, paneName, "export CLAUDE_CONFIG_DIR="+wantDir)
	sendToPaneAndCapture(t, paneName, `exec "$SHELL" -i`)
	time.Sleep(300 * time.Millisecond) // let the re-exec'd interactive shell come up
	out = sendToPaneAndCapture(t, paneName, "echo PANE2=[$CLAUDE_CONFIG_DIR]")
	if !strings.Contains(out, "PANE2=["+wantDir+"]") {
		t.Fatalf("expected exported CLAUDE_CONFIG_DIR to survive exec into the fallback shell, got pane content: %s", out)
	}
}

// sendToPaneAndCapture types cmd into the named tmux pane, waits briefly for
// it to execute, and returns the captured pane content. Uses the bare `tmux`
// binary directly (not inst.tmuxSession's helpers) so it reads the pane
// PROCESS's own terminal output rather than any tmux-session-level API —
// TMUX_TMPDIR isolation (TestMain) applies process-wide via env, so this is
// no less isolated than the tmux calls waitForTmuxSession/cleanupShellSessions
// already make the same way.
func sendToPaneAndCapture(t *testing.T, paneName, cmd string) string {
	t.Helper()
	if err := exec.Command("tmux", "send-keys", "-t", paneName, cmd, "Enter").Run(); err != nil {
		t.Fatalf("send-keys(%q) failed: %v", cmd, err)
	}
	time.Sleep(300 * time.Millisecond)
	out, err := exec.Command("tmux", "capture-pane", "-p", "-t", paneName).Output()
	if err != nil {
		t.Fatalf("capture-pane failed: %v", err)
	}
	return string(out)
}
