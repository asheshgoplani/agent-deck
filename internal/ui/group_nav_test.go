package ui

// Tests for v1.7.60 group-scoped keyboard navigation (issue: Christoph Becker
// feedback, "jumping between shells is too complicated. shortcuts needed").
//
// The new Alt+* layer adds group-scoped navigation on top of the existing
// global-scoped j/k/1-9/g/G// keys, which remain unchanged. These tests
// assert both the new behavior and the preservation of the existing one.

import (
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// buildTwoGroupHome returns a Home with two groups:
//   - group "alpha" with 3 sessions (a1, a2, a3)
//   - group "beta"  with 2 sessions (b1, b2)
//
// Returns the home, and the flatItem indices for each session keyed by ID.
func buildTwoGroupHome(t *testing.T) (*Home, map[string]int) {
	t.Helper()

	home := NewHome()
	home.width = 120
	home.height = 40
	home.initialLoading = false

	instances := []*session.Instance{
		session.NewInstanceWithTool("a1", "/tmp/a1", "claude"),
		session.NewInstanceWithTool("a2", "/tmp/a2", "claude"),
		session.NewInstanceWithTool("a3", "/tmp/a3", "claude"),
		session.NewInstanceWithTool("b1", "/tmp/b1", "claude"),
		session.NewInstanceWithTool("b2", "/tmp/b2", "claude"),
	}
	for i := range 3 {
		instances[i].GroupPath = "alpha"
	}
	for i := 3; i < 5; i++ {
		instances[i].GroupPath = "beta"
	}

	home.instancesMu.Lock()
	home.instances = instances
	home.instancesMu.Unlock()
	home.groupTree = session.NewGroupTree(instances)
	home.rebuildFlatItems()

	idx := map[string]int{}
	for i, it := range home.flatItems {
		if it.Type == session.ItemTypeSession && it.Session != nil {
			idx[it.Session.Title] = i
		}
	}
	for _, title := range []string{"a1", "a2", "a3", "b1", "b2"} {
		if _, ok := idx[title]; !ok {
			t.Fatalf("session %q not in flatItems; has: %v", title, idx)
		}
	}
	return home, idx
}

func altKeyMsg(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}, Alt: true}
}

