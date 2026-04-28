package session

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Tests in this file pin the regression that owns this branch
// (feature/sessions-dispear-on-restart). User report: pressing R on a
// busted Shell-type session leaves the TUI showing "no tmux session
// running" even though `tmux ls` confirms the underlying tmux session
// is alive on the default socket.
//
// All tests use a unique title per run to avoid collisions on a host
// that might already have agent-deck sessions running.

// TestRestart_ShellSession_PostRestartIsHealthy is the basic contract:
// after Restart() on a Shell instance, Status is not Error, the tmux
// session backing the instance exists, and exactly one tmux session
// matches the title prefix (no orphans).
func TestRestart_ShellSession_PostRestartIsHealthy(t *testing.T) {
	skipIfNoTmuxBinary(t)
	isolateUserHomeForShellRestart(t)

	title := uniqueShellTestTitle("HealthyRestart")
	inst := NewInstance(title, t.TempDir())
	if inst.Tool != "shell" {
		t.Fatalf("setup: NewInstance default Tool = %q, want shell", inst.Tool)
	}
	inst.Command = ""

	if err := inst.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	t.Cleanup(func() { cleanupShellSessions(title) })

	if !waitForTmuxSession(inst.tmuxSession.Name, 1*time.Second) {
		t.Fatalf("tmux session %q never appeared after Start", inst.tmuxSession.Name)
	}

	if err := inst.Restart(); err != nil {
		t.Fatalf("Restart returned error: %v", err)
	}

	// Settle: respawn-pane / new-session is async on some platforms.
	if !waitForTmuxSession(inst.tmuxSession.Name, 1*time.Second) {
		t.Fatalf("tmux session %q does not exist after Restart", inst.tmuxSession.Name)
	}

	if inst.Status == StatusError {
		t.Errorf("after Restart, Status = %s; want != error", inst.Status)
	}
	if !inst.tmuxSession.Exists() {
		t.Errorf("after Restart, inst.tmuxSession.Exists() = false; expected live session %q",
			inst.tmuxSession.Name)
	}

	matches := listTmuxSessionsWithPrefix(tmux.SessionPrefix + sanitizeTitleForPrefix(title) + "_")
	if len(matches) != 1 {
		t.Errorf("after Restart, expected exactly 1 tmux session matching prefix; got %d (%v) — orphan check",
			len(matches), matches)
	}
}

// TestRestart_ShellSession_AdoptsLiveTmuxOnNameMismatch reproduces the
// user's reported state: Instance.tmuxSession.Name does NOT point at any
// live tmux session, but a live tmux session matching the title prefix
// DOES exist on the same socket. Pressing R must heal the instance —
// either by adopting the live session, or by killing it and creating a
// fresh one — never silently leave the orphan running while the
// instance stays in StatusError.
func TestRestart_ShellSession_AdoptsLiveTmuxOnNameMismatch(t *testing.T) {
	skipIfNoTmuxBinary(t)
	isolateUserHomeForShellRestart(t)

	title := uniqueShellTestTitle("AdoptOnMismatch")
	inst := NewInstance(title, t.TempDir())
	inst.Command = ""

	if err := inst.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	t.Cleanup(func() { cleanupShellSessions(title) })

	if !waitForTmuxSession(inst.tmuxSession.Name, 1*time.Second) {
		t.Fatalf("tmux session %q never appeared after Start", inst.tmuxSession.Name)
	}
	liveName := inst.tmuxSession.Name

	// Mutate inst.tmuxSession.Name to a stale value that does not exist.
	// This exactly reproduces the user-reported state where agent-deck's
	// view of "my tmux session is dead" disagrees with reality.
	staleSess := tmux.NewSession(title, t.TempDir())
	staleSess.SocketName = inst.TmuxSocketName
	if staleSess.Name == liveName {
		t.Skip("setup: random suffix collision picked the live name; rerun")
	}
	inst.tmuxSession = staleSess

	if inst.tmuxSession.Exists() {
		t.Skip("setup: stale name unexpectedly resolved to a live session; rerun")
	}

	if err := inst.Restart(); err != nil {
		t.Fatalf("Restart returned error on stale-name adoption path: %v", err)
	}

	if inst.Status == StatusError {
		t.Errorf("after Restart, Status = error; expected the adoption path to heal status")
	}

	matches := listTmuxSessionsWithPrefix(tmux.SessionPrefix + sanitizeTitleForPrefix(title) + "_")
	_ = liveName
	if len(matches) > 1 {
		t.Errorf("after Restart, expected at most 1 tmux session matching prefix; got %d (%v) — orphan",
			len(matches), matches)
	}
	if len(matches) == 0 {
		t.Errorf("after Restart, expected exactly 1 tmux session matching prefix; got 0")
	}
}

// --- helpers ---

func uniqueShellTestTitle(tag string) string {
	return fmt.Sprintf("ShellRestart-%s-%d", tag, time.Now().UnixNano())
}

// sanitizeTitleForPrefix mirrors tmux.sanitizeName for the limited
// alphabet our titles use ([A-Za-z0-9-] passes through unchanged).
func sanitizeTitleForPrefix(title string) string { return title }

func cleanupShellSessions(title string) {
	for _, name := range listTmuxSessionsWithPrefix(tmux.SessionPrefix + sanitizeTitleForPrefix(title) + "_") {
		_ = exec.Command("tmux", "kill-session", "-t", name).Run()
	}
}

func listTmuxSessionsWithPrefix(prefix string) []string {
	out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}").Output()
	if err != nil {
		return nil
	}
	var matches []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			matches = append(matches, line)
		}
	}
	return matches
}

func waitForTmuxSession(name string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if exec.Command("tmux", "has-session", "-t", name).Run() == nil {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// isolateUserHomeForShellRestart prevents these tests from picking up
// the developer's ~/.agent-deck/config.toml (which could carry a custom
// TmuxSocketName, status-bar setting, etc.) Mirrors the pattern used in
// TestInstance_Restart_InterruptsAndResumes.
func isolateUserHomeForShellRestart(t *testing.T) {
	t.Helper()
	origHome := os.Getenv("HOME")
	t.Setenv("HOME", t.TempDir())
	ClearUserConfigCache()
	t.Cleanup(func() {
		os.Setenv("HOME", origHome)
		ClearUserConfigCache()
	})
}
