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
	homeDir := setXDGTestHome(t)
	config := "[tmux]\ninject_status_line = false\n"
	writeXDGTestConfig(t, homeDir, config)

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

func TestPersistClaudeDialogDefaults(t *testing.T) {
	origHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	os.Setenv("HOME", tmpHome)
	session.ClearUserConfigCache()
	defer func() {
		os.Setenv("HOME", origHome)
		session.ClearUserConfigCache()
	}()

	persistClaudeDialogDefaults(&session.ClaudeOptions{
		SkipPermissions:      false,
		AllowSkipPermissions: true,
		AutoMode:             true,
		UseChrome:            true,
		UseTeammateMode:      true,
	}, []string{"--agent", "reviewer", "", " --model "})
	cfg, err := session.LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	want := []string{"--agent", "reviewer", "--model"}
	if len(cfg.Claude.ExtraArgs) != len(want) {
		t.Fatalf("Claude.ExtraArgs = %v, want %v", cfg.Claude.ExtraArgs, want)
	}
	for i := range want {
		if cfg.Claude.ExtraArgs[i] != want[i] {
			t.Fatalf("Claude.ExtraArgs[%d] = %q, want %q", i, cfg.Claude.ExtraArgs[i], want[i])
		}
	}
	if cfg.Claude.DangerousMode == nil || *cfg.Claude.DangerousMode {
		t.Fatalf("Claude.DangerousMode = %v, want explicit false", cfg.Claude.DangerousMode)
	}
	if !cfg.Claude.AllowDangerousMode {
		t.Fatal("Claude.AllowDangerousMode = false, want true")
	}
	if !cfg.Claude.AutoMode {
		t.Fatal("Claude.AutoMode = false, want true")
	}
	if !cfg.Claude.UseChrome {
		t.Fatal("Claude.UseChrome = false, want true")
	}
	if !cfg.Claude.UseTeammateMode {
		t.Fatal("Claude.UseTeammateMode = false, want true")
	}

	persistClaudeDialogDefaults(&session.ClaudeOptions{}, nil)
	cfg, err = session.LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig after clear: %v", err)
	}
	if cfg.Claude.ExtraArgs != nil {
		t.Fatalf("Claude.ExtraArgs should clear to nil, got %v", cfg.Claude.ExtraArgs)
	}
}

// Co-credit @masta-g3 (PR #674): TUI session creation must produce
// Tool="pi" rather than Tool="shell" with Command="pi", matching the
// tmux/userconfig wiring already present.
func TestCreateSessionTool_Pi(t *testing.T) {
	tool, command := createSessionTool("pi")
	if tool != "pi" || command != "pi" {
		t.Fatalf("createSessionTool(\"pi\") = (%q, %q), want (\"pi\", \"pi\")", tool, command)
	}
}

// TUI session creation must produce Tool="copilot" rather than
// Tool="shell" with Command="copilot", matching the tmux/userconfig
// wiring already present since v1.7.26.
func TestCreateSessionTool_Copilot(t *testing.T) {
	tool, command := createSessionTool("copilot")
	if tool != "copilot" || command != "copilot" {
		t.Fatalf("createSessionTool(\"copilot\") = (%q, %q), want (\"copilot\", \"copilot\")", tool, command)
	}
}

// TUI session creation must produce Tool="crush" rather than
// Tool="shell" with Command="crush", matching the tmux/userconfig
// wiring for the charmbracelet/crush integration (Issue #940).
func TestCreateSessionTool_Crush(t *testing.T) {
	tool, command := createSessionTool("crush")
	if tool != "crush" || command != "crush" {
		t.Fatalf("createSessionTool(\"crush\") = (%q, %q), want (\"crush\", \"crush\")", tool, command)
	}
}

