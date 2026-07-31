package git

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	return runWorktreeScript("setup", scriptPath, scriptMode, repoDir, worktreePath, stdout, stderr, timeout)
}

// runWorktreeScript runs a worktree lifecycle hook (kind is "setup" or
// "destruction") with the same env, working directory, shebang dispatch and
// timeout semantics. Shared so the destruction hook (#worktree-destruction)
// stays identical to setup without duplicating the runner.
func runWorktreeScript(kind, scriptPath string, scriptMode os.FileMode, repoDir, worktreePath string, stdout, stderr io.Writer, timeout time.Duration) error {
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
		return fmt.Errorf("worktree %s script timed out after %s", kind, timeout)
	}
	if err != nil {
		return fmt.Errorf("worktree %s script failed: %w", kind, err)
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
	return CreateWorktreeWithStateAndSetup(repoDir, worktreePath, branchName, WorktreeStateOptions{}, stdout, stderr, setupTimeout)
}

// WorktreeStateOptions controls the issue #1029 with-state behavior of
// CreateWorktreeWithStateAndSetup. When WithState is false, the worktree is
// created clean from branch tip — the legacy behavior.
type WorktreeStateOptions struct {
	// WithState copies parent's staged/unstaged/untracked files into the
	// new worktree before the setup hook runs.
	WithState bool
	// WithIgnored, when WithState is true, also copies parent's gitignored
	// files (e.g., .env, .mcp.json). Implies WithState.
	WithIgnored bool
}

// CreateWorktreeWithStateAndSetup is CreateWorktreeWithSetup plus optional
// materialization of the parent session's working-tree state (#1029).
// Materialization happens BEFORE worktreeinclude processing and the setup
// script so both observe the realized state, per @smorin's spec.
func CreateWorktreeWithStateAndSetup(repoDir, worktreePath, branchName string, state WorktreeStateOptions, stdout, stderr io.Writer, setupTimeout time.Duration) (setupErr error, err error) {
	return CreateWorktreeWithSetupOptions(repoDir, worktreePath, branchName, state, WorktreeCreateOptions{}, stdout, stderr, setupTimeout)
}

// CreateWorktreeWithSetupOptions is CreateWorktreeWithStateAndSetup plus
// creation-time options (#1708). Sparse inheritance happens inside worktree
// creation, so parent-state materialization, .worktreeinclude, and the setup
// script still run afterwards in exactly this order.
func CreateWorktreeWithSetupOptions(repoDir, worktreePath, branchName string, state WorktreeStateOptions, create WorktreeCreateOptions, stdout, stderr io.Writer, setupTimeout time.Duration) (setupErr error, err error) {
	createdBranch := !BranchExists(repoDir, branchName)
	if err = CreateWorktreeWithOptions(repoDir, worktreePath, branchName, create); err != nil {
		return nil, err
	}

	if state.WithState {
		if matErr := MaterializeWipFromParent(repoDir, worktreePath, state.WithIgnored); matErr != nil {
			var cleanupErrs []string
			if rmErr := RemoveWorktree(repoDir, worktreePath, true); rmErr != nil {
				cleanupErrs = append(cleanupErrs, fmt.Sprintf("worktree remove failed: %v", rmErr))
			}
			if createdBranch {
				if brErr := DeleteBranch(resolveGitInvocationDir(repoDir), branchName, true); brErr != nil {
					cleanupErrs = append(cleanupErrs, fmt.Sprintf("branch delete failed: %v", brErr))
				}
			}
			if len(cleanupErrs) > 0 {
				return nil, fmt.Errorf("materialize parent state: %w; cleanup failed: %s", matErr, strings.Join(cleanupErrs, "; "))
			}
			return nil, fmt.Errorf("materialize parent state: %w", matErr)
		}
	}

	if inclErr := ProcessWorktreeInclude(repoDir, worktreePath, stderr); inclErr != nil {
		fmt.Fprintf(stderr, "worktreeinclude: %v\n", inclErr)
	}

	return RunWorktreeSetupAfterCreate(repoDir, worktreePath, stdout, stderr, setupTimeout), nil
}

