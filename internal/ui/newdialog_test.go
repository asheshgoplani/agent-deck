package ui

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
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

func TestNewDialog_SetSize_syncsPathInputWidth(t *testing.T) {
	d := NewNewDialog()

	d.SetSize(120, 40)
	// Preferred outer width 84 → text fields 84 − 12 = 72.
	if got := d.pathInput.Width; got != 72 {
		t.Fatalf("wide terminal: pathInput.Width = %d, want 72", got)
	}

	d.SetSize(55, 40)
	// Outer shrinks to terminal−10 (45), above min 44 → inputs 45 − 12 = 33.
	if got := d.pathInput.Width; got != 33 {
		t.Fatalf("narrow terminal: pathInput.Width = %d, want 33", got)
	}
}

func TestDialogPresetCommands(t *testing.T) {
	d := NewNewDialog()

	// Should have shell (empty), claude, gemini, opencode, codex, pi, copilot
	expectedCommands := []string{"", "claude", "gemini", "opencode", "codex", "pi", "copilot"}

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
		t.Errorf("expected 3 pathSuggestions, got %d", len(d.pathSuggestions))
	}

	// Verify full set is stored in allPathSuggestions
	if len(d.allPathSuggestions) != 3 {
		t.Errorf("expected 3 allPathSuggestions, got %d", len(d.allPathSuggestions))
	}
}

