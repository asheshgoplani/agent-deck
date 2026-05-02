// Package session: Session persistence regression test suite.
//
// Purpose
// -------
// This file holds the regression tests for the 2026-04-14 session-persistence
// incident. At 09:08:01 local time on the conductor host, a single SSH logout
// caused systemd-logind to tear down every agent-deck-managed tmux server,
// destroying 33 live Claude conversations (plus another 39 that ended up in
// "stopped" status). This was the third recurrence of the same class of bug.
//
// Mandate
// -------
// The repo-root CLAUDE.md file contains a "Session persistence: mandatory
// test coverage" section that makes this suite P0 forever. Any PR touching
// internal/tmux/**, internal/session/instance.go, internal/session/userconfig.go,
// internal/session/storage*.go, or cmd/agent-deck/session_cmd.go MUST run
// `go test -run TestPersistence_ ./internal/session/... -race -count=1` and
// include the output in the PR description. The following eight tests are
// permanently required — removing any of them without an RFC is forbidden:
//
//  1. TestPersistence_TmuxSurvivesLoginSessionRemoval
//  2. TestPersistence_TmuxDiesWithoutUserScope
//  3. TestPersistence_LinuxDefaultIsUserScope
//  4. TestPersistence_MacOSDefaultIsDirect
//  5. TestPersistence_RestartResumesConversation
//  6. TestPersistence_StartAfterSIGKILLResumesConversation
//  7. TestPersistence_ClaudeSessionIDSurvivesHookSidecarDeletion
//  8. TestPersistence_FreshSessionUsesSessionIDNotResume
//
// Phase 1 of v1.5.2 (this file) lands the shared helpers plus TEST-03 and
// TEST-04; Plans 02 and 03 of the phase append the remaining six tests.
//
// Safety note (tmux)
// ------------------
// On 2025-12-10, an earlier incident killed 40 user tmux sessions because a
// blanket `tmux kill-server` was run against all servers matching "agentdeck".
// Tests in this file MUST:
//   - use the `agentdeck-test-persist-<hex>` prefix for every server they create;
//   - only call `tmux kill-server -t <name>` with the exact server name they
//     own; and
//   - NEVER call `tmux kill-server` without a `-t <name>` filter.
//
// The helper uniqueTmuxServerName enforces this by registering a targeted
// t.Cleanup that kills only the server it allocated.
package session

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// uniqueTmuxServerName returns a tmux server name with the mandatory
// "agentdeck-test-persist-" prefix plus an 8-hex-character random suffix,
// and registers a t.Cleanup that runs `tmux kill-server -t <name>` on teardown.
//
// Safety: this helper NEVER runs a bare `tmux kill-server`. The -t filter is
// required by the repo CLAUDE.md tmux safety mandate (see the 2025-12-10
// incident notes in the package-level comment above).
func uniqueTmuxServerName(t *testing.T) string {
	t.Helper()
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("uniqueTmuxServerName: rand.Read: %v", err)
	}
	name := "agentdeck-test-persist-" + hex.EncodeToString(b[:])
	t.Cleanup(func() {
		// Safety: ONLY kill the server we created. Never run bare
		// `tmux kill-server` — that would destroy every user session on
		// the host. The -t <name> filter is mandatory.
		_ = exec.Command("tmux", "kill-server", "-t", name).Run()
	})
	return name
}

// requireSystemdRun skips the current test if systemd-run is unavailable.
//
// The skip message contains the literal substring "no systemd-run available:"
// so CI log scrapers and the grep-based acceptance criteria in the plan can
// detect a vacuous-skip regression.
func requireSystemdRun(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("systemd-run"); err != nil {
		t.Skipf("no systemd-run available: %v", err)
		return
	}
	if err := exec.Command("systemd-run", "--user", "--version").Run(); err != nil {
		t.Skipf("no systemd-run available: %v", err)
	}
}

// writeStubClaudeBinary writes an executable stub `claude` script into dir and
// returns dir so the caller can prepend it to PATH. The stub appends its argv
// (one arg per line) to the file named by AGENTDECK_TEST_ARGV_LOG (or /dev/null
// if that env var is unset), then sleeps 30 seconds so tmux panes created with
// it stay alive long enough to be inspected. The file is removed on test
// cleanup.
func writeStubClaudeBinary(t *testing.T, dir string) string {
	t.Helper()
	script := "#!/usr/bin/env bash\nprintf '%s\\n' \"$@\" >> \"${AGENTDECK_TEST_ARGV_LOG:-/dev/null}\"\nsleep 30\n"
	path := filepath.Join(dir, "claude")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("writeStubClaudeBinary: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	return dir
}

// isolatedHomeDir creates a fresh temp HOME with ~/.agent-deck/,
// ~/.agent-deck/hooks/, and ~/.claude/projects/ pre-created, then sets
// HOME to that path for the duration of the test and clears the
// agent-deck user-config cache so tests exercise the default branch of
// GetTmuxSettings(). A t.Cleanup is registered that clears the cache again
// once HOME is restored, so config state does not leak to adjacent tests.
func isolatedHomeDir(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	for _, sub := range []string{".agent-deck", ".agent-deck/hooks", ".claude/projects"} {
		if err := os.MkdirAll(filepath.Join(home, sub), 0o755); err != nil {
			t.Fatalf("isolatedHomeDir mkdir %s: %v", sub, err)
		}
	}
	t.Setenv("HOME", home)
	ClearUserConfigCache()
	t.Cleanup(func() { ClearUserConfigCache() })
	return home
}

// TestPersistence_LinuxDefaultIsUserScope pins REQ-1: on a Linux host where
// systemd-run is available and no config.toml overrides it, the default
// MUST be launch_in_user_scope=true. Phase 2 will flip the default; this
// test is RED against current v1.5.1 (userconfig.go pins the default at
// false, userconfig_test.go:~1102 still asserts that pinning).
//
// Skip semantics: on hosts without systemd-run, requireSystemdRun skips
// with "no systemd-run available: <err>" so macOS CI passes cleanly.
func TestPersistence_LinuxDefaultIsUserScope(t *testing.T) {
	requireSystemdRun(t)
	home := isolatedHomeDir(t)
	// Write an empty config so GetTmuxSettings() exercises the default
	// branch (no [tmux] section, no launch_in_user_scope override).
	cfg := filepath.Join(home, ".agent-deck", "config.toml")
	if err := os.WriteFile(cfg, []byte(""), 0o644); err != nil {
		t.Fatalf("write empty config: %v", err)
	}
	ClearUserConfigCache()

	settings := GetTmuxSettings()
	if !settings.GetLaunchInUserScope() {
		t.Fatalf("TEST-03 RED: GetLaunchInUserScope() returned false on a Linux+systemd host with no config; expected true. Phase 2 must flip the default. systemd-run present, no config override.")
	}
}

// TestPersistence_MacOSDefaultIsDirect pins REQ-1: on a host WITHOUT
// systemd-run (macOS, BSD, minimal Linux), the default MUST remain false
// and no error is logged. The test name says "MacOS" but its assertion
// body runs on any host where systemd-run is absent.
//
// Linux+systemd behavior (documented implementer choice, 2026-04-14):
// this test SKIPS on hosts where systemd-run is available. TEST-03
// covers the Linux+systemd default. TEST-04's assertion body only runs
// on hosts where systemd-run is absent. Rationale: GetTmuxSettings() in
// Phase 2 will detect systemd-run at call time; asserting
// "false on Linux+systemd" here would lock in the v1.5.1 bug and
// collide with TEST-03 after Phase 2.
func TestPersistence_MacOSDefaultIsDirect(t *testing.T) {
	if _, err := exec.LookPath("systemd-run"); err == nil {
		t.Skipf("systemd-run available; TEST-04 only asserts non-systemd behavior — see TEST-03 for Linux+systemd default")
		return
	}
	home := isolatedHomeDir(t)
	cfg := filepath.Join(home, ".agent-deck", "config.toml")
	if err := os.WriteFile(cfg, []byte(""), 0o644); err != nil {
		t.Fatalf("write empty config: %v", err)
	}
	ClearUserConfigCache()

	settings := GetTmuxSettings()
	if settings.GetLaunchInUserScope() {
		t.Fatalf("TEST-04: on a host without systemd-run, GetLaunchInUserScope() must return false, got true")
	}
}

// pidAlive returns true if a process with the given pid exists AND is not
// a zombie. syscall.Kill(pid, 0) returns nil for zombies, but for our
// "did tmux die?" assertions we treat a zombie as dead — the daemon has
// exited and is merely awaiting reap by its parent. We consult
// /proc/<pid>/status State: field; state "Z" (zombie) or "X" (dead,
// exiting) counts as dead. Non-positive pids and missing /proc entries
// are also dead.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if syscall.Kill(pid, syscall.Signal(0)) != nil {
		return false
	}
	data, rerr := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if rerr != nil {
		// /proc entry gone between the Kill(0) check and now — process has
		// been reaped. Treat as dead.
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "State:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				switch fields[1] {
				case "Z", "X":
					return false
				}
			}
			break
		}
	}
	return true
}

// randomHex8 returns 8 hex chars (4 random bytes) for use in unique unit /
// socket names. On rand.Read failure it calls t.Fatalf — a truly vacuous
// failure mode we want surfaced loudly.
func randomHex8(t *testing.T) string {
	t.Helper()
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("randomHex8: rand.Read: %v", err)
	}
	return hex.EncodeToString(b[:])
}

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
// Skip semantics: requireSystemdRun skips cleanly on macOS / non-systemd
// hosts with "no systemd-run available:" in the message.
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

