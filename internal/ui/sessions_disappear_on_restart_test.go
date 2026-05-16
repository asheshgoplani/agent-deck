package ui

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Tests in this file are regression guards for the bug that owns this branch
// (feature/sessions-dispear-on-restart). User report: pressing R on a busted
// session caused all OTHER error sessions to vanish from the unfiltered TUI
// view. The user has reported the symptom is intermittent and could not be
// reproduced after restarting agent-deck from the CLI — these tests therefore
// codify the contract ("a successful restart must not prune peer sessions
// from view") so any future regression that DOES make it deterministic gets
// caught here, even if today's run passes.
//
// All tests run pure in-memory: no SQLite, no tmux, no PTY. h.storage is
// nilled out so saveInstances() is a no-op and we can drive the message
// handler directly.

// TestRestartMsg_DoesNotPrunePeerErrorSessions_Unfiltered pins the contract:
// when sessionRestartedMsg fires successfully for one session, peer sessions
// (including other error-state ones) must remain visible in flatItems with
// no statusFilter active.
func TestRestartMsg_DoesNotPrunePeerErrorSessions_Unfiltered(t *testing.T) {
	home := NewHome()
	home.storage = nil // prevent disk writes during saveInstances
	home.statusFilter = ""

	smithy := newShellInstanceForRestartTest("smithy-id", "Smithy", session.StatusError)
	foo := newShellInstanceForRestartTest("foo-id", "Foo", session.StatusError)
	bar := newShellInstanceForRestartTest("bar-id", "Bar", session.StatusRunning)

	home.instances = []*session.Instance{smithy, foo, bar}
	home.instanceByID = map[string]*session.Instance{
		smithy.ID: smithy,
		foo.ID:    foo,
		bar.ID:    bar,
	}
	home.groupTree = session.NewGroupTree(home.instances)
	home.rebuildFlatItems()

	preIDs := visibleSessionIDsFromFlat(home)
	if len(preIDs) != 3 {
		t.Fatalf("setup: expected 3 visible sessions, got %d (%v)", len(preIDs), preIDs)
	}

	model, _ := home.Update(sessionRestartedMsg{sessionID: smithy.ID, err: nil})
	h, ok := model.(*Home)
	if !ok {
		t.Fatalf("Update returned %T, want *Home", model)
	}

	postIDs := visibleSessionIDsFromFlat(h)
	for _, want := range []string{smithy.ID, foo.ID, bar.ID} {
		if !sliceContainsString(postIDs, want) {
			t.Errorf("after sessionRestartedMsg, flatItems missing %q; got %v", want, postIDs)
		}
	}
	if h.statusFilter != "" {
		t.Errorf("statusFilter mutated to %q; expected unfiltered", h.statusFilter)
	}
}

// TestRestartMsg_DoesNotPrunePeerErrorSessions_FilteredToErrors pins the
// contract under the error-only filter, where the visual surface for "all
// my errors disappeared" is most visible — pressing R on one error must
// not collapse the other still-erroring peers.
func TestRestartMsg_DoesNotPrunePeerErrorSessions_FilteredToErrors(t *testing.T) {
	home := NewHome()
	home.storage = nil
	home.statusFilter = session.StatusError

	smithy := newShellInstanceForRestartTest("smithy-id", "Smithy", session.StatusError)
	foo := newShellInstanceForRestartTest("foo-id", "Foo", session.StatusError)
	bar := newShellInstanceForRestartTest("bar-id", "Bar", session.StatusRunning)

	home.instances = []*session.Instance{smithy, foo, bar}
	home.instanceByID = map[string]*session.Instance{
		smithy.ID: smithy,
		foo.ID:    foo,
		bar.ID:    bar,
	}
	home.groupTree = session.NewGroupTree(home.instances)
	home.rebuildFlatItems()

	preIDs := visibleSessionIDsFromFlat(home)
	if !sliceContainsString(preIDs, smithy.ID) || !sliceContainsString(preIDs, foo.ID) {
		t.Fatalf("setup: error filter should show smithy + foo, got %v", preIDs)
	}
	if sliceContainsString(preIDs, bar.ID) {
		t.Fatalf("setup: error filter should hide bar, got %v", preIDs)
	}

	model, _ := home.Update(sessionRestartedMsg{sessionID: smithy.ID, err: nil})
	h, _ := model.(*Home)

	postIDs := visibleSessionIDsFromFlat(h)
	if !sliceContainsString(postIDs, foo.ID) {
		t.Errorf("after sessionRestartedMsg, error-filtered list lost peer Foo; got %v", postIDs)
	}
}

// --- helpers ---

func newShellInstanceForRestartTest(id, title string, status session.Status) *session.Instance {
	return &session.Instance{
		ID:        id,
		Title:     title,
		Tool:      "shell",
		Status:    status,
		GroupPath: session.DefaultGroupPath,
		CreatedAt: time.Now(),
	}
}

func visibleSessionIDsFromFlat(h *Home) []string {
	var ids []string
	for _, item := range h.flatItems {
		if item.Type == session.ItemTypeSession && item.Session != nil {
			ids = append(ids, item.Session.ID)
		}
	}
	return ids
}

func sliceContainsString(s []string, want string) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}