// TUI session creation must produce Tool="hermes" rather than
// Tool="shell" with Command="hermes", matching the tmux/userconfig
// wiring for the Hermes Agent CLI integration.
func TestCreateSessionTool_Hermes(t *testing.T) {
	tool, command := createSessionTool("hermes")
	if tool != "hermes" || command != "hermes" {
		t.Fatalf("createSessionTool(\"hermes\") = (%q, %q), want (\"hermes\", \"hermes\")", tool, command)
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

// TestHomeUpdateStatusUpdateMsgBatchesKeyboardRestore is a regression guard for
// PR #613 (Bug 2 from issue #472). After the user detaches from a tmux attach,
// statusUpdateMsg's non-reload path must return a tea.Batch that includes the
// RestoreLegacyKeyboardCmd helper alongside tea.EnableMouseCellMotion. If a
// future refactor drops the keyboard-restore command, capitals silently break
// on Ghostty after the first tmux attach/detach cycle, and this test catches
// that regression.
func TestHomeUpdateStatusUpdateMsgBatchesKeyboardRestore(t *testing.T) {
	home := NewHome()

	_, cmd := home.Update(statusUpdateMsg{})
	if cmd == nil {
		t.Fatal("statusUpdateMsg returned nil cmd; keyboard-restore batch removed?")
	}

	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf(
			"expected statusUpdateMsg to return a tea.BatchMsg (mouse + keyboard restore); got %T. "+
				"RestoreLegacyKeyboardCmd was likely dropped from the handler, which will regress capitals on Ghostty after tmux detach.",
			msg,
		)
	}
	if len(batch) < 2 {
		t.Fatalf("expected batch of >= 2 commands (EnableMouseCellMotion + RestoreLegacyKeyboardCmd); got %d", len(batch))
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
	// The r-hotkey rename must lock the title so the #572 Claude-name sync
	// (e.g. an auto-assigned plan title) can't revert it on the next hook event.
	if !h.instances[0].TitleLocked {
		t.Error("TitleLocked = false after r-hotkey rename, want true")
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
	home.pendingTitleChanges[inst.ID] = pendingTitle{title: "renamed-title", locked: true}

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

func TestHomeRenamePendingChangeClearsAutoName(t *testing.T) {
	home := NewHome()
	home.width = 100
	home.height = 30

	// Create a quick-named session (AutoName=true with a machine-generated title)
	inst := session.NewInstance("quick-adjective-noun", "/tmp/project")
	inst.AutoName = true
	home.instancesMu.Lock()
	home.instances = []*session.Instance{inst}
	home.instanceByID[inst.ID] = inst
	home.instancesMu.Unlock()
	home.groupTree = session.NewGroupTree(home.instances)
	home.rebuildFlatItems()

	// User renamed the session; the rename was stored as a pending change
	// (save was skipped because isReloading=true at the time)
	home.pendingTitleChanges[inst.ID] = pendingTitle{title: "my-chosen-name", locked: true}

	// A reload replaces the instance with the stale disk version (AutoName=true, old title)
	reloadInst := session.NewInstance("quick-adjective-noun", "/tmp/project")
	reloadInst.ID = inst.ID
	reloadInst.AutoName = true

	reloadMsg := loadSessionsMsg{
		instances:    []*session.Instance{reloadInst},
		groups:       nil,
		restoreState: &reloadState{cursorSessionID: inst.ID},
	}

	model, _ := home.Update(reloadMsg)
	h := model.(*Home)

	// The user-chosen title must be re-applied
	if h.instances[0].Title != "my-chosen-name" {
		t.Errorf("Session title = %q, want %q", h.instances[0].Title, "my-chosen-name")
	}
	// AutoName must be cleared so the TUI shows the user-chosen name, not the live pane title
	if h.instances[0].AutoName {
		t.Error("AutoName = true after pending-rename re-apply, want false (user-chosen name should stick)")
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
	home.pendingTitleChanges[inst.ID] = pendingTitle{title: "desired-name", locked: true}

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

// TestHomeRenamePendingChangeRestoresTitleLock pins the #697 regression: a user
// rename re-applied after a reload race must come back LOCKED, otherwise the
// next #572 Claude-name sync reverts it to the cwd-folder default (the "my
// rename keeps disappearing on restart" bug). A sync-sourced pending title must
// stay UNLOCKED so it keeps tracking Claude's session name.
func TestHomeRenamePendingChangeRestoresTitleLock(t *testing.T) {
	// No RemoteSession case: pendingTitleChanges is keyed by session ID and
	// resolved through getInstanceByID, which only returns local
	// *session.Instance objects. A RemoteSession has no local instance and is
	// renamed via its own SSH-runner branch in handleGroupDialogKey, so it can
	// never reach this reload-reapply path.
	t.Run("user rename is relocked", func(t *testing.T) {
		home := NewHome()
		home.width = 100
		home.height = 30

		inst := session.NewInstance("original-name", "/tmp/project")
		inst.TitleLocked = true
		home.instancesMu.Lock()
		home.instances = []*session.Instance{inst}
		home.instanceByID[inst.ID] = inst
		home.instancesMu.Unlock()
		home.groupTree = session.NewGroupTree(home.instances)
		home.rebuildFlatItems()

		// User rename queued as locked (SetField sets TitleLocked=true).
		home.pendingTitleChanges[inst.ID] = pendingTitle{title: "renamed-title", locked: true}

		// Reload replaces the instance with a stale disk row: old title AND
		// UNLOCKED, because the lock was never persisted (save was skipped).
		reloadInst := session.NewInstance("original-name", "/tmp/project")
		reloadInst.ID = inst.ID
		reloadInst.TitleLocked = false

		model, _ := home.Update(loadSessionsMsg{
			instances:    []*session.Instance{reloadInst},
			restoreState: &reloadState{cursorSessionID: inst.ID},
		})
		h := model.(*Home)

		if h.instances[0].Title != "renamed-title" {
			t.Fatalf("Title = %q, want renamed-title", h.instances[0].Title)
		}
		if !h.instances[0].TitleLocked {
			t.Error("TitleLocked = false after reapply, want true (#697: else the next Claude-name sync reverts the rename)")
		}
	})

	t.Run("sync-sourced title stays unlocked", func(t *testing.T) {
		home := NewHome()
		home.width = 100
		home.height = 30

		inst := session.NewInstance("proj-ab", "/tmp/project")
		home.instancesMu.Lock()
		home.instances = []*session.Instance{inst}
		home.instanceByID[inst.ID] = inst
		home.instancesMu.Unlock()
		home.groupTree = session.NewGroupTree(home.instances)
		home.rebuildFlatItems()

		// An attach-time Claude-name sync queues an UNLOCKED title.
		home.pendingTitleChanges[inst.ID] = pendingTitle{title: "claude-name", locked: false}

		reloadInst := session.NewInstance("proj-ab", "/tmp/project")
		reloadInst.ID = inst.ID

		model, _ := home.Update(loadSessionsMsg{
			instances:    []*session.Instance{reloadInst},
			restoreState: &reloadState{cursorSessionID: inst.ID},
		})
		h := model.(*Home)

		if h.instances[0].Title != "claude-name" {
			t.Fatalf("Title = %q, want claude-name", h.instances[0].Title)
		}
		if h.instances[0].TitleLocked {
			t.Error("TitleLocked = true after sync reapply, want false (sync titles must keep tracking Claude)")
		}
	})
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
	searchEnabled := true
	config := session.GlobalSearchSettings{
		Enabled:        &searchEnabled,
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
	searchEnabled := true
	config := session.GlobalSearchSettings{
		Enabled:        &searchEnabled,
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

	homeDir := setXDGTestHome(t)

	if enabled != nil {
		value := "false"
		if *enabled {
			value = "true"
		}
		content := fmt.Sprintf("[instances]\nfollow_cwd_on_attach = %s\n", value)
		writeXDGTestConfig(t, homeDir, content)
	}
}

func setPreviewShowNotesConfigForTest(t *testing.T, enabled *bool) {
	t.Helper()

	homeDir := setXDGTestHome(t)

	if enabled != nil {
		value := "false"
		if *enabled {
			value = "true"
		}
		content := fmt.Sprintf("[preview]\nshow_notes = %s\n", value)
		writeXDGTestConfig(t, homeDir, content)
	}
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

func TestRemoteSelectionNOpensRemoteAwareNewDialog(t *testing.T) {
	home := NewHome()
	home.width = 100
	home.height = 30

	remote := session.RemoteSessionInfo{
		ID:         "remote-123",
		Title:      "remote-session",
		RemoteName: "myserver",
		Path:       "/srv/project",
		Group:      "work",
	}
	home.flatItems = []session.Item{{Type: session.ItemTypeRemoteSession, RemoteSession: &remote, RemoteName: "myserver"}}
	home.cursor = 0

	model, cmd := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	h, ok := model.(*Home)
	if !ok {
		t.Fatal("handleMainKey should return *Home")
	}
	if cmd != nil {
		t.Fatal("pressing n on a remote session should open the dialog, not quick-create")
	}
	if !h.newDialog.IsVisible() {
		t.Fatal("pressing n on a remote session should open the new-session dialog")
	}
	if h.pendingRemoteName != "myserver" {
		t.Fatalf("pendingRemoteName = %q, want myserver", h.pendingRemoteName)
	}
	if got := h.newDialog.GetSelectedGroup(); got != "work" {
		t.Fatalf("selected group = %q, want work", got)
	}
	_, gotPath, _ := h.newDialog.GetRemoteValues()
	if gotPath != "/srv/project" {
		t.Fatalf("dialog path = %q, want /srv/project", gotPath)
	}
}

func TestRemoteNewDialogCustomizationPreservesRemoteValues(t *testing.T) {
	home := NewHome()
	home.width = 100
	home.height = 30

	remote := session.RemoteSessionInfo{
		ID:         "remote-123",
		Title:      "remote-session",
		RemoteName: "myserver",
		Path:       "/srv/project",
		Group:      "work",
	}
	home.flatItems = []session.Item{{Type: session.ItemTypeRemoteSession, RemoteSession: &remote, RemoteName: "myserver"}}
	home.cursor = 0

	model, _ := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	h := model.(*Home)
	h.newDialog.nameInput.SetValue("custom remote")
	h.newDialog.pathInput.SetValue("~/custom-project")
	h.newDialog.SetDefaultTool("codex")

	name, path, command := h.newDialog.GetRemoteValues()
	if name != "custom remote" {
		t.Fatalf("name = %q, want custom remote", name)
	}
	if path != "~/custom-project" {
		t.Fatalf("path = %q, want ~/custom-project", path)
	}
	if command != "codex" {
		t.Fatalf("command = %q, want codex", command)
	}
	group := h.newDialog.GetSelectedGroup()
	if group != "work" {
		t.Fatalf("group = %q, want work", group)
	}

}

func TestSelectedRemotePreviewTarget(t *testing.T) {
	home := NewHome()
	home.width = 100
	home.height = 30

	remote := session.RemoteSessionInfo{ID: "remote-123", Title: "remote-session", RemoteName: "myserver"}
	home.flatItems = []session.Item{{Type: session.ItemTypeRemoteSession, RemoteSession: &remote, RemoteName: "myserver"}}
	home.cursor = 0

	remoteName, sessionID, previewKey, ok := home.selectedRemotePreviewTarget()
	if !ok {
		t.Fatal("selectedRemotePreviewTarget should resolve remote selection")
	}
	if remoteName != "myserver" {
		t.Fatalf("remoteName = %q, want %q", remoteName, "myserver")
	}
	if sessionID != "remote-123" {
		t.Fatalf("sessionID = %q, want %q", sessionID, "remote-123")
	}
	if previewKey != "remote:myserver:remote-123" {
		t.Fatalf("previewKey = %q, want %q", previewKey, "remote:myserver:remote-123")
	}
}

func TestRemoteSelectionQuickCreateStillRunsRemoteCommand(t *testing.T) {
	home := NewHome()
	home.width = 100
	home.height = 30

	remote := session.RemoteSessionInfo{ID: "remote-123", Title: "remote-session", RemoteName: "myserver"}
	home.flatItems = []session.Item{{Type: session.ItemTypeRemoteSession, RemoteSession: &remote, RemoteName: "myserver"}}
	home.cursor = 0

	_, cmd := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'N'}})
	if cmd == nil {
		t.Fatal("pressing N on remote selection should return remote create command")
	}

	msg := cmd()
	createMsg, ok := msg.(sessionCreatedMsg)
	if !ok {
		t.Fatalf("command returned %T, want sessionCreatedMsg", msg)
	}
	if createMsg.err == nil {
		t.Fatal("expected error when remote config is unavailable")
	}
}

func TestRenderRemotePreviewIncludesCachedResponse(t *testing.T) {
	home := NewHome()
	home.width = 100
	home.height = 30

	remote := session.RemoteSessionInfo{
		ID:     "remote-123",
		Title:  "remote-session",
		Status: "waiting",
		Path:   "/srv/project",
	}
	item := session.Item{Type: session.ItemTypeRemoteSession, RemoteSession: &remote, RemoteName: "myserver"}

	home.previewCache[remotePreviewCacheKey("myserver", "remote-123")] = "Remote answer"

	rendered := home.renderRemotePreview(item, 80, 20)
	if !strings.Contains(rendered, "Last response") {
		t.Fatalf("rendered preview should include last response header, got: %q", rendered)
	}
	if !strings.Contains(rendered, "Remote answer") {
		t.Fatalf("rendered preview should include cached remote response, got: %q", rendered)
	}
}

// Remote group `n` behavior is covered by
// TestRegression743_NOnRemoteGroup_OpensRemoteDialog.

func TestRenderRemotePreviewShowsEmptyStateAfterFetch(t *testing.T) {
	home := NewHome()
	home.width = 100
	home.height = 30

	remote := session.RemoteSessionInfo{
		ID:     "remote-123",
		Title:  "remote-session",
		Status: "waiting",
		Path:   "/srv/project",
	}
	item := session.Item{Type: session.ItemTypeRemoteSession, RemoteSession: &remote, RemoteName: "myserver"}
	key := remotePreviewCacheKey("myserver", "remote-123")

	home.previewCache[key] = ""
	home.previewCacheTime[key] = time.Now()

	rendered := home.renderRemotePreview(item, 80, 20)
	if !strings.Contains(rendered, "No response available yet.") {
		t.Fatalf("rendered preview should show empty-state copy after a fetch, got: %q", rendered)
	}
	if strings.Contains(rendered, "Fetching remote preview...") {
		t.Fatalf("rendered preview should not keep showing the loading state after an empty fetch, got: %q", rendered)
	}
}

func TestRenderRemotePreviewTruncatesCachedResponseLines(t *testing.T) {
	home := NewHome()
	home.width = 100
	home.height = 30

	remote := session.RemoteSessionInfo{
		ID:     "remote-123",
		Title:  "remote-session",
		Status: "running",
		Path:   "/srv/project",
	}
	item := session.Item{Type: session.ItemTypeRemoteSession, RemoteSession: &remote, RemoteName: "myserver"}

	lines := make([]string, 250)
	for i := range lines {
		lines[i] = fmt.Sprintf("line-%03d", i)
	}
	home.previewCache[remotePreviewCacheKey("myserver", "remote-123")] = strings.Join(lines, "\n")

	rendered := home.renderRemotePreview(item, 80, 20)
	if strings.Contains(rendered, "line-049") {
		t.Fatalf("rendered preview should drop lines outside the retained tail, got: %q", rendered)
	}
	if !strings.Contains(rendered, "line-050") || !strings.Contains(rendered, "line-249") {
		t.Fatalf("rendered preview should retain the last 200 lines, got: %q", rendered)
	}
}

func TestRenderRemotePreviewTruncatesCachedResponseBytes(t *testing.T) {
	home := NewHome()
	home.width = 100
	home.height = 30

	remote := session.RemoteSessionInfo{
		ID:     "remote-123",
		Title:  "remote-session",
		Status: "running",
		Path:   "/srv/project",
	}
	item := session.Item{Type: session.ItemTypeRemoteSession, RemoteSession: &remote, RemoteName: "myserver"}

	prefix := "TRUNCATE-ME"
	tail := "KEEP-TAIL"
	content := prefix + strings.Repeat("x", 20*1024) + tail
	home.previewCache[remotePreviewCacheKey("myserver", "remote-123")] = content

	rendered := home.renderRemotePreview(item, 80, 20)
	if strings.Contains(rendered, prefix) {
		t.Fatalf("rendered preview should drop content beyond the byte cap, got: %q", rendered)
	}
	if !strings.Contains(rendered, tail) {
		t.Fatalf("rendered preview should keep the most recent content, got: %q", rendered)
	}
}

func TestPreviewFetchedMsgUpdatesCacheTimeOnError(t *testing.T) {
	home := NewHome()
	key := remotePreviewCacheKey("myserver", "remote-123")
	home.previewFetchingID = key
	before := time.Now()

	model, _ := home.Update(previewFetchedMsg{previewKey: key, err: fmt.Errorf("fetch failed")})
	updated := model.(*Home)

	if updated.previewFetchingID != "" {
		t.Fatal("previewFetchingID should be cleared after fetch completion")
	}
	cacheTime, ok := updated.previewCacheTime[key]
	if !ok {
		t.Fatal("preview cache time should be recorded even when fetch fails")
	}
	if cacheTime.Before(before) {
		t.Fatalf("preview cache time %v should be at or after %v", cacheTime, before)
	}
	if _, ok := updated.previewCache[key]; ok {
		t.Fatal("preview content should not be cached when fetch fails")
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
	home.footerMode = session.FooterFull // verbose width-adaptive tiers

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
	home.footerMode = session.FooterFull // verbose width-adaptive tiers

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

func TestRenderHelpBarMinimalWithFreshRestartableSession(t *testing.T) {
	home := NewHome()
	home.width = 55
	home.height = 30
	home.footerMode = session.FooterFull // verbose width-adaptive tiers

	testSession := &session.Instance{
		ID:              "test-456",
		Title:           "Fresh Restart Session",
		Tool:            "claude",
		ClaudeSessionID: "session-xyz",
	}
	home.flatItems = []session.Item{{Type: session.ItemTypeSession, Session: testSession}}
	home.cursor = 0

	result := home.renderHelpBar()

	if !strings.Contains(result, "T") {
		t.Error("Minimal help bar should contain T key for fresh restart")
	}
}

func TestRenderHelpBarCompact(t *testing.T) {
	home := NewHome()
	home.width = 85 // Compact mode (70-99)
	home.height = 30
	home.footerMode = session.FooterFull // verbose width-adaptive tiers

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
	home.footerMode = session.FooterFull // verbose width-adaptive tiers

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
	home.footerMode = session.FooterFull // verbose width-adaptive tiers

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
	home.footerMode = session.FooterFull // Undo lives in the verbose bar's secondary hints

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

// ── Curated footer (opt-in via [ui] footer = curated) ──────────────────────

// curatedHome builds a Home in the curated footer style at a comfortable width.
func curatedHome() *Home {
	home := NewHome()
	home.width = 120
	home.height = 30
	home.footerMode = session.FooterCurated
	return home
}

func TestCuratedFooterAlwaysEndsWithSettingsThenHelp(t *testing.T) {
	home := curatedHome()
	// Live session selected so there is a context hint before settings/help.
	home.flatItems = []session.Item{
		{Type: session.ItemTypeSession, Session: &session.Instance{ID: "s1", Tool: "claude", Status: session.StatusRunning}},
	}
	home.cursor = 0

	result := home.renderHelpBar()

	settingsKey := home.actionKey(hotkeySettings)
	helpKey := home.actionKey(hotkeyHelp)
	si := strings.LastIndex(result, settingsKey+" ")
	hi := strings.LastIndex(result, helpKey+" ")
	if si == -1 {
		t.Fatalf("curated footer should advertise settings key %q\nGot: %q", settingsKey, result)
	}
	if hi == -1 {
		t.Fatalf("curated footer should advertise help key %q\nGot: %q", helpKey, result)
	}
	if !(si < hi) {
		t.Errorf("settings must come before help as the last two items\nGot: %q", result)
	}
	if !strings.Contains(result, "attach") {
		t.Errorf("curated footer for a live session should show attach\nGot: %q", result)
	}
}

func TestCuratedFooterLiveSessionShowsAttach(t *testing.T) {
	home := curatedHome()
	home.flatItems = []session.Item{
		{Type: session.ItemTypeSession, Session: &session.Instance{ID: "s1", Tool: "claude", Status: session.StatusRunning}},
	}
	home.cursor = 0

	result := home.renderHelpBar()
	if !strings.Contains(result, "⏎ attach") {
		t.Errorf("live session should advertise Enter attach\nGot: %q", result)
	}
	// Live session shows up to 3 context hints (attach first, then restart, …).
	if !strings.Contains(result, "restart") {
		t.Errorf("live session should advertise restart after attach\nGot: %q", result)
	}
	// Attach must be the first context hint.
	ai := strings.Index(result, "attach")
	ri := strings.Index(result, "restart")
	if ai == -1 || ri == -1 || ai > ri {
		t.Errorf("attach should come before restart\nGot: %q", result)
	}
}

func TestCuratedFooterCapsContextHints(t *testing.T) {
	home := curatedHome()
	// A forkable live session would offer attach/restart/fork; verify the
	// curated footer caps context hints at maxCuratedContextHints and never
	// exceeds it once settings/help are appended.
	home.flatItems = []session.Item{
		{Type: session.ItemTypeSession, Session: &session.Instance{
			ID:               "s1",
			Tool:             "claude",
			Status:           session.StatusRunning,
			ClaudeSessionID:  "abc",
			ClaudeDetectedAt: time.Now(), // recent → CanFork() true
		}},
	}
	home.cursor = 0

	hints := home.curatedContextHints(home.flatItems[0])
	if len(hints) != maxCuratedContextHints {
		t.Fatalf("forkable live session should yield %d context hints, got %d: %+v",
			maxCuratedContextHints, len(hints), hints)
	}
	result := home.renderHelpBar()
	if !strings.Contains(result, "fork") {
		t.Errorf("forkable live session should advertise fork\nGot: %q", result)
	}
}

func TestCuratedFooterDeadSessionShowsRestart(t *testing.T) {
	home := curatedHome()
	// Stopped Claude session with a known session id → restart + restart fresh.
	home.flatItems = []session.Item{
		{Type: session.ItemTypeSession, Session: &session.Instance{
			ID:              "s1",
			Tool:            "claude",
			Status:          session.StatusStopped,
			ClaudeSessionID: "abc",
		}},
	}
	home.cursor = 0

	result := home.renderHelpBar()
	if !strings.Contains(result, "restart") {
		t.Errorf("dead session should advertise restart\nGot: %q", result)
	}
	if !strings.Contains(result, "restart fresh") {
		t.Errorf("dead restartable session should advertise restart fresh\nGot: %q", result)
	}
	if !strings.Contains(result, "delete") {
		t.Errorf("dead session should advertise delete as the third action\nGot: %q", result)
	}
	if strings.Contains(result, "attach") {
		t.Errorf("dead session should not advertise attach\nGot: %q", result)
	}
}

func TestCuratedFooterErrorSessionShowsRestartRestartFreshDelete(t *testing.T) {
	home := curatedHome()
	// Errored Claude session with a session id → R / T / d, in that order.
	home.flatItems = []session.Item{
		{Type: session.ItemTypeSession, Session: &session.Instance{
			ID:              "s1",
			Tool:            "claude",
			Status:          session.StatusError,
			ClaudeSessionID: "abc",
		}},
	}
	home.cursor = 0

	hints := home.curatedContextHints(home.flatItems[0])
	got := make([]string, len(hints))
	for i, hint := range hints {
		got[i] = hint.label
	}
	want := []string{"restart", "restart fresh", "delete"}
	if len(got) != len(want) {
		t.Fatalf("errored session context hints = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("errored session context hints = %v, want %v", got, want)
		}
	}
}

func TestCuratedFooterErrorSessionShowsRestart(t *testing.T) {
	home := curatedHome()
	home.flatItems = []session.Item{
		{Type: session.ItemTypeSession, Session: &session.Instance{ID: "s1", Tool: "other", Status: session.StatusError}},
	}
	home.cursor = 0

	result := home.renderHelpBar()
	if !strings.Contains(result, "restart") {
		t.Errorf("errored session should advertise restart\nGot: %q", result)
	}
}

func TestCuratedFooterGroupShowsCollapseExpand(t *testing.T) {
	home := curatedHome()

	// Expanded group → collapse hint.
	home.flatItems = []session.Item{
		{Type: session.ItemTypeGroup, Path: "g", Group: &session.Group{Name: "g", Path: "g", Expanded: true}},
	}
	home.cursor = 0
	if result := home.renderHelpBar(); !strings.Contains(result, "Tab collapse") {
		t.Errorf("expanded group should advertise Tab collapse\nGot: %q", result)
	}

	// Collapsed group → expand hint.
	home.flatItems = []session.Item{
		{Type: session.ItemTypeGroup, Path: "g", Group: &session.Group{Name: "g", Path: "g", Expanded: false}},
	}
	if result := home.renderHelpBar(); !strings.Contains(result, "Tab expand") {
		t.Errorf("collapsed group should advertise Tab expand\nGot: %q", result)
	}
}

func TestCuratedFooterEmptyListShowsNew(t *testing.T) {
	home := curatedHome()
	home.flatItems = nil

	result := home.renderHelpBar()
	if !strings.Contains(result, "new") {
		t.Errorf("empty list should advertise new\nGot: %q", result)
	}
	// settings + help must still be present and last.
	if !strings.Contains(result, "help") {
		t.Errorf("curated footer should always include help\nGot: %q", result)
	}
}

func TestCuratedFooterJumpMode(t *testing.T) {
	home := curatedHome()
	home.jumpMode = true

	result := home.renderHelpBar()
	if !strings.Contains(result, "jump") || !strings.Contains(result, "cancel") {
		t.Errorf("jump mode should advertise jump and cancel\nGot: %q", result)
	}
}

// TestCuratedFooterRemoteSessionShowsAttach covers PR #1289 review nit 1: a
// remote-session row must still advertise the attach hint in the curated
// footer (it attaches over SSH just like a local session).
func TestCuratedFooterRemoteSessionShowsAttach(t *testing.T) {
	home := curatedHome()
	home.flatItems = []session.Item{
		{Type: session.ItemTypeRemoteSession, RemoteSession: &session.RemoteSessionInfo{}},
	}
	home.cursor = 0

	result := home.renderHelpBar()
	if !strings.Contains(result, "⏎ attach") {
		t.Errorf("curated footer for a remote session should advertise Enter attach\nGot: %q", result)
	}
}

// TestCuratedFooterQueuedSessionNotAttachable covers PR #1289 review nit 2b: a
// queued session has no tmux yet, so the curated footer must not advertise
// "⏎ attach" or "restart" for it — only delete and new make sense.
func TestCuratedFooterQueuedSessionNotAttachable(t *testing.T) {
	home := curatedHome()
	home.flatItems = []session.Item{
		{Type: session.ItemTypeSession, Session: &session.Instance{ID: "s1", Tool: "claude", Status: session.StatusQueued}},
	}
	home.cursor = 0

	result := home.renderHelpBar()
	if strings.Contains(result, "attach") {
		t.Errorf("queued session (no tmux yet) must not advertise attach\nGot: %q", result)
	}
	if strings.Contains(result, "restart") {
		t.Errorf("queued session (nothing running) must not advertise restart\nGot: %q", result)
	}
	if !strings.Contains(result, "delete") {
		t.Errorf("queued session should advertise delete\nGot: %q", result)
	}
}

// TestCuratedFooterNarrowKeepsSettingsAndHelp covers PR #1289 review nit 2a: on
// a narrow terminal the curated footer drops lower-priority CONTEXT hints by
// width rather than letting MaxWidth truncate the always-last settings/help
// global hints off the right edge.
func TestCuratedFooterNarrowKeepsSettingsAndHelp(t *testing.T) {
	home := curatedHome()
	home.width = 50 // narrow, but still >= layoutBreakpointSingle (curated, not tiny)
	// Forkable live session → three context hints (attach/restart/fork) that
	// would, together with settings+help, overflow 50 columns.
	home.flatItems = []session.Item{
		{Type: session.ItemTypeSession, Session: &session.Instance{
			ID:               "s1",
			Tool:             "claude",
			Status:           session.StatusRunning,
			ClaudeSessionID:  "abc",
			ClaudeDetectedAt: time.Now(),
		}},
	}
	home.cursor = 0

	result := home.renderHelpBar()
	settingsKey := home.actionKey(hotkeySettings)
	helpKey := home.actionKey(hotkeyHelp)
	if !strings.Contains(result, settingsKey+" settings") {
		t.Errorf("narrow curated footer must keep the settings hint\nGot: %q", result)
	}
	if !strings.Contains(result, helpKey+" help") {
		t.Errorf("narrow curated footer must keep the help hint\nGot: %q", result)
	}
}

// TestFitCuratedHintsAlwaysKeepsGlobals exercises the width-fitting helper
// directly: even when the bar is too narrow for any context hint, the global
// hints survive; and context hints are kept front-to-back (highest priority
// first) when there is room.
func TestFitCuratedHintsAlwaysKeepsGlobals(t *testing.T) {
	home := curatedHome()
	globals := []footerHint{{key: "s", label: "settings"}, {key: "?", label: "help"}}
	ctx := []footerHint{{key: "a", label: "attach"}, {key: "r", label: "restart"}, {key: "f", label: "fork"}}

	// Width too small for any context hint: only globals remain.
	home.width = 1
	got := home.fitCuratedHints(ctx, globals)
	if len(got) != len(globals) {
		t.Fatalf("at width=1 expected only the %d global hints, got %d: %+v", len(globals), len(got), got)
	}
	if got[len(got)-1].label != "help" {
		t.Errorf("help must remain the last hint, got %+v", got)
	}

	// Generous width: everything fits, context first then globals.
	home.width = 200
	got = home.fitCuratedHints(ctx, globals)
	if len(got) != len(ctx)+len(globals) {
		t.Fatalf("at width=200 expected all %d hints, got %d: %+v", len(ctx)+len(globals), len(got), got)
	}
	if got[0].label != "attach" || got[len(got)-1].label != "help" {
		t.Errorf("expected attach first and help last, got %+v", got)
	}
}

// TestFooterDefaultIsFull is the default-preserving guarantee for PR #1289:
// with no [ui] footer config the footer is the historic verbose "full" bar,
// not curated — so nobody's UI changes without an explicit opt-in.
func TestFooterDefaultIsFull(t *testing.T) {
	// Config layer: unset → full.
	if got := (session.UISettings{}).GetFooter(); got != session.FooterFull {
		t.Fatalf("default footer = %q, want %q (must preserve today's verbose bar)", got, session.FooterFull)
	}

	// Render layer: a Home seeded with the default footerMode must route to the
	// verbose width-adaptive bar, not the curated bar. The verbose bar uses
	// filled key chips and advertises many actions; a reliable distinguishing
	// marker is the "quit" hint, which the curated bar never shows.
	home := NewHome()
	home.width = 120
	home.height = 30
	home.footerMode = (session.UISettings{}).GetFooter() // simulate default load
	home.flatItems = []session.Item{
		{Type: session.ItemTypeSession, Session: &session.Instance{ID: "s1", Tool: "claude", Status: session.StatusRunning}},
	}
	home.cursor = 0

	full := home.renderHelpBar()

	home.footerMode = session.FooterCurated
	curated := home.renderHelpBar()

	if full == curated {
		t.Fatal("default (full) footer render is identical to curated render; default did not select the verbose bar")
	}
}

// TestFooterUnsetOrUnknownModeRendersHistoricBar guards the renderHelpBar
// routing directly: a truly-unset ("") or unrecognized footerMode must render
// the HISTORIC width-adaptive bar, NOT the curated bar. Only an explicit
// curated setting selects curated. This covers the path that
// TestFooterDefaultIsFull masks by hard-setting the normalized GetFooter value;
// here we set footerMode to raw values that bypass GetFooter normalization.
func TestFooterUnsetOrUnknownModeRendersHistoricBar(t *testing.T) {
	mkHome := func(mode string) *Home {
		home := NewHome()
		home.width = 120
		home.height = 30
		home.footerMode = mode
		home.flatItems = []session.Item{
			{Type: session.ItemTypeSession, Session: &session.Instance{ID: "s1", Tool: "claude", Status: session.StatusRunning}},
		}
		home.cursor = 0
		return home
	}

	// The verbose width-adaptive bar (full tier at width 120) renders a
	// capitalized context title ("Session:") that the curated bar never emits;
	// the curated bar uses lowercase inline labels ("attach", "settings"). Use
	// the "Session:" prefix as the distinguishing marker.
	const historicMarker = "Session:"
	curated := mkHome(session.FooterCurated).renderHelpBar()
	if strings.Contains(curated, historicMarker) {
		t.Fatalf("precondition failed: curated bar unexpectedly contains %q; marker invalid", historicMarker)
	}

	for _, mode := range []string{"", "bogus", "FULL-ish", "default"} {
		got := mkHome(mode).renderHelpBar()
		if got == curated {
			t.Errorf("footerMode=%q rendered the curated bar; unset/unknown must route to the historic bar", mode)
		}
		if !strings.Contains(got, historicMarker) {
			t.Errorf("footerMode=%q did not render the historic verbose bar (no %q marker)", mode, historicMarker)
		}
	}
}

// TestRenderHelpBarCursorNegativeNoPanic guards against a negative-index panic:
// with items present but cursor == -1 (e.g. a transient state before cursor is
// restored), the help-bar renderers must not index flatItems[-1]. Exercises
// every footer mode that has a cursor-indexed context branch.
func TestRenderHelpBarCursorNegativeNoPanic(t *testing.T) {
	modes := []string{
		session.FooterCurated,
		session.FooterFull,
		session.FooterCompact,
		session.FooterMinimal,
		"", // unset → historic width-adaptive
	}
	for _, mode := range modes {
		home := NewHome()
		home.width = 120
		home.height = 30
		home.footerMode = mode
		home.flatItems = []session.Item{
			{Type: session.ItemTypeSession, Session: &session.Instance{ID: "s1", Tool: "claude", Status: session.StatusRunning}},
		}
		home.cursor = -1 // items present, but no valid selection

		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("footerMode=%q: renderHelpBar panicked with cursor=-1: %v", mode, r)
				}
			}()
			_ = home.renderHelpBar()
		}()
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
				// v1.7.59: the update banner now renders via ShouldNudge,
				// which requires ReleasesBehind > NudgeThreshold. Any
				// value >5 flips the same banner path this test measured.
				home.updateInfo = &update.UpdateInfo{
					Available: true, CurrentVersion: "1.0", LatestVersion: "2.0",
					ReleasesBehind: 30,
				}
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

func TestRestartSessionFreshCmdSessionMissingReturnsError(t *testing.T) {
	home := NewHome()
	inst := session.NewInstance("restart-fresh-test", "/tmp/project")

	cmd := home.restartSessionFresh(inst)
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
	// Default matches upstream's original hardcoded behavior so existing
	// users see no change unless they opt into a narrower exclude-set.
	defaultExcludes := map[session.Status]bool{
		session.StatusError:   true,
		session.StatusStopped: true,
	}
	errorOnly := map[session.Status]bool{session.StatusError: true}
	excludeNothing := map[session.Status]bool{}

	tests := []struct {
		name     string
		filter   session.Status
		status   session.Status
		excludes map[session.Status]bool
		want     bool
	}{
		// Default exclude-set ({error, stopped}): % hides both, matching
		// upstream's prior hardcoded behavior exactly.
		{"default-running", FilterModeActive, session.StatusRunning, defaultExcludes, true},
		{"default-waiting", FilterModeActive, session.StatusWaiting, defaultExcludes, true},
		{"default-idle", FilterModeActive, session.StatusIdle, defaultExcludes, true},
		{"default-starting", FilterModeActive, session.StatusStarting, defaultExcludes, true},
		{"default-error-hidden", FilterModeActive, session.StatusError, defaultExcludes, false},
		{"default-stopped-hidden", FilterModeActive, session.StatusStopped, defaultExcludes, false},

		// Opt-in via active_filter_excludes = ["error"]: closed/stopped
		// sessions remain visible — the regression fix for users who
		// found the upstream default too aggressive.
		{"erronly-stopped-visible", FilterModeActive, session.StatusStopped, errorOnly, true},
		{"erronly-error-hidden", FilterModeActive, session.StatusError, errorOnly, false},
		{"erronly-running-visible", FilterModeActive, session.StatusRunning, errorOnly, true},

		// Empty exclude-set: % filter shows everything (degenerate but valid).
		{"empty-error-visible", FilterModeActive, session.StatusError, excludeNothing, true},
		{"empty-stopped-visible", FilterModeActive, session.StatusStopped, excludeNothing, true},

		// Concrete status filters ignore the exclude-set entirely.
		{"concrete-running-match", session.StatusRunning, session.StatusRunning, defaultExcludes, true},
		{"concrete-running-no-match", session.StatusRunning, session.StatusWaiting, defaultExcludes, false},
		{"concrete-error-match", session.StatusError, session.StatusError, defaultExcludes, true},
		{"concrete-error-no-stopped", session.StatusError, session.StatusStopped, defaultExcludes, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &Home{activeFilterExcludes: tt.excludes}
			got := h.matchesStatusFilter(tt.filter, tt.status)
			if got != tt.want {
				t.Errorf("matchesStatusFilter(%q, %q, %v) = %v, want %v",
					tt.filter, tt.status, tt.excludes, got, tt.want)
			}
		})
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

// RemoteSession N/A for collapsed-group headers: archive partitioning applies to
// local ItemTypeSession rows; remote rows are excluded (see
// TestRebuildFlatItemsArchivedViewOmitsRemoteSessions).
func TestRebuildFlatItemsCollapsedGroupKeepsHeaderWithArchivedSessions(t *testing.T) {
	h := &Home{}
	instances := []*session.Instance{
		session.NewInstanceWithGroup("active", "/tmp/a", "work"),
		session.NewInstanceWithGroup("archived", "/tmp/b", "work"),
	}
	instances[1].ArchivedAt = time.Now().UTC()

	h.groupTree = session.NewGroupTree(instances)
	h.groupTree.CollapseGroup("work")
	h.windowsCollapsed = make(map[string]bool)

	h.rebuildFlatItems()

	if len(h.flatItems) != 1 {
		t.Fatalf("collapsed group with active sessions: got %d flat items, want 1 group header", len(h.flatItems))
	}
	if h.flatItems[0].Type != session.ItemTypeGroup || h.flatItems[0].Path != "work" {
		t.Fatalf("expected collapsed work group header, got %+v", h.flatItems[0])
	}
}

func TestRebuildFlatItemsCollapsedGroupKeepsHeaderInArchivedView(t *testing.T) {
	h := NewHome()
	h.statusFilter = FilterModeArchived

	instances := []*session.Instance{
		session.NewInstanceWithGroup("active", "/tmp/a", "work"),
		session.NewInstanceWithGroup("archived", "/tmp/b", "work"),
	}
	instances[1].ArchivedAt = time.Now().UTC()
	h.instancesMu.Lock()
	h.instances = instances
	h.instanceByID[instances[0].ID] = instances[0]
	h.instanceByID[instances[1].ID] = instances[1]
	h.instancesMu.Unlock()

	h.groupTree = session.NewGroupTree(instances)
	h.groupTree.CollapseGroup("work")
	h.windowsCollapsed = make(map[string]bool)

	h.rebuildFlatItems()
	if h.statusFilter != FilterModeArchived {
		t.Fatalf("statusFilter = %q, want %q", h.statusFilter, FilterModeArchived)
	}

	if len(h.flatItems) != 1 {
		t.Fatalf("archived view + collapsed group: got %d flat items, want 1 group header", len(h.flatItems))
	}
	if h.flatItems[0].Type != session.ItemTypeGroup || h.flatItems[0].Path != "work" {
		t.Fatalf("expected group header in archived view, got %+v", h.flatItems[0])
	}
}

func TestRebuildFlatItemsArchivedViewOmitsRemoteSessions(t *testing.T) {
	h := NewHome()
	h.statusFilter = FilterModeArchived

	inst := session.NewInstanceWithGroup("archived", "/tmp/b", "work")
	inst.ArchivedAt = time.Now().UTC()
	h.instancesMu.Lock()
	h.instances = []*session.Instance{inst}
	h.instanceByID[inst.ID] = inst
	h.instancesMu.Unlock()

	h.groupTree = session.NewGroupTree(h.instances)
	h.windowsCollapsed = make(map[string]bool)

	h.remoteSessionsMu.Lock()
	h.remoteSessions = map[string][]session.RemoteSessionInfo{
		"dev": {{ID: "remote-1", Title: "remote-session", RemoteName: "dev"}},
	}
	h.remoteSessionsMu.Unlock()

	h.rebuildFlatItems()

	for _, item := range h.flatItems {
		if item.Type == session.ItemTypeRemoteGroup || item.Type == session.ItemTypeRemoteSession {
			t.Fatalf("archived view should omit remote rows, got %+v", item)
		}
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

func TestStatusUpdateMsg_PreservesSelectedSessionAcrossRebuild(t *testing.T) {
	h := newAttachReturnTestHome()
	s1 := session.NewInstanceWithGroup("first", "/tmp/first", "work")
	s1.ID = "s1"
	s2 := session.NewInstanceWithGroup("second", "/tmp/second", "work")
	s2.ID = "s2"
	setAttachReturnTestInstances(h, []*session.Instance{s1, s2})

	h.groupTree = session.NewGroupTree([]*session.Instance{s2, s1})

	model, _ := h.Update(statusUpdateMsg{})
	home := model.(*Home)

	if got := selectedSessionID(home); got != s2.ID {
		t.Fatalf("selected session = %q, want %q", got, s2.ID)
	}
}

func TestStatusUpdateMsg_ReconcilesAttachedSessionBeforeRender(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	h := newAttachReturnTestHome()
	inst := session.NewInstanceWithGroupAndTool("exited", "/tmp/exited", "work", "codex")
	inst.ID = "exited-session"
	inst.CreatedAt = time.Now().Add(-2 * time.Second)
	inst.Status = session.StatusRunning
	setAttachReturnTestInstances(h, []*session.Instance{inst})

	hooksDir := session.GetHooksDir()
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	hookPath := filepath.Join(hooksDir, inst.ID+".json")
	hookBody := fmt.Sprintf(
		`{"status":"running","session_id":"stale-session","event":"UserPromptSubmit","ts":%d}`,
		time.Now().Unix(),
	)
	if err := os.WriteFile(hookPath, []byte(hookBody), 0o644); err != nil {
		t.Fatalf("write stale hook: %v", err)
	}

	model, _ := h.Update(statusUpdateMsg{attachedSessionID: inst.ID})
	home := model.(*Home)

	if got := inst.GetStatusThreadSafe(); got != session.StatusError {
		t.Fatalf("attached session status = %q, want %q", got, session.StatusError)
	}
	if got := home.getSessionRenderState(inst).status; got != session.StatusError {
		t.Fatalf("render snapshot status = %q, want %q", got, session.StatusError)
	}
	if _, err := os.Stat(hookPath); !os.IsNotExist(err) {
		t.Fatalf("stale hook file still exists or stat failed with unexpected error: %v", err)
	}
}

func TestStatusUpdateMsg_FollowsNotificationSwitchSession(t *testing.T) {
	h := newAttachReturnTestHome()
	s1 := session.NewInstanceWithGroup("first", "/tmp/first", "work")
	s1.ID = "s1"
	s2 := session.NewInstanceWithGroup("second", "/tmp/second", "work")
	s2.ID = "s2"
	setAttachReturnTestInstances(h, []*session.Instance{s1, s2})

	h.lastNotifSwitchID = s1.ID
	h.groupTree = session.NewGroupTree([]*session.Instance{s2, s1})

	model, _ := h.Update(statusUpdateMsg{})
	home := model.(*Home)

	if got := selectedSessionID(home); got != s1.ID {
		t.Fatalf("selected session = %q, want switched session %q", got, s1.ID)
	}
	if home.lastNotifSwitchID != "" {
		t.Fatalf("lastNotifSwitchID = %q, want cleared", home.lastNotifSwitchID)
	}
}

func TestAttachReturnGraceSuppressesPreviewRefresh(t *testing.T) {
	h := NewHome()
	now := time.Now()
	h.beginAttachReturnGrace(now)

	if !h.shouldSuppressPreviewRefresh(now.Add(attachReturnPreviewGrace / 2)) {
		t.Fatal("expected preview refresh suppression during attach-return grace period")
	}
	if h.shouldSuppressPreviewRefresh(now.Add(attachReturnPreviewGrace + 100*time.Millisecond)) {
		t.Fatal("expected preview refresh suppression to expire after grace period")
	}
	if hotUntil := time.Unix(0, h.navigationHotUntil.Load()); !hotUntil.After(now) {
		t.Fatal("expected navigation hot window after attach return")
	}
}

func newAttachReturnTestHome() *Home {
	h := NewHome()
	h.width = 100
	h.height = 30
	h.initialLoading = false
	return h
}

func setAttachReturnTestInstances(h *Home, instances []*session.Instance) {
	h.instancesMu.Lock()
	h.instances = instances
	h.instanceByID = make(map[string]*session.Instance, len(instances))
	for _, inst := range instances {
		h.instanceByID[inst.ID] = inst
	}
	h.instancesMu.Unlock()
	h.groupTree = session.NewGroupTree(instances)
	h.rebuildFlatItems()
	h.moveCursorToSession(instances[len(instances)-1].ID)
}

func selectedSessionID(h *Home) string {
	if h.cursor < 0 || h.cursor >= len(h.flatItems) {
		return ""
	}
	item := h.flatItems[h.cursor]
	if item.Type == session.ItemTypeSession && item.Session != nil {
		return item.Session.ID
	}
	return ""
}

// TestHandleMainKeyQuickApproveWaitingSession verifies that pressing the
// quick-approve hotkey on a waiting session returns the home model without
// panicking. With no attached tmux session the send is a no-op, which is the
// behavior we want to confirm for the happy path.
func TestHandleMainKeyQuickApproveWaitingSession(t *testing.T) {
	home := NewHome()
	home.width = 100
	home.height = 30

	inst := &session.Instance{
		ID:     "session-waiting",
		Title:  "Waiting Session",
		Tool:   "claude",
		Status: session.StatusWaiting,
	}
	home.flatItems = []session.Item{{Type: session.ItemTypeSession, Session: inst}}
	home.cursor = 0
	home.instanceByID[inst.ID] = inst

	model, _ := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if _, ok := model.(*Home); !ok {
		t.Fatal("handleMainKey should return *Home")
	}
}

// TestHandleMainKeyQuickApproveOnRunningSession verifies the handler also
// works on a running session (no status guard). Bash-tool permission prompts
// in Claude Code leave the session in StatusRunning, so this is the common
// case in practice.
func TestHandleMainKeyQuickApproveOnRunningSession(t *testing.T) {
	home := NewHome()
	home.width = 100
	home.height = 30

	inst := &session.Instance{
		ID:     "session-running",
		Title:  "Running Session",
		Tool:   "claude",
		Status: session.StatusRunning,
	}
	home.flatItems = []session.Item{{Type: session.ItemTypeSession, Session: inst}}
	home.cursor = 0
	home.instanceByID[inst.ID] = inst

	model, _ := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if _, ok := model.(*Home); !ok {
		t.Fatal("handleMainKey should return *Home")
	}
}

// TestHandleMainKeyQuickApproveOnGroupItem verifies the handler does not
// crash when the cursor is on a non-session item such as a group.
func TestHandleMainKeyQuickApproveOnGroupItem(t *testing.T) {
	home := NewHome()
	home.width = 100
	home.height = 30

	home.flatItems = []session.Item{
		{Type: session.ItemTypeGroup, Path: "personal", Group: &session.Group{Name: "Personal"}},
	}
	home.cursor = 0

	model, _ := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if _, ok := model.(*Home); !ok {
		t.Fatal("handleMainKey should return *Home")
	}
}

// TestHandleMainKeyQuickApproveSkipsNonClaudeTool verifies the tool guard:
// pressing the hotkey on a non-Claude session (e.g. a shell pane) is a
// silent no-op so a stray press cannot dump a "1" into a vim/shell buffer.
func TestHandleMainKeyQuickApproveSkipsNonClaudeTool(t *testing.T) {
	home := NewHome()
	home.width = 100
	home.height = 30

	inst := &session.Instance{
		ID:     "session-shell",
		Title:  "Shell Session",
		Tool:   "shell",
		Status: session.StatusRunning,
	}
	home.flatItems = []session.Item{{Type: session.ItemTypeSession, Session: inst}}
	home.cursor = 0
	home.instanceByID[inst.ID] = inst

	model, _ := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if _, ok := model.(*Home); !ok {
		t.Fatal("handleMainKey should return *Home")
	}
}

// TestRegression743_NOnRemoteSession_NeverCreatesLocally guards #743.
// v1.7.68 shipped d9a5de8 which removed the remote early-return from the `n`
// key handler, so pressing `n` on a remote session opened the local
// newDialog and created a LOCAL session instead of a remote one. Since #1353
// the dialog DOES open for remote targets (so the user can pick a tool), but
// the remote target must be recorded so submit routes the create to the
// remote over SSH — the #743 invariant ("never create on localhost") is now
// enforced at submit time. See issue1353_remote_new_dialog_test.go for the
// submit-routing coverage.
func TestRegression743_NOnRemoteSession_NeverCreatesLocally(t *testing.T) {
	home := NewHome()
	home.width = 100
	home.height = 30

	remote := session.RemoteSessionInfo{ID: "remote-123", Title: "remote-session", RemoteName: "myserver"}
	home.flatItems = []session.Item{{Type: session.ItemTypeRemoteSession, RemoteSession: &remote, RemoteName: "myserver"}}
	home.cursor = 0

	model, _ := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	h, ok := model.(*Home)
	if !ok {
		t.Fatal("handleMainKey should return *Home")
	}
	if !h.newDialog.IsVisible() {
		t.Fatal("pressing n on a remote session must open the new-session dialog (#1353)")
	}
	if h.pendingRemoteName != "myserver" {
		t.Fatalf("remote target must be recorded so submit creates on the remote, not localhost (#743); got %q", h.pendingRemoteName)
	}
}

// TestRegression743_NOnRemoteGroup_NeverCreatesLocally — same contract for
// cursor on a remote group header row.
func TestRegression743_NOnRemoteGroup_NeverCreatesLocally(t *testing.T) {
	home := NewHome()
	home.width = 100
	home.height = 30

	home.flatItems = []session.Item{{Type: session.ItemTypeRemoteGroup, RemoteName: "myserver", Path: "remotes/myserver"}}
	home.cursor = 0

	model, _ := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	h, ok := model.(*Home)
	if !ok {
		t.Fatal("handleMainKey should return *Home")
	}
	if !h.newDialog.IsVisible() {
		t.Fatal("pressing n on a remote group must open the new-session dialog (#1353)")
	}
	if h.pendingRemoteName != "myserver" {
		t.Fatalf("remote target must be recorded so submit creates on the remote, not localhost (#743); got %q", h.pendingRemoteName)
	}
}

// TestHome_TerminalNavigationKeys verifies the PgUp/PgDn/Home/End bindings
// added alongside the existing vi-style pagination (#38). PgUp/PgDn are
// half-page aliases of Ctrl+U/Ctrl+D; Home/End jump to the first/last item
// (End fills the gap where no single-key jump-to-bottom existed, since G
// opens global search). Also covers the emacs-style Ctrl+N/Ctrl+P line
// navigation aliases for the main session list.
func TestHome_TerminalNavigationKeys(t *testing.T) {
	// Build a 100-item list so pagination + absolute jumps have room to move.
	items := make([]session.Item, 100)
	for i := range items {
		items[i] = session.Item{
			Type:    session.ItemTypeSession,
			Session: &session.Instance{ID: fmt.Sprintf("s%d", i), Title: fmt.Sprintf("S%d", i)},
			Level:   0,
		}
	}

	const width, height = 100, 30

	// Compute half-page from the actual getVisibleHeight so the test
	// stays correct if the viewport formula changes.
	h0 := newTestHomeWithItems(width, height, items)
	halfPage := h0.getVisibleHeight() / 2
	if halfPage < 1 {
		halfPage = 1
	}
	last := len(items) - 1

	tests := []struct {
		name        string
		key         tea.KeyMsg
		startCursor int
		wantCursor  int
	}{
		{"PgUp from middle", tea.KeyMsg{Type: tea.KeyPgUp}, 50, 50 - halfPage},
		{"PgUp clamps at top", tea.KeyMsg{Type: tea.KeyPgUp}, 0, 0},
		{"PgDown from middle", tea.KeyMsg{Type: tea.KeyPgDown}, 10, 10 + halfPage},
		{"PgDown clamps at bottom", tea.KeyMsg{Type: tea.KeyPgDown}, last, last},
		{"Home from middle", tea.KeyMsg{Type: tea.KeyHome}, 50, 0},
		{"Home at top no-op", tea.KeyMsg{Type: tea.KeyHome}, 0, 0},
		{"End from middle", tea.KeyMsg{Type: tea.KeyEnd}, 5, last},
		{"End at bottom no-op", tea.KeyMsg{Type: tea.KeyEnd}, last, last},
		// Emacs-style line navigation (ctrl+n / ctrl+p)
		{"ctrl+n moves down", tea.KeyMsg{Type: tea.KeyCtrlN}, 10, 11},
		{"ctrl+n clamps at bottom", tea.KeyMsg{Type: tea.KeyCtrlN}, last, last},
		{"ctrl+p moves up", tea.KeyMsg{Type: tea.KeyCtrlP}, 10, 9},
		{"ctrl+p clamps at top", tea.KeyMsg{Type: tea.KeyCtrlP}, 0, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHomeWithItems(width, height, items)
			h.cursor = tc.startCursor
			h.previewScrollOffset = 42 // non-zero to verify reset contract
			updated, _ := h.Update(tc.key)
			got := updated.(*Home).cursor
			if got != tc.wantCursor {
				t.Fatalf("cursor = %d, want %d (halfPage=%d)", got, tc.wantCursor, halfPage)
			}
			if updated.(*Home).previewScrollOffset != 0 {
				t.Fatalf("previewScrollOffset = %d, want 0 (nav handlers must reset)",
					updated.(*Home).previewScrollOffset)
			}
		})
	}

	t.Run("End on empty list does not crash", func(t *testing.T) {
		h := newTestHomeWithItems(width, height, nil)
		updated, _ := h.Update(tea.KeyMsg{Type: tea.KeyEnd})
		got := updated.(*Home).cursor
		if got != 0 {
			t.Fatalf("cursor = %d, want 0 on empty list", got)
		}
	})

	t.Run("Home on empty list does not crash", func(t *testing.T) {
		h := newTestHomeWithItems(width, height, nil)
		updated, _ := h.Update(tea.KeyMsg{Type: tea.KeyHome})
		got := updated.(*Home).cursor
		if got != 0 {
			t.Fatalf("cursor = %d, want 0 on empty list", got)
		}
	})
}

// defaultGroupItem returns a flat item for the protected "My Sessions" group.
func defaultGroupItem() session.Item {
	return session.Item{
		Type:  session.ItemTypeGroup,
		Path:  session.DefaultGroupPath,
		Level: 0,
		Group: &session.Group{
			Name:     session.DefaultGroupName,
			Path:     session.DefaultGroupPath,
			Expanded: true,
		},
	}
}

// TestDeleteBindingOnDefaultGroupShowsNotice guards against the silent no-op
// where pressing the delete binding ('d') on the protected "My Sessions" default
// group did nothing: no dialog, no message. The handler must open the
// acknowledge-only notice modal (the same centered modal used for the delete
// confirmation, so it can't be clamped off the viewport) and must not set a
// transient error banner.
func TestDeleteBindingOnDefaultGroupShowsNotice(t *testing.T) {
	home := newTestHomeWithItems(100, 30, []session.Item{defaultGroupItem()})
	home.cursor = 0

	model, _ := home.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	h := model.(*Home)

	if !h.confirmDialog.IsVisible() {
		t.Fatal("'d' on the default group must open the notice modal, got nothing (silent no-op)")
	}
	if got := h.confirmDialog.GetConfirmType(); got != ConfirmNotice {
		t.Errorf("default-group block must use ConfirmNotice, got %v", got)
	}
	if h.err != nil {
		t.Errorf("notice modal must not also set a transient error banner, got %v", h.err)
	}
}

// TestDeleteBindingOnDefaultGroupRendersNotice closes the gap left by #1334: the
// handler set h.err, but the error banner is appended below a full-height panel
// and clampViewToViewport sliced it off the bottom of the viewport, so the user
// saw nothing - the no-op stayed silent in practice. Routing through the modal
// fixes that. Assert the rendered View actually contains the message.
func TestDeleteBindingOnDefaultGroupRendersNotice(t *testing.T) {
	home := newTestHomeWithItems(100, 30, []session.Item{defaultGroupItem()})
	home.cursor = 0

	model, _ := home.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	h := model.(*Home)

	out := h.View()
	if !strings.Contains(out, session.DefaultGroupName) {
		t.Errorf("rendered View must show the default-group notice naming %q; got none", session.DefaultGroupName)
	}
	if !strings.Contains(out, "can't be deleted") {
		t.Error("rendered View must explain the group can't be deleted")
	}
}

// TestNoticeModalDismisses verifies the acknowledge-only modal closes on the
// usual dismiss keys and leaves no lingering error state.
func TestNoticeModalDismisses(t *testing.T) {
	for _, key := range []rune{'\r', '\x1b', 'o'} { // Enter, Esc, o
		home := newTestHomeWithItems(100, 30, []session.Item{defaultGroupItem()})
		home.cursor = 0
		model, _ := home.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
		h := model.(*Home)
		if !h.confirmDialog.IsVisible() {
			t.Fatal("precondition: notice modal must be open")
		}

		var msg tea.KeyMsg
		switch key {
		case '\r':
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case '\x1b':
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{key}}
		}
		model, _ = h.Update(msg)
		h = model.(*Home)
		if h.confirmDialog.IsVisible() {
			t.Errorf("notice modal must close after dismiss key %q", string(key))
		}
	}
}

// TestDeleteBindingOnDefaultGroupWhenScopedShowsNotice locks in the ordering of
// the delete handler: when the TUI is scoped to the default group itself
// (groupScope == DefaultGroupPath), both the default-group and scoped-root
// conditions match. The default-group notice must win so the feedback stays
// specific.
func TestDeleteBindingOnDefaultGroupWhenScopedShowsNotice(t *testing.T) {
	home := newTestHomeWithItems(100, 30, []session.Item{defaultGroupItem()})
	home.cursor = 0
	home.groupScope = session.DefaultGroupPath

	model, _ := home.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	h := model.(*Home)

	if got := h.confirmDialog.GetConfirmType(); got != ConfirmNotice {
		t.Fatalf("scoped default group must show the default-group notice, got type %v", got)
	}
	out := h.View()
	if !strings.Contains(out, session.DefaultGroupName) {
		t.Error("scoped default-group notice must name the default group, not the scoped-root message")
	}
	if h.err != nil {
		t.Errorf("notice path must not set a transient error, got %v", h.err)
	}
}

// TestDeleteBindingOnNonDefaultGroupOpensDialog is the positive counterpart: a
// regular group still opens the delete confirmation, so the new default-group
// branch does not shadow normal group deletion.
func TestDeleteBindingOnNonDefaultGroupOpensDialog(t *testing.T) {
	items := []session.Item{
		{
			Type:  session.ItemTypeGroup,
			Path:  "work",
			Level: 0,
			Group: &session.Group{Name: "Work", Path: "work", Expanded: true},
		},
	}
	home := newTestHomeWithItems(100, 30, items)
	home.cursor = 0

	model, _ := home.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	h := model.(*Home)

	if !h.confirmDialog.IsVisible() {
		t.Error("delete-group confirmation must open for a non-default group")
	}
	if got := h.confirmDialog.GetConfirmType(); got != ConfirmDeleteGroup {
		t.Errorf("non-default group must open the real delete confirmation, got type %v", got)
	}
	if h.err != nil {
		t.Errorf("non-default group delete must not set an error, got %v", h.err)
	}
}
