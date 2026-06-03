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
//     sessionForkBeforeStartHook is invoked with the resolved
//     git.WorktreeStateOptions before forkedInst.Start() so tests can capture
//     the prepared fork without spawning a real tmux session.
//
// Why structural assertions instead of end-to-end handler invocation:
// handleSessionFork calls os.Exit on every error path, and there is no
// runMain/TestHelperProcess subprocess harness in this package. The
// existing precedent for cmd-level invariant assertions is
// session_remove_kill_test.go's extractFuncBody approach — we follow it.
// session.ClaudeOptions also doesn't carry WithState / IncludeGitignored
// fields (upstream routes those through git.WorktreeStateOptions), so the
// hook exposes the resolved git-layer state directly.

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/git"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/testutil"
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

func initGitRepoForForkStateTest(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	for _, args := range [][]string{
		{"init"},
		{"commit", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(testutil.CleanGitEnv(os.Environ()),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, string(out))
		}
	}
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
// invariants of the with-state path's wiring into ClaudeOptions,
// git.WorktreeStateOptions, and the MaterializeWipFromParent call site:
//
//  1. opts.WorktreeBranch is set to the resolved wtBranch so the forked
//     session knows which branch it lives on.
//  2. MaterializeWipFromParent is called with *withStateGitignored as the
//     includeIgnored argument, so --with-state-and-gitignored actually
//     flips on ignored-file inclusion.
//  3. The sessionForkBeforeStartHook is invoked with the resolved
//     git.WorktreeStateOptions before forkedInst.Start(), so contract tests can
//     short-circuit before tmux mutation.
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
	hookIdx := strings.Index(folded, "sessionForkBeforeStartHook(inst, forkedInst, git.WorktreeStateOptions{WithState: wantState, WithIgnored: *withStateGitignored})")
	if hookIdx < 0 {
		t.Fatalf("handleSessionFork must invoke sessionForkBeforeStartHook(inst, "+
			"forkedInst, git.WorktreeStateOptions{WithState: wantState, "+
			"WithIgnored: *withStateGitignored}); folded body:\n%s", folded)
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

func TestSessionFork_WithStateHookCapturesResolvedStateBeforeStart(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)

	configDir := filepath.Join(home, ".agent-deck")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("[worktree]\nbranch_prefix = \"\"\ndefault_location = \"sibling\"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	session.ClearUserConfigCache()

	repo := filepath.Join(home, "repo")
	initGitRepoForForkStateTest(t, repo)

	parent := session.NewInstanceWithGroupAndTool("parent", repo, "evalgrp", "claude")
	parent.ClaudeSessionID = "00000000-0000-4000-8000-000000000001"
	parent.ClaudeDetectedAt = time.Now()

	profile := "fork_state_hook"
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	if err := storage.SaveWithGroups([]*session.Instance{parent}, session.NewGroupTreeWithGroups([]*session.Instance{parent}, nil)); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}

	var capturedParent *session.Instance
	var capturedFork *session.Instance
	var capturedState git.WorktreeStateOptions
	oldHook := sessionForkBeforeStartHook
	sessionForkBeforeStartHook = func(parent *session.Instance, forked *session.Instance, state git.WorktreeStateOptions) {
		capturedParent = parent
		capturedFork = forked
		capturedState = state
	}
	t.Cleanup(func() { sessionForkBeforeStartHook = oldHook })

	handleSessionFork(profile, []string{
		"parent",
		"--with-state-and-gitignored",
		"-w", "fork/hook-state",
		"-t", "forked",
	})

	if capturedParent == nil || capturedParent.ID != parent.ID {
		t.Fatalf("hook captured parent = %+v, want parent %s", capturedParent, parent.ID)
	}
	if capturedFork == nil {
		t.Fatal("hook did not capture forked instance")
	}
	if capturedFork.WorktreeBranch != "fork/hook-state" {
		t.Fatalf("forked WorktreeBranch = %q, want fork/hook-state", capturedFork.WorktreeBranch)
	}
	if !capturedState.WithState || !capturedState.WithIgnored {
		t.Fatalf("captured state = %+v, want WithState and WithIgnored true", capturedState)
	}
}

// TestSessionFork_WithState_RefusesMidOpWithActionableHint_StructuralGuard
// pins the gap 6 flow: handler must call git.DetectInProgressOperation on the
// parent's ProjectPath, build a map of abort commands covering all 5 in-flight
// operation kinds, and emit the actionable "finish or abort" message before
// worktree creation. If a future contributor drops a kind from the abortCmd
// map, this test fails so the regression cannot land silently.
func TestSessionFork_WithState_RefusesMidOpWithActionableHint_StructuralGuard(t *testing.T) {
	body := mustExtractHandleSessionFork(t)
	for _, marker := range []string{
		"git.DetectInProgressOperation(inst.ProjectPath)",
		`abortCmd := map[string]string`,
		"git rebase --abort",
		"git merge --abort",
		"git cherry-pick --abort",
		"git revert --abort",
		"git bisect reset",
		"parent session is mid-%s; finish or abort the %s before forking with state",
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("handleSessionFork missing required marker: %q", marker)
		}
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

// TestBranchCleanupHint_ShellQuotesPathAndBranch guards that the manual-cleanup
// hint is copy-paste-safe: repo root and branch names containing spaces or
// shell metacharacters must be quoted so the printed `git -C ... branch -D ...`
// fragment runs as a single argument each rather than word-splitting.
func TestBranchCleanupHint_ShellQuotesPathAndBranch(t *testing.T) {
	if got := branchCleanupHint(false, "/repo", "feature/x"); got != "" {
		t.Fatalf("expected empty hint when branch was not created, got %q", got)
	}

	got := branchCleanupHint(true, "/home/u/my repo", "feature/has space")
	if strings.Contains(got, "git -C /home/u/my repo ") {
		t.Errorf("repo root with a space was not quoted in hint: %q", got)
	}
	if strings.Contains(got, "branch -D feature/has space") {
		t.Errorf("branch name with a space was not quoted in hint: %q", got)
	}
	if !strings.Contains(got, "'/home/u/my repo'") {
		t.Errorf("expected repo root single-quoted, got %q", got)
	}
	if !strings.Contains(got, "'feature/has space'") {
		t.Errorf("expected branch name single-quoted, got %q", got)
	}
}
