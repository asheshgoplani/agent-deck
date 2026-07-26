package testutil

// Regression coverage for the second root cause of the 2026-07-18 pty
// exhaustion: cleanup that unlinks a tmux socket dir without killing the
// servers bound to it.
//
// Unlinking a Unix socket does not signal its listener. The tmux server keeps
// running with every pane and every pty it owns, and is now unreachable by any
// socket path — no `kill-server`, no `list-sessions`, nothing can address it
// again. The only way out is `kill -9` on a pid you first have to find. ~50 of
// them accumulated this way, holding 507 of the host's 511 ptys, and every
// attach on the machine failed with "Device not configured".
//
// These tests spawn a real tmux server inside an isolated dir, run the
// cleanup, and require the server to be gone. On the pre-fix cleanup, which
// went straight to RemoveAll, the server survives.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func requireTmuxBinary(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux binary not on PATH; skipping")
	}
}

// tmuxPidOnSocket returns the server pid listening on `socket`, or 0.
func tmuxPidOnSocket(t *testing.T, socket string) int {
	t.Helper()
	cmd := exec.Command("tmux", "-S", socket, "display-message", "-p", "#{pid}")
	cmd.Env = envWithoutTmuxVars()
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0
	}
	return pid
}

// processAlive reports whether pid still exists. Signal 0 probes without
// delivering anything, which is what lets this distinguish "server detached
// from its socket but still holding ptys" from "server actually gone" — the
// distinction the pre-fix cleanup got wrong.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// TestIsolateTmuxSocket_CleanupKillsServersBeforeRemovingDir is the headline
// regression: a server spawned under the isolated TMUX_TMPDIR must be dead
// once cleanup returns, not merely unreachable.
func TestIsolateTmuxSocket_CleanupKillsServersBeforeRemovingDir(t *testing.T) {
	requireTmuxBinary(t)

	cleanup := IsolateTmuxSocket()
	dir := os.Getenv("TMUX_TMPDIR")
	if dir == "" {
		cleanup()
		t.Fatal("IsolateTmuxSocket did not set TMUX_TMPDIR")
	}

	// Spawn a server exactly the way an isolated test does: -L against the
	// isolated TMUX_TMPDIR.
	spawn := exec.Command("tmux", "-L", "leakprobe", "new-session", "-d", "-x", "80", "-y", "24", "-s", "probe", "sleep 600")
	spawn.Env = append(envWithoutTmuxVars(), "TMUX_TMPDIR="+dir)
	if out, err := spawn.CombinedOutput(); err != nil {
		cleanup()
		t.Skipf("could not spawn probe tmux server: %v: %s", err, out)
	}

	socket := filepath.Join(dir, tmuxUIDDir(t, dir), "leakprobe")
	pid := tmuxPidOnSocket(t, socket)
	if pid == 0 {
		cleanup()
		t.Fatalf("probe server did not come up on %s", socket)
	}

	cleanup()

	// Poll briefly: kill-server is asynchronous from our side.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && processAlive(pid) {
		time.Sleep(50 * time.Millisecond)
	}
	if processAlive(pid) {
		// Don't leave our own probe behind after failing over exactly this.
		if proc, err := os.FindProcess(pid); err == nil {
			_ = proc.Kill()
		}
		t.Fatalf("tmux server pid %d survived IsolateTmuxSocket cleanup.\n"+
			"Its socket dir was removed, so no socket path can reach it — it will hold "+
			"its panes' ptys until someone kills the pid by hand. Cleanup must "+
			"kill-server BEFORE RemoveAll (2026-07-18 pty exhaustion).", pid)
	}
}

// TestShortTmuxSocket_CleanupKillsServer pins the same rule for the `-S`
// helper, which had the identical RemoveAll-only cleanup.
func TestShortTmuxSocket_CleanupKillsServer(t *testing.T) {
	requireTmuxBinary(t)

	socket, cleanup := ShortTmuxSocket()

	spawn := exec.Command("tmux", "-S", socket, "new-session", "-d", "-x", "80", "-y", "24", "-s", "probe", "sleep 600")
	spawn.Env = envWithoutTmuxVars()
	if out, err := spawn.CombinedOutput(); err != nil {
		cleanup()
		t.Skipf("could not spawn probe tmux server: %v: %s", err, out)
	}

	pid := tmuxPidOnSocket(t, socket)
	if pid == 0 {
		cleanup()
		t.Fatalf("probe server did not come up on %s", socket)
	}

	cleanup()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && processAlive(pid) {
		time.Sleep(50 * time.Millisecond)
	}
	if processAlive(pid) {
		if proc, err := os.FindProcess(pid); err == nil {
			_ = proc.Kill()
		}
		t.Fatalf("tmux server pid %d survived ShortTmuxSocket cleanup — the socket was "+
			"unlinked out from under a live server (2026-07-18 pty exhaustion)", pid)
	}
}

// tmuxUIDDir finds the tmux-<uid> subdirectory tmux created under dir.
func tmuxUIDDir(t *testing.T, dir string) string {
	t.Helper()
	entries, err := filepath.Glob(filepath.Join(dir, "tmux-*"))
	if err != nil || len(entries) == 0 {
		t.Fatalf("no tmux-<uid> dir under %s", dir)
	}
	return filepath.Base(entries[0])
}
