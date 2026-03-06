// Package jujutsu provides jj (Jujutsu) VCS operations for agent-deck.
// It mirrors the internal/git package pattern with package-level functions
// that execute jj CLI commands.
package jujutsu

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// IsJJRepo checks if the given directory is inside a jj repository by running
// `jj root`. Returns false if jj is not installed or the directory is not a jj repo.
func IsJJRepo(dir string) bool {
	if _, err := exec.LookPath("jj"); err != nil {
		return false
	}
	cmd := exec.Command("jj", "root", "-R", dir, "--ignore-working-copy")
	return cmd.Run() == nil
}

// GetRepoRoot returns the root directory of the jj repository.
func GetRepoRoot(dir string) (string, error) {
	cmd := exec.Command("jj", "root", "-R", dir, "--ignore-working-copy")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("not a jj repository: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// GetCurrentBranch returns the first bookmark of the current working-copy change.
func GetCurrentBranch(dir string) (string, error) {
	cmd := exec.Command("jj", "log", "-r", "@", "--no-graph", "-T", "bookmarks", "-R", dir, "--ignore-working-copy")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get current bookmark: %w", err)
	}
	raw := strings.TrimSpace(string(output))
	if raw == "" {
		return "", nil
	}
	// jj may return multiple bookmarks separated by spaces; take the first
	parts := strings.Fields(raw)
	// jj appends a '*' to bookmarks that have local changes; strip it
	return strings.TrimRight(parts[0], "*"), nil
}

// BranchExists checks if a bookmark exists in the repository.
func BranchExists(repoDir, branchName string) bool {
	cmd := exec.Command("jj", "bookmark", "list", "--name", branchName, "-R", repoDir, "--ignore-working-copy")
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(output)) != ""
}

// Workspace represents a jj workspace parsed from `jj workspace list`.
type Workspace struct {
	Name string
	Path string
}

// ListWorkspaces returns all workspaces for the repository.
func ListWorkspaces(repoDir string) ([]Workspace, error) {
	cmd := exec.Command("jj", "workspace", "list", "-R", repoDir, "--ignore-working-copy")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list workspaces: %w", err)
	}
	return parseWorkspaceList(string(output)), nil
}

// parseWorkspaceList parses the output of `jj workspace list`.
// Format: "<name>: <change-id> <description>" or "<name>: <path>"
func parseWorkspaceList(output string) []Workspace {
	var workspaces []Workspace
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		// Format: "name: rest..."
		parts := strings.SplitN(line, ":", 2)
		if len(parts) < 2 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		workspaces = append(workspaces, Workspace{
			Name: name,
		})
	}
	return workspaces
}

// GetWorkspacePath returns the filesystem path for a named workspace.
// It resolves the path from the repo root's .jj/working-copy stores.
func GetWorkspacePath(repoDir, workspaceName string) (string, error) {
	if workspaceName == "default" {
		return GetRepoRoot(repoDir)
	}
	// jj stores workspace paths in .jj/working-copy/<name>/working-copy.
	// The most reliable way is to use `jj workspace root` if available,
	// or infer from the workspace add convention.
	// For now, we use `jj root -R` from within the workspace.
	// Since workspace paths are not directly exposed via CLI, we check
	// the .jj directory for workspace path entries.
	root, err := GetRepoRoot(repoDir)
	if err != nil {
		return "", err
	}

	// Check common workspace location patterns
	// jj workspace add creates workspaces at the specified path
	// We store the path when creating, so for discovery we need the store
	storePath := filepath.Join(root, ".jj", "repo", "working_copies", workspaceName)
	if info, err := os.Stat(storePath); err == nil && info.IsDir() {
		// Read the working copy path from the store
		data, err := os.ReadFile(filepath.Join(storePath, "working_copy_path"))
		if err == nil {
			p := strings.TrimSpace(string(data))
			if filepath.IsAbs(p) {
				return p, nil
			}
			return filepath.Join(root, p), nil
		}
	}

	return "", fmt.Errorf("could not determine path for workspace %q", workspaceName)
}

// IsDefaultWorkspace returns true if the given directory is the default workspace.
func IsDefaultWorkspace(dir string) (bool, error) {
	root, err := GetRepoRoot(dir)
	if err != nil {
		return false, err
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return false, err
	}
	return absDir == root, nil
}

// IsWorktree checks if the directory is a non-default jj workspace.
func IsWorktree(dir string) bool {
	isDefault, err := IsDefaultWorkspace(dir)
	if err != nil {
		return false
	}
	return !isDefault
}

