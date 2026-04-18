package ui

// Issue #666 — TUI-level bug reproduce for the "session disappears from
// its chosen group" symptom.
//
// Root cause: internal/ui/home.go:4762 (the createSessionFromGlobalSearch
// path, where the TUI imports an existing Claude conversation from global
// search) passed h.getCurrentGroupPath() directly into
// session.NewInstanceWithGroupAndTool. getCurrentGroupPath returns ""
// when the cursor is on a flatItem that is neither a group nor a session
// (ItemTypeWindow, ItemTypeRemoteGroup, ItemTypeRemoteSession, creating-
// placeholder). NewInstanceWithGroupAndTool then OVERRODE the
// extractGroupPath default with "" (instance.go:466-467), leaving
// inst.GroupPath="". Storage persisted '' (pre-v1.7.25) and the next
// reload re-derived via extractGroupPath(ProjectPath) — surfacing the
// session under a path-derived group instead of the one the user was
// browsing.
//
// The fix at home.go:4762 uses resolveNewSessionGroup(), which rescues
// empty by falling back to groupScope or DefaultGroupPath. This file
// pins the contract.

import (
	"os"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestIssue666_ResolveNewSessionGroup_CursorOnWindow_FallsBackToDefault
// is the small-seam unit test: when getCurrentGroupPath can't produce a
// group from cursor position, the resolver must NOT return empty.
//
// On v1.7.24 baseline: resolveNewSessionGroup doesn't exist; home.go:4762
// used getCurrentGroupPath directly — which returned "". RED.
//
// On v1.7.25 fix: resolveNewSessionGroup wraps getCurrentGroupPath with
// rescue-to-scope-or-default. GREEN.
func TestIssue666_ResolveNewSessionGroup_CursorOnWindow_FallsBackToDefault(t *testing.T) {
	h := &Home{
		flatItems: []session.Item{
			{Type: session.ItemTypeWindow, WindowName: "window-1"},
		},
		cursor: 0,
	}

	// Pin the pre-existing bug signal for documentation purposes:
	// getCurrentGroupPath alone (the v1.7.24 code path) returns empty.
	if pre := h.getCurrentGroupPath(); pre != "" {
		t.Fatalf("fixture broken: getCurrentGroupPath expected '' for Window cursor, got %q", pre)
	}

	got := h.resolveNewSessionGroup()
	if got == "" {
		t.Fatalf("issue #666: resolveNewSessionGroup returned empty. The " +
			"downstream call site (home.go:4762, createSessionFromGlobalSearch) " +
			"would feed this empty string to NewInstanceWithGroupAndTool, which " +
			"overrides the extractGroupPath default with '', producing a session " +
			"that disappears from its group on the next reload.")
	}
	if got != session.DefaultGroupPath {
		t.Fatalf("expected DefaultGroupPath fallback for unscoped Home with no cursor group, got %q", got)
	}
}

// TestIssue666_ResolveNewSessionGroup_ScopedView_UsesScope covers the
// second rescue arm: if the TUI is running in scoped-group mode
// (agent-deck -g some-group), the scope is the correct anchor — not
// DefaultGroupPath.
func TestIssue666_ResolveNewSessionGroup_ScopedView_UsesScope(t *testing.T) {
	h := &Home{
		flatItems:  []session.Item{{Type: session.ItemTypeWindow}},
		cursor:     0,
		groupScope: "agent-deck",
	}
	got := h.resolveNewSessionGroup()
	if got != "agent-deck" {
		t.Fatalf("scoped Home with cursor on Window should rescue via groupScope; got %q, want agent-deck", got)
	}
}

// TestIssue666_ResolveNewSessionGroup_CursorOnGroup_PreservesIt is the
// over-reach guard: when getCurrentGroupPath yields a real group (cursor
// on a Group or Session item), the resolver passes it through unchanged.
func TestIssue666_ResolveNewSessionGroup_CursorOnGroup_PreservesIt(t *testing.T) {
	grp := &session.Group{Name: "work", Path: "work"}
	h := &Home{
		flatItems: []session.Item{
			{Type: session.ItemTypeGroup, Group: grp, Path: "work"},
		},
		cursor: 0,
	}
	if got := h.resolveNewSessionGroup(); got != "work" {
		t.Fatalf("resolver must pass through explicit cursor group; got %q, want work", got)
	}
}

// TestIssue666_GlobalSearchImport_EndToEnd_PreservesGroupAcrossReload is
// the full integration test that the user asked for: it simulates the
// exact call chain at home.go:4762 (Window cursor → resolveNewSessionGroup
// → NewInstanceWithGroupAndTool) and then persists through the storage
// layer + reloads to prove the GroupPath survives the round-trip.
//
// The test runs against the REAL SQLite storage path the TUI uses, not
// a stub — same SaveWithGroups / LoadWithGroups / extractGroupPath
// fallback code that hits in production.
//
// Three-config behavior (what to expect under the revert dance the user
// prescribed):
//
//  1. Both fixes present (current branch): reload GroupPath == "agent-deck". GREEN.
//  2. Only storage.go:280 belt-and-braces (4762 reverted): inst.GroupPath="" at save
//     → belt-and-braces normalizes to DefaultGroupPath → reload GroupPath ==
//     "my-sessions", not "agent-deck". RED (asserting == "agent-deck").
//  3. Both fixes reverted (v1.7.24 baseline): save persists ” → reload re-derives
//     via extractGroupPath(ProjectPath) → for /tmp/claude-proj, GroupPath == "claude-proj"
//     (some path-derived name). RED.
func TestIssue666_GlobalSearchImport_EndToEnd_PreservesGroupAcrossReload(t *testing.T) {
	// --- storage setup: isolate via a temp HOME so we don't touch the
	// user's real state.db. NewStorageWithProfile walks the profile
	// layout under HOME + .agent-deck and migrates as needed.
	origHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	os.Setenv("HOME", tmpHome)
	session.ClearUserConfigCache()
	t.Cleanup(func() {
		os.Setenv("HOME", origHome)
		session.ClearUserConfigCache()
	})
	storage, err := session.NewStorageWithProfile("_issue666_tui")
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	t.Cleanup(func() { storage.Close() })

	// --- Home state: cursor on a Window item, scoped to "agent-deck"
	// (simulating the user browsing their agent-deck conductor group when
	// they import from global search).
	h := &Home{
		flatItems: []session.Item{
			{Type: session.ItemTypeWindow, WindowName: "tmux-window-0"},
		},
		cursor:     0,
		groupScope: "agent-deck",
	}

	// --- MIRROR the exact sequence at home.go:4762.
	// createSessionFromGlobalSearch builds the instance like this:
	//   inst := session.NewInstanceWithGroupAndTool(title, projectPath,
	//       h.resolveNewSessionGroup(), "claude")
	projectPath := "/tmp/claude-proj" // a path whose extractGroupPath returns "claude-proj"
	groupPath := h.resolveNewSessionGroup()
	inst := session.NewInstanceWithGroupAndTool(
		"imported-session",
		projectPath,
		groupPath,
		"claude",
	)
	inst.ClaudeSessionID = "test-global-search-session-id"
	inst.CreatedAt = time.Now()

	// --- Save (identical to forceSaveInstances flow)
	tree := session.NewGroupTree([]*session.Instance{inst})
	if err := storage.SaveWithGroups([]*session.Instance{inst}, tree); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}

	// --- Reload
	reloaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups: %v", err)
	}
	if len(reloaded) != 1 {
		t.Fatalf("expected 1 instance after reload, got %d", len(reloaded))
	}

	got := reloaded[0].GroupPath
	if got == "" {
		t.Fatalf("issue #666 root: GroupPath empty after reload")
	}
	if got == "claude-proj" {
		t.Fatalf("issue #666 root: reload silently re-derived GroupPath via "+
			"extractGroupPath(%q)=%q. The storage.go:280 belt-and-braces guard "+
			"is missing or bypassed.", projectPath, got)
	}
	if got == session.DefaultGroupPath {
		t.Fatalf("issue #666 partial fix: GroupPath collapsed to %q, "+
			"meaning the empty string reached the save layer and was only "+
			"caught by belt-and-braces. The real fix at home.go:4762 is "+
			"missing — resolveNewSessionGroup must rescue empty upstream, "+
			"preserving the scoped group (agent-deck).", got)
	}
	if got != "agent-deck" {
		t.Fatalf("GroupPath after reload = %q, want %q (scope)", got, "agent-deck")
	}
}
