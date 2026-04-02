package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// conductorInstance builds a minimal conductor session for testing.
func conductorInstance(id, name, path string) *session.Instance {
	return &session.Instance{
		ID:          id,
		Title:       "conductor-" + name,
		GroupPath:   "conductor",
		ProjectPath: path,
		Status:      session.StatusWaiting,
		IsConductor: true,
	}
}

// showWithConductors is a test helper that opens the dialog with the given
// conductors and optional suggested parent ID.
func showWithConductors(t *testing.T, conductors []*session.Instance, suggestedID string) *NewDialog {
	t.Helper()
	d := NewNewDialog()
	d.ShowInGroup("default", "default", "/tmp", conductors, suggestedID)
	return d
}

// --- ShowInGroup / pre-selection ---

func TestShowInGroup_NoConductors_NoRow(t *testing.T) {
	d := showWithConductors(t, nil, "")
	if len(d.conductorSessions) != 0 {
		t.Errorf("conductorSessions: got %d, want 0", len(d.conductorSessions))
	}
	if d.conductorCursor != 0 {
		t.Errorf("conductorCursor: got %d, want 0", d.conductorCursor)
	}
}

func TestShowInGroup_ResetsSelectionOnReopen(t *testing.T) {
	cs := []*session.Instance{conductorInstance("a", "main", "/a")}
	d := showWithConductors(t, cs, "a") // pre-selects a
	if d.conductorCursor != 1 {
		t.Fatalf("pre-selection failed: got cursor %d, want 1", d.conductorCursor)
	}
	// Re-open with no suggestion → cursor resets to 0 (None).
	d.ShowInGroup("default", "default", "/tmp", cs, "")
	if d.conductorCursor != 0 {
		t.Errorf("cursor after reopen without suggestion: got %d, want 0", d.conductorCursor)
	}
}

func TestShowInGroup_PreSelectsFirstConductor(t *testing.T) {
	cs := []*session.Instance{
		conductorInstance("id-1", "alpha", "/alpha"),
		conductorInstance("id-2", "beta", "/beta"),
	}
	d := showWithConductors(t, cs, "id-1")
	if d.conductorCursor != 1 {
		t.Errorf("conductorCursor: got %d, want 1 (first conductor)", d.conductorCursor)
	}
}

func TestShowInGroup_PreSelectsSecondConductor(t *testing.T) {
	cs := []*session.Instance{
		conductorInstance("id-1", "alpha", "/alpha"),
		conductorInstance("id-2", "beta", "/beta"),
	}
	d := showWithConductors(t, cs, "id-2")
	if d.conductorCursor != 2 {
		t.Errorf("conductorCursor: got %d, want 2 (second conductor)", d.conductorCursor)
	}
}

func TestShowInGroup_UnknownSuggestionDefaultsToNone(t *testing.T) {
	cs := []*session.Instance{conductorInstance("id-1", "main", "/main")}
	d := showWithConductors(t, cs, "does-not-exist")
	if d.conductorCursor != 0 {
		t.Errorf("conductorCursor: got %d, want 0 (None) for unknown ID", d.conductorCursor)
	}
}

// --- GetParentSessionID / GetParentProjectPath ---

func TestGetParentSessionID_NoneSelected(t *testing.T) {
	cs := []*session.Instance{conductorInstance("id-1", "main", "/main")}
	d := showWithConductors(t, cs, "") // cursor = 0 = None
	if got := d.GetParentSessionID(); got != "" {
		t.Errorf("GetParentSessionID: got %q, want empty when None selected", got)
	}
	if got := d.GetParentProjectPath(); got != "" {
		t.Errorf("GetParentProjectPath: got %q, want empty when None selected", got)
	}
}

func TestGetParentSessionID_ConductorSelected(t *testing.T) {
	cs := []*session.Instance{
		conductorInstance("id-1", "alpha", "/path/alpha"),
		conductorInstance("id-2", "beta", "/path/beta"),
	}
	d := showWithConductors(t, cs, "id-2")
	if got := d.GetParentSessionID(); got != "id-2" {
		t.Errorf("GetParentSessionID: got %q, want %q", got, "id-2")
	}
	if got := d.GetParentProjectPath(); got != "/path/beta" {
		t.Errorf("GetParentProjectPath: got %q, want %q", got, "/path/beta")
	}
}

func TestGetParentSessionID_NoConductors(t *testing.T) {
	d := showWithConductors(t, nil, "")
	if got := d.GetParentSessionID(); got != "" {
		t.Errorf("GetParentSessionID: got %q, want empty with no conductors", got)
	}
}

// --- focusConductor in focus targets ---

func TestRebuildFocusTargets_NoConductors_ExcludesConductorField(t *testing.T) {
	d := showWithConductors(t, nil, "")
	for _, ft := range d.focusTargets {
		if ft == focusConductor {
			t.Error("focusConductor should not be in focusTargets when no conductors exist")
		}
	}
}

func TestRebuildFocusTargets_WithConductors_IncludesConductorField(t *testing.T) {
	cs := []*session.Instance{conductorInstance("id-1", "main", "/main")}
	d := showWithConductors(t, cs, "")
	found := false
	for _, ft := range d.focusTargets {
		if ft == focusConductor {
			found = true
			break
		}
	}
	if !found {
		t.Error("focusConductor should be in focusTargets when conductors exist")
	}
}

