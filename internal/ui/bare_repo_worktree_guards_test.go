// bare_repo_worktree_guards_test.go — regression tests for #742 (v1.7.68).
//
// Context (@Clindbergh, 2026-04-22): the #715 migration in v1.7.58 added
// git.IsGitRepoOrBareProjectRoot and moved every CLI call site
// (launch, add, session add, worktree list) to the broader check so users
// could pass a bare-repo project root transparently. The TUI new-session
// and fork paths in internal/ui/home.go were missed, so any worktree
// creation that uses a bare-repo project root falls through the narrow
// git.IsGitRepo guard:
//
//   - New session with worktree (home.go:~5100) → "Path is not a git
//     repository" error; worktree never created.
//   - Fork with worktree (home.go:~7366) → same error; fork fails.
//   - Multi-repo with worktree (home.go:~7762) → silently falls through
//     to os.Symlink, so the worktree is not created AND the setup script
//     at <projectRoot>/.agent-deck/worktree-setup.sh is never run —
//     exactly the symptom in #742.
//
// This guard is structural (source-level) because:
//
//   1. The bug is "the code took a narrow check when the broad one was
//      available" — a source-visible invariant, not a runtime one.
//   2. A real-TUI integration test would need a live Bubble Tea session,
//      a real git binary, and a scripted dialog interaction; that cost
//      massively outweighs the clarity gain over a source regex.
//   3. The existing internal/git/bare_repo_test.go suite already proves
//      that git.IsGitRepoOrBareProjectRoot + git.GetWorktreeBaseRoot +
//      git.CreateWorktreeWithSetup handle bare layouts correctly; the
//      only gap is "does home.go route bare inputs into those helpers?"

package ui

import (
	"os"
	"regexp"
	"testing"
)

// TestRegression742_HomeWorktreeGuardsAcceptBareProjectRoot asserts that
// every use of a git-repo preflight check in internal/ui/home.go uses the
// broader IsGitRepoOrBareProjectRoot helper, not the narrow IsGitRepo. Any
// new worktree-creation site that sneaks in a narrow check will fail this
// guard at test time instead of at user-report time.
func TestRegression742_HomeWorktreeGuardsAcceptBareProjectRoot(t *testing.T) {
	data, err := os.ReadFile("home.go")
	if err != nil {
		t.Fatalf("read home.go: %v", err)
	}
	narrow := regexp.MustCompile(`git\.IsGitRepo\(`)
	// `IsGitRepoOrBareProjectRoot` also starts with `git.IsGitRepo(` — be
	// careful to match ONLY the narrow variant. Negative lookahead isn't
	// available in RE2, so post-filter the matches.
	src := string(data)
	offsets := narrow.FindAllStringIndex(src, -1)
	var narrowHits []int
	for _, loc := range offsets {
		// Skip if this is actually IsGitRepoOrBareProjectRoot.
		tail := src[loc[0]:]
		if len(tail) >= len("git.IsGitRepoOrBareProjectRoot(") &&
			tail[:len("git.IsGitRepoOrBareProjectRoot(")] == "git.IsGitRepoOrBareProjectRoot(" {
			continue
		}
		narrowHits = append(narrowHits, loc[0])
	}
	if len(narrowHits) != 0 {
		for _, off := range narrowHits {
			line := 1
			for i := 0; i < off && i < len(src); i++ {
				if src[i] == '\n' {
					line++
				}
			}
			t.Errorf("home.go:%d uses git.IsGitRepo(...) — worktree guards must use git.IsGitRepoOrBareProjectRoot(...) so bare-repo project roots pass (#742)", line)
		}
	}
}
