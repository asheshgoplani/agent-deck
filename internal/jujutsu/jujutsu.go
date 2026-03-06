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

	"github.com/asheshgoplani/agent-deck/internal/git"
	"github.com/asheshgoplani/agent-deck/internal/vcs"
)

type JJBackend struct {
	repoDir string
}

// Compile-time check that *JJBackend satisfies vcs.Backend.
var _ vcs.Backend = (*JJBackend)(nil)

func NewJJBackend(dir string) (*JJBackend, error) {
	if !IsJJRepo(dir) {
		return nil, fmt.Errorf("not a jujutsu repository: %s", dir)
	}
	root, err := GetWorktreeBaseRoot(dir)
	if err != nil {
		return nil, err
	}
	return &JJBackend{root}, nil
}

func (g *JJBackend) Type() vcs.Type { return vcs.TypeJujutsu }

// RepoDir returns the root directory of the repository.
func (b *JJBackend) RepoDir() string { return b.repoDir }

// WorktreePath generates a workspace path using the backend's repoDir.
// Delegates to the shared template logic in the git package (VCS-agnostic).
func (b *JJBackend) WorktreePath(opts vcs.WorktreePathOptions) string {
	return git.WorktreePath(git.WorktreePathOptions{
		Branch:    opts.Branch,
		Location:  opts.Location,
		RepoDir:   b.repoDir,
		SessionID: opts.SessionID,
		Template:  opts.Template,
	})
}

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
func (b *JJBackend) GetCurrentBranch() (string, error) {
	cmd := exec.Command("jj", "log", "-r", "@", "--no-graph", "-T", "bookmarks", "-R", b.repoDir, "--ignore-working-copy")
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
func (b *JJBackend) BranchExists(branchName string) bool {
	cmd := exec.Command("jj", "bookmark", "list", "--name", branchName, "-R", b.repoDir, "--ignore-working-copy")
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

// ListWorktrees returns all workspaces for the repository.
func (b *JJBackend) ListWorktrees() ([]vcs.Worktree, error) {
	cmd := exec.Command("jj", "workspace", "list", "-R", b.repoDir, "-T", "name ++ ':' ++ target.commit_id() ++ \"\\n\"", "--ignore-working-copy")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list workspaces: %w", err)
	}
	return parseWorkspacesList(string(output))
}

func parseWorkspacesList(output string) ([]vcs.Worktree, error) {
	var workspaces []vcs.Worktree
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
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
		commitId := strings.TrimSpace(parts[1])
		workspaces = append(workspaces, vcs.Worktree{
			Branch: name,
			Commit: commitId,
		})
	}
	return workspaces, nil
}

// getWorkspacePath returns the filesystem path for a named workspace.
// It resolves the path from the repo root's .jj/working-copy stores.
func getWorkspacePath(repoDir, workspaceName string) (string, error) {
	if workspaceName == "default" {
		return GetRepoRoot(repoDir)
	}

	cmd := exec.Command("jj", "workspace", "root", "--name", workspaceName, "--ignore-working-copy")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get workspace path: %w", err)
	}
	return string(output), nil
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
func (b *JJBackend) CreateWorktree(workspacePath, branchName string) error {
	// Derive workspace name from the path
	wsName := workspaceNameFromPath(workspacePath)

	cmd := exec.Command("jj", "workspace", "add", "--name", wsName, workspacePath, "-R", b.repoDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create workspace: %s: %w", strings.TrimSpace(string(output)), err)
	}

	// Create/set bookmark on the new workspace's working copy
	if branchName != "" {
		if b.BranchExists(branchName) {
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

// RemoveWorktree forgets a workspace and optionally removes its directory.
func (b *JJBackend) RemoveWorktree(workspacePath string, force bool) error {
	wsName := workspaceNameFromPath(workspacePath)

	cmd := exec.Command("jj", "workspace", "forget", wsName, "-R", b.repoDir)
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

// PruneWorktrees removes workspace entries whose directories no longer exist.
func (b *JJBackend) PruneWorktrees() error {
	workspaces, err := b.ListWorktrees()
	if err != nil {
		return err
	}
	for _, ws := range workspaces {
		if ws.Path == "default" {
			continue
		}
		path, pathErr := getWorkspacePath(b.repoDir, ws.Branch)
		if pathErr != nil {
			continue
		}
		if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
			cmd := exec.Command("jj", "workspace", "forget", ws.Branch, "-R", b.repoDir)
			_ = cmd.Run()
		}
	}
	return nil
}

// HasUncommittedChanges checks if the working copy has uncommitted changes.
func (b *JJBackend) HasUncommittedChanges() (bool, error) {
	cmd := exec.Command("jj", "diff", "--stat", "-R", b.repoDir, "--ignore-working-copy")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("failed to check jj diff: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return strings.TrimSpace(string(output)) != "", nil
}

// GetDefaultBranch returns the default branch name (checks for main/master bookmarks).
func (b *JJBackend) GetDefaultBranch() (string, error) {
	if b.BranchExists("main") {
		return "main", nil
	}
	if b.BranchExists("master") {
		return "master", nil
	}
	return "", errors.New("could not determine default branch (no main or master bookmark)")
}

// MergeBranch creates a merge change combining the current change with the given bookmark.
func (b *JJBackend) MergeBranch(branchName string) error {
	cmd := exec.Command("jj", "new", "@", branchName, "-R", b.repoDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("merge failed: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

// DeleteBranch deletes a bookmark.
func (b *JJBackend) DeleteBranch(branchName string, force bool) error {
	cmd := exec.Command("jj", "bookmark", "delete", branchName, "-R", b.repoDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to delete bookmark: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

// CheckoutBranch moves the working copy to a new change based on the given bookmark.
func (b *JJBackend) CheckoutBranch(branchName string) error {
	cmd := exec.Command("jj", "new", branchName, "-R", b.repoDir)
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

func (b *JJBackend) GetWorktreeForBranch(branchName string) (string, error) {
	worktrees, err := b.ListWorktrees()
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
