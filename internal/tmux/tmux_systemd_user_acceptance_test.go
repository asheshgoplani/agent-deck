//go:build systemd_user_integration

package tmux

// This file is deliberately build-tagged. It launches real transient units and
// tmux servers, so it is enabled only by the disposable GitHub Actions
// acceptance job. Do not bootstrap a user manager from a test: the workflow
// owns that privileged/disposable-host setup.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const systemdUserAcceptanceEnv = "AGENT_DECK_SYSTEMD_USER_ACCEPTANCE"

// requireSystemdUserAcceptance makes accidental local tagged runs fail closed.
// The CI workflow sets the explicit opt-in only after it bootstraps and probes
// a disposable user manager; missing binaries, runtime paths, or D-Bus are a
// failure, never a Skip that could turn this acceptance gate green without
// running its controls.
func requireSystemdUserAcceptance(t *testing.T) {
	t.Helper()
	if os.Getenv(systemdUserAcceptanceEnv) != "1" {
		t.Fatalf("%s=1 is required; this real systemd-user fixture is enabled only by its disposable CI workflow", systemdUserAcceptanceEnv)
	}
	for _, binary := range []string{"systemd-run", "systemctl", "tmux"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Fatalf("required acceptance prerequisite %q unavailable: %v", binary, err)
		}
	}
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		t.Fatal("XDG_RUNTIME_DIR is required to address the disposable systemd user manager")
	}
	if info, err := os.Stat(runtimeDir); err != nil || !info.IsDir() {
		t.Fatalf("XDG_RUNTIME_DIR=%q is not an accessible directory: %v", runtimeDir, err)
	}
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" {
		t.Fatal("DBUS_SESSION_BUS_ADDRESS is required; workflow must deliberately export the user-manager bus")
	}
	if out, err := exec.Command("systemctl", "--user", "show-environment").CombinedOutput(); err != nil {
		t.Fatalf("systemd user manager is unavailable: %v\n%s", err, out)
	}
}

// systemdUserTMuxFixture owns a unique socket directory and exactly one
// transient unit. Cleanup addresses only that socket via -S: no PID/process
// matching, no socket-name resolution, and never a global kill-server.
type systemdUserTMuxFixture struct {
	t           *testing.T
	dir         string
	socket      string
	unit        string
	unitStopped bool
}

func newSystemdUserTMuxFixture(t *testing.T, unit string) *systemdUserTMuxFixture {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ad-systemd-user-")
	require.NoError(t, err)
	f := &systemdUserTMuxFixture{t: t, dir: dir, socket: filepath.Join(dir, "tmux.sock"), unit: unit}
	t.Cleanup(f.cleanup)
	return f
}

func (f *systemdUserTMuxFixture) command(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	// -S is authoritative, but also scrub inherited client-selection state so
	// no command can be redirected toward a caller's tmux server.
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "TMUX") {
			env = append(env, kv)
		}
	}
	cmd.Env = env
	return cmd
}

func (f *systemdUserTMuxFixture) spawnScope(t *testing.T, session string) {
	t.Helper()
	args := []string{
		"--user", "--scope", "--quiet", "--collect", "--unit", strings.TrimSuffix(f.unit, ".scope"),
		"--property=KillMode=none", "tmux", "-S", f.socket,
		"new-session", "-d", "-s", session, "sh", "-c", "exec sleep 600",
	}
	out, err := f.command("systemd-run", args...).CombinedOutput()
	require.NoErrorf(t, err, "explicit scope spawn failed: %s", out)
	f.requireKillModeNone(t)
	f.requireSession(t, session)
}

