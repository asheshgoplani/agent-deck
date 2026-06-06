package ui

import (
	"testing"

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
