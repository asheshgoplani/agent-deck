package session

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

func reloadPair(id, prevPane, nextPane string) (prev, next *Instance) {
	prev = NewInstanceWithTool(id, "/tmp", "claude")
	prev.ID = id
	prev.tmuxSession = tmux.ReconnectSessionLazy(prevPane, id, "/tmp", "claude", "idle")
	next = NewInstanceWithTool(id, "/tmp", "claude")
	next.ID = id
	next.tmuxSession = tmux.ReconnectSessionLazy(nextPane, id, "/tmp", "claude", "idle")
	return prev, next
}

// The TUI swaps in freshly loaded sessions on every storage reload. The
// rebuilt session keeps the detail the old one last showed, so the sidebar
// does not flash it away until the next status pass.
func TestCarryOverReloadState_SameSessionKeepsSubstate(t *testing.T) {
	prev, next := reloadPair("carry-same", "agentdeck_carry_1111", "agentdeck_carry_1111")
	prev.tmuxSession.SetCachedSubstateForTest(SubstateIdleAtEmptyPrompt, "detail")

	CarryOverReloadState([]*Instance{prev}, []*Instance{next})

	if got := next.CachedSubstate(); got != SubstateIdleAtEmptyPrompt {
		t.Fatalf("CachedSubstate after reload = %q, want %q", got, SubstateIdleAtEmptyPrompt)
	}
	if got := next.SubstateDetail(); got != "detail" {
		t.Fatalf("SubstateDetail after reload = %q, want %q", got, "detail")
	}
}

// A session whose pane changed (restarted between loads) starts clean.
func TestCarryOverReloadState_RestartedPaneStartsClean(t *testing.T) {
	prev, next := reloadPair("carry-restart", "agentdeck_carry_1111", "agentdeck_carry_2222")
	prev.tmuxSession.SetCachedSubstateForTest(SubstateIdleAtEmptyPrompt, "")

	CarryOverReloadState([]*Instance{prev}, []*Instance{next})

	if got := next.CachedSubstate(); got != SubstateNone {
		t.Fatalf("CachedSubstate after reload = %q, want none for a restarted pane", got)
	}
}

// Remote sessions are not part of the storage reload. They reach the TUI as
// RemoteSessionInfo rows from the remote host's own `agent-deck list` over
// SSH, which runs the CLI load path on that host. The TUI holds no tmux
// session or acknowledgment for them, so there is nothing to carry over.
func TestCarryOverReloadState_RemoteSessions(t *testing.T) {
	t.Skip("not applicable: RemoteSessionInfo comes from the remote's own CLI load over SSH, not the local storage reload")
}
