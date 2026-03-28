package git

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// FindWorktreeSetupScript returns the path to the worktree setup script
// if one exists at <repoDir>/.agent-deck/worktree-setup.sh, or empty string.
func FindWorktreeSetupScript(repoDir string) string {
	p := filepath.Join(repoDir, ".agent-deck", "worktree-setup.sh")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

// worktreeSetupTimeout is the maximum time a setup script is allowed to run.
var worktreeSetupTimeout = 60 * time.Second

// RunWorktreeSetupScript executes the setup script with AGENT_DECK_REPO_ROOT
// and AGENT_DECK_WORKTREE_PATH environment variables set. Working directory
// is set to worktreePath. Output is streamed to the provided writers.
func RunWorktreeSetupScript(scriptPath, repoDir, worktreePath string, stdout, stderr io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), worktreeSetupTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-e", scriptPath)
	cmd.Dir = worktreePath
	cmd.Env = append(os.Environ(),
		"AGENT_DECK_REPO_ROOT="+repoDir,
		"AGENT_DECK_WORKTREE_PATH="+worktreePath,
	)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = 5 * time.Second

	err := cmd.Run()

	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("worktree setup script timed out after %s", worktreeSetupTimeout)
	}
	if err != nil {
		return fmt.Errorf("worktree setup script failed: %w", err)
	}
	return nil
}

// CreateWorktreeWithSetup creates a worktree and runs the setup script if present.
// Setup script failure is non-fatal: the worktree is still valid.
// Output is streamed to the provided writers.
func CreateWorktreeWithSetup(repoDir, worktreePath, branchName string, stdout, stderr io.Writer) (setupErr error, err error) {
	if err = CreateWorktree(repoDir, worktreePath, branchName); err != nil {
		return nil, err
	}

	scriptPath := FindWorktreeSetupScript(repoDir)
	if scriptPath == "" {
		return nil, nil
	}

	fmt.Fprintln(stderr, "Running worktree setup script...")
	setupErr = RunWorktreeSetupScript(scriptPath, repoDir, worktreePath, stdout, stderr)
	return setupErr, nil
}
