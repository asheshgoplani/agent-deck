package ui

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestShowArchivedRoundTripsUIState(t *testing.T) {
	home, _ := buildTwoGroupHome(t)
	if home.storage == nil {
		t.Skip("no storage wired in test home; round-trip not exercised")
	}

	// Restore the persisted flag to false when the test finishes so the shared
	// _test profile DB is not left with show_archived=true, which would leak
	// into later NewHome() constructions in the same package (order-dependent
	// flakiness hazard).
	t.Cleanup(func() {
		home.showArchived = false
		home.saveUIState()
	})

	home.showArchived = true
	home.saveUIState()

	// Flip in memory, then reload from persisted state.
	home.showArchived = false
	home.loadUIState()

	if !home.showArchived {
		t.Fatal("show_archived did not round-trip through saveUIState/loadUIState")
	}
	_ = session.StatusStopped // keep import used if assertions change
}

// TestShowArchivedMarshalRoundTrip exercises the JSON marshal/unmarshal path
// for the ShowArchived field without requiring a live storage connection.
// This guarantees the field survives even when the storage-based test skips.
func TestShowArchivedMarshalRoundTrip(t *testing.T) {
	original := uiState{ShowArchived: true}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	var restored uiState
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	if !restored.ShowArchived {
		t.Fatal("ShowArchived did not survive JSON round-trip")
	}
}

func timeNowForTest() time.Time { return time.Now() }

func archivedRowVisible(h *Home, title string) bool {
	for _, it := range h.flatItems {
		if it.Type == session.ItemTypeSession && it.Session != nil && it.Session.Title == title {
			return true
		}
	}
	return false
}

func TestRebuildFlatItems_HidesArchivedByDefault(t *testing.T) {
	home, _ := buildTwoGroupHome(t)

	// Archive a1 directly on the model, then rebuild with showArchived off.
	home.instancesMu.RLock()
	for _, inst := range home.instances {
		if inst.Title == "a1" {
			inst.ArchivedAt = timeNowForTest()
		}
	}
	home.instancesMu.RUnlock()

	home.showArchived = false
	home.rebuildFlatItems()
	if archivedRowVisible(home, "a1") {
		t.Fatal("archived session a1 must be hidden when showArchived is off")
	}

	home.showArchived = true
	home.rebuildFlatItems()
	if !archivedRowVisible(home, "a1") {
		t.Fatal("archived session a1 must be visible when showArchived is on")
	}
}

func keyMsgString(s string) tea.KeyMsg {
	// Build a KeyMsg whose .String() equals s for the simple cases we need.
	switch s {
	case "A":
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'A'}}
	case "ctrl+a":
		return tea.KeyMsg{Type: tea.KeyCtrlA}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestArchiveKey_ArchivesAndParksSelection(t *testing.T) {
	home, idx := buildTwoGroupHome(t)
	home.showArchived = false // ensure default regardless of persisted ui state
	home.rebuildFlatItems()
	home.cursor = idx["a1"]

	model, _ := home.handleMainKey(keyMsgString("A"))
	h := model.(*Home)

	// a1 is archived but PARKED: it stays visible+selected (showArchived off) so
	// a second 'A' undoes it in place. It only hides after navigating away
	// (covered by TestArchiveThenNavigate_HidesStickyRow).
	if !archivedRowVisible(h, "a1") {
		t.Fatal("just-archived a1 should stay visible (sticky) for in-place undo")
	}
	if h.cursor < 0 || h.cursor >= len(h.flatItems) {
		t.Fatalf("cursor out of range after archiving: %d / %d", h.cursor, len(h.flatItems))
	}
	if it := h.flatItems[h.cursor]; it.Type != session.ItemTypeSession || it.Session == nil || it.Session.Title != "a1" {
		t.Fatalf("cursor must stay on the just-archived a1; got %+v", it)
	}
	// Confirm the instance really carries ArchivedAt.
	found := false
	h.instancesMu.RLock()
	for _, inst := range h.instances {
		if inst.Title == "a1" && inst.IsArchived() {
			found = true
		}
	}
	h.instancesMu.RUnlock()
	if !found {
		t.Fatal("a1 instance must have ArchivedAt set")
	}
}

