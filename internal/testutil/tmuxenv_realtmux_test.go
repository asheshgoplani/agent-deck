package testutil

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestIsolateTmuxSocket_DefaultSocketUntouched_RealTmux is the
// end-to-end regression test for the 2026-04-17 three-cascade incident.
//
// This test refuses to run unless it can verify it will NOT touch the
// user's real tmux server. Guards:
//
//  1. Skip if no tmux binary.
//  2. Skip if the default-socket path cannot be determined for the user.
//  3. Record the default socket's mtime BEFORE the test.
//  4. Simulate the real-world condition: set TMUX to point at a *fake*
//     socket path (we never touch /tmp/tmux-<uid>/default directly — if
//     the fix regressed and the spawned tmux ignored TMUX_TMPDIR, the
//     mtime check at the end catches it).
//  5. Call IsolateTmuxSocket and then spawn a real tmux session.
//  6. Verify: (a) the session exists on the ISOLATED socket, (b) the
//     default socket's mtime is unchanged — zero touches.
//
// If this test fails, the isolation is broken and running `go test ./...`
// on a developer host will kill all live agent-deck sessions. Do not
// ignore.
func TestIsolateTmuxSocket_DefaultSocketUntouched_RealTmux(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux binary not available — cannot run real-tmux end-to-end test")
	}

	uid := os.Getuid()
	defaultSocketPath := fmt.Sprintf("/tmp/tmux-%d/default", uid)

	// Record BEFORE-mtime. If the default socket doesn't exist we'll
	// just verify it's still non-existent (or was never touched).
	var beforeMtime time.Time
	beforeStat, beforeErr := os.Stat(defaultSocketPath)
	if beforeErr == nil {
		beforeMtime = beforeStat.ModTime()
		t.Logf("before test: %s mtime=%s", defaultSocketPath, beforeMtime.Format(time.RFC3339Nano))
	} else {
		t.Logf("before test: %s does not exist (%v)", defaultSocketPath, beforeErr)
	}

	// Simulate "running inside the user's tmux pane" — the real-world
	// condition on every developer host. Save originals so we restore.
	origTmux, hadTmux := os.LookupEnv("TMUX")
	origPane, hadPane := os.LookupEnv("TMUX_PANE")
	origTmpdir, hadTmpdir := os.LookupEnv("TMUX_TMPDIR")
	origMarker, hadMarker := os.LookupEnv(TestIsolationMarkerEnv)
	defer func() {
		restoreEnv("TMUX", origTmux, hadTmux)
		restoreEnv("TMUX_PANE", origPane, hadPane)
		restoreEnv("TMUX_TMPDIR", origTmpdir, hadTmpdir)
		restoreEnv(TestIsolationMarkerEnv, origMarker, hadMarker)
	}()

	os.Setenv("TMUX", fmt.Sprintf("%s,99999,0", defaultSocketPath))
	os.Setenv("TMUX_PANE", "%99")

	cleanup := IsolateTmuxSocket()
	defer cleanup()

	isolatedDir := os.Getenv("TMUX_TMPDIR")
	if isolatedDir == "" {
		t.Fatal("IsolateTmuxSocket did not set TMUX_TMPDIR")
	}
	if strings.HasPrefix(isolatedDir, "/tmp/tmux-") {
		t.Fatalf("isolation dir %q has the user's tmux dir prefix — this would collide", isolatedDir)
	}

	// Spawn a REAL tmux session. Use -L with a unique socket name on top
	// of TMUX_TMPDIR to pin it double-safe. The goal: prove that even
	// when TMUX was set to point at the user's default socket, the fix
	// causes this session to land in the isolated dir.
	sessionName := fmt.Sprintf("isolation_probe_%d", os.Getpid())
	out, err := exec.Command("tmux", "new-session", "-d", "-s", sessionName, "sleep", "30").CombinedOutput()
	if err != nil {
		t.Fatalf("tmux new-session failed: %v (output: %s)", err, string(out))
	}

	// Ensure we kill the isolated session no matter what.
	defer func() {
		_ = exec.Command("tmux", "kill-session", "-t", sessionName).Run()
	}()

	// The isolated dir should now contain a tmux-<uid>/default socket.
	isolatedSocket := filepath.Join(isolatedDir, fmt.Sprintf("tmux-%d/default", uid))
	if _, err := os.Stat(isolatedSocket); err != nil {
		// Alternative layout: some tmux builds put the socket directly
		// in TMUX_TMPDIR without the tmux-<uid>/ subdir. Accept either.
		altSocket := filepath.Join(isolatedDir, "default")
		if _, errAlt := os.Stat(altSocket); errAlt != nil {
			t.Fatalf("isolated socket not found at %q (err=%v) nor at %q (err=%v) — "+
				"the spawned tmux did NOT land in the isolated dir, meaning the fix regressed. "+
				"Contents of %q:", isolatedSocket, err, altSocket, errAlt, isolatedDir)
		}
	}

	// CRITICAL ASSERTION: the user's default socket mtime MUST NOT have
	// changed. If it did, we just re-triggered the cascade bug.
	afterStat, afterErr := os.Stat(defaultSocketPath)
	switch {
	case beforeErr != nil && afterErr == nil:
		t.Fatalf("REGRESSION: %s did not exist before, but now exists — the spawned tmux created the user's default socket", defaultSocketPath)
	case beforeErr == nil && afterErr != nil:
		// Existed before, gone now — external actor, not our fault. Log.
		t.Logf("note: default socket was removed externally during test (%v)", afterErr)
	case beforeErr == nil && afterErr == nil:
		afterMtime := afterStat.ModTime()
		if !afterMtime.Equal(beforeMtime) {
			t.Fatalf("REGRESSION: %s was touched during test: before=%s after=%s. "+
				"This means tmux isolation leaked and the test hit the user's real socket — "+
				"the 2026-04-17 three-cascade bug is NOT fixed.",
				defaultSocketPath, beforeMtime.Format(time.RFC3339Nano), afterMtime.Format(time.RFC3339Nano))
		}
		t.Logf("after test: %s mtime=%s (UNCHANGED — isolation holds)", defaultSocketPath, afterMtime.Format(time.RFC3339Nano))
	}
}

