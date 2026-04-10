package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/update"
)

func TestNewHome(t *testing.T) {
	home := NewHome()
	if home == nil {
		t.Fatal("NewHome returned nil")
	}
	if home.storage == nil {
		t.Error("Storage should be initialized")
	}
	if home.search == nil {
		t.Error("Search component should be initialized")
	}
	if home.newDialog == nil {
		t.Error("NewDialog component should be initialized")
	}
}

func TestNewHome_DisablesTmuxNotificationsWhenStatusInjectionDisabled(t *testing.T) {
	origHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	os.Setenv("HOME", tmpHome)
	session.ClearUserConfigCache()
	defer func() {
		os.Setenv("HOME", origHome)
		session.ClearUserConfigCache()
	}()

	configDir := filepath.Join(tmpHome, ".agent-deck")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() failed: %v", err)
	}
	configPath := filepath.Join(configDir, "config.toml")
	config := "[tmux]\ninject_status_line = false\n"
	if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
		t.Fatalf("WriteFile() failed: %v", err)
	}

	home := NewHome()
	if home.manageTmuxNotifications {
		t.Fatal("manageTmuxNotifications should be false when inject_status_line is disabled")
	}
	if home.notificationsEnabled {
		t.Fatal("notificationsEnabled should stay false when tmux status injection is disabled")
	}
	if home.notificationManager != nil {
		t.Fatal("notificationManager should not initialize when tmux status injection is disabled")
	}
}

func TestApplyCreateSessionToolOverrides_GeminiExplicitFalsePersists(t *testing.T) {
	inst := session.NewInstanceWithTool("gemini-test", "/tmp/test", "gemini")
	applyCreateSessionToolOverrides(inst, "gemini", false)
	if inst.GeminiYoloMode == nil {
		t.Fatal("GeminiYoloMode should be set when Gemini YOLO is explicitly disabled")
	}
	if *inst.GeminiYoloMode {
		t.Fatal("GeminiYoloMode should be false when Gemini YOLO is explicitly disabled")
	}
}

func TestApplyCreateSessionToolOverrides_NonGeminiNoop(t *testing.T) {
	inst := session.NewInstanceWithTool("claude-test", "/tmp/test", "claude")
	applyCreateSessionToolOverrides(inst, "claude", true)
	if inst.GeminiYoloMode != nil {
		t.Fatalf("GeminiYoloMode = %v, want nil for non-gemini tools", inst.GeminiYoloMode)
	}
}

func TestHomeInit(t *testing.T) {
	home := NewHome()
	cmd := home.Init()
	// Init should return a command for loading sessions
	if cmd == nil {
		t.Error("Init should return a command")
	}
}

func TestHomeView(t *testing.T) {
	home := NewHome()
	home.width = 100
	home.height = 30

	view := home.View()
	if view == "" {
		t.Error("View should not be empty")
	}
	if view == "Loading..." {
		// Initial state is OK
		return
	}
}

func TestHomeView_StaysWithinBoundsWhileNavigating(t *testing.T) {
	home := NewHome()
	home.width = 200
	home.height = 55
	home.initialLoading = false

	instances := []*session.Instance{
		session.NewInstanceWithTool("conductor-ryan", "/tmp/conductor", "claude"),
		session.NewInstanceWithTool("copy from server", "/tmp/social-copy", "claude"),
		session.NewInstanceWithTool("test", "/tmp/social-test", "claude"),
		session.NewInstanceWithTool("vscode on linux", "/tmp/linux", "claude"),
		session.NewInstanceWithTool("about gsd", "/tmp/about-gsd", "claude"),
		session.NewInstanceWithTool("places to work from", "/tmp/places", "claude"),
	}

	instances[0].GroupPath = "conductor"
	for _, inst := range instances[1:] {
		inst.GroupPath = "Social Monitor"
	}
	instances[3].Status = session.StatusError

	home.instancesMu.Lock()
	home.instances = instances
	home.instancesMu.Unlock()
	home.groupTree = session.NewGroupTree(instances)
	home.rebuildFlatItems()

	if len(home.flatItems) == 0 {
		t.Fatal("expected flatItems to be populated")
	}

	for cursor := range home.flatItems {
		home.cursor = cursor
		view := home.View()
		assertViewWithinBounds(t, view, home.width, home.height, fmt.Sprintf("cursor=%d type=%v", cursor, home.flatItems[cursor].Type))
	}
}

func assertViewWithinBounds(t *testing.T, view string, width, height int, context string) {
	t.Helper()

	lines := strings.Split(view, "\n")
	if len(lines) != height {
		t.Fatalf("%s: line count = %d, want %d", context, len(lines), height)
	}

	for i, line := range lines {
		if got := lipgloss.Width(line); got > width {
			t.Fatalf("%s: line %d width = %d, want <= %d\nline=%q", context, i, got, width, line)
		}
	}
}

func TestHomeUpdateQuit(t *testing.T) {
	home := NewHome()
	home.width = 100
	home.height = 30

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}}
	_, cmd := home.Update(msg)

	// Should return quit command
	if cmd == nil {
		t.Log("Quit command expected (may be nil in test context)")
	}
}

func TestHomeUpdateResize(t *testing.T) {
	home := NewHome()

	msg := tea.WindowSizeMsg{Width: 120, Height: 40}
	model, _ := home.Update(msg)

	h, ok := model.(*Home)
	if !ok {
		t.Fatal("Update should return *Home")
	}
	if h.width != 120 {
		t.Errorf("Width = %d, want 120", h.width)
	}
	if h.height != 40 {
		t.Errorf("Height = %d, want 40", h.height)
	}
}

func TestHomeUpdateSearch(t *testing.T) {
	home := NewHome()
	home.width = 100
	home.height = 30

	// Disable global search to test local search behavior
	home.globalSearchIndex = nil

	// Press / to open search (should open local search when global is not available)
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}}
	model, _ := home.Update(msg)

	h, ok := model.(*Home)
	if !ok {
		t.Fatal("Update should return *Home")
	}
	if !h.search.IsVisible() {
		t.Error("Local search should be visible after pressing / when global search is not available")
	}
}

func TestHomeUpdateNewDialog(t *testing.T) {
	home := NewHome()
	home.width = 100
	home.height = 30

	// Press n to open new dialog
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}}
	model, _ := home.Update(msg)

	h, ok := model.(*Home)
	if !ok {
		t.Fatal("Update should return *Home")
	}
	if !h.newDialog.IsVisible() {
		t.Error("New dialog should be visible after pressing n")
	}
}

func TestHomeLoadSessions(t *testing.T) {
	home := NewHome()

	// Trigger load sessions
	msg := home.loadSessions()

	loadMsg, ok := msg.(loadSessionsMsg)
	if !ok {
		t.Fatal("loadSessions should return loadSessionsMsg")
	}

	// Should not error on empty storage
	if loadMsg.err != nil {
		t.Errorf("Unexpected error: %v", loadMsg.err)
	}
}

func TestHomeRenameGroupWithR(t *testing.T) {
	home := NewHome()
	home.width = 100
	home.height = 30

	// Create a group tree with a group
	home.groupTree = session.NewGroupTree([]*session.Instance{})
	home.groupTree.CreateGroup("test-group")
	home.rebuildFlatItems()

	// Position cursor on the group
	home.cursor = 0
	if len(home.flatItems) == 0 {
		t.Fatal("flatItems should have at least one group")
	}
	if home.flatItems[0].Type != session.ItemTypeGroup {
		t.Fatal("First item should be a group")
	}

	// Press r to open rename dialog
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}}
	model, _ := home.Update(msg)

	h, ok := model.(*Home)
	if !ok {
		t.Fatal("Update should return *Home")
	}
	if !h.groupDialog.IsVisible() {
		t.Error("Group dialog should be visible after pressing r on a group")
	}
	if h.groupDialog.Mode() != GroupDialogRename {
		t.Errorf("Dialog mode = %v, want GroupDialogRename", h.groupDialog.Mode())
	}
}

func TestHomeRenameSessionWithR(t *testing.T) {
	home := NewHome()
	home.width = 100
	home.height = 30

	// Create a test session
	inst := session.NewInstance("test-session", "/tmp/project")
	home.instancesMu.Lock()
	home.instances = []*session.Instance{inst}
	home.instancesMu.Unlock()
	home.groupTree = session.NewGroupTree(home.instances)
	home.rebuildFlatItems()

	// Find and position cursor on the session (skip the group)
	sessionIdx := -1
	for i, item := range home.flatItems {
		if item.Type == session.ItemTypeSession {
			sessionIdx = i
			break
		}
	}
	if sessionIdx == -1 {
		t.Fatal("No session found in flatItems")
	}
	home.cursor = sessionIdx

	// Press r to open rename dialog
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}}
	model, _ := home.Update(msg)

	h, ok := model.(*Home)
	if !ok {
		t.Fatal("Update should return *Home")
	}
	if !h.groupDialog.IsVisible() {
		t.Error("Group dialog should be visible after pressing r on a session")
	}
	if h.groupDialog.Mode() != GroupDialogRenameSession {
		t.Errorf("Dialog mode = %v, want GroupDialogRenameSession", h.groupDialog.Mode())
	}
	if h.groupDialog.GetSessionID() != inst.ID {
		t.Errorf("Session ID = %s, want %s", h.groupDialog.GetSessionID(), inst.ID)
	}
}

