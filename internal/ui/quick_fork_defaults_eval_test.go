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

// TestEval_ForkDialog_BuiltInDefaultsVisibleOnOpen proves that, with NO
// [fork] config present, the fork dialog opens on a git project with the
// built-in defaults (worktree + carry-state) ALREADY checked while
// include-gitignored renders unchecked — i.e. the user SEES the safe default
// and can opt into the unbounded gitignored copy without it firing silently.
// This is the disclosure-visible contract that pure getter tests can't express.
func TestEval_ForkDialog_BuiltInDefaultsVisibleOnOpen(t *testing.T) {
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

	// State getters: defaults seeded with zero interaction. Worktree +
	// carry-parent-state default ON; include-gitignored is opt-in (off).
	if !d.IsWorktreeEnabled() {
		t.Error("worktree must default ON in a git repo with no [fork] config")
	}
	if !d.IsWithStateEnabled() {
		t.Error("carry-parent-state must default ON with no [fork] config")
	}
	if d.IsWithStateAndGitignoredEnabled() {
		t.Error("include-gitignored must default OFF with no [fork] config (opt-in)")
	}

	// Rendered, user-visible disclosure: carry-state is checked on open;
	// include-gitignored renders unchecked so its cost is opt-in.
	view := d.View()
	if !strings.Contains(view, "[x] Carry parent state") {
		t.Errorf("dialog must render %q checked on open; view:\n%s", "[x] Carry parent state", view)
	}
	if !strings.Contains(view, "[ ] Include gitignored files") {
		t.Errorf("dialog must render %q unchecked on open; view:\n%s", "[ ] Include gitignored files", view)
	}
}