// --- Up/down navigation ---

func sendSpecialKey(d *NewDialog, keyType tea.KeyType) *NewDialog {
	updated, _ := d.Update(tea.KeyMsg{Type: keyType})
	return updated
}

// focusOnConductor tabs through focus targets until focusConductor is active.
func focusOnConductor(t *testing.T, d *NewDialog) *NewDialog {
	t.Helper()
	for range d.focusTargets {
		if d.currentTarget() == focusConductor {
			return d
		}
		d = sendSpecialKey(d, tea.KeyTab)
	}
	if d.currentTarget() != focusConductor {
		t.Fatal("could not focus conductor field")
	}
	return d
}

func TestConductorNavigation_DownMovesSelection(t *testing.T) {
	cs := []*session.Instance{
		conductorInstance("id-1", "alpha", "/alpha"),
		conductorInstance("id-2", "beta", "/beta"),
	}
	d := showWithConductors(t, cs, "")
	d = focusOnConductor(t, d)

	if d.conductorCursor != 0 {
		t.Fatalf("initial cursor: got %d, want 0", d.conductorCursor)
	}
	d = sendSpecialKey(d, tea.KeyDown)
	if d.conductorCursor != 1 {
		t.Errorf("after down: got %d, want 1", d.conductorCursor)
	}
	d = sendSpecialKey(d, tea.KeyDown)
	if d.conductorCursor != 2 {
		t.Errorf("after second down: got %d, want 2", d.conductorCursor)
	}
}

func TestConductorNavigation_DownAtLastItemMovesFocus(t *testing.T) {
	cs := []*session.Instance{conductorInstance("id-1", "main", "/main")}
	d := showWithConductors(t, cs, "id-1") // cursor = 1 (last item)
	// Use claude so focusOptions is appended after focusConductor,
	// ensuring there is always a next field to advance to.
	d.SetDefaultTool("claude")
	d = focusOnConductor(t, d)

	// At the last item, Down should advance focus (not stay on conductor).
	d = sendSpecialKey(d, tea.KeyDown)
	if d.currentTarget() == focusConductor {
		t.Error("focus should have moved away from conductor after Down on last item")
	}
}

func TestConductorNavigation_UpMovesSelection(t *testing.T) {
	cs := []*session.Instance{
		conductorInstance("id-1", "alpha", "/alpha"),
		conductorInstance("id-2", "beta", "/beta"),
	}
	d := showWithConductors(t, cs, "id-2") // cursor = 2
	d = focusOnConductor(t, d)

	d = sendSpecialKey(d, tea.KeyUp)
	if d.conductorCursor != 1 {
		t.Errorf("after up: got %d, want 1", d.conductorCursor)
	}
}

func TestConductorNavigation_UpAtNoneMovesBackFocus(t *testing.T) {
	cs := []*session.Instance{conductorInstance("id-1", "main", "/main")}
	d := showWithConductors(t, cs, "") // cursor = 0 (None)
	d = focusOnConductor(t, d)

	// At None (first item), Up should retreat focus to previous field.
	d = sendSpecialKey(d, tea.KeyUp)
	if d.currentTarget() == focusConductor {
		t.Error("focus should have moved away from conductor after Up on None")
	}
}

// --- View rendering ---

func TestView_ConductorRowNotRendered_WhenNoConductors(t *testing.T) {
	d := showWithConductors(t, nil, "")
	view := d.View()
	if strings.Contains(view, "Conducting parent") {
		t.Error("conductor row should not appear when no conductors exist")
	}
}

func TestView_ConductorRowRendered_WhenConductorsExist(t *testing.T) {
	cs := []*session.Instance{conductorInstance("id-1", "main", "/main")}
	d := showWithConductors(t, cs, "")
	view := d.View()
	if !strings.Contains(view, "Conducting parent") {
		t.Error("conductor row should appear when conductors exist")
	}
}

func TestView_ShowsNoneAsDefaultSelection(t *testing.T) {
	cs := []*session.Instance{conductorInstance("id-1", "main", "/main")}
	d := showWithConductors(t, cs, "")
	view := d.View()
	if !strings.Contains(view, "None") {
		t.Error("view should show 'None' as an option")
	}
}

func TestView_ShowsConductorName(t *testing.T) {
	cs := []*session.Instance{conductorInstance("id-1", "myteam", "/work")}
	d := showWithConductors(t, cs, "")
	view := d.View()
	if !strings.Contains(view, "myteam") {
		t.Error("view should show conductor name (without 'conductor-' prefix)")
	}
	if strings.Contains(view, "conductor-myteam") {
		t.Error("view should strip the 'conductor-' prefix from the conductor name")
	}
}

func TestView_SelectedConductorMarked(t *testing.T) {
	cs := []*session.Instance{conductorInstance("id-1", "main", "/main")}
	d := showWithConductors(t, cs, "id-1") // pre-select main
	view := d.View()
	if !strings.Contains(view, "▶") {
		t.Error("selected conductor should be marked with ▶")
	}
}
