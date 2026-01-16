// Package git provides git worktree operations for agent-deck
package git

import (
	"bufio"
	"errors"
	"fmt"
	"os/exec"
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

// GenerateWorktreePath generates a sibling directory path for a worktree
// based on the repository directory and branch name
func GenerateWorktreePath(repoDir, branchName string) string {
	// Sanitize branch name for filesystem
	sanitized := branchName
	sanitized = strings.ReplaceAll(sanitized, "/", "-")
	sanitized = strings.ReplaceAll(sanitized, " ", "-")

	return repoDir + "-" + sanitized
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

	// For worktrees, common-dir points to the main repo's .git directory
	// We need to get the parent of that
	if strings.HasSuffix(commonDir, ".git") {
		return strings.TrimSuffix(commonDir, "/.git"), nil
	}

	// If already in main repo, just get toplevel
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
