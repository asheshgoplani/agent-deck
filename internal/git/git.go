// Package git provides git worktree operations for agent-deck
package git

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var consecutiveDashesRe = regexp.MustCompile(`-+`)

// Worktree represents a git worktree
type Worktree struct {
	Path   string // Filesystem path to the worktree
	Branch string // Branch name checked out in this worktree
	Commit string // HEAD commit SHA
	Bare   bool   // Whether this is the bare repository
}

// IsGitRepo checks if the given directory is inside a git repository
func IsGitRepo(dir string) bool {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--git-dir")
	err := cmd.Run()
	return err == nil
}

// IsBareRepo returns true if dir itself is a bare git repository. For a linked
// worktree checkout, this returns false — use IsBareRepoWorktree for that case.
func IsBareRepo(dir string) bool {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--is-bare-repository")
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(output)) == "true"
}

// IsBareRepoWorktree returns true if dir is a linked worktree whose shared
// git-common-dir is itself a bare repository — covers both the nested ".bare/"
// pattern from issue #715 and the bare-at-root layout. In either case
// there is no "main" worktree; every linked worktree is equal.
func IsBareRepoWorktree(dir string) bool {
	commonDir, err := gitCommonDirAbs(dir)
	if err != nil {
		return false
	}
	return IsBareRepo(commonDir)
}

// isNestedBareLayout reports whether bareDir is the conventional nested
// ".bare/" form (issue #715). The alternative is a bare-at-root layout
// where the bare dir itself is the project root and linked worktrees live as
// direct children alongside HEAD/objects/refs (e.g. `git clone --bare repo.git`
// with worktrees added inside). The two are distinguished by convention: the
// reserved basename ".bare" marks nested; anything else is at-root.
func isNestedBareLayout(bareDir string) bool {
	return filepath.Base(filepath.Clean(bareDir)) == ".bare"
}

// IsBareRepoAtRoot returns true if dir is a bare repository serving as the
// project root itself (linked worktrees live as direct children inside it).
// False for normal repos, linked worktrees, and the nested ".bare/" layout.
func IsBareRepoAtRoot(dir string) bool {
	return IsBareRepo(dir) && !isNestedBareLayout(dir)
}

// gitCommonDirAbs returns the absolute path reported by
// `git rev-parse --git-common-dir`, resolving any relative path against dir.
func gitCommonDirAbs(dir string) (string, error) {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--git-common-dir")
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	commonDir := strings.TrimSpace(string(output))
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Clean(filepath.Join(dir, commonDir))
	}
	return commonDir, nil
}

// resolveWorktreeToToplevel returns the actual working tree for path via
// `git rev-parse --show-toplevel`. No-op for a regular working tree; for a
// submodule's gitdir (which `git worktree list --porcelain` reports as the
// worktree path for the main checkout) it returns the real working tree.
// Falls back to path on any git failure.
func resolveWorktreeToToplevel(path string) string {
	cmd := exec.Command("git", "-C", path, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return path
	}
	top := strings.TrimSpace(string(out))
	if top == "" {
		return path
	}
	return top
}

// isGitDir reports whether dir is a git directory (bare repo, a .git folder,
// .git/modules/<sub>, .git/worktrees/<wt>) rather than a real working tree.
// Used as a deletion-safety check before os.RemoveAll.
//
// Detection is path-structural plus IsBareRepo. `git rev-parse --show-toplevel`
// is unusable here: it errors out from inside .git/ and .git/worktrees/ (no
// working tree), AND it returns false-negative for a submodule gitdir under
// .git/modules/ (because that gitdir's core.worktree config makes git treat
// the submodule's working tree as the toplevel). Both classes are caught
// structurally instead.
//
// Non-git paths and orphaned worktree directories at user-chosen locations
// (the case exercised by TestRemoveWorktree's force-fallback path) are NOT
// flagged — they are legitimate os.RemoveAll targets.
func isGitDir(dir string) bool {
	if IsBareRepo(dir) {
		return true
	}
	clean := filepath.Clean(dir)
	if filepath.Base(clean) == ".git" {
		return true
	}
	parts := strings.Split(clean, string(filepath.Separator))
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == ".git" && (parts[i+1] == "modules" || parts[i+1] == "worktrees") {
			return true
		}
	}
	return false
}

