package ui

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/stretchr/testify/assert"
)

func TestQuickForkInputs_DefaultsAndBranchSlug(t *testing.T) {
	src := session.NewInstanceWithTool("My Feature", "/tmp/proj", "claude")
	src.GroupPath = "team/x"
	fork := session.ForkSettings{} // comprehensive defaults

	in := quickForkInputs(src, fork, false /* parentSandboxed */)

	assert.Equal(t, "My Feature (fork)", in.Title)
	assert.Equal(t, "team/x", in.GroupPath)
	assert.Equal(t, "fork/my-feature", in.Branch)
	assert.True(t, in.Plan.Worktree)
	assert.True(t, in.Plan.WithState)
	assert.True(t, in.Plan.WithIgnored)
	assert.False(t, in.Plan.Sandbox)
}

func TestQuickForkInputs_BranchPrefixOverride(t *testing.T) {
	src := session.NewInstanceWithTool("Fix Bug", "/tmp/proj", "claude")
	prefix := "wip/"
	fork := session.ForkSettings{BranchPrefix: prefix}
	in := quickForkInputs(src, fork, false)
	assert.Equal(t, "wip/fix-bug", in.Branch)
}

func TestQuickForkInputs_BranchSlugUsesGitSanitizer(t *testing.T) {
	src := session.NewInstanceWithTool("Fix: Bug? 101", "/tmp/proj", "claude")
	in := quickForkInputs(src, session.ForkSettings{}, false)
	assert.Equal(t, "fork/fix-bug-101", in.Branch)
}

func TestQuickForkInputs_DockerAutoMatchesSandboxedParent(t *testing.T) {
	src := session.NewInstanceWithTool("svc", "/tmp/proj", "claude")
	in := quickForkInputs(src, session.ForkSettings{}, true /* parentSandboxed */)
	assert.True(t, in.Plan.Sandbox, "docker=auto + sandboxed parent -> sandbox on")
}

func TestForkInstanceDeps_OpenCodeUsesResolvedWorktreeDir(t *testing.T) {
	source := session.NewInstanceWithTool("oc parent", "/tmp/original", "opencode")
	source.OpenCodeSessionID = "ses_parent"
	source.OpenCodeDetectedAt = time.Now()

	opts := &session.ClaudeOptions{
		WorkDir:          "/tmp/original-wt",
		WorktreePath:     "/tmp/original-wt",
		WorktreeRepoRoot: "/tmp/original",
		WorktreeBranch:   "fork/oc-parent",
	}

	// Exercise the deps.createInstance wiring directly — this is the exact seam
	// Step 4 changes. Calling createInstance (not completeFork) keeps the test
	// lean: no DetectOpenCodeSession goroutine and no start/multi-repo machinery.
	// writeOpenCodeForkScript writes via os.CreateTemp, which works under any HOME.
	deps := defaultForkInstanceDeps()
	inst, err := deps.createInstance(source, "oc parent (fork)", "", opts)
	if err != nil {
		t.Fatalf("createInstance: %v", err)
	}
	if inst.ProjectPath != "/tmp/original-wt" {
		t.Fatalf("OpenCode fork ProjectPath = %q, want resolved worktree dir", inst.ProjectPath)
	}
	if inst.WorktreePath != "/tmp/original-wt" || inst.WorktreeRepoRoot != "/tmp/original" || inst.WorktreeBranch != "fork/oc-parent" {
		t.Fatalf("OpenCode fork worktree metadata not copied: %+v", inst)
	}
}
