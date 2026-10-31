package tmux

import "testing"

// A storage reload rebuilds the Session object for a pane that keeps running.
// The rebuilt object starts with no cached substate, so the sidebar's detail
// line (e.g. "idle-at-empty-prompt") vanished until the next classification
// and reappeared seconds later: a flash on every reload.
func TestInheritCachedSubstate_SamePaneKeepsDetail(t *testing.T) {
	prev := ReconnectSessionLazy("agentdeck_same_1234", "same", "/tmp", "claude", "idle")
	prev.SetCachedSubstateForTest(SubstateIdleAtEmptyPrompt, "detail")
	next := ReconnectSessionLazy("agentdeck_same_1234", "same", "/tmp", "claude", "idle")

	next.InheritCachedSubstate(prev)

	if got := next.CachedSubstate(); got != SubstateIdleAtEmptyPrompt {
		t.Fatalf("CachedSubstate = %q, want %q carried over from the same pane", got, SubstateIdleAtEmptyPrompt)
	}
	if got := next.CachedSubstateDetail(); got != "detail" {
		t.Fatalf("CachedSubstateDetail = %q, want %q", got, "detail")
	}
}

// A different pane (a restarted or replaced session) must not inherit a
// detail that described someone else's frame.
func TestInheritCachedSubstate_DifferentPaneStaysEmpty(t *testing.T) {
	prev := ReconnectSessionLazy("agentdeck_old_1234", "x", "/tmp", "claude", "idle")
	prev.SetCachedSubstateForTest(SubstateIdleAtEmptyPrompt, "")
	next := ReconnectSessionLazy("agentdeck_new_5678", "x", "/tmp", "claude", "idle")

	next.InheritCachedSubstate(prev)

	if got := next.CachedSubstate(); got != SubstateNone {
		t.Fatalf("CachedSubstate = %q, want none for a different pane", got)
	}
}
