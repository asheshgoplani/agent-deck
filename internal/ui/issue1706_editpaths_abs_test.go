package ui

import (
	"os"
	"path/filepath"
	"testing"
)

// Related to #1706: the edit-paths dialog persists its list straight into
// project_path / additional_paths, so a relative entry would be validated
// against the TUI's cwd and then read back by tmux, the Claude project slug and
// the #1731 hook-cwd ownership check, each of which resolves it differently.
func TestIssue1706_EditPathsDialogResolvesRelativeEntries(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	inst := newTestMultiRepoInstance([]string{"/tmp/repo-a", "/tmp/repo-b"})
	d := NewEditPathsDialog()
	d.Show(inst, nil)
	d.paths = []string{"/tmp/repo-a", "repo-relative"}

	got := d.GetPaths()
	if len(got) != 2 {
		t.Fatalf("GetPaths() returned %d paths, want 2: %v", len(got), got)
	}
	want := filepath.Join(cwd, "repo-relative")
	if got[1] != want {
		t.Fatalf("GetPaths()[1] = %q, want %q", got[1], want)
	}
	if got[0] != "/tmp/repo-a" {
		t.Fatalf("GetPaths()[0] = %q, want the absolute entry untouched", got[0])
	}
}

// Resolving before the dedupe also collapses two spellings of one directory,
// which would otherwise be stored as two distinct additional_paths entries.
func TestIssue1706_EditPathsDialogDedupesEquivalentSpellings(t *testing.T) {
	inst := newTestMultiRepoInstance([]string{"/tmp/repo-a", "/tmp/repo-b"})
	d := NewEditPathsDialog()
	d.Show(inst, nil)
	d.paths = []string{"/tmp/repo-a", "repo-relative", "./repo-relative"}

	if got := d.GetPaths(); len(got) != 2 {
		t.Fatalf("GetPaths() returned %d paths, want 2 (the two spellings are one directory): %v", len(got), got)
	}
}
