//go:build eval_smoke

// Behavioral eval for `agent-deck session fork --with-state-and-gitignored`
// (issue #1029, Task A8 in the post-merge gap analysis).
//
// Closes gap 10 (CLI portion): a real-binary end-to-end test that proves the
// CLI handler in cmd/agent-deck/session_cmd.go actually wires
// MaterializeWipFromParent's includeIgnored=true through to disk when the
// user passes `--with-state-and-gitignored`. The companion unit tests in
// cmd/agent-deck/session_cmd_fork_state_test.go assert the source-level wiring
// (via extractFuncBody) but cannot prove the resulting binary actually
// materializes a gitignored file in the destination worktree — that is a
// behavioral disclosure contract, exactly what the eval harness exists to
// catch (RFC docs/rfc/EVALUATOR_HARNESS.md).
//
// Strategy notes:
//
//   - We seed a parent session via `add -c claude` and inject a fake
//     ClaudeSessionID via `session set <id> claude-session-id <uuid>`. The
//     SetField mutator at internal/session/mutators.go:228 sets
//     ClaudeDetectedAt = time.Now(), which satisfies CanFork()'s recency gate
//     without ever starting a real Claude process.
//   - The fork handler's worktree creation + MaterializeWipFromParent run
//     BEFORE forkedInst.Start() (see session_cmd.go:762-823 vs 855), so
//     destination worktree state exists on disk regardless of whether the
//     downstream Start() succeeds. We assert directly on the filesystem and
//     git state and tolerate a non-zero exit from the binary (Start() likely
//     fails because no real claude binary is on PATH). This isolates the test
//     to gap 10's actual contract — materialize wiring — and stays robust to
//     drift in unrelated Start() plumbing.
//   - We pin `[worktree] branch_prefix = ""` so the requested `-w` value is
//     used verbatim, not auto-prefixed with the default "feature/".
package session_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/tests/eval/harness"
)