func TestArchiveKey_UnarchivesWhenSelectionArchived(t *testing.T) {
	home, _ := buildTwoGroupHome(t)
	// Reveal archived rows so we can land the cursor on one.
	home.showArchived = true
	home.instancesMu.RLock()
	for _, inst := range home.instances {
		if inst.Title == "a1" {
			inst.ArchivedAt = timeNowForTest()
			inst.Status = session.StatusStopped
		}
	}
	home.instancesMu.RUnlock()
	home.rebuildFlatItems()

	// Put cursor on a1 (now archived, visible).
	for i, it := range home.flatItems {
		if it.Type == session.ItemTypeSession && it.Session != nil && it.Session.Title == "a1" {
			home.cursor = i
		}
	}

	model, _ := home.handleMainKey(keyMsgString("A"))
	h := model.(*Home)

	h.instancesMu.RLock()
	defer h.instancesMu.RUnlock()
	for _, inst := range h.instances {
		if inst.Title == "a1" {
			if inst.IsArchived() {
				t.Fatal("pressing A on an archived row must unarchive it")
			}
			if inst.Status != session.StatusStopped {
				t.Fatalf("unarchived session should remain stopped, got %s", inst.Status)
			}
		}
	}
}

// TestArchiveKey_AgainUndoesWhileParked verifies the just-archived row stays
// visible+selected (showArchived off) so a second 'A' undoes it in place.
func TestArchiveKey_AgainUndoesWhileParked(t *testing.T) {
	home, idx := buildTwoGroupHome(t)
	home.showArchived = false
	home.rebuildFlatItems()
	home.cursor = idx["a1"]

	// First A: archive a1. With the sticky hold it must remain visible+selected.
	model, _ := home.handleMainKey(keyMsgString("A"))
	h := model.(*Home)
	if !archivedRowVisible(h, "a1") {
		t.Fatal("just-archived a1 must stay visible (sticky) for in-place undo")
	}
	if h.stickyArchivedID == "" {
		t.Fatal("stickyArchivedID must be set after archiving with showArchived off")
	}
	if it := h.flatItems[h.cursor]; it.Type != session.ItemTypeSession || it.Session == nil || it.Session.Title != "a1" {
		t.Fatalf("cursor must stay on the just-archived a1; got %+v", it)
	}

	// Second A (without navigating): must UNARCHIVE a1, not archive a neighbor.
	model, _ = h.handleMainKey(keyMsgString("A"))
	h = model.(*Home)
	if h.stickyArchivedID != "" {
		t.Fatal("stickyArchivedID must clear after undo")
	}
	h.instancesMu.RLock()
	defer h.instancesMu.RUnlock()
	for _, inst := range h.instances {
		if inst.Title == "a1" && inst.IsArchived() {
			t.Fatal("pressing A again must unarchive a1")
		}
	}
}

// TestArchiveThenNavigate_HidesStickyRow verifies the sticky hold releases on
// navigation so the archived row hides on the next rebuild.
func TestArchiveThenNavigate_HidesStickyRow(t *testing.T) {
	home, idx := buildTwoGroupHome(t)
	home.showArchived = false
	home.rebuildFlatItems()
	home.cursor = idx["a1"]

	model, _ := home.handleMainKey(keyMsgString("A"))
	h := model.(*Home)
	if h.stickyArchivedID == "" {
		t.Fatal("precondition: sticky hold set after archive")
	}

	// Navigate away (down), then rebuild — the archived row must now be hidden.
	model, _ = h.handleMainKey(keyMsgString("j"))
	h = model.(*Home)
	if h.stickyArchivedID != "" {
		t.Fatal("navigation must release the sticky hold")
	}
	h.rebuildFlatItems()
	if archivedRowVisible(h, "a1") {
		t.Fatal("archived a1 must hide once the sticky hold is released")
	}
}

// TestArchiveThenNavigate_HidesWithoutManualRebuild reproduces the user-reported
// bug: after archiving, the row "stays for multiple actions". Navigating away must
// hide the just-archived row immediately — without any caller manually invoking
// rebuildFlatItems — because in the live app the row otherwise lingers until some
// unrelated background rebuild fires.
func TestArchiveThenNavigate_HidesWithoutManualRebuild(t *testing.T) {
	home, idx := buildTwoGroupHome(t)
	home.showArchived = false
	home.rebuildFlatItems()
	home.cursor = idx["a1"]

	model, _ := home.handleMainKey(keyMsgString("A"))
	h := model.(*Home)
	if h.stickyArchivedID == "" {
		t.Fatal("precondition: sticky hold set after archive")
	}
	if !archivedRowVisible(h, "a1") {
		t.Fatal("precondition: just-archived a1 stays visible (sticky)")
	}

	// Navigate away (down). No manual rebuild — the row must already be gone.
	model, _ = h.handleMainKey(keyMsgString("j"))
	h = model.(*Home)
	if h.stickyArchivedID != "" {
		t.Fatal("navigation must release the sticky hold")
	}
	if archivedRowVisible(h, "a1") {
		t.Fatal("archived a1 must hide immediately on navigate-away, without a manual rebuild")
	}
}