// startTmuxInsideFakeLogin launches a tmux server as a grandchild of a
// throwaway fake-login-<hex> user scope — mirroring the production
// LaunchInUserScope=false path where tmux inherits the user's SSH
// login-session cgroup. Used by TEST-02 to confirm that WITHOUT
// cgroup isolation, a scope teardown does kill the tmux server.
//
// Returns (fakeName, tmuxServerPID). Registers cleanup that stops the
// scope and kills the private tmux socket (-L <serverName>).
//
// Safety: tmux socket name and scope name both use per-test random
// suffixes. kill-server is confined to the -L <serverName> socket.
func startTmuxInsideFakeLogin(t *testing.T, serverName string) (string, int) {
	t.Helper()
	fakeName := "fake-login-" + randomHex8(t)
	// Start tmux as a grandchild of the fake-login scope. The outer
	// `sleep 300` keeps the scope alive until the test body tears it down.
	shellCmd := "tmux -L " + serverName + " new-session -d -s persist bash -c 'exec sleep 300'; exec sleep 300"
	cmd := exec.Command("systemd-run", "--user", "--scope", "--quiet",
		"--collect", "--unit="+fakeName,
		"bash", "-c", shellCmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("startTmuxInsideFakeLogin: systemd-run start: %v", err)
	}
	t.Cleanup(func() {
		_ = exec.Command("systemctl", "--user", "stop", fakeName+".scope").Run()
		// -L <serverName> confines kill-server to this test's private socket.
		_ = exec.Command("tmux", "-L", serverName, "kill-server").Run()
	})
	// Poll up to 3s for the tmux server process to appear. pgrep with
	// the unique -L <serverName> argument ensures we only ever match
	// the server we just started.
	deadline := time.Now().Add(3 * time.Second)
	var pid int
	for time.Now().Before(deadline) {
		out, err := exec.Command("pgrep", "-f", "tmux -L "+serverName+" ").Output()
		if err == nil {
			for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				p, perr := strconv.Atoi(line)
				if perr == nil && p > 0 {
					pid = p
					break
				}
			}
			if pid > 0 {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatalf("startTmuxInsideFakeLogin: could not locate tmux server PID for -L %s within 3s", serverName)
	}
	return fakeName, pid
}

// pidCgroup returns the contents of /proc/<pid>/cgroup (unified hierarchy
// v2 line). Empty string on any error.
func pidCgroup(pid int) string {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cgroup")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// TestPersistence_TmuxDiesWithoutUserScope is the INVERSE PIN. It asserts
// that when tmux is spawned WITHOUT the systemd-run --user --scope wrap
// (i.e., launch_in_user_scope=false — the current v1.5.1 default and also
// the explicit opt-out path after Phase 2), a login-session scope teardown
// DOES kill the tmux server. This replicates the 2026-04-14 incident root
// cause and must stay green for the entire milestone. Any future "fix"
// that silently masks opt-outs will break this test.
//
// Skip semantics:
//   - requireSystemdRun skips cleanly on macOS / non-systemd hosts with
//     "no systemd-run available:" in the message.
//   - If this process is already running inside a transient scope (e.g., a
//     tmux-spawn-*.scope used by agent-deck itself, or a nested
//     systemd-run --scope call), systemd places the child scope's tracked
//     processes in the PARENT scope's cgroup rather than the new unit's
//     cgroup. In that edge case the scope-teardown simulation is a no-op
//     and the test skips with a diagnostic so CI (running from a normal
//     login shell) still exercises the assertion.
func TestPersistence_TmuxDiesWithoutUserScope(t *testing.T) {
	requireSystemdRun(t)
	_ = isolatedHomeDir(t)
	serverName := uniqueTmuxServerName(t)

	fakeName, pid := startTmuxInsideFakeLogin(t, serverName)
	if !pidAlive(pid) {
		t.Fatalf("setup failure: tmux server pid %d not alive immediately after spawn", pid)
	}

	// Diagnostic: record the actual cgroup placement so failures surface the
	// systemd nesting edge case loudly.
	t.Logf("tmux pid=%d cgroup=%q", pid, pidCgroup(pid))

	// Nested-scope edge case: if tmux did not actually land inside the
	// fake-login scope's cgroup, the scope teardown cannot kill it and the
	// assertion below would be testing nothing. Skip cleanly so CI running
	// from a normal login shell (where tmux DOES land in the scope cgroup)
	// still exercises the real assertion.
	cg := pidCgroup(pid)
	if !strings.Contains(cg, fakeName+".scope") {
		t.Skipf("TEST-02 skipped: tmux pid %d did not land in %s.scope cgroup (got %q) — this process is likely already inside a transient scope, which reparents child scopes. Run from a login shell or the verify-session-persistence.sh harness.", pid, fakeName, cg)
	}

	// Simulate the 2026-04-14 incident: systemd-logind forcibly terminates
	// an SSH login-session scope when the user logs out. That is NOT a
	// polite `systemctl stop` — scopes by default release their cgroup
	// without actively killing members, and `systemctl kill` on an
	// already-transitioning scope can race with concurrent tmux forks.
	// The only atomic, race-free primitive is cgroup v2's `cgroup.kill`,
	// which SIGKILLs every task in the cgroup (and any concurrently
	// forking descendants) in one kernel operation. This matches the
	// effective behavior logind applies to a session scope on logout.
	scopeCg, scopeErr := exec.Command("systemctl", "--user", "show",
		"-p", "ControlGroup", "--value", fakeName+".scope").Output()
	scopeCgPath := strings.TrimSpace(string(scopeCg))
	if scopeErr != nil || scopeCgPath == "" {
		t.Fatalf("could not resolve ControlGroup for %s: err=%v out=%q", fakeName, scopeErr, scopeCgPath)
	}
	killFile := "/sys/fs/cgroup" + scopeCgPath + "/cgroup.kill"
	if err := os.WriteFile(killFile, []byte("1"), 0o644); err != nil {
		t.Fatalf("write cgroup.kill %s: %v", killFile, err)
	}

	// Poll up to 3s for the pid to die. cgroup.kill delivers SIGKILL to
	// all tasks atomically; reap is near-instant but scheduler latency can
	// add tens of milliseconds.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !pidAlive(pid) {
			return // PASS — tmux died with the scope as expected.
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Final diagnostic before failing: report the pid's cgroup state so a
	// nested-scope or SIGKILL-not-delivered regression is easy to diagnose.
	finalCg := pidCgroup(pid)
	t.Fatalf("TEST-02 INVERSE PIN: tmux server pid %d survived cgroup.kill SIGKILL teardown WITHOUT launch_in_user_scope after 3s. "+
		"Pid cgroup after kill: %q. "+
		"The opt-out path must remain vulnerable so any future 'fix' that silently masks opt-outs is caught. Expected death.",
		pid, finalCg)
}

// ----------------------------------------------------------------------------
// Wave 3 (Plan 03): resume-dispatch helpers + TEST-05, TEST-06, TEST-07, TEST-08
// ----------------------------------------------------------------------------
//
// These tests exercise the REAL Claude dispatch paths (`(*Instance).Start()`
// and `(*Instance).Restart()`) by placing a stub `claude` binary on PATH and
// capturing the argv it receives when the dispatch spawns it inside a real
// tmux session. The contract under test is REQ-2: every path that starts a
// Claude session on an Instance with a non-empty `ClaudeSessionID` and an
// existing JSONL transcript MUST produce `claude --resume <id>`; a fresh
// session (no transcript) MUST produce `claude --session-id <id>`.
//
// Per CLAUDE.md no-mocking rule: tmux and shell spawn are real binaries; only
// the `claude` binary itself is a stub (explicitly carved out in CONTEXT.md
// because these tests assert on the spawned command line, not Claude's
// behavior).

// readCapturedClaudeArgv polls the stub claude argv log until it is non-empty
// (stub has been spawned and wrote its argv), then returns the argv lines.
// Fatals if timeout elapses with empty log (dispatch never spawned claude).
func readCapturedClaudeArgv(t *testing.T, logPath string, timeout time.Duration) []string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(logPath)
		if err == nil && len(data) > 0 {
			var argv []string
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimSpace(line)
				if line != "" {
					argv = append(argv, line)
				}
			}
			if len(argv) > 0 {
				return argv
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("readCapturedClaudeArgv: no argv captured in %s at %s — stub claude was never spawned; check PATH prepending and tmux session creation", timeout, logPath)
	return nil // unreachable
}

// newClaudeInstanceForDispatch constructs an *Instance wired for Claude
// dispatch testing. It:
//   - creates a real project directory under the isolated HOME,
//   - calls NewInstanceWithTool so inst.tmuxSession is initialized,
//   - overrides inst.ID with a deterministic-per-test hex suffix so the
//     hook-sidecar path is predictable (TEST-07 sanity assertion),
//   - generates a uuid-shaped inst.ClaudeSessionID from 16 random bytes,
//   - sets inst.Command = "claude" so buildClaudeCommandWithMessage takes
//     the `baseCommand == "claude"` branch,
//   - registers t.Cleanup to kill the tmux session via the safe (Name-
//     scoped) (*tmux.Session).Kill() path.
func newClaudeInstanceForDispatch(t *testing.T, home string) *Instance {
	t.Helper()
	var idb [4]byte
	if _, err := rand.Read(idb[:]); err != nil {
		t.Fatalf("newClaudeInstanceForDispatch: rand.Read(idb): %v", err)
	}
	var sidb [16]byte
	if _, err := rand.Read(sidb[:]); err != nil {
		t.Fatalf("newClaudeInstanceForDispatch: rand.Read(sidb): %v", err)
	}
	sidHex := hex.EncodeToString(sidb[:])
	sid := sidHex[0:8] + "-" + sidHex[8:12] + "-" + sidHex[12:16] + "-" + sidHex[16:20] + "-" + sidHex[20:32]

	projectPath := filepath.Join(home, "project")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatalf("newClaudeInstanceForDispatch: mkdir project: %v", err)
	}
	title := "persist-test-" + hex.EncodeToString(idb[:])
	inst := NewInstanceWithTool(title, projectPath, "claude")
	// Override auto-generated ID so the sidecar path is deterministic for
	// TEST-07 and log messages reference a recognizable test ID. Note: the
	// tmux session Name was set by NewInstanceWithTool from the title — it
	// remains unique via the "persist-test-<hex>" suffix regardless of ID.
	inst.ID = "test-" + hex.EncodeToString(idb[:])
	inst.ClaudeSessionID = sid
	inst.Command = "claude"

	t.Cleanup(func() {
		// inst.tmuxSession.Kill() targets the unique session Name — SAFE;
		// does NOT call bare `tmux kill-server`.
		if inst.tmuxSession != nil {
			_ = inst.tmuxSession.Kill()
		}
	})
	return inst
}

// setupStubClaudeOnPATH drops the writeStubClaudeBinary helper's stub script
// at <home>/bin/claude, sets AGENTDECK_TEST_ARGV_LOG so the stub logs argv to
// a known file, and configures the dispatch to invoke the stub by its
// ABSOLUTE path.
//
// [Deviation Rule 3 — Blocking fix applied during Plan 03 Task 2]
// The plan prescribed prepending <home>/bin to PATH so `claude` in the
// spawned tmux pane would resolve to the stub. That does not work on this
// executor host (or any environment where tests run inside a pre-existing
// tmux server): `tmux new-session` uses the DEFAULT tmux socket, which is
// already owned by a server started long before the test. That server's
// environment was captured at its own startup, and new sessions inherit the
// server's PATH rather than the spawning client's PATH. Empirically
// confirmed: env vars set via `t.Setenv("PATH", ...)` do not reach the
// initial process of a tmux pane on the default socket.
//
// The robust fix: write a real agent-deck config.toml under the isolated
// HOME with `[claude] command = "<abs stub path>"`. GetClaudeCommand() picks
// this up at dispatch time (instance.go:4121, :492), and the spawned
// command embeds the stub's ABSOLUTE path — no PATH search needed. We also
// forward AGENTDECK_TEST_ARGV_LOG into the tmux pane environment via
// tmux set-environment on the default socket (inline export is redundant
// but cheap; env-var resolution inside the stub reads it from the pane's
// env, which tmux set-environment on the default socket injects correctly
// for ALL subsequent new-sessions in that server).
//
// Returns the argv log path.
func setupStubClaudeOnPATH(t *testing.T, home string) string {
	t.Helper()
	binDir := filepath.Join(home, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("setupStubClaudeOnPATH: mkdir binDir: %v", err)
	}
	writeStubClaudeBinary(t, binDir)
	stubAbs := filepath.Join(binDir, "claude")
	argvLog := filepath.Join(home, "claude-argv.log")

	// Write an empty argv log file so readers see a sentinel rather than
	// ENOENT before the stub first runs.
	if err := os.WriteFile(argvLog, nil, 0o644); err != nil {
		t.Fatalf("setupStubClaudeOnPATH: init argvLog: %v", err)
	}

	// Configure [claude] command = <abs stub> via the user config under the
	// isolated HOME. This is read by GetClaudeCommand() at dispatch time.
	cfgPath := filepath.Join(home, ".agent-deck", "config.toml")
	cfgBody := "[claude]\ncommand = \"" + stubAbs + "\"\n"
	if err := os.WriteFile(cfgPath, []byte(cfgBody), 0o644); err != nil {
		t.Fatalf("setupStubClaudeOnPATH: write config.toml: %v", err)
	}
	ClearUserConfigCache()
	t.Cleanup(func() { ClearUserConfigCache() })

	// [Deviation Rule 3 — Blocking fix] GetClaudeConfigDir() at claude.go:234
	// short-circuits to the CLAUDE_CONFIG_DIR env var when set. On this
	// executor host (and on any real user's machine) that env var is
	// pre-set to the user's real ~/.claude — which poisons
	// sessionHasConversationData() by pointing it at the real home's
	// projects/ dir instead of the isolated HOME. We unset it for the
	// duration of the test so GetClaudeConfigDir() falls through to the
	// os.UserHomeDir() default under our isolated HOME. The Plan 01
	// isolatedHomeDir helper only sets HOME; this helper layers the
	// CLAUDE_CONFIG_DIR unset that the dispatch tests require.
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	// Set env var on THIS test process. Go's exec inherits; tmux binary
	// sees it. But tmux new-session does not propagate to the server's
	// pane env on the default socket, so also inject via tmux
	// set-environment -g (global) on the default socket as a belt-and-
	// suspenders path — the stub resolves AGENTDECK_TEST_ARGV_LOG inside
	// the pane's env.
	t.Setenv("AGENTDECK_TEST_ARGV_LOG", argvLog)
	// Best-effort: set it globally on the default tmux socket so new
	// sessions inherit. Errors ignored (no-op if tmux is absent or server
	// is unreachable — tests that need it call requireTmux() first).
	_ = exec.Command("tmux", "set-environment", "-g", "AGENTDECK_TEST_ARGV_LOG", argvLog).Run()
	// Register a cleanup to unset the global tmux env var so it does not
	// leak across tests or into the user's interactive sessions.
	t.Cleanup(func() {
		_ = exec.Command("tmux", "set-environment", "-g", "-u", "AGENTDECK_TEST_ARGV_LOG").Run()
	})
	return argvLog
}

// writeSyntheticJSONLTranscript writes a 2-line synthetic Claude JSONL
// transcript at ~/.claude/projects/<ConvertToClaudeDirName(ProjectPath)>/<ClaudeSessionID>.jsonl
// under the isolated HOME. Each line embeds a literal "sessionId" field so
// sessionHasConversationData() returns true (it greps for `"sessionId"`).
// Returns the full transcript path. Registers t.Cleanup to remove it.
func writeSyntheticJSONLTranscript(t *testing.T, home string, inst *Instance) string {
	t.Helper()
	projectDirName := ConvertToClaudeDirName(inst.ProjectPath)
	dir := filepath.Join(home, ".claude", "projects", projectDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("writeSyntheticJSONLTranscript: mkdir projects: %v", err)
	}
	path := filepath.Join(dir, inst.ClaudeSessionID+".jsonl")
	// sessionHasConversationData scans for the literal substring `"sessionId"`.
	// Embedding it in each line guarantees a real-conversation signal.
	lines := []map[string]interface{}{
		{"sessionId": inst.ClaudeSessionID, "role": "user", "content": "hello"},
		{"sessionId": inst.ClaudeSessionID, "role": "assistant", "content": "hi back"},
	}
	var buf []byte
	for _, ln := range lines {
		b, err := json.Marshal(ln)
		if err != nil {
			t.Fatalf("writeSyntheticJSONLTranscript: marshal jsonl: %v", err)
		}
		buf = append(buf, b...)
		buf = append(buf, '\n')
	}
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatalf("writeSyntheticJSONLTranscript: write jsonl: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	return path
}

// TestPersistence_FreshSessionUsesSessionIDNotResume pins REQ-2 fresh-session
// contract: buildClaudeResumeCommand() on an Instance with no JSONL transcript
// MUST produce "claude --session-id <id>", NOT "claude --resume <id>". Passing
// --resume for a non-existent conversation id causes claude to exit with
// "No conversation found".
//
// Per CONTEXT.md FAIL-or-PASS qualifier: current v1.5.1 code at
// internal/session/instance.go:4150 routes this correctly via
// sessionHasConversationData() — so this test PASSES today as a regression
// guard. The unambiguous failure message below protects against future
// regressions that would flip the branch. This test does NOT exercise the
// Start() dispatch path (TEST-06 does); it guards the helper contract only.
func TestPersistence_FreshSessionUsesSessionIDNotResume(t *testing.T) {
	home := isolatedHomeDir(t)
	inst := newClaudeInstanceForDispatch(t, home)
	// NO JSONL transcript — fresh session.

	cmdLine := inst.buildClaudeResumeCommand()

	if !strings.Contains(cmdLine, "--session-id "+inst.ClaudeSessionID) {
		t.Fatalf("TEST-08: buildClaudeResumeCommand() with NO JSONL transcript MUST use '--session-id %s'. This prevents 'No conversation found' errors on first start. Got: %q", inst.ClaudeSessionID, cmdLine)
	}
	if strings.Contains(cmdLine, "--resume") {
		t.Fatalf("TEST-08: buildClaudeResumeCommand() must NOT use --resume for a fresh session (no JSONL transcript exists at ~/.claude/projects/<hash>/<id>.jsonl). Got: %q", cmdLine)
	}
}

// requireTmux skips the current test if the tmux binary is not on PATH. The
// skip message contains "no tmux available:" so CI log scrapers can detect a
// vacuous-skip regression.
func requireTmux(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skipf("no tmux available: %v", err)
	}
}

// TestPersistence_RestartResumesConversation pins REQ-2 Restart() contract:
// when a JSONL transcript exists for the instance's ClaudeSessionID,
// inst.Restart() MUST spawn "claude --resume <id>", NOT "claude --session-id
// <new-uuid>".
//
// Driven via the REAL Restart() dispatch path (internal/session/instance.go
// line 3763). Stub claude on PATH captures argv to AGENTDECK_TEST_ARGV_LOG.
//
// Per CONTEXT.md FAIL-or-PASS qualifier and the dispatch_path_analysis in
// 03-PLAN.md: current v1.5.1 code at instance.go:3789 correctly routes
// Restart() through buildClaudeResumeCommand() — so this test may PASS
// today. Phase 3's REQ-2 fix lives in Start() (TEST-06), not Restart(). This
// test is kept as a REGRESSION GUARD: any future change that breaks
// Restart()'s resume routing (e.g. removing the `i.ClaudeSessionID != ""`
// check at line 3788) will fail this test. Either outcome (PASS now, FAIL
// after regression) is acceptable; the unambiguous failure message below
// prevents silent breakage.
func TestPersistence_RestartResumesConversation(t *testing.T) {
	requireTmux(t)
	home := isolatedHomeDir(t)
	argvLog := setupStubClaudeOnPATH(t, home)
	inst := newClaudeInstanceForDispatch(t, home)

	// First bring the tmux session up so Restart()'s respawn-pane branch
	// (instance.go:3788 — requires tmuxSession.Exists()) is taken.
	//
	// IMPORTANT: Start() at instance.go:566-567 MINTS A NEW UUID via
	// generateUUID() and overwrites inst.ClaudeSessionID with it. Any JSONL
	// transcript written BEFORE Start() points at a stale UUID that is no
	// longer inst.ClaudeSessionID by the time Restart() runs. To pin
	// Restart()'s resume routing, the JSONL must be written AFTER Start()
	// completes, under the post-Start ClaudeSessionID. This mirrors the
	// 2026-04-14 production scenario: a real Claude session ran to the point
	// of writing a transcript, then tmux was SIGKILLed; on restart, Claude
	// finds a JSONL matching its current session UUID.
	if err := inst.Start(); err != nil {
		t.Fatalf("setup: inst.Start: %v", err)
	}
	time.Sleep(500 * time.Millisecond) // let initial argv be written

	// Now write the synthetic JSONL for the POST-Start ClaudeSessionID so
	// sessionHasConversationData() returns true when Restart() consults it.
	jsonlPath := writeSyntheticJSONLTranscript(t, home, inst)

	// Reset the argv log so the subsequent Restart's argv is the only entry.
	if err := os.WriteFile(argvLog, nil, 0o644); err != nil {
		t.Fatalf("truncate argvLog: %v", err)
	}

	if err := inst.Restart(); err != nil {
		t.Fatalf("Restart: %v", err)
	}

	argv := readCapturedClaudeArgv(t, argvLog, 3*time.Second)
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "--resume") || !strings.Contains(joined, inst.ClaudeSessionID) {
		t.Fatalf("TEST-05 RED: after Restart() with JSONL transcript at %s, captured claude argv must contain '--resume %s'. Got argv: %v", jsonlPath, inst.ClaudeSessionID, argv)
	}
}

