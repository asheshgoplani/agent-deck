package ui

import (
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestNewNewDialog(t *testing.T) {
	d := NewNewDialog()

	if d == nil {
		t.Fatal("NewNewDialog returned nil")
	}
	if d.IsVisible() {
		t.Error("Dialog should not be visible by default")
	}
	if len(d.presetCommands) == 0 {
		t.Error("presetCommands should not be empty")
	}
}

func TestDialogVisibility(t *testing.T) {
	d := NewNewDialog()

	d.Show()
	if !d.IsVisible() {
		t.Error("Dialog should be visible after Show()")
	}

	d.Hide()
	if d.IsVisible() {
		t.Error("Dialog should not be visible after Hide()")
	}
}

func TestDialogSetSize(t *testing.T) {
	d := NewNewDialog()
	d.SetSize(100, 50)

	if d.width != 100 {
		t.Errorf("Width = %d, want 100", d.width)
	}
	if d.height != 50 {
		t.Errorf("Height = %d, want 50", d.height)
	}
}

func TestDialogPresetCommands(t *testing.T) {
	d := NewNewDialog()

	// Should have shell (empty), claude, gemini, opencode, codex
	expectedCommands := []string{"", "claude", "gemini", "opencode", "codex"}

	if len(d.presetCommands) != len(expectedCommands) {
		t.Errorf("Expected %d preset commands, got %d", len(expectedCommands), len(d.presetCommands))
	}

	for i, cmd := range expectedCommands {
		if d.presetCommands[i] != cmd {
			t.Errorf("presetCommands[%d] = %s, want %s", i, d.presetCommands[i], cmd)
		}
	}
}

func TestDialogGetValues(t *testing.T) {
	d := NewNewDialog()
	d.nameInput.SetValue("my-session")
	d.pathInput.SetValue("/tmp/project")
	d.commandCursor = 1 // claude

	name, path, command := d.GetValues()

	if name != "my-session" {
		t.Errorf("name = %s, want my-session", name)
	}
	if path != "/tmp/project" {
		t.Errorf("path = %s, want /tmp/project", path)
	}
	if command != "claude" {
		t.Errorf("command = %s, want claude", command)
	}
}

func TestDialogExpandTilde(t *testing.T) {
	d := NewNewDialog()
	d.nameInput.SetValue("test")
	d.pathInput.SetValue("~/projects")

	_, path, _ := d.GetValues()

	home, _ := os.UserHomeDir()
	if !strings.HasPrefix(path, home) {
		t.Errorf("path should expand ~ to home directory, got %s", path)
	}
}

func TestDialogView(t *testing.T) {
	d := NewNewDialog()

	// Not visible - should return empty
	view := d.View()
	if view != "" {
		t.Error("View should be empty when not visible")
	}

	// Visible - should return content
	d.SetSize(80, 24)
	d.Show()
	view = d.View()
	if view == "" {
		t.Error("View should not be empty when visible")
	}
	if !strings.Contains(view, "New Session") {
		t.Error("View should contain 'New Session' title")
	}
}

func TestNewDialog_SetPathSuggestions(t *testing.T) {
	d := NewNewDialog()

	paths := []string{
		"/Users/test/project1",
		"/Users/test/project2",
		"/Users/test/other",
	}

	d.SetPathSuggestions(paths)

	if len(d.pathSuggestions) != 3 {
		t.Errorf("expected 3 suggestions, got %d", len(d.pathSuggestions))
	}

	// Verify suggestions are set on textinput
	available := d.pathInput.AvailableSuggestions()
	if len(available) != 3 {
		t.Errorf("expected 3 available suggestions on pathInput, got %d", len(available))
	}
}

func TestNewDialog_ShowSuggestionsEnabled(t *testing.T) {
	d := NewNewDialog()

	// ShowSuggestions should be enabled by default
	if !d.pathInput.ShowSuggestions {
		t.Error("expected ShowSuggestions to be true on pathInput")
	}
}

func TestNewDialog_SuggestionFiltering(t *testing.T) {
	d := NewNewDialog()

	paths := []string{
		"/Users/test/project-alpha",
		"/Users/test/project-beta",
		"/Users/test/other-thing",
	}

	d.SetPathSuggestions(paths)

	// Verify suggestions are available
	available := d.pathInput.AvailableSuggestions()
	if len(available) != 3 {
		t.Errorf("expected 3 available suggestions, got %d", len(available))
	}

	// Verify specific suggestions are in the list
	hasProjectAlpha := false
	hasProjectBeta := false
	hasOtherThing := false
	for _, s := range available {
		if s == "/Users/test/project-alpha" {
			hasProjectAlpha = true
		}
		if s == "/Users/test/project-beta" {
			hasProjectBeta = true
		}
		if s == "/Users/test/other-thing" {
			hasOtherThing = true
		}
	}

	if !hasProjectAlpha || !hasProjectBeta || !hasOtherThing {
		t.Error("not all expected suggestions are available")
	}
}

func TestNewDialog_MalformedPathFix(t *testing.T) {
	home, _ := os.UserHomeDir()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "normal tilde path",
			input:    "~/projects/myapp",
			expected: home + "/projects/myapp",
		},
		{
			name:     "malformed path with cwd prefix",
			input:    "/Users/someone/claude-deck~/projects/myapp",
			expected: home + "/projects/myapp",
		},
		{
			name:     "already expanded path",
			input:    "/Users/ashesh/projects/myapp",
			expected: "/Users/ashesh/projects/myapp",
		},
		{
			name:     "just tilde",
			input:    "~",
			expected: home,
		},
		{
			name:     "malformed path with different prefix",
			input:    "/some/random/path~/other/path",
			expected: home + "/other/path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := NewNewDialog()
			d.pathInput.SetValue(tt.input)

			_, path, _ := d.GetValues()

			if path != tt.expected {
				t.Errorf("GetValues() path = %q, want %q", path, tt.expected)
			}
		})
	}
}

