package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Issue #2995: in the TUI, Shift+Up/Down (and Ctrl+Up/Down, K, J) on a
// sub-session whose parent is in another group, or whose parent was deleted,
// did nothing. The row is drawn at top level in its group, so the reorder keys
// must move it among that group's top-level rows, and the cursor must follow.
func TestReorderKeys_OrphanedSubSession_Issue2995(t *testing.T) {
	session.SetGroupSortMode("creation")
	t.Cleanup(func() { session.SetGroupSortMode("creation") })

	type tc struct {
		name       string
		withParent bool
		key        tea.KeyMsg
		up         bool
	}
	runes := func(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }
	var cases []tc
	for _, p := range []struct {
		label string
		with  bool
	}{{"parent in another group", true}, {"parent deleted", false}} {
		cases = append(cases,
			tc{p.label + "/shift+up", p.with, tea.KeyMsg{Type: tea.KeyShiftUp}, true},
			tc{p.label + "/ctrl+up", p.with, tea.KeyMsg{Type: tea.KeyCtrlUp}, true},
			tc{p.label + "/K", p.with, runes("K"), true},
			tc{p.label + "/shift+down", p.with, tea.KeyMsg{Type: tea.KeyShiftDown}, false},
			tc{p.label + "/J", p.with, runes("J"), false},
		)
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			parent := session.NewInstanceWithTool("boss-session", "/tmp/a", "claude")
			parent.GroupPath = "alpha"
			child := session.NewInstanceWithTool("orphan-kid", "/tmp/b", "claude")
			child.GroupPath = "beta"
			child.ParentSessionID = parent.ID
			peer := session.NewInstanceWithTool("plain-peer", "/tmp/c", "claude")
			peer.GroupPath = "beta"

			// Up: start with the peer above the orphan. Down: orphan above.
			if c.up {
				peer.Order, child.Order = 0, 1
			} else {
				child.Order, peer.Order = 0, 1
			}
			instances := []*session.Instance{child, peer}
			if c.withParent {
				instances = append([]*session.Instance{parent}, instances...)
			}

			h := NewHome()
			h.width, h.height = 120, 30
			h.instancesMu.Lock()
			h.instances = instances
			for _, in := range instances {
				h.instanceByID[in.ID] = in
			}
			h.instancesMu.Unlock()
			h.groupTree = session.NewGroupTree(instances)
			h.rebuildFlatItems()

			drawnBeta := func() []string {
				var out []string
				for _, it := range h.flatItems {
					if it.Type == session.ItemTypeSession && it.Session != nil && it.Path == "beta" {
						out = append(out, it.Session.Title)
					}
				}
				return out
			}
			before := []string{"plain-peer", "orphan-kid"}
			want := []string{"orphan-kid", "plain-peer"}
			if !c.up {
				before, want = want, before
			}
			if got := drawnBeta(); !equalStringSlices(got, before) {
				t.Fatalf("precondition: beta drawn as %v, want %v", got, before)
			}

			h.moveCursorToSession(child.ID)
			h.Update(c.key)

			if got := drawnBeta(); !equalStringSlices(got, want) {
				t.Fatalf("after %s on the orphaned row, beta drawn as %v, want %v", c.key.String(), got, want)
			}
			if it := h.flatItems[h.cursor]; it.Session == nil || it.Session.ID != child.ID {
				t.Errorf("cursor left the moved row")
			}
			if child.ParentSessionID != parent.ID {
				t.Errorf("reorder dropped the parent link: ParentSessionID = %q", child.ParentSessionID)
			}
		})
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