func TestHomeRenameSessionComplete(t *testing.T) {
	home := NewHome()
	home.width = 100
	home.height = 30

	// Create a test session
	inst := session.NewInstance("original-name", "/tmp/project")
	home.instancesMu.Lock()
	home.instances = []*session.Instance{inst}
	home.instanceByID[inst.ID] = inst // Also populate the O(1) lookup map
	home.instancesMu.Unlock()
	home.groupTree = session.NewGroupTree(home.instances)
	home.rebuildFlatItems()

	// Find and position cursor on the session
	sessionIdx := -1
	for i, item := range home.flatItems {
		if item.Type == session.ItemTypeSession {
			sessionIdx = i
			break
		}
	}
	home.cursor = sessionIdx

	// Press r to open rename dialog
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}}
	home.Update(msg)

	// Simulate typing a new name
	home.groupDialog.nameInput.SetValue("new-name")

	// Press Enter to confirm
	enterMsg := tea.KeyMsg{Type: tea.KeyEnter}
	model, _ := home.Update(enterMsg)

	h, ok := model.(*Home)
	if !ok {
		t.Fatal("Update should return *Home")
	}
	if h.groupDialog.IsVisible() {
		t.Error("Dialog should be hidden after pressing Enter")
	}
	if h.instances[0].Title != "new-name" {
		t.Errorf("Session title = %s, want new-name", h.instances[0].Title)
	}
}

func TestHomeMoveSessionWithDuplicateGroupNamesUsesSelectedPath(t *testing.T) {
	home := NewHome()
	home.width = 100
	home.height = 30

	inst := &session.Instance{
		ID:          "sess-1",
		Title:       "session-1",
		ProjectPath: "/tmp/project",
		GroupPath:   "work/frontend",
	}

	tree := session.NewGroupTree([]*session.Instance{})
	tree.CreateGroup("work")
	tree.CreateSubgroup("work", "frontend")
	tree.CreateGroup("play")
	tree.CreateSubgroup("play", "frontend")
	tree.AddSession(inst)

	home.instancesMu.Lock()
	home.instances = []*session.Instance{inst}
	home.instanceByID[inst.ID] = inst
	home.instancesMu.Unlock()
	home.groupTree = tree
	home.rebuildFlatItems()

	sessionIdx := -1
	for i, item := range home.flatItems {
		if item.Type == session.ItemTypeSession && item.Session != nil && item.Session.ID == inst.ID {
			sessionIdx = i
			break
		}
	}
	if sessionIdx == -1 {
		t.Fatal("session item not found in flatItems")
	}
	home.cursor = sessionIdx

	model, _ := home.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'M'}})
	h, ok := model.(*Home)
	if !ok {
		t.Fatal("Update should return *Home")
	}
	if !h.groupDialog.IsVisible() || h.groupDialog.Mode() != GroupDialogMove {
		t.Fatal("move dialog should be visible after pressing M on a session")
	}

	targetIdx := -1
	for i, path := range h.groupDialog.groupPaths {
		if path == "play/frontend" {
			targetIdx = i
			break
		}
	}
	if targetIdx == -1 {
		t.Fatalf("target group path not found in move dialog: %v", h.groupDialog.groupPaths)
	}
	h.groupDialog.selected = targetIdx

	model, _ = h.Update(tea.KeyMsg{Type: tea.KeyEnter})
	h2, ok := model.(*Home)
	if !ok {
		t.Fatal("Update should return *Home")
	}

	moved := h2.getInstanceByID(inst.ID)
	if moved == nil {
		t.Fatal("moved instance not found by ID")
	}
	if moved.GroupPath != "play/frontend" {
		t.Fatalf("GroupPath = %q, want %q", moved.GroupPath, "play/frontend")
	}
}

func TestHomeEnterDuringLaunchingDoesNotShowStartingError(t *testing.T) {
	home := NewHome()
	home.width = 100
	home.height = 30

	inst := session.NewInstance("launching-session", "/tmp/project")
	home.instancesMu.Lock()
	home.instances = []*session.Instance{inst}
	home.instanceByID[inst.ID] = inst
	home.instancesMu.Unlock()

	home.flatItems = []session.Item{
		{Type: session.ItemTypeSession, Session: inst},
	}
	home.cursor = 0
	home.launchingSessions[inst.ID] = time.Now()

	model, _ := home.Update(tea.KeyMsg{Type: tea.KeyEnter})
	h, ok := model.(*Home)
	if !ok {
		t.Fatal("Update should return *Home")
	}

	if h.err != nil && strings.Contains(h.err.Error(), "session is starting, please wait") {
		t.Fatalf("unexpected launch block error: %v", h.err)
	}
}

func TestLaunchAnimationMinDurationByTool(t *testing.T) {
	if got := launchAnimationMinDuration("claude"); got != minLaunchAnimationDurationClaude {
		t.Fatalf("claude min duration = %v, want %v", got, minLaunchAnimationDurationClaude)
	}
	if got := launchAnimationMinDuration("gemini"); got != minLaunchAnimationDurationClaude {
		t.Fatalf("gemini min duration = %v, want %v", got, minLaunchAnimationDurationClaude)
	}
	if got := launchAnimationMinDuration("shell"); got != minLaunchAnimationDurationDefault {
		t.Fatalf("default min duration = %v, want %v", got, minLaunchAnimationDurationDefault)
	}
}

func TestHomeRenamePendingChangesSurviveReload(t *testing.T) {
	home := NewHome()
	home.width = 100
	home.height = 30

	// Create a test session
	inst := session.NewInstance("original-name", "/tmp/project")
	home.instancesMu.Lock()
	home.instances = []*session.Instance{inst}
	home.instanceByID[inst.ID] = inst
	home.instancesMu.Unlock()
	home.groupTree = session.NewGroupTree(home.instances)
	home.rebuildFlatItems()

	// Simulate a rename that stores a pending title change
	home.pendingTitleChanges[inst.ID] = "renamed-title"

	// Simulate a reload (loadSessionsMsg) with the OLD title from disk
	reloadInst := session.NewInstance("original-name", "/tmp/project")
	reloadInst.ID = inst.ID // Same session, old title

	reloadMsg := loadSessionsMsg{
		instances:    []*session.Instance{reloadInst},
		groups:       nil,
		restoreState: &reloadState{cursorSessionID: inst.ID},
	}

	model, _ := home.Update(reloadMsg)
	h := model.(*Home)

	// The pending rename should have been re-applied after reload
	if h.instances[0].Title != "renamed-title" {
		t.Errorf("Session title = %s, want renamed-title (pending rename should survive reload)", h.instances[0].Title)
	}
	// Pending changes should be cleared after re-application
	if len(h.pendingTitleChanges) != 0 {
		t.Errorf("pendingTitleChanges should be empty after re-application, got %d", len(h.pendingTitleChanges))
	}
}

func TestHomeRenamePendingChangesNoop(t *testing.T) {
	home := NewHome()
	home.width = 100
	home.height = 30

	// Create a test session
	inst := session.NewInstance("desired-name", "/tmp/project")
	home.instancesMu.Lock()
	home.instances = []*session.Instance{inst}
	home.instanceByID[inst.ID] = inst
	home.instancesMu.Unlock()
	home.groupTree = session.NewGroupTree(home.instances)
	home.rebuildFlatItems()

	// Store a pending change that matches the current title (normal save succeeded)
	home.pendingTitleChanges[inst.ID] = "desired-name"

	// Reload with data that already has the correct title
	reloadInst := session.NewInstance("desired-name", "/tmp/project")
	reloadInst.ID = inst.ID

	reloadMsg := loadSessionsMsg{
		instances:    []*session.Instance{reloadInst},
		groups:       nil,
		restoreState: &reloadState{cursorSessionID: inst.ID},
	}

	model, _ := home.Update(reloadMsg)
	h := model.(*Home)

	// Title should still be correct
	if h.instances[0].Title != "desired-name" {
		t.Errorf("Session title = %s, want desired-name", h.instances[0].Title)
	}
	// Pending changes should be cleared (no re-application needed)
	if len(h.pendingTitleChanges) != 0 {
		t.Errorf("pendingTitleChanges should be empty, got %d", len(h.pendingTitleChanges))
	}
}

func TestHomeGlobalSearchInitialized(t *testing.T) {
	home := NewHome()
	if home.globalSearch == nil {
		t.Error("GlobalSearch component should be initialized")
	}
	// globalSearchIndex may be nil if not enabled in config, that's OK
}

func TestHomeSearchOpensGlobalWhenAvailable(t *testing.T) {
	home := NewHome()
	home.width = 100
	home.height = 30

	// Create a mock index
	tmpDir := t.TempDir()
	config := session.GlobalSearchSettings{
		Enabled:        true,
		Tier:           "instant",
		MemoryLimitMB:  100,
		IndexRateLimit: 100,
	}
	index, err := session.NewGlobalSearchIndex(tmpDir, config)
	if err != nil {
		t.Fatalf("Failed to create test index: %v", err)
	}
	defer index.Close()

	home.globalSearchIndex = index
	home.globalSearch.SetIndex(index)

	// Press / to open search - should open global search when index is available
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}}
	model, _ := home.Update(msg)

	h, ok := model.(*Home)
	if !ok {
		t.Fatal("Update should return *Home")
	}
	if !h.globalSearch.IsVisible() {
		t.Error("Global search should be visible after pressing / when index is available")
	}
	if h.search.IsVisible() {
		t.Error("Local search should NOT be visible when global search opens")
	}
}

