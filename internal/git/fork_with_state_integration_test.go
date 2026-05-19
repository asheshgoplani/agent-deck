package git

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// bareLayoutWithDivergentParent builds the canonical agent-deck bare-repo
// layout used in production and seeds it with a parent worktree whose HEAD
// has diverged from main:
//
//	projectRoot/
//	├── .bare/              (the bare repo, seeded with one commit on main)
//	├── worktree1/          (linked worktree on main — the "seed" HEAD)
//	└── parent-wt/          (linked worktree on parent-branch — the divergent parent)
//
// The parent worktree has one extra commit on top of main and (optionally)
// some dirty WIP. Returns project root, parent worktree path, seed HEAD,
// and parent HEAD (already verified to differ).
//
// Shared by both A6 (fork-with-state correctness) and A7 (setup-hook
// observation) — keep changes here aware of both call sites.
func bareLayoutWithDivergentParent(t *testing.T, withWIP bool) (projectRoot, parentDir, seedHead, parentHead string) {
	t.Helper()

	projectRoot, bareDir, worktrees := createBareRepoLayout(t, "worktree1")
	seedDir := worktrees[0]
	seedHead = strings.TrimSpace(runGit(t, seedDir, "rev-parse", "HEAD"))

	// Create a linked parent worktree on a fresh branch off main, then add a
	// commit so its HEAD diverges from main. The project root has no .git;
	// invoke worktree add from inside .bare/ (the actual git dir).
	parentDir = filepath.Join(projectRoot, "parent-wt")
	runGit(t, bareDir, "worktree", "add", "-b", "parent-branch", parentDir, "main")
	// Per-worktree identity (the bare repo has none configured).
	runGit(t, parentDir, "config", "user.email", "test@test.com")
	runGit(t, parentDir, "config", "user.name", "Test User")

	if err := os.WriteFile(filepath.Join(parentDir, "README.md"), []byte("parent change\n"), 0o644); err != nil {
		t.Fatalf("write parent README: %v", err)
	}
	runGit(t, parentDir, "commit", "-am", "parent commit")

	parentHead = strings.TrimSpace(runGit(t, parentDir, "rev-parse", "HEAD"))
	if seedHead == parentHead {
		t.Fatal("setup invariant: seed and parent HEAD should differ after parent commit")
	}

	if withWIP {
		// Untracked WIP file — exercises the copyUntrackedFromParent path of
		// MaterializeWipFromParent.
		if err := os.WriteFile(filepath.Join(parentDir, "wip.txt"), []byte("parent-wip\n"), 0o644); err != nil {
			t.Fatalf("write parent WIP: %v", err)
		}
	}

	return projectRoot, parentDir, seedHead, parentHead
}

// TestForkWithState_BareRepoLayoutLinkedParentWorktree is an integration test
// for the bare-repo project layout: the project root contains `.bare/` (the
// actual bare repository), and the parent agent-deck session lives in a
// LINKED worktree (a sibling directory pointing into .bare). When the user
// runs `agent-deck session fork --with-state`, the new fork worktree MUST be
// anchored at the parent worktree's HEAD, NOT at the bare repo's default
// branch HEAD or any other worktree's HEAD.
//
// Closes gap 6 from the post-merge gap analysis.
func TestForkWithState_BareRepoLayoutLinkedParentWorktree(t *testing.T) {
	projectRoot, parentDir, seedHead, parentHead := bareLayoutWithDivergentParent(t, true /* withWIP */)

	// Resolve parent HEAD via the production helper (mirrors handleSessionFork).
	parentHeadResolved, err := HeadCommit(parentDir)
	if err != nil {
		t.Fatalf("HeadCommit(parentDir): %v", err)
	}
	if parentHeadResolved != parentHead {
		t.Fatalf("HeadCommit returned %s, want %s", parentHeadResolved, parentHead)
	}

	// handleSessionFork calls git.GetWorktreeBaseRoot first to find the repo
	// root for the fork — mirror that resolution explicitly so the test fails
	// loudly if the layout convention shifts.
	baseRoot, err := GetWorktreeBaseRoot(parentDir)
	if err != nil {
		t.Fatalf("GetWorktreeBaseRoot(parentDir): %v", err)
	}
	resolvedProjectRoot, _ := filepath.EvalSymlinks(projectRoot)
	resolvedBaseRoot, _ := filepath.EvalSymlinks(baseRoot)
	if resolvedBaseRoot != resolvedProjectRoot {
		t.Fatalf("GetWorktreeBaseRoot(parentDir) = %q, want project root %q", resolvedBaseRoot, resolvedProjectRoot)
	}

	// Create the fork worktree anchored at the parent HEAD via the new helper.
	forkDir := filepath.Join(projectRoot, "fork-wt")
	createdBranch, err := CreateWorktreeAtStartPoint(baseRoot, forkDir, "fork/bare-layout", parentHeadResolved)
	if err != nil {
		t.Fatalf("CreateWorktreeAtStartPoint: %v", err)
	}
	if !createdBranch {
		t.Fatal("CreateWorktreeAtStartPoint returned createdBranch=false for a new branch")
	}

	// Materialize parent's WIP into the fork.
	if err := MaterializeWipFromParent(parentDir, forkDir, false); err != nil {
		t.Fatalf("MaterializeWipFromParent: %v", err)
	}

	// CONTRACT 1: fork HEAD must equal parent HEAD, NOT seed HEAD.
	// This is the bug class — fork being anchored at the bare repo's default
	// branch (seed HEAD) instead of the parent worktree's HEAD.
	forkHead := strings.TrimSpace(runGit(t, forkDir, "rev-parse", "HEAD"))
	if forkHead != parentHead {
		t.Fatalf("fork HEAD = %s, want parent HEAD %s (seed HEAD %s)\nfork was anchored at wrong commit",
			forkHead, parentHead, seedHead)
	}

	// CONTRACT 2: parent WIP must be present in the fork worktree.
	wipBytes, err := os.ReadFile(filepath.Join(forkDir, "wip.txt"))
	if err != nil {
		t.Fatalf("fork missing parent WIP file: %v", err)
	}
	if string(wipBytes) != "parent-wip\n" {
		t.Fatalf("fork WIP content mismatch: got %q, want %q", string(wipBytes), "parent-wip\n")
	}
}