// TestNewDialog_TabDoesNotOverwriteCustomPath tests Issue #22:
// When user enters a new folder path and presses Tab to move to agent selection,
// the custom path should NOT be overwritten by a suggestion.
func TestNewDialog_TabDoesNotOverwriteCustomPath(t *testing.T) {
	d := NewNewDialog()
	d.Show() // Dialog must be visible for Update to process keys

	// Set up suggestions (simulating previously used paths)
	suggestions := []string{
		"/Users/test/old-project-1",
		"/Users/test/old-project-2",
	}
	d.SetPathSuggestions(suggestions)

	// User is on path field (focusIndex 1)
	d.focusIndex = 1
	d.updateFocus()

	// User types a completely NEW path that doesn't match any suggestion
	customPath := "/Users/test/brand-new-project"
	d.pathInput.SetValue(customPath)

	// User presses Tab to move to command selection
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyTab})

	// The custom path should be PRESERVED, not overwritten
	_, path, _ := d.GetValues()

	if path != customPath {
		t.Errorf("Tab overwrote custom path!\nGot: %q\nWant: %q\nThis is the bug from Issue #22", path, customPath)
	}

	// Focus should have moved to command field
	if d.focusIndex != 2 {
		t.Errorf("focusIndex = %d, want 2 (command field)", d.focusIndex)
	}
}

// TestNewDialog_TabAppliesSuggestionWhenNavigated tests that Tab DOES apply
// the suggestion when the user explicitly navigated to one using Ctrl+N/P.
func TestNewDialog_TabAppliesSuggestionWhenNavigated(t *testing.T) {
	d := NewNewDialog()
	d.Show()

	suggestions := []string{
		"/Users/test/project-1",
		"/Users/test/project-2",
	}
	d.SetPathSuggestions(suggestions)

	// User is on path field
	d.focusIndex = 1
	d.updateFocus()

	// User types something, then navigates to suggestion with Ctrl+N
	d.pathInput.SetValue("/some/partial")
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyCtrlN})

	// Now Tab should apply the suggestion
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyTab})

	_, path, _ := d.GetValues()

	// Should be the second suggestion (Ctrl+N moved from 0 to 1)
	if path != "/Users/test/project-2" {
		t.Errorf("Tab should apply suggestion after Ctrl+N navigation\nGot: %q\nWant: %q", path, "/Users/test/project-2")
	}
}