func TestNewDialog_ShowSuggestionsDisabled(t *testing.T) {
	d := NewNewDialog()

	// ShowSuggestions should be disabled — we use our own dropdown with filtering
	if d.pathInput.ShowSuggestions {
		t.Error("expected ShowSuggestions to be false on pathInput (we use custom dropdown)")
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

	// Type "project" to filter
	d.pathInput.SetValue("project")
	d.filterPathSuggestions()

	if len(d.pathSuggestions) != 2 {
		t.Errorf("expected 2 filtered suggestions for 'project', got %d", len(d.pathSuggestions))
	}

	// Verify the correct paths are in the filtered list
	for _, s := range d.pathSuggestions {
		if !strings.Contains(s, "project") {
			t.Errorf("filtered suggestion %q should contain 'project'", s)
		}
	}

	// Full set should remain unchanged
	if len(d.allPathSuggestions) != 3 {
		t.Errorf("allPathSuggestions should still be 3, got %d", len(d.allPathSuggestions))
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
	d.focusIndex = 2
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
	if d.focusIndex != 3 {
		t.Errorf("focusIndex = %d, want 3 (command field)", d.focusIndex)
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
	d.focusIndex = 2
	d.updateFocus()

	// User types something, then navigates to suggestion with Ctrl+N.
	// Cursor convention: 0 = "Type custom path…" (synthetic), 1 = first
	// real suggestion, 2 = second. Two presses lands on the second.
	d.pathInput.SetValue("/some/partial")
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyCtrlN})

	// Now Tab should apply the selected suggestion
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyTab})

	_, path, _ := d.GetValues()

	// Should be the second suggestion (cursor 0 → 1 → 2 = suggestions[1])
	if path != "/Users/test/project-2" {
		t.Errorf(
			"Tab should apply suggestion after Ctrl+N navigation\nGot: %q\nWant: %q",
			path,
			"/Users/test/project-2",
		)
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

	d.focusIndex = 2
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

func TestNewDialog_PreviewRecentSession_ShellCommand(t *testing.T) {
	d := NewNewDialog()
	d.Show()

	d.commandCursor = 2 // non-shell
	d.commandInput.SetValue("old-command")

	rs := &statedb.RecentSessionRow{
		Title:       "recent-shell",
		ProjectPath: "/tmp/recent-shell",
		Tool:        "shell",
		Command:     "uv run agent",
	}
	d.previewRecentSession(rs)

	if d.commandCursor != 0 {
		t.Fatalf("commandCursor = %d, want 0 (shell)", d.commandCursor)
	}
	if got := d.commandInput.Value(); got != "uv run agent" {
		t.Fatalf("commandInput = %q, want %q", got, "uv run agent")
	}
	if d.nameInput.Value() != "recent-shell" {
		t.Fatalf("nameInput = %q, want %q", d.nameInput.Value(), "recent-shell")
	}
}

func TestNewDialog_RestoreSnapshot_RestoresToolOptionsAndCommandInput(t *testing.T) {
	d := NewNewDialog()
	d.Show()

	originalClaude := &session.ClaudeOptions{
		SessionMode:          "resume",
		ResumeSessionID:      "abc123",
		SkipPermissions:      true,
		AllowSkipPermissions: false,
		UseChrome:            true,
		UseTeammateMode:      true,
	}
	d.nameInput.SetValue("orig-name")
	d.pathInput.SetValue("/tmp/orig")
	d.commandCursor = 0
	d.commandInput.SetValue("echo original")
	d.claudeOptions.SetFromOptions(originalClaude)
	d.geminiOptions.SetDefaults(true)
	d.codexOptions.SetDefaults(true)

	snapshot := d.saveSnapshot()

	// Mutate state to ensure restore actually rewinds everything.
	d.nameInput.SetValue("mutated-name")
	d.pathInput.SetValue("/tmp/mutated")
	d.commandCursor = 1
	d.commandInput.SetValue("echo mutated")
	d.claudeOptions.SetFromOptions(&session.ClaudeOptions{SessionMode: "new"})
	d.geminiOptions.SetDefaults(false)
	d.codexOptions.SetDefaults(false)

	d.restoreSnapshot(snapshot)

	if got := d.nameInput.Value(); got != "orig-name" {
		t.Fatalf("nameInput = %q, want %q", got, "orig-name")
	}
	if got := d.pathInput.Value(); got != "/tmp/orig" {
		t.Fatalf("pathInput = %q, want %q", got, "/tmp/orig")
	}
	if d.commandCursor != 0 {
		t.Fatalf("commandCursor = %d, want 0", d.commandCursor)
	}
	if got := d.commandInput.Value(); got != "echo original" {
		t.Fatalf("commandInput = %q, want %q", got, "echo original")
	}

	restoredClaude := d.claudeOptions.GetOptions()
	if restoredClaude == nil {
		t.Fatal("restored Claude options are nil")
	}
	if restoredClaude.SessionMode != "resume" || restoredClaude.ResumeSessionID != "abc123" {
		t.Fatalf("restored Claude session mode/id = %q/%q, want resume/abc123",
			restoredClaude.SessionMode, restoredClaude.ResumeSessionID)
	}
	if !restoredClaude.SkipPermissions || !restoredClaude.UseChrome || !restoredClaude.UseTeammateMode {
		t.Fatalf("restored Claude toggles incorrect: %+v", restoredClaude)
	}
	if !d.geminiOptions.GetYoloMode() {
		t.Fatal("gemini yolo mode was not restored")
	}
	if !d.codexOptions.GetYoloMode() {
		t.Fatal("codex yolo mode was not restored")
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

	dialog.ShowInGroup("projects", "Projects", "", nil, "")

	if dialog.worktreeEnabled {
		t.Error("worktreeEnabled should be reset to false on ShowInGroup")
	}
	if dialog.branchInput.Value() != "" {
		t.Errorf("branchInput should be reset, got: %q", dialog.branchInput.Value())
	}
}

func TestNewDialog_ShowInGroup_ResetsMultiRepo(t *testing.T) {
	dialog := NewNewDialog()
	dialog.multiRepoEnabled = true
	dialog.multiRepoPaths = []string{"/path/a", "/path/b"}
	dialog.multiRepoPathCursor = 1
	dialog.multiRepoEditing = true

	dialog.ShowInGroup("projects", "Projects", "", nil, "")

	if dialog.multiRepoEnabled {
		t.Error("multiRepoEnabled should be reset to false on ShowInGroup")
	}
	if dialog.multiRepoPaths != nil {
		t.Errorf("multiRepoPaths should be nil, got: %v", dialog.multiRepoPaths)
	}
	if dialog.multiRepoPathCursor != 0 {
		t.Errorf("multiRepoPathCursor should be 0, got: %d", dialog.multiRepoPathCursor)
	}
	if dialog.multiRepoEditing {
		t.Error("multiRepoEditing should be false on ShowInGroup")
	}
}

func TestNewDialog_ShowInGroup_UsesConfiguredWorktreeDefault(t *testing.T) {
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

	dialog := NewNewDialog()
	dialog.ShowInGroup("projects", "Projects", "", nil, "")

	if !dialog.worktreeEnabled {
		t.Error("worktreeEnabled should default to true from config on ShowInGroup")
	}
}

func TestNewDialog_ShowInGroup_SetsDefaultPath(t *testing.T) {
	dialog := NewNewDialog()

	dialog.ShowInGroup("projects", "Projects", "/test/default/path", nil, "")

	// Verify path input is set to the default path
	if dialog.pathInput.Value() != "/test/default/path" {
		t.Errorf("pathInput should be set to default path, got: %q", dialog.pathInput.Value())
	}
}

func TestNewDialog_ShowInGroup_EmptyDefaultPath(t *testing.T) {
	dialog := NewNewDialog()

	dialog.ShowInGroup("projects", "Projects", "", nil, "")

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
	dialog.sandboxEnabled = false
	dialog.inheritedSettings = nil
	dialog.commandCursor = 1 // preset command (not custom input)
	dialog.rebuildFocusTargets()
	dialog.focusIndex = 3 // Command field

	// Press 'w' to toggle worktree.
	dialog, _ = dialog.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})

	if !dialog.worktreeEnabled {
		t.Error("Worktree should be enabled after pressing 'w' on command field")
	}

	// Focus should move to branch field.
	if dialog.focusIndex != dialog.indexOf(focusBranch) {
		t.Errorf("Focus should move to branch field (%d), got %d", dialog.indexOf(focusBranch), dialog.focusIndex)
	}

	// Press 'w' again to disable (need to be on command field).
	dialog.focusIndex = 3
	dialog, _ = dialog.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})

	if dialog.worktreeEnabled {
		t.Error("Worktree should be disabled after pressing 'w' again")
	}
}

