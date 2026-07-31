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

// TestHashScriptFile_FIFO_RejectedWithoutHanging pins that hashScriptFile
// itself — not just the GateAndRun* wrappers — refuses to hang on a FIFO.
// This matters because TrustScript (used by `agent-deck worktree
// trust-scripts`, a CLI entry point with no consent-policy short-circuit or
// caller-side IsRegular guard in front of it at all) calls hashScriptFile
// directly, so the safety has to live inside hashScriptFile/
// openScriptFileForHashing to cover that path too.
func TestHashScriptFile_FIFO_RejectedWithoutHanging(t *testing.T) {
	repoDir := t.TempDir()
	fifoPath := filepath.Join(repoDir, "fifo")
	if err := syscall.Mkfifo(fifoPath, 0o644); err != nil {
		t.Skipf("mkfifo not supported on this platform/filesystem: %v", err)
	}

	done := make(chan struct {
		hash string
		err  error
	}, 1)
	go func() {
		hash, err := hashScriptFile(fifoPath)
		done <- struct {
			hash string
			err  error
		}{hash, err}
	}()

	select {
	case r := <-done:
		if r.err == nil {
			t.Fatalf("expected hashScriptFile to reject a FIFO, got hash=%q err=nil", r.hash)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("hashScriptFile hung opening/reading a FIFO with no writer")
	}
}

// TestTrustScript_FIFO_RejectedWithoutHanging exercises the actual CLI trust
// path end-to-end: TrustScript has no other guard in front of it (unlike
// GateAndRunWorktreeSetupScript, which checks scriptMode.IsRegular() before
// ever calling hashScriptFile), so this is the test that would have caught
// the gap a reviewer found in the first version of this fix.
func TestTrustScript_FIFO_RejectedWithoutHanging(t *testing.T) {
	repoDir := t.TempDir()
	fifoPath := filepath.Join(repoDir, "fifo")
	if err := syscall.Mkfifo(fifoPath, 0o644); err != nil {
		t.Skipf("mkfifo not supported on this platform/filesystem: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := TrustScript(repoDir, "setup", fifoPath)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected TrustScript to reject a FIFO, got nil error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("TrustScript hung opening/reading a FIFO with no writer")
	}
}