// TestNewDialog_TypingResetsSuggestionNavigation tests that typing after
// navigating suggestions resets the navigation state.
func TestNewDialog_TypingResetsSuggestionNavigation(t *testing.T) {
	d := NewNewDialog()
	d.Show()

	suggestions := []string{
		"/Users/test/project-1",
		"/Users/test/project-2",
	}
	d.SetPathSuggestions(suggestions)

	d.focusIndex = 1
	d.updateFocus()

	// User navigates to a suggestion
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyCtrlN})

	// Verify navigation flag is set
	if !d.suggestionNavigated {
		t.Error("suggestionNavigated should be true after Ctrl+N")
	}

	// User then types something new - simulate by sending a key
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})

	// Flag should be reset
	if d.suggestionNavigated {
		t.Error("suggestionNavigated should be false after typing")
	}

	// Set a custom path and press Tab
	d.pathInput.SetValue("/my/new/path")
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyTab})

	_, path, _ := d.GetValues()

	if path != "/my/new/path" {
		t.Errorf("Typing should reset suggestion navigation\nGot: %q\nWant: %q", path, "/my/new/path")
	}
}

// ===== Worktree Support Tests =====

func TestNewDialog_WorktreeToggle(t *testing.T) {
	dialog := NewNewDialog()
	if dialog.worktreeEnabled {
		t.Error("Worktree should be disabled by default")
	}
	dialog.ToggleWorktree()
	if !dialog.worktreeEnabled {
		t.Error("Worktree should be enabled after toggle")
	}
	dialog.ToggleWorktree()
	if dialog.worktreeEnabled {
		t.Error("Worktree should be disabled after second toggle")
	}
}

func TestNewDialog_IsWorktreeEnabled(t *testing.T) {
	dialog := NewNewDialog()
	if dialog.IsWorktreeEnabled() {
		t.Error("IsWorktreeEnabled should return false by default")
	}
	dialog.worktreeEnabled = true
	if !dialog.IsWorktreeEnabled() {
		t.Error("IsWorktreeEnabled should return true when enabled")
	}
}

func TestNewDialog_GetValuesWithWorktree(t *testing.T) {
	dialog := NewNewDialog()
	dialog.worktreeEnabled = true
	dialog.branchInput.SetValue("feature/test")
	dialog.nameInput.SetValue("test-session")
	dialog.pathInput.SetValue("/tmp/project")

	name, path, command, branch, enabled := dialog.GetValuesWithWorktree()

	if !enabled {
		t.Error("worktreeEnabled should be true")
	}
	if branch != "feature/test" {
		t.Errorf("Branch: got %q, want %q", branch, "feature/test")
	}
	if name != "test-session" {
		t.Errorf("Name: got %q, want %q", name, "test-session")
	}
	if path != "/tmp/project" {
		t.Errorf("Path: got %q, want %q", path, "/tmp/project")
	}
	// command should be empty or shell when commandCursor is 0
	_ = command
}

func TestNewDialog_GetValuesWithWorktree_Disabled(t *testing.T) {
	dialog := NewNewDialog()
	dialog.worktreeEnabled = false
	dialog.branchInput.SetValue("feature/test")

	_, _, _, branch, enabled := dialog.GetValuesWithWorktree()

	if enabled {
		t.Error("worktreeEnabled should be false")
	}
	// Branch value is still returned even when disabled
	if branch != "feature/test" {
		t.Errorf("Branch: got %q, want %q", branch, "feature/test")
	}
}