// GetWorktreeBaseRoot returns the default workspace path (equivalent to main worktree in git).
func GetWorktreeBaseRoot(dir string) (string, error) {
	return GetRepoRoot(dir)
}

// CreateWorkspace creates a new jj workspace at the given path.
func CreateWorkspace(repoDir, workspacePath, branchName string) error {
	// Derive workspace name from the path
	wsName := workspaceNameFromPath(workspacePath)

	cmd := exec.Command("jj", "workspace", "add", "--name", wsName, workspacePath, "-R", repoDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create workspace: %s: %w", strings.TrimSpace(string(output)), err)
	}

	// Create/set bookmark on the new workspace's working copy
	if branchName != "" {
		if BranchExists(repoDir, branchName) {
			// Set existing bookmark to point to the new workspace's working copy
			cmd = exec.Command("jj", "bookmark", "set", branchName, "-r", "@", "-R", workspacePath)
		} else {
			// Create new bookmark
			cmd = exec.Command("jj", "bookmark", "create", branchName, "-r", "@", "-R", workspacePath)
		}
		output, err = cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("failed to set bookmark: %s: %w", strings.TrimSpace(string(output)), err)
		}
	}

	return nil
}

// RemoveWorkspace forgets a workspace and optionally removes its directory.
func RemoveWorkspace(repoDir, workspacePath string, force bool) error {
	wsName := workspaceNameFromPath(workspacePath)

	cmd := exec.Command("jj", "workspace", "forget", wsName, "-R", repoDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to forget workspace: %s: %w", strings.TrimSpace(string(output)), err)
	}

	// jj workspace forget doesn't remove the directory, so we do it ourselves
	if err := os.RemoveAll(workspacePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove workspace directory: %w", err)
	}

	return nil
}

// PruneWorkspaces removes workspace entries whose directories no longer exist.
func PruneWorkspaces(repoDir string) error {
	workspaces, err := ListWorkspaces(repoDir)
	if err != nil {
		return err
	}
	for _, ws := range workspaces {
		if ws.Name == "default" {
			continue
		}
		path, pathErr := GetWorkspacePath(repoDir, ws.Name)
		if pathErr != nil {
			continue
		}
		if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
			cmd := exec.Command("jj", "workspace", "forget", ws.Name, "-R", repoDir)
			_ = cmd.Run()
		}
	}
	return nil
}

// HasUncommittedChanges checks if the working copy has uncommitted changes.
func HasUncommittedChanges(dir string) (bool, error) {
	cmd := exec.Command("jj", "diff", "--stat", "-R", dir, "--ignore-working-copy")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("failed to check jj diff: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return strings.TrimSpace(string(output)) != "", nil
}

// GetDefaultBranch returns the default branch name (checks for main/master bookmarks).
func GetDefaultBranch(repoDir string) (string, error) {
	if BranchExists(repoDir, "main") {
		return "main", nil
	}
	if BranchExists(repoDir, "master") {
		return "master", nil
	}
	return "", errors.New("could not determine default branch (no main or master bookmark)")
}

// MergeBranch creates a merge change combining the current change with the given bookmark.
func MergeBranch(repoDir, branchName string) error {
	cmd := exec.Command("jj", "new", "@", branchName, "-R", repoDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("merge failed: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

// DeleteBranch deletes a bookmark.
func DeleteBranch(repoDir, branchName string, force bool) error {
	cmd := exec.Command("jj", "bookmark", "delete", branchName, "-R", repoDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to delete bookmark: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

// CheckoutBranch moves the working copy to a new change based on the given bookmark.
func CheckoutBranch(repoDir, branchName string) error {
	cmd := exec.Command("jj", "new", branchName, "-R", repoDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to checkout %s: %s: %w", branchName, strings.TrimSpace(string(output)), err)
	}
	return nil
}

// AbortMerge undoes the last operation (equivalent to aborting a merge).
func AbortMerge(repoDir string) error {
	cmd := exec.Command("jj", "undo", "-R", repoDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to undo: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

// GetMainWorktreePath returns the path to the default workspace.
func GetMainWorktreePath(dir string) (string, error) {
	return GetRepoRoot(dir)
}

// workspaceNameFromPath generates a workspace name from a filesystem path.
func workspaceNameFromPath(path string) string {
	name := filepath.Base(path)
	// Replace characters that might be problematic in workspace names
	name = strings.ReplaceAll(name, " ", "-")
	return name
}
