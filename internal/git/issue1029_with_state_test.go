package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestMaterializeWipFromParent_CopiesStagedUnstagedUntracked_RegressionFor1029
// is the RED test for issue #1029. @smorin asked for a third fork mode that
// copies the parent session's full working state — staged + unstaged + untracked
// (+ optionally gitignored) — into a fresh worktree, so users can explore
// multiple agent paths in parallel from the exact same WIP without stash
// juggling or shared-path races.
//
// The hard contract from the issue:
//  1. Parent checkout MUST remain read-only (no stash push, no add, no index
//     mutation) — verified separately below.
//  2. Child's `git status --porcelain` must match parent's at materialization
//     time, with staged/unstaged/untracked faithfully reproduced.
//  3. Gitignored files only with explicit opt-in (separate test).
//
// The minimum failing assertion: a parent repo with one staged file, one
// unstaged edit, and one untracked file produces a child worktree whose
// `git status --porcelain` is byte-identical (sorted) to the parent's.
func TestMaterializeWipFromParent_CopiesStagedUnstagedUntracked_RegressionFor1029(t *testing.T) {
	parent := t.TempDir()
	createTestRepo(t, parent)

	// Set up parent WIP: one staged add, one unstaged edit, one untracked.
	writeWipFile(t, parent, "staged.txt", "staged content\n")
	gitMustRun(t, parent, "add", "staged.txt")

	appendWipFile(t, parent, "README.md", "\nunstaged edit\n")

	writeWipFile(t, parent, "untracked.txt", "untracked content\n")

	parentStatusBefore := gitPorcelain(t, parent)

	// Create child worktree from parent's HEAD on a fresh branch.
	child := filepath.Join(t.TempDir(), "child")
	if err := CreateWorktree(parent, child, "fork-1029"); err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}

	// ACTION under test.
	if err := MaterializeWipFromParent(parent, child, false /* includeIgnored */); err != nil {
		t.Fatalf("MaterializeWipFromParent: %v", err)
	}

	parentStatusAfter := gitPorcelain(t, parent)
	if parentStatusAfter != parentStatusBefore {
		t.Fatalf("parent state MUST remain read-only.\nbefore:\n%s\nafter:\n%s",
			parentStatusBefore, parentStatusAfter)
	}

	childStatus := gitPorcelain(t, child)
	if childStatus != parentStatusBefore {
		t.Fatalf("child status must mirror parent WIP.\nparent:\n%s\nchild:\n%s",
			parentStatusBefore, childStatus)
	}

	// And the file contents must actually be there.
	mustHaveWipFile(t, child, "staged.txt", "staged content\n")
	mustHaveWipFile(t, child, "untracked.txt", "untracked content\n")
	if got := readWipFile(t, child, "README.md"); !strings.Contains(got, "unstaged edit") {
		t.Fatalf("child README.md missing unstaged edit; got %q", got)
	}
}

func TestMaterializeWipFromParent_FromSubdirectoryKeepsRepoRelativeUntrackedPaths(t *testing.T) {
	parent := t.TempDir()
	createTestRepo(t, parent)

	writeWipFile(t, parent, "a/tracked.txt", "tracked a\n")
	writeWipFile(t, parent, "b/tracked.txt", "tracked b\n")
	gitMustRun(t, parent, "add", ".")
	gitMustRun(t, parent, "commit", "-m", "add tracked")

	parentSubdir := filepath.Join(parent, "a")
	writeWipFile(t, parent, "a/new-a.txt", "untracked a\n")
	writeWipFile(t, parent, "b/new-b.txt", "untracked b\n")

	parentStatusBefore := gitPorcelain(t, parent)

	child := filepath.Join(t.TempDir(), "child")
	if err := CreateWorktree(parent, child, "fork-1029-subdir"); err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}

	if err := MaterializeWipFromParent(parentSubdir, child, false /* includeIgnored */); err != nil {
		t.Fatalf("MaterializeWipFromParent: %v", err)
	}

	childStatus := gitPorcelain(t, child)
	if childStatus != parentStatusBefore {
		t.Fatalf("child status must mirror parent repo WIP when parent path is a subdirectory.\nparent:\n%s\nchild:\n%s",
			parentStatusBefore, childStatus)
	}

	mustHaveWipFile(t, child, "a/new-a.txt", "untracked a\n")
	mustHaveWipFile(t, child, "b/new-b.txt", "untracked b\n")
	mustNotHaveWipFile(t, child, "new-a.txt")
}

func TestMaterializeWipFromParent_FromSubdirectoryKeepsRepoRelativeIgnoredPaths(t *testing.T) {
	parent := t.TempDir()
	createTestRepo(t, parent)

	writeWipFile(t, parent, ".gitignore", "*.env\n")
	writeWipFile(t, parent, "a/tracked.txt", "tracked a\n")
	writeWipFile(t, parent, "b/tracked.txt", "tracked b\n")
	gitMustRun(t, parent, "add", ".")
	gitMustRun(t, parent, "commit", "-m", "add tracked and ignore")

	parentSubdir := filepath.Join(parent, "a")
	writeWipFile(t, parent, "a/secrets.env", "ignored a\n")
	writeWipFile(t, parent, "b/secrets.env", "ignored b\n")

	child := filepath.Join(t.TempDir(), "child")
	if err := CreateWorktree(parent, child, "fork-1029-subdir-ignored"); err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}

	if err := MaterializeWipFromParent(parentSubdir, child, true /* includeIgnored */); err != nil {
		t.Fatalf("MaterializeWipFromParent: %v", err)
	}

	mustHaveWipFile(t, child, "a/secrets.env", "ignored a\n")
	mustHaveWipFile(t, child, "b/secrets.env", "ignored b\n")
	mustNotHaveWipFile(t, child, "secrets.env")
}

func writeWipFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendWipFile(t *testing.T, dir, rel, suffix string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	f, err := os.OpenFile(full, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(suffix); err != nil {
		t.Fatal(err)
	}
}

func readWipFile(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

func mustHaveWipFile(t *testing.T, dir, rel, want string) {
	t.Helper()
	if got := readWipFile(t, dir, rel); got != want {
		t.Fatalf("%s content mismatch.\nwant: %q\ngot:  %q", rel, want, got)
	}
}

func mustNotHaveWipFile(t *testing.T, dir, rel string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, rel)); err == nil {
		t.Fatalf("%s unexpectedly exists in %s", rel, dir)
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat %s in %s: %v", rel, dir, err)
	}
}

func gitMustRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, string(out))
	}
}

func gitPorcelain(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "status", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	// Sort lines so we compare set-equality, not git's enumeration order.
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}
