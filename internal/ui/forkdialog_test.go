package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	tea "github.com/charmbracelet/bubbletea"
)

func TestForkDialog_WorktreeControlsVisibleForBareProjectRoot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	projectRoot := t.TempDir()
	bareDir := filepath.Join(projectRoot, ".bare")
	if err := exec.Command("git", "init", "--bare", bareDir).Run(); err != nil {
		t.Fatalf("git init --bare: %v", err)
	}

	d := NewForkDialog()
	d.SetSize(90, 40)
	d.Show("Bare Root Parent", projectRoot, "", nil, "")

	view := d.View()
	if !strings.Contains(view, "Create in worktree") {
		t.Fatalf("bare project root should show worktree controls; view:\n%s", view)
	}

	// 'w' toggles worktree only while the group field has focus (B2 focus model;
	// see the toggle-off test below). Move focus before pressing w.
	d.setFocus(forkFocusGroup)
	before := d.IsWorktreeEnabled()
	updated, _ := d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})
	if updated.IsWorktreeEnabled() == before {
		t.Fatal("pressing w should toggle worktree mode for a bare project root")
	}
}

func TestNewForkDialog(t *testing.T) {
	d := NewForkDialog()
	if d == nil {
		t.Fatal("NewForkDialog() returned nil")
	}
	if d.IsVisible() {
		t.Error("Dialog should not be visible initially")
	}
}

func TestForkDialog_Show(t *testing.T) {
	d := NewForkDialog()
	d.Show("Original Session", "/path/to/project", "group/path", nil, "")

	if !d.IsVisible() {
		t.Error("Dialog should be visible after Show()")
	}

	name, group := d.GetValues()
	if name != "Original Session (fork)" {
		t.Errorf("Name = %s, want 'Original Session (fork)'", name)
	}
	if group != "group/path" {
		t.Errorf("Group = %s, want 'group/path'", group)
	}
}

func TestForkDialog_Show_UsesConfiguredWorktreeDefault(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	tempDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", originalHome)
	session.ClearUserConfigCache()
	defer session.ClearUserConfigCache()

	agentDeckDir := filepath.Join(tempDir, ".agent-deck")
	if err := os.MkdirAll(agentDeckDir, 0700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := session.SaveUserConfig(&session.UserConfig{
		Worktree: session.WorktreeSettings{DefaultEnabled: true},
	}); err != nil {
		t.Fatalf("SaveUserConfig: %v", err)
	}
	session.ClearUserConfigCache()

	// The config default only applies when the project is worktree-capable
	// (F6), so use a real git repo as the project path.
	projectPath := filepath.Join(tempDir, "project")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatalf("MkdirAll project: %v", err)
	}
	if err := exec.Command("git", "init", projectPath).Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	d := NewForkDialog()
	d.Show("Original Session", projectPath, "group/path", nil, "")

	if !d.worktreeEnabled {
		t.Error("worktreeEnabled should default to true from config on Show")
	}
}

func TestForkDialog_Hide(t *testing.T) {
	d := NewForkDialog()
	d.Show("Test", "/path", "group", nil, "")

	if !d.IsVisible() {
		t.Error("Dialog should be visible after Show()")
	}

	d.Hide()

	if d.IsVisible() {
		t.Error("Dialog should not be visible after Hide()")
	}
}

func TestForkDialog_GetValues(t *testing.T) {
	d := NewForkDialog()
	d.Show("My Session", "/project", "work/team", nil, "")

	name, group := d.GetValues()
	if name != "My Session (fork)" {
		t.Errorf("Name = %s, want 'My Session (fork)'", name)
	}
	if group != "work/team" {
		t.Errorf("Group = %s, want 'work/team'", group)
	}
}

func TestForkDialog_SetSize(t *testing.T) {
	d := NewForkDialog()
	d.SetSize(100, 50)

	if d.width != 100 {
		t.Errorf("Width = %d, want 100", d.width)
	}
	if d.height != 50 {
		t.Errorf("Height = %d, want 50", d.height)
	}
}

func TestForkDialog_EmptyProjectPath(t *testing.T) {
	d := NewForkDialog()
	d.Show("Test", "", "", nil, "")

	if !d.IsVisible() {
		t.Error("Dialog should be visible even with empty paths")
	}

	name, group := d.GetValues()
	if name != "Test (fork)" {
		t.Errorf("Name = %s, want 'Test (fork)'", name)
	}
	if group != "" {
		t.Errorf("Group = %s, want ''", group)
	}
}

