package ui

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestOrphanWatchdog_ForceExitsHungProcess pins the TestMain hard watchdog
// (armOrphanWatchdog in testmain_test.go).
//
// Incident it guards (2026-06-21): seven orphaned `ui.test` binaries — each
// reparented to PID 1 after their `go test` parent was interrupted — pinned a
// CPU core at ~100% for over two days and overheated the machine. They
// outlived their own -test.timeout because this package leaks hundreds of
// background workers (statusWorker / logWorker / StorageWatcher.pollLoop,
// started eagerly in NewHomeWithProfileAndMode and never torn down by most
// tests). When the soft timeout finally fires it panics and dumps every
// goroutine stack under a stop-the-world; a heap of busy leaked workers can
// wedge that STW so the panic never completes and the process spins forever.
//
// os.Exit performs no stop-the-world, so an independent os.Exit-based deadline
// is the only reliable backstop. This test re-execs the test binary in a child
// mode that hangs well past an env-shortened hard deadline and asserts the
// watchdog force-exits it (code 2) promptly, rather than letting it spin.
func TestOrphanWatchdog_ForceExitsHungProcess(t *testing.T) {
	if os.Getenv("AD_ORPHAN_WATCHDOG_CHILD") == "1" {
		// Simulate a wedged test: block far longer than the hard deadline.
		// The watchdog must os.Exit(2) before this returns.
		time.Sleep(30 * time.Second)
		return
	}

	// Bound the parent's wait so a regressed watchdog fails fast and never
	// leaves the child spinning (the exact failure mode under test).
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, os.Args[0],
		"-test.run=^TestOrphanWatchdog_ForceExitsHungProcess$", "-test.v")
	cmd.Env = append(os.Environ(),
		"AD_ORPHAN_WATCHDOG_CHILD=1",
		// Shorten the hard deadline so the test runs in seconds, not minutes.
		"AGENTDECK_TEST_HARD_TIMEOUT=2s",
	)

	start := time.Now()
	out, err := cmd.CombinedOutput()
	elapsed := time.Since(start)

	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("orphan watchdog did NOT fire: child still running after %s "+
			"(this is the overheat bug)\nchild output:\n%s", elapsed, out)
	}

	ee, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("child exited without an error code; want watchdog os.Exit(2). "+
			"err=%v elapsed=%s\nchild output:\n%s", err, elapsed, out)
	}
	if ee.ExitCode() != 2 {
		t.Fatalf("watchdog exit code = %d, want 2 (elapsed=%s)\nchild output:\n%s",
			ee.ExitCode(), elapsed, out)
	}
	// Sanity: it should fire near the 2s deadline, not at the 15s ctx bound.
	if elapsed > 10*time.Second {
		t.Fatalf("watchdog fired too late: %s (deadline was ~2s)\nchild output:\n%s",
			elapsed, out)
	}
}
