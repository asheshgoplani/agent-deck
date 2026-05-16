package git

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Behavior: .worktreeinclude with a matching gitignored file copies it to the worktree.
// Reference: https://code.claude.com/docs/en/worktrees#copy-gitignored-files-into-worktrees
func TestProcessWorktreeInclude_CopiesGitignoredFile(t *testing.T) {
	repoDir := t.TempDir()
	initGitRepo(t, repoDir)

	writeFile(t, filepath.Join(repoDir, ".gitignore"), ".env\n")
	gitAdd(t, repoDir, ".gitignore")
	gitCommit(t, repoDir, "add gitignore")

	writeFile(t, filepath.Join(repoDir, ".env"), "SECRET=hunter2")

	writeFile(t, filepath.Join(repoDir, ".worktreeinclude"), ".env\n")
	gitAdd(t, repoDir, ".worktreeinclude")
	gitCommit(t, repoDir, "add worktreeinclude")

	worktreeDir := t.TempDir()

	var stderr bytes.Buffer
	err := ProcessWorktreeInclude(repoDir, worktreeDir, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(worktreeDir, ".env"))
	if err != nil {
		t.Fatalf("expected .env to be copied: %v", err)
	}
	if string(got) != "SECRET=hunter2" {
		t.Errorf("got %q, want %q", got, "SECRET=hunter2")
	}
}

func TestProcessWorktreeInclude_TrackedFileNotCopied(t *testing.T) {
	repoDir := t.TempDir()
	initGitRepo(t, repoDir)

	writeFile(t, filepath.Join(repoDir, "config.json"), `{"tracked": true}`)
	gitAdd(t, repoDir, "config.json")
	gitCommit(t, repoDir, "add config")

	writeFile(t, filepath.Join(repoDir, ".worktreeinclude"), "config.json\n")
	gitAdd(t, repoDir, ".worktreeinclude")
	gitCommit(t, repoDir, "add worktreeinclude")

	worktreeDir := t.TempDir()

	var stderr bytes.Buffer
	err := ProcessWorktreeInclude(repoDir, worktreeDir, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(worktreeDir, "config.json")); !os.IsNotExist(err) {
		t.Error("tracked file should not be copied by ProcessWorktreeInclude")
	}
}

func TestProcessWorktreeInclude_Integration_RunsBeforeSetupScript(t *testing.T) {
	repoDir := t.TempDir()
	initGitRepo(t, repoDir)

	writeFile(t, filepath.Join(repoDir, ".gitignore"), ".env\n")
	gitAdd(t, repoDir, ".gitignore")

	writeFile(t, filepath.Join(repoDir, ".env"), "DB_HOST=localhost")
	writeFile(t, filepath.Join(repoDir, ".worktreeinclude"), ".env\n")
	gitAdd(t, repoDir, ".worktreeinclude")

	scriptDir := filepath.Join(repoDir, ".agent-deck")
	script := "#!/bin/sh\ncat \"$AGENT_DECK_WORKTREE_PATH/.env\"\n"
	writeFile(t, filepath.Join(scriptDir, "worktree-setup.sh"), script)
	gitAdd(t, repoDir, ".")
	gitCommit(t, repoDir, "setup")

	worktreePath := filepath.Join(repoDir, ".worktrees", "include-integration")

	var stdout, stderr bytes.Buffer
	setupErr, err := CreateWorktreeWithSetup(repoDir, worktreePath, "include-integration", &stdout, &stderr, 0)
	if err != nil {
		t.Fatalf("worktree creation failed: %v", err)
	}
	if setupErr != nil {
		t.Fatalf("setup script failed: %v (stderr: %s)", setupErr, stderr.String())
	}

	if !strings.Contains(stdout.String(), "DB_HOST=localhost") {
		t.Errorf("setup script should see .env from include, got stdout=%q", stdout.String())
	}
}