// ===== Validate & Inline Error Tests (Issue #93) =====

func TestForkDialog_CharLimitMatchesMaxNameLength(t *testing.T) {
	d := NewForkDialog()
	if d.nameInput.CharLimit != MaxNameLength {
		t.Errorf("nameInput.CharLimit = %d, want %d (MaxNameLength)", d.nameInput.CharLimit, MaxNameLength)
	}
}

func TestForkDialog_Validate_EmptyName(t *testing.T) {
	d := NewForkDialog()
	d.nameInput.SetValue("")

	err := d.Validate()
	if err == "" {
		t.Error("Validate() should reject empty names")
	}
	if err != "Session name cannot be empty" {
		t.Errorf("Unexpected error: %q", err)
	}
}

func TestForkDialog_CharLimitTruncatesLongNames(t *testing.T) {
	d := NewForkDialog()
	longName := strings.Repeat("x", MaxNameLength+10)
	d.nameInput.SetValue(longName)

	// CharLimit should truncate the value to MaxNameLength
	actual := d.nameInput.Value()
	if len(actual) > MaxNameLength {
		t.Errorf("nameInput should truncate to MaxNameLength (%d), but got length %d", MaxNameLength, len(actual))
	}

	// Validation should pass since the textinput truncated
	err := d.Validate()
	if err != "" {
		t.Errorf("Validate() should pass after CharLimit truncation, got: %q", err)
	}
}

func TestForkDialog_Validate_ValidName(t *testing.T) {
	d := NewForkDialog()
	d.nameInput.SetValue("my-fork")

	err := d.Validate()
	if err != "" {
		t.Errorf("Validate() should accept valid name, got: %q", err)
	}
}

func TestForkDialog_SetError_ShowsInView(t *testing.T) {
	d := NewForkDialog()
	d.SetSize(80, 40)
	d.Show("Test", "/path", "group", nil, "")

	d.SetError("Name is required")
	view := d.View()

	if !strings.Contains(view, "Name is required") {
		t.Error("View should display the inline error message")
	}
}

func TestForkDialog_ClearError_HidesFromView(t *testing.T) {
	d := NewForkDialog()
	d.SetSize(80, 40)
	d.Show("Test", "/path", "group", nil, "")

	d.SetError("Name is required")
	d.ClearError()
	view := d.View()

	if strings.Contains(view, "Name is required") {
		t.Error("View should not display the error after ClearError()")
	}
}

func TestForkDialog_Show_ClearsError(t *testing.T) {
	d := NewForkDialog()
	d.SetError("Previous error")
	d.Show("Test", "/path", "group", nil, "")

	if d.validationErr != "" {
		t.Error("Show() should clear validationErr")
	}
}

// ===== Fork-with-state dialog state (PR-B Task B1) =====

func TestForkDialog_WithState_HiddenWhenWorktreeUnavailable(t *testing.T) {
	d := NewForkDialog()
	d.Show("Test", t.TempDir(), "group", nil, "")
	if d.IsWorktreeEnabled() {
		t.Fatal("worktree should be disabled when the project is not git-capable")
	}
	if d.IsWithStateEnabled() {
		t.Error("with-state should stay off when no worktree can be created")
	}
	if d.IsWithStateAndGitignoredEnabled() {
		t.Error("gitignored should stay off when no worktree can be created")
	}
}

func TestForkDialog_ToggleWithState_NoOpUnlessWorktreeEnabled(t *testing.T) {
	d := NewForkDialog()
	d.Show("Test", "/path", "group", nil, "")
	d.worktreeEnabled = false
	d.ToggleWithState()
	if d.IsWithStateEnabled() {
		t.Error("ToggleWithState must be a no-op when worktree is disabled")
	}
}

func TestForkDialog_ToggleWithState_EnablesWhenWorktreeEnabled(t *testing.T) {
	d := NewForkDialog()
	d.Show("Test", "/path", "group", nil, "")
	d.worktreeEnabled = true
	d.ToggleWithState()
	if !d.IsWithStateEnabled() {
		t.Error("ToggleWithState should enable with-state when worktree is on")
	}
}