func TestNewDialog_ShortcutsBlockedDuringTextInput(t *testing.T) {
	dialog := NewNewDialog()
	dialog.Show()
	dialog.sandboxEnabled = false
	dialog.inheritedSettings = nil
	dialog.commandCursor = 0 // custom command input (text field active)
	dialog.rebuildFocusTargets()

	// Navigate to command field.
	cmdIdx := dialog.indexOf(focusCommand)
	dialog.focusIndex = cmdIdx
	dialog.updateFocus()

	// Press 's' — should NOT toggle sandbox when custom command input is focused.
	dialog, _ = dialog.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if dialog.sandboxEnabled {
		t.Error("Pressing 's' on custom command input should type, not toggle sandbox")
	}

	// Press 'w' — should NOT toggle worktree.
	dialog, _ = dialog.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})
	if dialog.worktreeEnabled {
		t.Error("Pressing 'w' on custom command input should type, not toggle worktree")
	}

	// Press 'm' — should NOT toggle multi-repo.
	dialog, _ = dialog.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	if dialog.multiRepoEnabled {
		t.Error("Pressing 'm' on custom command input should type, not toggle multi-repo")
	}

	// Also verify shortcuts don't fire on name field.
	dialog.focusIndex = 0 // focusName
	dialog.updateFocus()
	dialog, _ = dialog.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if dialog.sandboxEnabled {
		t.Error("Pressing 's' on name input should not toggle sandbox")
	}
}

func TestNewDialog_TabNavigationWithWorktree(t *testing.T) {
	dialog := NewNewDialog()
	dialog.Show()
	dialog.sandboxEnabled = false
	dialog.inheritedSettings = nil
	dialog.focusIndex = 0
	dialog.worktreeEnabled = true
	dialog.rebuildFocusTargets()

	branchIdx := dialog.indexOf(focusBranch)
	maxIdx := len(dialog.focusTargets) - 1

	// Tab through: 0 -> 1 -> 2 -> 3(worktree) -> 4(sandbox) -> branchIdx(branch) -> 0.
	for i := 1; i <= maxIdx; i++ {
		dialog, _ = dialog.Update(tea.KeyMsg{Type: tea.KeyTab})
		want := i
		if i == branchIdx {
			want = branchIdx
		}
		if dialog.focusIndex != want {
			t.Errorf("After Tab %d, focusIndex = %d, want %d", i, dialog.focusIndex, want)
		}
	}

	// One more Tab should wrap to 0.
	dialog, _ = dialog.Update(tea.KeyMsg{Type: tea.KeyTab})
	if dialog.focusIndex != 0 {
		t.Errorf("After final Tab, focusIndex = %d, want 0 (wrap around)", dialog.focusIndex)
	}
}

func TestNewDialog_TabNavigationWithoutWorktree(t *testing.T) {
	dialog := NewNewDialog()
	dialog.Show()
	dialog.sandboxEnabled = false
	dialog.inheritedSettings = nil
	dialog.focusIndex = 0
	dialog.worktreeEnabled = false
	dialog.rebuildFocusTargets()

	maxIdx := len(dialog.focusTargets) - 1

	// Tab through: 0 -> 1 -> 2 -> 3(worktree) -> 4(sandbox) -> 0.
	for i := 1; i <= maxIdx; i++ {
		dialog, _ = dialog.Update(tea.KeyMsg{Type: tea.KeyTab})
		if dialog.focusIndex != i {
			t.Errorf("After Tab %d, focusIndex = %d, want %d", i, dialog.focusIndex, i)
		}
	}

	// One more Tab should wrap to 0.
	dialog, _ = dialog.Update(tea.KeyMsg{Type: tea.KeyTab})
	if dialog.focusIndex != 0 {
		t.Errorf("After final Tab, focusIndex = %d, want 0 (wrap around)", dialog.focusIndex)
	}
}