// RunWorktreeSetupAfterCreate runs the worktree setup script for an
// already-created worktree. Extracted from CreateWorktreeWithStateAndSetup
// so the fork-with-state path can sequence Create → Materialize → Setup
// with per-step error handling. Returns the script's exit error; nil if no
// script. Caller is responsible for ProcessWorktreeInclude if desired.
//
// Every caller (CreateWorktreeWithSetupOptions and its callers in
// cmd/session_cmd.go, vcs_helper.go, vcsbackend.go, multi_repo_worktree.go)
// funnels through here, so the consent gate (GateAndRunWorktreeSetupScript)
// applies uniformly without needing a change at each of those call sites.
func RunWorktreeSetupAfterCreate(repoDir, worktreePath string, stdout, stderr io.Writer, setupTimeout time.Duration) error {
	scriptPath, _ := FindWorktreeSetupScript(repoDir)
	if scriptPath == "" {
		return nil
	}
	fmt.Fprintln(stderr, "Running worktree setup script...")
	start := time.Now()
	setupErr := GateAndRunWorktreeSetupScript(repoDir, worktreePath, stdout, stderr, setupTimeout)
	elapsed := time.Since(start).Round(100 * time.Millisecond)
	if setupErr != nil {
		fmt.Fprintf(stderr, "Worktree setup script failed after %s: %v\n", elapsed, setupErr)
	} else {
		fmt.Fprintf(stderr, "Worktree setup script completed in %s\n", elapsed)
	}
	return setupErr
}

// GateAndRunWorktreeSetupScript finds, content-hashes, consent-gates, and —
// only if permitted — runs .agent-deck/worktree-setup.sh. This is the single
// entry point every caller (TUI, CLI, session layer) should use instead of
// pairing FindWorktreeSetupScript with RunWorktreeSetupScript directly,
// so the trust decision can never be bypassed by a new call site forgetting
// the gate. Returns nil if no script exists. See scriptconsent.go.
func GateAndRunWorktreeSetupScript(repoDir, worktreePath string, stdout, stderr io.Writer, timeout time.Duration) error {
	scriptPath, scriptMode := FindWorktreeSetupScript(repoDir)
	if scriptPath == "" {
		return nil
	}
	if handled, err := scriptConsentPolicyShortCircuit("setup", scriptPath); handled {
		if err != nil {
			return err
		}
		return RunWorktreeSetupScript(scriptPath, scriptMode, repoDir, worktreePath, stdout, stderr, timeout)
	}
	if !scriptMode.IsRegular() {
		return fmt.Errorf("worktree setup script consent: %s is not a regular file (refusing to hash a symlink/FIFO/device target, which could hang indefinitely); point .agent-deck/worktree-setup.sh at a real file", scriptPath)
	}
	hash, err := hashScriptFile(scriptPath)
	if err != nil {
		return fmt.Errorf("worktree setup script: reading %s for consent check: %w", scriptPath, err)
	}
	if err := checkScriptConsent("setup", repoDir, scriptPath, hash, stderr); err != nil {
		return err
	}
	return RunWorktreeSetupScript(scriptPath, scriptMode, repoDir, worktreePath, stdout, stderr, timeout)
}