func TestNewDialog_Validate_WorktreeEnabled_EmptyBranch(t *testing.T) {
	dialog := NewNewDialog()
	dialog.nameInput.SetValue("test-session")
	dialog.pathInput.SetValue("/tmp/project")
	dialog.worktreeEnabled = true
	dialog.branchInput.SetValue("")

	err := dialog.Validate()
	if err == "" {
		t.Error("Validation should fail when worktree enabled but branch is empty")
	}
	if err != "Branch name required for worktree" {
		t.Errorf("Unexpected error message: %q", err)
	}
}

func TestNewDialog_Validate_WorktreeEnabled_InvalidBranch(t *testing.T) {
	dialog := NewNewDialog()
	dialog.nameInput.SetValue("test-session")
	dialog.pathInput.SetValue("/tmp/project")
	dialog.worktreeEnabled = true
	dialog.branchInput.SetValue("feature..test") // Invalid: contains ..

	err := dialog.Validate()
	if err == "" {
		t.Error("Validation should fail for invalid branch name")
	}
	if err != "branch name cannot contain '..'" {
		t.Errorf("Unexpected error message: %q", err)
	}
}

func TestNewDialog_Validate_WorktreeEnabled_ValidBranch(t *testing.T) {
	dialog := NewNewDialog()
	dialog.nameInput.SetValue("test-session")
	dialog.pathInput.SetValue("/tmp/project")
	dialog.worktreeEnabled = true
	dialog.branchInput.SetValue("feature/test-branch")

	err := dialog.Validate()
	if err != "" {
		t.Errorf("Validation should pass for valid branch, got: %q", err)
	}
}

func TestNewDialog_Validate_WorktreeDisabled_IgnoresBranch(t *testing.T) {
	dialog := NewNewDialog()
	dialog.nameInput.SetValue("test-session")
	dialog.pathInput.SetValue("/tmp/project")
	dialog.worktreeEnabled = false
	dialog.branchInput.SetValue("") // Empty branch, but worktree disabled

	err := dialog.Validate()
	if err != "" {
		t.Errorf("Validation should pass when worktree disabled, got: %q", err)
	}
}

func TestNewDialog_ShowInGroup_ResetsWorktree(t *testing.T) {
	dialog := NewNewDialog()
	dialog.worktreeEnabled = true
	dialog.branchInput.SetValue("feature/old-branch")

	dialog.ShowInGroup("projects", "Projects", "")

	if dialog.worktreeEnabled {
		t.Error("worktreeEnabled should be reset to false on ShowInGroup")
	}
	if dialog.branchInput.Value() != "" {
		t.Errorf("branchInput should be reset, got: %q", dialog.branchInput.Value())
	}
}

func TestNewDialog_ShowInGroup_SetsDefaultPath(t *testing.T) {
	dialog := NewNewDialog()

	dialog.ShowInGroup("projects", "Projects", "/test/default/path")

	// Verify path input is set to the default path
	if dialog.pathInput.Value() != "/test/default/path" {
		t.Errorf("pathInput should be set to default path, got: %q", dialog.pathInput.Value())
	}
}

func TestNewDialog_ShowInGroup_EmptyDefaultPath(t *testing.T) {
	dialog := NewNewDialog()

	dialog.ShowInGroup("projects", "Projects", "")

	// With empty default path, it should fall back to current working directory
	// We can't test the exact value, but we can verify it's not empty
	// (assuming we're not in a system temp directory)
	value := dialog.pathInput.Value()
	if value == "" {
		t.Error("pathInput should not be empty when defaultPath is empty (should use cwd)")
	}
}

func TestNewDialog_BranchInputInitialized(t *testing.T) {
	dialog := NewNewDialog()

	// Verify branch input is properly initialized
	if dialog.branchInput.Placeholder != "feature/branch-name" {
		t.Errorf("branchInput placeholder: got %q, want %q",
			dialog.branchInput.Placeholder, "feature/branch-name")
	}
}