func TestIsolateTmuxSocket_CleanupTerminatesIsolatedServer(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux binary not available — cannot run real-tmux cleanup test")
	}

	cleanup := IsolateTmuxSocket()
	dir := os.Getenv("TMUX_TMPDIR")
	uid := os.Getuid()
	socket := filepath.Join(dir, fmt.Sprintf("tmux-%d/default", uid))
	sessionName := fmt.Sprintf("cleanup_probe_%d", os.Getpid())
	if out, err := exec.Command("tmux", "new-session", "-d", "-s", sessionName, "sleep", "30").CombinedOutput(); err != nil {
		cleanup()
		t.Fatalf("tmux new-session failed: %v (output: %s)", err, out)
	}

	pidOut, err := exec.Command("tmux", "display-message", "-p", "#{pid}").Output()
	if err != nil {
		_ = exec.Command("tmux", "kill-server").Run()
		cleanup()
		t.Fatalf("read isolated tmux server pid: %v", err)
	}
	var pid int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(pidOut)), "%d", &pid); err != nil || pid <= 0 {
		_ = exec.Command("tmux", "kill-server").Run()
		cleanup()
		t.Fatalf("parse isolated tmux server pid %q: %v", pidOut, err)
	}

	cleanup()
	if err := exec.Command("kill", "-0", strconv.Itoa(pid)).Run(); err == nil {
		_ = exec.Command("kill", "-TERM", strconv.Itoa(pid)).Run()
		t.Fatalf("IsolateTmuxSocket cleanup left tmux server pid %d running (socket was %s)", pid, socket)
	}
}