func TestProcessWorktreeInclude_GlobPatternMatchesMultiple(t *testing.T) {
	repoDir := t.TempDir()
	initGitRepo(t, repoDir)

	writeFile(t, filepath.Join(repoDir, ".gitignore"), "*.env\n")
	gitAdd(t, repoDir, ".gitignore")

	writeFile(t, filepath.Join(repoDir, "app.env"), "APP=1")
	writeFile(t, filepath.Join(repoDir, "test.env"), "TEST=1")

	writeFile(t, filepath.Join(repoDir, ".worktreeinclude"), "*.env\n")
	gitAdd(t, repoDir, ".worktreeinclude")
	gitCommit(t, repoDir, "setup")

	worktreeDir := t.TempDir()

	var stderr bytes.Buffer
	err := ProcessWorktreeInclude(repoDir, worktreeDir, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, name := range []string{"app.env", "test.env"} {
		if _, err := os.Stat(filepath.Join(worktreeDir, name)); err != nil {
			t.Errorf("expected %s to be copied: %v", name, err)
		}
	}
}

func TestProcessWorktreeInclude_DirectoryCopiedRecursively(t *testing.T) {
	repoDir := t.TempDir()
	initGitRepo(t, repoDir)

	writeFile(t, filepath.Join(repoDir, ".gitignore"), ".secrets/\n")
	gitAdd(t, repoDir, ".gitignore")

	writeFile(t, filepath.Join(repoDir, ".secrets", "key.pem"), "PRIVATE")
	writeFile(t, filepath.Join(repoDir, ".secrets", "sub", "cert.pem"), "CERT")

	writeFile(t, filepath.Join(repoDir, ".worktreeinclude"), ".secrets/\n")
	gitAdd(t, repoDir, ".worktreeinclude")
	gitCommit(t, repoDir, "setup")

	worktreeDir := t.TempDir()

	var stderr bytes.Buffer
	err := ProcessWorktreeInclude(repoDir, worktreeDir, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(worktreeDir, ".secrets", "key.pem"))
	if err != nil {
		t.Fatalf("expected .secrets/key.pem: %v", err)
	}
	if string(got) != "PRIVATE" {
		t.Errorf("got %q", got)
	}

	got, err = os.ReadFile(filepath.Join(worktreeDir, ".secrets", "sub", "cert.pem"))
	if err != nil {
		t.Fatalf("expected .secrets/sub/cert.pem: %v", err)
	}
	if string(got) != "CERT" {
		t.Errorf("got %q", got)
	}
}

func TestProcessWorktreeInclude_DirectoryMergesIntoExisting(t *testing.T) {
	repoDir := t.TempDir()
	initGitRepo(t, repoDir)

	writeFile(t, filepath.Join(repoDir, ".gitignore"), ".secrets/\n")
	gitAdd(t, repoDir, ".gitignore")

	writeFile(t, filepath.Join(repoDir, ".secrets", "key.pem"), "PRIVATE")
	writeFile(t, filepath.Join(repoDir, ".secrets", "new.pem"), "NEW")

	writeFile(t, filepath.Join(repoDir, ".worktreeinclude"), ".secrets/\n")
	gitAdd(t, repoDir, ".worktreeinclude")
	gitCommit(t, repoDir, "setup")

	worktreeDir := t.TempDir()
	// Pre-existing directory with one file already present
	writeFile(t, filepath.Join(worktreeDir, ".secrets", "key.pem"), "EXISTING")

	var stderr bytes.Buffer
	err := ProcessWorktreeInclude(repoDir, worktreeDir, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Existing file should NOT be overwritten
	got, _ := os.ReadFile(filepath.Join(worktreeDir, ".secrets", "key.pem"))
	if string(got) != "EXISTING" {
		t.Errorf("existing file was overwritten: got %q", got)
	}

	// New file should be copied
	got, err = os.ReadFile(filepath.Join(worktreeDir, ".secrets", "new.pem"))
	if err != nil {
		t.Fatalf("expected new.pem to be copied: %v", err)
	}
	if string(got) != "NEW" {
		t.Errorf("got %q, want %q", got, "NEW")
	}
}

func TestProcessWorktreeInclude_CommentsAndBlankLinesIgnored(t *testing.T) {
	repoDir := t.TempDir()
	initGitRepo(t, repoDir)

	writeFile(t, filepath.Join(repoDir, ".gitignore"), ".env\n")
	gitAdd(t, repoDir, ".gitignore")

	writeFile(t, filepath.Join(repoDir, ".env"), "VAL=1")

	includeContent := "# This is a comment\n\n.env\n\n# Another comment\n"
	writeFile(t, filepath.Join(repoDir, ".worktreeinclude"), includeContent)
	gitAdd(t, repoDir, ".worktreeinclude")
	gitCommit(t, repoDir, "setup")

	worktreeDir := t.TempDir()

	var stderr bytes.Buffer
	err := ProcessWorktreeInclude(repoDir, worktreeDir, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(worktreeDir, ".env"))
	if err != nil {
		t.Fatalf(".env should have been copied: %v", err)
	}
	if string(got) != "VAL=1" {
		t.Errorf("got %q, want %q", got, "VAL=1")
	}
}

func TestProcessWorktreeInclude_ExistingDestNotOverwritten(t *testing.T) {
	repoDir := t.TempDir()
	initGitRepo(t, repoDir)

	writeFile(t, filepath.Join(repoDir, ".gitignore"), ".env\n")
	gitAdd(t, repoDir, ".gitignore")

	writeFile(t, filepath.Join(repoDir, ".env"), "FROM_REPO")
	writeFile(t, filepath.Join(repoDir, ".worktreeinclude"), ".env\n")
	gitAdd(t, repoDir, ".worktreeinclude")
	gitCommit(t, repoDir, "setup")

	worktreeDir := t.TempDir()
	writeFile(t, filepath.Join(worktreeDir, ".env"), "ALREADY_HERE")

	var stderr bytes.Buffer
	err := ProcessWorktreeInclude(repoDir, worktreeDir, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, _ := os.ReadFile(filepath.Join(worktreeDir, ".env"))
	if string(got) != "ALREADY_HERE" {
		t.Errorf("existing file was overwritten: got %q", got)
	}
}

func TestProcessWorktreeInclude_MissingSourceSkipped(t *testing.T) {
	repoDir := t.TempDir()
	initGitRepo(t, repoDir)

	writeFile(t, filepath.Join(repoDir, ".gitignore"), ".env\n")
	gitAdd(t, repoDir, ".gitignore")

	writeFile(t, filepath.Join(repoDir, ".worktreeinclude"), ".env\n")
	gitAdd(t, repoDir, ".worktreeinclude")
	gitCommit(t, repoDir, "add files")

	worktreeDir := t.TempDir()

	var stderr bytes.Buffer
	err := ProcessWorktreeInclude(repoDir, worktreeDir, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(worktreeDir, ".env")); !os.IsNotExist(err) {
		t.Error("missing source file should not produce anything in worktree")
	}
}

func TestProcessWorktreeInclude_NoFileIsNoop(t *testing.T) {
	repoDir := t.TempDir()
	initGitRepo(t, repoDir)
	worktreeDir := t.TempDir()

	var stderr bytes.Buffer
	err := ProcessWorktreeInclude(repoDir, worktreeDir, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stderr.Len() != 0 {
		t.Errorf("expected no output, got stderr=%q", stderr.String())
	}
}

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	writeFile(t, filepath.Join(dir, "README.md"), "# test")
	gitAdd(t, dir, ".")
	gitCommit(t, dir, "init")
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitAdd(t *testing.T, dir, path string) {
	t.Helper()
	cmd := exec.Command("git", "add", path)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add %s: %v\n%s", path, err, out)
	}
}

func gitCommit(t *testing.T, dir, msg string) {
	t.Helper()
	cmd := exec.Command("git", "commit", "-m", msg)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
}
