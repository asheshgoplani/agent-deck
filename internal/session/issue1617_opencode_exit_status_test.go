package session

import "testing"

// TestTerminatedPaneStatus_Issue1617 pins the classification of a session whose
// tmux pane/session has vanished after having been started.
//
// OpenCode emits no lifecycle hooks (it is not in IsHookEmittingTool), so status
// detection falls back to tmux content sniffing. A clean in-session `/exit`
// closes the pane exactly like a crash, so before the fix a clean OpenCode exit
// was misclassified as StatusError (✕) — the bug reported in issue #1617. It
// must now read as StatusStopped (■, done). Hook-emitting / all other tools keep
// treating a vanished pane as StatusError (a real crash signal).
func TestTerminatedPaneStatus_Issue1617(t *testing.T) {
	cases := map[string]Status{
		"opencode": StatusStopped, // the fix: clean /exit → stopped, not error
		"claude":   StatusError,
		"codex":    StatusError,
		"gemini":   StatusError,
		"hermes":   StatusError,
		"cursor":   StatusError,
		"pi":       StatusError,
		"shell":    StatusError,
		"":         StatusError,
	}
	for tool, want := range cases {
		i := &Instance{Tool: tool}
		if got := i.terminatedPaneStatus(); got != want {
			t.Errorf("terminatedPaneStatus() for tool %q = %q, want %q", tool, got, want)
		}
	}
}
