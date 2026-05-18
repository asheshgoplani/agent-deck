package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestReorderKeysPlusMinus is a regression guard for PR #892 (takeover of @oryaacov):
// `+` and `-` must reorder sessions in the tree, alongside the existing `K`/`J` and
// `Shift+↑/↓` bindings. Many terminals drop modifier info on arrow keys, so the
// plain-ASCII `+`/`-` is the discoverable, terminal-portable default.
func TestReorderKeysPlusMinus(t *testing.T) {
	build := func() (*Home, []*session.Instance) {
		h := NewHome()
		h.width, h.height = 120, 30
		instances := []*session.Instance{
			session.NewInstanceWithTool("alpha", "/tmp/a", "claude"),
			session.NewInstanceWithTool("bravo", "/tmp/b", "claude"),
		}
		for _, inst := range instances {
			inst.GroupPath = "g"
		}
		h.instancesMu.Lock()
		h.instances = instances
		h.instancesMu.Unlock()
		h.groupTree = session.NewGroupTree(instances)
		h.rebuildFlatItems()
		return h, instances
	}

	sessionIDs := func(h *Home) []string {
		g := h.groupTree.Groups["g"]
		ids := make([]string, len(g.Sessions))
		for i, s := range g.Sessions {
			ids[i] = s.ID
		}
		return ids
	}

	findSession := func(h *Home, id string) int {
		for i, it := range h.flatItems {
			if it.Type == session.ItemTypeSession && it.Session != nil && it.Session.ID == id {
				return i
			}
		}
		return -1
	}

	// `+` on second session moves it up.
	h, instances := build()
	h.cursor = findSession(h, instances[1].ID)
	if h.cursor < 0 {
		t.Fatal("bravo not in flatItems")
	}
	h.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'+'}})
	if got := sessionIDs(h); got[0] != instances[1].ID || got[1] != instances[0].ID {
		t.Fatalf("+ should move bravo above alpha; got order=%v", got)
	}

	// `-` on first session moves it down.
	h, instances = build()
	h.cursor = findSession(h, instances[0].ID)
	if h.cursor < 0 {
		t.Fatal("alpha not in flatItems")
	}
	h.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'-'}})
	if got := sessionIDs(h); got[0] != instances[1].ID || got[1] != instances[0].ID {
		t.Fatalf("- should move alpha below bravo; got order=%v", got)
	}
}
