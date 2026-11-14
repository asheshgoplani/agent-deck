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

// The TUI swaps in freshly loaded sessions on every storage reload.
// CarryOverReloadState hands each rebuilt tmux session the one the same
// Instance ID had before, so the sidebar keeps the detail the old one last
// showed until the next status pass. Whether a pair inherits (same pane name)
// or starts clean (pane restarted between loads) is decided by
// tmux.Session.InheritCachedSubstate and tested in internal/tmux
// (TestInheritCachedSubstate_SamePaneKeepsDetail and
// TestInheritCachedSubstate_DifferentPaneStaysEmpty).
func TestCarryOverReloadState_PairsSameSessionByID(t *testing.T) {
	prev, next := reloadPair("carry-same", "agentdeck_carry_1111", "agentdeck_carry_1111")
	other, _ := reloadPair("carry-other", "agentdeck_carry_3333", "agentdeck_carry_3333")

	pairs := reloadCarryPairs([]*Instance{other, prev}, []*Instance{next})

	if len(pairs) != 1 {
		t.Fatalf("reloadCarryPairs returned %d pairs, want 1", len(pairs))
	}
	if pairs[0].next != next.GetTmuxSession() {
		t.Fatalf("pair.next is not the rebuilt session's tmux session")
	}
	if pairs[0].prev != prev.GetTmuxSession() {
		t.Fatalf("pair.prev is not the tmux session the same ID had before the reload")
	}
}

// A session that was not loaded before the reload has nothing to inherit.
func TestCarryOverReloadState_NewSessionHasNoPrev(t *testing.T) {
	prev, _ := reloadPair("carry-old", "agentdeck_carry_1111", "agentdeck_carry_1111")
	_, next := reloadPair("carry-new", "agentdeck_carry_2222", "agentdeck_carry_2222")

	pairs := reloadCarryPairs([]*Instance{prev}, []*Instance{next})

	if len(pairs) != 1 || pairs[0].prev != nil {
		t.Fatalf("reloadCarryPairs = %+v, want one pair with no prev", pairs)
	}
}

// Remote sessions are not part of the storage reload. They reach the TUI as
// RemoteSessionInfo rows from the remote host's own `agent-deck list` over
// SSH, which runs the CLI load path on that host. The TUI holds no tmux
// session or acknowledgment for them, so there is nothing to carry over.
func TestCarryOverReloadState_RemoteSessions(t *testing.T) {
	t.Skip("not applicable: RemoteSessionInfo comes from the remote's own CLI load over SSH, not the local storage reload")
}