// GateAndRunWorktreeDestructionScript mirrors GateAndRunWorktreeSetupScript
// for .agent-deck/worktree-destruction.sh.
func GateAndRunWorktreeDestructionScript(repoDir, worktreePath string, stdout, stderr io.Writer, timeout time.Duration) error {
	scriptPath, scriptMode := FindWorktreeDestructionScript(repoDir)
	if scriptPath == "" {
		return nil
	}
	if handled, err := scriptConsentPolicyShortCircuit("destruction", scriptPath); handled {
		if err != nil {
			return err
		}
		return RunWorktreeDestructionScript(scriptPath, scriptMode, repoDir, worktreePath, stdout, stderr, timeout)
	}
	if !scriptMode.IsRegular() {
		return fmt.Errorf("worktree destruction script consent: %s is not a regular file (refusing to hash a symlink/FIFO/device target, which could hang indefinitely); point .agent-deck/worktree-destruction.sh at a real file", scriptPath)
	}
	hash, err := hashScriptFile(scriptPath)
	if err != nil {
		return fmt.Errorf("worktree destruction script: reading %s for consent check: %w", scriptPath, err)
	}
	if err := checkScriptConsent("destruction", repoDir, scriptPath, hash, stderr); err != nil {
		return err
	}
	return RunWorktreeDestructionScript(scriptPath, scriptMode, repoDir, worktreePath, stdout, stderr, timeout)
}

// DefaultWorktreeDestructionTimeout bounds how long
// .agent-deck/worktree-destruction.sh may run. The setup hook resolves its
// timeout at the session layer, but RemoveWorktree (package git) cannot import
// session without an import cycle, so the destruction hook uses a fixed default.
// ponytail: fixed 60s, add a [worktree].destruction_timeout_seconds config if a
// user ever needs to tune it.
const DefaultWorktreeDestructionTimeout = 60 * time.Second

// FindWorktreeDestructionScript mirrors FindWorktreeSetupScript for the
// pre-removal hook at <repoDir>/.agent-deck/worktree-destruction.sh.
func FindWorktreeDestructionScript(repoDir string) (string, os.FileMode) {
	p := filepath.Join(repoDir, ".agent-deck", "worktree-destruction.sh")
	if info, err := os.Stat(p); err == nil {
		return p, info.Mode()
	}
	return "", 0
}

// RunWorktreeDestructionScript executes the destruction script with the same
// environment, working directory (worktreePath, which still exists at call
// time), shebang dispatch and timeout semantics as the setup script.
func RunWorktreeDestructionScript(scriptPath string, scriptMode os.FileMode, repoDir, worktreePath string, stdout, stderr io.Writer, timeout time.Duration) error {
	return runWorktreeScript("destruction", scriptPath, scriptMode, repoDir, worktreePath, stdout, stderr, timeout)
}

// RunWorktreeDestructionBeforeRemove runs the destruction script (if present)
// just before a worktree is removed. Failure is non-fatal: removal proceeds
// regardless, mirroring setup's "hook failure doesn't block the operation".
//
// This is the ONLY caller of the destruction script, and RemoveWorktree
// (git.go) is the ONLY caller of this function — so gating here, rather
// than at each of RemoveWorktree's ten call sites (cmd/session_cmd.go,
// cmd/worktree_cmd.go, cmd/main.go, internal/ui/home.go, web_mutator.go,
// vcsbackend.go, session/worktree_removal.go, plus RemoveWorktree's own
// rollback call in this file), closes all of them at once, including the
// remote-triggered path in web_mutator.go which has no terminal to prompt
// on and must fail closed rather than silently execute.
func RunWorktreeDestructionBeforeRemove(repoDir, worktreePath string, stdout, stderr io.Writer, timeout time.Duration) error {
	scriptPath, _ := FindWorktreeDestructionScript(repoDir)
	if scriptPath == "" {
		return nil
	}
	fmt.Fprintln(stderr, "Running worktree destruction script...")
	start := time.Now()
	err := GateAndRunWorktreeDestructionScript(repoDir, worktreePath, stdout, stderr, timeout)
	elapsed := time.Since(start).Round(100 * time.Millisecond)
	if err != nil {
		fmt.Fprintf(stderr, "Worktree destruction script failed after %s: %v\n", elapsed, err)
	} else {
		fmt.Fprintf(stderr, "Worktree destruction script completed in %s\n", elapsed)
	}
	return err
}
