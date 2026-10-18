package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Issue #2683: Claude Code's agent-view supervisor and its bg-spare workers
// keep the environment of the pane that first started them, including
// AGENTDECK_INSTANCE_ID. Their hooks belong to whichever conversation was
// backgrounded, not to that pane, so they must not be credited to it.

const bgWorkerTestSession = "99999999-9999-4999-8999-999999999999"

func setupBgWorkerHookEnv(t *testing.T) (claudeDir string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	claudeDir = filepath.Join(home, ".claude")
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	t.Setenv("CLAUDE_JOB_DIR", "")
	t.Setenv("AGENTDECK_INSTANCE_ID", "inst-pane-a")
	return claudeDir
}

func runBgWorkerHook(t *testing.T) {
	t.Helper()
	runHookHandlerWith(t, `{"hook_event_name":"PostToolUse","session_id":"`+bgWorkerTestSession+`"}`)
}

func hookStatusWritten(t *testing.T) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(getHooksDir(), "inst-pane-a.json"))
	return err == nil
}

func lifecycleLogHasBgReject(t *testing.T) bool {
	t.Helper()
	data, _ := os.ReadFile(session.GetSessionIDLifecycleLogPath())
	return strings.Contains(string(data), `"reason":"hook_from_background_worker"`) &&
		strings.Contains(string(data), bgWorkerTestSession)
}

func TestHookHandler_ForegroundHookIsCredited(t *testing.T) {
	setupBgWorkerHookEnv(t)
	runBgWorkerHook(t)
	if !hookStatusWritten(t) {
		t.Fatal("a hook from the pane's own Claude must still write the status file")
	}
}

func TestHookHandler_DropsHookWithClaudeJobDir(t *testing.T) {
	setupBgWorkerHookEnv(t)
	t.Setenv("CLAUDE_JOB_DIR", t.TempDir())
	runBgWorkerHook(t)
	runBgWorkerHook(t)
	if hookStatusWritten(t) {
		t.Fatal("a background worker hook (CLAUDE_JOB_DIR set) was credited to the pane instance")
	}
	if !lifecycleLogHasBgReject(t) {
		t.Fatal("the dropped hook must leave a lifecycle reject line")
	}
	data, _ := os.ReadFile(session.GetSessionIDLifecycleLogPath())
	if n := strings.Count(string(data), "hook_from_background_worker"); n != 1 {
		t.Fatalf("want one lifecycle line per worker session, got %d", n)
	}
}

func TestHookHandler_DropsHookFromBgKindPidFile(t *testing.T) {
	claudeDir := setupBgWorkerHookEnv(t)
	sessionsDir := filepath.Join(claudeDir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pid := os.Getppid()
	rec := fmt.Sprintf(`{"pid":%d,"sessionId":%q,"kind":"bg"}`, pid, bgWorkerTestSession)
	if err := os.WriteFile(filepath.Join(sessionsDir, fmt.Sprintf("%d.json", pid)), []byte(rec), 0o600); err != nil {
		t.Fatal(err)
	}
	runBgWorkerHook(t)
	if hookStatusWritten(t) {
		t.Fatal("a hook from a kind=bg Claude process was credited to the pane instance")
	}
	if !lifecycleLogHasBgReject(t) {
		t.Fatal("the dropped hook must leave a lifecycle reject line")
	}
}

// Review follow-up on #2683: CLAUDE_JOB_DIR can leak into a pane through its
// tmux server's environment. When the nearest Claude ancestor's sessions
// record says it is a foreground (non-bg) process, that record wins and the
// hook is credited.
func TestHookHandler_ForegroundKindBeatsLeakedClaudeJobDir(t *testing.T) {
	claudeDir := setupBgWorkerHookEnv(t)
	t.Setenv("CLAUDE_JOB_DIR", t.TempDir())
	sessionsDir := filepath.Join(claudeDir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pid := os.Getppid()
	rec := fmt.Sprintf(`{"pid":%d,"sessionId":%q,"kind":"interactive"}`, pid, bgWorkerTestSession)
	if err := os.WriteFile(filepath.Join(sessionsDir, fmt.Sprintf("%d.json", pid)), []byte(rec), 0o600); err != nil {
		t.Fatal(err)
	}
	runBgWorkerHook(t)
	if !hookStatusWritten(t) {
		t.Fatal("a hook whose nearest Claude ancestor is a foreground process was dropped because CLAUDE_JOB_DIR leaked into the pane")
	}
	if lifecycleLogHasBgReject(t) {
		t.Fatal("a credited foreground hook must not log a background-worker reject")
	}
}

// The process-level scrub runs before any tmux exec for every command except
// the hook handler, which reads CLAUDE_JOB_DIR as a bg-worker signal.
func TestScrubClaudeJobDirForCommand(t *testing.T) {
	t.Setenv("CLAUDE_JOB_DIR", "/tmp/jobs/abc")
	scrubClaudeJobDirForCommand([]string{"hook-handler"})
	if _, ok := os.LookupEnv("CLAUDE_JOB_DIR"); !ok {
		t.Fatal("hook-handler must keep CLAUDE_JOB_DIR")
	}
	scrubClaudeJobDirForCommand([]string{"add", "."})
	if _, ok := os.LookupEnv("CLAUDE_JOB_DIR"); ok {
		t.Fatal("CLAUDE_JOB_DIR must be scrubbed before agent-deck can start a tmux server")
	}
	scrubClaudeJobDirForCommand(nil) // bare TUI launch: no panic, still scrubbed
}
