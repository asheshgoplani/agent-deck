package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The comprehensive quick-fork default forces WithState=true for every repo, but
// with-state materialization is git-only — the downstream fork path rejects it on
// jujutsu ("--with-state is only supported for git repositories"). So the default
// `f` keystroke regressed non-git backends from "create the supported jj workspace"
// to "error". gateForkStateForBackend must disable with-state on non-git backends
// so quick fork degrades to a plain (workspace) fork. Proper jj with-state support
// is tracked in #1305.

// nonGit dir is neither git nor jj → vcsbackend.Detect errors → state must be off.
func TestGateForkStateForBackend_NonGitDegradesWithState(t *testing.T) {
	src := session.NewInstanceWithTool("feat", t.TempDir(), "claude")
	in := quickForkInputs(src, session.ForkSettings{}, false)
	require.True(t, in.Plan.WithState, "precondition: comprehensive default forces with-state")
	require.True(t, in.Plan.WithIgnored, "precondition: comprehensive default forces with-ignored")

	out := gateForkStateForBackend(in, src.ProjectPath)

	assert.False(t, out.Plan.WithState, "with-state must be gated off on a non-git backend")
	assert.False(t, out.Plan.WithIgnored, "with-ignored must follow with-state off")
	assert.True(t, out.Plan.Worktree, "worktree (workspace) fork stays enabled — only state is gated")
}

// TestResolveQuickForkSpec_NonGitRepoDropsWithState guards the f-path itself
// (the seam quickForkSession forks from), not just the gate helper: running the
// default quick fork against a non-git repo must not carry with-state. This is
// the maintainer-requested "default quick-fork against a non-Git repo" guard.
func TestResolveQuickForkSpec_NonGitRepoDropsWithState(t *testing.T) {
	src := session.NewInstanceWithTool("feat", t.TempDir(), "claude")

	spec := resolveQuickForkSpec(src, session.ForkSettings{})

	assert.False(t, spec.Plan.WithState, "default quick fork must not force with-state on a non-git repo")
	assert.False(t, spec.Plan.WithIgnored)
	assert.True(t, spec.Plan.Worktree, "worktree (workspace) fork stays enabled")
}

// TestResolveQuickForkSpec_GitRepoKeepsWithState confirms the f-path keeps the
// comprehensive with-state default on a real git repo.
func TestResolveQuickForkSpec_GitRepoKeepsWithState(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	repo := t.TempDir()
	gitMustUI(t, repo, "init", "-q")
	src := session.NewInstanceWithTool("feat", repo, "claude")

	spec := resolveQuickForkSpec(src, session.ForkSettings{})

	assert.True(t, spec.Plan.WithState, "git repos keep the with-state default through the f path")
	assert.True(t, spec.Plan.WithIgnored)
}

// A real git repo must keep the with-state default untouched.
func TestGateForkStateForBackend_GitRepoKeepsWithState(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	repo := t.TempDir()
	gitMustUI(t, repo, "init", "-q")

	src := session.NewInstanceWithTool("feat", repo, "claude")
	in := quickForkInputs(src, session.ForkSettings{}, false)

	out := gateForkStateForBackend(in, repo)

	assert.True(t, out.Plan.WithState, "git repos keep the with-state default")
	assert.True(t, out.Plan.WithIgnored, "git repos keep the with-ignored default")
}

// A colocated jujutsu repo must degrade just like any non-git backend. Skipped
// where the jj binary is absent (most CI); the non-git case above guards the
// detection-error branch deterministically.
func TestGateForkStateForBackend_JujutsuDegradesWithState(t *testing.T) {
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj not on PATH")
	}
	repo := t.TempDir()
	jj := exec.Command("jj", "git", "init", "--colocate")
	jj.Dir = repo
	jj.Env = append(os.Environ(), "JJ_CONFIG="+filepath.Join(repo, "nonexistent-jj-config.toml"))
	if out, err := jj.CombinedOutput(); err != nil {
		t.Skipf("jj git init --colocate failed in this env: %v\n%s", err, out)
	}

	src := session.NewInstanceWithTool("feat", repo, "claude")
	in := quickForkInputs(src, session.ForkSettings{}, false)

	out := gateForkStateForBackend(in, repo)

	assert.False(t, out.Plan.WithState, "jujutsu must degrade to a plain workspace fork")
	assert.False(t, out.Plan.WithIgnored)
	assert.True(t, out.Plan.Worktree)
}
