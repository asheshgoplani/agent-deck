package ui

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// fieldStoreHome builds a Home from the shape of the maintainer's store when
// the deck showed the "conductors" group twice: eight root groups, the
// Maestro supervisor pinning "conductors" to the top, one archived session.
func fieldStoreHome(t *testing.T) *Home {
	t.Helper()
	type gs struct {
		name, path string
		n          int
		expanded   bool
	}
	specs := []gs{
		{"conductors", "conductors", 4, true},
		{"My Sessions", "my-sessions", 12, false},
		{"agent-deck", "agent-deck", 15, false},
		{"tmp", "tmp", 13, false},
		{"personal", "personal", 18, false},
		{"opengraphdb", "opengraphdb", 0, false},
		{"company", "company", 1, false},
		{"watchers", "watchers", 3, false},
	}
	var groups []*session.GroupData
	var insts []*session.Instance
	statuses := []session.Status{session.StatusIdle, session.StatusRunning, session.StatusWaiting, session.StatusError}
	for i, sp := range specs {
		groups = append(groups, &session.GroupData{Name: sp.name, Path: sp.path, Expanded: sp.expanded, Order: i})
		for j := 0; j < sp.n; j++ {
			title := fmt.Sprintf("%s-s%d", sp.path, j)
			if sp.path == "conductors" && j == 0 {
				title = session.MaestroSessionTitle
			}
			inst := session.NewInstanceWithTool(title, "/tmp/"+sp.path, "claude")
			inst.GroupPath = sp.path
			inst.Status = statuses[j%len(statuses)]
			insts = append(insts, inst)
		}
	}
	h := NewHome()
	h.width, h.height = 215, 50
	h.initialLoading = false
	h.instancesMu.Lock()
	h.instances = insts
	h.instancesMu.Unlock()
	h.groupTree = session.NewGroupTreeWithGroups(insts, groups)
	h.rebuildFlatItems()
	return h
}

// assertListFrameUnique renders the sidebar list and asserts every logical
// row is drawn once: a root group's header text appears on exactly one line.
func assertListFrameUnique(t *testing.T, h *Home, ctx string) {
	t.Helper()
	assertNoDuplicateRows(t, h.flatItems)
	assertRowShape(t, h.flatItems, ctx)
	lines := strings.Split(stripAnsi(h.renderSessionList(h.sessionsPaneWidth(), h.height-6)), "\n")
	seen := map[string]int{}
	for _, l := range lines {
		l = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), "0123456789·"))
		if strings.HasPrefix(l, "▾ ") || strings.HasPrefix(l, "▸ ") {
			name := strings.Fields(l)[1]
			seen[name]++
			if seen[name] > 1 {
				t.Fatalf("%s: group header %q drawn twice in one frame:\n%s", ctx, name, strings.Join(lines, "\n"))
			}
		}
	}
}

// assertRowShape checks the per-row structure the renderer relies on: a
// group's Level matches its path depth and root hotkey numbers are unique.
func assertRowShape(t *testing.T, items []session.Item, ctx string) {
	t.Helper()
	nums := map[int]string{}
	for i, it := range items {
		if it.Type != session.ItemTypeGroup || it.Group == nil {
			continue
		}
		if it.Path != it.Group.Path {
			t.Fatalf("%s: row %d: Item.Path %q != Group.Path %q", ctx, i, it.Path, it.Group.Path)
		}
		if want := session.GetGroupLevel(it.Group.Path); it.Level != want {
			t.Fatalf("%s: row %d: group %q Level %d, path depth says %d", ctx, i, it.Path, it.Level, want)
		}
		if it.Level == 0 && it.RootGroupNum > 0 {
			if prev, ok := nums[it.RootGroupNum]; ok && prev != it.Path {
				t.Fatalf("%s: row %d: hotkey %d shared by %q and %q", ctx, i, it.RootGroupNum, prev, it.Path)
			}
			nums[it.RootGroupNum] = it.Path
		}
	}
}

func TestDupGroupRow_FieldStoreKeySequences(t *testing.T) {
	keys := []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune("1")},
		{Type: tea.KeyRunes, Runes: []rune("2")},
		{Type: tea.KeyDown}, {Type: tea.KeyUp}, {Type: tea.KeyDown},
		{Type: tea.KeyTab}, {Type: tea.KeyRight}, {Type: tea.KeyLeft},
		{Type: tea.KeyRunes, Runes: []rune("h")}, {Type: tea.KeyRunes, Runes: []rune("l")},
		{Type: tea.KeyShiftUp}, {Type: tea.KeyShiftDown},
		{Type: tea.KeyRunes, Runes: []rune("K")}, {Type: tea.KeyRunes, Runes: []rune("J")},
	}
	for seed := int64(0); seed < 40; seed++ {
		h := fieldStoreHome(t)
		defer h.cancel()
		rng := rand.New(rand.NewSource(seed))
		var trail []string
		for step := 0; step < 60; step++ {
			k := keys[rng.Intn(len(keys))]
			trail = append(trail, k.String())
			h.Update(k)
			assertListFrameUnique(t, h, fmt.Sprintf("seed %d step %d keys %v", seed, step, trail))
		}
	}
}