func plainKeyMsg(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

// ---------- Alt+j / Alt+k: next/prev session in current group ----------

func TestGroupNav_AltJ_MovesToNextSessionInGroup(t *testing.T) {
	home, idx := buildTwoGroupHome(t)
	home.cursor = idx["a1"]

	model, _ := home.handleMainKey(altKeyMsg('j'))
	h := model.(*Home)

	if h.cursor != idx["a2"] {
		t.Fatalf("alt+j from a1: cursor = %d, want %d (a2)", h.cursor, idx["a2"])
	}
}

func TestGroupNav_AltJ_DoesNotCrossGroupBoundary(t *testing.T) {
	home, idx := buildTwoGroupHome(t)
	home.cursor = idx["a3"] // last session of group alpha

	model, _ := home.handleMainKey(altKeyMsg('j'))
	h := model.(*Home)

	if h.cursor != idx["a3"] {
		t.Fatalf("alt+j from last-in-group a3: cursor = %d (item %s), want %d (a3, no-op). b1 is at %d",
			h.cursor, describeCursor(h), idx["a3"], idx["b1"])
	}
}

func TestGroupNav_AltK_MovesToPrevSessionInGroup(t *testing.T) {
	home, idx := buildTwoGroupHome(t)
	home.cursor = idx["a3"]

	model, _ := home.handleMainKey(altKeyMsg('k'))
	h := model.(*Home)

	if h.cursor != idx["a2"] {
		t.Fatalf("alt+k from a3: cursor = %d, want %d (a2)", h.cursor, idx["a2"])
	}
}

func TestGroupNav_AltK_DoesNotCrossGroupBoundary(t *testing.T) {
	home, idx := buildTwoGroupHome(t)
	home.cursor = idx["b1"] // first session of group beta

	model, _ := home.handleMainKey(altKeyMsg('k'))
	h := model.(*Home)

	if h.cursor != idx["b1"] {
		t.Fatalf("alt+k from first-in-group b1: cursor = %d, want %d (b1, no-op)",
			h.cursor, idx["b1"])
	}
}

func TestGroupNav_AltJ_FromGroupHeader_GoesToFirstSession(t *testing.T) {
	home, idx := buildTwoGroupHome(t)

	// Find group "alpha" header index
	headerIdx := -1
	for i, it := range home.flatItems {
		if it.Type == session.ItemTypeGroup && it.Path == "alpha" {
			headerIdx = i
			break
		}
	}
	if headerIdx == -1 {
		t.Fatal("could not find alpha group header in flatItems")
	}
	home.cursor = headerIdx

	model, _ := home.handleMainKey(altKeyMsg('j'))
	h := model.(*Home)

	if h.cursor != idx["a1"] {
		t.Fatalf("alt+j from alpha header: cursor = %d, want %d (a1)", h.cursor, idx["a1"])
	}
}

// ---------- Alt+1..9: Nth session in current group ----------

func TestGroupNav_Alt2_JumpsToSecondInGroup(t *testing.T) {
	home, idx := buildTwoGroupHome(t)
	home.cursor = idx["a1"]

	model, _ := home.handleMainKey(altKeyMsg('2'))
	h := model.(*Home)

	if h.cursor != idx["a2"] {
		t.Fatalf("alt+2 in group alpha: cursor = %d, want %d (a2)", h.cursor, idx["a2"])
	}
}

func TestGroupNav_Alt3_JumpsToThirdInGroup(t *testing.T) {
	home, idx := buildTwoGroupHome(t)
	home.cursor = idx["a1"]

	model, _ := home.handleMainKey(altKeyMsg('3'))
	h := model.(*Home)

	if h.cursor != idx["a3"] {
		t.Fatalf("alt+3 in group alpha: cursor = %d, want %d (a3)", h.cursor, idx["a3"])
	}
}

func TestGroupNav_Alt5_BeyondGroup_IsNoop(t *testing.T) {
	home, idx := buildTwoGroupHome(t)
	home.cursor = idx["a1"]

	model, _ := home.handleMainKey(altKeyMsg('5')) // alpha has only 3 sessions
	h := model.(*Home)

	if h.cursor != idx["a1"] {
		t.Fatalf("alt+5 in 3-session group: cursor = %d, want %d (a1, no-op)", h.cursor, idx["a1"])
	}
}

func TestGroupNav_Alt1_InBetaGroup_LandsOnB1(t *testing.T) {
	home, idx := buildTwoGroupHome(t)
	home.cursor = idx["b2"]

	model, _ := home.handleMainKey(altKeyMsg('1'))
	h := model.(*Home)

	if h.cursor != idx["b1"] {
		t.Fatalf("alt+1 while in group beta: cursor = %d, want %d (b1) -- MUST NOT jump to a1 in group alpha",
			h.cursor, idx["b1"])
	}
}

// ---------- Alt+g / Alt+G: first / last session in current group ----------

func TestGroupNav_AltG_LowerCase_JumpsToFirstInGroup(t *testing.T) {
	home, idx := buildTwoGroupHome(t)
	home.cursor = idx["a3"]

	model, _ := home.handleMainKey(altKeyMsg('g'))
	h := model.(*Home)

	if h.cursor != idx["a1"] {
		t.Fatalf("alt+g from a3: cursor = %d, want %d (a1)", h.cursor, idx["a1"])
	}
}

func TestGroupNav_AltShiftG_JumpsToLastInGroup(t *testing.T) {
	home, idx := buildTwoGroupHome(t)
	home.cursor = idx["a1"]

	model, _ := home.handleMainKey(altKeyMsg('G'))
	h := model.(*Home)

	if h.cursor != idx["a3"] {
		t.Fatalf("alt+G from a1: cursor = %d, want %d (a3)", h.cursor, idx["a3"])
	}
}

func TestGroupNav_AltG_InBetaGroup_LandsOnB1NotA1(t *testing.T) {
	home, idx := buildTwoGroupHome(t)
	home.cursor = idx["b2"]

	model, _ := home.handleMainKey(altKeyMsg('g'))
	h := model.(*Home)

	if h.cursor != idx["b1"] {
		t.Fatalf("alt+g in group beta: cursor = %d, want %d (b1, not a1 at %d)",
			h.cursor, idx["b1"], idx["a1"])
	}
}

// ---------- Alt+/: in-group filter search ----------

func TestGroupNav_AltSlash_OpensSearchScopedToCurrentGroup(t *testing.T) {
	home, idx := buildTwoGroupHome(t)
	home.cursor = idx["a2"]

	model, _ := home.handleMainKey(altKeyMsg('/'))
	h := model.(*Home)

	if !h.search.IsVisible() {
		t.Fatal("alt+/ should open the local Search overlay")
	}
	// The scoped search should have only sessions from group alpha in its item pool.
	items := h.search.allItems
	got := map[string]bool{}
	for _, it := range items {
		got[it.Title] = true
	}
	want := []string{"a1", "a2", "a3"}
	for _, title := range want {
		if !got[title] {
			t.Errorf("alt+/ in group alpha: missing %q from scoped search items. Got: %v", title, got)
		}
	}
	for _, title := range []string{"b1", "b2"} {
		if got[title] {
			t.Errorf("alt+/ in group alpha: %q from group beta leaked into scoped search items", title)
		}
	}
}

// ---------- Regression: plain j/k/1-9/g/G// unchanged ----------

func TestGroupNav_Regression_PlainJ_StillMovesDownFlatList(t *testing.T) {
	home, idx := buildTwoGroupHome(t)
	home.cursor = idx["a1"]

	model, _ := home.handleMainKey(plainKeyMsg('j'))
	h := model.(*Home)

	// Plain j is the existing "generic down in flat list" — a1 → a2 (same
	// outcome as alt+j here, but the important assertion is that it ISN'T
	// "in-group-only" semantics. We don't care about the exact index so
	// long as it's +1 from the previous cursor and not a no-op.
	if h.cursor != idx["a1"]+1 {
		t.Fatalf("plain j regression: cursor = %d, want %d (cursor+1)", h.cursor, idx["a1"]+1)
	}
}

func TestGroupNav_Regression_PlainJ_CrossesGroupBoundary(t *testing.T) {
	home, idx := buildTwoGroupHome(t)
	home.cursor = idx["a3"]

	model, _ := home.handleMainKey(plainKeyMsg('j'))
	h := model.(*Home)

	// Plain j from last-in-group must continue into the next item
	// (group header or session of next group). Alt+j would no-op here.
	if h.cursor == idx["a3"] {
		t.Fatal("plain j regression: cursor did not move from a3 -- this regresses existing global nav")
	}
}

func TestGroupNav_Regression_Plain1_JumpsToFirstRootGroup(t *testing.T) {
	home, idx := buildTwoGroupHome(t)
	home.cursor = idx["b2"]

	model, _ := home.handleMainKey(plainKeyMsg('1'))
	h := model.(*Home)

	// Plain 1 still invokes existing jumpToRootGroup which lands on the
	// alpha group header (first root group).
	item := h.flatItems[h.cursor]
	if item.Type != session.ItemTypeGroup || item.Path != "alpha" {
		t.Fatalf("plain 1 regression: cursor = %d (type=%v path=%q), want alpha group header",
			h.cursor, item.Type, item.Path)
	}
}

// ---------- Eval-harness (render + key dispatch + cursor identity) ----------

// TestGroupNav_EvalHarness_RendersAndLandsOnRightSession renders the actual
// TUI (home.View produces a non-empty frame) before and after Alt+N so we
// catch regressions where the key reaches the handler but the rendering path
// around it is broken. Mirrors the spec: "Eval harness test renders TUI with
// 3 sessions in 2 groups, Alt+1/2/3 lands on right session within current
// group."
func TestGroupNav_EvalHarness_RendersAndLandsOnRightSession(t *testing.T) {
	home, idx := buildTwoGroupHome(t)
	// Start on group alpha's first session so "current group" is alpha.
	home.cursor = idx["a1"]

	// Render once so the view pipeline is exercised at least once before
	// we start driving keys through it.
	initial := home.View()
	if initial == "" {
		t.Fatal("initial View() returned empty")
	}

	cases := []struct {
		r    rune
		want string
	}{
		{'1', "a1"},
		{'2', "a2"},
		{'3', "a3"},
	}
	for _, tc := range cases {
		model, _ := home.handleMainKey(altKeyMsg(tc.r))
		h := model.(*Home)
		if h.cursor != idx[tc.want] {
			t.Fatalf("alt+%c in group alpha: cursor on %s, want %s",
				tc.r, describeCursor(h), tc.want)
		}
		frame := h.View()
		if frame == "" {
			t.Fatalf("alt+%c produced empty View() frame", tc.r)
		}
		home = h
	}

	// Switch to beta group, Alt+1 must land on b1 (not a1 in alpha).
	home.cursor = idx["b2"]
	model, _ := home.handleMainKey(altKeyMsg('1'))
	h := model.(*Home)
	if h.cursor != idx["b1"] {
		t.Fatalf("alt+1 in group beta: cursor on %s, want b1", describeCursor(h))
	}
	if h.View() == "" {
		t.Fatal("post-alt+1 View() is empty in beta group")
	}
}

// ---------- First-launch nav-hint (discoverability) ----------

// TestNavHint_ShownOnFirstLaunch_DismissedAfterKeypress exercises the
// discoverability path end-to-end: sentinel absent -> hint shows -> first
// keypress dismisses and leaves a sentinel file so it never shows again.
func TestNavHint_ShownOnFirstLaunch_DismissedAfterKeypress(t *testing.T) {
	// Isolate HOME and disable the test-profile bypass so the hint code runs.
	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	origXDGData := os.Getenv("XDG_DATA_HOME")
	origProfile := os.Getenv("AGENTDECK_PROFILE")
	os.Setenv("HOME", tmpHome)
	os.Setenv("XDG_DATA_HOME", filepath.Join(tmpHome, ".local", "share"))
	os.Unsetenv("AGENTDECK_PROFILE")
	t.Cleanup(func() {
		os.Setenv("HOME", origHome)
		os.Setenv("XDG_DATA_HOME", origXDGData)
		os.Setenv("AGENTDECK_PROFILE", origProfile)
	})

	if navHintAlreadyShown() {
		t.Fatal("precondition: sentinel should NOT exist in isolated HOME")
	}

	home := NewHome()
	home.width = 120
	home.height = 40
	home.initialLoading = false

	if !home.navHintActive {
		t.Fatal("expected navHintActive=true after fresh NewHome with no sentinel")
	}
	if home.maintenanceMsg != navHintText {
		t.Fatalf("maintenanceMsg = %q, want %q", home.maintenanceMsg, navHintText)
	}

	// Sentinel should already be written (write-through on display, so the
	// hint never repeats even if the TUI crashes before first keypress).
	sentinel := filepath.Join(tmpHome, ".local", "share", "agent-deck", ".nav-hint-v1760-shown")
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("sentinel not written at %s: %v", sentinel, err)
	}

	// First keypress dismisses.
	model, _ := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	h := model.(*Home)
	if h.navHintActive {
		t.Error("navHintActive should clear after first keypress")
	}
	if h.maintenanceMsg != "" {
		t.Errorf("maintenanceMsg = %q, want empty after dismissal", h.maintenanceMsg)
	}
}