// TestPersistence_StartAfterSIGKILLResumesConversation is the core REQ-2 RED
// test. Models the 2026-04-14 incident: tmux server is SIGKILLed by an SSH
// logout, instance transitions to Status=error, user runs "agent-deck session
// start" — which calls inst.Start() (cmd/agent-deck/session_cmd.go:188).
//
// The CONTRACT: Start() on an Instance with a populated ClaudeSessionID and
// JSONL transcript MUST spawn "claude --resume <id>", NOT a new UUID.
//
// Per dispatch_path_analysis in 03-PLAN.md: current v1.5.1 Start()
// (instance.go:1873) calls buildClaudeCommand() at line 1883, which runs
// through the capture-resume pattern (line 550+) that generates a brand-new
// UUID and spawns "claude --session-id <NEW_UUID>". It does NOT consult
// i.ClaudeSessionID. So this test FAILS RED on current code.
//
// Phase 3's REQ-2 fix: route Start() through buildClaudeResumeCommand() when
// IsClaudeCompatible(i.Tool) && i.ClaudeSessionID != "" — mirroring the
// Restart() code path at line 3789.
func TestPersistence_StartAfterSIGKILLResumesConversation(t *testing.T) {
	requireTmux(t)
	home := isolatedHomeDir(t)
	argvLog := setupStubClaudeOnPATH(t, home)
	inst := newClaudeInstanceForDispatch(t, home)
	// Simulate the post-SIGKILL state transition.
	inst.Status = StatusError
	writeSyntheticJSONLTranscript(t, home, inst)

	if err := inst.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	argv := readCapturedClaudeArgv(t, argvLog, 3*time.Second)
	joined := strings.Join(argv, " ")

	if !strings.Contains(joined, "--resume") || !strings.Contains(joined, inst.ClaudeSessionID) {
		t.Fatalf("TEST-06 RED: after inst.Start() with Status=StatusError, ClaudeSessionID=%s, and JSONL transcript present, captured claude argv must contain '--resume %s'. Got argv: %v. This is the 2026-04-14 incident REQ-2 root cause: Start() dispatches through buildClaudeCommand (instance.go:1883) instead of buildClaudeResumeCommand. Phase 3 must fix this.", inst.ClaudeSessionID, inst.ClaudeSessionID, argv)
	}
	if strings.Contains(joined, "--session-id") && !strings.Contains(joined, inst.ClaudeSessionID) {
		t.Fatalf("TEST-06 RED: Start() minted a NEW session UUID instead of resuming ClaudeSessionID=%s. Argv: %v", inst.ClaudeSessionID, argv)
	}
}