func (f *systemdUserTMuxFixture) spawnService(t *testing.T, session string) {
	t.Helper()
	args := []string{
		"--user", "--unit", f.unit, "--quiet",
		"--property=Type=forking",
		"--property=Restart=on-failure",
		"--property=RestartSec=2s",
		"--property=StartLimitBurst=10",
		"--property=StartLimitIntervalSec=60",
		"--property=KillMode=none",
		"--property=TimeoutStopSec=15s",
		"tmux", "-S", f.socket,
		"new-session", "-d", "-s", session, "sh", "-c", "exec sleep 600",
	}
	out, err := f.command("systemd-run", args...).CombinedOutput()
	require.NoErrorf(t, err, "service spawn failed: %s", out)
	f.requireKillModeNone(t)
	f.requireSession(t, session)
}

func (f *systemdUserTMuxFixture) requireKillModeNone(t *testing.T) {
	t.Helper()
	out, err := f.command("systemctl", "--user", "show", f.unit, "-p", "KillMode", "--value").CombinedOutput()
	require.NoErrorf(t, err, "could not inspect KillMode for %s: %s", f.unit, out)
	require.Equal(t, "none", strings.TrimSpace(string(out)), "real user manager must apply KillMode=none to %s", f.unit)
}

func (f *systemdUserTMuxFixture) requireSession(t *testing.T, name string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last []byte
	for time.Now().Before(deadline) {
		var err error
		last, err = f.command("tmux", "-S", f.socket, "has-session", "-t", name).CombinedOutput()
		if err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("session %q did not become reachable on exact socket %s: %s", name, f.socket, last)
}

func (f *systemdUserTMuxFixture) addSibling(t *testing.T, name string) {
	t.Helper()
	out, err := f.command("tmux", "-S", f.socket,
		"new-session", "-d", "-s", name, "sh", "-c", "exec sleep 600").CombinedOutput()
	require.NoErrorf(t, err, "sibling spawn failed on exact socket %s: %s", f.socket, out)
	f.requireSession(t, name)
}

func (f *systemdUserTMuxFixture) stopUnit(t *testing.T) {
	t.Helper()
	out, err := f.command("systemctl", "--user", "stop", f.unit).CombinedOutput()
	require.NoErrorf(t, err, "stop %s failed: %s", f.unit, out)
	f.unitStopped = true
}

func (f *systemdUserTMuxFixture) cleanup() {
	// If an assertion failed before the explicit stop, still retire only this
	// exact transient unit. Continue to exact-socket teardown even if systemd
	// reports an error so the server is never stranded.
	if !f.unitStopped {
		if out, err := f.command("systemctl", "--user", "stop", f.unit).CombinedOutput(); err != nil {
			f.t.Errorf("cleanup stop %s: %v: %s", f.unit, err, out)
		}
	}

	// KILL BEFORE REMOVE. A failure is reported and the directory is preserved:
	// unlinking it after a failed kill-server would strand a live tmux server.
	if out, err := f.command("tmux", "-S", f.socket, "kill-server").CombinedOutput(); err != nil {
		f.t.Errorf("cleanup kill-server exact socket %s: %v: %s (socket directory preserved)", f.socket, err, out)
		return
	}
	if out, err := f.command("tmux", "-S", f.socket, "has-session").CombinedOutput(); err == nil {
		f.t.Errorf("cleanup kill-server reported success but exact socket %s remains reachable: %s (socket directory preserved)", f.socket, out)
		return
	}
	if out, err := f.command("systemctl", "--user", "reset-failed", f.unit).CombinedOutput(); err != nil {
		f.t.Errorf("cleanup reset-failed %s: %v: %s", f.unit, err, out)
	}
	if err := os.RemoveAll(f.dir); err != nil {
		f.t.Errorf("cleanup remove socket directory %s after successful kill-server: %v", f.dir, err)
	}
}

func acceptanceUnitBase(t *testing.T, kind string) string {
	t.Helper()
	return "agentdeck-tmux-accept-" + kind + "-" + randomServerSuffix(t)
}

// TestTmuxSystemdUser_ExplicitScope_... proves that a real user manager
// accepts and applies KillMode=none to the explicit scope form. Stopping the
// per-session scope must leave the owned and sibling sessions on the shared
// exact socket alive.
func TestTmuxSystemdUser_ExplicitScope_KillModeNonePreservesSharedServer(t *testing.T) {
	requireSystemdUserAcceptance(t)
	base := acceptanceUnitBase(t, "scope")
	f := newSystemdUserTMuxFixture(t, base+".scope")
	owner, sibling := "scope-owner", "scope-sibling"
	f.spawnScope(t, owner)
	f.addSibling(t, sibling)
	f.stopUnit(t)
	f.requireSession(t, owner)
	f.requireSession(t, sibling)
}

// TestTmuxSystemdUser_Service_... covers the service form independently so a
// passing scope test cannot mask a service property rejection or downgrade.
func TestTmuxSystemdUser_Service_KillModeNonePreservesSharedServer(t *testing.T) {
	requireSystemdUserAcceptance(t)
	base := acceptanceUnitBase(t, "service")
	f := newSystemdUserTMuxFixture(t, base+".service")
	owner, sibling := "service-owner", "service-sibling"
	f.spawnService(t, owner)
	f.addSibling(t, sibling)
	f.stopUnit(t)
	f.requireSession(t, owner)
	f.requireSession(t, sibling)
}

// TestTmuxSystemdUser_ServiceFallbackScope_... forces only the first service
// invocation to fail at the launcher seam. Start then executes its real scope
// retry against the real manager; the test inspects its applied property and
// proves stopping that fallback scope cannot kill a sibling.
func TestTmuxSystemdUser_ServiceFallbackScope_KillModeNonePreservesSharedServer(t *testing.T) {
	requireSystemdUserAcceptance(t)

	base := acceptanceUnitBase(t, "fallback")
	dir, err := os.MkdirTemp("/tmp", "ad-systemd-fallback-")
	require.NoError(t, err)
	serverName := "fallback-" + randomServerSuffix(t)
	socket := filepath.Join(dir, "tmux-"+strconv.Itoa(os.Getuid()), serverName)
	// The fallback must exercise Start's real service→scope branch. Its -L
	// server is still isolated because this test gives it a private TMUX_TMPDIR;
	// cleanup resolves the resulting socket by this exact absolute path.
	// systemd-run executes through the manager, which has its own environment.
	// Set the private socket base on both sides so Start's -L probe and the
	// manager's spawned tmux resolve to this fixture's one exact socket.
	out, err := exec.Command("systemctl", "--user", "set-environment", "TMUX_TMPDIR="+dir).CombinedOutput()
	require.NoErrorf(t, err, "set private manager TMUX_TMPDIR failed: %s", out)
	t.Cleanup(func() {
		if output, unsetErr := exec.Command("systemctl", "--user", "unset-environment", "TMUX_TMPDIR").CombinedOutput(); unsetErr != nil {
			t.Errorf("cleanup unset manager TMUX_TMPDIR: %v: %s", unsetErr, output)
		}
	})
	t.Setenv("TMUX_TMPDIR", dir)
	f := &systemdUserTMuxFixture{t: t, dir: dir, socket: socket, unit: base + ".scope"}
	t.Cleanup(f.cleanup)

	originalExec := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		if name == "systemd-run" && containsServiceUnitFlag(args) {
			return exec.Command("false")
		}
		return originalExec(name, args...)
	}
	t.Cleanup(func() { execCommand = originalExec })

	s := NewSession("accept-fallback-"+randomServerSuffix(t), "/tmp")
	s.SocketName = serverName
	s.LaunchAs = "service"
	require.NoError(t, s.Start(""), "forced service failure must recover via real scope fallback")
	f.unit = serviceUnitBase(s.Name) + ".scope"
	f.requireKillModeNone(t)
	f.requireSession(t, s.Name)
	f.addSibling(t, "fallback-sibling")
	f.stopUnit(t)
	f.requireSession(t, s.Name)
	f.requireSession(t, "fallback-sibling")
}
