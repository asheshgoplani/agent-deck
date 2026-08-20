package ui

import "testing"

// Claude Code renders its session name as the pane title. Until it derives a
// task name, that session name IS the `--name` address agent-deck passed at
// launch — so the "live task description" of a fresh session is its own random
// handle. Persisting that as the auto-name description is what pinned
// auto-named rows to a handle forever.
func TestCleanPaneTitleForRejectsPeerNameEcho(t *testing.T) {
	const id = "ce08010d-1787223144"

	if got := cleanPaneTitleFor("✳ pearly-mesa-ce08010d", id); got != "" {
		t.Errorf("peer-name echo leaked through as %q, want empty", got)
	}
	if got := cleanPaneTitleFor("✳ pale-swallow-ce08010d-ce08010d", id); got != "" {
		t.Errorf("compounded peer-name echo leaked through as %q, want empty", got)
	}

	// A real task description must still survive, spinner and all.
	if got := cleanPaneTitleFor("✳ GIS implementation and DevOps deployment", id); got != "GIS implementation and DevOps deployment" {
		t.Errorf("task description = %q, want it preserved", got)
	}
	// Another session's address is not this session's echo.
	if got := cleanPaneTitleFor("✳ pearly-mesa-d0afc551", id); got != "pearly-mesa-d0afc551" {
		t.Errorf("foreign address = %q, want it preserved", got)
	}
	// No instance context: behave exactly like cleanPaneTitle.
	if got := cleanPaneTitleFor("✳ pearly-mesa-ce08010d", ""); got != "pearly-mesa-ce08010d" {
		t.Errorf("with no instance ID, got %q, want the plain cleaned title", got)
	}
	// Generic titles stay filtered.
	if got := cleanPaneTitleFor("Claude Code", id); got != "" {
		t.Errorf("generic title = %q, want empty", got)
	}
}