func TestNewDialog_WorktreeToggle_ViaKeyPress(t *testing.T) {
	dialog := NewNewDialog()
	dialog.Show()
	dialog.focusIndex = 2 // Command field

	// Press 'w' to toggle worktree
	dialog, _ = dialog.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})

	if !dialog.worktreeEnabled {
		t.Error("Worktree should be enabled after pressing 'w' on command field")
	}

	// Focus should move to branch field
	if dialog.focusIndex != 3 {
		t.Errorf("Focus should move to branch field (3), got %d", dialog.focusIndex)
	}

	// Press 'w' again to disable (need to be on command field)
	dialog.focusIndex = 2
	dialog, _ = dialog.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})

	if dialog.worktreeEnabled {
		t.Error("Worktree should be disabled after pressing 'w' again")
	}
}

func TestNewDialog_TabNavigationWithWorktree(t *testing.T) {
	dialog := NewNewDialog()
	dialog.Show()
	dialog.focusIndex = 0
	dialog.worktreeEnabled = true

	// Tab through all fields: 0 -> 1 -> 2 -> 3 -> 0
	dialog, _ = dialog.Update(tea.KeyMsg{Type: tea.KeyTab})
	if dialog.focusIndex != 1 {
		t.Errorf("After first Tab, focusIndex = %d, want 1", dialog.focusIndex)
	}

	dialog, _ = dialog.Update(tea.KeyMsg{Type: tea.KeyTab})
	if dialog.focusIndex != 2 {
		t.Errorf("After second Tab, focusIndex = %d, want 2", dialog.focusIndex)
	}

	dialog, _ = dialog.Update(tea.KeyMsg{Type: tea.KeyTab})
	if dialog.focusIndex != 3 {
		t.Errorf("After third Tab, focusIndex = %d, want 3 (branch field)", dialog.focusIndex)
	}

	dialog, _ = dialog.Update(tea.KeyMsg{Type: tea.KeyTab})
	if dialog.focusIndex != 0 {
		t.Errorf("After fourth Tab, focusIndex = %d, want 0 (wrap around)", dialog.focusIndex)
	}
}

func TestNewDialog_TabNavigationWithoutWorktree(t *testing.T) {
	dialog := NewNewDialog()
	dialog.Show()
	dialog.focusIndex = 0
	dialog.worktreeEnabled = false

	// Tab through fields: 0 -> 1 -> 2 -> 0 (no branch field)
	dialog, _ = dialog.Update(tea.KeyMsg{Type: tea.KeyTab})
	if dialog.focusIndex != 1 {
		t.Errorf("After first Tab, focusIndex = %d, want 1", dialog.focusIndex)
	}

	dialog, _ = dialog.Update(tea.KeyMsg{Type: tea.KeyTab})
	if dialog.focusIndex != 2 {
		t.Errorf("After second Tab, focusIndex = %d, want 2", dialog.focusIndex)
	}

	dialog, _ = dialog.Update(tea.KeyMsg{Type: tea.KeyTab})
	if dialog.focusIndex != 0 {
		t.Errorf("After third Tab, focusIndex = %d, want 0 (wrap around, skip branch)", dialog.focusIndex)
	}
}

func TestNewDialog_View_ShowsWorktreeCheckbox(t *testing.T) {
	dialog := NewNewDialog()
	dialog.SetSize(80, 40)
	dialog.Show()
	dialog.focusIndex = 2 // Command field

	view := dialog.View()

	// Should show worktree checkbox
	if !strings.Contains(view, "Create in worktree") {
		t.Error("View should contain 'Create in worktree' checkbox")
	}

	// Should show hint when on command field
	if !strings.Contains(view, "press w") {
		t.Error("View should contain 'press w' hint when on command field")
	}
}

func TestNewDialog_View_ShowsBranchInputWhenEnabled(t *testing.T) {
	dialog := NewNewDialog()
	dialog.SetSize(80, 40)
	dialog.Show()
	dialog.worktreeEnabled = true

	view := dialog.View()

	// Should show branch input
	if !strings.Contains(view, "Branch:") {
		t.Error("View should contain 'Branch:' label when worktree enabled")
	}

	// Checkbox should be checked
	if !strings.Contains(view, "[x]") {
		t.Error("View should show checked checkbox [x] when worktree enabled")
	}
}

