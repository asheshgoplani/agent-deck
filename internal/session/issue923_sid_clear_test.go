// Issue #923 — clearing claude_session_id via the CLI/TUI mutator must
// also drop the hook .sid sidecar, otherwise the next restart re-reads
// the stale anchor and re-injects the old id (negating the user's clear).
//
// Bug reporter: @bautrey, audit pass 2026-05-10.

package session

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSetField_ClearClaudeSessionID_DeletesHookSidecar locks the
// fix shape: when SetField clears the claude session id, the
// `~/.agent-deck/hooks/<id>.sid` anchor must be removed so a
// subsequent ReadHookSessionAnchor returns empty.
func TestSetField_ClearClaudeSessionID_DeletesHookSidecar(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	const instanceID = "00000000-0000-0000-0000-000000000923"
	const oldID = "abc-123-stale"

	// Pre-condition: a non-empty .sid anchor exists for the instance,
	// matching the real-world state before the user clears the id.
	WriteHookSessionAnchor(instanceID, oldID)
	if got := ReadHookSessionAnchor(instanceID); got != oldID {
		t.Fatalf("setup: expected anchor %q, got %q", oldID, got)
	}

	inst := &Instance{
		ID:              instanceID,
		Tool:            "claude",
		Title:           "issue-923",
		ClaudeSessionID: oldID,
	}

	// User explicitly clears the session id.
	if _, _, err := SetField(inst, FieldClaudeSessionID, "", nil); err != nil {
		t.Fatalf("SetField clear: %v", err)
	}

	// Post-condition: the in-memory field is empty AND the sidecar is
	// gone (or at minimum reads as empty).
	if inst.ClaudeSessionID != "" {
		t.Fatalf("instance ClaudeSessionID not cleared; got %q", inst.ClaudeSessionID)
	}
	if got := ReadHookSessionAnchor(instanceID); got != "" {
		t.Fatalf("hook sidecar still has stale id after clear; got %q want \"\"", got)
	}

	// Belt-and-suspenders: also assert the file is gone on disk so a
	// future hook tick can't re-populate from a half-deleted state.
	sidPath := filepath.Join(home, ".agent-deck", "hooks", instanceID+".sid")
	if _, err := os.Stat(sidPath); !os.IsNotExist(err) {
		t.Errorf("hook sidecar file still present at %q; want removed (stat err=%v)", sidPath, err)
	}
}

// TestSetField_NonEmptyClaudeSessionID_KeepsSidecar guards against a
// fix that over-deletes — when a CALLER replaces one id with another
// non-empty id, the sidecar must be left alone (or kept consistent
// with the new value); we MUST NOT clear it as a side-effect.
func TestSetField_NonEmptyClaudeSessionID_KeepsSidecar(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	const instanceID = "00000000-0000-0000-0000-000000000924"
	const oldID = "old-id"
	const newID = "new-id"

	WriteHookSessionAnchor(instanceID, oldID)

	inst := &Instance{
		ID:              instanceID,
		Tool:            "claude",
		Title:           "issue-923-keep",
		ClaudeSessionID: oldID,
	}

	if _, _, err := SetField(inst, FieldClaudeSessionID, newID, nil); err != nil {
		t.Fatalf("SetField replace: %v", err)
	}

	// Anchor must NOT be empty — fix should ONLY clear on empty value.
	if got := ReadHookSessionAnchor(instanceID); got == "" {
		t.Fatalf("hook sidecar wrongly cleared on non-empty replace")
	}
}
