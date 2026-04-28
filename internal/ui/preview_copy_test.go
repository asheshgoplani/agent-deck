package ui

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestBuildSessionInfoForCopy_Worktree pins #791: when the user yanks the
// preview info for a worktree session, the resulting clipboard payload must
// contain the three values surfaced in the right pane (Repo, Path, Branch)
// in a plain-text shape that's pasteable straight into a shell or doc.
func TestBuildSessionInfoForCopy_Worktree(t *testing.T) {
	inst := &session.Instance{
		Title:            "feature/x",
		ProjectPath:      "/Users/ashesh/repo/.worktrees/feature-x",
		WorktreePath:     "/Users/ashesh/repo/.worktrees/feature-x",
		WorktreeRepoRoot: "/Users/ashesh/repo",
		WorktreeBranch:   "feature/x",
	}

	got := buildSessionInfoForCopy(inst)

	// Three labelled lines, in a stable order so users can rely on the format.
	for _, want := range []string{
		"Repo: /Users/ashesh/repo",
		"Path: /Users/ashesh/repo/.worktrees/feature-x",
		"Branch: feature/x",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("expected payload to contain %q, got:\n%s", want, got)
		}
	}
}

// TestBuildSessionInfoForCopy_PlainSession verifies non-worktree sessions
// still produce a usable payload (just the project path) — no Branch/Repo
// noise when those fields aren't populated.
func TestBuildSessionInfoForCopy_PlainSession(t *testing.T) {
	inst := &session.Instance{
		Title:       "scratch",
		ProjectPath: "/tmp/scratch",
	}

	got := buildSessionInfoForCopy(inst)

	if !strings.Contains(got, "Path: /tmp/scratch") {
		t.Errorf("expected Path line, got:\n%s", got)
	}
	if strings.Contains(got, "Branch:") {
		t.Errorf("non-worktree session should not emit Branch line, got:\n%s", got)
	}
	if strings.Contains(got, "Repo:") {
		t.Errorf("non-worktree session should not emit Repo line, got:\n%s", got)
	}
}

// TestBuildSessionInfoForCopy_MultiRepo verifies that multi-repo sessions
// surface every project path so the user can paste the full set.
func TestBuildSessionInfoForCopy_MultiRepo(t *testing.T) {
	inst := &session.Instance{
		Title:            "multi",
		ProjectPath:      "/repos/api",
		MultiRepoEnabled: true,
		AdditionalPaths:  []string{"/repos/web", "/repos/shared"},
	}

	got := buildSessionInfoForCopy(inst)

	for _, want := range []string{"/repos/api", "/repos/web", "/repos/shared"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected payload to contain %q, got:\n%s", want, got)
		}
	}
}
