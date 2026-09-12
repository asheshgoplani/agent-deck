package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
)

// This file adds an optional `proj` backend for worktree creation.
//
// proj (https://github.com/Applied-Shared/proj) creates a worktree by
// reflink-copying an existing template worktree instead of checking the tree
// out from scratch. On a multi-gigabyte monorepo that is the difference
// between seconds and minutes, and the copy carries over ignored build
// artifacts so the new worktree starts with a warm build cache rather than a
// cold one.
//
// proj hardcodes its own layout, so it cannot serve every request agent-deck
// makes. Everything here is therefore a *narrowing* check: unless the
// repository, the requested path, and the host tooling all match what proj
// supports, creation falls through to the ordinary `git worktree add` path and
// behavior is byte-for-byte unchanged.

// Worktree backend values for [worktree] backend in config.toml.
const (
	// WorktreeBackendAuto uses proj when the repository layout and host
	// tooling support it, and plain git otherwise. The default.
	WorktreeBackendAuto = "auto"
	// WorktreeBackendProj is auto, except that a repository proj cannot serve
	// is an error rather than a silent fallback — for catching a misconfigured
	// layout instead of quietly losing the reflink speedup.
	WorktreeBackendProj = "proj"
	// WorktreeBackendGit disables the proj backend entirely.
	WorktreeBackendGit = "git"
)

// projDefaultRoot mirrors proj's own PROJ_ROOT default. Keep in sync with the
// `PROJ_ROOT="${PROJ_ROOT:-/mnt/work}"` line in the proj entrypoint: if the two
// disagree, detection here and proj's actual behavior diverge.
const projDefaultRoot = "/mnt/work"

// projTemplatePrefix is the directory-name prefix proj treats as a template
// worktree to copy from ("00-master", "00-main", ...).
const projTemplatePrefix = "00-"

// worktreeBackend holds the configured [worktree] backend. The git package
// deliberately does not read config itself (see WorktreeCreateOptions), so the
// session layer pushes the resolved value in at startup via SetWorktreeBackend.
// atomic.Value keeps a concurrent TUI redraw from racing a session create.
var worktreeBackend atomic.Value

// ParseWorktreeBackend normalizes a configured backend name. Unknown values
// resolve to auto, so a typo can never silently disable worktree creation.
func ParseWorktreeBackend(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case WorktreeBackendGit:
		return WorktreeBackendGit
	case WorktreeBackendProj:
		return WorktreeBackendProj
	default:
		return WorktreeBackendAuto
	}
}

// SetWorktreeBackend records the configured backend for subsequent worktree
// creation. Called once during startup config resolution.
func SetWorktreeBackend(s string) { worktreeBackend.Store(ParseWorktreeBackend(s)) }

// WorktreeBackend returns the configured backend, defaulting to auto.
func WorktreeBackend() string {
	if v, ok := worktreeBackend.Load().(string); ok && v != "" {
		return v
	}
	return WorktreeBackendAuto
}

// projProject is a resolved proj-managed project: the parent directory that
// holds .repo, the template worktrees, and every created worktree as a sibling.
type projProject struct {
	// root is PROJ_ROOT, the directory holding projects/.
	root string
	// dir is <root>/projects/<project>, proj's unit of work. proj-new must run
	// with this as its working directory.
	dir string
}

// projRoot resolves PROJ_ROOT the way proj itself does: the environment wins,
// otherwise the compiled-in default.
func projRoot() string {
	if v := strings.TrimSpace(os.Getenv("PROJ_ROOT")); v != "" {
		return v
	}
	return projDefaultRoot
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// resolveProjProject walks up from dir looking for a proj project: a directory
// sitting directly under $PROJ_ROOT/projects that contains a .repo checkout.
//
// Walking up (rather than matching dir exactly) is what lets a caller pass any
// of the handles agent-deck might hold — the project dir, its .repo, one of its
// worktrees, or a subdirectory of one — and still resolve the same project.
func resolveProjProject(dir string) (projProject, bool) {
	if strings.TrimSpace(dir) == "" {
		return projProject{}, false
	}
	root := projRoot()
	projects := filepath.Join(root, "projects")

	abs, err := filepath.Abs(dir)
	if err != nil {
		return projProject{}, false
	}

	for p := filepath.Clean(abs); ; {
		if filepath.Dir(p) == projects {
			if !isDir(filepath.Join(p, ".repo")) {
				return projProject{}, false
			}
			return projProject{root: root, dir: p}, true
		}
		parent := filepath.Dir(p)
		if parent == p {
			return projProject{}, false
		}
		p = parent
	}
}

// projTemplateCount reports how many 00-* template worktrees the project has.
//
// It matters that this is exactly one. proj-new picks the template with
// `fzf -1 -0`, which auto-selects a lone candidate but opens an *interactive*
// picker when several match — that would hang agent-deck, which runs proj
// without a terminal. Zero templates is a plain proj-new failure. Either way
// the count must be 1 before this backend is safe to use.
func projTemplateCount(projectDir string) int {
	entries, err := os.ReadDir(projectDir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), projTemplatePrefix) {
			n++
		}
	}
	return n
}