// TestArchive_PersistsUnderReloadChurn reproduces the user-reported bug where
// archiving a session whose tmux is missing/errored "comes back" after a moment.
// Such sessions churn the state DB constantly (status poller → save → storage
// watcher → reload), so the state file's mtime is always newer than the TUI's
// last load. When the user presses 'A', the non-force saveInstances() hits the
// external-change guard in saveInstancesWithForce and ABORTS the write; the next
// reload then runs `h.instances = msg.instances`, swapping in a fresh disk object
// whose ArchivedAt is zero — silently un-archiving the row.
//
// Archiving is a deliberate user mutation and MUST persist regardless of reload
// churn (the same guarantee create/fork/delete already get via forceSaveInstances).
func TestArchive_PersistsUnderReloadChurn(t *testing.T) {
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", t.TempDir())
	session.ClearUserConfigCache()
	t.Cleanup(func() {
		os.Setenv("HOME", origHome)
		session.ClearUserConfigCache()
	})

	const profile = "_archive_persist_churn"
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	t.Cleanup(func() { storage.Close() })

	inst := session.NewInstanceWithTool("err1", "/tmp/err1", "claude")
	insts := []*session.Instance{inst}
	tree := session.NewGroupTree(insts)
	if err := storage.SaveWithGroups(insts, tree); err != nil {
		t.Fatalf("seed SaveWithGroups: %v", err)
	}

	h := &Home{
		profile:      profile,
		storage:      storage,
		instances:    insts,
		instanceByID: map[string]*session.Instance{inst.ID: inst},
		groupTree:    tree,
		width:        120,
		height:       40,
	}
	h.rebuildFlatItems()
	for i, it := range h.flatItems {
		if it.Type == session.ItemTypeSession && it.Session != nil && it.Session.ID == inst.ID {
			h.cursor = i
		}
	}

	// Reproduce the churn: our last load is stale relative to the file on disk,
	// so the external-change guard in the non-force save path would abort.
	h.reloadMu.Lock()
	h.lastLoadMtime = time.Now().Add(-time.Hour)
	h.reloadMu.Unlock()

	model, _ := h.handleMainKey(keyMsgString("A"))
	h = model.(*Home)
	if !inst.IsArchived() {
		t.Fatal("precondition: in-memory ArchivedAt set after pressing A")
	}

	// Reload from disk — this is exactly what the background watcher does.
	loaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups: %v", err)
	}
	var got *session.Instance
	for _, in := range loaded {
		if in.ID == inst.ID {
			got = in
		}
	}
	if got == nil {
		t.Fatal("instance missing after reload")
	}
	if !got.IsArchived() {
		t.Fatal("archive did not persist to disk — the save was aborted by the " +
			"external-change guard, so the next reload un-archives the row (the " +
			"user-reported 'comes back'). toggleArchiveSelected must force the save.")
	}
}

// TestArchive_CapturesLiveAutoNameBeforeKill reproduces the user-reported bug:
// archiving an auto-named session "loses the auto generated name from
// description". Auto-named rows render the LIVE tmux pane title (the render
// snapshot's paneTitle). Archiving stops the process (Kill), so the live pane
// title vanishes; the row then falls back to GetAutoNameDescription(). That
// description is only persisted by the 2s background tick, so a session archived
// before the tick fires reverts to its bare random handle.
//
// toggleArchiveSelected must capture the live pane title into the auto-name
// description BEFORE killing, so the meaningful name survives the stop.
func TestArchive_CapturesLiveAutoNameBeforeKill(t *testing.T) {
	home, idx := buildTwoGroupHome(t)
	home.showArchived = false
	home.rebuildFlatItems()
	home.cursor = idx["a1"]

	var a1 *session.Instance
	home.instancesMu.RLock()
	for _, inst := range home.instances {
		if inst.Title == "a1" {
			a1 = inst
		}
	}
	home.instancesMu.RUnlock()
	if a1 == nil {
		t.Fatal("a1 not found")
	}

	// a1 is an auto-named quick session whose live task description is visible on
	// the row but has NOT yet been persisted by the background tick.
	a1.AutoName = true
	a1.SetAutoNameDescription("")
	const live = "Refactoring the auth module"
	home.sessionRenderSnapshot.Store(map[string]sessionRenderState{
		a1.ID: {
			status:    a1.GetStatusThreadSafe(),
			tool:      a1.GetToolThreadSafe(),
			paneTitle: live,
		},
	})

	// Sanity: the row currently shows the live description (not the handle).
	if got := displaySessionTitle(a1, live); got != live {
		t.Fatalf("precondition: row should show live description; got %q", got)
	}

	model, _ := home.handleMainKey(keyMsgString("A"))
	h := model.(*Home)
	_ = h

	if !a1.IsArchived() {
		t.Fatal("precondition: a1 must be archived after pressing A")
	}
	// After the stop, there is no live pane title. The row must still show the
	// captured description, NOT the bare handle "a1".
	if got := a1.GetAutoNameDescription(); got != live {
		t.Fatalf("archive must capture the live task description before killing; "+
			"GetAutoNameDescription() = %q, want %q", got, live)
	}
	if got := displaySessionTitle(a1, ""); got != live {
		t.Fatalf("archived auto-named row must keep its description, not revert to "+
			"the handle; displaySessionTitle = %q, want %q", got, live)
	}
}