func TestHomeSearchOpensLocalWhenNoIndex(t *testing.T) {
	home := NewHome()
	home.width = 100
	home.height = 30

	// Ensure no global search index
	home.globalSearchIndex = nil

	// Press / to open search - should fall back to local search
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}}
	model, _ := home.Update(msg)

	h, ok := model.(*Home)
	if !ok {
		t.Fatal("Update should return *Home")
	}
	if h.globalSearch.IsVisible() {
		t.Error("Global search should NOT be visible when index is nil")
	}
	if !h.search.IsVisible() {
		t.Error("Local search should be visible when global index is not available")
	}
}

func TestHomeGlobalSearchEscape(t *testing.T) {
	home := NewHome()
	home.width = 100
	home.height = 30

	// Create a mock index
	tmpDir := t.TempDir()
	config := session.GlobalSearchSettings{
		Enabled:        true,
		Tier:           "instant",
		MemoryLimitMB:  100,
		IndexRateLimit: 100,
	}
	index, err := session.NewGlobalSearchIndex(tmpDir, config)
	if err != nil {
		t.Fatalf("Failed to create test index: %v", err)
	}
	defer index.Close()

	home.globalSearchIndex = index
	home.globalSearch.SetIndex(index)

	// Open global search with /
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}}
	home.Update(msg)

	if !home.globalSearch.IsVisible() {
		t.Fatal("Global search should be visible after pressing /")
	}

	// Press Escape to close
	escMsg := tea.KeyMsg{Type: tea.KeyEsc}
	model, _ := home.Update(escMsg)

	h, ok := model.(*Home)
	if !ok {
		t.Fatal("Update should return *Home")
	}
	if h.globalSearch.IsVisible() {
		t.Error("Global search should be hidden after pressing Escape")
	}
}

func TestGetLayoutMode(t *testing.T) {
	tests := []struct {
		name     string
		width    int
		expected string
	}{
		{"narrow phone", 45, "single"},
		{"phone landscape", 65, "stacked"},
		{"tablet", 85, "dual"},
		{"desktop", 120, "dual"},
		{"exact boundary 50", 50, "stacked"},
		{"exact boundary 80", 80, "dual"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := NewHome()
			home.width = tt.width
			got := home.getLayoutMode()
			if got != tt.expected {
				t.Errorf("getLayoutMode() at width %d = %q, want %q", tt.width, got, tt.expected)
			}
		})
	}
}

func TestHandleMainKeyEditNotesStartsEditor(t *testing.T) {
	enabled := true
	setPreviewShowNotesConfigForTest(t, &enabled)

	home := NewHome()
	home.width = 100
	home.height = 30

	inst := &session.Instance{
		ID:    "session-notes",
		Title: "Session With Notes",
		Tool:  "claude",
		Notes: "existing notes",
	}
	home.flatItems = []session.Item{{Type: session.ItemTypeSession, Session: inst}}
	home.cursor = 0
	home.instanceByID[inst.ID] = inst

	model, _ := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	h, ok := model.(*Home)
	if !ok {
		t.Fatal("handleMainKey should return *Home")
	}

	if !h.notesEditing {
		t.Fatal("notes editor should be active after pressing edit hotkey")
	}
	if h.notesEditingSessionID != inst.ID {
		t.Fatalf("notes editing session = %q, want %q", h.notesEditingSessionID, inst.ID)
	}
	if got := h.notesEditor.Value(); got != inst.Notes {
		t.Fatalf("notes editor value = %q, want %q", got, inst.Notes)
	}
}

func TestHandleNotesEditorKeySave(t *testing.T) {
	home := NewHome()
	home.width = 100
	home.height = 30
	home.storage = nil // Avoid touching persistence in this unit test.

	inst := &session.Instance{
		ID:    "session-save-notes",
		Title: "Save Notes",
		Tool:  "claude",
		Notes: "before",
	}
	home.flatItems = []session.Item{{Type: session.ItemTypeSession, Session: inst}}
	home.cursor = 0
	home.instanceByID[inst.ID] = inst
	home.beginNotesEditing(inst)
	home.notesEditor.SetValue("after")

	model, _ := home.handleNotesEditorKey(tea.KeyMsg{Type: tea.KeyCtrlS})
	h, ok := model.(*Home)
	if !ok {
		t.Fatal("handleNotesEditorKey should return *Home")
	}

	if got := inst.Notes; got != "after" {
		t.Fatalf("session notes = %q, want %q", got, "after")
	}
	if h.notesEditing {
		t.Fatal("notes editor should close after save")
	}
}

func TestNotesSectionLineBudget(t *testing.T) {
	tests := []struct {
		name          string
		remaining     int
		reserveOutput bool
		split         float64
		want          int
	}{
		{name: "none", remaining: 0, reserveOutput: true, split: 0.33, want: 0},
		{name: "default split", remaining: 20, reserveOutput: true, split: 0.33, want: 6},
		{name: "clamp minimum", remaining: 5, reserveOutput: true, split: 0.1, want: 2},
		{name: "clamp maximum", remaining: 10, reserveOutput: true, split: 0.9, want: 7},
		{name: "no output reserve", remaining: 8, reserveOutput: false, split: 0.33, want: 8},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := notesSectionLineBudget(tt.remaining, tt.reserveOutput, tt.split); got != tt.want {
				t.Fatalf("notesSectionLineBudget(%d,%v,%v) = %d, want %d", tt.remaining, tt.reserveOutput, tt.split, got, tt.want)
			}
		})
	}
}

func setFollowCwdOnAttachConfigForTest(t *testing.T, enabled *bool) {
	t.Helper()

	homeDir, err := os.MkdirTemp("", "follow-cwd-test-*")
	if err != nil {
		t.Fatalf("failed to create temp home: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(homeDir) })
	t.Setenv("HOME", homeDir)

	configDir := filepath.Join(homeDir, ".agent-deck")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("failed to create config directory: %v", err)
	}

	if enabled != nil {
		value := "false"
		if *enabled {
			value = "true"
		}
		content := fmt.Sprintf("[instances]\nfollow_cwd_on_attach = %s\n", value)
		configPath := filepath.Join(configDir, session.UserConfigFileName)
		if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
			t.Fatalf("failed to write config.toml: %v", err)
		}
	}

	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)
}

func setPreviewShowNotesConfigForTest(t *testing.T, enabled *bool) {
	t.Helper()

	homeDir, err := os.MkdirTemp("", "follow-cwd-test-*")
	if err != nil {
		t.Fatalf("failed to create temp home: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(homeDir) })
	t.Setenv("HOME", homeDir)

	configDir := filepath.Join(homeDir, ".agent-deck")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("failed to create config directory: %v", err)
	}

	if enabled != nil {
		value := "false"
		if *enabled {
			value = "true"
		}
		content := fmt.Sprintf("[preview]\nshow_notes = %s\n", value)
		configPath := filepath.Join(configDir, session.UserConfigFileName)
		if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
			t.Fatalf("failed to write config.toml: %v", err)
		}
	}

	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)
}

func TestFollowAttachReturnCwdEnabledUpdatesProjectPath(t *testing.T) {
	enabled := true
	setFollowCwdOnAttachConfigForTest(t, &enabled)

	home := NewHome()
	home.storage = nil // Prevent persistence side effects in this unit test.

	initialDir := t.TempDir()
	inst := session.NewInstance("follow-cwd", initialDir)
	newDir := t.TempDir()

	home.instancesMu.Lock()
	home.instances = []*session.Instance{inst}
	home.instanceByID[inst.ID] = inst
	home.instancesMu.Unlock()

	home.followAttachReturnCwd(statusUpdateMsg{attachedSessionID: inst.ID, attachedWorkDir: newDir})

	want := filepath.Clean(newDir)
	if got := inst.ProjectPath; got != want {
		t.Fatalf("project path = %q, want %q", got, want)
	}
	tmuxSess := inst.GetTmuxSession()
	if tmuxSess == nil {
		t.Fatal("tmux session should be initialized")
	}
	if got := tmuxSess.WorkDir; got != want {
		t.Fatalf("tmux work dir = %q, want %q", got, want)
	}
}

func TestFollowAttachReturnCwdDisabledDoesNotUpdateProjectPath(t *testing.T) {
	disabled := false
	setFollowCwdOnAttachConfigForTest(t, &disabled)

	home := NewHome()
	home.storage = nil

	initialDir := t.TempDir()
	inst := session.NewInstance("no-follow-cwd", initialDir)
	newDir := t.TempDir()

	home.instancesMu.Lock()
	home.instances = []*session.Instance{inst}
	home.instanceByID[inst.ID] = inst
	home.instancesMu.Unlock()

	home.followAttachReturnCwd(statusUpdateMsg{attachedSessionID: inst.ID, attachedWorkDir: newDir})

	if got := inst.ProjectPath; got != initialDir {
		t.Fatalf("project path changed = %q, want %q", got, initialDir)
	}
}