// TestPersistence_ClaudeSessionIDSurvivesHookSidecarDeletion pins the
// invariant from docs/session-id-lifecycle.md: instance JSON is the
// authoritative ClaudeSessionID source. The hook sidecar at
// ~/.agent-deck/hooks/<id>.sid is a read-only fallback for hook-event
// processing (updateSessionIDFromHook at instance.go:2626) — it is NOT
// consulted by Start() or Restart() dispatch. Deleting the sidecar MUST NOT
// affect the claude --resume command produced by a session start.
//
// Flow:
//  1. Write sidecar at ~/.agent-deck/hooks/<id>.sid with ClaudeSessionID.
//  2. Delete the sidecar (simulates corruption or cleanup incident).
//  3. Call inst.Start() with a JSONL transcript present.
//  4. Assert captured claude argv contains "--resume <ClaudeSessionID>".
//
// Per dispatch_path_analysis in 03-PLAN.md: this test FAILS RED on current
// v1.5.1 for the SAME root cause as TEST-06 (Start() bypasses resume).
// After Phase 3 fixes Start() to route through buildClaudeResumeCommand,
// this test will GREEN because ClaudeSessionID is read from the Instance
// struct (which mirrors instance JSON storage), NOT from the sidecar.
func TestPersistence_ClaudeSessionIDSurvivesHookSidecarDeletion(t *testing.T) {
	requireTmux(t)
	home := isolatedHomeDir(t)
	argvLog := setupStubClaudeOnPATH(t, home)
	inst := newClaudeInstanceForDispatch(t, home)

	sidecarPath := filepath.Join(home, ".agent-deck", "hooks", inst.ID+".sid")
	if got := HookSessionAnchorPath(inst.ID); got != sidecarPath {
		t.Fatalf("sidecar path mismatch: got %q want %q — isolatedHomeDir HOME override may not have propagated", got, sidecarPath)
	}
	if err := os.MkdirAll(filepath.Dir(sidecarPath), 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	if err := os.WriteFile(sidecarPath, []byte(inst.ClaudeSessionID+"\n"), 0o644); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	if _, err := os.Stat(sidecarPath); err != nil {
		t.Fatalf("setup: sidecar not written: %v", err)
	}

	if err := os.Remove(sidecarPath); err != nil {
		t.Fatalf("delete sidecar: %v", err)
	}
	if _, err := os.Stat(sidecarPath); !os.IsNotExist(err) {
		t.Fatalf("setup: sidecar still present after delete: err=%v", err)
	}
	if inst.ClaudeSessionID == "" {
		t.Fatalf("TEST-07 RED: ClaudeSessionID was cleared when sidecar was deleted; expected instance-JSON to remain authoritative")
	}

	writeSyntheticJSONLTranscript(t, home, inst)

	if err := inst.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	argv := readCapturedClaudeArgv(t, argvLog, 3*time.Second)
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "--resume") || !strings.Contains(joined, inst.ClaudeSessionID) {
		t.Fatalf("TEST-07 RED: after deleting hook sidecar at %s, inst.Start() must still spawn 'claude --resume %s' because ClaudeSessionID lives in instance storage, not the sidecar. Got argv: %v. Root cause: Start() bypasses buildClaudeResumeCommand — same as TEST-06. Phase 3 fix will make both tests GREEN.", sidecarPath, inst.ClaudeSessionID, argv)
	}
}

// TestPersistence_ClaudeSessionIDPreservedThroughStopError pins PERSIST-08:
// Instance.ClaudeSessionID MUST be preserved through any transition to
// StatusStopped or StatusError, and MUST remain unchanged after Start() is
// called on an instance that already has a populated ClaudeSessionID.
//
// This test is strictly additive — it is NOT one of the eight mandated
// TestPersistence_* tests in CLAUDE.md. It lives in the same file so a
// future refactor that accidentally clears ClaudeSessionID on an error-
// recovery path is caught by the same suite the CLAUDE.md mandate runs.
//
// Per 03-CONTEXT.md Decision 3: the 2026-04-14 divergence was caused by
// buildClaudeCommand() at instance.go:566-567 MINTING a new UUID and
// overwriting i.ClaudeSessionID. Once Plan 03-03 routes Start() through
// buildClaudeResumeCommand, that overwrite stops firing — but this test
// guards the invariant independently of the dispatch path so we catch any
// future code path that explicitly assigns i.ClaudeSessionID = "".
//
// Likely result on current v1.5.1 code: PASSES (there is no code that
// explicitly clears ClaudeSessionID on Stop/Error transitions). This test
// is a regression guard, not a RED test. Plan 03-02 is the RED test that
// drives the Start() dispatch fix.
func TestPersistence_ClaudeSessionIDPreservedThroughStopError(t *testing.T) {
	requireTmux(t)
	home := isolatedHomeDir(t)
	setupStubClaudeOnPATH(t, home)
	inst := newClaudeInstanceForDispatch(t, home)
	originalID := inst.ClaudeSessionID
	if originalID == "" {
		t.Fatalf("setup: newClaudeInstanceForDispatch returned empty ClaudeSessionID")
	}

	// Step 1: simulate StatusRunning → StatusStopped transition.
	inst.Status = StatusRunning
	inst.Status = StatusStopped
	if inst.ClaudeSessionID != originalID {
		t.Fatalf("PERSIST-08: ClaudeSessionID cleared on StatusRunning→StatusStopped transition. want %q got %q", originalID, inst.ClaudeSessionID)
	}

	// Step 2: simulate a post-SIGKILL StatusError transition.
	inst.Status = StatusError
	if inst.ClaudeSessionID != originalID {
		t.Fatalf("PERSIST-08: ClaudeSessionID cleared on StatusError transition. want %q got %q", originalID, inst.ClaudeSessionID)
	}

	// Step 3: write a JSONL transcript so the post-Start resume path would
	// naturally fire (once Plan 03-03 lands). Even if it does not fire today
	// (Start mints new UUID on current code), this test's contract is about
	// PERSISTENCE of the ID, not dispatch — it fails only if Start() or any
	// downstream path explicitly clears i.ClaudeSessionID. NOTE: on current
	// code, Start() at instance.go:566-567 WILL overwrite i.ClaudeSessionID
	// with a newly minted UUID. That is the bug Plan 03-03 fixes. Until that
	// fix lands, this test will fail at Step 4 below — which is the intended
	// contract: this test GUARDS the invariant. Once Plan 03-03 lands, Step 4
	// passes because Start() routes through buildClaudeResumeCommand() which
	// never mints a new UUID.
	writeSyntheticJSONLTranscript(t, home, inst)

	// Step 4: call Start() and assert the ID is still the original.
	if err := inst.Start(); err != nil {
		t.Fatalf("inst.Start: %v", err)
	}
	if inst.ClaudeSessionID != originalID {
		t.Fatalf("PERSIST-08: Start() overwrote ClaudeSessionID. want %q got %q — this is the 2026-04-14 root cause (instance.go:566-567 mint). Plan 03-03 routes Start() through buildClaudeResumeCommand when ClaudeSessionID != \"\", which never mints a new UUID.", originalID, inst.ClaudeSessionID)
	}
}