func TestNewDialog_View_ShowsWorktreeCheckbox(t *testing.T) {
	dialog := NewNewDialog()
	dialog.SetSize(80, 40)
	dialog.Show()
	dialog.focusIndex = 3 // Command field

	view := dialog.View()

	// Should show worktree checkbox.
	if !strings.Contains(view, "Create in worktree") {
		t.Error("View should contain 'Create in worktree' checkbox")
	}

	// Should show shortcut hint when on command field.
	if !strings.Contains(view, "(w)") {
		t.Error("View should contain '(w)' hint when on command field")
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

// ===== Checkbox Focus Tests =====

func TestNewDialog_WorktreeCheckbox_SpaceToggle(t *testing.T) {
	dialog := NewNewDialog()
	dialog.Show()
	dialog.sandboxEnabled = false
	dialog.inheritedSettings = nil
	dialog.rebuildFocusTargets()
	dialog.focusIndex = 4 // Worktree checkbox

	// Space toggles worktree on.
	dialog, _ = dialog.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})

	if !dialog.worktreeEnabled {
		t.Error("Space on worktree checkbox should enable worktree")
	}

	// Focus should jump to branch field.
	if dialog.focusIndex != dialog.indexOf(focusBranch) {
		t.Errorf("Focus should move to branch field (%d), got %d", dialog.indexOf(focusBranch), dialog.focusIndex)
	}

	// Navigate back and space again to disable.
	dialog.focusIndex = 4
	dialog, _ = dialog.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})

	if dialog.worktreeEnabled {
		t.Error("Space on worktree checkbox should disable worktree")
	}
}

func TestNewDialog_SandboxCheckbox_SpaceToggle(t *testing.T) {
	dialog := NewNewDialog()
	dialog.Show()
	dialog.sandboxEnabled = false // Ensure known initial state.
	dialog.focusIndex = 5         // Sandbox checkbox

	// Space toggles sandbox on.
	dialog, _ = dialog.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})

	if !dialog.sandboxEnabled {
		t.Error("Space on sandbox checkbox should enable sandbox")
	}

	// Space again toggles off.
	dialog.focusIndex = 5
	dialog, _ = dialog.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})

	if dialog.sandboxEnabled {
		t.Error("Space on sandbox checkbox should disable sandbox")
	}
}

func TestNewDialog_CheckboxesFocusIndependently(t *testing.T) {
	dialog := NewNewDialog()
	dialog.SetSize(80, 40)
	dialog.Show()

	// Focus on worktree checkbox — only it should highlight.
	dialog.focusIndex = 4
	view := dialog.View()

	// Worktree line should have the focus indicator.
	if !strings.Contains(view, "Create in worktree") {
		t.Error("View should contain worktree checkbox")
	}

	// Focus on sandbox checkbox — only it should highlight.
	dialog.focusIndex = 5
	view = dialog.View()

	if !strings.Contains(view, "Run in Docker sandbox") {
		t.Error("View should contain sandbox checkbox")
	}
}

func TestNewDialog_ShowInGroup_ClearsError(t *testing.T) {
	d := NewNewDialog()
	d.SetError("Previous error")
	d.ShowInGroup("group", "Group", "", nil, "")

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

	d.ShowInGroup("projects", "Projects", "", nil, "")

	if d.branchAutoSet {
		t.Error("branchAutoSet should be reset to false on ShowInGroup")
	}
}

func TestNewDialog_ShowInGroup_DefaultWorktree_SetsBranchAutoSet(t *testing.T) {
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

	d := NewNewDialog()
	d.ShowInGroup("projects", "Projects", "", nil, "")

	if !d.worktreeEnabled {
		t.Fatal("worktreeEnabled should be true from config default")
	}
	if !d.branchAutoSet {
		t.Error("branchAutoSet should be true when worktree is enabled by config default")
	}
}

func TestNewDialog_ShowInGroup_DefaultWorktree_AutoPopulatesBranchFromName(t *testing.T) {
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

	d := NewNewDialog()
	d.ShowInGroup("projects", "Projects", "", nil, "")
	d.nameInput.SetValue("amber-falcon")

	// Simulate the name-change handler: it calls autoBranchFromName() only when branchAutoSet is true.
	if d.worktreeEnabled && d.branchAutoSet {
		d.autoBranchFromName()
	}

	if got := d.branchInput.Value(); got != "feature/amber-falcon" {
		t.Errorf("branch = %q, want %q; branch should auto-populate when worktree is default-enabled", got, "feature/amber-falcon")
	}
}

// ===== Soft-Select Tests =====