// projToolsAvailable reports whether the host can actually run proj-new to
// completion. Each binary here is one proj-new depends on at a different stage,
// and a missing one fails *after* proj has already created a branch and copied
// a tree — so all of them are checked up front instead.
func projToolsAvailable() bool {
	for _, bin := range []string{
		"proj",     // the entrypoint
		"proj-new", // dispatched to by `proj new`
		"fzf",      // template selection
		// proj relocates the new worktree's index with `git relocate`, a
		// non-upstream subcommand. Without it proj-new aborts at its last step,
		// having already done all of its work.
		"git-relocate",
	} {
		if _, err := exec.LookPath(bin); err != nil {
			return false
		}
	}
	return true
}

// projPlan is a creation request that the proj backend can serve.
type projPlan struct {
	project  projProject
	treeName string
	branch   string
}

// planProjCreate decides whether proj can create exactly the worktree spec
// describes. ok=false means the caller should use plain `git worktree add`;
// the returned reason explains why, for the WorktreeBackendProj error path.
func planProjCreate(spec worktreeAddSpec) (projPlan, string, bool) {
	project, ok := resolveProjProject(spec.repoDir)
	if !ok {
		return projPlan{}, fmt.Sprintf("%s is not inside a proj project (expected <%s>/projects/<name> containing .repo)", spec.repoDir, projRoot()), false
	}
	if n := projTemplateCount(project.dir); n != 1 {
		return projPlan{}, fmt.Sprintf("proj project %s has %d %s* template worktrees, need exactly 1", project.dir, n, projTemplatePrefix), false
	}
	if !projToolsAvailable() {
		return projPlan{}, "proj, proj-new, fzf or git-relocate is not on PATH", false
	}

	// proj always creates the worktree at <project dir>/<name>. A request for
	// any other path (a sibling "repo-branch", a .worktrees/ subdirectory, a
	// custom template root) cannot be honored without lying to the caller
	// about where the worktree landed, so it goes to git instead.
	clean := filepath.Clean(spec.worktreePath)
	if filepath.Dir(clean) != project.dir {
		return projPlan{}, fmt.Sprintf("requested path %s is outside the proj project directory %s", clean, project.dir), false
	}
	name := filepath.Base(clean)
	if name == "" || name == "." || name == string(filepath.Separator) {
		return projPlan{}, fmt.Sprintf("cannot derive a worktree name from %s", clean), false
	}
	// Never let a worktree land on proj's own reserved directories: .repo is
	// the checkout every worktree is linked to, and a 00-* name is the template
	// proj copies from.
	if name == ".repo" || strings.HasPrefix(name, projTemplatePrefix) {
		return projPlan{}, fmt.Sprintf("%q is reserved by proj", name), false
	}

	return projPlan{project: project, treeName: name, branch: spec.branchName}, "", true
}

// runProjNew creates the worktree through proj.
//
// The branch is materialized here, before proj runs, and proj is then always
// invoked in its --existing mode. That is deliberate: left to itself proj names
// the branch $PROJ_PREFIX/<treename>, which would quietly override the branch
// agent-deck resolved (and its start point — see the origin/default-branch
// rooting in CreateWorktreeWithOptions). Creating the branch first and handing
// it to proj keeps proj responsible for the fast copy and nothing else.
func runProjNew(plan projPlan, spec worktreeAddSpec) error {
	createdBranch, err := ensureProjBranch(plan, spec)
	if err != nil {
		return fmt.Errorf("%s: %w", spec.failMsg, err)
	}

	args := []string{"new", "--existing", plan.branch}
	if spec.branch.Mode == worktreeBranchRemote && spec.branch.Remote != "" {
		args = append(args, "--remote", spec.branch.Remote)
	}
	args = append(args, plan.treeName)

	cmd := exec.Command("proj", args...)
	// proj-new refuses to run unless its working directory is the project dir.
	cmd.Dir = plan.project.dir
	// Pin PROJ_ROOT to the root detection resolved, so proj cannot disagree
	// with us about which layout it is operating on.
	cmd.Env = append(os.Environ(), "PROJ_ROOT="+plan.project.root)

	if output, cmdErr := cmd.CombinedOutput(); cmdErr != nil {
		rollbackProjBranch(spec, plan, createdBranch)
		return fmt.Errorf("%s: proj new failed: %s: %w", spec.failMsg, strings.TrimSpace(string(output)), cmdErr)
	}

	// proj reports success before the tree is usable in one case worth
	// catching: a copy that silently produced nothing at the expected path.
	if !isDir(spec.worktreePath) {
		rollbackProjBranch(spec, plan, createdBranch)
		return fmt.Errorf("%s: proj new reported success but %s does not exist", spec.failMsg, spec.worktreePath)
	}

	if !spec.sparse.Enabled {
		return nil
	}
	// A reflinked worktree inherits the *template's* sparse state, not the
	// state of the worktree the caller asked to inherit from, so the requested
	// patterns are replayed over the copy. There is no --no-checkout saving to
	// lose here the way there is on the git path: the tree is already a cheap
	// reflink by this point, and narrowing it is a local operation.
	if applyErr := ApplySparseCheckout(spec.worktreePath, spec.sparse); applyErr != nil {
		var cleanupErrs []string
		if rmErr := RemoveWorktree(spec.repoDir, spec.worktreePath, true); rmErr != nil {
			cleanupErrs = append(cleanupErrs, fmt.Sprintf("worktree remove failed: %v", rmErr))
		}
		if createdBranch {
			if brErr := DeleteBranch(spec.repoDir, plan.branch, true); brErr != nil {
				cleanupErrs = append(cleanupErrs, fmt.Sprintf("branch delete failed: %v", brErr))
			}
		}
		if len(cleanupErrs) > 0 {
			return fmt.Errorf("inherit sparse checkout: %w; cleanup failed: %s", applyErr, strings.Join(cleanupErrs, "; "))
		}
		return fmt.Errorf("inherit sparse checkout: %w", applyErr)
	}
	return nil
}