// TestPersistence_SessionIDFallbackWhenJSONLMissing pins CONTEXT Decision 5:
// when Instance.ClaudeSessionID is populated but NO JSONL transcript exists
// under ~/.claude/projects/<hash>/, inst.Start() MUST produce
// "claude --session-id <stored-id>" — NEVER "--resume" and NEVER a newly
// minted UUID.
//
// This is the regression test for the 2026-04-14 conductor-host divergence:
// stored ClaudeSessionID = f1e103df-... but Claude was writing to a DIFFERENT
// UUID (b9403638-...) that had never been captured. Root cause: current
// Start() at instance.go:1883 routes through buildClaudeCommand, which at
// line 566-567 unconditionally mints a fresh UUID and overwrites
// i.ClaudeSessionID. The fix (Plan 03-03) routes Start() through
// buildClaudeResumeCommand when ClaudeSessionID != "". That helper produces
// --session-id <stored-id> (no mint) when the JSONL is absent — see
// instance.go:4175-4177.
//
// Expected on CURRENT v1.5.1 code: RED (FAIL). Start() mints a NEW UUID and
// captured argv contains that new UUID, not the stored "deadbeef-..." one.
// After Plan 03-03: GREEN.
func TestPersistence_SessionIDFallbackWhenJSONLMissing(t *testing.T) {
	requireTmux(t)
	home := isolatedHomeDir(t)
	argvLog := setupStubClaudeOnPATH(t, home)
	inst := newClaudeInstanceForDispatch(t, home)

	// Pin a recognizable stored ID so the assertion messages are unambiguous.
	// The value is a valid-shaped uuid so any downstream parser tolerates it.
	storedID := "deadbeef-fake-uuid-0000-000000000001"
	inst.ClaudeSessionID = storedID

	// Explicitly DO NOT call writeSyntheticJSONLTranscript — the absence of
	// the JSONL is the entire point of this test. Verify no JSONL exists for
	// this stored ID under the isolated HOME.
	projectDirName := ConvertToClaudeDirName(inst.ProjectPath)
	jsonlPath := filepath.Join(home, ".claude", "projects", projectDirName, storedID+".jsonl")
	if _, err := os.Stat(jsonlPath); !os.IsNotExist(err) {
		t.Fatalf("setup: JSONL already exists at %s (want ENOENT): err=%v", jsonlPath, err)
	}

	if err := inst.Start(); err != nil {
		t.Fatalf("inst.Start: %v", err)
	}

	argv := readCapturedClaudeArgv(t, argvLog, 3*time.Second)
	joined := strings.Join(argv, " ")

	// Assertion A: argv must contain the stored ID exactly.
	if !strings.Contains(joined, storedID) {
		t.Fatalf("SessionIDFallback RED: captured argv does not contain stored ClaudeSessionID %q — the stored ID was discarded / overwritten. Root cause: instance.go:566-567 mints a fresh UUID. Argv: %v", storedID, argv)
	}

	// Assertion B: argv must contain --session-id (the no-JSONL fallback), NOT --resume.
	if !strings.Contains(joined, "--session-id "+storedID) {
		t.Fatalf("SessionIDFallback RED: captured argv does not contain '--session-id %s'. With no JSONL present, buildClaudeResumeCommand must produce --session-id, not --resume. Argv: %v", storedID, argv)
	}
	if strings.Contains(joined, "--resume") {
		t.Fatalf("SessionIDFallback RED: captured argv contains --resume despite no JSONL transcript. This would cause claude 'No conversation found' errors on startup. Argv: %v", argv)
	}

	// Assertion C: inst.ClaudeSessionID must still equal the original stored
	// value — Start() must NOT have overwritten it with a freshly minted UUID.
	if inst.ClaudeSessionID != storedID {
		t.Fatalf("SessionIDFallback RED: Start() overwrote ClaudeSessionID. want %q got %q. This is the 2026-04-14 divergence root cause: instance.go:566-567 mints a fresh UUID and clobbers the stored ID, so Claude and agent-deck track different UUIDs thereafter.", storedID, inst.ClaudeSessionID)
	}
}

// captureSessionLog swaps the package-level sessionLog handle with a JSON-
// backed bytes.Buffer logger for the duration of the test. Mirrors the
// captureCgroupIsolationLog helper at userconfig_log_test.go:17-24 (Phase 2
// OBS-01 pattern). Because sessionLog is a package-level var shared across
// instance.go, callers must ensure tests using this helper do not run in
// parallel with one another — the helper restores the original handler on
// t.Cleanup, so sequential test-by-test usage is safe.
func captureSessionLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	original := sessionLog
	sessionLog = slog.New(slog.NewJSONHandler(buf, nil))
	t.Cleanup(func() { sessionLog = original })
	return buf
}

// resumeLogLines parses the capture buffer and returns all records whose
// message has prefix "resume: ". Each returned map is the full decoded JSON
// record so callers can assert on both msg and attrs.
func resumeLogLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("decode log line %q: %v", line, err)
		}
		if m, ok := rec["msg"].(string); ok && strings.HasPrefix(m, "resume: ") {
			out = append(out, rec)
		}
	}
	return out
}

// TestPersistence_ResumeLogEmitted_ConversationDataPresent pins OBS-02
// branch #1: when JSONL evidence exists for the stored ClaudeSessionID,
// buildClaudeResumeCommand emits exactly one Info record
// "resume: id=<id> reason=conversation_data_present".
func TestPersistence_ResumeLogEmitted_ConversationDataPresent(t *testing.T) {
	// [Rule 3 — Blocking fix] Mirror setupStubClaudeOnPATH's CLAUDE_CONFIG_DIR
	// unset: GetClaudeConfigDir() (instance.go:4848) short-circuits to the env
	// var when set, which on this executor points at the real user ~/.claude
	// instead of the isolated HOME. Without this unset, sessionHasConversationData
	// reads from the wrong dir and returns false, flipping the reason to
	// session_id_flag_no_jsonl (observed in Task 2 first run). See Plan 03-03
	// SUMMARY's identical deviation note at session_persistence_test.go:681.
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home := isolatedHomeDir(t)
	inst := newClaudeInstanceForDispatch(t, home)
	writeSyntheticJSONLTranscript(t, home, inst)
	buf := captureSessionLog(t)

	_ = inst.buildClaudeResumeCommand()

	lines := resumeLogLines(t, buf)
	if len(lines) != 1 {
		t.Fatalf("OBS-02: want exactly 1 'resume: ' log record, got %d. Buffer: %q", len(lines), buf.String())
	}
	msg := lines[0]["msg"].(string)
	wantPrefix := "resume: id=" + inst.ClaudeSessionID + " reason=conversation_data_present"
	if !strings.Contains(msg, wantPrefix) {
		t.Fatalf("OBS-02 message contract: want substring %q, got msg %q", wantPrefix, msg)
	}
	if got, _ := lines[0]["reason"].(string); got != "conversation_data_present" {
		t.Fatalf("OBS-02 reason attr: want %q, got %q", "conversation_data_present", got)
	}
	if got, _ := lines[0]["claude_session_id"].(string); got != inst.ClaudeSessionID {
		t.Fatalf("OBS-02 claude_session_id attr: want %q, got %q", inst.ClaudeSessionID, got)
	}
	if got, _ := lines[0]["path"].(string); got != inst.ProjectPath {
		t.Fatalf("OBS-02 path attr: want %q, got %q", inst.ProjectPath, got)
	}
	if got, _ := lines[0]["instance_id"].(string); got != inst.ID {
		t.Fatalf("OBS-02 instance_id attr: want %q, got %q", inst.ID, got)
	}
}

// TestPersistence_ResumeLogEmitted_SessionIDFlagNoJSONL pins OBS-02 branch #2:
// when no JSONL evidence exists but ClaudeSessionID is populated,
// buildClaudeResumeCommand emits exactly one Info record
// "resume: id=<id> reason=session_id_flag_no_jsonl".
func TestPersistence_ResumeLogEmitted_SessionIDFlagNoJSONL(t *testing.T) {
	home := isolatedHomeDir(t)
	inst := newClaudeInstanceForDispatch(t, home)
	// Do NOT write JSONL — absence is the point.
	buf := captureSessionLog(t)

	_ = inst.buildClaudeResumeCommand()

	lines := resumeLogLines(t, buf)
	if len(lines) != 1 {
		t.Fatalf("OBS-02 (no-jsonl): want exactly 1 'resume: ' record, got %d. Buffer: %q", len(lines), buf.String())
	}
	msg := lines[0]["msg"].(string)
	wantPrefix := "resume: id=" + inst.ClaudeSessionID + " reason=session_id_flag_no_jsonl"
	if !strings.Contains(msg, wantPrefix) {
		t.Fatalf("OBS-02 (no-jsonl) message: want substring %q, got %q", wantPrefix, msg)
	}
	if got, _ := lines[0]["reason"].(string); got != "session_id_flag_no_jsonl" {
		t.Fatalf("OBS-02 (no-jsonl) reason: want %q, got %q", "session_id_flag_no_jsonl", got)
	}
}