func TestNewDialog_SoftSelect_InitialState(t *testing.T) {
	d := NewNewDialog()
	d.SetSize(80, 40)
	d.Show()

	// After Show(), path is pre-filled and soft-selected
	if !d.pathSoftSelected {
		t.Error("pathSoftSelected should be true after Show()")
	}
	if d.pathInput.Value() == "" {
		t.Error("path should be pre-filled with CWD after Show()")
	}
}

func TestNewDialog_SoftSelect_TypeClearsField(t *testing.T) {
	d := NewNewDialog()
	d.SetSize(80, 40)
	d.Show()

	// Move focus to path field
	d.focusIndex = 2
	d.updateFocus()

	originalPath := d.pathInput.Value()
	if originalPath == "" {
		t.Fatal("path should be pre-filled")
	}
	if !d.pathSoftSelected {
		t.Fatal("pathSoftSelected should be true when focusing path with value")
	}

	// Type a character while soft-selected
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})

	// Field should have only the typed character (old value cleared)
	val := d.pathInput.Value()
	if val != "x" {
		t.Errorf("path = %q, want %q (old value should be replaced)", val, "x")
	}
	if d.pathSoftSelected {
		t.Error("pathSoftSelected should be false after typing")
	}
}

func TestNewDialog_SoftSelect_BackspaceClearsField(t *testing.T) {
	d := NewNewDialog()
	d.SetSize(80, 40)
	d.Show()

	d.focusIndex = 2
	d.updateFocus()

	if !d.pathSoftSelected {
		t.Fatal("pathSoftSelected should be true")
	}

	// Press backspace while soft-selected
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyBackspace})

	if d.pathInput.Value() != "" {
		t.Errorf("path should be empty after backspace, got %q", d.pathInput.Value())
	}
	if d.pathSoftSelected {
		t.Error("pathSoftSelected should be false after backspace")
	}
}

func TestNewDialog_SoftSelect_MovementExits(t *testing.T) {
	d := NewNewDialog()
	d.SetSize(80, 40)
	d.Show()

	d.focusIndex = 2
	d.updateFocus()

	originalPath := d.pathInput.Value()
	if !d.pathSoftSelected {
		t.Fatal("pathSoftSelected should be true")
	}

	// Press Left to exit soft-select
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyLeft})

	// Value should be preserved
	if d.pathInput.Value() != originalPath {
		t.Errorf("path = %q, want %q (value should be preserved)", d.pathInput.Value(), originalPath)
	}
	if d.pathSoftSelected {
		t.Error("pathSoftSelected should be false after Left key")
	}
}

func TestNewDialog_SoftSelect_TabPreservesValue(t *testing.T) {
	d := NewNewDialog()
	d.SetSize(80, 40)
	d.Show()

	d.focusIndex = 2
	d.updateFocus()

	originalPath := d.pathInput.Value()
	if !d.pathSoftSelected {
		t.Fatal("pathSoftSelected should be true")
	}

	// Press Tab to accept and move to next field
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyTab})

	// Value should be preserved (Tab accepts as-is)
	if d.pathInput.Value() != originalPath {
		t.Errorf("path = %q, want %q (Tab should preserve value)", d.pathInput.Value(), originalPath)
	}
	// Focus should have moved forward
	if d.focusIndex != 3 {
		t.Errorf("focusIndex = %d, want 3 (should move to command)", d.focusIndex)
	}
}

func TestNewDialog_SoftSelect_CtrlNExits(t *testing.T) {
	d := NewNewDialog()
	d.SetSize(80, 40)
	d.Show()

	suggestions := []string{"/path/one", "/path/two"}
	d.SetPathSuggestions(suggestions)

	d.focusIndex = 2
	d.updateFocus()

	if !d.pathSoftSelected {
		t.Fatal("pathSoftSelected should be true")
	}

	// Ctrl+N should exit soft-select and navigate suggestions
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyCtrlN})

	if d.pathSoftSelected {
		t.Error("pathSoftSelected should be false after Ctrl+N")
	}
	if !d.suggestionNavigated {
		t.Error("suggestionNavigated should be true after Ctrl+N")
	}
}

func TestNewDialog_SoftSelect_ReactivatesOnRefocus(t *testing.T) {
	d := NewNewDialog()
	d.SetSize(80, 40)
	d.Show()

	d.focusIndex = 2
	d.updateFocus()

	// Exit soft-select by typing
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if d.pathSoftSelected {
		t.Fatal("should not be soft-selected after typing")
	}

	// Set a value back and Tab away
	d.pathInput.SetValue("/some/path")
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyTab}) // move to command

	// Shift+Tab back to path
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyShiftTab})

	if d.focusIndex != 2 {
		t.Fatalf("focusIndex = %d, want 2", d.focusIndex)
	}
	if !d.pathSoftSelected {
		t.Error("pathSoftSelected should reactivate when refocusing path field with value")
	}
}