// TestEval_SessionForkWithState_RealBinary is the gap-10 CLI eval. Drives the
// compiled agent-deck binary against a scratch HOME and a real seeded git
// repo, runs `session fork ... --with-state-and-gitignored -w fork/eval-state`,
// and asserts the destination worktree exists with parent's full WIP
// (staged + unstaged + untracked + gitignored) materialized.
func TestEval_SessionForkWithState_RealBinary(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	sb := harness.NewSandbox(t)

	// Pin worktree config so `-w fork/eval-state` is not auto-prefixed and
	// lands at <repo>/.worktrees/fork-eval-state by default.
	cfgDir := filepath.Join(sb.Home, ".agent-deck")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir agent-deck config dir: %v", err)
	}
	cfgPath := filepath.Join(cfgDir, "config.toml")
	// `sibling` keeps the destination worktree OUT of the parent repo, so
	// our "parent must remain read-only" porcelain assertion doesn't have to
	// special-case a freshly-created .worktrees/ directory inside the repo.
	if err := os.WriteFile(cfgPath, []byte(`[worktree]
branch_prefix = ""
default_location = "sibling"
`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Set up a real git repo as the parent session's ProjectPath. The repo
	// needs a seed commit so HEAD resolves, and per-repo identity so commits
	// don't fail under sandboxed git.
	repoDir := filepath.Join(sb.Home, "proj")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	gitInit(t, repoDir)

	// .gitignore so `secrets.env` is a real gitignored file (not just
	// untracked). MaterializeWipFromParent must copy it ONLY when
	// includeIgnored=true.
	writeFile(t, repoDir, ".gitignore", "secrets.env\n")
	writeFile(t, repoDir, "README.md", "seed\n")
	gitMust(t, repoDir, "add", ".")
	gitMust(t, repoDir, "commit", "-m", "seed")

	// Build parent WIP: staged + unstaged + untracked + gitignored.
	writeFile(t, repoDir, "staged.txt", "staged content\n")
	gitMust(t, repoDir, "add", "staged.txt")

	appendFile(t, repoDir, "README.md", "\nunstaged edit\n")

	writeFile(t, repoDir, "untracked.txt", "untracked content\n")

	writeFile(t, repoDir, "secrets.env", "API_KEY=parent-secret\n")

	parentStatusBefore := gitPorcelain(t, repoDir)

	// Register parent session with the claude tool. The binary is never
	// invoked — we synthesize ClaudeSessionID below to satisfy CanFork().
	runBin(t, sb, "add", "-c", "claude", "-t", "parent", "-g", "evalgrp", repoDir)

	// Inject a fake but well-formed ClaudeSessionID so CanFork() returns true.
	// SetField also stamps ClaudeDetectedAt = time.Now() so the 5-minute
	// recency window in CanFork() is satisfied.
	runBin(t, sb, "session", "set", "parent", "claude-session-id",
		"00000000-0000-4000-8000-000000000001")

	// Run the fork. Per the fork-with-state contract, `-w` names the fresh
	// destination branch; `-b` is not required for with-state mode. We
	// tolerate non-zero exit because forkedInst.Start() at session_cmd.go:855
	// will try to spawn `claude` in tmux — that's downstream of gap 10's
	// contract (worktree creation + materialize, which run at 762-823).
	forkOut, forkErr := runBinTry(sb, "session", "fork", "parent",
		"--with-state-and-gitignored", "-w", "fork/eval-state",
		"-t", "fork-eval")

	// Parent invariant: read-only. MaterializeWipFromParent must not mutate
	// parent's index or working tree.
	parentStatusAfter := gitPorcelain(t, repoDir)
	if parentStatusAfter != parentStatusBefore {
		t.Fatalf("fork --with-state-and-gitignored mutated parent WIP.\nbefore:\n%s\nafter:\n%s",
			parentStatusBefore, parentStatusAfter)
	}

	// Destination worktree path. With default_location="sibling" and branch
	// sanitization (/ -> -), `fork/eval-state` lands at
	// `<repo>-fork-eval-state` alongside the parent repo. We verify via
	// `git worktree list` instead of recomputing the template so the
	// assertion stays robust to future template changes.
	forkPath := worktreePathForBranch(t, repoDir, "fork/eval-state")
	if forkPath == "" {
		// Surface the fork's combined output to make triage easy when the
		// destination is missing (e.g. validator rejected, branch collision).
		t.Fatalf("destination worktree for branch 'fork/eval-state' not found.\n"+
			"fork err: %v\nfork combined output:\n%s", forkErr, forkOut)
	}

	// 1. Branch invariant: destination is on `fork/eval-state`.
	gotBranch := strings.TrimSpace(gitOut(t, forkPath, "rev-parse", "--abbrev-ref", "HEAD"))
	if gotBranch != "fork/eval-state" {
		t.Errorf("destination HEAD branch: got %q, want %q", gotBranch, "fork/eval-state")
	}

	// 2. Materialize invariant: tracked WIP (staged, unstaged, untracked)
	// reproduced. Comparing porcelain status against parent's pre-fork
	// snapshot is the strongest single assertion — it covers byte-identity
	// of the index and working tree.
	childStatus := gitPorcelain(t, forkPath)
	if childStatus != parentStatusBefore {
		t.Errorf("destination status must mirror parent WIP.\nparent (before):\n%s\nchild:\n%s",
			parentStatusBefore, childStatus)
	}

	// 3. File-content sanity: spot-check that staged/untracked content
	// actually landed on disk. Catches a porcelain-equal-but-empty-file
	// regression that a status-only assertion would miss.
	mustHaveFile(t, forkPath, "staged.txt", "staged content\n")
	mustHaveFile(t, forkPath, "untracked.txt", "untracked content\n")
	if got := readFile(t, forkPath, "README.md"); !strings.Contains(got, "unstaged edit") {
		t.Errorf("destination README.md missing unstaged edit; got %q", got)
	}

	// 4. The discriminating assertion for --with-state-and-gitignored vs
	// --with-state: the gitignored file MUST be present. Without the
	// includeIgnored=true wiring, secrets.env would not be copied.
	mustHaveFile(t, forkPath, "secrets.env", "API_KEY=parent-secret\n")
}

// TestEval_SessionForkWithState_RejectsExistingBranch drives the real binary
// against a pre-existing destination branch and asserts the user-facing error
// text matches the spec's wording from ValidateForkWithStateDestination.
//
// This is the subprocess counterpart to
// TestSessionFork_WithState_RejectsExistingDestinationBranch in
// cmd/agent-deck/session_cmd_fork_state_test.go (source-introspection-based).
// Pinning the exact wording here guarantees the validator's user-facing
// message survives binary-level wiring (out.Error formatting, stderr routing).
func TestEval_SessionForkWithState_RejectsExistingBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	sb := harness.NewSandbox(t)

	// Pin worktree settings so `-w fork/collision-test` is used verbatim and
	// destination resolution matches what the validator will check.
	cfgDir := filepath.Join(sb.Home, ".agent-deck")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir agent-deck config dir: %v", err)
	}
	cfgPath := filepath.Join(cfgDir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(`[worktree]
branch_prefix = ""
default_location = "sibling"
`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	repoDir := filepath.Join(sb.Home, "proj")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	gitInit(t, repoDir)
	writeFile(t, repoDir, "README.md", "seed\n")
	gitMust(t, repoDir, "add", ".")
	gitMust(t, repoDir, "commit", "-m", "seed")

	// Pre-create the destination branch in the seed repo BEFORE running the
	// fork. The validator must trip on this and refuse.
	gitMust(t, repoDir, "branch", "fork/collision-test")

	// Register parent session and inject fake claude session id so CanFork()
	// is satisfied (same trick as the happy-path test).
	runBin(t, sb, "add", "-c", "claude", "-t", "parent", "-g", "evalgrp", repoDir)
	runBin(t, sb, "session", "set", "parent", "claude-session-id",
		"00000000-0000-4000-8000-000000000001")

	out, err := runBinTry(sb, "session", "fork", "parent",
		"--with-state", "-w", "fork/collision-test",
		"-t", "fork-collision")
	if err == nil {
		t.Fatalf("expected non-zero exit on destination-branch collision, got success.\noutput:\n%s", out)
	}

	wantMsg := "branch 'fork/collision-test' already exists; choose a new destination branch for --with-state"
	if !strings.Contains(out, wantMsg) {
		t.Fatalf("collision error wording mismatch.\nwant substring: %q\ngot stderr+stdout:\n%s", wantMsg, out)
	}
}

// TestEval_SessionForkWithState_RejectsExistingWorktree drives the real binary
// against a branch that already has a linked worktree. With-state must refuse
// this destination before the legacy reuse path; otherwise materialization is
// skipped and the fork may point at an unrelated existing worktree.
func TestEval_SessionForkWithState_RejectsExistingWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	sb := harness.NewSandbox(t)

	cfgDir := filepath.Join(sb.Home, ".agent-deck")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir agent-deck config dir: %v", err)
	}
	cfgPath := filepath.Join(cfgDir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(`[worktree]
branch_prefix = ""
default_location = "sibling"
`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	repoDir := filepath.Join(sb.Home, "proj")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	gitInit(t, repoDir)
	writeFile(t, repoDir, "README.md", "seed\n")
	gitMust(t, repoDir, "add", ".")
	gitMust(t, repoDir, "commit", "-m", "seed")

	existingWT := filepath.Join(sb.Home, "existing-wt")
	gitMust(t, repoDir, "worktree", "add", "-b", "fork/used", existingWT)
	existingWT = worktreePathForBranch(t, repoDir, "fork/used")
	if existingWT == "" {
		t.Fatal("setup invariant: expected pre-created worktree for fork/used")
	}

	runBin(t, sb, "add", "-c", "claude", "-t", "parent", "-g", "evalgrp", repoDir)
	runBin(t, sb, "session", "set", "parent", "claude-session-id",
		"00000000-0000-4000-8000-000000000001")

	out, err := runBinTry(sb, "session", "fork", "parent",
		"--with-state", "-w", "fork/used",
		"-t", "fork-used")
	if err == nil {
		t.Fatalf("expected non-zero exit on destination-worktree collision, got success.\noutput:\n%s", out)
	}

	wantMsg := fmt.Sprintf("branch 'fork/used' already has a worktree at %s; choose a new destination branch for --with-state", existingWT)
	if !strings.Contains(out, wantMsg) {
		t.Fatalf("worktree collision error wording mismatch.\nwant substring: %q\ngot stderr+stdout:\n%s", wantMsg, out)
	}
}

// TestEval_SessionForkWithState_RefusesMidRebaseParent drives the real binary
// against a parent in mid-rebase state and asserts the actionable error
// wording from gap 6: "parent session is mid-rebase; finish or abort the
// rebase before forking with state (cd <parent> && git rebase --abort)".
// This refusal now happens BEFORE worktree creation (via the exported
// git.DetectInProgressOperation helper), so there is no worktree to clean up
// on this code path. The cleanup-on-error assertions below additionally
// guard against regressions where a future contributor moves the detection
// AFTER CreateWorktreeAtStartPoint.
func TestEval_SessionForkWithState_RefusesMidRebaseParent(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	sb := harness.NewSandbox(t)

	cfgDir := filepath.Join(sb.Home, ".agent-deck")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir agent-deck config dir: %v", err)
	}
	cfgPath := filepath.Join(cfgDir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(`[worktree]
branch_prefix = ""
default_location = "sibling"
`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	repoDir := filepath.Join(sb.Home, "proj")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	gitInit(t, repoDir)
	writeFile(t, repoDir, "README.md", "seed\n")
	gitMust(t, repoDir, "add", ".")
	gitMust(t, repoDir, "commit", "-m", "seed")

	// Force the parent's working tree into mid-rebase state. The plain-file
	// `mkdir .git/rebase-merge` trick exercises the same refuseUnsafeParentState
	// path that a real conflicted rebase would (see
	// TestRefuseUnsafeParentState_Rebase_RegressionForFollowup in
	// internal/git/issue1029_edge_test.go for the same trick). We pick this
	// over a real conflict because real rebases are flaky across git versions
	// (conflict marker variance, interactive editor prompts).
	gitDir := filepath.Join(repoDir, ".git")
	if err := os.MkdirAll(filepath.Join(gitDir, "rebase-merge"), 0o755); err != nil {
		t.Fatalf("mkdir rebase-merge: %v", err)
	}

	runBin(t, sb, "add", "-c", "claude", "-t", "parent", "-g", "evalgrp", repoDir)
	runBin(t, sb, "session", "set", "parent", "claude-session-id",
		"00000000-0000-4000-8000-000000000001")

	out, err := runBinTry(sb, "session", "fork", "parent",
		"--with-state", "-w", "fork/midrebase", "-b",
		"-t", "fork-midrebase")
	if err == nil {
		t.Fatalf("expected non-zero exit when parent is mid-rebase, got success.\noutput:\n%s", out)
	}
	// Gap 6: handler must surface the full actionable wording with the parent
	// path and exact abort command, not just a "mid-rebase" substring.
	expected := fmt.Sprintf("parent session is mid-rebase; finish or abort the rebase before forking with state (cd %s && git rebase --abort)", repoDir)
	if !strings.Contains(out, expected) {
		t.Fatalf("expected actionable mid-rebase error %q, got:\n%s", expected, out)
	}

	// Cleanup-on-error: the destination branch must not have been left
	// behind. CreateWorktreeAtStartPoint creates the branch as a side-effect
	// of `git worktree add -b`, and the followup MaterializeWipFromParent
	// failure triggers `branch -D` cleanup at session_cmd.go:801.
	branchList := strings.TrimSpace(gitOut(t, repoDir, "branch", "--list", "fork/midrebase"))
	if branchList != "" {
		t.Errorf("destination branch must be cleaned up after mid-rebase refusal; git branch --list returned: %q", branchList)
	}

	// Cleanup-on-error: no linked worktree for fork/midrebase must remain.
	if leakedPath := worktreePathForBranch(t, repoDir, "fork/midrebase"); leakedPath != "" {
		t.Errorf("destination worktree must be cleaned up after mid-rebase refusal; found leaked worktree at: %s", leakedPath)
	}
}

// TestEval_SessionForkWithState_SubmoduleWarning drives the real binary
// against a parent repo that has .gitmodules and asserts the stderr warning
// is emitted. Pins gap 7's user-visible signal: callers must know that
// submodules under the parent's ProjectPath will be copied as files (not
// recursed into) by MaterializeWipFromParent.
//
// The fork itself may or may not fully succeed (downstream Start() needs a
// real claude binary), but the warning is emitted BEFORE worktree creation,
// so it is independent of exit code. We assert only on the warning text.
func TestEval_SessionForkWithState_SubmoduleWarning(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	sb := harness.NewSandbox(t)

	cfgDir := filepath.Join(sb.Home, ".agent-deck")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir agent-deck config dir: %v", err)
	}
	cfgPath := filepath.Join(cfgDir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(`[worktree]
branch_prefix = ""
default_location = "sibling"
`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	repoDir := filepath.Join(sb.Home, "proj")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	gitInit(t, repoDir)
	writeFile(t, repoDir, "README.md", "seed\n")
	// .gitmodules at repo root is the canonical signal HasSubmodules looks for.
	// We don't actually init a submodule — the warning fires purely on file
	// presence, matching the helper's intentional minimalism.
	writeFile(t, repoDir, ".gitmodules", "[submodule \"lib\"]\n\tpath = lib\n\turl = https://example.invalid/lib.git\n")
	gitMust(t, repoDir, "add", ".")
	gitMust(t, repoDir, "commit", "-m", "seed")

	runBin(t, sb, "add", "-c", "claude", "-t", "parent", "-g", "evalgrp", repoDir)
	runBin(t, sb, "session", "set", "parent", "claude-session-id",
		"00000000-0000-4000-8000-000000000001")

	out, _ := runBinTry(sb, "session", "fork", "parent",
		"--with-state", "-w", "fork/submodule-test", "-b",
		"-t", "fork-submodule")

	wantMsg := "submodules detected — copied as files, not recursed"
	if !strings.Contains(out, wantMsg) {
		t.Fatalf("submodule warning missing.\nwant substring: %q\ngot stderr+stdout:\n%s", wantMsg, out)
	}
}

// ---- file/git helpers (local to keep the eval package self-contained) ----

func gitInit(t *testing.T, dir string) {
	t.Helper()
	gitMust(t, dir, "init", "-q", "-b", "main")
	gitMust(t, dir, "config", "user.email", "eval@test")
	gitMust(t, dir, "config", "user.name", "Eval Test")
}

func gitMust(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Eval Test",
		"GIT_AUTHOR_EMAIL=eval@test",
		"GIT_COMMITTER_NAME=Eval Test",
		"GIT_COMMITTER_EMAIL=eval@test",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, string(out))
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, string(out))
	}
	return string(out)
}

