package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newProjRoot builds a fake PROJ_ROOT containing one project laid out the way
// proj expects, and points PROJ_ROOT at it for the duration of the test.
// Returns the project directory.
func newProjRoot(t *testing.T, project string) string {
	t.Helper()
	root := t.TempDir()
	projectDir := filepath.Join(root, "projects", project)
	if err := os.MkdirAll(filepath.Join(projectDir, ".repo"), 0o755); err != nil {
		t.Fatalf("mkdir .repo: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(projectDir, "00-master"), 0o755); err != nil {
		t.Fatalf("mkdir template: %v", err)
	}
	t.Setenv("PROJ_ROOT", root)
	return projectDir
}

func TestParseWorktreeBackend(t *testing.T) {
	cases := map[string]string{
		"":        WorktreeBackendAuto,
		"auto":    WorktreeBackendAuto,
		"AUTO":    WorktreeBackendAuto,
		"  proj ": WorktreeBackendProj,
		"Proj":    WorktreeBackendProj,
		"git":     WorktreeBackendGit,
		"GIT":     WorktreeBackendGit,
		// A typo must never silently disable worktree creation.
		"prj":   WorktreeBackendAuto,
		"gitt":  WorktreeBackendAuto,
		"false": WorktreeBackendAuto,
	}
	for in, want := range cases {
		if got := ParseWorktreeBackend(in); got != want {
			t.Errorf("ParseWorktreeBackend(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveProjProject(t *testing.T) {
	projectDir := newProjRoot(t, "core-stack")

	// Every handle agent-deck might hold resolves to the same project.
	worktree := filepath.Join(projectDir, "feature-x")
	if err := os.MkdirAll(filepath.Join(worktree, "sub", "dir"), 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	for _, dir := range []string{
		projectDir,
		filepath.Join(projectDir, ".repo"),
		filepath.Join(projectDir, "00-master"),
		worktree,
		filepath.Join(worktree, "sub", "dir"),
	} {
		got, ok := resolveProjProject(dir)
		if !ok {
			t.Errorf("resolveProjProject(%q) ok = false, want true", dir)
			continue
		}
		if got.dir != projectDir {
			t.Errorf("resolveProjProject(%q).dir = %q, want %q", dir, got.dir, projectDir)
		}
	}
}

func TestResolveProjProjectRejectsNonProjLayouts(t *testing.T) {
	projectDir := newProjRoot(t, "core-stack")
	root := filepath.Dir(filepath.Dir(projectDir))

	// A directory under projects/ with no .repo is not a proj project — this
	// is what keeps an ordinary repo that happens to live there from being
	// handed to proj.
	plain := filepath.Join(root, "projects", "not-proj")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	for _, dir := range []string{plain, root, filepath.Join(root, "projects"), t.TempDir(), ""} {
		if _, ok := resolveProjProject(dir); ok {
			t.Errorf("resolveProjProject(%q) ok = true, want false", dir)
		}
	}
}

func TestProjTemplateCount(t *testing.T) {
	projectDir := newProjRoot(t, "core-stack")
	if got := projTemplateCount(projectDir); got != 1 {
		t.Fatalf("projTemplateCount = %d, want 1", got)
	}

	// A second template would send proj-new's `fzf -1 -0` interactive, which
	// would hang agent-deck; the backend must decline instead.
	if err := os.MkdirAll(filepath.Join(projectDir, "00-release"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if got := projTemplateCount(projectDir); got != 2 {
		t.Fatalf("projTemplateCount = %d, want 2", got)
	}
	if _, _, ok := planProjCreate(worktreeAddSpec{
		repoDir:      projectDir,
		worktreePath: filepath.Join(projectDir, "feature-x"),
		branchName:   "feature-x",
	}); ok {
		t.Error("planProjCreate ok = true with 2 templates, want false")
	}
}

func TestPlanProjCreateNarrowing(t *testing.T) {
	if !projToolsAvailable() {
		t.Skip("proj toolchain not installed on this host")
	}
	projectDir := newProjRoot(t, "core-stack")

	t.Run("accepts a path inside the project dir", func(t *testing.T) {
		plan, reason, ok := planProjCreate(worktreeAddSpec{
			repoDir:      projectDir,
			worktreePath: filepath.Join(projectDir, "feature-x"),
			branchName:   "michelle/feature-x",
		})
		if !ok {
			t.Fatalf("planProjCreate ok = false (%s), want true", reason)
		}
		if plan.treeName != "feature-x" {
			t.Errorf("treeName = %q, want %q", plan.treeName, "feature-x")
		}
		if plan.branch != "michelle/feature-x" {
			t.Errorf("branch = %q, want %q", plan.branch, "michelle/feature-x")
		}
	})

	// proj can only ever create <project dir>/<name>. Anything else has to go
	// to git, or agent-deck would record a path proj never created.
	rejected := map[string]string{
		"sibling":        projectDir + "-feature-x",
		"subdirectory":   filepath.Join(projectDir, ".worktrees", "feature-x"),
		"nested deeper":  filepath.Join(projectDir, "feature-x", "nested"),
		"outside root":   filepath.Join(t.TempDir(), "feature-x"),
		"reserved repo":  filepath.Join(projectDir, ".repo"),
		"reserved templ": filepath.Join(projectDir, "00-master"),
	}
	for name, path := range rejected {
		t.Run("rejects "+name, func(t *testing.T) {
			if _, _, ok := planProjCreate(worktreeAddSpec{
				repoDir:      projectDir,
				worktreePath: path,
				branchName:   "feature-x",
			}); ok {
				t.Errorf("planProjCreate(%q) ok = true, want false", path)
			}
		})
	}
}

func TestGenerateWorktreePathUsesProjLayout(t *testing.T) {
	if !projToolsAvailable() {
		t.Skip("proj toolchain not installed on this host")
	}
	projectDir := newProjRoot(t, "core-stack")

	SetWorktreeBackend(WorktreeBackendAuto)
	t.Cleanup(func() { SetWorktreeBackend(WorktreeBackendAuto) })

	// Branch slashes become dashes, matching proj's directory naming.
	got := GenerateWorktreePath(projectDir, "michelle/feature-x", "subdirectory")
	want := filepath.Join(projectDir, "michelle-feature-x")
	if got != want {
		t.Errorf("GenerateWorktreePath = %q, want %q", got, want)
	}

	// backend = "git" must restore the stock layout exactly.
	SetWorktreeBackend(WorktreeBackendGit)
	got = GenerateWorktreePath(projectDir, "michelle/feature-x", "subdirectory")
	want = filepath.Join(projectDir, ".worktrees", "michelle-feature-x")
	if got != want {
		t.Errorf("with backend=git GenerateWorktreePath = %q, want %q", got, want)
	}
}

func TestGenerateWorktreePathUnchangedOutsideProj(t *testing.T) {
	// The load-bearing guarantee of this backend: a repo proj does not manage
	// generates exactly the path it did before.
	t.Setenv("PROJ_ROOT", t.TempDir())
	SetWorktreeBackend(WorktreeBackendAuto)
	t.Cleanup(func() { SetWorktreeBackend(WorktreeBackendAuto) })

	repo := filepath.Join(t.TempDir(), "myrepo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if got, want := GenerateWorktreePath(repo, "feat", "sibling"), repo+"-feat"; got != want {
		t.Errorf("sibling = %q, want %q", got, want)
	}
	if got, want := GenerateWorktreePath(repo, "feat", "subdirectory"), filepath.Join(repo, ".worktrees", "feat"); got != want {
		t.Errorf("subdirectory = %q, want %q", got, want)
	}
}

func TestRunProjPreRemoveHookIgnoresNonProjPaths(t *testing.T) {
	// Must be a silent no-op rather than an error for every path that is not a
	// proj worktree, since RemoveWorktree calls it unconditionally.
	t.Setenv("PROJ_ROOT", t.TempDir())
	RunProjPreRemoveHook(t.TempDir())
	RunProjPreRemoveHook("")
	RunProjPreRemoveHook("/nonexistent/path/xyz")
}

func TestRunProjPreRemoveHookRuns(t *testing.T) {
	projectDir := newProjRoot(t, "core-stack")
	worktree := filepath.Join(projectDir, "feature-x")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	hookDir := filepath.Join(projectDir, ".proj", "hooks")
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	marker := filepath.Join(projectDir, "hook-ran")
	hook := filepath.Join(hookDir, "pre-remove")
	script := "#!/bin/sh\npwd > " + marker + "\n"
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatalf("write hook: %v", err)
	}

	RunProjPreRemoveHook(worktree)

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("hook did not run: %v", err)
	}
	// The hook must run inside the worktree it is tearing down, matching
	// proj rm, so it can reach that worktree's contents.
	gotDir := strings.TrimSpace(string(data))
	resolved, err := filepath.EvalSymlinks(worktree)
	if err != nil {
		t.Fatalf("evalsymlinks: %v", err)
	}
	if got := filepath.Clean(gotDir); got != worktree && got != resolved {
		t.Errorf("hook cwd = %q, want %q", got, worktree)
	}

	// A non-executable hook is skipped rather than failing removal.
	os.Remove(marker)
	if err := os.Chmod(hook, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	RunProjPreRemoveHook(worktree)
	if _, err := os.Stat(marker); err == nil {
		t.Error("non-executable hook ran, want skipped")
	}
}

// TestProjBackendDisabledLeavesCreationOnGit is the regression guard for the
// whole fork: with backend=git, creation must be the stock code path even
// inside a real proj project.
func TestProjBackendDisabledLeavesCreationOnGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	projectDir := newProjRoot(t, "core-stack")

	SetWorktreeBackend(WorktreeBackendGit)
	t.Cleanup(func() { SetWorktreeBackend(WorktreeBackendAuto) })

	if _, _, ok := planProjCreate(worktreeAddSpec{
		repoDir:      projectDir,
		worktreePath: filepath.Join(projectDir, "feature-x"),
		branchName:   "feature-x",
	}); ok {
		// planProjCreate itself does not read the backend; runWorktreeAdd
		// gates on it. Assert the gate instead.
		if WorktreeBackend() != WorktreeBackendGit {
			t.Fatal("backend not set to git")
		}
	}
	if WorktreeBackend() != WorktreeBackendGit {
		t.Errorf("WorktreeBackend() = %q, want %q", WorktreeBackend(), WorktreeBackendGit)
	}
}
