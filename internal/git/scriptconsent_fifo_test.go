//go:build !windows

package git

import (
	"bytes"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestGateAndRunWorktreeSetupScript_FIFO_RejectedWithoutHanging reproduces
// the exact vector named in the review finding: a committed
// .agent-deck/worktree-setup.sh that is (or, via a symlink, points at) a
// FIFO with no writer. Before the fix, hashScriptFile's unbounded io.Copy
// blocked on this forever, and it ran BEFORE checkScriptConsent ever
// consulted the policy — so even run_repo_scripts = "never" hung instead of
// denying instantly. Both policies must now return promptly with an error.
//
// Unix-only (syscall.Mkfifo has no Windows equivalent); CI for this repo
// runs macOS and Linux runners only.
func TestGateAndRunWorktreeSetupScript_FIFO_RejectedWithoutHanging(t *testing.T) {
	repoDir := t.TempDir()
	dir := filepath.Join(repoDir, ".agent-deck")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	fifoPath := filepath.Join(dir, "worktree-setup.sh")
	if err := syscall.Mkfifo(fifoPath, 0o644); err != nil {
		t.Skipf("mkfifo not supported on this platform/filesystem: %v", err)
	}

	for _, policy := range []ScriptConsentPolicy{ScriptConsentNever, ScriptConsentPrompt} {
		policy := policy
		t.Run(string(policy), func(t *testing.T) {
			resetScriptConsentForTest(t, ScriptConsentConfig{Policy: policy})

			done := make(chan error, 1)
			go func() {
				done <- GateAndRunWorktreeSetupScript(repoDir, repoDir, &bytes.Buffer{}, &bytes.Buffer{}, 0)
			}()

			select {
			case err := <-done:
				if err == nil {
					t.Fatal("expected a FIFO script file (no writer) to be rejected, got nil error")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("GateAndRunWorktreeSetupScript hung reading a FIFO script with no writer — " +
					"this is exactly the hang the policy short-circuit + IsRegular guard exist to prevent")
			}
		})
	}
}