func TestForkDialog_ToggleWorktreeOff_ClearsWithStateAndGitignored(t *testing.T) {
	d := NewForkDialog()
	d.Show("Test", "/path", "group", nil, "")
	d.worktreeEnabled = true
	d.ToggleWithState()              // with-state on
	d.ToggleWithStateAndGitignored() // gitignored on
	d.ToggleWorktree()               // worktree off -> must clear nested state
	if d.IsWorktreeEnabled() {
		t.Fatal("precondition: worktree should now be off")
	}
	if d.IsWithStateEnabled() {
		t.Error("turning worktree off must clear with-state")
	}
	if d.IsWithStateAndGitignoredEnabled() {
		t.Error("turning worktree off must clear gitignored")
	}
}

func TestForkDialog_ToggleGitignored_NoOpUnlessWithStateEnabled(t *testing.T) {
	d := NewForkDialog()
	d.Show("Test", "/path", "group", nil, "")
	d.worktreeEnabled = true // with-state still off
	d.ToggleWithStateAndGitignored()
	if d.IsWithStateAndGitignoredEnabled() {
		t.Error("ToggleWithStateAndGitignored must be a no-op when with-state is off")
	}
}

func TestForkDialog_ToggleGitignored_EnablesWhenWithStateEnabled(t *testing.T) {
	d := NewForkDialog()
	d.Show("Test", "/path", "group", nil, "")
	d.worktreeEnabled = true
	d.ToggleWithState()
	d.ToggleWithStateAndGitignored()
	if !d.IsWithStateAndGitignoredEnabled() {
		t.Error("ToggleWithStateAndGitignored should enable gitignored when with-state on")
	}
}

func TestForkDialog_ToggleWithStateOff_ClearsGitignored(t *testing.T) {
	d := NewForkDialog()
	d.Show("Test", "/path", "group", nil, "")
	d.worktreeEnabled = true
	d.ToggleWithState()              // on
	d.ToggleWithStateAndGitignored() // gitignored on
	d.ToggleWithState()              // off -> must clear gitignored
	if d.IsWithStateEnabled() {
		t.Fatal("precondition: with-state should now be off")
	}
	if d.IsWithStateAndGitignoredEnabled() {
		t.Error("turning with-state off must clear gitignored")
	}
}

// ===== Fork-with-state rendering + keys (PR-B Task B3) =====

func TestForkDialog_View_ShowsCarryStateAndHintWhenWorktreeOn(t *testing.T) {
	d := NewForkDialog()
	d.SetSize(80, 40)
	d.Show("T", "/p", "g", nil, "")
	d.worktreeEnabled = true
	d.worktreeCapable = true
	d.updateFocus()
	view := d.View()
	if !strings.Contains(view, "Carry parent state") {
		t.Errorf("worktree-on view should show 'Carry parent state'; view:\n%s", view)
	}
	if !strings.Contains(view, "creates a NEW branch at parent HEAD") {
		t.Errorf("view should show the new-branch hint; view:\n%s", view)
	}
}

func TestForkDialog_View_HidesCarryStateWhenWorktreeOff(t *testing.T) {
	d := NewForkDialog()
	d.SetSize(80, 40)
	d.Show("T", "/p", "g", nil, "")
	d.worktreeEnabled = false
	d.worktreeCapable = true
	d.updateFocus()
	if strings.Contains(d.View(), "Carry parent state") {
		t.Error("worktree-off view must not show the with-state checkbox")
	}
}

func TestForkDialog_View_ShowsGitignoredOnlyWhenWithStateOn(t *testing.T) {
	d := NewForkDialog()
	d.SetSize(80, 40)
	d.Show("T", "/p", "g", nil, "")
	d.worktreeEnabled = true
	d.worktreeCapable = true
	d.updateFocus()
	if strings.Contains(d.View(), "Include gitignored files") {
		t.Error("gitignored checkbox must be hidden when with-state off")
	}
	d.ToggleWithState()
	if !strings.Contains(d.View(), "Include gitignored files") {
		t.Error("gitignored checkbox must show when with-state on")
	}
}

func TestForkDialog_Space_TogglesFocusedCarryState(t *testing.T) {
	d := NewForkDialog()
	d.SetSize(80, 40)
	d.Show("T", "/p", "g", nil, "")
	d.worktreeCapable = true
	d.worktreeEnabled = true
	d.setFocus(forkFocusCarryState)
	if d.currentFocusName() != "carryState" {
		t.Fatalf("precondition: focus should be carryState, got %s", d.currentFocusName())
	}
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeySpace})
	if !d.IsWithStateEnabled() {
		t.Error("Space on carryState should enable with-state")
	}
}