// TestNavHint_SkippedWhenSentinelExists verifies the hint does not re-show
// on subsequent launches (sentinel already present).
func TestNavHint_SkippedWhenSentinelExists(t *testing.T) {
	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	origXDGData := os.Getenv("XDG_DATA_HOME")
	origProfile := os.Getenv("AGENTDECK_PROFILE")
	os.Setenv("HOME", tmpHome)
	os.Setenv("XDG_DATA_HOME", filepath.Join(tmpHome, ".local", "share"))
	os.Unsetenv("AGENTDECK_PROFILE")
	t.Cleanup(func() {
		os.Setenv("HOME", origHome)
		os.Setenv("XDG_DATA_HOME", origXDGData)
		os.Setenv("AGENTDECK_PROFILE", origProfile)
	})

	// Pre-seed sentinel.
	sentinel := filepath.Join(tmpHome, ".local", "share", "agent-deck", ".nav-hint-v1760-shown")
	if err := os.MkdirAll(filepath.Dir(sentinel), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sentinel, []byte("seeded\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	home := NewHome()
	if home.navHintActive {
		t.Error("navHintActive should be false when sentinel exists")
	}
	if home.maintenanceMsg == navHintText {
		t.Error("maintenanceMsg should NOT be the nav hint when sentinel exists")
	}
}

// ---------- helpers ----------

func describeCursor(h *Home) string {
	if h.cursor < 0 || h.cursor >= len(h.flatItems) {
		return "<out-of-range>"
	}
	it := h.flatItems[h.cursor]
	switch it.Type {
	case session.ItemTypeGroup:
		return "group:" + it.Path
	case session.ItemTypeSession:
		if it.Session != nil {
			return "session:" + it.Session.Title + "@" + it.Session.GroupPath
		}
	}
	return "other"
}
