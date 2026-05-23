package session

import "testing"

// Instance.LastObservedActivity must be safe to call when the instance
// has no live tmux session (freshly loaded, never started, or stopped).
// Returning (zero, false) is the load-bearing contract — callers in the
// render path lean on the bool to decide whether to consult the time
// value, and the zero time gives a safe sentinel for callers that don't.

func TestInstance_LastObservedActivity_NilTmuxSession(t *testing.T) {
	inst := &Instance{ID: "sess-no-tmux"}
	ts, observed := inst.LastObservedActivity()
	if observed {
		t.Errorf("Instance with nil tmuxSession must return observed=false, got %v", ts)
	}
	if !ts.IsZero() {
		t.Errorf("Instance with nil tmuxSession must return zero time, got %v", ts)
	}
}