// TestArchive_AutoNameDescriptionSurvivesReload is the end-to-end guarantee:
// archiving an auto-named session must persist its live description to disk so a
// subsequent reload (the constant churn errored/missing-tmux sessions generate)
// reloads the meaningful name, not the bare handle. Uses real storage so the
// h.storage.WriteAutoNameDescription path actually runs (unlike the nil-storage
// buildTwoGroupHome), mirroring TestArchive_PersistsUnderReloadChurn.
func TestArchive_AutoNameDescriptionSurvivesReload(t *testing.T) {
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", t.TempDir())
	session.ClearUserConfigCache()
	t.Cleanup(func() {
		os.Setenv("HOME", origHome)
		session.ClearUserConfigCache()
	})

	const profile = "_archive_autoname_reload"
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	t.Cleanup(func() { storage.Close() })

	inst := session.NewInstanceWithTool("q-9c1d", "/tmp/q9c1d", "claude")
	inst.AutoName = true // auto-named quick session; no description persisted yet
	insts := []*session.Instance{inst}
	tree := session.NewGroupTree(insts)
	if err := storage.SaveWithGroups(insts, tree); err != nil {
		t.Fatalf("seed SaveWithGroups: %v", err)
	}

	h := &Home{
		profile:      profile,
		storage:      storage,
		instances:    insts,
		instanceByID: map[string]*session.Instance{inst.ID: inst},
		groupTree:    tree,
		width:        120,
		height:       40,
	}
	h.sessionRenderSnapshot.Store(make(map[string]sessionRenderState))
	h.rebuildFlatItems()
	for i, it := range h.flatItems {
		if it.Type == session.ItemTypeSession && it.Session != nil && it.Session.ID == inst.ID {
			h.cursor = i
		}
	}

	// The live task description is on the row but not yet persisted by any tick.
	const live = "Implementing the parser"
	h.sessionRenderSnapshot.Store(map[string]sessionRenderState{
		inst.ID: {
			status:    inst.GetStatusThreadSafe(),
			tool:      inst.GetToolThreadSafe(),
			paneTitle: live,
		},
	})

	model, _ := h.handleMainKey(keyMsgString("A"))
	h = model.(*Home)
	if !inst.IsArchived() {
		t.Fatal("precondition: a session must be archived after pressing A")
	}

	// Reload from disk — exactly what the background watcher does on churn.
	loaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups: %v", err)
	}
	var got *session.Instance
	for _, in := range loaded {
		if in.ID == inst.ID {
			got = in
		}
	}
	if got == nil {
		t.Fatal("instance missing after reload")
	}
	if !got.IsArchived() {
		t.Fatal("archived_at did not survive reload")
	}
	if got.GetAutoNameDescription() != live {
		t.Fatalf("auto-name description did not survive archive+reload: got %q want %q "+
			"(the reloaded row would show its bare handle — the user-reported "+
			"'loses the auto generated name')", got.GetAutoNameDescription(), live)
	}
}

func TestToggleArchivedKey_RevealsArchivedInline(t *testing.T) {
	home, _ := buildTwoGroupHome(t)
	home.instancesMu.RLock()
	for _, inst := range home.instances {
		if inst.Title == "a1" {
			inst.ArchivedAt = timeNowForTest()
		}
	}
	home.instancesMu.RUnlock()
	home.showArchived = false
	home.rebuildFlatItems()
	if archivedRowVisible(home, "a1") {
		t.Fatal("precondition: a1 should be hidden")
	}

	model, _ := home.handleMainKey(keyMsgString("ctrl+a"))
	h := model.(*Home)

	if !h.showArchived {
		t.Fatal("ctrl+a must turn showArchived on")
	}
	if !archivedRowVisible(h, "a1") {
		t.Fatal("ctrl+a must reveal archived a1 inline")
	}
}

func TestRenderSessionItem_ArchivedShowsBadge(t *testing.T) {
	home, _ := buildTwoGroupHome(t)
	home.showArchived = true
	home.instancesMu.RLock()
	for _, inst := range home.instances {
		if inst.Title == "a1" {
			inst.ArchivedAt = timeNowForTest()
			inst.Status = session.StatusStopped
		}
	}
	home.instancesMu.RUnlock()
	home.rebuildFlatItems()

	out := home.View()
	if !strings.Contains(out, "[archived]") {
		t.Fatal("archived session row must render an [archived] badge")
	}
}
