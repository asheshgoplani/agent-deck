package ui

import "testing"

func TestArchiveHotkeysRegisteredWithDefaults(t *testing.T) {
	bindings := resolveHotkeys(nil)
	if bindings[hotkeyArchiveSession] != "A" {
		t.Fatalf("archive_session default = %q, want \"A\"", bindings[hotkeyArchiveSession])
	}
	if bindings[hotkeyToggleArchived] != "ctrl+a" {
		t.Fatalf("toggle_archived default = %q, want \"ctrl+a\"", bindings[hotkeyToggleArchived])
	}

	// Canonical lookup: pressing "A" / "shift+a" both resolve to the archive
	// action's canonical key; pressing "ctrl+a" resolves to the toggle.
	lookup, _ := buildHotkeyLookup(bindings)
	if lookup["A"] != "A" || lookup["shift+a"] != "A" {
		t.Fatalf("expected A and shift+a to map to canonical \"A\"; got %q, %q", lookup["A"], lookup["shift+a"])
	}
	if lookup["ctrl+a"] != "ctrl+a" {
		t.Fatalf("expected ctrl+a to map to canonical \"ctrl+a\"; got %q", lookup["ctrl+a"])
	}
}
