package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestUnrecognizedCommandNonTTY_RefusesInsteadOfHanging is walk defect #4's
// regression test. `remote g14 health` (SSH-forwarded, no pty) hung
// indefinitely: g14 runs v1.16.10, which predates the `health` subcommand,
// so args[0]=="health" matched no case in main()'s dispatch switch and fell
// through into the alt-screen bubbletea TUI boot, which then blocked on a
// synchronous read that a non-interactive SSH exec can never satisfy.
//
// This branch already recognizes "health", so the same failure mode is
// reproduced here with a still-unrecognized command word standing in for
// "any subcommand the other side doesn't have yet" — the general case the
// walk's rule asks to guard, not just this one command. A bounded context
// means a regression here fails this test at the timeout instead of hanging
// the whole `go test` run.
func TestUnrecognizedCommandNonTTY_RefusesInsteadOfHanging(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	bin := channelsCLIBinary(t)
	home := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "totally-unrecognized-subcommand")
	cmd.Env = append(os.Environ(), "HOME="+home, "AGENTDECK_PROFILE=nontty_fallback_test")
	// Deliberately no pty and no TMUX*/os.Stdin wiring: stdout/stderr are
	// plain pipes, exactly like a forwarded SSH exec.
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	err := cmd.Run()
	elapsed := time.Since(start)

	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("hung: process had to be killed after %s (this is exactly walk defect #4's hang); stdout=%q stderr=%q", elapsed, stdout.String(), stderr.String())
	}
	if elapsed > 2*time.Second {
		t.Errorf("took %s to refuse; want a fast, plain-text refusal, not a near-timeout", elapsed)
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 2 {
		t.Fatalf("exit = %v (stdout=%q stderr=%q), want exit code 2", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "is not a recognized command") || !strings.Contains(stderr.String(), "not a terminal") {
		t.Fatalf("stderr = %q, want a clear non-terminal refusal", stderr.String())
	}
}