// TestPersistence_ResumeLogEmitted_FreshSession pins OBS-02 branch #3:
// Start() on an Instance with EMPTY ClaudeSessionID emits exactly one Info
// record "resume: none reason=fresh_session".
func TestPersistence_ResumeLogEmitted_FreshSession(t *testing.T) {
	requireTmux(t)
	home := isolatedHomeDir(t)
	setupStubClaudeOnPATH(t, home)
	inst := newClaudeInstanceForDispatch(t, home)
	inst.ClaudeSessionID = "" // force the fresh-session branch in Start()
	buf := captureSessionLog(t)

	if err := inst.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	lines := resumeLogLines(t, buf)
	if len(lines) != 1 {
		t.Fatalf("OBS-02 (fresh): want exactly 1 'resume: ' record, got %d. Buffer: %q", len(lines), buf.String())
	}
	msg := lines[0]["msg"].(string)
	if !strings.Contains(msg, "resume: none reason=fresh_session") {
		t.Fatalf("OBS-02 (fresh) message: want substring %q, got %q", "resume: none reason=fresh_session", msg)
	}
	if got, _ := lines[0]["reason"].(string); got != "fresh_session" {
		t.Fatalf("OBS-02 (fresh) reason: want %q, got %q", "fresh_session", got)
	}
}

// TestPersistence_ExplicitOptOutHonoredOnLinux pins PERSIST-03: an explicit
// `launch_in_user_scope = false` in config.toml MUST always return false,
// even on a Linux+systemd host where the new default (Plan 02) would
// otherwise return true. This closes the gap that TEST-02 alone does not
// cover — TEST-02 spawns tmux directly with the field, never exercising
// the config-load path.
//
// Skip semantics: requireSystemdRun skips cleanly on non-systemd hosts
// with "no systemd-run available:" in the message.
//
// Four-arm structure:
//   - Arm 1: empty config → default fires → MUST be true on Linux+systemd.
//   - Arm 2: explicit false → MUST stay false even though default would be true.
//   - Arm 3: explicit true → MUST be true everywhere.
//   - Arm 4: direct *bool pointer-state assertions (W2 checker fix). Locks
//     the decoder contract at the field level so a future refactor cannot
//     silently erase the nil-vs-zero distinction.
//
// RED note: against current code (LaunchInUserScope is a plain bool), arm 1
// fails AND arm 4 fails to compile (settings.LaunchInUserScope != nil is a
// type error). After Plan 02 Task 2 flips the default and migrates the
// field to *bool, this test goes GREEN across all four arms.
func TestPersistence_ExplicitOptOutHonoredOnLinux(t *testing.T) {
	requireSystemdRun(t)

	// Arm 1: empty config → default fires → MUST be true on Linux+systemd.
	// This is the same assertion as TEST-03 but co-located so a regression
	// is caught here too.
	t.Run("empty_config_defaults_true", func(t *testing.T) {
		home := isolatedHomeDir(t)
		cfg := filepath.Join(home, ".agent-deck", "config.toml")
		if err := os.WriteFile(cfg, []byte(""), 0o644); err != nil {
			t.Fatalf("write empty config: %v", err)
		}
		ClearUserConfigCache()
		resetSystemdDetectionCacheForTest()
		if got := GetTmuxSettings().GetLaunchInUserScope(); !got {
			t.Fatalf("EXPLICIT-OPT-OUT-RED arm1: empty config on Linux+systemd returned false, want true (default flip not in)")
		}
	})

	// Arm 2: explicit false → MUST stay false even though default would be true.
	t.Run("explicit_false_overrides_default", func(t *testing.T) {
		home := isolatedHomeDir(t)
		cfg := filepath.Join(home, ".agent-deck", "config.toml")
		body := "[tmux]\nlaunch_in_user_scope = false\n"
		if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
			t.Fatalf("write config.toml: %v", err)
		}
		ClearUserConfigCache()
		resetSystemdDetectionCacheForTest()
		if got := GetTmuxSettings().GetLaunchInUserScope(); got {
			t.Fatalf("EXPLICIT-OPT-OUT-RED arm2: GetLaunchInUserScope()=true with explicit false override; PERSIST-03 violated")
		}
	})

	// Arm 3: explicit true → MUST be true everywhere (sanity for the symmetry).
	t.Run("explicit_true_overrides", func(t *testing.T) {
		home := isolatedHomeDir(t)
		cfg := filepath.Join(home, ".agent-deck", "config.toml")
		body := "[tmux]\nlaunch_in_user_scope = true\n"
		if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
			t.Fatalf("write config.toml: %v", err)
		}
		ClearUserConfigCache()
		resetSystemdDetectionCacheForTest()
		if got := GetTmuxSettings().GetLaunchInUserScope(); !got {
			t.Fatalf("EXPLICIT-OPT-OUT-RED arm3: explicit true override returned false")
		}
	})

	// Arm 4: direct *bool pointer-state assertions. Locks the decoder
	// contract at the field level so a future refactor cannot silently
	// erase the nil-vs-zero distinction. This is the W2 fix from the
	// checker review — three sub-assertions (4a/4b/4c) prove the decoder
	// honors nil/false/true at the field level, not just via the getter.
	t.Run("pointer_state_locked", func(t *testing.T) {
		// 4a: empty config → field absent → pointer MUST be nil.
		home := isolatedHomeDir(t)
		cfg := filepath.Join(home, ".agent-deck", "config.toml")
		if err := os.WriteFile(cfg, []byte(""), 0o644); err != nil {
			t.Fatalf("write empty config: %v", err)
		}
		ClearUserConfigCache()
		resetSystemdDetectionCacheForTest()
		settings := GetTmuxSettings()
		if settings.LaunchInUserScope != nil {
			t.Fatalf("EXPLICIT-OPT-OUT-RED arm4a: expected nil pointer for absent field, got non-nil pointing to %v", *settings.LaunchInUserScope)
		}

		// 4b: explicit false → pointer MUST be non-nil pointing to false.
		home = isolatedHomeDir(t)
		cfg = filepath.Join(home, ".agent-deck", "config.toml")
		if err := os.WriteFile(cfg, []byte("[tmux]\nlaunch_in_user_scope = false\n"), 0o644); err != nil {
			t.Fatalf("write explicit-false config: %v", err)
		}
		ClearUserConfigCache()
		settings = GetTmuxSettings()
		if settings.LaunchInUserScope == nil {
			t.Fatalf("EXPLICIT-OPT-OUT-RED arm4b: expected non-nil pointer for explicit false, got nil")
		}
		if *settings.LaunchInUserScope != false {
			t.Fatalf("EXPLICIT-OPT-OUT-RED arm4b: expected *false, got *%v", *settings.LaunchInUserScope)
		}

		// 4c: explicit true → pointer MUST be non-nil pointing to true.
		home = isolatedHomeDir(t)
		cfg = filepath.Join(home, ".agent-deck", "config.toml")
		if err := os.WriteFile(cfg, []byte("[tmux]\nlaunch_in_user_scope = true\n"), 0o644); err != nil {
			t.Fatalf("write explicit-true config: %v", err)
		}
		ClearUserConfigCache()
		settings = GetTmuxSettings()
		if settings.LaunchInUserScope == nil {
			t.Fatalf("EXPLICIT-OPT-OUT-RED arm4c: expected non-nil pointer for explicit true, got nil")
		}
		if *settings.LaunchInUserScope != true {
			t.Fatalf("EXPLICIT-OPT-OUT-RED arm4c: expected *true, got *%v", *settings.LaunchInUserScope)
		}
	})
}