func TestNewDialog_View_HidesBranchInputWhenDisabled(t *testing.T) {
	dialog := NewNewDialog()
	dialog.SetSize(80, 40)
	dialog.Show()
	dialog.worktreeEnabled = false

	view := dialog.View()

	// Should NOT show branch input label
	if strings.Contains(view, "Branch:") {
		t.Error("View should NOT contain 'Branch:' label when worktree disabled")
	}

	// Checkbox should be unchecked
	if !strings.Contains(view, "[ ]") {
		t.Error("View should show unchecked checkbox [ ] when worktree disabled")
	}
}

// ===== CharLimit & Inline Error Tests (Issue #93) =====

func TestNewDialog_CharLimitMatchesMaxNameLength(t *testing.T) {
	d := NewNewDialog()
	if d.nameInput.CharLimit != MaxNameLength {
		t.Errorf("nameInput.CharLimit = %d, want %d (MaxNameLength)", d.nameInput.CharLimit, MaxNameLength)
	}
}

func TestNewDialog_CharLimitTruncatesLongNames(t *testing.T) {
	d := NewNewDialog()
	d.pathInput.SetValue("/tmp/project")
	// Try to set a name longer than MaxNameLength via textinput
	longName := strings.Repeat("a", MaxNameLength+10)
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

func TestNewDialog_Validate_NameAtMaxLength(t *testing.T) {
	d := NewNewDialog()
	d.pathInput.SetValue("/tmp/project")
	exactName := strings.Repeat("a", MaxNameLength)
	d.nameInput.SetValue(exactName)

	err := d.Validate()
	if err != "" {
		t.Errorf("Validate() should accept name at exactly MaxNameLength, got: %q", err)
	}
}

func TestNewDialog_SetError_ShowsInView(t *testing.T) {
	d := NewNewDialog()
	d.SetSize(80, 40)
	d.Show()

	d.SetError("Something went wrong")
	view := d.View()

	if !strings.Contains(view, "Something went wrong") {
		t.Error("View should display the inline error message")
	}
}

func TestNewDialog_ClearError_HidesFromView(t *testing.T) {
	d := NewNewDialog()
	d.SetSize(80, 40)
	d.Show()

	d.SetError("Something went wrong")
	d.ClearError()
	view := d.View()

	if strings.Contains(view, "Something went wrong") {
		t.Error("View should not display the error after ClearError()")
	}
}

func TestNewDialog_ShowInGroup_ClearsError(t *testing.T) {
	d := NewNewDialog()
	d.SetError("Previous error")
	d.ShowInGroup("group", "Group", "")

	if d.validationErr != "" {
		t.Error("ShowInGroup should clear validationErr")
	}
}

// ===== Worktree Branch Auto-Matching Tests =====

func TestNewDialog_ToggleWorktree_AutoPopulatesBranch(t *testing.T) {
	d := NewNewDialog()
	d.nameInput.SetValue("amber-falcon")

	// Toggling worktree ON should auto-populate branch from session name
	d.ToggleWorktree()

	if !d.worktreeEnabled {
		t.Fatal("worktreeEnabled should be true after toggle")
	}
	if d.branchInput.Value() != "feature/amber-falcon" {
		t.Errorf("branch = %q, want %q", d.branchInput.Value(), "feature/amber-falcon")
	}
	if !d.branchAutoSet {
		t.Error("branchAutoSet should be true after auto-population")
	}
}

func TestNewDialog_ToggleWorktree_EmptyName_NoBranch(t *testing.T) {
	d := NewNewDialog()
	// Name is empty

	d.ToggleWorktree()

	if d.branchInput.Value() != "" {
		t.Errorf("branch should be empty when name is empty, got %q", d.branchInput.Value())
	}
}

func TestNewDialog_ShowInGroup_ResetsBranchAutoSet(t *testing.T) {
	d := NewNewDialog()
	d.branchAutoSet = true

	d.ShowInGroup("projects", "Projects", "")

	if d.branchAutoSet {
		t.Error("branchAutoSet should be reset to false on ShowInGroup")
	}
}
