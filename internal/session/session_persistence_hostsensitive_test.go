//go:build hostsensitive

// Host-sensitive session-persistence tests. Built and run only when the
// `hostsensitive` build tag is supplied (e.g. nightly job:
// `go test -tags hostsensitive`). See issue #969 for the categorization
// rationale.

package session

import (
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// startFakeLoginScope launches a throwaway systemd user scope that simulates
// an SSH login-session scope: `systemd-run --user --scope --unit=fake-login-<hex>
// bash -c "exec sleep 300"`. The scope stays alive until the test (or its
// cleanup) calls `systemctl --user stop <name>.scope`. Returns the unit name
// (without the ".scope" suffix) and registers a best-effort stop in t.Cleanup.
//
// Safety: scope unit names use the literal "fake-login-" prefix plus an 8-hex
// random suffix. Cleanup only ever stops that exact unit — never a wildcard.
func startFakeLoginScope(t *testing.T) string {
	t.Helper()
	fakeName := "fake-login-" + randomHex8(t)
	cmd := exec.Command("systemd-run", "--user", "--scope", "--quiet",
		"--collect", "--unit="+fakeName,
		"bash", "-c", "exec sleep 300")
	if err := cmd.Start(); err != nil {
		t.Fatalf("startFakeLoginScope: systemd-run start: %v", err)
	}
	t.Cleanup(func() {
		// Idempotent: scope may already be stopped by the test body.
		_ = exec.Command("systemctl", "--user", "stop", fakeName+".scope").Run()
	})
	// Give systemd up to 2s to register the transient scope so a racing
	// systemctl stop in the test body is not a no-op.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := exec.Command("systemctl", "--user", "is-active", "--quiet", fakeName+".scope").Run(); err == nil {
			return fakeName
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Not strictly fatal — the scope may be in "activating" state which
	// is still stoppable. Return the name and let the caller proceed.
	return fakeName
}

// startAgentDeckTmuxInUserScope launches a tmux server under its OWN
// `agentdeck-tmux-<serverName>` user scope — mirroring the production
// `LaunchInUserScope=true` path in internal/tmux/tmux.go:startCommandSpec.
// Uses `tmux -L <serverName>` so kill-server is scoped to this test's
// private socket (never touches user sessions — see repo CLAUDE.md tmux
// safety mandate and 2025-12-10 incident).
//
// Returns the tmux server PID (read from `systemctl --user show -p MainPID`).
// Registers cleanup that kills the private tmux socket and stops the scope.
func startAgentDeckTmuxInUserScope(t *testing.T, serverName string) int {
	t.Helper()
	unit := "agentdeck-tmux-" + serverName
	cmd := exec.Command("systemd-run", "--user", "--scope", "--quiet",
		"--collect", "--unit="+unit,
		"tmux", "-L", serverName, "new-session", "-d", "-s", "persist",
		"bash", "-c", "exec sleep 300")
	if err := cmd.Start(); err != nil {
		t.Fatalf("startAgentDeckTmuxInUserScope: systemd-run start: %v", err)
	}
	t.Cleanup(func() {
		// -L <serverName> confines kill-server to this test's private socket.
		_ = exec.Command("tmux", "-L", serverName, "kill-server").Run()
		_ = exec.Command("systemctl", "--user", "stop", unit+".scope").Run()
	})
	// Wait up to 2s for `tmux -L <serverName> list-sessions` to succeed.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := exec.Command("tmux", "-L", serverName, "list-sessions").Run(); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	// Read MainPID from systemd user manager — the server PID is the
	// MainPID of its enclosing scope.
	out, err := exec.Command("systemctl", "--user", "show", "-p", "MainPID", "--value", unit+".scope").Output()
	if err != nil {
		t.Fatalf("startAgentDeckTmuxInUserScope: systemctl show MainPID: %v", err)
	}
	pidStr := strings.TrimSpace(string(out))
	pid, perr := strconv.Atoi(pidStr)
	if perr != nil || pid <= 0 {
		t.Skipf("startAgentDeckTmuxInUserScope: MainPID unavailable (%q) — systemd user scope does not track MainPID for double-forking tmux on this host (CI runners, nested tmux). Skipping cgroup survival test.", pidStr)
	}
	return pid
}

// TestPersistence_TmuxSurvivesLoginSessionRemoval replicates the 2026-04-14
// incident root cause. It:
//
//  1. Checks GetLaunchInUserScope() default — on current v1.5.1 this is
//     false, which means the production path would have inherited the
//     login-session cgroup and died. Test fails RED here with a diagnostic
//     message telling Phase 2 what to fix. No tmux spawning happens in
//     the RED branch, so there is nothing to leak.
//  2. (Post-Phase-2 flow) Starts a fake-login user scope simulating an
//     SSH login session, starts a tmux server under its OWN
//     agentdeck-tmux-<name>.scope (mirroring the fix), tears down the
//     fake-login scope, and asserts the tmux server survives because it
//     was parented under user@UID.service, NOT under the login-session
//     scope tree.
//
// Host sensitivity: depends on `systemd-run --user`, a running user
// systemd manager that tracks MainPID for the spawned scope (NOT the case
// on most CI runners and inside nested tmux), and `loginctl enable-linger`
// being set for the executing user. Gate behind the `hostsensitive` build
// tag so default pre-push / CI runs stay deterministic; opt in via:
//
//	go test -tags hostsensitive -race ./internal/session/...
func TestPersistence_TmuxSurvivesLoginSessionRemoval(t *testing.T) {
	requireSystemdRun(t)

	// RED-state gate: if the default is still false, this test fails with
	// the diagnostic that tells Phase 2 what to fix. This check intentionally
	// runs BEFORE any tmux spawning so the RED message is unambiguous and
	// no tmux server is created to leak.
	_ = isolatedHomeDir(t)
	settings := GetTmuxSettings()
	if !settings.GetLaunchInUserScope() {
		t.Fatalf("TEST-01 RED: GetLaunchInUserScope() default is false on Linux+systemd; simulated teardown would kill production tmux. Phase 2 must flip the default; rerun this test after the flip to exercise real cgroup survival.")
	}

	// Post-Phase-2 flow: simulate the 2026-04-14 incident.
	serverName := uniqueTmuxServerName(t)
	fakeLogin := startFakeLoginScope(t)

	pid := startAgentDeckTmuxInUserScope(t, serverName)
	if !pidAlive(pid) {
		t.Fatalf("setup failure: tmux pid %d not alive immediately after spawn", pid)
	}

	// Teardown the fake login scope — simulates logind removing an SSH login session.
	if err := exec.Command("systemctl", "--user", "stop", fakeLogin+".scope").Run(); err != nil {
		// Treat non-existence as acceptable (already stopped / never registered).
		t.Logf("systemctl stop %s: %v (continuing)", fakeLogin, err)
	}

	// Give systemd up to 3s to settle the teardown.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}

	if !pidAlive(pid) {
		t.Fatalf("TEST-01 RED: tmux server pid %d died after fake-login scope teardown; expected to survive because the server was launched under its own agentdeck-tmux-<name>.scope. The 2026-04-14 incident is recurring.", pid)
	}
}