func TestForkDialog_Space_TogglesFocusedGitignored(t *testing.T) {
	d := NewForkDialog()
	d.SetSize(80, 40)
	d.Show("T", "/p", "g", nil, "")
	d.worktreeCapable = true
	d.worktreeEnabled = true
	d.ToggleWithState()
	d.setFocus(forkFocusGitignored)
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeySpace})
	if !d.IsWithStateAndGitignoredEnabled() {
		t.Error("Space on gitignored should enable gitignored")
	}
}

func TestForkDialog_Y_TogglesWithStateFromGroup(t *testing.T) {
	d := NewForkDialog()
	d.SetSize(80, 40)
	d.Show("T", "/p", "g", nil, "")
	d.worktreeEnabled = true
	d.setFocus(forkFocusGroup)
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if !d.IsWithStateEnabled() {
		t.Error("y on the group row should toggle with-state on")
	}
}

func TestForkDialog_I_TogglesGitignoredFromGroup(t *testing.T) {
	d := NewForkDialog()
	d.SetSize(80, 40)
	d.Show("T", "/p", "g", nil, "")
	d.worktreeEnabled = true
	d.ToggleWithState()
	d.setFocus(forkFocusGroup)
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}})
	if !d.IsWithStateAndGitignoredEnabled() {
		t.Error("i on the group row should toggle gitignored on")
	}
}

func TestForkDialog_I_TypeableInBranchField(t *testing.T) {
	// Decision 3: i/y must remain typeable in text inputs (branch names contain 'i').
	d := NewForkDialog()
	d.SetSize(80, 40)
	d.Show("T", "/p", "g", nil, "")
	d.worktreeCapable = true
	d.worktreeEnabled = true
	d.setFocus(forkFocusBranch)
	d.branchInput.SetValue("")
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}})
	if d.branchInput.Value() != "i" {
		t.Errorf("i should be typeable in the branch field, got %q", d.branchInput.Value())
	}
}

// TestForkDialog_NameInput_AcceptsUnderscore verifies that typing '_' into the
// name input reaches the textinput buffer (regression test for BUG-02).
func TestForkDialog_NameInput_AcceptsUnderscore(t *testing.T) {
	d := NewForkDialog()
	d.Show("Original Session", "/path/to/project", "group/path", nil, "")

	// focusIndex defaults to 0 (name input) after Show; ensure name input is focused.
	d.nameInput.SetValue("")

	underscoreKey := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'_'}}
	updated, _ := d.Update(underscoreKey)

	if updated.nameInput.Value() != "_" {
		t.Errorf("nameInput.Value() = %q after typing '_', want %q", updated.nameInput.Value(), "_")
	}
}

// --- Focus-target navigation (PR-B Task B2) ---

// forkConductorInstance builds a minimal conductor *session.Instance for fork
// dialog tests (mirrors the helper used by newdialog_conductor_test.go).
func forkConductorInstance(id, name, path string) *session.Instance {
	return &session.Instance{
		ID:          id,
		Title:       "conductor-" + name,
		GroupPath:   "conductor",
		ProjectPath: path,
		Status:      session.StatusWaiting,
		IsConductor: true,
	}
}

// tabOrder drives the dialog with the given key and records currentFocusName()
// after each step, starting from the initial focus before any key.
func tabOrder(d *ForkDialog, key tea.KeyMsg, steps int) []string {
	got := []string{d.currentFocusName()}
	for i := 0; i < steps; i++ {
		d, _ = d.Update(key)
		got = append(got, d.currentFocusName())
	}
	return got
}

func TestForkDialog_Focus_InitialIsName(t *testing.T) {
	d := NewForkDialog()
	d.Show("Test", "/path", "group", nil, "")
	if got := d.currentFocusName(); got != "name" {
		t.Errorf("initial focus = %q, want %q", got, "name")
	}
}

func TestForkDialog_Focus_WorktreeOff_TabOrder(t *testing.T) {
	d := NewForkDialog()
	d.Show("Test", "/path", "group", nil, "")
	d.worktreeEnabled = false

	tab := tea.KeyMsg{Type: tea.KeyTab}
	got := tabOrder(d, tab, 3)
	want := []string{"name", "group", "options", "options"}
	if !equalStrs(got, want) {
		t.Errorf("Tab order = %v, want %v", got, want)
	}
}

