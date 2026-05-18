package git

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCreateWorktreeWithStateAndSetup_WiresMaterialization_RegressionFor1029
// pins the integration shape: when WithState is true, the wrapper creates a
// fresh worktree AND materializes parent WIP before returning, so the (later)
// setup hook would observe a realized working tree.
func TestCreateWorktreeWithStateAndSetup_WiresMaterialization_RegressionFor1029(t *testing.T) {
	parent := t.TempDir()
	createTestRepo(t, parent)

	if err := os.WriteFile(filepath.Join(parent, "wip.txt"), []byte("hello-wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	child := filepath.Join(t.TempDir(), "child-wired")
	var stdout, stderr bytes.Buffer
	_, err := CreateWorktreeWithStateAndSetup(
		parent, child, "fork-1029-wired",
		WorktreeStateOptions{WithState: true},
		&stdout, &stderr, 0,
	)
	if err != nil {
		t.Fatalf("CreateWorktreeWithStateAndSetup: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(child, "wip.txt"))
	if err != nil {
		t.Fatalf("read child wip.txt: %v", err)
	}
	if string(got) != "hello-wip\n" {
		t.Fatalf("wip not materialized; got %q", got)
	}
}

// TestMaterializeWipFromParent_Empty_NoOp_RegressionFor1029 ensures a clean
// parent (no WIP) produces a clean child with no error — the boundary case
// where every diff is empty and ls-files returns nothing.
func TestMaterializeWipFromParent_Empty_NoOp_RegressionFor1029(t *testing.T) {
	parent := t.TempDir()
	createTestRepo(t, parent)

	child := filepath.Join(t.TempDir(), "child")
	if err := CreateWorktree(parent, child, "fork-1029-empty"); err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}

	if err := MaterializeWipFromParent(parent, child, false); err != nil {
		t.Fatalf("MaterializeWipFromParent on clean parent: %v", err)
	}
	if got := gitPorcelain(t, child); got != "" {
		t.Fatalf("child should be clean; got %q", got)
	}
}

// TestMaterializeWipFromParent_BinaryFile_RegressionFor1029 — binary blobs
// must round-trip byte-identically. The PNG magic header + a random byte
// payload is enough to exercise the `--binary` path through git diff/apply.
func TestMaterializeWipFromParent_BinaryFile_RegressionFor1029(t *testing.T) {
	parent := t.TempDir()
	createTestRepo(t, parent)

	bin := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0xff, 0x7f, 0x10}
	if err := os.WriteFile(filepath.Join(parent, "blob.bin"), bin, 0o644); err != nil {
		t.Fatal(err)
	}
	gitMustRun(t, parent, "add", "blob.bin") // staged-add

	child := filepath.Join(t.TempDir(), "child")
	if err := CreateWorktree(parent, child, "fork-1029-bin"); err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	if err := MaterializeWipFromParent(parent, child, false); err != nil {
		t.Fatalf("MaterializeWipFromParent: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(child, "blob.bin"))
	if err != nil {
		t.Fatalf("read child blob.bin: %v", err)
	}
	if string(got) != string(bin) {
		t.Fatalf("binary content drift.\nwant: % x\ngot:  % x", bin, got)
	}
}

// TestMaterializeWipFromParent_Symlink_RegressionFor1029 — an untracked
// symlink in parent must appear as a symlink in child with the same target.
func TestMaterializeWipFromParent_Symlink_RegressionFor1029(t *testing.T) {
	parent := t.TempDir()
	createTestRepo(t, parent)

	if err := os.Symlink("README.md", filepath.Join(parent, "link.md")); err != nil {
		t.Skipf("symlinks unsupported on this platform: %v", err)
	}

	child := filepath.Join(t.TempDir(), "child")
	if err := CreateWorktree(parent, child, "fork-1029-link"); err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	if err := MaterializeWipFromParent(parent, child, false); err != nil {
		t.Fatalf("MaterializeWipFromParent: %v", err)
	}

	info, err := os.Lstat(filepath.Join(child, "link.md"))
	if err != nil {
		t.Fatalf("lstat child link: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected symlink, got mode %v", info.Mode())
	}
	target, err := os.Readlink(filepath.Join(child, "link.md"))
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if target != "README.md" {
		t.Fatalf("symlink target drift: want README.md, got %s", target)
	}
}

// TestMaterializeWipFromParent_IgnoredOptIn_RegressionFor1029 — gitignored
// files must be skipped by default and copied only when includeIgnored=true.
// This matches the `--with-state-and-gitignored` opt-in from #1029.
func TestMaterializeWipFromParent_IgnoredOptIn_RegressionFor1029(t *testing.T) {
	parent := t.TempDir()
	createTestRepo(t, parent)

	// Establish .gitignore so secrets.env is ignored.
	if err := os.WriteFile(filepath.Join(parent, ".gitignore"), []byte("secrets.env\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitMustRun(t, parent, "add", ".gitignore")
	gitMustRun(t, parent, "commit", "-m", "ignore secrets")

	if err := os.WriteFile(filepath.Join(parent, "secrets.env"), []byte("API_KEY=xyz\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Pass 1: without opt-in → child must NOT have the ignored file.
	child1 := filepath.Join(t.TempDir(), "child1")
	if err := CreateWorktree(parent, child1, "fork-1029-ign1"); err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	if err := MaterializeWipFromParent(parent, child1, false); err != nil {
		t.Fatalf("MaterializeWipFromParent: %v", err)
	}
	if _, err := os.Stat(filepath.Join(child1, "secrets.env")); !os.IsNotExist(err) {
		t.Fatalf("ignored file leaked without opt-in: %v", err)
	}

	// Pass 2: with opt-in → child must have the ignored file with same content.
	child2 := filepath.Join(t.TempDir(), "child2")
	if err := CreateWorktree(parent, child2, "fork-1029-ign2"); err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	if err := MaterializeWipFromParent(parent, child2, true); err != nil {
		t.Fatalf("MaterializeWipFromParent (with ignored): %v", err)
	}
	got, err := os.ReadFile(filepath.Join(child2, "secrets.env"))
	if err != nil {
		t.Fatalf("ignored file missing with opt-in: %v", err)
	}
	if string(got) != "API_KEY=xyz\n" {
		t.Fatalf("ignored content drift: %q", got)
	}
}

// TestMaterializeWipFromParent_RefusesMidMerge_RegressionFor1029 — @smorin's
// spec requires unsafe in-flight states to be refused with an actionable
// error rather than silently materialized over the top of a half-done merge.
func TestMaterializeWipFromParent_RefusesMidMerge_RegressionFor1029(t *testing.T) {
	parent := t.TempDir()
	createTestRepo(t, parent)

	// Fake a mid-merge by dropping MERGE_HEAD into the gitdir.
	gitDir := filepath.Join(parent, ".git")
	if err := os.WriteFile(filepath.Join(gitDir, "MERGE_HEAD"), []byte("deadbeef\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	child := filepath.Join(t.TempDir(), "child")
	if err := CreateWorktree(parent, child, "fork-1029-merge"); err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	err := MaterializeWipFromParent(parent, child, false)
	if err == nil {
		t.Fatal("expected refusal during mid-merge, got nil")
	}
	if !strings.Contains(err.Error(), "merge") {
		t.Fatalf("expected error to mention 'merge'; got: %v", err)
	}
}

// TestMaterializeWipFromParent_DeletedFile_RegressionFor1029 — a tracked file
// removed in parent's working tree must also be absent (in the same state)
// in the child.
func TestMaterializeWipFromParent_DeletedFile_RegressionFor1029(t *testing.T) {
	parent := t.TempDir()
	createTestRepo(t, parent)

	// Add a file, commit it, then remove it (unstaged delete).
	if err := os.WriteFile(filepath.Join(parent, "doomed.txt"), []byte("bye\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitMustRun(t, parent, "add", "doomed.txt")
	gitMustRun(t, parent, "commit", "-m", "add doomed")
	if err := os.Remove(filepath.Join(parent, "doomed.txt")); err != nil {
		t.Fatal(err)
	}

	child := filepath.Join(t.TempDir(), "child")
	if err := CreateWorktree(parent, child, "fork-1029-del"); err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	if err := MaterializeWipFromParent(parent, child, false); err != nil {
		t.Fatalf("MaterializeWipFromParent: %v", err)
	}

	if _, err := os.Stat(filepath.Join(child, "doomed.txt")); !os.IsNotExist(err) {
		t.Fatalf("deleted-in-parent file present in child: %v", err)
	}
	want := gitPorcelain(t, parent)
	got := gitPorcelain(t, child)
	if want != got {
		t.Fatalf("status mismatch.\nparent:\n%s\nchild:\n%s", want, got)
	}
}