func TestFollowAttachReturnCwdRejectsInvalidPaths(t *testing.T) {
	enabled := true
	setFollowCwdOnAttachConfigForTest(t, &enabled)

	tests := []struct {
		name    string
		workDir string
	}{
		{name: "relative", workDir: "relative/path"},
		{name: "missing", workDir: filepath.Join(t.TempDir(), "missing")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := NewHome()
			home.storage = nil

			initialDir := t.TempDir()
			inst := session.NewInstance("reject-path", initialDir)

			home.instancesMu.Lock()
			home.instances = []*session.Instance{inst}
			home.instanceByID[inst.ID] = inst
			home.instancesMu.Unlock()

			home.followAttachReturnCwd(statusUpdateMsg{attachedSessionID: inst.ID, attachedWorkDir: tt.workDir})

			if got := inst.ProjectPath; got != initialDir {
				t.Fatalf("project path changed = %q, want %q", got, initialDir)
			}
		})
	}
}

func TestHandleMainKeyEditNotesDisabledWhenShowNotesFalse(t *testing.T) {
	disabled := false
	setPreviewShowNotesConfigForTest(t, &disabled)

	home := NewHome()
	home.width = 100
	home.height = 30

	inst := session.NewInstance("notes-disabled", t.TempDir())
	home.flatItems = []session.Item{{Type: session.ItemTypeSession, Session: inst}}
	home.cursor = 0

	model, _ := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	h, ok := model.(*Home)
	if !ok {
		t.Fatal("handleMainKey should return *Home")
	}
	if h.notesEditing {
		t.Fatal("notes editor should remain disabled when show_notes=false")
	}
	if h.notesEditingSessionID != "" {
		t.Fatalf("notesEditingSessionID = %q, want empty", h.notesEditingSessionID)
	}
}

func TestHandleMainKeyEditNotesDisabledByDefault(t *testing.T) {
	// When no config exists (nil show_notes), notes should be OFF by default.
	setPreviewShowNotesConfigForTest(t, nil)

	home := NewHome()
	home.width = 100
	home.height = 30

	inst := session.NewInstance("notes-default-off", t.TempDir())
	home.flatItems = []session.Item{{Type: session.ItemTypeSession, Session: inst}}
	home.cursor = 0

	model, _ := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	h, ok := model.(*Home)
	if !ok {
		t.Fatal("handleMainKey should return *Home")
	}
	if h.notesEditing {
		t.Fatal("notes editor should remain disabled when show_notes is not configured (default off)")
	}
}

func TestHandleMainKeyEditNotesEnabledWhenShowNotesTrue(t *testing.T) {
	enabled := true
	setPreviewShowNotesConfigForTest(t, &enabled)

	home := NewHome()
	home.width = 100
	home.height = 30

	inst := session.NewInstance("notes-enabled", t.TempDir())
	home.flatItems = []session.Item{{Type: session.ItemTypeSession, Session: inst}}
	home.cursor = 0

	model, _ := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	h, ok := model.(*Home)
	if !ok {
		t.Fatal("handleMainKey should return *Home")
	}
	if !h.notesEditing {
		t.Fatal("notes editor should be enabled when show_notes=true")
	}
}

func TestRenderSessionListEmptyUsesConfiguredKeys(t *testing.T) {
	home := NewHome()
	home.setHotkeys(resolveHotkeys(map[string]string{
		hotkeyNewSession:  "a",
		hotkeyImport:      "b",
		hotkeyCreateGroup: "c",
	}))

	rendered := home.renderSessionList(60, 22)

	for _, want := range []string{
		"Press a to create a new session",
		"Press b to import existing tmux sessions",
		"Press c to create a group",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("empty state missing hint %q\nrendered=%q", want, rendered)
		}
	}
}

func TestRenderSessionListEmptyWithUnboundPrimaryActions(t *testing.T) {
	home := NewHome()
	home.setHotkeys(resolveHotkeys(map[string]string{
		hotkeyNewSession:  "",
		hotkeyImport:      "",
		hotkeyCreateGroup: "",
	}))

	rendered := home.renderSessionList(60, 22)

	if !strings.Contains(rendered, "Create or import sessions to get started") {
		t.Fatalf("empty state should show fallback hint when all actions are unbound\nrendered=%q", rendered)
	}
}

func TestSessionClosedMsgUsesConfiguredRestartHint(t *testing.T) {
	home := NewHome()
	home.storage = nil
	home.setHotkeys(resolveHotkeys(map[string]string{hotkeyRestart: "ctrl+r"}))

	inst := session.NewInstance("closed-session", t.TempDir())
	home.instancesMu.Lock()
	home.instances = []*session.Instance{inst}
	home.instanceByID[inst.ID] = inst
	home.instancesMu.Unlock()

	model, _ := home.Update(sessionClosedMsg{sessionID: inst.ID})
	h, ok := model.(*Home)
	if !ok {
		t.Fatal("Update should return *Home")
	}

	if h.err == nil {
		t.Fatal("expected close-session message to be set")
	}
	if !strings.Contains(h.err.Error(), "ctrl+r to restart") {
		t.Fatalf("close-session message should use configured restart key, got %q", h.err.Error())
	}
}

func TestDeleteAndCloseSessionUseDistinctActions(t *testing.T) {
	home := NewHome()
	home.width = 100
	home.height = 30

	inst := session.NewInstance("actions-session", t.TempDir())
	home.flatItems = []session.Item{{Type: session.ItemTypeSession, Session: inst}}
	home.cursor = 0
	home.instanceByID[inst.ID] = inst

	model, _ := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	h, ok := model.(*Home)
	if !ok {
		t.Fatal("handleMainKey should return *Home")
	}
	if !h.confirmDialog.IsVisible() {
		t.Fatal("delete should show confirmation dialog")
	}
	if got := h.confirmDialog.GetConfirmType(); got != ConfirmDeleteSession {
		t.Fatalf("confirm type after delete = %v, want %v", got, ConfirmDeleteSession)
	}

	h.confirmDialog.Hide()

	model, _ = h.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'D'}})
	h, ok = model.(*Home)
	if !ok {
		t.Fatal("handleMainKey should return *Home")
	}
	if !h.confirmDialog.IsVisible() {
		t.Fatal("close should show confirmation dialog")
	}
	if got := h.confirmDialog.GetConfirmType(); got != ConfirmCloseSession {
		t.Fatalf("confirm type after close = %v, want %v", got, ConfirmCloseSession)
	}
}

func TestDeleteHotkeyRemapAndCloseUnbind(t *testing.T) {
	home := NewHome()
	home.width = 100
	home.height = 30
	home.setHotkeys(resolveHotkeys(map[string]string{
		hotkeyDelete:       "backspace",
		hotkeyCloseSession: "",
	}))

	inst := session.NewInstance("actions-remap", t.TempDir())
	home.flatItems = []session.Item{{Type: session.ItemTypeSession, Session: inst}}
	home.cursor = 0
	home.instanceByID[inst.ID] = inst

	model, _ := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'D'}})
	h, ok := model.(*Home)
	if !ok {
		t.Fatal("handleMainKey should return *Home")
	}
	if h.confirmDialog.IsVisible() {
		t.Fatal("unbound close_session key should not open confirmation")
	}

	model, _ = h.handleMainKey(tea.KeyMsg{Type: tea.KeyBackspace})
	h, ok = model.(*Home)
	if !ok {
		t.Fatal("handleMainKey should return *Home")
	}
	if !h.confirmDialog.IsVisible() {
		t.Fatal("remapped delete key should show confirmation dialog")
	}
	if got := h.confirmDialog.GetConfirmType(); got != ConfirmDeleteSession {
		t.Fatalf("confirm type after remapped delete = %v, want %v", got, ConfirmDeleteSession)
	}
}

func TestRemoteDeleteAndCloseUseDistinctActions(t *testing.T) {
	home := NewHome()
	home.width = 100
	home.height = 30

	remote := session.RemoteSessionInfo{ID: "remote-123", Title: "remote-session", RemoteName: "myserver"}
	home.flatItems = []session.Item{{Type: session.ItemTypeRemoteSession, RemoteSession: &remote, RemoteName: "myserver"}}
	home.cursor = 0

	model, _ := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	h, ok := model.(*Home)
	if !ok {
		t.Fatal("handleMainKey should return *Home")
	}
	if !h.confirmDialog.IsVisible() {
		t.Fatal("delete should show confirmation dialog")
	}
	if got := h.confirmDialog.GetConfirmType(); got != ConfirmDeleteRemoteSession {
		t.Fatalf("confirm type after delete = %v, want %v", got, ConfirmDeleteRemoteSession)
	}
	if got := h.confirmDialog.GetRemoteName(); got != "myserver" {
		t.Fatalf("remote name after delete = %q, want %q", got, "myserver")
	}

	h.confirmDialog.Hide()

	model, _ = h.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'D'}})
	h, ok = model.(*Home)
	if !ok {
		t.Fatal("handleMainKey should return *Home")
	}
	if !h.confirmDialog.IsVisible() {
		t.Fatal("close should show confirmation dialog")
	}
	if got := h.confirmDialog.GetConfirmType(); got != ConfirmCloseRemoteSession {
		t.Fatalf("confirm type after close = %v, want %v", got, ConfirmCloseRemoteSession)
	}
	if got := h.confirmDialog.GetRemoteName(); got != "myserver" {
		t.Fatalf("remote name after close = %q, want %q", got, "myserver")
	}
}

