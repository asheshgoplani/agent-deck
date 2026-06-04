package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runHookHandlerWithStdin feeds payload to handleHookHandler via os.Stdin,
// mirroring how Claude Code invokes `agent-deck hook-handler` as a fresh
// process per hook event. os.Stdin is restored on return.
func runHookHandlerWithStdin(t *testing.T, payload string) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "hook-stdin-*.json")
	if err != nil {
		t.Fatalf("create stdin temp: %v", err)
	}
	if _, err := f.WriteString(payload); err != nil {
		t.Fatalf("write stdin temp: %v", err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatalf("seek stdin temp: %v", err)
	}
	orig := os.Stdin
	os.Stdin = f
	defer func() {
		os.Stdin = orig
		_ = f.Close()
	}()
	handleHookHandler()
}

// readLogTolerant returns the debug.log body, or "" if it was never created
// (no log lines emitted). Unlike readLog it does not fail the test on a
// missing file, so assertions about the ABSENCE of a log line stay meaningful.
func readLogTolerant(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "debug.log"))
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("read debug.log: %v", err)
	}
	return string(data)
}

// TestHookHandler_MissingProjectDir_DegradesNotFatal reproduces issue #1233:
// when a running session's registered worktree (the hook payload's cwd /
// PROJECT_DIR) is renamed or removed out from under it, the hook-handler
// must GRACEFULLY DEGRADE — log a single WARN suggesting `agent-deck session
// move` and soft-skip the invocation — instead of emitting a FATAL-class
// error on every tool call.
func TestHookHandler_MissingProjectDir_DegradesNotFatal(t *testing.T) {
	logDir := initTestLogging(t)

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENTDECK_INSTANCE_ID", "inst-1233")

	// A worktree path that no longer exists (renamed out from under the session).
	missingDir := filepath.Join(home, "worktrees", "renamed-away")

	// UserPromptSubmit maps to "running"; absent the degrade guard it would
	// write a status file. We assert it is soft-skipped instead.
	payload := `{"hook_event_name":"UserPromptSubmit","session_id":"s1","cwd":"` + missingDir + `"}`

	// Two invocations simulate two tool calls in a row (two processes).
	runHookHandlerWithStdin(t, payload)
	runHookHandlerWithStdin(t, payload)

	body := readLogTolerant(t, logDir)

	// (1) The degrade marker is emitted at WARN, not as a FATAL/ERROR.
	if got := strings.Count(body, `"hook_projectdir_missing"`); got != 1 {
		t.Errorf("expected exactly one hook_projectdir_missing WARN (logged once), got %d; log:\n%s", got, body)
	}
	if !strings.Contains(body, `"level":"WARN"`) {
		t.Errorf("expected WARN level for missing project dir; log:\n%s", body)
	}
	if strings.Contains(body, `"level":"ERROR"`) || strings.Contains(body, `"level":"FATAL"`) {
		t.Errorf("missing project dir must not produce a FATAL/ERROR log line; log:\n%s", body)
	}

	// (2) The WARN carries the offending path and the session-move remedy.
	if !strings.Contains(body, missingDir) {
		t.Errorf("expected the missing path %q in the WARN; log:\n%s", missingDir, body)
	}
	if !strings.Contains(body, "session move") {
		t.Errorf("expected an `agent-deck session move` suggestion in the WARN; log:\n%s", body)
	}

	// (3) Soft-skip: no status file is written for the broken session.
	statusFile := filepath.Join(getHooksDir(), "inst-1233.json")
	if _, err := os.Stat(statusFile); err == nil {
		t.Errorf("expected soft-skip (no status file) when project dir is missing, but %s exists", statusFile)
	}
}

// TestHookHandler_PresentProjectDir_NotDegraded guards against over-eager
// skipping: when cwd exists (the normal case) the handler proceeds and writes
// the status file, and no missing-project-dir WARN is emitted. An empty cwd
// (older Claude Code that doesn't send one) is treated as present.
func TestHookHandler_PresentProjectDir_NotDegraded(t *testing.T) {
	logDir := initTestLogging(t)

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENTDECK_INSTANCE_ID", "inst-ok")

	cwd := filepath.Join(home, "live-worktree")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir cwd: %v", err)
	}

	payload := `{"hook_event_name":"UserPromptSubmit","session_id":"s2","cwd":"` + cwd + `"}`
	runHookHandlerWithStdin(t, payload)

	body := readLogTolerant(t, logDir)
	if strings.Contains(body, `"hook_projectdir_missing"`) {
		t.Errorf("did not expect a missing-project-dir WARN when cwd exists; log:\n%s", body)
	}

	statusFile := filepath.Join(getHooksDir(), "inst-ok.json")
	if _, err := os.Stat(statusFile); err != nil {
		t.Errorf("expected status file to be written when project dir exists: %v", err)
	}
}