// gitPorcelain returns `git status --porcelain` with lines sorted for stable
// comparison across runs (the order isn't guaranteed across git versions).
func gitPorcelain(t *testing.T, dir string) string {
	t.Helper()
	raw := gitOut(t, dir, "status", "--porcelain")
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	// Sort for stable comparison.
	for i := 0; i < len(lines); i++ {
		for j := i + 1; j < len(lines); j++ {
			if lines[j] < lines[i] {
				lines[i], lines[j] = lines[j], lines[i]
			}
		}
	}
	return strings.Join(lines, "\n")
}

// worktreePathForBranch returns the absolute path of the linked worktree
// checked out on branch `refs/heads/<branch>`, or "" if no such worktree
// exists. Uses `git worktree list --porcelain` for robustness.
func worktreePathForBranch(t *testing.T, repoDir, branch string) string {
	t.Helper()
	out := gitOut(t, repoDir, "worktree", "list", "--porcelain")
	wantRef := "branch refs/heads/" + branch
	var currentPath string
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			currentPath = strings.TrimPrefix(line, "worktree ")
		case line == wantRef:
			return currentPath
		}
	}
	return ""
}

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func appendFile(t *testing.T, dir, rel, suffix string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	f, err := os.OpenFile(full, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open %s for append: %v", rel, err)
	}
	defer f.Close()
	if _, err := f.WriteString(suffix); err != nil {
		t.Fatalf("append %s: %v", rel, err)
	}
}

func readFile(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

func mustHaveFile(t *testing.T, dir, rel, want string) {
	t.Helper()
	got := readFile(t, dir, rel)
	if got != want {
		t.Errorf("file %s in %s: got %q, want %q", rel, dir, got, want)
	}
}