func TestRemoteRestartReturnsRemoteCommand(t *testing.T) {
	home := NewHome()
	home.width = 100
	home.height = 30

	remote := session.RemoteSessionInfo{ID: "remote-123", Title: "remote-session", RemoteName: "myserver"}
	home.flatItems = []session.Item{{Type: session.ItemTypeRemoteSession, RemoteSession: &remote, RemoteName: "myserver"}}
	home.cursor = 0

	model, cmd := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'R'}})
	h, ok := model.(*Home)
	if !ok {
		t.Fatal("handleMainKey should return *Home")
	}
	if cmd == nil {
		t.Fatal("restart should return a command")
	}

	msg := cmd()
	restartMsg, ok := msg.(remoteSessionRestartedMsg)
	if !ok {
		t.Fatalf("command returned %T, want remoteSessionRestartedMsg", msg)
	}
	if restartMsg.remoteName != "myserver" {
		t.Fatalf("remoteName = %q, want %q", restartMsg.remoteName, "myserver")
	}
	if restartMsg.sessionID != "remote-123" {
		t.Fatalf("sessionID = %q, want %q", restartMsg.sessionID, "remote-123")
	}
	if restartMsg.title != "remote-session" {
		t.Fatalf("title = %q, want %q", restartMsg.title, "remote-session")
	}
	if restartMsg.err == nil {
		t.Fatal("expected error when remote config is unavailable")
	}

	_ = h
}

func TestRenderHelpBarTiny(t *testing.T) {
	home := NewHome()
	home.width = 45 // Tiny mode (<50 cols)
	home.height = 30

	result := home.renderHelpBar()

	// Should contain minimal hint
	if !strings.Contains(result, "?") {
		t.Error("Tiny help bar should contain ? for help")
	}
	// Should NOT contain full shortcuts
	if strings.Contains(result, "Attach") {
		t.Error("Tiny help bar should not contain 'Attach'")
	}
	if strings.Contains(result, "Global") {
		t.Error("Tiny help bar should not contain 'Global'")
	}
}

func TestRenderHelpBarTinyUsesConfiguredHelpKey(t *testing.T) {
	home := NewHome()
	home.width = 45
	home.height = 30
	home.setHotkeys(resolveHotkeys(map[string]string{"help": "h"}))

	result := home.renderHelpBar()
	if !strings.Contains(result, "h for help") {
		t.Fatalf("tiny help bar should use remapped help key, got %q", result)
	}
}

func TestRenderHelpBarTinyHandlesUnboundHelpKey(t *testing.T) {
	home := NewHome()
	home.width = 45
	home.height = 30
	home.setHotkeys(resolveHotkeys(map[string]string{"help": ""}))

	result := home.renderHelpBar()
	if !strings.Contains(result, "Help key unbound") {
		t.Fatalf("tiny help bar should show unbound message, got %q", result)
	}
}

func TestRenderHelpBarMinimal(t *testing.T) {
	home := NewHome()
	home.width = 55 // Minimal mode (50-69)
	home.height = 30

	result := home.renderHelpBar()

	// Should contain key-only hints
	if !strings.Contains(result, "?") {
		t.Error("Minimal help bar should contain ?")
	}
	if !strings.Contains(result, "q") {
		t.Error("Minimal help bar should contain q")
	}
	// Should NOT contain full descriptions
	if strings.Contains(result, "Attach") {
		t.Error("Minimal help bar should not contain full descriptions")
	}
}

func TestRenderHelpBarMinimalWithSession(t *testing.T) {
	home := NewHome()
	home.width = 55 // Minimal mode (50-69)
	home.height = 30

	// Add a session to test context-specific keys
	testSession := &session.Instance{
		ID:    "test-123",
		Title: "Test Session",
		Tool:  "claude",
	}
	home.flatItems = []session.Item{
		{Type: session.ItemTypeSession, Session: testSession},
	}
	home.cursor = 0

	result := home.renderHelpBar()

	// Should contain key indicators
	if !strings.Contains(result, "n") {
		t.Error("Minimal help bar should contain n key")
	}
	if !strings.Contains(result, "R") {
		t.Error("Minimal help bar should contain R key for restart")
	}
	// Should NOT contain full descriptions
	if strings.Contains(result, "Attach") {
		t.Error("Minimal help bar should not contain full descriptions")
	}
}

func TestRenderHelpBarCompact(t *testing.T) {
	home := NewHome()
	home.width = 85 // Compact mode (70-99)
	home.height = 30

	result := home.renderHelpBar()

	// Should contain abbreviated hints
	if !strings.Contains(result, "?") {
		t.Error("Compact help bar should contain ?")
	}
	// Should contain some descriptions but abbreviated
	if strings.Contains(result, "Global") {
		t.Error("Compact help bar should not contain 'Global'")
	}
}

func TestRenderHelpBarCompactWithSession(t *testing.T) {
	home := NewHome()
	home.width = 85 // Compact mode (70-99)
	home.height = 30

	// Add a session with fork capability
	// ClaudeDetectedAt must be recent for CanFork() to return true
	testSession := &session.Instance{
		ID:               "test-123",
		Title:            "Test Session",
		Tool:             "claude",
		ClaudeSessionID:  "session-abc",
		ClaudeDetectedAt: time.Now(), // Must be recent for CanFork()
	}
	home.flatItems = []session.Item{
		{Type: session.ItemTypeSession, Session: testSession},
	}
	home.cursor = 0

	result := home.renderHelpBar()

	// Should have abbreviated descriptions
	if !strings.Contains(result, "New") {
		t.Error("Compact help bar should contain 'New'")
	}
	if !strings.Contains(result, "Restart") {
		t.Error("Compact help bar should contain 'Restart'")
	}
	// Should have fork since session can fork
	if !strings.Contains(result, "Fork") {
		t.Error("Compact help bar should contain 'Fork' for forkable session")
	}
	// Should NOT contain full verbose text
	if strings.Contains(result, "Global") {
		t.Error("Compact help bar should not contain 'Global'")
	}
}

func TestRenderHelpBarCompactWithGroup(t *testing.T) {
	home := NewHome()
	home.width = 85 // Compact mode (70-99)
	home.height = 30

	// Add a group
	home.flatItems = []session.Item{
		{Type: session.ItemTypeGroup, Path: "test-group", Level: 0},
	}
	home.cursor = 0

	result := home.renderHelpBar()

	// Should have toggle hint for groups
	if !strings.Contains(result, "Toggle") {
		t.Error("Compact help bar should contain 'Toggle' for groups")
	}
}

func TestHomeViewNarrowTerminal(t *testing.T) {
	tests := []struct {
		name          string
		width, height int
		shouldRender  bool
	}{
		{"too narrow", 35, 20, false},
		{"minimum width", 40, 12, true},
		{"narrow but ok", 50, 15, true},
		{"issue #2 case", 79, 70, true},
		{"normal", 100, 30, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := NewHome()
			home.width = tt.width
			home.height = tt.height

			view := home.View()

			if tt.shouldRender {
				if strings.Contains(view, "Terminal too small") {
					t.Errorf("width=%d height=%d should render, got 'too small' message", tt.width, tt.height)
				}
			} else {
				if !strings.Contains(view, "Terminal too small") {
					t.Errorf("width=%d height=%d should show 'too small', got normal render", tt.width, tt.height)
				}
			}
		})
	}
}

func TestHomeViewStackedLayout(t *testing.T) {
	home := NewHome()
	home.width = 65 // Stacked mode (50-79)
	home.height = 40
	home.initialLoading = false

	// Add a test session so we have content
	inst := &session.Instance{ID: "test1", Title: "Test Session", Tool: "claude", Status: session.StatusIdle}
	home.instancesMu.Lock()
	home.instances = []*session.Instance{inst}
	home.instancesMu.Unlock()
	home.groupTree = session.NewGroupTree(home.instances)
	home.rebuildFlatItems()

	view := home.View()

	// In stacked mode, we should NOT see side-by-side separator
	// The view should render without panicking
	if view == "" {
		t.Error("View should not be empty")
	}
	if strings.Contains(view, "Terminal too small") {
		t.Error("65-col terminal should not show 'too small' error")
	}
}

func TestHomeViewUsesCachedPreviewDuringNavigationBursts(t *testing.T) {
	tests := []struct {
		name   string
		width  int
		height int
	}{
		{name: "dual layout", width: 100, height: 30},
		{name: "stacked layout", width: 65, height: 50},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := NewHome()
			home.width = tt.width
			home.height = tt.height
			home.initialLoading = false

			inst := session.NewInstanceWithTool("Preview Session", "/tmp/project", "other")
			inst.ID = "preview-session"
			inst.Status = session.StatusIdle
			inst.CreatedAt = time.Now().Add(-time.Minute)

			home.instancesMu.Lock()
			home.instances = []*session.Instance{inst}
			home.instanceByID[inst.ID] = inst
			home.instancesMu.Unlock()
			home.groupTree = session.NewGroupTree(home.instances)
			home.rebuildFlatItems()
			home.refreshSessionRenderSnapshot(home.instances)
			for i, item := range home.flatItems {
				if item.Type == session.ItemTypeSession && item.Session != nil && item.Session.ID == inst.ID {
					home.cursor = i
					break
				}
			}

			home.previewCacheMu.Lock()
			home.previewCache[inst.ID] = "cached preview content that should remain visible immediately"
			home.previewCacheTime[inst.ID] = time.Now()
			home.previewCacheMu.Unlock()

			home.isNavigating = true
			home.lastNavigationTime = time.Now()
			home.lastAttachReturn = time.Now()
			home.navigationHotUntil.Store(time.Now().Add(900 * time.Millisecond).UnixNano())

			view := home.View()

			if !strings.Contains(view, "cached preview content") {
				t.Fatalf("View() should render cached preview content during navigation burst:\n%s", view)
			}
			if strings.Contains(view, "Preview paused while navigating...") {
				t.Fatalf("View() should not suppress preview pane during navigation burst:\n%s", view)
			}
			if strings.Contains(view, "Moving... preview updating") {
				t.Fatalf("View() should not replace cached preview during navigation burst:\n%s", view)
			}
			if strings.Contains(view, "Returned from session... refreshing preview") {
				t.Fatalf("View() should not hide cached preview after attach return:\n%s", view)
			}
		})
	}
}

