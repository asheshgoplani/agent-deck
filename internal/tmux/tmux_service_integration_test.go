// Integration tests for the service-mode systemd invocation shape
// produced by startCommandSpec (v1.7.21). These tests validate the REAL
// systemd mechanism: spawn tmux as a transient service with the exact
// properties startCommandSpec emits, then exercise Restart=on-failure
// (kill -9) and clean-stop semantics.
//
// These tests deliberately do NOT go through Session.Start to avoid the
// default-tmux-socket sharing issue: production sessions share one
// daemon on the user's default socket and let the FIRST scope/service
// wrap anchor the daemon's cgroup; in tests we can't rely on there
// being no pre-existing server, so we use `-L <uniqueName>` to create
// a fresh isolated server per test. The unit tests in
// tmux_launch_as_test.go pin the argv shape separately — these tests
// ensure that argv shape actually delivers the advertised semantics on
// a real systemd host.
//
// These tests require systemd-user + systemctl and skip on any other
// platform (macOS, non-systemd Linux).
//
// Safety: every transient unit name gets a random 8-char suffix; every
// tmux server gets a unique -L name. Cleanup runs `systemctl --user
// stop` + `reset-failed` + `tmux -L NAME kill-server`, so a failed
// test cannot leak units or daemons.
package tmux

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func requireSystemdUserRun(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("systemd-run"); err != nil {
		t.Skipf("systemd-run not available: %v", err)
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		t.Skipf("systemctl not available: %v", err)
	}
	if err := exec.Command("systemd-run", "--user", "--version").Run(); err != nil {
		t.Skipf("systemd-run --user not operational: %v", err)
	}
	if err := exec.Command("systemctl", "--user", "is-active", "default.target").Run(); err != nil {
		// User manager not active — common on CI. Skip.
		t.Skipf("systemd --user manager not active: %v", err)
	}
}

// spawnServiceModeTmux executes the exact systemd-run invocation shape
// produced by startCommandSpec's service branch, but inserts -L so the
// spawned tmux creates a fresh isolated server instead of attaching to
// any pre-existing default-socket daemon. Returns the unit name so
// tests can poll systemctl show / issue systemctl stop on it.
func spawnServiceModeTmux(t *testing.T, tmuxLName, tmuxSessName string) (unit string, err error) {
	t.Helper()
	unit = "agentdeck-tmux-v1721svcit-" + randomServerSuffix(t) + ".service"
	args := []string{
		"--user", "--unit", unit, "--quiet",
		"--property=Type=forking",
		"--property=Restart=on-failure",
		"--property=RestartSec=2s", // shorter than production 5s for test speed
		"--property=StartLimitBurst=10",
		"--property=StartLimitIntervalSec=60",
		"--property=KillMode=control-group",
		"--property=TimeoutStopSec=15s",
		// -L creates a test-isolated tmux socket so no default-socket server
		// is contacted; this is the single necessary deviation from
		// startCommandSpec's production argv for tests to be reliable.
		"tmux", "-L", tmuxLName,
		"new-session", "-d", "-s", tmuxSessName, "-c", "/tmp",
		"bash", "-c", "exec sleep 600",
	}
	out, err := exec.Command("systemd-run", args...).CombinedOutput()
	if err != nil {
		return unit, fmt.Errorf("systemd-run: %w (output: %s)", err, string(out))
	}
	return unit, nil
}

// TestTmuxService_RestartsOnUnexpectedKill: with the argv shape
// startCommandSpec emits, a SIGKILL on the tmux daemon must trigger
// systemd's Restart=on-failure within ~10s. This is the headline
// v1.7.21 guarantee — if this test fails, the service-mode argv has a
// property bug.
func TestTmuxService_RestartsOnUnexpectedKill(t *testing.T) {
	requireSystemdUserRun(t)
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skipf("tmux not available: %v", err)
	}

	tmuxLName := "v1721-svcit-" + randomServerSuffix(t)
	tmuxSessName := "svcrestart"
	unit, err := spawnServiceModeTmux(t, tmuxLName, tmuxSessName)
	require.NoError(t, err, "service spawn must succeed on a systemd-user host")

	t.Cleanup(func() {
		_ = exec.Command("systemctl", "--user", "stop", unit).Run()
		_ = exec.Command("systemctl", "--user", "reset-failed", unit).Run()
		_ = exec.Command("tmux", "-L", tmuxLName, "kill-server").Run()
	})

	initialPID := waitForServicePID(t, unit, 5*time.Second)
	require.NotZero(t, initialPID, "tmux daemon PID must be visible via systemctl show within 5s (unit=%s)", unit)

	// SIGKILL — simulates OOM, kernel signal, or bug-induced crash.
	require.NoError(t, syscall.Kill(initialPID, syscall.SIGKILL),
		"kill -9 must succeed to meaningfully exercise Restart=on-failure")

	// Poll for restart. RestartSec=2s + Type=forking fork detection ~ a few
	// hundred ms → the new PID should appear within ~8s on a sane host.
	deadline := time.Now().Add(15 * time.Second)
	var newPID int
	var restarts int
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		newPID = readServicePID(unit)
		restarts = readServiceNRestarts(unit)
		if newPID != 0 && newPID != initialPID && restarts >= 1 {
			break
		}
	}

	require.NotZero(t, newPID,
		"expected a new tmux daemon PID after kill -9 + 15s wait, got 0 — Restart=on-failure did not fire")
	require.NotEqual(t, initialPID, newPID,
		"expected new tmux PID to differ from killed one; Restart did not actually respawn")
	require.GreaterOrEqual(t, restarts, 1,
		"NRestarts must be >=1 after SIGKILL; got %d", restarts)
}