func TestForkDialog_Focus_WorktreeOff_ShiftTabReverses(t *testing.T) {
	d := NewForkDialog()
	d.Show("Test", "/path", "group", nil, "")
	d.worktreeEnabled = false

	// Move to options first.
	tab := tea.KeyMsg{Type: tea.KeyTab}
	d, _ = d.Update(tab)
	d, _ = d.Update(tab)
	if got := d.currentFocusName(); got != "options" {
		t.Fatalf("after 2 tabs focus = %q, want options", got)
	}

	shiftTab := tea.KeyMsg{Type: tea.KeyShiftTab}
	d, _ = d.Update(shiftTab)
	if got := d.currentFocusName(); got != "group" {
		t.Errorf("after shift+tab focus = %q, want group", got)
	}
	d, _ = d.Update(shiftTab)
	if got := d.currentFocusName(); got != "name" {
		t.Errorf("after 2 shift+tab focus = %q, want name", got)
	}
}

func TestForkDialog_Focus_WorktreeOn_WithStateOff_TabOrder(t *testing.T) {
	d := NewForkDialog()
	d.Show("Test", "/path", "group", nil, "")
	d.worktreeCapable = true
	d.worktreeEnabled = true

	tab := tea.KeyMsg{Type: tea.KeyTab}
	got := tabOrder(d, tab, 4)
	want := []string{"name", "group", "branch", "carryState", "options"}
	if !equalStrs(got, want) {
		t.Errorf("Tab order = %v, want %v", got, want)
	}
}

func TestForkDialog_Focus_WorktreeOn_WithStateOn_TabOrder(t *testing.T) {
	d := NewForkDialog()
	d.Show("Test", "/path", "group", nil, "")
	d.worktreeCapable = true
	d.worktreeEnabled = true
	d.ToggleWithState()
	if !d.IsWithStateEnabled() {
		t.Fatal("ToggleWithState should have enabled with-state")
	}

	tab := tea.KeyMsg{Type: tea.KeyTab}
	got := tabOrder(d, tab, 5)
	want := []string{"name", "group", "branch", "carryState", "gitignored", "options"}
	if !equalStrs(got, want) {
		t.Errorf("Tab order = %v, want %v", got, want)
	}
}

func TestForkDialog_Focus_Conductor_BetweenGroupAndOptions(t *testing.T) {
	cs := []*session.Instance{forkConductorInstance("id-1", "alpha", "/a")}
	d := NewForkDialog()
	d.Show("Test", "/path", "group", cs, "")
	d.worktreeEnabled = false

	tab := tea.KeyMsg{Type: tea.KeyTab}
	got := tabOrder(d, tab, 3)
	want := []string{"name", "group", "conductor", "options"}
	if !equalStrs(got, want) {
		t.Errorf("Tab order with conductor = %v, want %v", got, want)
	}
}

func TestForkDialog_Focus_Conductor_WorktreeOn_Order(t *testing.T) {
	cs := []*session.Instance{forkConductorInstance("id-1", "alpha", "/a")}
	d := NewForkDialog()
	d.Show("Test", "/path", "group", cs, "")
	d.worktreeCapable = true
	d.worktreeEnabled = true

	tab := tea.KeyMsg{Type: tea.KeyTab}
	got := tabOrder(d, tab, 5)
	want := []string{"name", "group", "conductor", "branch", "carryState", "options"}
	if !equalStrs(got, want) {
		t.Errorf("Tab order conductor+worktree = %v, want %v", got, want)
	}
}

func TestForkDialog_Focus_DanglingIndexReadsSafely(t *testing.T) {
	d := NewForkDialog()
	d.Show("Test", "/path", "group", nil, "")
	d.worktreeCapable = true
	d.worktreeEnabled = true

	// Land focus on carryState (a worktree-only target).
	tab := tea.KeyMsg{Type: tea.KeyTab}
	d, _ = d.Update(tab) // group
	d, _ = d.Update(tab) // branch
	d, _ = d.Update(tab) // carryState
	if got := d.currentFocusName(); got != "carryState" {
		t.Fatalf("setup: focus = %q, want carryState", got)
	}

	// Collapse the worktree-only targets out from under the cursor without
	// clamping. Reading focus must not panic and must yield a valid target.
	d.worktreeEnabled = false
	got := d.currentFocusName()
	if got != "name" && got != "group" && got != "options" {
		t.Errorf("dangling focus read = %q, want a valid surviving target", got)
	}
}

