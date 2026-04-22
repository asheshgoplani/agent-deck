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

// DefaultWorktreeSetupTimeout is the fallback used when callers pass a
// non-positive timeout. Kept in sync with session.DefaultWorktreeSetupTimeout
// — duplicated here to avoid a session → git import cycle.
const DefaultWorktreeSetupTimeout = 60 * time.Second

// RunWorktreeSetupScript executes the setup script with AGENT_DECK_REPO_ROOT
// and AGENT_DECK_WORKTREE_PATH environment variables set. Working directory
// is set to worktreePath. Output is streamed to the provided writers. A
// non-positive timeout falls back to DefaultWorktreeSetupTimeout so the
// legacy 60s behaviour holds for any caller that has not adopted the new
// [worktree].setup_timeout_seconds config knob (GH #724).
func RunWorktreeSetupScript(scriptPath, repoDir, worktreePath string, stdout, stderr io.Writer, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = DefaultWorktreeSetupTimeout
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
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
		return fmt.Errorf("worktree setup script timed out after %s", timeout)
	}
	if err != nil {
		return fmt.Errorf("worktree setup script failed: %w", err)
	}
	return nil
}

// CreateWorktreeWithSetup creates a worktree and runs the setup script if present.
// Setup script failure is non-fatal: the worktree is still valid.
// Output is streamed to the provided writers. A non-positive setupTimeout
// falls back to DefaultWorktreeSetupTimeout.
func CreateWorktreeWithSetup(repoDir, worktreePath, branchName string, stdout, stderr io.Writer, setupTimeout time.Duration) (setupErr error, err error) {
	if err = CreateWorktree(repoDir, worktreePath, branchName); err != nil {
		return nil, err
	}

	scriptPath := FindWorktreeSetupScript(repoDir)
	if scriptPath == "" {
		return nil, nil
	}

	fmt.Fprintln(stderr, "Running worktree setup script...")
	setupErr = RunWorktreeSetupScript(scriptPath, repoDir, worktreePath, stdout, stderr, setupTimeout)
	return setupErr, nil
}
