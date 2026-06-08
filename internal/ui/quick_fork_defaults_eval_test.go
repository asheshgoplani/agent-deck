//go:build eval_smoke

package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestEval_ForkDialog_ComprehensiveDefaultsVisibleOnOpen proves that, with NO
// [fork] config present, the fork dialog opens on a git project with the
// comprehensive defaults (worktree + carry-state + gitignored) ALREADY checked
// — i.e. the user SEES "comprehensive, tweak down" without pressing a key.
// This is the disclosure-visible contract that pure getter tests can't express.
func TestEval_ForkDialog_ComprehensiveDefaultsVisibleOnOpen(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	// Scratch HOME so the developer's real ~/.agent-deck/config.toml (which may
	// carry a [fork] section) can't perturb the default under test.
	home := t.TempDir()
	t.Setenv("HOME", home)
	session.ClearUserConfigCache()
	t.Cleanup(func() { session.ClearUserConfigCache() })

	// Real git repo so git.IsGitRepoOrBareProjectRoot() -> worktreeCapable=true,
	// which lets the worktree + nested with-state rows render.
	repo := filepath.Join(home, "proj")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	d := NewForkDialog()
	d.SetSize(90, 40)
	d.Show("Eval Parent", repo, "", nil, "")

	// State getters: comprehensive defaults seeded with zero interaction.
	if !d.IsWorktreeEnabled() {
		t.Error("worktree must default ON in a git repo with no [fork] config")
	}
	if !d.IsWithStateEnabled() {
		t.Error("carry-parent-state must default ON with no [fork] config")
	}
	if !d.IsWithStateAndGitignoredEnabled() {
		t.Error("include-gitignored must default ON with no [fork] config")
	}

	// Rendered, user-visible disclosure: the checked boxes appear on open.
	view := d.View()
	for _, want := range []string{"[x] Carry parent state", "[x] Include gitignored files"} {
		if !strings.Contains(view, want) {
			t.Errorf("dialog must render %q checked on open; view:\n%s", want, view)
		}
	}
}