// findNestedBareRepo returns the path to a bare git repository nested under
// dir, if one exists. The conventional layout from issue #715 places it at
// "<projectRoot>/.bare"; this helper first probes that path, then scans
// direct children as a fallback so alternative names still work.
//
// Uses isBareRepoSelf rather than IsBareRepo so that internal subdirs of a
// bare repo (hooks/, objects/, refs/, ...) aren't misidentified — IsBareRepo
// resolves via parent discovery, so any subdir of a bare repo reports true.
func findNestedBareRepo(dir string) string {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return ""
	}
	conventional := filepath.Join(dir, ".bare")
	if isBareRepoSelf(conventional) {
		return conventional
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		candidate := filepath.Join(dir, e.Name())
		if isBareRepoSelf(candidate) {
			return candidate
		}
	}
	return ""
}

// isBareRepoSelf returns true only when dir is itself the bare git
// repository, not merely a descendant of one. `git rev-parse
// --is-bare-repository` walks up the tree, so any subdir of a bare repo
// reports true; this helper additionally confirms the git-dir is the
// candidate itself (reported as "." by rev-parse).
func isBareRepoSelf(dir string) bool {
	if !IsBareRepo(dir) {
		return false
	}
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--git-dir")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	gitDir := strings.TrimSpace(string(out))
	if gitDir == "." || gitDir == "" {
		return true
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Clean(filepath.Join(dir, gitDir))
	}
	canonicalDir, _ := filepath.EvalSymlinks(dir)
	canonicalGit, _ := filepath.EvalSymlinks(gitDir)
	if canonicalDir == "" {
		canonicalDir = filepath.Clean(dir)
	}
	if canonicalGit == "" {
		canonicalGit = gitDir
	}
	return canonicalDir == canonicalGit
}

// resolveGitInvocationDir returns a directory suitable for `git -C <dir>`
// invocations. If dir is itself a git dir (worktree, main repo, or bare repo),
// it is returned unchanged. If dir is a bare-repo project root — i.e. not a
// git dir but contains a nested bare repo — the path to that bare repo is
// returned. Callers can pass the project root transparently.
func resolveGitInvocationDir(dir string) string {
	if IsGitRepo(dir) {
		return dir
	}
	if bare := findNestedBareRepo(dir); bare != "" {
		return bare
	}
	return dir
}

// IsGitRepoOrBareProjectRoot returns true if dir is either a regular git
// directory (normal repo, linked worktree, or a bare repo) or the project
// root of a bare-repo layout (contains a nested bare repo such as .bare/).
// Callers that need to validate "is this a path agent-deck can launch a
// session from?" should prefer this over IsGitRepo.
func IsGitRepoOrBareProjectRoot(dir string) bool {
	if IsGitRepo(dir) {
		return true
	}
	return findNestedBareRepo(dir) != ""
}

