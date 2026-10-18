package session

import (
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/triggers"
)

// TestTriggerResolverForSessions_UsesSnapshot: the TUI resolves targets from
// a snapshot of its own sessions taken on the UI goroutine. A title or id in
// the snapshot resolves with the current profile; a name in no profile does
// not (a dead target).
func TestTriggerResolverForSessions_UsesSnapshot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	resolve := TriggerResolverForSessions("work", []triggers.Resolved{
		{SessionID: "0123456789abcdef", Title: "inbox-triage"},
	})
	got, ok := resolve("inbox-triage", "")
	if !ok || got.SessionID != "0123456789abcdef" || got.Profile != "work" {
		t.Fatalf("title: got %+v ok=%v, want the snapshot session in profile work", got, ok)
	}
	if got, ok := resolve("01234567", ""); !ok || got.Title != "inbox-triage" {
		t.Errorf("id prefix: got %+v ok=%v", got, ok)
	}
	if _, ok := resolve("overnight-orchestrator", ""); ok {
		t.Error("a title no session has resolved")
	}
}
