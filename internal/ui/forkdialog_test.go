package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	tea "github.com/charmbracelet/bubbletea"
)

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

	d := NewForkDialog()
	d.Show("Original Session", "/path/to/project", "group/path", nil, "")

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

func TestForkDialog_WithState_DefaultsFalseAfterShow(t *testing.T) {
	d := NewForkDialog()
	d.Show("Test", "/path", "group", nil, "")
	if d.IsWithStateEnabled() {
		t.Error("IsWithStateEnabled should default false after Show")
	}
	if d.IsWithStateAndGitignoredEnabled() {
		t.Error("IsWithStateAndGitignoredEnabled should default false after Show")
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