func TestHomeViewSingleColumnLayout(t *testing.T) {
	home := NewHome()
	home.width = 45 // Single column mode (<50)
	home.height = 30
	home.initialLoading = false

	// Add a test session
	inst := &session.Instance{ID: "test1", Title: "Test Session", Tool: "claude", Status: session.StatusIdle}
	home.instancesMu.Lock()
	home.instances = []*session.Instance{inst}
	home.instancesMu.Unlock()
	home.groupTree = session.NewGroupTree(home.instances)
	home.rebuildFlatItems()

	view := home.View()

	// In single column mode, should show list only (no preview)
	if view == "" {
		t.Error("View should not be empty")
	}
	if strings.Contains(view, "Terminal too small") {
		t.Error("45-col terminal should not show 'too small' error")
	}
}

func TestPushUndoStackLIFO(t *testing.T) {
	home := NewHome()

	// Push 3 sessions
	for i := 0; i < 3; i++ {
		inst := session.NewInstance(fmt.Sprintf("session-%d", i), "/tmp")
		home.pushUndoStack(inst)
	}

	if len(home.undoStack) != 3 {
		t.Fatalf("undoStack length = %d, want 3", len(home.undoStack))
	}

	// Verify LIFO order: last pushed should be at the end
	if home.undoStack[2].instance.Title != "session-2" {
		t.Errorf("top of stack = %s, want session-2", home.undoStack[2].instance.Title)
	}
	if home.undoStack[0].instance.Title != "session-0" {
		t.Errorf("bottom of stack = %s, want session-0", home.undoStack[0].instance.Title)
	}
}

func TestPushUndoStackCap(t *testing.T) {
	home := NewHome()

	// Push 12 sessions (exceeds cap of 10)
	for i := 0; i < 12; i++ {
		inst := session.NewInstance(fmt.Sprintf("session-%d", i), "/tmp")
		home.pushUndoStack(inst)
	}

	if len(home.undoStack) != 10 {
		t.Fatalf("undoStack length = %d, want 10 (capped)", len(home.undoStack))
	}

	// Oldest 2 should be dropped, so first entry should be session-2
	if home.undoStack[0].instance.Title != "session-2" {
		t.Errorf("bottom of stack = %s, want session-2 (oldest dropped)", home.undoStack[0].instance.Title)
	}
	// Most recent should be session-11
	if home.undoStack[9].instance.Title != "session-11" {
		t.Errorf("top of stack = %s, want session-11", home.undoStack[9].instance.Title)
	}
}

func TestCtrlZEmptyStack(t *testing.T) {
	home := NewHome()
	home.width = 100
	home.height = 30

	// Press Ctrl+Z with empty stack
	msg := tea.KeyMsg{Type: tea.KeyCtrlZ}
	model, cmd := home.Update(msg)

	h, ok := model.(*Home)
	if !ok {
		t.Fatal("Update should return *Home")
	}

	// Should show "nothing to undo" error
	if h.err == nil {
		t.Error("Expected error message for empty undo stack")
	} else if !strings.Contains(h.err.Error(), "nothing to undo") {
		t.Errorf("Error = %q, want 'nothing to undo'", h.err.Error())
	}

	// Should not return a command
	if cmd != nil {
		t.Error("Expected nil command for empty undo stack")
	}
}

func TestUndoHintInHelpBar(t *testing.T) {
	home := NewHome()
	home.width = 200 // Wide terminal to fit all hints including Undo
	home.height = 30

	// Add a session to have context (non-Claude to reduce hint count)
	inst := &session.Instance{ID: "test-1", Title: "Test", Tool: "other"}
	home.flatItems = []session.Item{
		{Type: session.ItemTypeSession, Session: inst},
	}
	home.cursor = 0

	// No undo stack: should NOT show ^Z
	result := home.renderHelpBar()
	if strings.Contains(result, "Undo") {
		t.Error("Help bar should NOT show Undo when undo stack is empty")
	}

	// Push to undo stack: should show ^Z
	home.pushUndoStack(session.NewInstance("deleted", "/tmp"))
	result = home.renderHelpBar()
	if !strings.Contains(result, "Undo") {
		t.Errorf("Help bar should show Undo when undo stack is non-empty\nGot: %q", result)
	}
}

// newTestHomeWithItems creates a Home with flatItems populated, initial loading disabled, and sized.
func newTestHomeWithItems(width, height int, items []session.Item) *Home {
	home := NewHome()
	home.width = width
	home.height = height
	home.initialLoading = false
	home.flatItems = items
	home.lastClickIndex = -1
	return home
}

func TestMouseYToItemIndex(t *testing.T) {
	// Standard layout: header(1) + filter(1) + panelTitle(2) = startY 4
	// No banners, no scroll offset
	items := []session.Item{
		{Type: session.ItemTypeGroup, Path: "group-a", Level: 0},
		{Type: session.ItemTypeSession, Session: &session.Instance{ID: "s1", Title: "Session 1"}, Level: 1},
		{Type: session.ItemTypeSession, Session: &session.Instance{ID: "s2", Title: "Session 2"}, Level: 1},
	}

	tests := []struct {
		name       string
		y          int
		viewOffset int
		wantIndex  int
		banners    bool // enable update + maintenance banners
	}{
		{"click on first item", 4, 0, 0, false},
		{"click on second item", 5, 0, 1, false},
		{"click on third item", 6, 0, 2, false},
		{"click above list", 3, 0, -1, false},
		{"click way below items", 20, 0, -1, false},
		{"with banners first item", 6, 0, 0, true},
		{"with banners second item", 7, 0, 1, true},
		{"scrolled down click first visible", 5, 1, 1, false}, // line 4 = "more above", line 5 = first item
		{"scrolled down click more-above indicator", 4, 1, -1, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := newTestHomeWithItems(100, 30, items)
			home.viewOffset = tc.viewOffset
			if tc.banners {
				home.updateInfo = &update.UpdateInfo{Available: true, CurrentVersion: "1.0", LatestVersion: "2.0"}
				home.maintenanceMsg = "test maintenance"
			}

			got := home.mouseYToItemIndex(tc.y)
			if got != tc.wantIndex {
				t.Errorf("mouseYToItemIndex(y=%d, viewOffset=%d) = %d, want %d", tc.y, tc.viewOffset, got, tc.wantIndex)
			}
		})
	}
}

func TestMouseYToItemIndexEmptyList(t *testing.T) {
	home := newTestHomeWithItems(100, 30, nil)

	if got := home.mouseYToItemIndex(5); got != -1 {
		t.Errorf("mouseYToItemIndex on empty list = %d, want -1", got)
	}
}

func TestMouseClickXBoundaryPerLayout(t *testing.T) {
	items := []session.Item{
		{Type: session.ItemTypeSession, Session: &session.Instance{ID: "s1", Title: "S1"}, Level: 0},
		{Type: session.ItemTypeSession, Session: &session.Instance{ID: "s2", Title: "S2"}, Level: 0},
	}

	tests := []struct {
		name        string
		width       int
		x           int
		wantChanged bool // whether cursor should move from 0 to 1
	}{
		{"dual layout click in list", 100, 10, true},
		{"dual layout click in preview", 100, 50, false},
		{"stacked layout click anywhere", 65, 50, true},
		{"single layout click anywhere", 45, 10, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := newTestHomeWithItems(tc.width, 30, items)
			home.cursor = 0

			msg := tea.MouseMsg{X: tc.x, Y: 5, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}
			model, _ := home.Update(msg)
			h := model.(*Home)

			changed := h.cursor != 0
			if changed != tc.wantChanged {
				t.Errorf("cursor changed=%v, want changed=%v (cursor=%d)", changed, tc.wantChanged, h.cursor)
			}
		})
	}
}

func TestMouseSingleClickSelectsItem(t *testing.T) {
	items := []session.Item{
		{Type: session.ItemTypeGroup, Path: "group-a", Level: 0},
		{Type: session.ItemTypeSession, Session: &session.Instance{ID: "s1", Title: "Session 1"}, Level: 1},
		{Type: session.ItemTypeSession, Session: &session.Instance{ID: "s2", Title: "Session 2"}, Level: 1},
	}

	home := newTestHomeWithItems(100, 30, items)
	home.cursor = 0

	// Click on second item (y=5 in standard layout)
	msg := tea.MouseMsg{
		X:      5,
		Y:      5,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
	}

	model, _ := home.Update(msg)
	h := model.(*Home)

	if h.cursor != 1 {
		t.Errorf("cursor = %d after click, want 1", h.cursor)
	}
}

