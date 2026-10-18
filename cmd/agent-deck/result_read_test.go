package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// `session output` run from inside a parent records the turn it showed, so
// the inbox notice for that turn withdraws itself at the drain. A read from
// outside any session (a human at a shell), a self read, and a read that
// names no turn (pane, terminal or clipped output) record nothing.
func TestNoteResultRead_WithdrawsTheNoticeForTheReadTurn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("AGENT_DECK_PROFILE", "")
	session.ResetInboxFingerprintCacheForTest()

	finished := time.Unix(time.Now().Add(-time.Minute).Unix(), 0)
	commit := func(child string) {
		t.Helper()
		ev := session.TransitionNotificationEvent{ChildSessionID: child, ChildTitle: child, FromStatus: "running",
			ToStatus: "waiting", Timestamp: finished, TurnFingerprint: child + "@1", TurnUUID: "final-" + child,
			Tier: session.TurnTierUrgent}
		if err := session.CommitToInbox("parent-x", ev); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []string{"child-read", "child-older", "child-unnamed", "child-shell"} {
		commit(c)
	}

	t.Setenv("AGENTDECK_INSTANCE_ID", "parent-x")
	noteResultRead("child-read", "final-child-read")
	noteResultRead("child-older", "midturn-child-older") // shown an older record
	noteResultRead("child-unnamed", "")                  // pane or clipped read
	noteResultRead("parent-x", "final-parent-x")         // a self read is not a result read
	t.Setenv("AGENTDECK_INSTANCE_ID", "")
	noteResultRead("child-shell", "final-child-shell") // outside a session: ignored

	delivered, err := session.DrainInboxForParent("parent-x")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, ev := range delivered {
		got[ev.ChildSessionID] = true
	}
	if len(delivered) != 3 || got["child-read"] || !got["child-older"] || !got["child-unnamed"] || !got["child-shell"] {
		t.Fatalf("delivered %+v, want all but child-read (the only final turn the parent was shown)", delivered)
	}
}