// ===== Filter Path Tests =====

func TestNewDialog_FilterPaths_SubstringMatch(t *testing.T) {
	d := NewNewDialog()
	d.SetPathSuggestions([]string{
		"/Users/test/skulk-project",
		"/Users/test/other-project",
		"/Users/test/skulking-around",
	})

	d.pathInput.SetValue("skulk")
	d.filterPathSuggestions()

	if len(d.pathSuggestions) != 2 {
		t.Errorf("expected 2 matching paths for 'skulk', got %d", len(d.pathSuggestions))
	}
}

func TestNewDialog_FilterPaths_CaseInsensitive(t *testing.T) {
	d := NewNewDialog()
	d.SetPathSuggestions([]string{
		"/Users/test/MyProject",
		"/Users/test/other",
	})

	d.pathInput.SetValue("MYPROJECT")
	d.filterPathSuggestions()

	if len(d.pathSuggestions) != 1 {
		t.Errorf("expected 1 matching path for 'MYPROJECT' (case insensitive), got %d", len(d.pathSuggestions))
	}
}

func TestNewDialog_FilterPaths_NoMatch(t *testing.T) {
	d := NewNewDialog()
	d.SetPathSuggestions([]string{
		"/Users/test/project-alpha",
		"/Users/test/project-beta",
	})

	d.pathInput.SetValue("zzz")
	d.filterPathSuggestions()

	if len(d.pathSuggestions) != 0 {
		t.Errorf("expected 0 matching paths for 'zzz', got %d", len(d.pathSuggestions))
	}
}

func TestNewDialog_FilterPaths_EmptyInput(t *testing.T) {
	d := NewNewDialog()
	paths := []string{
		"/Users/test/project-alpha",
		"/Users/test/project-beta",
		"/Users/test/other-thing",
	}
	d.SetPathSuggestions(paths)

	d.pathInput.SetValue("")
	d.filterPathSuggestions()

	if len(d.pathSuggestions) != 3 {
		t.Errorf("expected all 3 suggestions for empty input, got %d", len(d.pathSuggestions))
	}
}

func TestNewDialog_BranchPrefix_Default(t *testing.T) {
	d := NewNewDialog()
	if d.branchPrefix != "feature/" {
		t.Errorf("expected branchPrefix %q from constructor, got %q", "feature/", d.branchPrefix)
	}
}

func TestNewDialog_BranchPrefix_Custom_AutoPopulates(t *testing.T) {
	d := NewNewDialog()
	d.branchPrefix = "dev/"
	d.nameInput.SetValue("my-session")
	d.autoBranchFromName()

	if got := d.branchInput.Value(); got != "dev/my-session" {
		t.Errorf("expected branch %q, got %q", "dev/my-session", got)
	}
}

func TestNewDialog_BranchPrefix_Empty_NoPrefix(t *testing.T) {
	d := NewNewDialog()
	d.branchPrefix = ""
	d.nameInput.SetValue("my-session")
	d.autoBranchFromName()

	if got := d.branchInput.Value(); got != "my-session" {
		t.Errorf("expected branch %q, got %q", "my-session", got)
	}
}

func TestNewDialog_BranchPrefix_Placeholder_Updated(t *testing.T) {
	d := NewNewDialog()
	d.branchPrefix = "fix/"
	d.branchInput.Placeholder = d.branchPrefix + "branch-name"

	if d.branchInput.Placeholder != "fix/branch-name" {
		t.Errorf("expected placeholder %q, got %q", "fix/branch-name", d.branchInput.Placeholder)
	}
}

func TestNewDialog_ToggleWorktree_CustomPrefix(t *testing.T) {
	d := NewNewDialog()
	d.branchPrefix = "dev/"
	d.nameInput.SetValue("cool-feature")
	d.ToggleWorktree()

	if got := d.branchInput.Value(); got != "dev/cool-feature" {
		t.Errorf("expected branch %q, got %q", "dev/cool-feature", got)
	}
}

func TestOverlayDropdown_Basic(t *testing.T) {
	base := "line0\nline1\nline2\nline3\nline4"
	overlay := "AAA\nBBB"

	result := overlayDropdown(base, overlay, 1, 0)
	lines := strings.Split(result, "\n")

	if len(lines) != 5 {
		t.Fatalf("expected 5 lines, got %d", len(lines))
	}
	if lines[0] != "line0" {
		t.Errorf("line 0: expected %q, got %q", "line0", lines[0])
	}
	// overlay at col 0: "AAA" replaces first 3 chars of "line1", remainder "e1" preserved
	if lines[1] != "AAAe1" {
		t.Errorf("line 1: expected %q, got %q", "AAAe1", lines[1])
	}
}

