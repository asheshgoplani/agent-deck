package vcsbackend

import (
	"strings"
	"testing"
)

// Detect on a directory that is neither a git nor a jujutsu repo must
// fail loudly so callers (e.g. WebMutator.FinishWorktree, see issue
// #1126) can return the correct surface error.
func TestDetect_NonRepoReturnsError(t *testing.T) {
	tmp := t.TempDir()
	_, err := Detect(tmp)
	if err == nil {
		t.Fatalf("expected error for non-repo directory, got nil")
	}
	if !strings.Contains(err.Error(), "not a git or jujutsu repository") {
		t.Errorf("expected diagnostic in error, got %q", err.Error())
	}
}
