// Tests for --select preselection behavior (issue #709).
//
// Home.SetInitialSelection preselects a session by ID or Title. Unlike
// SetGroupScope, it does NOT hide any groups — every group the user has
// configured stays visible in the sidebar, the cursor simply lands on the
// matching session.
package ui

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestSetInitialSelection_PositionsCursorAndKeepsAllGroupsVisible asserts the
// core behavior wanted by #709: cursor lands on the requested session AND
// every group remains in the flat item list.
func TestSetInitialSelection_PositionsCursorAndKeepsAllGroupsVisible(t *testing.T) {
	h := &Home{}
	h.windowsCollapsed = make(map[string]bool)

	s1 := session.NewInstanceWithGroup("alpha", "/tmp/a", "work")
	s1.ID = "sess-alpha"
	s2 := session.NewInstanceWithGroup("beta", "/tmp/b", "personal")
	s2.ID = "sess-beta"
	s3 := session.NewInstanceWithGroup("gamma", "/tmp/g", "clients/acme")
	s3.ID = "sess-gamma"

	h.instances = []*session.Instance{s1, s2, s3}
	h.groupTree = session.NewGroupTree(h.instances)

	// Preselect the 'beta' session in the 'personal' group.
	h.SetInitialSelection("sess-beta")
	h.rebuildFlatItems()
	h.applyInitialSelection()

	// Cursor must point at sess-beta.
	if h.cursor < 0 || h.cursor >= len(h.flatItems) {
		t.Fatalf("cursor %d out of range (flatItems len=%d)", h.cursor, len(h.flatItems))
	}
	sel := h.flatItems[h.cursor]
	if sel.Type != session.ItemTypeSession || sel.Session == nil || sel.Session.ID != "sess-beta" {
		t.Fatalf("cursor landed on %+v, want session sess-beta", sel)
	}

	// All three groups must remain visible in the flat items (no scope filter).
	gotGroups := map[string]bool{}
	for _, it := range h.flatItems {
		if it.Type == session.ItemTypeGroup {
			gotGroups[it.Path] = true
		}
	}
	for _, want := range []string{"work", "personal", "clients/acme"} {
		if !gotGroups[want] {
			t.Errorf("group %q missing from flat items; got %v", want, gotGroups)
		}
	}
}

// TestSetInitialSelection_MatchesByTitle verifies the user may pass a human
// title instead of an internal session ID.
func TestSetInitialSelection_MatchesByTitle(t *testing.T) {
	h := &Home{}
	h.windowsCollapsed = make(map[string]bool)

	s1 := session.NewInstanceWithGroup("my-project", "/tmp/a", "work")
	s1.ID = "internal-xyz-1"
	h.instances = []*session.Instance{s1}
	h.groupTree = session.NewGroupTree(h.instances)

	h.SetInitialSelection("my-project")
	h.rebuildFlatItems()
	h.applyInitialSelection()

	sel := h.flatItems[h.cursor]
	if sel.Type != session.ItemTypeSession || sel.Session == nil || sel.Session.ID != "internal-xyz-1" {
		t.Fatalf("title match: cursor landed on %+v, want internal-xyz-1", sel)
	}
}

// TestSetInitialSelection_GroupScopePrecedence covers issue #709 scope rule:
// if both -g and --select are passed and the selected session IS inside the
// scope, cursor positions within the group view. If the session is OUTSIDE
// the scope, SetInitialSelection must report it is unresolved (so main.go can
// warn) rather than silently exposing the scoped-out session.
func TestSetInitialSelection_GroupScopePrecedence(t *testing.T) {
	t.Run("session in scope → cursor positions", func(t *testing.T) {
		h := &Home{}
		h.windowsCollapsed = make(map[string]bool)
		s1 := session.NewInstanceWithGroup("in-scope", "/tmp/a", "work")
		s1.ID = "id-in"
		s2 := session.NewInstanceWithGroup("out-scope", "/tmp/b", "personal")
		s2.ID = "id-out"
		h.instances = []*session.Instance{s1, s2}
		h.groupTree = session.NewGroupTree(h.instances)
		h.SetGroupScope("work")
		h.SetInitialSelection("id-in")
		h.rebuildFlatItems()

		if ok := h.applyInitialSelection(); !ok {
			t.Fatalf("applyInitialSelection returned false, want true (session is in scope)")
		}
		sel := h.flatItems[h.cursor]
		if sel.Type != session.ItemTypeSession || sel.Session.ID != "id-in" {
			t.Fatalf("cursor on %+v, want id-in", sel)
		}
	})

	t.Run("session out of scope → applyInitialSelection returns false", func(t *testing.T) {
		h := &Home{}
		h.windowsCollapsed = make(map[string]bool)
		s1 := session.NewInstanceWithGroup("in-scope", "/tmp/a", "work")
		s1.ID = "id-in"
		s2 := session.NewInstanceWithGroup("out-scope", "/tmp/b", "personal")
		s2.ID = "id-out"
		h.instances = []*session.Instance{s1, s2}
		h.groupTree = session.NewGroupTree(h.instances)
		h.SetGroupScope("work")
		h.SetInitialSelection("id-out") // outside the 'work' scope
		h.rebuildFlatItems()

		if ok := h.applyInitialSelection(); ok {
			t.Fatalf("applyInitialSelection returned true, want false (session not in scope)")
		}
		// The 'out' session must NOT be in flatItems because scope hides it.
		for _, it := range h.flatItems {
			if it.Type == session.ItemTypeSession && it.Session != nil && it.Session.ID == "id-out" {
				t.Fatalf("scoped-out session appeared in flatItems: %+v", it)
			}
		}
	})

	t.Run("unknown id → applyInitialSelection returns false", func(t *testing.T) {
		h := &Home{}
		h.windowsCollapsed = make(map[string]bool)
		s1 := session.NewInstanceWithGroup("only", "/tmp/a", "work")
		s1.ID = "only-id"
		h.instances = []*session.Instance{s1}
		h.groupTree = session.NewGroupTree(h.instances)
		h.SetInitialSelection("does-not-exist")
		h.rebuildFlatItems()
		if ok := h.applyInitialSelection(); ok {
			t.Fatalf("applyInitialSelection returned true for unknown id, want false")
		}
	})
}

// TestSetInitialSelection_NormalizationIsLenient lets users pass the title as
// typed in the CLI even if it has trailing whitespace / odd casing.
func TestSetInitialSelection_NormalizationIsLenient(t *testing.T) {
	h := &Home{}
	h.windowsCollapsed = make(map[string]bool)
	s1 := session.NewInstanceWithGroup("My Session", "/tmp/a", "work")
	s1.ID = "id-1"
	h.instances = []*session.Instance{s1}
	h.groupTree = session.NewGroupTree(h.instances)
	h.SetInitialSelection("  my session  ") // whitespace + casing
	h.rebuildFlatItems()

	if ok := h.applyInitialSelection(); !ok {
		t.Fatalf("applyInitialSelection returned false, want lenient match")
	}
	if strings.TrimSpace(h.initialSelect) == "" {
		t.Fatal("initialSelect got cleared unexpectedly before apply")
	}
}