func TestOverlayDropdown_WithOffset(t *testing.T) {
	base := "0123456789\n0123456789\n0123456789"
	overlay := "XX"

	result := overlayDropdown(base, overlay, 1, 3)
	lines := strings.Split(result, "\n")

	// Line 1 should be "012XX56789"
	if lines[1] != "012XX56789" {
		t.Errorf("expected %q, got %q", "012XX56789", lines[1])
	}
	// Other lines unchanged
	if lines[0] != "0123456789" {
		t.Errorf("line 0 should be unchanged, got %q", lines[0])
	}
	if lines[2] != "0123456789" {
		t.Errorf("line 2 should be unchanged, got %q", lines[2])
	}
}

func TestOverlayDropdown_PreservesLineCount(t *testing.T) {
	base := "a\nb\nc\nd\ne\nf"
	overlay := "X\nY\nZ"

	result := overlayDropdown(base, overlay, 2, 0)
	lines := strings.Split(result, "\n")

	if len(lines) != 6 {
		t.Fatalf("overlay should not change line count: expected 6, got %d", len(lines))
	}
}

func TestOverlayDropdown_OutOfBounds(t *testing.T) {
	base := "a\nb"
	overlay := "X\nY\nZ"

	// Overlay starts at row 1, only 1 line fits (row 1), row 2 is out of bounds
	result := overlayDropdown(base, overlay, 1, 0)
	lines := strings.Split(result, "\n")

	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	if lines[0] != "a" {
		t.Errorf("line 0 should be unchanged, got %q", lines[0])
	}
}

// TestNewDialog_NameInput_AcceptsUnderscore verifies that typing '_' into the
// name input reaches the textinput buffer (regression test for BUG-02).
func TestNewDialog_NameInput_AcceptsUnderscore(t *testing.T) {
	d := NewNewDialog()
	d.Show()

	underscoreKey := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'_'}}
	updated, _ := d.Update(underscoreKey)

	if updated.nameInput.Value() != "_" {
		t.Errorf("nameInput.Value() = %q after typing '_', want %q", updated.nameInput.Value(), "_")
	}
}

// TestNewDialog_PathInput_AcceptsUnderscore verifies that typing '_' into the
// path input reaches the textinput buffer (regression test for BUG-02).
// Focus targets: focusName(0), focusMultiRepo(1), focusPath(2), ...
// Two Tabs are required to reach focusPath from focusName.
func TestNewDialog_PathInput_AcceptsUnderscore(t *testing.T) {
	d := NewNewDialog()
	d.Show()

	// Tab twice to reach the path input field (focusName -> focusMultiRepo -> focusPath).
	d = sendSpecialKey(d, tea.KeyTab)
	d = sendSpecialKey(d, tea.KeyTab)

	// Type '_' — the soft-select logic clears any pre-populated value and
	// focuses the textinput before letting the rune reach it.
	underscoreKey := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'_'}}
	updated, _ := d.Update(underscoreKey)

	if !strings.Contains(updated.pathInput.Value(), "_") {
		t.Errorf("pathInput.Value() = %q after typing '_', want value to contain '_'", updated.pathInput.Value())
	}
}

// TestNewDialog_View_ShowsStartQueryField_WhenClaudeSelected asserts the
// v1.7.67 "Start query" input renders in the claude-options panel when
// the claude preset is selected. The field is the dedicated entry point
// for claude-code's positional startup-query argument, replacing the
// extra-args misuse documented in @Clindbergh's GH #725 report.
func TestNewDialog_View_ShowsStartQueryField_WhenClaudeSelected(t *testing.T) {
	dialog := NewNewDialog()
	dialog.SetSize(100, 50)
	dialog.Show()
	// commandCursor = 1 selects "claude" (see buildPresetCommands order).
	dialog.commandCursor = 1
	dialog.updateToolOptions()

	view := dialog.View()

	if !strings.Contains(view, "Start query") {
		t.Errorf(
			"View should contain 'Start query' label when claude is "+
				"selected; without this label the user has no discoverable "+
				"way to pass a per-session startup query. got:\n%s",
			view,
		)
	}
}