// TestPersistence_CustomCommandResumesFromLatestJSONL pins REQ-7 / TEST-09.
// It models the 2026-04-15 conductor incident: an Instance launched via a
// custom wrapper script (inst.Command != "") has never had a ClaudeSessionID
// bound to it (agent-deck side), but Claude Code has written one or more
// JSONL transcripts under ~/.claude/projects/<encoded>/. On Start(), the
// latest JSONL by mtime MUST be discovered, its UUID MUST be written back
// into inst.ClaudeSessionID before spawn, and the spawned claude argv MUST
// contain --resume <that-uuid>. With two JSONLs of different mtimes, newer
// wins. If no JSONL exists, Start() falls through to fresh-session (no
// --resume, no error).
//
// RED today: Start()'s empty-ID claude-compatible branch at
// internal/session/instance.go:1895-1901 calls buildClaudeCommand which
// MINTS a new UUID. No disk scan happens. Phase 5 plan 05-02 adds a
// discoverLatestClaudeJSONL helper in claude.go + wires the prelude into
// Start() and StartWithMessage().
// writeCustomWrapperScript stages a functional custom-wrapper shell script at
// <home>/bin/my-wrapper.sh. The script writes a sentinel line to
// AGENTDECK_TEST_ARGV_LOG so readCapturedClaudeArgv observes non-empty output
// in the RED-state spawn path (where buildClaudeCommand(i.Command) returns the
// wrapper path verbatim and the config.toml [claude] command override is NOT
// consulted — see instance.go:485-597). Without this, the tmux pane would
// exec a non-existent file, die immediately, and readCapturedClaudeArgv would
// time out with a generic "stub claude was never spawned" message instead of
// our targeted TEST-09 RED diagnostic.
//
// This is a deviation from plan 05-01's "the file need not exist" claim;
// see 05-01-SUMMARY.md for rationale. The wrapper is only invoked in the RED
// (pre-fix) dispatch path and in the no-JSONL sub-case; the GREEN path routes
// through buildClaudeResumeCommand which uses GetClaudeCommand() directly.
func writeCustomWrapperScript(t *testing.T, home string) string {
	t.Helper()
	binDir := filepath.Join(home, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("writeCustomWrapperScript: mkdir binDir: %v", err)
	}
	wrapperPath := filepath.Join(binDir, "my-wrapper.sh")
	script := "#!/usr/bin/env bash\n" +
		"printf 'wrapper_invoked\\n' >> \"${AGENTDECK_TEST_ARGV_LOG:-/dev/null}\"\n" +
		"printf '%s\\n' \"$@\" >> \"${AGENTDECK_TEST_ARGV_LOG:-/dev/null}\"\n" +
		"sleep 30\n"
	if err := os.WriteFile(wrapperPath, []byte(script), 0o755); err != nil {
		t.Fatalf("writeCustomWrapperScript: write: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(wrapperPath) })
	return wrapperPath
}

func TestPersistence_CustomCommandResumesFromLatestJSONL(t *testing.T) {
	requireTmux(t)
	home := isolatedHomeDir(t)
	argvLog := setupStubClaudeOnPATH(t, home)
	inst := newClaudeInstanceForDispatch(t, home)

	// REQ-7 / D-04 preconditions: custom Command (non-empty), empty ClaudeSessionID.
	// The wrapper is a functional script that emits a sentinel to argvLog; see
	// writeCustomWrapperScript for rationale (deviation from plan 05-01).
	inst.Command = writeCustomWrapperScript(t, home)
	inst.ClaudeSessionID = ""
	// Simulate a restart scenario: session was previously started (non-zero
	// ClaudeDetectedAt) but lost its session ID. Without this, the #608 fix
	// would skip JSONL discovery for what is actually a restart-recovery case.
	inst.ClaudeDetectedAt = time.Now().Add(-1 * time.Hour)

	const (
		olderUUID = "11111111-1111-1111-1111-111111111111"
		newerUUID = "22222222-2222-2222-2222-222222222222"
	)
	projectDir := filepath.Join(home, ".claude", "projects", ConvertToClaudeDirName(inst.ProjectPath))
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir projectDir: %v", err)
	}
	writeJSONL := func(uuid string, mtime time.Time) string {
		p := filepath.Join(projectDir, uuid+".jsonl")
		body := []byte(`{"sessionId":"` + uuid + `","role":"user","content":"hi"}` + "\n")
		if err := os.WriteFile(p, body, 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatalf("chtimes %s: %v", p, err)
		}
		t.Cleanup(func() { _ = os.Remove(p) })
		return p
	}
	now := time.Now()
	writeJSONL(olderUUID, now.Add(-30*time.Second))
	writeJSONL(newerUUID, now)

	if err := inst.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// PERSIST-12 write-through check fires FIRST so the RED diagnostic is
	// unambiguous even when the pre-fix dispatch never invokes the stub.
	if inst.ClaudeSessionID != newerUUID {
		t.Fatalf("TEST-09 PERSIST-12 RED: after Start() with Command=%q, empty ClaudeSessionID, and TWO JSONLs (%s older, %s newer) under %s, inst.ClaudeSessionID=%q, want %q (newer JSONL UUID). The Phase 5 helper must mutate i.ClaudeSessionID before spawn so subsequent Restart() takes the Phase 3 fast path. This is the 2026-04-15 incident REQ-7 root cause: Start()'s empty-ID branch at instance.go:1895-1901 dispatches through buildClaudeCommand (fresh UUID) instead of discovering the newest JSONL on disk.", inst.Command, olderUUID, newerUUID, projectDir, inst.ClaudeSessionID, newerUUID)
	}

	argv := readCapturedClaudeArgv(t, argvLog, 3*time.Second)
	joined := strings.Join(argv, " ")

	if !strings.Contains(joined, "--resume") || !strings.Contains(joined, newerUUID) {
		t.Fatalf("TEST-09 RED: after inst.Start() captured claude argv MUST contain '--resume %s'. Got argv: %v. Phase 5 plan 05-02 must route empty-ID Claude-compatible Starts through buildClaudeResumeCommand (via discoverLatestClaudeJSONL write-through).", newerUUID, argv)
	}
	if strings.Contains(joined, olderUUID) {
		t.Fatalf("TEST-09 RED: claude argv contains the OLDER JSONL uuid %s; newer %s must win on mtime. Argv: %v", olderUUID, newerUUID, argv)
	}

	// PERSIST-13 fresh-fallback: no JSONL → no --resume, no error. This sub-case
	// exercises the graceful-fallback contract; the custom wrapper runs (no
	// discovery hit → dispatch to buildClaudeCommand(i.Command)) and MUST NOT
	// receive a --resume flag.
	t.Run("no_jsonl_falls_through_to_fresh", func(t *testing.T) {
		home2 := isolatedHomeDir(t)
		argvLog2 := setupStubClaudeOnPATH(t, home2)
		inst2 := newClaudeInstanceForDispatch(t, home2)
		inst2.Command = writeCustomWrapperScript(t, home2)
		inst2.ClaudeSessionID = ""
		// Deliberately stage no JSONL.
		if err := inst2.Start(); err != nil {
			t.Fatalf("Start: %v", err)
		}
		argv2 := readCapturedClaudeArgv(t, argvLog2, 3*time.Second)
		joined2 := strings.Join(argv2, " ")
		if strings.Contains(joined2, "--resume") {
			t.Fatalf("TEST-09 PERSIST-13: Start() with no JSONL must not pass --resume. Argv: %v", argv2)
		}
		if inst2.ClaudeSessionID != "" {
			t.Fatalf("TEST-09 PERSIST-13: Start() with no JSONL must leave ClaudeSessionID empty. Got %q", inst2.ClaudeSessionID)
		}
	})
}

// TestPersistence_DiscoverLatestClaudeJSONL_Unit is a host-portable,
// tmux-free unit test for the pure Phase 5 helper discoverLatestClaudeJSONL
// (internal/session/claude.go). It complements
// TestPersistence_CustomCommandResumesFromLatestJSONL by locking the helper's
// filename-selection rules independently of the Start() dispatch path.
//
// This test runs on every host (macOS, Linux, WSL) — no external dependency.
func TestPersistence_DiscoverLatestClaudeJSONL_Unit(t *testing.T) {
	const projectPath = "/fake/project/for-unit-test"

	stage := func(t *testing.T, home, name string, mtime time.Time) {
		t.Helper()
		dir := filepath.Join(home, ".claude", "projects", ConvertToClaudeDirName(projectPath))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(`{"sessionId":"x"}`+"\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatalf("chtimes %s: %v", p, err)
		}
	}

	t.Run("newest_wins_on_mtime", func(t *testing.T) {
		home := isolatedHomeDir(t)
		t.Setenv("CLAUDE_CONFIG_DIR", "")
		now := time.Now()
		stage(t, home, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa.jsonl", now.Add(-30*time.Second))
		stage(t, home, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb.jsonl", now)
		got, found := discoverLatestClaudeJSONL(projectPath)
		if !found {
			t.Fatalf("newest_wins_on_mtime: found=false, want true")
		}
		if got != "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb" {
			t.Fatalf("newest_wins_on_mtime: got %q, want bbbbbbbb-...", got)
		}
	})

	t.Run("agent_prefix_skipped", func(t *testing.T) {
		home := isolatedHomeDir(t)
		t.Setenv("CLAUDE_CONFIG_DIR", "")
		now := time.Now()
		stage(t, home, "agent-cccccccc-cccc-cccc-cccc-cccccccccccc.jsonl", now)
		stage(t, home, "dddddddd-dddd-dddd-dddd-dddddddddddd.jsonl", now.Add(-30*time.Second))
		got, found := discoverLatestClaudeJSONL(projectPath)
		if !found {
			t.Fatalf("agent_prefix_skipped: found=false, want true")
		}
		if got != "dddddddd-dddd-dddd-dddd-dddddddddddd" {
			t.Fatalf("agent_prefix_skipped: got %q, want dddddddd-... (agent-* must be skipped even when newer)", got)
		}
	})

	t.Run("non_uuid_skipped", func(t *testing.T) {
		home := isolatedHomeDir(t)
		t.Setenv("CLAUDE_CONFIG_DIR", "")
		now := time.Now()
		stage(t, home, "not-a-uuid.jsonl", now)
		stage(t, home, "random.jsonl", now)
		stage(t, home, "eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee.jsonl", now.Add(-30*time.Second))
		got, found := discoverLatestClaudeJSONL(projectPath)
		if !found {
			t.Fatalf("non_uuid_skipped: found=false, want true")
		}
		if got != "eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee" {
			t.Fatalf("non_uuid_skipped: got %q, want eeeeeeee-... (non-UUID filenames must be skipped)", got)
		}
	})

	t.Run("empty_dir", func(t *testing.T) {
		home := isolatedHomeDir(t)
		t.Setenv("CLAUDE_CONFIG_DIR", "")
		dir := filepath.Join(home, ".claude", "projects", ConvertToClaudeDirName(projectPath))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		got, found := discoverLatestClaudeJSONL(projectPath)
		if found || got != "" {
			t.Fatalf("empty_dir: got (%q, %v), want (\"\", false)", got, found)
		}
	})

	t.Run("missing_dir", func(t *testing.T) {
		_ = isolatedHomeDir(t)
		t.Setenv("CLAUDE_CONFIG_DIR", "")
		got, found := discoverLatestClaudeJSONL(projectPath)
		if found || got != "" {
			t.Fatalf("missing_dir: got (%q, %v), want (\"\", false)", got, found)
		}
	})

	t.Run("no_recency_cap", func(t *testing.T) {
		home := isolatedHomeDir(t)
		t.Setenv("CLAUDE_CONFIG_DIR", "")
		stage(t, home, "ffffffff-ffff-ffff-ffff-ffffffffffff.jsonl", time.Now().Add(-2*time.Hour))
		got, found := discoverLatestClaudeJSONL(projectPath)
		if !found {
			t.Fatalf("no_recency_cap: found=false on a 2-hour-old jsonl; helper MUST NOT have a 5-minute cap (spec D-05)")
		}
		if got != "ffffffff-ffff-ffff-ffff-ffffffffffff" {
			t.Fatalf("no_recency_cap: got %q, want ffffffff-...", got)
		}
	})
}

// TestEnsureClaudeSessionIDFromDisk_NewSessionSkipsDiscovery verifies that a
// brand-new session (ClaudeDetectedAt is zero) does NOT inherit another
// session's JSONL via disk discovery. This is the regression test for
// https://github.com/asheshgoplani/agent-deck/issues/608
//
// Scenario: directory already has a JSONL from session A. User creates session
// B in the same directory. Session B should start fresh, not resume A's
// conversation.
func TestEnsureClaudeSessionIDFromDisk_NewSessionSkipsDiscovery(t *testing.T) {
	const projectPath = "/fake/project/issue-608"
	const existingUUID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"

	home := isolatedHomeDir(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	// Stage a JSONL from an existing session in this directory.
	dir := filepath.Join(home, ".claude", "projects", ConvertToClaudeDirName(projectPath))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	p := filepath.Join(dir, existingUUID+".jsonl")
	if err := os.WriteFile(p, []byte(`{"sessionId":"`+existingUUID+`"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write jsonl: %v", err)
	}

	// Create a brand-new instance: empty ClaudeSessionID, zero ClaudeDetectedAt.
	inst := &Instance{
		ID:               "test-new-session",
		ProjectPath:      projectPath,
		Tool:             "claude",
		ClaudeSessionID:  "",
		ClaudeDetectedAt: time.Time{}, // zero = never started before
	}

	inst.ensureClaudeSessionIDFromDisk()

	if inst.ClaudeSessionID != "" {
		t.Fatalf("Issue #608: brand-new session (ClaudeDetectedAt=zero) got "+
			"ClaudeSessionID=%q from disk discovery. Want empty string. "+
			"New sessions must NOT inherit another session's conversation.",
			inst.ClaudeSessionID)
	}
}

// TestEnsureClaudeSessionIDFromDisk_RestartDoesDiscovery verifies that a
// restarting session (ClaudeDetectedAt is non-zero, but ClaudeSessionID was
// lost) DOES discover the JSONL from disk. This is the restart-recovery case
// that REQ-7 / v1.5.2 fixed — must not regress.
func TestEnsureClaudeSessionIDFromDisk_RestartDoesDiscovery(t *testing.T) {
	const projectPath = "/fake/project/restart-recovery"
	const existingUUID = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"

	home := isolatedHomeDir(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	// Stage a JSONL from this session's previous run.
	dir := filepath.Join(home, ".claude", "projects", ConvertToClaudeDirName(projectPath))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	p := filepath.Join(dir, existingUUID+".jsonl")
	if err := os.WriteFile(p, []byte(`{"sessionId":"`+existingUUID+`"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write jsonl: %v", err)
	}

	// Create a restarting instance: empty ClaudeSessionID, but non-zero
	// ClaudeDetectedAt (it previously had a conversation).
	inst := &Instance{
		ID:               "test-restart-session",
		ProjectPath:      projectPath,
		Tool:             "claude",
		ClaudeSessionID:  "",
		ClaudeDetectedAt: time.Now().Add(-1 * time.Hour), // was running before
	}

	inst.ensureClaudeSessionIDFromDisk()

	if inst.ClaudeSessionID != existingUUID {
		t.Fatalf("Restart recovery broken: session with ClaudeDetectedAt set "+
			"should discover JSONL. Got ClaudeSessionID=%q, want %q.",
			inst.ClaudeSessionID, existingUUID)
	}
}

// TestPersistence_PluginsSurviveRestart locks the RFC PLUGIN_ATTACH.md §2
// invariant: an Instance with a populated Plugins list MUST replay its
// enabledPlugins overlay on every spawn. The contract is "Plugins persist
// across restart and re-apply on the next worker-scratch creation."
//
// This test exercises the full persistence cycle without requiring a real
// tmux session (which is independently covered by
// TestPersistence_RestartResumesConversation). The cycle:
//
//  1. Construct an Instance with Plugins=["octopus"] under a HOME with a
//     valid catalog containing octopus.
//  2. Call EnsureWorkerScratchConfigDir → assert scratch settings.json
//     contains enabledPlugins["octopus@nyldn/claude-octopus"] = true.
//  3. Persist via state.db: MarshalToolData → UnmarshalToolData → assert
//     the unmarshalled Plugins matches the original list.
//  4. Construct a "reloaded" Instance from the unmarshalled data, call
//     Ensure again on a fresh scratch dir → assert enabledPlugins still
//     reflects the same overlay (i.e., Restart's worker-scratch path
//     re-applies the persisted Plugins).
//
// Mandate: CLAUDE.md:13-31 lists internal/session/{instance,userconfig,storage*}.go
// as touched paths for plugin attach. RFC §2/§8.1 explicitly committed
// to this test. Removing it requires an RFC.
func TestPersistence_PluginsSurviveRestart(t *testing.T) {
	home := isolatedHomeDir(t)

	// Catalog with a non-channel-emitting plugin (channel auto-link is
	// covered separately in plugin_channels_test.go; this test focuses on
	// the persistence/scratch-replay invariant).
	catalogPath := filepath.Join(home, ".agent-deck", "config.toml")
	if err := os.WriteFile(catalogPath, []byte(`
[plugins.octopus]
name = "octopus"
source = "nyldn/claude-octopus"
emits_channel = false
auto_install = false
`), 0o600); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
	ClearUserConfigCache()

	// Source profile dir for the scratch's symlink mirror.
	sourceProfile := filepath.Join(home, ".claude")
	if err := os.MkdirAll(sourceProfile, 0o700); err != nil {
		t.Fatalf("mkdir source profile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceProfile, "settings.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write source settings: %v", err)
	}

	// Phase 1: original instance writes scratch settings.json with the
	// allow-list overlay.
	original := &Instance{
		ID:      "11111111-1111-1111-1111-111111111111",
		Tool:    "claude",
		Title:   "persist-test",
		Plugins: []string{"octopus"},
	}

	scratch1, err := original.EnsureWorkerScratchConfigDir(sourceProfile)
	if err != nil {
		t.Fatalf("first Ensure: %v", err)
	}
	if scratch1 == "" {
		t.Fatal("first Ensure must create scratch dir for non-empty Plugins")
	}
	assertScratchHasOctopus := func(scratchDir, phase string) {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(scratchDir, "settings.json"))
		if err != nil {
			t.Fatalf("[%s] read scratch settings.json: %v", phase, err)
		}
		var parsed map[string]interface{}
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatalf("[%s] unmarshal scratch settings: %v", phase, err)
		}
		plugins, _ := parsed["enabledPlugins"].(map[string]interface{})
		if plugins == nil {
			t.Fatalf("[%s] scratch settings missing enabledPlugins block: %s", phase, string(data))
		}
		if v, ok := plugins["octopus@nyldn/claude-octopus"]; !ok || v != true {
			t.Fatalf("[%s] enabledPlugins[octopus@...] must be true; got %v (full block: %v)", phase, plugins["octopus@nyldn/claude-octopus"], plugins)
		}
	}
	assertScratchHasOctopus(scratch1, "first-spawn")

	// Phase 2: state.db round-trip. Marshal the instance the same way
	// storage.go does, then Unmarshal — this is the exact bytes-on-disk
	// path that survives a process restart.
	marshalled := statedb.MarshalToolData(
		original.ClaudeSessionID, original.ClaudeDetectedAt,
		original.GeminiSessionID, original.GeminiDetectedAt,
		original.GeminiYoloMode, original.GeminiModel,
		original.OpenCodeSessionID, original.OpenCodeDetectedAt,
		original.CodexSessionID, original.CodexDetectedAt,
		original.LatestPrompt, original.Notes, original.LoadedMCPNames,
		original.ToolOptionsJSON,
		nil, original.SandboxContainer, // sandbox JSON nil — not needed for plugins persistence
		original.SSHHost, original.SSHRemotePath,
		original.MultiRepoEnabled, original.AdditionalPaths,
		original.MultiRepoTempDir, nil,
		original.Channels,
		original.ExtraArgs,
		original.Plugins,
		original.PluginChannelLinkDisabled,
		original.AutoLinkedChannels,
		original.Color,
	)
	_, _, _, _, _, _, _, _, _, _, _, _, _, _, _, _, _, _,
		_, _, _, _, _, _, restoredPlugins, restoredLinkDisabled, _, _ := statedb.UnmarshalToolData(marshalled)
	if !reflect.DeepEqual(restoredPlugins, []string{"octopus"}) {
		t.Fatalf("state.db round-trip: Plugins = %v, want [octopus]", restoredPlugins)
	}
	if restoredLinkDisabled != false {
		t.Fatalf("state.db round-trip: PluginChannelLinkDisabled = %v, want false", restoredLinkDisabled)
	}

	// Phase 3: reconstruct the instance from persisted bytes and re-Ensure.
	// This models a session reload after a process restart — the scratch
	// dir is recreated under the same instance ID, with the same Plugins.
	reloaded := &Instance{
		ID:                        original.ID,
		Tool:                      original.Tool,
		Title:                     original.Title,
		Plugins:                   restoredPlugins,
		PluginChannelLinkDisabled: restoredLinkDisabled,
	}
	scratch2, err := reloaded.EnsureWorkerScratchConfigDir(sourceProfile)
	if err != nil {
		t.Fatalf("re-Ensure after restart: %v", err)
	}
	if scratch2 == "" {
		t.Fatal("re-Ensure must produce scratch dir for reloaded instance with non-empty Plugins")
	}
	if scratch2 != scratch1 {
		t.Fatalf("scratch dir is keyed on instance ID and MUST be deterministic across restarts; got first=%q, second=%q", scratch1, scratch2)
	}
	assertScratchHasOctopus(scratch2, "post-restart")
}