// GetRepoRoot returns the root directory of the git repository containing dir
func GetRepoRoot(dir string) (string, error) {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("not a git repository: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// GetCurrentBranch returns the current branch name for the repository at dir
func GetCurrentBranch(dir string) (string, error) {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get current branch: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// BranchExists checks if a branch exists in the repository
func BranchExists(repoDir, branchName string) bool {
	repoDir = resolveGitInvocationDir(repoDir)
	cmd := exec.Command("git", "-C", repoDir, "show-ref", "--verify", "--quiet", "refs/heads/"+branchName)
	err := cmd.Run()
	return err == nil
}

func remoteBranchExists(repoDir, remoteName, branchName string) bool {
	repoDir = resolveGitInvocationDir(repoDir)
	cmd := exec.Command("git", "-C", repoDir, "show-ref", "--verify", "--quiet", "refs/remotes/"+remoteName+"/"+branchName)
	err := cmd.Run()
	return err == nil
}

type worktreeBranchMode int

const (
	worktreeBranchNew worktreeBranchMode = iota
	worktreeBranchLocal
	worktreeBranchRemote
)

type worktreeBranchResolution struct {
	Branch string
	Mode   worktreeBranchMode
	Remote string
}

// ValidateBranchName validates that a branch name follows git's naming rules
func ValidateBranchName(name string) error {
	if name == "" {
		return errors.New("branch name cannot be empty")
	}

	// Check for leading/trailing spaces
	if strings.TrimSpace(name) != name {
		return errors.New("branch name cannot have leading or trailing spaces")
	}

	// Check for double dots
	if strings.Contains(name, "..") {
		return errors.New("branch name cannot contain '..'")
	}

	// Check for starting with dot
	if strings.HasPrefix(name, ".") {
		return errors.New("branch name cannot start with '.'")
	}

	// Check for ending with .lock
	if strings.HasSuffix(name, ".lock") {
		return errors.New("branch name cannot end with '.lock'")
	}

	// Check for invalid characters
	invalidChars := []string{" ", "\t", "~", "^", ":", "?", "*", "[", "\\"}
	for _, char := range invalidChars {
		if strings.Contains(name, char) {
			return fmt.Errorf("branch name cannot contain '%s'", char)
		}
	}

	// Check for @{ sequence
	if strings.Contains(name, "@{") {
		return errors.New("branch name cannot contain '@{'")
	}

	// Check for just @
	if name == "@" {
		return errors.New("branch name cannot be just '@'")
	}

	return nil
}

// GenerateWorktreePath generates a worktree directory path based on the
// repository directory, branch name, and location strategy.
// Location "subdirectory" places worktrees under <repo>/.worktrees/<branch>.
// Location "sibling" (or empty) places worktrees as <repo>-<branch> alongside the repo.
// A custom path (containing "/" or starting with "~") places worktrees at <path>/<repo_name>/<branch>.
//
// True-bare-at-root layout overrides the sibling/subdirectory defaults: linked
// worktrees live as direct children of the bare dir (<repo>/<branch>), since
// neither default makes sense when the project root *is* the bare repo. Custom
// path templates still take precedence (see WorktreePath in template.go).
func GenerateWorktreePath(repoDir, branchName, location string) string {
	// Sanitize branch name for filesystem
	sanitized := branchName
	sanitized = strings.ReplaceAll(sanitized, "/", "-")
	sanitized = strings.ReplaceAll(sanitized, " ", "-")

	// Custom path: contains "/" or starts with "~"
	if strings.Contains(location, "/") || strings.HasPrefix(location, "~") {
		expanded := location
		if strings.HasPrefix(expanded, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				expanded = filepath.Join(home, expanded[2:])
			}
		} else if expanded == "~" {
			if home, err := os.UserHomeDir(); err == nil {
				expanded = home
			}
		}
		repoName := filepath.Base(repoDir)
		return filepath.Join(expanded, repoName, sanitized)
	}

	if IsBareRepoAtRoot(repoDir) {
		return filepath.Join(repoDir, sanitized)
	}

	switch location {
	case "subdirectory":
		return filepath.Join(repoDir, ".worktrees", sanitized)
	default: // "sibling" or empty
		return repoDir + "-" + sanitized
	}
}

// CreateWorktree creates a new git worktree at worktreePath for the given branch
// If the branch doesn't exist, it will be created
func CreateWorktree(repoDir, worktreePath, branchName string) error {
	// Validate branch name first
	if err := ValidateBranchName(branchName); err != nil {
		return fmt.Errorf("invalid branch name: %w", err)
	}

	// Transparently resolve a bare-repo project root (no .git, but contains
	// a nested bare repo like .bare/) to the underlying git dir.
	repoDir = resolveGitInvocationDir(repoDir)

	// Check if it's a git repo
	if !IsGitRepo(repoDir) {
		return errors.New("not a git repository")
	}

	resolution, err := resolveWorktreeBranch(repoDir, branchName)
	if err != nil {
		return err
	}

	var cmd *exec.Cmd
	switch resolution.Mode {
	case worktreeBranchLocal:
		// Reuse an existing local branch.
		cmd = exec.Command("git", "-C", repoDir, "worktree", "add", worktreePath, branchName)
	case worktreeBranchRemote:
		// Create a local tracking branch from the default remote.
		remoteRef := resolution.Remote + "/" + branchName
		cmd = exec.Command("git", "-C", repoDir, "worktree", "add", "--track", "-b", branchName, worktreePath, remoteRef)
	default:
		// Create a new local branch. Regression #973: if an origin remote
		// exists, fetch its default branch and root the new branch there
		// rather than at the caller's local HEAD — which can be an old tag
		// (the 414-file near-miss). Fetch is best-effort: offline / no remote
		// falls through to a HEAD-based branch.
		if base, ok := freshOriginDefaultBranchRef(repoDir); ok {
			cmd = exec.Command("git", "-C", repoDir, "worktree", "add", "-b", branchName, worktreePath, base)
		} else {
			cmd = exec.Command("git", "-C", repoDir, "worktree", "add", "-b", branchName, worktreePath)
		}
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create worktree: %s: %w", strings.TrimSpace(string(output)), err)
	}

	return nil
}

// ListWorktrees returns all worktrees for the repository at repoDir
func ListWorktrees(repoDir string) ([]Worktree, error) {
	repoDir = resolveGitInvocationDir(repoDir)
	if !IsGitRepo(repoDir) {
		return nil, errors.New("not a git repository")
	}

	cmd := exec.Command("git", "-C", repoDir, "worktree", "list", "--porcelain")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list worktrees: %w", err)
	}

	return parseWorktreeList(string(output)), nil
}

// parseWorktreeList parses the output of `git worktree list --porcelain`
func parseWorktreeList(output string) []Worktree {
	var worktrees []Worktree
	var current Worktree

	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()

		if line == "" {
			// Empty line marks end of worktree entry
			if current.Path != "" {
				worktrees = append(worktrees, current)
			}
			current = Worktree{}
			continue
		}

		if strings.HasPrefix(line, "worktree ") {
			current.Path = strings.TrimPrefix(line, "worktree ")
		} else if strings.HasPrefix(line, "HEAD ") {
			current.Commit = strings.TrimPrefix(line, "HEAD ")
		} else if strings.HasPrefix(line, "branch ") {
			// Branch is in format "refs/heads/branch-name"
			branch := strings.TrimPrefix(line, "branch ")
			branch = strings.TrimPrefix(branch, "refs/heads/")
			current.Branch = branch
		} else if line == "bare" {
			current.Bare = true
		} else if line == "detached" {
			// Detached HEAD, branch will be empty
			current.Branch = ""
		}
	}

	// Don't forget the last entry if output doesn't end with empty line
	if current.Path != "" {
		worktrees = append(worktrees, current)
	}

	// `git worktree list --porcelain` reports the gitdir (not the working
	// tree) for a plain submodule's main checkout — normalize it back.
	for i := range worktrees {
		if !worktrees[i].Bare {
			worktrees[i].Path = resolveWorktreeToToplevel(worktrees[i].Path)
		}
	}

	return worktrees
}