// TestNewDialog_GetClaudeStartQuery_ReturnsInputValue asserts the
// accessor returns the raw input string (multi-word, un-split). This is
// the value the launch code path assigns to Instance.StartupQuery; if it
// split on spaces here (as extra-args does via strings.Fields), the
// bug @Clindbergh reported would reappear.
func TestNewDialog_GetClaudeStartQuery_ReturnsInputValue(t *testing.T) {
	dialog := NewNewDialog()
	dialog.Show()
	dialog.commandCursor = 1 // claude
	dialog.updateToolOptions()

	// Use reflection to drive the test even before GetClaudeStartQuery exists.
	dv := reflect.ValueOf(dialog)
	method := dv.MethodByName("GetClaudeStartQuery")
	if !method.IsValid() {
		t.Fatalf(
			"NewDialog.GetClaudeStartQuery() does not exist; add it in " +
				"internal/ui/newdialog.go next to GetClaudeExtraArgs. It " +
				"must return string (NOT []string — the query is a single " +
				"positional arg, never split on spaces).",
		)
	}

	// Drive the underlying input through ClaudeOptionsPanel.SetStartQuery
	// or direct field access; use reflection to stay compile-safe.
	panelMethod := reflect.ValueOf(dialog.claudeOptions).MethodByName("SetStartQuery")
	if !panelMethod.IsValid() {
		t.Fatalf(
			"ClaudeOptionsPanel.SetStartQuery(string) does not exist; " +
				"add it next to SetExtraArgs in internal/ui/claudeoptions.go.",
		)
	}
	panelMethod.Call([]reflect.Value{reflect.ValueOf("explain the codebase")})

	out := method.Call(nil)
	if len(out) != 1 || out[0].Kind() != reflect.String {
		t.Fatalf("GetClaudeStartQuery must return (string); got %v", out)
	}
	got := out[0].String()
	if got != "explain the codebase" {
		t.Errorf(
			"GetClaudeStartQuery() = %q, want %q (exact string, no space-split)",
			got, "explain the codebase",
		)
	}
}

func TestNewDialog_ShowInGroup_LoadsConfiguredClaudeExtraArgs(t *testing.T) {
	origHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	os.Setenv("HOME", tmpHome)
	session.ClearUserConfigCache()
	defer func() {
		os.Setenv("HOME", origHome)
		session.ClearUserConfigCache()
	}()

	if err := session.SaveUserConfig(&session.UserConfig{
		Claude: session.ClaudeSettings{
			ExtraArgs:       []string{"--agent", "reviewer", "--model", "opus"},
			UseChrome:       true,
			UseTeammateMode: true,
		},
	}); err != nil {
		t.Fatalf("SaveUserConfig: %v", err)
	}

	dialog := NewNewDialog()
	dialog.SetDefaultTool("claude")
	dialog.ShowInGroup("default", "default", "", nil, "")

	got := dialog.GetClaudeExtraArgs()
	want := []string{"--agent", "reviewer", "--model", "opus"}
	if len(got) != len(want) {
		t.Fatalf("GetClaudeExtraArgs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("GetClaudeExtraArgs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	opts := dialog.GetClaudeOptions()
	if opts == nil {
		t.Fatal("GetClaudeOptions() = nil")
	}
	if !opts.UseChrome {
		t.Fatal("GetClaudeOptions().UseChrome = false, want true")
	}
	if !opts.UseTeammateMode {
		t.Fatal("GetClaudeOptions().UseTeammateMode = false, want true")
	}
}

// TestNewDialog_StartQuery_ClearsBetweenOpenings is the RED regression for
// #741 (@Clindbergh). Filed against v1.7.67 after #725 shipped the dedicated
// "Start query" field: opening the new-session dialog a second time showed
// the previous invocation's query instead of an empty field. The backend is
// correct (Instance.StartupQuery is `json:"-"` so SQLite doesn't persist it);
// the leak is purely at the TUI layer — ShowInGroup clears nameInput,
// pathInput, branchInput, etc. but never resets claudeOptions.startQueryInput.
// This test opens the dialog, sets a query, closes, re-opens, and asserts
// the field is empty.
func TestNewDialog_StartQuery_ClearsBetweenOpenings(t *testing.T) {
	dialog := NewNewDialog()
	dialog.Show()
	dialog.commandCursor = 1 // claude
	dialog.updateToolOptions()

	dialog.claudeOptions.SetStartQuery("explain this repo")
	if got := dialog.GetClaudeStartQuery(); got != "explain this repo" {
		t.Fatalf("precondition: GetClaudeStartQuery() = %q, want %q", got, "explain this repo")
	}

	dialog.Hide()
	dialog.Show()
	dialog.commandCursor = 1
	dialog.updateToolOptions()

	if got := dialog.GetClaudeStartQuery(); got != "" {
		t.Errorf(
			"Start query must be empty on re-open; got %q. Per-session "+
				"startup queries are ephemeral (Instance.StartupQuery is "+
				"json:\"-\") and must not leak between dialog invocations.",
			got,
		)
	}
}
