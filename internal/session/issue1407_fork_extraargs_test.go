package session

// Regression tests for issue #1407 — fork inherits the parent's ExtraArgs
// only inside the one-shot baked fork command. Because the tokens were never
// persisted on the forked instance record, they silently dropped on the
// fork's first restart (buildClaudeResumeCommand reads the fork's OWN empty
// ExtraArgs), and a fork-of-a-fork never got them at all (the fork builder
// reads the parent's persisted ExtraArgs, which is empty on a gen-1 fork).
//
// Contract under test: CreateForkedInstanceWithOptions persists an
// independent copy of the parent's ExtraArgs onto the fork, exactly like it
// already persists ClaudeOptions via SetClaudeOptions.

import (
	"strings"
	"testing"
	"time"
)

// newForkableParent returns a claude parent with conversation data so the
// fork builder accepts it, carrying the given extra args.
func newForkableParent(t *testing.T, extraArgs []string) *Instance {
	t.Helper()
	parent := NewInstanceWithTool("ea-fork-parent", t.TempDir(), "claude")
	parent.ClaudeSessionID = "11111111-1111-1111-1111-111111111111"
	parent.ClaudeDetectedAt = time.Now()
	parent.ExtraArgs = extraArgs
	return parent
}

// TestIssue1407_ForkPersistsParentExtraArgs is the core regression: the fork
// record itself must carry the parent's ExtraArgs so every later
// start/restart rebuild includes them.
func TestIssue1407_ForkPersistsParentExtraArgs(t *testing.T) {
	extraArgsTestEnv(t)

	parent := newForkableParent(t, []string{"--verbose", "--debug"})

	forked, cmd, err := parent.CreateForkedInstanceWithOptions("ea-fork", "", nil)
	if err != nil {
		t.Fatalf("CreateForkedInstanceWithOptions: %v", err)
	}

	// Existing invariant guard: the baked one-shot fork command inherits
	// the flags (this already worked pre-fix) — and exactly ONCE each:
	// persisting the tokens on the fork must not double-emit them in the
	// command built from the parent.
	for _, tok := range []string{"--verbose", "--debug"} {
		if n := strings.Count(cmd, tok); n != 1 {
			t.Fatalf("baked fork command must contain %s exactly once, found %d times:\n%s", tok, n, cmd)
		}
	}

	// The fix: the fork's OWN record must persist the tokens.
	if len(forked.ExtraArgs) != 2 || forked.ExtraArgs[0] != "--verbose" || forked.ExtraArgs[1] != "--debug" {
		t.Fatalf(
			"fork did not persist parent ExtraArgs (issue #1407): got %v, want [--verbose --debug]",
			forked.ExtraArgs,
		)
	}

	// Must be an independent copy — mutating the parent's slice afterwards
	// must not alias into the fork's persisted record.
	parent.ExtraArgs[0] = "--mutated"
	if forked.ExtraArgs[0] != "--verbose" {
		t.Fatalf(
			"fork ExtraArgs aliases the parent slice; want independent copy, got %v",
			forked.ExtraArgs,
		)
	}
}

// TestIssue1407_ForkRestartKeepsExtraArgs reproduces symptom 1 of the issue:
// after the one-shot fork command has run, every restart rebuilds the command
// via buildClaudeResumeCommand from the fork's own ExtraArgs. Pre-fix this
// dropped the flags silently.
func TestIssue1407_ForkRestartKeepsExtraArgs(t *testing.T) {
	extraArgsTestEnv(t)

	parent := newForkableParent(t, []string{"--verbose", "--debug"})

	forked, _, err := parent.CreateForkedInstanceWithOptions("ea-fork-restart", "", nil)
	if err != nil {
		t.Fatalf("CreateForkedInstanceWithOptions: %v", err)
	}

	// Simulate the fork having started once: the transient sentinel is
	// consumed and the fork has its own conversation id.
	forked.IsForkAwaitingStart = false
	forked.ClaudeSessionID = "22222222-2222-2222-2222-222222222222"

	cmd := forked.buildClaudeResumeCommand()
	if !strings.Contains(cmd, "--verbose") || !strings.Contains(cmd, "--debug") {
		t.Fatalf(
			"fork restart dropped inherited extra args (issue #1407 symptom 1); got:\n%s",
			cmd,
		)
	}
}

// TestIssue1407_ForkOfForkInheritsExtraArgs reproduces symptom 2: a
// second-generation fork builds its baked command from ITS parent's (the
// gen-1 fork's) persisted ExtraArgs. Pre-fix that was empty, so gen-2 never
// saw the flags even in the initial command.
func TestIssue1407_ForkOfForkInheritsExtraArgs(t *testing.T) {
	extraArgsTestEnv(t)

	parent := newForkableParent(t, []string{"--verbose", "--debug"})

	gen1, _, err := parent.CreateForkedInstanceWithOptions("ea-fork-gen1", "", nil)
	if err != nil {
		t.Fatalf("gen1 fork: %v", err)
	}
	// Simulate gen1 having started and acquired its own conversation.
	gen1.IsForkAwaitingStart = false
	gen1.ClaudeSessionID = "22222222-2222-2222-2222-222222222222"
	gen1.ClaudeDetectedAt = time.Now()

	gen2, cmd2, err := gen1.CreateForkedInstanceWithOptions("ea-fork-gen2", "", nil)
	if err != nil {
		t.Fatalf("gen2 fork: %v", err)
	}

	if !strings.Contains(cmd2, "--verbose") || !strings.Contains(cmd2, "--debug") {
		t.Fatalf(
			"fork-of-fork baked command missing extra args (issue #1407 symptom 2); got:\n%s",
			cmd2,
		)
	}
	if len(gen2.ExtraArgs) != 2 || gen2.ExtraArgs[0] != "--verbose" || gen2.ExtraArgs[1] != "--debug" {
		t.Fatalf("gen2 fork did not persist ExtraArgs: got %v", gen2.ExtraArgs)
	}
}

// TestIssue1407_ForkWithEmptyParentExtraArgsStaysEmpty guards the no-op case:
// a parent without extra args must produce a fork whose ExtraArgs stays nil
// (omitempty keeps tool_data clean).
func TestIssue1407_ForkWithEmptyParentExtraArgsStaysEmpty(t *testing.T) {
	extraArgsTestEnv(t)

	parent := newForkableParent(t, nil)

	forked, _, err := parent.CreateForkedInstanceWithOptions("ea-fork-empty", "", nil)
	if err != nil {
		t.Fatalf("CreateForkedInstanceWithOptions: %v", err)
	}
	if forked.ExtraArgs != nil {
		t.Fatalf("fork of parent without ExtraArgs should keep nil, got %v", forked.ExtraArgs)
	}
}