// TestTmuxService_ExplicitStopDoesNotTriggerRestart: `systemctl --user
// stop` on the service unit must be CLEAN — Restart=on-failure must NOT
// fire. This is what guarantees `agent-deck remove` is truly terminal
// and that a stopped service stays stopped.
func TestTmuxService_ExplicitStopDoesNotTriggerRestart(t *testing.T) {
	requireSystemdUserRun(t)
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skipf("tmux not available: %v", err)
	}

	tmuxLName := "v1721-svcitstop-" + randomServerSuffix(t)
	tmuxSessName := "svcstop"
	unit, err := spawnServiceModeTmux(t, tmuxLName, tmuxSessName)
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = exec.Command("systemctl", "--user", "stop", unit).Run()
		_ = exec.Command("systemctl", "--user", "reset-failed", unit).Run()
		_ = exec.Command("tmux", "-L", tmuxLName, "kill-server").Run()
	})

	pid := waitForServicePID(t, unit, 5*time.Second)
	require.NotZero(t, pid)

	require.NoError(t, exec.Command("systemctl", "--user", "stop", unit).Run(),
		"systemctl --user stop must succeed on an active unit")
	_ = exec.Command("systemctl", "--user", "reset-failed", unit).Run()

	// Give systemd a beat to finalize; then assert no new PID re-appears
	// within a 4s window. Restart=on-failure must NOT fire on clean stop.
	time.Sleep(4 * time.Second)
	after := readServicePID(unit)
	require.Zero(t, after,
		"expected tmux daemon to stay dead after systemctl stop; got PID %d", after)
}

// TestStopServiceUnit_HandlesMissingSystemd: on hosts without systemctl
// the helper must return nil (no-op) so `agent-deck remove` on a
// non-systemd host doesn't spew errors.
func TestStopServiceUnit_HandlesMissingSystemd(t *testing.T) {
	// We can't easily remove systemctl from PATH in a test without
	// mutating global state. Instead, call StopServiceUnit with a
	// name that will never match an existing unit and verify it
	// returns nil (best-effort semantics).
	err := StopServiceUnit("does-not-exist-" + randomServerSuffix(t))
	require.NoError(t, err, "StopServiceUnit must never return an error — it's best-effort cleanup")
}

// waitForServicePID polls systemctl show until MainPID becomes non-zero
// or the timeout elapses. Returns 0 on timeout.
func waitForServicePID(t *testing.T, unitName string, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pid := readServicePID(unitName); pid != 0 {
			return pid
		}
		time.Sleep(100 * time.Millisecond)
	}
	return 0
}

// readServicePID extracts MainPID from `systemctl --user show`. Returns
// 0 on any error or if the PID is not alive (stale).
func readServicePID(unitName string) int {
	out, err := exec.Command("systemctl", "--user", "show", unitName, "-p", "MainPID").Output()
	if err != nil {
		return 0
	}
	line := strings.TrimSpace(string(out))
	const prefix = "MainPID="
	if !strings.HasPrefix(line, prefix) {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimPrefix(line, prefix))
	if err != nil || n == 0 {
		return 0
	}
	// Signal 0 is the kernel permission+existence check with no delivery.
	if err := syscall.Kill(n, 0); err != nil {
		return 0
	}
	return n
}

// readServiceNRestarts extracts NRestarts from `systemctl --user show`.
func readServiceNRestarts(unitName string) int {
	out, err := exec.Command("systemctl", "--user", "show", unitName, "-p", "NRestarts").Output()
	if err != nil {
		return 0
	}
	line := strings.TrimSpace(string(out))
	const prefix = "NRestarts="
	if !strings.HasPrefix(line, prefix) {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimPrefix(line, prefix))
	if err != nil {
		return 0
	}
	return n
}
