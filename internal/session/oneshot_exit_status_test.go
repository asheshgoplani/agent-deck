package session

import "testing"

// TestClassifyTerminatedPane_CleanExitVsCrash pins the classification of a
// session whose tmux pane has terminated after having been started.
//
// A one-shot worker runs a command that finishes and exits. When tmux still
// holds the dead pane (remain-on-exit), the real process exit code is
// available: exit 0 is a clean completion (■ StatusStopped), not a crash — the
// bug was that every terminated pane read as StatusError (✕), making a
// successful one-shot exit indistinguishable from a genuine failure. A
// non-zero exit is a real crash → StatusError.
//
// When no exit code is available (pane torn down without remain-on-exit, so
// tmux discarded the exit status), classification falls back to the per-tool
// heuristic: OpenCode's hookless `/exit` reads as stopped (#1617); every other
// tool's vanished pane stays a crash signal.
func TestClassifyTerminatedPane_CleanExitVsCrash(t *testing.T) {
	tests := []struct {
		name         string
		exitCode     int
		haveExitCode bool
		tool         string
		want         Status
	}{
		// Exit code known (remain-on-exit): the code decides, tool is irrelevant.
		{"clean exit 0 (shell)", 0, true, "shell", StatusStopped},
		{"clean exit 0 (claude)", 0, true, "claude", StatusStopped},
		{"clean exit 0 (sandboxed worker)", 0, true, "codex", StatusStopped},
		{"crash exit 1", 1, true, "shell", StatusError},
		{"crash exit 137 (SIGKILL)", 137, true, "claude", StatusError},
		{"crash exit 2 (opencode)", 2, true, "opencode", StatusError},

		// No exit code (pane torn down): fall back to the per-tool heuristic.
		{"no exit code, opencode clean /exit", 0, false, "opencode", StatusStopped},
		{"no exit code, claude crash", 0, false, "claude", StatusError},
		{"no exit code, shell", 0, false, "shell", StatusError},
		{"no exit code, unknown tool", 0, false, "", StatusError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyTerminatedPane(tt.exitCode, tt.haveExitCode, tt.tool)
			if got != tt.want {
				t.Errorf("classifyTerminatedPane(%d, %v, %q) = %q, want %q",
					tt.exitCode, tt.haveExitCode, tt.tool, got, tt.want)
			}
		})
	}
}

// TestTerminatedPaneStatus_NilTmuxFallsBackToTool guards the no-tmux path: with
// no session to read an exit code from, terminatedPaneStatus must degrade to
// the per-tool heuristic rather than assume a clean exit.
func TestTerminatedPaneStatus_NilTmuxFallsBackToTool(t *testing.T) {
	cases := map[string]Status{
		"opencode": StatusStopped,
		"claude":   StatusError,
		"shell":    StatusError,
		"":         StatusError,
	}
	for tool, want := range cases {
		i := &Instance{Tool: tool}
		if got := i.terminatedPaneStatus(); got != want {
			t.Errorf("terminatedPaneStatus() nil tmux, tool %q = %q, want %q", tool, got, want)
		}
	}
}
