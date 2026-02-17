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
	"strings"
)

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
	cmd := exec.Command("git", "-C", repoDir, "show-ref", "--verify", "--quiet", "refs/heads/"+branchName)
	err := cmd.Run()
	return err == nil
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

	// Check if it's a git repo
	if !IsGitRepo(repoDir) {
		return errors.New("not a git repository")
	}

	var cmd *exec.Cmd

	if BranchExists(repoDir, branchName) {
		// Use existing branch
		cmd = exec.Command("git", "-C", repoDir, "worktree", "add", worktreePath, branchName)
	} else {
		// Create new branch with -b flag
		cmd = exec.Command("git", "-C", repoDir, "worktree", "add", "-b", branchName, worktreePath)
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create worktree: %s: %w", strings.TrimSpace(string(output)), err)
	}

	return nil
}

// ListWorktrees returns all worktrees for the repository at repoDir
func ListWorktrees(repoDir string) ([]Worktree, error) {
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

	return worktrees
}

// RemoveWorktree removes a worktree from the repository
// If force is true, it will remove even if there are uncommitted changes
func RemoveWorktree(repoDir, worktreePath string, force bool) error {
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
		return fmt.Errorf("failed to remove worktree: %s: %w", strings.TrimSpace(string(output)), err)
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

// GetMainWorktreePath returns the path to the main worktree (original clone)
func GetMainWorktreePath(dir string) (string, error) {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--git-common-dir")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get common git dir: %w", err)
	}

	commonDir := strings.TrimSpace(string(output))

	// --git-common-dir may return a relative path; resolve it relative to dir
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Clean(filepath.Join(dir, commonDir))
	}

	// For worktrees, common-dir points to the main repo's .git directory
	// We need to get the parent of that
	if strings.HasSuffix(commonDir, ".git") {
		return strings.TrimSuffix(commonDir, string(filepath.Separator)+".git"), nil
	}

	// If already in main repo, just get toplevel
	return GetRepoRoot(dir)
}

// GetWorktreeBaseRoot returns the root of the main repository, resolving through
// worktrees if necessary. When called from a normal repo, it behaves identically
// to GetRepoRoot. When called from within a worktree, it follows --git-common-dir
// back to the main repo root, preventing worktree nesting.
func GetWorktreeBaseRoot(dir string) (string, error) {
	if IsWorktree(dir) {
		return GetMainWorktreePath(dir)
	}
	return GetRepoRoot(dir)
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
	re := regexp.MustCompile(`-+`)
	sanitized = re.ReplaceAllString(sanitized, "-")

	// Remove leading/trailing dashes
	sanitized = strings.Trim(sanitized, "-")

	return sanitized
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