func TestMouseDoubleClickActivatesSession(t *testing.T) {
	inst := session.NewInstance("test-session", "/tmp/project")
	items := []session.Item{
		{Type: session.ItemTypeGroup, Path: "my-sessions", Level: 0},
		{Type: session.ItemTypeSession, Session: inst, Level: 1},
	}

	home := newTestHomeWithItems(100, 30, items)
	home.cursor = 1 // Already on the session

	clickMsg := tea.MouseMsg{
		X:      5,
		Y:      5,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
	}

	// First click: selects item
	model, _ := home.Update(clickMsg)
	h := model.(*Home)

	// Second click within 500ms: should trigger attach (returns a command)
	model, cmd := h.Update(clickMsg)
	h = model.(*Home)

	// Double-click on a session should attempt attach (produces a command)
	// The session doesn't have a tmux session, so attachSession returns nil cmd,
	// but the double-click path resets lastClickIndex
	if h.lastClickIndex != -1 {
		t.Errorf("lastClickIndex = %d after double-click, want -1 (reset)", h.lastClickIndex)
	}
	_ = cmd // cmd may be nil since test session has no tmux backing
}

func TestMouseDoubleClickTogglesGroup(t *testing.T) {
	home := NewHome()
	home.width = 100
	home.height = 30
	home.initialLoading = false

	// Create a real group tree so ToggleGroup works
	home.groupTree = session.NewGroupTree([]*session.Instance{})
	home.groupTree.CreateGroup("test-group")
	home.rebuildFlatItems()

	if len(home.flatItems) == 0 {
		t.Fatal("flatItems should have at least one group")
	}

	// Verify group starts expanded
	wasExpanded := home.flatItems[0].Group.Expanded

	// y=4 = first item in list (header:1 + filter:1 + panel title:2)
	clickMsg := tea.MouseMsg{
		X:      5,
		Y:      4,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
	}

	// First click
	model, _ := home.Update(clickMsg)
	h := model.(*Home)

	// Second click (double-click to toggle)
	model, _ = h.Update(clickMsg)
	h = model.(*Home)

	// Find the group again after rebuild
	for _, item := range h.flatItems {
		if item.Type == session.ItemTypeGroup && item.Path == "test-group" {
			if item.Group.Expanded == wasExpanded {
				t.Error("Group expanded state should have toggled after double-click")
			}
			return
		}
	}
	t.Error("test-group not found in flatItems after double-click")
}

func TestMouseClickIgnoredInPreviewPanel(t *testing.T) {
	items := []session.Item{
		{Type: session.ItemTypeSession, Session: &session.Instance{ID: "s1", Title: "S1"}, Level: 0},
		{Type: session.ItemTypeSession, Session: &session.Instance{ID: "s2", Title: "S2"}, Level: 0},
	}

	home := newTestHomeWithItems(100, 30, items) // dual layout (width >= 80)
	home.cursor = 0

	// Click in preview panel (x=50, well past 35% of 100)
	msg := tea.MouseMsg{
		X:      50,
		Y:      5,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
	}

	model, _ := home.Update(msg)
	h := model.(*Home)
	if h.cursor != 0 {
		t.Errorf("cursor = %d after click in preview panel, want 0 (unchanged)", h.cursor)
	}
}

func TestMouseReleaseIgnored(t *testing.T) {
	items := []session.Item{
		{Type: session.ItemTypeSession, Session: &session.Instance{ID: "s1", Title: "S1"}, Level: 0},
		{Type: session.ItemTypeSession, Session: &session.Instance{ID: "s2", Title: "S2"}, Level: 0},
	}

	home := newTestHomeWithItems(100, 30, items)
	home.cursor = 0

	// Mouse release should not move cursor
	msg := tea.MouseMsg{
		X:      5,
		Y:      5,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionRelease,
	}

	model, _ := home.Update(msg)
	h := model.(*Home)
	if h.cursor != 0 {
		t.Errorf("cursor = %d after mouse release, want 0 (unchanged)", h.cursor)
	}
}

func TestMouseIgnoredWhenDialogVisible(t *testing.T) {
	items := []session.Item{
		{Type: session.ItemTypeSession, Session: &session.Instance{ID: "s1", Title: "S1"}, Level: 0},
		{Type: session.ItemTypeSession, Session: &session.Instance{ID: "s2", Title: "S2"}, Level: 0},
	}

	home := newTestHomeWithItems(100, 30, items)
	home.cursor = 0

	// Show search overlay
	home.search.Show()

	msg := tea.MouseMsg{
		X:      5,
		Y:      5,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
	}

	model, _ := home.Update(msg)
	h := model.(*Home)
	if h.cursor != 0 {
		t.Errorf("cursor = %d after click with search visible, want 0 (unchanged)", h.cursor)
	}
}

func TestMouseClickInStackedPreviewAreaIgnored(t *testing.T) {
	// Generate enough items to fill the list area
	items := make([]session.Item, 30)
	for i := range items {
		items[i] = session.Item{
			Type:    session.ItemTypeSession,
			Session: &session.Instance{ID: fmt.Sprintf("s%d", i), Title: fmt.Sprintf("Session %d", i)},
			Level:   0,
		}
	}

	// Stacked layout: width 65, height 40
	// contentHeight = 40 - 1(header) - 2(help) - 1(filter) = 36
	// listHeight = (36 * 60) / 100 = 21, list content = 21 - 2(title) = 19 lines
	// List content starts at y=4, ends around y=22
	// y=25 should be in the preview section
	home := newTestHomeWithItems(65, 40, items)
	home.cursor = 0

	msg := tea.MouseMsg{X: 10, Y: 25, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}
	model, _ := home.Update(msg)
	h := model.(*Home)

	if h.cursor != 0 {
		t.Errorf("cursor = %d after click in stacked preview area, want 0 (unchanged)", h.cursor)
	}
}

func TestMouseDoubleClickVerifiesItemIdentity(t *testing.T) {
	items := []session.Item{
		{Type: session.ItemTypeSession, Session: &session.Instance{ID: "s1", Title: "Session 1"}, Level: 0},
		{Type: session.ItemTypeSession, Session: &session.Instance{ID: "s2", Title: "Session 2"}, Level: 0},
	}

	home := newTestHomeWithItems(100, 30, items)

	// Click on index 0 (session s1)
	clickMsg := tea.MouseMsg{X: 5, Y: 4, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}
	model, _ := home.Update(clickMsg)
	h := model.(*Home)

	// Now swap items so index 0 is a different session (simulates rebuildFlatItems shifting items)
	h.flatItems = []session.Item{
		{Type: session.ItemTypeSession, Session: &session.Instance{ID: "s2", Title: "Session 2"}, Level: 0},
		{Type: session.ItemTypeSession, Session: &session.Instance{ID: "s1", Title: "Session 1"}, Level: 0},
	}

	// Second click at same position — same index but different item, should NOT double-click
	model, _ = h.Update(clickMsg)
	h = model.(*Home)

	// If it were a false double-click, lastClickIndex would be -1 (reset).
	// Since the item ID mismatches, it should be treated as a single click.
	if h.lastClickIndex == -1 {
		t.Error("click on different item at same index should not trigger double-click")
	}
}

func TestHomeViewAllLayoutModes(t *testing.T) {
	testCases := []struct {
		name       string
		width      int
		height     int
		layoutMode string
	}{
		{"single column", 45, 30, "single"},
		{"stacked", 65, 40, "stacked"},
		{"dual column", 100, 40, "dual"},
		{"issue #2 exact", 79, 70, "stacked"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			home := NewHome()
			home.width = tc.width
			home.height = tc.height
			home.initialLoading = false

			// Verify layout mode detection
			if got := home.getLayoutMode(); got != tc.layoutMode {
				t.Errorf("getLayoutMode() = %q, want %q", got, tc.layoutMode)
			}

			// Verify view renders without error
			view := home.View()
			if view == "" {
				t.Error("View should not be empty")
			}
			if strings.Contains(view, "Terminal too small") {
				t.Errorf("Terminal %dx%d should render, got 'too small'", tc.width, tc.height)
			}
		})
	}
}

func TestSessionRestartedMsgErrorClearsResumingAnimation(t *testing.T) {
	home := NewHome()
	inst := session.NewInstance("restart-test", "/tmp/project")

	home.instancesMu.Lock()
	home.instances = []*session.Instance{inst}
	home.instanceByID[inst.ID] = inst
	home.instancesMu.Unlock()

	home.resumingSessions[inst.ID] = time.Now()

	model, _ := home.Update(sessionRestartedMsg{
		sessionID: inst.ID,
		err:       fmt.Errorf("restart failed"),
	})
	h := model.(*Home)

	if _, ok := h.resumingSessions[inst.ID]; ok {
		t.Fatal("resuming animation should be cleared after restart error")
	}
	if h.err == nil {
		t.Fatal("expected restart error to be set")
	}
	if !strings.Contains(h.err.Error(), "failed to restart session") {
		t.Fatalf("unexpected error: %v", h.err)
	}
}