// TestForkWithState_SetupHookObservesMaterializedState verifies the
// orchestration order: setup hook runs AFTER MaterializeWipFromParent, so the
// hook sees the realized working tree (parent's WIP). This pins the git-layer
// helper sequence used by the CLI handler; command-layer ordering is covered by
// the eval tests in tests/eval/session/fork_with_state_test.go.
//
// Closes gap 7 from the post-merge gap analysis.
func TestForkWithState_SetupHookObservesMaterializedState(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("setup hook test uses /bin/sh")
	}

	projectRoot, parentDir, _, parentHead := bareLayoutWithDivergentParent(t, true /* withWIP */)

	// Install a setup hook at <projectRoot>/.agent-deck/worktree-setup.sh —
	// the canonical location FindWorktreeSetupScript looks at when repoDir
	// is the project root (mirrors what handleSessionFork passes).
	scriptDir := filepath.Join(projectRoot, ".agent-deck")
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		t.Fatalf("mkdir setup-script dir: %v", err)
	}

	// Marker lives outside the worktree so materialization can't accidentally
	// create or clobber it.
	marker := filepath.Join(t.TempDir(), "setup-observed.txt")

	// Setup script (cwd = worktree per RunWorktreeSetupScript) reads parent's
	// WIP file from the materialized tree and writes its content to the marker.
	// If the hook ran BEFORE materialization, wip.txt would not exist yet and
	// the script would write NO_WIP_OBSERVED instead.
	script := fmt.Sprintf("#!/bin/sh\nset -e\nif [ -f wip.txt ]; then\n  cat wip.txt > %q\nelse\n  echo NO_WIP_OBSERVED > %q\nfi\n", marker, marker)
	scriptPath := filepath.Join(scriptDir, "worktree-setup.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write setup script: %v", err)
	}

	// Run the full fork-with-state sequence in handleSessionFork's order:
	// CreateWorktreeAtStartPoint → MaterializeWipFromParent →
	// ProcessWorktreeInclude → RunWorktreeSetupAfterCreate.
	forkDir := filepath.Join(projectRoot, "fork-observation-wt")

	if _, err := CreateWorktreeAtStartPoint(projectRoot, forkDir, "fork/observation", parentHead); err != nil {
		t.Fatalf("CreateWorktreeAtStartPoint: %v", err)
	}
	if err := MaterializeWipFromParent(parentDir, forkDir, false); err != nil {
		t.Fatalf("MaterializeWipFromParent: %v", err)
	}
	if err := ProcessWorktreeInclude(projectRoot, forkDir, os.Stderr); err != nil {
		t.Logf("ProcessWorktreeInclude (non-fatal): %v", err)
	}
	if err := RunWorktreeSetupAfterCreate(projectRoot, forkDir, os.Stdout, os.Stderr, 0 /* unlimited */); err != nil {
		t.Fatalf("RunWorktreeSetupAfterCreate: %v", err)
	}

	// The marker must contain the materialized WIP content. If the hook ran
	// before materialization, the marker would be missing, empty, or contain
	// NO_WIP_OBSERVED.
	observed, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("marker missing: %v (setup hook may not have run)", err)
	}
	wantContent := "parent-wip\n" // from bareLayoutWithDivergentParent(withWIP=true)
	if string(observed) != wantContent {
		t.Fatalf("setup hook observed wrong content:\ngot:  %q\nwant: %q\n(this means the hook ran before MaterializeWipFromParent)", observed, wantContent)
	}
}