// ensureProjBranch creates the branch proj will check out, reproducing the
// branch resolution the git path would have applied. It reports whether this
// call created the branch, so a later failure can roll it back.
func ensureProjBranch(plan projPlan, spec worktreeAddSpec) (bool, error) {
	if BranchExists(spec.repoDir, plan.branch) {
		return false, nil
	}

	var args []string
	switch {
	case spec.branch.Mode == worktreeBranchRemote && spec.branch.Remote != "":
		// Match the git path's `--track -b <branch> <remote>/<branch>`.
		args = []string{"-C", spec.repoDir, "branch", "--track", plan.branch, spec.branch.Remote + "/" + plan.branch}
	case strings.TrimSpace(spec.branch.StartPoint) != "":
		args = []string{"-C", spec.repoDir, "branch", plan.branch, spec.branch.StartPoint}
	default:
		args = []string{"-C", spec.repoDir, "branch", plan.branch}
	}

	if output, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		return false, fmt.Errorf("failed to create branch %q: %s: %w", plan.branch, strings.TrimSpace(string(output)), err)
	}
	return true, nil
}

// rollbackProjBranch removes a branch this package created for a proj run that
// then failed, so a retry does not trip over a leftover branch. Best effort:
// the caller is already returning the original failure.
func rollbackProjBranch(spec worktreeAddSpec, plan projPlan, created bool) {
	if !created {
		return
	}
	_ = DeleteBranch(spec.repoDir, plan.branch, true)
}

// ProjWorktreePath returns the path proj would create for branchName in a proj
// project containing repoDir, so path generation agrees with what the proj
// backend will actually do. ok=false means repoDir is not proj-managed (or the
// backend is off) and the caller should use its normal layout rules.
func ProjWorktreePath(repoDir, branchName string) (string, bool) {
	if WorktreeBackend() == WorktreeBackendGit {
		return "", false
	}
	project, ok := resolveProjProject(repoDir)
	if !ok {
		return "", false
	}
	if projTemplateCount(project.dir) != 1 || !projToolsAvailable() {
		return "", false
	}
	name := strings.ReplaceAll(branchName, "/", "-")
	name = strings.ReplaceAll(name, " ", "-")
	if name == "" || name == ".repo" || strings.HasPrefix(name, projTemplatePrefix) {
		return "", false
	}
	return filepath.Join(project.dir, name), true
}

// RunProjPreRemoveHook runs a proj project's .proj/hooks/pre-remove for
// worktreePath, the teardown counterpart to the post-new hook proj runs during
// creation. `proj rm` is otherwise just `git worktree remove`, which
// RemoveWorktree already does — this hook is the only part of proj's removal
// that agent-deck would otherwise skip, typically leaving whatever the hook
// tears down (a dev container, a port binding) running after the session is
// gone.
//
// Best effort by design, matching RunWorktreeDestructionBeforeRemove: removal
// must proceed even when the hook fails.
func RunProjPreRemoveHook(worktreePath string) {
	project, ok := resolveProjProject(worktreePath)
	if !ok {
		return
	}
	// Only for a worktree sitting directly in the project dir — never for the
	// project dir itself, .repo, or a template.
	if filepath.Dir(filepath.Clean(worktreePath)) != project.dir {
		return
	}
	hook := filepath.Join(project.dir, ".proj", "hooks", "pre-remove")
	info, err := os.Stat(hook)
	if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		return
	}
	cmd := exec.Command(hook)
	cmd.Dir = worktreePath
	cmd.Env = append(os.Environ(), "PROJ_ROOT="+project.root)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
}