func TestForkDialog_Focus_ToggleWorktreeOffViaW_ReclampsIndex(t *testing.T) {
	d := NewForkDialog()
	d.Show("Test", "/path", "group", nil, "")
	d.worktreeCapable = true
	d.worktreeEnabled = true

	// Focus the group field (where 'w' toggles worktree), then disable it.
	d.setFocus(forkFocusGroup)
	w := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}}
	d, _ = d.Update(w)

	if d.worktreeEnabled {
		t.Fatal("'w' on group should have toggled worktree off")
	}
	// focusIndex must remain within the (now shorter) slice.
	if d.focusIndex < 0 || d.focusIndex >= len(d.focusTargets()) {
		t.Errorf("focusIndex %d out of range for %d targets", d.focusIndex, len(d.focusTargets()))
	}
}

func equalStrs(a, b []string) bool {
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

// F6: a dialog that is not worktree-capable must never expose worktree focus
// stops, even if worktreeEnabled was somehow set true.
func TestForkDialog_Focus_NonCapableHidesWorktreeTargets(t *testing.T) {
	d := NewForkDialog()
	d.Show("Test", "/path", "group", nil, "")
	d.worktreeCapable = false
	d.worktreeEnabled = true
	d.withStateEnabled = true

	for _, target := range d.focusTargets() {
		switch target {
		case forkFocusBranch, forkFocusCarryState, forkFocusGitignored:
			t.Fatalf("non-capable dialog exposed worktree focus target %v", target)
		}
	}

	// Tab-walking must also never land on a worktree stop.
	tab := tea.KeyMsg{Type: tea.KeyTab}
	got := tabOrder(d, tab, len(d.focusTargets()))
	for _, name := range got {
		if name == "branch" || name == "carryState" || name == "gitignored" {
			t.Fatalf("tab walk reached worktree stop %q on non-capable dialog: %v", name, got)
		}
	}
}

// F7/F10: Enter on a focused carry-state checkbox toggles it (does not submit).
func TestForkDialog_Enter_TogglesFocusedCarryState(t *testing.T) {
	d := NewForkDialog()
	d.SetSize(80, 40)
	d.Show("T", "/p", "g", nil, "")
	d.worktreeCapable = true
	d.worktreeEnabled = true
	d.setFocus(forkFocusCarryState)
	if d.currentFocusName() != "carryState" {
		t.Fatalf("precondition: focus should be carryState, got %s", d.currentFocusName())
	}
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !d.IsWithStateEnabled() {
		t.Error("Enter on carryState should enable with-state")
	}
}

// F7/F10: Enter on a focused include-gitignored checkbox toggles it.
func TestForkDialog_Enter_TogglesFocusedGitignored(t *testing.T) {
	d := NewForkDialog()
	d.SetSize(80, 40)
	d.Show("T", "/p", "g", nil, "")
	d.worktreeCapable = true
	d.worktreeEnabled = true
	d.ToggleWithState()
	d.setFocus(forkFocusGitignored)
	if d.currentFocusName() != "gitignored" {
		t.Fatalf("precondition: focus should be gitignored, got %s", d.currentFocusName())
	}
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !d.IsWithStateAndGitignoredEnabled() {
		t.Error("Enter on gitignored should enable gitignored")
	}
}

// F7/F10: Enter on the name field still submits and does not toggle with-state.
func TestForkDialog_Enter_OnNameStillSubmits(t *testing.T) {
	d := NewForkDialog()
	d.SetSize(80, 40)
	d.Show("T", "/p", "g", nil, "")
	d.worktreeCapable = true
	d.worktreeEnabled = true
	d.setFocus(forkFocusName)

	updated, cmd := d.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if updated == nil {
		t.Fatal("Enter on name must return a non-nil model (signal completion)")
	}
	if cmd != nil {
		t.Error("Enter on name should signal completion with a nil cmd, as before")
	}
	if updated.IsWithStateEnabled() {
		t.Error("Enter on name must not toggle with-state")
	}
	if !updated.IsVisible() {
		t.Error("Enter on name must not hide the dialog (submit is handled by the caller)")
	}
}