// RemoveWorktree removes a worktree from the repository.
// If force is true, it will remove even if there are uncommitted changes.
// When force is true and git fails (e.g. "Directory not empty" due to
// untracked files like node_modules), falls back to removing the directory
// directly and pruning stale worktree references.
func RemoveWorktree(repoDir, worktreePath string, force bool) error {
	repoDir = resolveGitInvocationDir(repoDir)
	if !IsGitRepo(repoDir) {
		return errors.New("not a git repository")
	}

	args := []string{"-C", repoDir, "worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, worktreePath)

	cmd := exec.Command("git", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if !force {
			return fmt.Errorf("failed to remove worktree: %s: %w", strings.TrimSpace(string(output)), err)
		}
		// Force mode: git worktree remove --force can still fail when the
		// directory contains untracked content. Fall back to deleting the
		// directory and pruning the stale worktree reference.
		//
		// Refuse if the path is a git directory (bare repo, .git/modules/<sub>,
		// .git/worktrees/<wt>). A pre-fix bug stored a submodule gitdir as
		// WorktreePath; without this guard, session deletion destroyed the
		// submodule's git history.
		if isGitDir(worktreePath) {
			return fmt.Errorf("refusing to remove %q: path is a git directory, not a working tree (likely a stale session row from before the submodule path-normalization fix)", worktreePath)
		}
		if rmErr := os.RemoveAll(worktreePath); rmErr != nil {
			return fmt.Errorf("failed to remove worktree directory: %w (git error: %s)", rmErr, strings.TrimSpace(string(output)))
		}
		return PruneWorktrees(repoDir)
	}

	return nil
}

// GetWorktreeForBranch returns the worktree path for a given branch, if any
func GetWorktreeForBranch(repoDir, branchName string) (string, error) {
	worktrees, err := ListWorktrees(repoDir)
	if err != nil {
		return "", err
	}

	for _, wt := range worktrees {
		if wt.Branch == branchName {
			return wt.Path, nil
		}
	}

	return "", nil
}

// IsWorktree checks if the given directory is a git worktree (not the main repo)
func IsWorktree(dir string) bool {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--git-common-dir")
	output, err := cmd.Output()
	if err != nil {
		return false
	}

	commonDir := strings.TrimSpace(string(output))

	cmd = exec.Command("git", "-C", dir, "rev-parse", "--git-dir")
	output, err = cmd.Output()
	if err != nil {
		return false
	}

	gitDir := strings.TrimSpace(string(output))

	// If common-dir and git-dir differ, it's a worktree
	return commonDir != gitDir && commonDir != "."
}

// GetMainWorktreePath returns the path to the "project root" — the directory
// that hosts shared config like .agent-deck/.
//
//   - Normal repo: the repo root (parent of .git/).
//   - Linked worktree of a normal repo: the main worktree (parent of .git/).
//   - Nested ".bare/" layout (issue #715): the parent of .bare/. There is no
//     "main" worktree; every linked worktree is equal, shared config lives
//     next to .bare/.
//   - True-bare-at-root layout: the bare repo dir itself (e.g. "kslifeinc.git").
//     Linked worktrees live as direct children, shared config lives inside.
func GetMainWorktreePath(dir string) (string, error) {
	commonDir, err := gitCommonDirAbs(dir)
	if err != nil {
		return "", fmt.Errorf("failed to get common git dir: %w", err)
	}

	// Bare common-dir: choose project root based on layout convention.
	if IsBareRepo(commonDir) {
		if isNestedBareLayout(commonDir) {
			return filepath.Dir(commonDir), nil
		}
		return commonDir, nil
	}

	// Normal worktree: common-dir ends in .git; strip it to get the main worktree root.
	if strings.HasSuffix(commonDir, ".git") {
		return strings.TrimSuffix(commonDir, string(filepath.Separator)+".git"), nil
	}

	// Already in the main repo.
	return GetRepoRoot(dir)
}

// GetWorktreeBaseRoot returns the "project root" suitable for locating shared
// config (.agent-deck/, setup scripts, etc.). Accepts:
//
//   - A normal repo dir → repo root.
//   - A linked worktree → the main worktree (or, for bare-repo layouts, the
//     parent of .bare/ or the bare-at-root dir itself).
//   - A nested-bare project root (no .git but contains .bare/) → that root.
//   - A bare-at-root project root → the bare dir itself.
//
// This guarantees that downstream .agent-deck/ lookups resolve to a single
// stable location regardless of which worktree (or the project root) the
// caller started from.
func GetWorktreeBaseRoot(dir string) (string, error) {
	if IsGitRepo(dir) {
		if IsWorktree(dir) {
			return GetMainWorktreePath(dir)
		}
		// Bare repo with no linked-worktree context: pick project root by layout.
		if IsBareRepo(dir) {
			if isNestedBareLayout(dir) {
				return filepath.Dir(dir), nil
			}
			return dir, nil
		}
		return GetRepoRoot(dir)
	}
	// Not a git dir itself — might be a project root with a nested bare repo.
	if bare := findNestedBareRepo(dir); bare != "" {
		return GetMainWorktreePath(bare)
	}
	return "", fmt.Errorf("not a git repository: %s", dir)
}

// SanitizeBranchName converts a string to a valid branch name
func SanitizeBranchName(name string) string {
	// Replace common invalid characters
	replacer := strings.NewReplacer(
		" ", "-",
		"..", "-",
		"~", "-",
		"^", "-",
		":", "-",
		"?", "-",
		"*", "-",
		"[", "-",
		"\\", "-",
		"@{", "-",
	)

	sanitized := replacer.Replace(name)

	// Remove leading dots
	for strings.HasPrefix(sanitized, ".") {
		sanitized = strings.TrimPrefix(sanitized, ".")
	}

	// Remove trailing .lock
	for strings.HasSuffix(sanitized, ".lock") {
		sanitized = strings.TrimSuffix(sanitized, ".lock")
	}

	// Remove consecutive dashes
	sanitized = consecutiveDashesRe.ReplaceAllString(sanitized, "-")

	// Remove leading/trailing dashes
	sanitized = strings.Trim(sanitized, "-")

	return sanitized
}

// freshOriginDefaultBranchRef fetches the default branch from the default
// remote and returns the remote-tracking ref (e.g. "origin/main") that callers
// should base a new branch on. Returns ok=false when there is no remote, no
// resolvable default branch, or the fetch fails (e.g. offline) — in which
// case callers must fall back to a HEAD-based branch.
//
// Pinned by TestCreateWorktree_NewBranch_BranchesFromFreshOriginMain_RegressionFor973.
func freshOriginDefaultBranchRef(repoDir string) (string, bool) {
	remote, err := getDefaultRemote(repoDir)
	if err != nil || remote == "" {
		return "", false
	}
	defaultBranch, err := GetDefaultBranch(repoDir)
	if err != nil || defaultBranch == "" {
		return "", false
	}
	fetch := exec.Command("git", "-C", repoDir, "fetch", "--quiet", remote, defaultBranch)
	if err := fetch.Run(); err != nil {
		return "", false
	}
	return remote + "/" + defaultBranch, true
}

func resolveWorktreeBranch(repoDir, branchName string) (worktreeBranchResolution, error) {
	if !IsGitRepo(repoDir) {
		return worktreeBranchResolution{}, errors.New("not a git repository")
	}

	resolution := worktreeBranchResolution{
		Branch: branchName,
		Mode:   worktreeBranchNew,
	}

	if BranchExists(repoDir, branchName) {
		resolution.Mode = worktreeBranchLocal
		return resolution, nil
	}

	defaultRemote, err := getDefaultRemote(repoDir)
	if err == nil && defaultRemote != "" && remoteBranchExists(repoDir, defaultRemote, branchName) {
		resolution.Mode = worktreeBranchRemote
		resolution.Remote = defaultRemote
	}

	return resolution, nil
}

func getDefaultRemote(repoDir string) (string, error) {
	remotes, err := listRemotes(repoDir)
	if err != nil {
		return "", err
	}
	if len(remotes) == 0 {
		return "", errors.New("no git remotes configured")
	}

	currentBranch, err := GetCurrentBranch(repoDir)
	if err == nil && currentBranch != "" && currentBranch != "HEAD" {
		cmd := exec.Command("git", "-C", repoDir, "config", "--get", "branch."+currentBranch+".remote")
		output, err := cmd.Output()
		if err == nil {
			remote := strings.TrimSpace(string(output))
			if remote != "" {
				return remote, nil
			}
		}
	}

	for _, remote := range remotes {
		if remote == "origin" {
			return remote, nil
		}
	}

	if len(remotes) == 1 {
		return remotes[0], nil
	}

	return "", fmt.Errorf("could not determine default remote from %d remotes", len(remotes))
}

func listRemotes(repoDir string) ([]string, error) {
	cmd := exec.Command("git", "-C", repoDir, "remote")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list remotes: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	var remotes []string
	for _, line := range lines {
		remote := strings.TrimSpace(line)
		if remote != "" {
			remotes = append(remotes, remote)
		}
	}
	return remotes, nil
}

func listRefShortNames(repoDir string, refs ...string) ([]string, error) {
	args := []string{"-C", repoDir, "for-each-ref", "--format=%(refname:short)"}
	args = append(args, refs...)
	cmd := exec.Command("git", args...)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list refs: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	var names []string
	for _, line := range lines {
		name := strings.TrimSpace(line)
		if name != "" {
			names = append(names, name)
		}
	}
	return names, nil
}

// ListBranchCandidates returns unique branch names from local branches and the
// default remote, normalized to plain branch names without a remote prefix.
func ListBranchCandidates(repoDir string) ([]string, error) {
	repoDir = resolveGitInvocationDir(repoDir)
	if !IsGitRepo(repoDir) {
		return nil, errors.New("not a git repository")
	}

	repoRoot, err := GetWorktreeBaseRoot(repoDir)
	if err == nil && repoRoot != "" {
		repoDir = repoRoot
	}

	branches, err := listRefShortNames(repoDir, "refs/heads")
	if err != nil {
		return nil, err
	}

	seen := make(map[string]struct{}, len(branches))
	for _, branch := range branches {
		seen[branch] = struct{}{}
	}

	// Iterate all remotes (origin, fork remotes, etc.) and keep the remote prefix
	// so callers can distinguish local branches from remote-only branches.
	remotes, err := listRemotes(repoDir)
	if err == nil {
		for _, remote := range remotes {
			remoteBranches, err := listRefShortNames(repoDir, "refs/remotes/"+remote)
			if err != nil {
				continue
			}
			for _, branch := range remoteBranches {
				if branch == remote+"/HEAD" {
					continue
				}
				if branch == "" {
					continue
				}
				seen[branch] = struct{}{}
			}
		}
	}

	branches = branches[:0]
	for branch := range seen {
		branches = append(branches, branch)
	}
	sort.Strings(branches)
	return branches, nil
}

// HasUncommittedChanges checks if the repository at dir has uncommitted changes
func HasUncommittedChanges(dir string) (bool, error) {
	cmd := exec.Command("git", "-C", dir, "status", "--porcelain")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("failed to check git status: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return strings.TrimSpace(string(output)) != "", nil
}

// GetDefaultBranch returns the default branch name (e.g. "main" or "master") for the repo
func GetDefaultBranch(repoDir string) (string, error) {
	// Try symbolic-ref first (works when remote HEAD is set)
	cmd := exec.Command("git", "-C", repoDir, "symbolic-ref", "refs/remotes/origin/HEAD")
	output, err := cmd.Output()
	if err == nil {
		ref := strings.TrimSpace(string(output))
		branch := strings.TrimPrefix(ref, "refs/remotes/origin/")
		if branch != ref && branch != "" {
			return branch, nil
		}
	}

	// Fallback: check for common default branch names
	if BranchExists(repoDir, "main") {
		return "main", nil
	}
	if BranchExists(repoDir, "master") {
		return "master", nil
	}

	return "", errors.New("could not determine default branch (no origin/HEAD, no main or master branch)")
}

// MergeBranch merges the given branch into the current branch of the repository
func MergeBranch(repoDir, branchName string) error {
	cmd := exec.Command("git", "-C", repoDir, "merge", branchName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("merge failed: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

// DeleteBranch deletes a local branch. If force is true, uses -D (force delete).
func DeleteBranch(repoDir, branchName string, force bool) error {
	flag := "-d"
	if force {
		flag = "-D"
	}
	cmd := exec.Command("git", "-C", repoDir, "branch", flag, branchName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to delete branch: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

// PruneWorktrees removes stale worktree references
func PruneWorktrees(repoDir string) error {
	cmd := exec.Command("git", "-C", repoDir, "worktree", "prune")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to prune worktrees: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}
