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

// RunWorktreeSetupScript executes the setup script with AGENT_DECK_REPO_ROOT
// and AGENT_DECK_WORKTREE_PATH environment variables set. Working directory
// is set to worktreePath. Output is streamed to the provided writers.
//
// Dispatch (#773):
//   - If scriptPath has the user-executable bit set, the script is invoked
//     directly so the kernel honors its shebang line (e.g. #!/usr/bin/env bash,
//     #!/usr/bin/env python3). This lets users write the setup script in any
//     language they like.
//   - Otherwise (legacy 0644 setups predating #773), fall back to `sh -e
//     <path>` so existing repos keep working without a chmod.
//
// Timeout semantics (post-#727 follow-up):
//   - timeout > 0  → bounded by context.WithTimeout
//   - timeout <= 0 → unlimited (context.Background, no deadline)
//
// The session layer resolves the legacy 60s default before calling here;
// callers that want bounded runs must pass a positive duration explicitly.
func RunWorktreeSetupScript(scriptPath, repoDir, worktreePath string, stdout, stderr io.Writer, timeout time.Duration) error {
	var ctx context.Context
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), timeout)
	} else {
		ctx, cancel = context.WithCancel(context.Background())
	}
	defer cancel()

	cmd := buildSetupCmd(ctx, scriptPath)
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

// buildSetupCmd picks how to invoke the setup script (#773). Executable
// scripts run directly so the kernel honors their shebang line; legacy
// non-executable scripts run via `sh -e <path>` for backwards compatibility.
func buildSetupCmd(ctx context.Context, scriptPath string) *exec.Cmd {
	if info, err := os.Stat(scriptPath); err == nil && info.Mode()&0o111 != 0 {
		return exec.CommandContext(ctx, scriptPath)
	}
	return exec.CommandContext(ctx, "sh", "-e", scriptPath)
}

// CreateWorktreeWithSetup creates a worktree and runs the setup script if present.
// Setup script failure is non-fatal: the worktree is still valid.
// Output is streamed to the provided writers. A non-positive setupTimeout
// means "no deadline" — see RunWorktreeSetupScript for the full semantic.
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
