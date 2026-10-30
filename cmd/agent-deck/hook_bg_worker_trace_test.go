package main

import (
	"path/filepath"
	"testing"
)

// Tracing (ASK-129) meets the background-worker drop (#2683): a hook from a
// Claude background worker carries the pane's AGENTDECK_INSTANCE_ID but does
// not belong to it, so it must not be traced under that instance either. A
// foreground hook with tracing on still writes the trace spool.

func setupBgWorkerTraceEnv(t *testing.T) (home string) {
	t.Helper()
	setupBgWorkerHookEnv(t)
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AGENTDECK_PROFILE", "default")
	t.Setenv("AGENTDECK_TOOL", "claude")
	t.Setenv("AGENTDECK_TRACE", "summaries")
	return home
}

func TestHookHandler_ForegroundHookIsTraced(t *testing.T) {
	home := setupBgWorkerTraceEnv(t)
	runBgWorkerHook(t)
	if files := traceFiles(t, home); len(files) != 1 {
		t.Fatalf("a foreground hook with tracing on must write the trace spool, got %v", files)
	}
	if !hookStatusWritten(t) {
		t.Fatal("a foreground hook must still write the status file")
	}
}

func TestHookHandler_BgWorkerHookIsNotTraced(t *testing.T) {
	home := setupBgWorkerTraceEnv(t)
	t.Setenv("CLAUDE_JOB_DIR", t.TempDir())
	runBgWorkerHook(t)
	if files := traceFiles(t, home); len(files) != 0 {
		t.Fatalf("a background worker hook was traced under the pane instance: %v", files)
	}
	if hookStatusWritten(t) {
		t.Fatal("a background worker hook was credited to the pane instance")
	}
	if !lifecycleLogHasBgReject(t) {
		t.Fatal("the dropped hook must leave a lifecycle reject line")
	}
}
