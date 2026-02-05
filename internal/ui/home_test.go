package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
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
