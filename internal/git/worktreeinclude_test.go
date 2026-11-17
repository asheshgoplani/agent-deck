package git

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	ignore "github.com/sabhiram/go-gitignore"
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

// Regression: the include walk must not descend into nested worktrees living
// under the repo (e.g. agent-deck's own .worktrees/ output). Otherwise it finds
// every other worktree's gitignored .env, recreates the full nested path, and
// the .worktrees forest grows one level deeper on every spawn — eventually
// gigabytes of duplicated skeletons that make each new spawn crawl.
func TestProcessWorktreeInclude_DoesNotDescendIntoNestedWorktrees(t *testing.T) {
	repoDir := t.TempDir()
	initGitRepo(t, repoDir)

	writeFile(t, filepath.Join(repoDir, ".gitignore"), ".env\n.worktrees/\n")
	gitAdd(t, repoDir, ".gitignore")

	writeFile(t, filepath.Join(repoDir, ".env"), "ROOT")
	writeFile(t, filepath.Join(repoDir, ".worktreeinclude"), ".env\n")
	gitAdd(t, repoDir, ".worktreeinclude")
	gitCommit(t, repoDir, "setup")

	// A real linked worktree under .worktrees/ — it has its own .git pointer
	// file, marking a worktree boundary the walk must stop at.
	nested := filepath.Join(repoDir, ".worktrees", "other")
	cmd := exec.Command("git", "worktree", "add", "-b", "other", nested)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v\n%s", err, out)
	}
	writeFile(t, filepath.Join(nested, ".env"), "NESTED")

	worktreeDir := t.TempDir()

	var stderr bytes.Buffer
	if err := ProcessWorktreeInclude(repoDir, worktreeDir, &stderr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The repo's own .env is copied.
	got, err := os.ReadFile(filepath.Join(worktreeDir, ".env"))
	if err != nil || string(got) != "ROOT" {
		t.Fatalf("expected root .env to be copied, got %q (err=%v)", got, err)
	}

	// The nested worktree's .env must NOT be reconstructed under the new worktree.
	if _, err := os.Stat(filepath.Join(worktreeDir, ".worktrees")); !os.IsNotExist(err) {
		t.Errorf(".worktrees forest was copied into the new worktree (err=%v)", err)
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

func TestWalkRoots(t *testing.T) {
	tests := []struct {
		name     string
		patterns []string
		want     []string
	}{
		{
			name:     "bare name matches at any depth",
			patterns: []string{".env"},
			want:     []string{"."},
		},
		{
			name:     "leading glob matches at any depth",
			patterns: []string{"*.env"},
			want:     []string{"."},
		},
		{
			name:     "directory pattern prunes to its parent",
			patterns: []string{".claude/rules/personal/"},
			want:     []string{".claude/rules"},
		},
		{
			name:     "file pattern prunes to its directory",
			patterns: []string{"config/local.json"},
			want:     []string{"config"},
		},
		{
			name:     "anchored file pattern prunes to its directory",
			patterns: []string{"/config/local.json"},
			want:     []string{"config"},
		},
		{
			name:     "glob inside a static prefix keeps the prefix",
			patterns: []string{"config/*/local.json"},
			want:     []string{"config"},
		},
		{
			name:     "nested roots collapse",
			patterns: []string{"a/b/c.txt", "a/b/d.txt"},
			want:     []string{"a/b"},
		},
		{
			name:     "a full walk wins over pruned roots",
			patterns: []string{"config/x.txt", ".env"},
			want:     []string{"."},
		},
		{
			name:     "a negation does not widen the walk",
			patterns: []string{"config/x.txt", "!secret"},
			want:     []string{"config"},
		},
		{
			name:     "a negation alone keeps no roots",
			patterns: []string{"!secret"},
			want:     nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := walkRoots(tt.patterns)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("walkRoots(%q) = %q, want %q", tt.patterns, got, tt.want)
			}
		})
	}
}

// recordingFS records every path the walk opens, so a test can prove the walk
// stayed inside the pattern roots.
type recordingFS struct {
	fsys   fs.FS
	opened []string
}

func (r *recordingFS) Open(name string) (fs.File, error) {
	r.opened = append(r.opened, name)
	return r.fsys.Open(name)
}

// Regression: a narrow pattern must not walk the rest of the checkout. Large
// build directories hold millions of entries, and a full walk there takes
// about a minute (duolingo-android: 7.9M entries, 45s with find).
func TestWalkIncludeRoots_StaysInsidePatternRoot(t *testing.T) {
	repoDir := t.TempDir()
	writeFile(t, filepath.Join(repoDir, ".claude", "rules", "personal", "rule.md"), "RULE")
	writeFile(t, filepath.Join(repoDir, "build", "output.bin"), "BIG")
	writeFile(t, filepath.Join(repoDir, "build", "nested", "deep.bin"), "BIG")

	patterns := []string{".claude/rules/personal/"}
	matcher := ignore.CompileIgnoreLines(patterns...)

	rec := &recordingFS{fsys: os.DirFS(repoDir)}
	candidates, err := walkIncludeRoots(rec, walkRoots(patterns), matcher)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !reflect.DeepEqual(candidates, []string{".claude/rules/personal/rule.md"}) {
		t.Errorf("candidates = %q, want [.claude/rules/personal/rule.md]", candidates)
	}
	for _, opened := range rec.opened {
		if opened == "build" || strings.HasPrefix(opened, "build/") {
			t.Errorf("walk opened %q, which is outside the pattern root", opened)
		}
	}
}

// A bare name matches at any depth, so the walk falls back to the repository
// root and still finds the file in a subdirectory.
func TestProcessWorktreeInclude_BareNameMatchesAtAnyDepth(t *testing.T) {
	repoDir := t.TempDir()
	initGitRepo(t, repoDir)

	writeFile(t, filepath.Join(repoDir, ".gitignore"), ".env\n")
	gitAdd(t, repoDir, ".gitignore")

	writeFile(t, filepath.Join(repoDir, "sub", ".env"), "NESTED=1")
	writeFile(t, filepath.Join(repoDir, ".worktreeinclude"), ".env\n")
	gitAdd(t, repoDir, ".worktreeinclude")
	gitCommit(t, repoDir, "setup")

	worktreeDir := t.TempDir()

	var stderr bytes.Buffer
	if err := ProcessWorktreeInclude(repoDir, worktreeDir, &stderr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(worktreeDir, "sub", ".env"))
	if err != nil {
		t.Fatalf("expected sub/.env to be copied: %v", err)
	}
	if string(got) != "NESTED=1" {
		t.Errorf("got %q, want %q", got, "NESTED=1")
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
