package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/events"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// The bulk remove (--all-errored, cleanup) announces every removed row.
func TestBulkRemovePublishesSessionClosedPerRow(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	first := addTestSession(t, home, filepath.Join(home, "err-a"), "err-a")
	forceSetStatus(t, home, first, session.StatusError)
	second := addTestSession(t, home, filepath.Join(home, "err-b"), "err-b")
	forceSetStatus(t, home, second, session.StatusError)
	if stdout, stderr, code := runAgentDeck(t, home, "session", "remove", "--all-errored", "--json"); code != 0 {
		t.Fatalf("--all-errored: %d %s %s", code, stdout, stderr)
	}
	if got := openTestBusDir(t, home).Kinds[events.KindMacappSessionClosed]; got != 2 {
		t.Fatalf("session.closed frames after a bulk remove of 2: %d", got)
	}
}

// A bulk remove that fails part way exits; the rows it already deleted must
// still be swept and announced. So the cleanup runs inside the delete loop,
// right after each delete, and the failure exits drain the events tap first.
func TestBulkRemoveCleansUpEachRowBeforeAnyExit(t *testing.T) {
	src, err := os.ReadFile("session_remove_cmd.go")
	if err != nil {
		t.Fatal(err)
	}
	body := extractFuncBody(string(src), "bulkRemoveSessions")
	del := strings.Index(body, "storage.DeleteInstance(inst.ID)")
	cleanup := strings.Index(body, "session.CleanupRemovedSession(storage.Profile(), inst.ID)")
	save := strings.Index(body, "storage.SaveGroupsOnly(")
	if del < 0 || cleanup < del || save < cleanup {
		t.Fatalf("cleanup must follow each delete inside the loop, before the groups save (delete=%d cleanup=%d save=%d)", del, cleanup, save)
	}
	if strings.Contains(body, "exitCLI(") {
		t.Fatal("bulkRemoveSessions must exit through exitBulkRemove, which drains the events tap")
	}
	if !strings.Contains(extractFuncBody(string(src), "exitBulkRemove"), "events.CloseDefault()") {
		t.Fatal("exitBulkRemove must drain the events tap before exiting")
	}
}

// Every path that deletes a session row runs the shared cleanup, which
// sweeps the inboxes and the notify-state record and publishes
// macapp.session.closed. Paths with their own behavioral test are covered
// there; this pins the ones that need git or a conductor to run.
func TestSessionDeletePathsUseSharedCleanup(t *testing.T) {
	for _, c := range []struct{ file, fn string }{
		{"main.go", "handleRemove"},
		{"session_remove_cmd.go", "handleSessionRemove"},
		{"session_remove_cmd.go", "bulkRemoveSessions"},
		{"worktree_cmd.go", "handleWorktreeFinish"},
		{"conductor_cmd.go", "handleConductorTeardown"},
		{"../../internal/ui/web_mutator.go", "DeleteSession"},
		{"../../internal/ui/web_mutator.go", "FinishWorktree"},
	} {
		src, err := os.ReadFile(c.file)
		if err != nil {
			t.Fatal(err)
		}
		body := extractFuncBody(string(src), c.fn)
		if body == "" {
			t.Fatalf("%s: %s not found", c.file, c.fn)
		}
		if !strings.Contains(body, "session.CleanupRemovedSession(") {
			t.Errorf("%s %s deletes a session row without session.CleanupRemovedSession", c.file, c.fn)
		}
		for _, old := range []string{"session.SweepInboxesForChildSession(", "session.RemoveNotifyStateRecord("} {
			if strings.Contains(body, old) {
				t.Errorf("%s %s calls %s directly; use session.CleanupRemovedSession", c.file, c.fn, old)
			}
		}
	}
}
