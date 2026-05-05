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
// The returned os.FileMode captures the file's permission bits at discovery
// time, eliminating a TOCTOU race between finding and dispatching the script.
func FindWorktreeSetupScript(repoDir string) (string, os.FileMode) {
	p := filepath.Join(repoDir, ".agent-deck", "worktree-setup.sh")
	if info, err := os.Stat(p); err == nil {
		return p, info.Mode()
	}
	return "", 0
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
func RunWorktreeSetupScript(scriptPath string, scriptMode os.FileMode, repoDir, worktreePath string, stdout, stderr io.Writer, timeout time.Duration) error {
	var ctx context.Context
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), timeout)
	} else {
		ctx, cancel = context.WithCancel(context.Background())
	}
	defer cancel()

	cmd := buildSetupCmd(ctx, scriptPath, scriptMode)
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
//
// The mode is passed from the caller (captured at discovery time) to avoid
// a redundant os.Stat that is vulnerable to TOCTOU races — e.g. when
// a concurrent `git rebase` on the main worktree momentarily removes the
// file between FindWorktreeSetupScript and execution.
func buildSetupCmd(ctx context.Context, scriptPath string, mode os.FileMode) *exec.Cmd {
	if mode&0o111 != 0 {
		return exec.CommandContext(ctx, scriptPath)
	}
	return exec.CommandContext(ctx, "sh", "-e", scriptPath)
}

// CreateWorktreeWithSetup creates a worktree and runs the setup script if present.
// Setup script failure is non-fatal: the worktree is still valid.
// Output is streamed to the provided writers. A non-positive setupTimeout
// means "no deadline" — see RunWorktreeSetupScript for the full semantic.
//
// User-visible progress (#768): the start preamble, an explicit completion
// line on success, and an explicit failure line on error are written to
// stderr so callers (CLI streaming directly, TUI capturing into a buffer
// for later display) can show the user what happened. Without these,
// users couldn't tell whether the script had run, finished, or finished
// cleanly before claude started.
func CreateWorktreeWithSetup(repoDir, worktreePath, branchName string, stdout, stderr io.Writer, setupTimeout time.Duration) (setupErr error, err error) {
	if err = CreateWorktree(repoDir, worktreePath, branchName); err != nil {
		return nil, err
	}

	scriptPath, scriptMode := FindWorktreeSetupScript(repoDir)
	if scriptPath == "" {
		return nil, nil
	}

	fmt.Fprintln(stderr, "Running worktree setup script...")
	start := time.Now()
	setupErr = RunWorktreeSetupScript(scriptPath, scriptMode, repoDir, worktreePath, stdout, stderr, setupTimeout)
	elapsed := time.Since(start).Round(100 * time.Millisecond)
	if setupErr != nil {
		fmt.Fprintf(stderr, "Worktree setup script failed after %s: %v\n", elapsed, setupErr)
	} else {
		fmt.Fprintf(stderr, "Worktree setup script completed in %s\n", elapsed)
	}
	return setupErr, nil
}
