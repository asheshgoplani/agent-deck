// session_cmd_fork_state_test.go — CLI contract tests for
// `agent-deck session fork --with-state` (Task A4, closing gap 9 of the
// post-#1029 followup).
//
// These tests guard:
//
//  1. The four destination-rejection branches in handleSessionFork — the
//     "requires an explicit worktree branch" check for both --with-state
//     and --with-state-and-gitignored, plus the two
//     DestinationCollisionError surfaces ("already exists" and
//     "already has a worktree"). Each must be present and worded to give
//     the user an actionable next step ("choose a new destination branch
//     for --with-state").
//
//  2. The propagation contract — opts.WorktreeBranch carries the resolved
//     branch into ClaudeOptions, MaterializeWipFromParent is called with
//     the gitignored flag wired through, and the
//     sessionForkBeforeStartHook is invoked before forkedInst.Start() so
//     tests can capture the prepared fork without spawning a real tmux
//     session.
//
// Why structural assertions instead of end-to-end handler invocation:
// handleSessionFork calls os.Exit on every error path, and there is no
// runMain/TestHelperProcess subprocess harness in this package. The
// existing precedent for cmd-level invariant assertions is
// session_remove_kill_test.go's extractFuncBody approach — we follow it.
// session.ClaudeOptions also doesn't carry WithState / IncludeGitignored
// fields (those live as local vars in the handler and flow into
// MaterializeWipFromParent's last argument), so the propagation test
// asserts the actual wiring rather than nonexistent struct fields.

package main

import (
	"os"
	"strings"
	"testing"
)

// foldSpaces collapses runs of whitespace so multi-line source can be matched
// with a single literal substring.
func foldSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func mustExtractHandleSessionFork(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile("session_cmd.go")
	if err != nil {
		t.Fatalf("read session_cmd.go: %v", err)
	}
	body := extractFuncBody(string(src), "handleSessionFork")
	if body == "" {
		t.Fatalf("could not extract handleSessionFork body — file layout changed?")
	}
	return body
}

// TestSessionFork_WithStateRequiresExplicitDestinationBranch locks in the
// validation that --with-state without -w / --worktree is rejected with a
// user-actionable message. Without this, the handler would silently fall
// into the no-worktree path and materialize nothing.
func TestSessionFork_WithStateRequiresExplicitDestinationBranch(t *testing.T) {
	body := mustExtractHandleSessionFork(t)
	folded := foldSpaces(body)

	// Both --with-state and --with-state-and-gitignored share the same
	// wantState gate, so a single check covers both flags' behavior.
	if !strings.Contains(folded, "wantState := *withState || *withStateGitignored") {
		t.Errorf("handleSessionFork must compute wantState as the union of "+
			"--with-state and --with-state-and-gitignored; body did not contain "+
			"the expected expression. Body (folded):\n%s", folded)
	}
	if !strings.Contains(folded, `--with-state requires an explicit worktree branch (-w/--worktree)`) {
		t.Errorf("handleSessionFork must reject --with-state without a worktree "+
			"branch with a user-actionable error. Body (folded):\n%s", folded)
	}
}

// TestSessionFork_WithStateAndGitignoredRequiresExplicitDestinationBranch —
// the implies-wantState union (above) means the same check guards
// --with-state-and-gitignored. We assert it explicitly so a future split of
// the two paths must also split the test.
func TestSessionFork_WithStateAndGitignoredRequiresExplicitDestinationBranch(t *testing.T) {
	body := mustExtractHandleSessionFork(t)
	folded := foldSpaces(body)

	// The withStateGitignored flag must be referenced inside the wantState
	// expression, which then drives the explicit-branch check.
	if !strings.Contains(folded, "*withStateGitignored") {
		t.Errorf("handleSessionFork must reference the withStateGitignored flag; "+
			"folded body:\n%s", folded)
	}
	if !strings.Contains(folded, "wantState && wtBranch ==") {
		t.Errorf("handleSessionFork must gate explicit-branch enforcement on "+
			"wantState (so --with-state-and-gitignored takes the same path); "+
			"folded body:\n%s", folded)
	}
}

// TestSessionFork_WithState_RejectsExistingDestinationBranch — the
// DestinationCollisionError(BranchExists) branch must produce a message
// that names the branch and tells the user what to do.
func TestSessionFork_WithState_RejectsExistingDestinationBranch(t *testing.T) {
	body := mustExtractHandleSessionFork(t)
	folded := foldSpaces(body)

	if !strings.Contains(folded, "git.CollisionBranchExists") {
		t.Errorf("handleSessionFork must handle CollisionBranchExists explicitly; "+
			"folded body:\n%s", folded)
	}
	if !strings.Contains(folded, "already exists") {
		t.Errorf("handleSessionFork must mention 'already exists' on branch collision; "+
			"folded body:\n%s", folded)
	}
	if !strings.Contains(folded, "choose a new destination branch for --with-state") {
		t.Errorf("handleSessionFork must give actionable guidance "+
			"('choose a new destination branch for --with-state') on collision; "+
			"folded body:\n%s", folded)
	}
}