func TestRestartSessionCmdSessionMissingReturnsError(t *testing.T) {
	home := NewHome()
	inst := session.NewInstance("restart-test", "/tmp/project")

	// Build command with a valid instance, then simulate reload/delete before cmd runs.
	cmd := home.restartSession(inst)
	home.instancesMu.Lock()
	delete(home.instanceByID, inst.ID)
	home.instancesMu.Unlock()

	msg := cmd()
	restarted, ok := msg.(sessionRestartedMsg)
	if !ok {
		t.Fatalf("expected sessionRestartedMsg, got %T", msg)
	}
	if restarted.err == nil {
		t.Fatal("expected error when session no longer exists")
	}
	if !strings.Contains(restarted.err.Error(), "session no longer exists") {
		t.Fatalf("unexpected error: %v", restarted.err)
	}
}

func TestRebuildFlatItemsAutoClearsEmptyStatusFilter(t *testing.T) {
	home := NewHome()
	home.initialLoading = false

	// Create sessions that are all "running"
	inst1 := &session.Instance{ID: "s1", Title: "Session 1", Tool: "claude", Status: session.StatusRunning}
	inst2 := &session.Instance{ID: "s2", Title: "Session 2", Tool: "claude", Status: session.StatusRunning}

	home.instancesMu.Lock()
	home.instances = []*session.Instance{inst1, inst2}
	home.instanceByID[inst1.ID] = inst1
	home.instanceByID[inst2.ID] = inst2
	home.instancesMu.Unlock()
	home.groupTree = session.NewGroupTree(home.instances)

	// Set a filter for a status that no session has
	home.statusFilter = session.StatusError

	home.rebuildFlatItems()

	// Filter should have been auto-cleared since no sessions match "error"
	if home.statusFilter != "" {
		t.Errorf("statusFilter should be auto-cleared when filter matches nothing, got %q", home.statusFilter)
	}

	// All sessions should be visible
	sessionCount := 0
	for _, item := range home.flatItems {
		if item.Type == session.ItemTypeSession {
			sessionCount++
		}
	}
	if sessionCount != 2 {
		t.Errorf("expected 2 sessions in flatItems after auto-clear, got %d", sessionCount)
	}
}

func TestRebuildFlatItemsKeepsValidStatusFilter(t *testing.T) {
	home := NewHome()
	home.initialLoading = false

	// Create sessions with mixed statuses
	inst1 := &session.Instance{ID: "s1", Title: "Session 1", Tool: "claude", Status: session.StatusRunning}
	inst2 := &session.Instance{ID: "s2", Title: "Session 2", Tool: "claude", Status: session.StatusError}

	home.instancesMu.Lock()
	home.instances = []*session.Instance{inst1, inst2}
	home.instanceByID[inst1.ID] = inst1
	home.instanceByID[inst2.ID] = inst2
	home.instancesMu.Unlock()
	home.groupTree = session.NewGroupTree(home.instances)

	// Filter for error - one session matches
	home.statusFilter = session.StatusError

	home.rebuildFlatItems()

	// Filter should remain because it matches a session
	if home.statusFilter != session.StatusError {
		t.Errorf("statusFilter should remain %q when sessions match, got %q", session.StatusError, home.statusFilter)
	}

	// Only the error session should be visible
	sessionCount := 0
	for _, item := range home.flatItems {
		if item.Type == session.ItemTypeSession {
			sessionCount++
		}
	}
	if sessionCount != 1 {
		t.Errorf("expected 1 session in flatItems with error filter, got %d", sessionCount)
	}
}

func TestMatchesStatusFilter(t *testing.T) {
	tests := []struct {
		filter session.Status
		status session.Status
		want   bool
	}{
		// Active filter: excludes error and stopped only
		{FilterModeActive, session.StatusRunning, true},
		{FilterModeActive, session.StatusWaiting, true},
		{FilterModeActive, session.StatusIdle, true},
		{FilterModeActive, session.StatusStarting, true},
		{FilterModeActive, session.StatusError, false},
		{FilterModeActive, session.StatusStopped, false},
		// Concrete status filters: exact match
		{session.StatusRunning, session.StatusRunning, true},
		{session.StatusRunning, session.StatusWaiting, false},
		{session.StatusError, session.StatusError, true},
		{session.StatusError, session.StatusStopped, false},
	}
	for _, tt := range tests {
		got := matchesStatusFilter(tt.filter, tt.status)
		if got != tt.want {
			t.Errorf("matchesStatusFilter(%q, %q) = %v, want %v", tt.filter, tt.status, got, tt.want)
		}
	}
}

func TestSetGroupScope(t *testing.T) {
	home := NewHome()

	// Default is empty
	if home.groupScope != "" {
		t.Errorf("expected empty groupScope by default, got %q", home.groupScope)
	}

	// Set a group scope
	home.SetGroupScope("work")
	if home.groupScope != "work" {
		t.Errorf("expected groupScope %q, got %q", "work", home.groupScope)
	}

	// Overwrite with another value
	home.SetGroupScope("clients/acme")
	if home.groupScope != "clients/acme" {
		t.Errorf("expected groupScope %q, got %q", "clients/acme", home.groupScope)
	}
}

func TestGroupScopeNormalization(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"work", "work"},
		{"Work", "work"},
		{"My Group", "my-group"},
		{"MY GROUP", "my-group"},
		{"clients/Acme Corp", "clients/acme-corp"},
		{"already-normalized", "already-normalized"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			home := NewHome()
			home.SetGroupScope(tt.input)
			if home.groupScope != tt.want {
				t.Errorf("SetGroupScope(%q): got %q, want %q", tt.input, home.groupScope, tt.want)
			}
		})
	}
}

func TestRebuildFlatItemsGroupScope(t *testing.T) {
	h := &Home{}
	h.groupScope = "work"

	instances := []*session.Instance{
		session.NewInstanceWithGroup("s1", "/tmp/p1", "work"),
		session.NewInstanceWithGroup("s2", "/tmp/p2", "work/frontend"),
		session.NewInstanceWithGroup("s3", "/tmp/p3", "personal"),
	}
	h.groupTree = session.NewGroupTree(instances)
	h.windowsCollapsed = make(map[string]bool)

	h.rebuildFlatItems()

	for _, item := range h.flatItems {
		if item.Type == session.ItemTypeSession && item.Session != nil {
			if item.Session.GroupPath == "personal" {
				t.Errorf("found session in 'personal' group, expected only work and children")
			}
		}
		if item.Type == session.ItemTypeGroup && item.Path == "personal" {
			t.Errorf("found 'personal' group header, expected only work and children")
		}
	}

	found := map[string]bool{}
	for _, item := range h.flatItems {
		if item.Type == session.ItemTypeSession && item.Session != nil {
			found[item.Session.Title] = true
		}
	}
	if !found["s1"] {
		t.Error("missing session s1 (work group)")
	}
	if !found["s2"] {
		t.Error("missing session s2 (work/frontend group)")
	}
}

func TestRebuildFlatItemsGroupScopeComposesWithStatusFilter(t *testing.T) {
	h := &Home{}
	h.groupScope = "work"
	h.statusFilter = session.StatusRunning

	instances := []*session.Instance{
		session.NewInstanceWithGroup("running-work", "/tmp/p1", "work"),
		session.NewInstanceWithGroup("idle-work", "/tmp/p2", "work"),
		session.NewInstanceWithGroup("running-personal", "/tmp/p3", "personal"),
	}
	instances[0].Status = session.StatusRunning
	instances[1].Status = session.StatusIdle
	instances[2].Status = session.StatusRunning

	h.groupTree = session.NewGroupTree(instances)
	h.windowsCollapsed = make(map[string]bool)

	h.rebuildFlatItems()

	for _, item := range h.flatItems {
		if item.Type == session.ItemTypeSession && item.Session != nil {
			if item.Session.GroupPath == "personal" {
				t.Errorf("found personal session, expected only work group")
			}
			if item.Session.Status != session.StatusRunning {
				t.Errorf("found non-running session %q, expected only running", item.Session.Title)
			}
		}
	}
}

func TestIsInGroupScope(t *testing.T) {
	h := &Home{}

	// No scope set: everything is in scope
	if !h.isInGroupScope("anything") {
		t.Error("expected all paths in scope when groupScope is empty")
	}

	h.groupScope = "work"

	tests := []struct {
		path string
		want bool
	}{
		{"work", true},
		{"work/frontend", true},
		{"work/frontend/react", true},
		{"personal", false},
		{"worker", false}, // should NOT match (not a child)
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := h.isInGroupScope(tt.path); got != tt.want {
				t.Errorf("isInGroupScope(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestScopedGroupPaths(t *testing.T) {
	h := &Home{}
	instances := []*session.Instance{
		session.NewInstanceWithGroup("s1", "/tmp/p1", "work"),
		session.NewInstanceWithGroup("s2", "/tmp/p2", "work/frontend"),
		session.NewInstanceWithGroup("s3", "/tmp/p3", "personal"),
	}
	h.groupTree = session.NewGroupTree(instances)

	// No scope: returns all paths
	allPaths := h.scopedGroupPaths()
	if len(allPaths) < 3 {
		t.Errorf("expected at least 3 group paths without scope, got %d", len(allPaths))
	}

	// With scope: returns only work and children
	h.groupScope = "work"
	scopedPaths := h.scopedGroupPaths()
	for _, p := range scopedPaths {
		if !h.isInGroupScope(p) {
			t.Errorf("scopedGroupPaths returned %q which is not in scope", p)
		}
	}
	// Verify personal is excluded
	for _, p := range scopedPaths {
		if p == "personal" {
			t.Error("scopedGroupPaths should not include 'personal' when scoped to 'work'")
		}
	}
}