// TestSessionFork_WithState_RejectsExistingDestinationWorktree — the
// DestinationCollisionError(WorktreeExists) branch must produce a message
// that names the existing worktree path and tells the user what to do.
func TestSessionFork_WithState_RejectsExistingDestinationWorktree(t *testing.T) {
	body := mustExtractHandleSessionFork(t)
	folded := foldSpaces(body)

	if !strings.Contains(folded, "git.CollisionWorktreeExists") {
		t.Errorf("handleSessionFork must handle CollisionWorktreeExists explicitly; "+
			"folded body:\n%s", folded)
	}
	if !strings.Contains(folded, "already has a worktree") {
		t.Errorf("handleSessionFork must mention 'already has a worktree' on "+
			"worktree collision; folded body:\n%s", folded)
	}
	if !strings.Contains(folded, "choose a new destination branch for --with-state") {
		t.Errorf("handleSessionFork must give actionable guidance on worktree "+
			"collision; folded body:\n%s", folded)
	}
}

// TestSessionFork_WithStateOptionsPropagatedBeforeStart locks in three
// invariants of the with-state path's wiring into ClaudeOptions and the
// MaterializeWipFromParent call site:
//
//  1. opts.WorktreeBranch is set to the resolved wtBranch so the forked
//     session knows which branch it lives on.
//  2. MaterializeWipFromParent is called with *withStateGitignored as the
//     includeIgnored argument, so --with-state-and-gitignored actually
//     flips on ignored-file inclusion.
//  3. The sessionForkBeforeStartHook is invoked before forkedInst.Start(),
//     so contract tests can short-circuit before tmux mutation.
//
// (ClaudeOptions has no WithState / IncludeGitignored fields; the with-state
// behavior is expressed at the call site of MaterializeWipFromParent.)
func TestSessionFork_WithStateOptionsPropagatedBeforeStart(t *testing.T) {
	body := mustExtractHandleSessionFork(t)
	folded := foldSpaces(body)

	if !strings.Contains(folded, "opts.WorktreeBranch = wtBranch") {
		t.Errorf("handleSessionFork must propagate the resolved branch into "+
			"opts.WorktreeBranch; folded body:\n%s", folded)
	}

	// MaterializeWipFromParent must be called with the gitignored flag —
	// that's how `--with-state-and-gitignored` becomes observable behavior.
	if !strings.Contains(folded, "git.MaterializeWipFromParent(inst.ProjectPath, worktreePath, *withStateGitignored)") {
		t.Errorf("handleSessionFork must wire *withStateGitignored as the "+
			"includeIgnored argument to MaterializeWipFromParent; folded body:\n%s",
			folded)
	}

	// The hook must fire BEFORE forkedInst.Start() so tests can capture the
	// prepared fork without spawning a real tmux session.
	hookIdx := strings.Index(folded, "sessionForkBeforeStartHook(inst, forkedInst, opts)")
	if hookIdx < 0 {
		t.Fatalf("handleSessionFork must invoke sessionForkBeforeStartHook(inst, "+
			"forkedInst, opts); folded body:\n%s", folded)
	}
	startIdx := strings.Index(folded, "forkedInst.Start()")
	if startIdx < 0 {
		t.Fatalf("handleSessionFork must call forkedInst.Start(); folded body:\n%s",
			folded)
	}
	if hookIdx > startIdx {
		t.Errorf("sessionForkBeforeStartHook must be invoked BEFORE "+
			"forkedInst.Start() (hook idx %d > start idx %d); folded body:\n%s",
			hookIdx, startIdx, folded)
	}

	// The hook path must short-circuit (return) so persistence and tmux
	// mutation never run when the hook is set.
	hookBlock := folded[hookIdx:]
	cutEnd := len(hookBlock)
	if idx := strings.Index(hookBlock, "forkedInst.Start()"); idx >= 0 {
		cutEnd = idx
	}
	if !strings.Contains(hookBlock[:cutEnd], "return") {
		t.Errorf("handleSessionFork must return immediately after invoking "+
			"sessionForkBeforeStartHook to short-circuit the Start() path; "+
			"folded segment:\n%s", hookBlock[:cutEnd])
	}
}

// TestSessionForkBeforeStartHook_NilInProduction is a belt-and-braces check:
// the production binary must leave the hook nil so accidental test imports
// can't inject behavior into a real fork. Tests that need the hook assign
// it inside a t.Cleanup that restores nil.
func TestSessionForkBeforeStartHook_NilInProduction(t *testing.T) {
	if sessionForkBeforeStartHook != nil {
		t.Fatal("sessionForkBeforeStartHook must be nil at package init " +
			"(a previous test leaked an assignment without restoring nil)")
	}
}
