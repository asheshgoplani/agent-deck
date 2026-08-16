package tmux

// Regression coverage for the keysender leak that caused two incidents.
//
// OpenKeySender used to spawn a BARE `tmux -C`. A control-mode client with no
// command falls back to `new-session`, so every insert-mode entry minted an
// extra session with a live shell pane holding a pty, and Close() — which only
// killed the client — left it behind:
//
//   - 2026-07-18: ~26 such orphans plus leaked test servers held 507/511 ptys.
//     Every attach on the machine failed with "Device not configured".
//   - 2026-07-26: on macOS a process keeps its original argv, so the default
//     socket's server, auto-started by one of these clients, was ITSELF named
//     exactly "tmux -C". The hourly reaper matched `pgrep -fx "tmux -C"`, hit
//     the main server, and all ~65 live agent-deck sessions died at once.
//
// The fix is an explicit `attach-session -t <target>`: nothing is created, and
// nothing carries the bare argv. These tests pin both halves. They fail on the
// pre-fix implementation — the first on the leftover session, the second on the
// argv.

import (
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// listSessionNames returns the sorted session names on the isolated socket.
func listSessionNames(t *testing.T, socket string) []string {
	t.Helper()
	out, err := exec.Command("tmux", "-L", socket, "list-sessions", "-F", "#{session_name}").CombinedOutput()
	if err != nil {
		t.Fatalf("list-sessions: %v: %s", err, out)
	}
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			names = append(names, line)
		}
	}
	sort.Strings(names)
	return names
}

// controlClientArgv returns the full argv of every control-mode client
// currently attached to the server on `socket`, read from ps so the assertion
// sees exactly what a process-matching reaper would see.
func controlClientArgv(t *testing.T, socket string) []string {
	t.Helper()
	out, err := exec.Command("tmux", "-L", socket, "list-clients",
		"-F", "#{client_control_mode} #{client_pid}").CombinedOutput()
	if err != nil {
		t.Fatalf("list-clients: %v: %s", err, out)
	}
	var argv []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "1" {
			continue
		}
		ps, err := exec.Command("ps", "-o", "args=", "-p", fields[1]).Output()
		if err != nil {
			// The client exited between list-clients and ps; nothing to assert.
			continue
		}
		if a := strings.TrimSpace(string(ps)); a != "" {
			argv = append(argv, a)
		}
	}
	return argv
}

// TestOpenKeySender_CreatesNoImplicitSession is the 2026-07-18 regression: the
// keysender must not add a session to the server, and must not leave one
// behind after Close. Pre-fix, the bare `tmux -C` implicitly created session
// "1" (with a shell pane) on Open, and it OUTLIVED Close.
func TestOpenKeySender_CreatesNoImplicitSession(t *testing.T) {
	requireTmux(t)
	socket, target := makeIsolatedServer(t)

	before := listSessionNames(t, socket)
	if len(before) != 1 || before[0] != target {
		t.Fatalf("test setup: want exactly [%s] on the isolated server, got %v", target, before)
	}

	sender, err := OpenKeySender(socket, target)
	if err != nil {
		t.Fatalf("OpenKeySender: %v", err)
	}

	if during := listSessionNames(t, socket); !equalStrings(during, before) {
		_ = sender.Close()
		t.Fatalf("OpenKeySender created %v extra session(s): sessions went from %v to %v.\n"+
			"A control-mode client with no command falls back to new-session — pass an "+
			"explicit `attach-session -t <target>` instead (2026-07-18 pty exhaustion).",
			len(during)-len(before), before, during)
	}

	if err := sender.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if after := listSessionNames(t, socket); !equalStrings(after, before) {
		t.Fatalf("Close did not tear down everything Open spawned: sessions %v, want %v", after, before)
	}
}

// bareControlArgv matches an argv that is nothing but a control-mode tmux
// invocation, with or without the global UTF-8 flag and a socket selector:
// "tmux -C", "tmux -u -C", "tmux -u -L foo -C", "tmux -S /path -C". This is
// the shape the 2026-07-26 reaper matched with `pgrep -fx "tmux -C"` — on
// macOS a server auto-started by such a client inherits the argv and becomes
// indistinguishable from it.
//
// The optional `-u` group is load-bearing, not cosmetic: since #1867 the
// factory emits it on every parsed client, so a regression back to a bare
// control invocation would produce "tmux -u -L foo -C". Without the group this
// pattern would silently stop matching that and the guard would go green while
// guarding nothing.
var bareControlArgv = regexp.MustCompile(`^(\S*/)?tmux( -u)?( -[LS] \S+)? -C$`)

// TestOpenKeySender_ArgvIsNeverBareControlMode is the 2026-07-26 regression.
// The client this package spawns must carry its command in argv so no
// process-name match can ever confuse it — or a server wearing its argv — with
// a reapable orphan.
func TestOpenKeySender_ArgvIsNeverBareControlMode(t *testing.T) {
	requireTmux(t)
	socket, target := makeIsolatedServer(t)

	sender, err := OpenKeySender(socket, target)
	if err != nil {
		t.Fatalf("OpenKeySender: %v", err)
	}
	defer sender.Close()

	argv := controlClientArgv(t, socket)
	if len(argv) == 0 {
		t.Fatal("no control-mode client registered with the server after OpenKeySender")
	}
	for _, a := range argv {
		if bareControlArgv.MatchString(a) {
			t.Errorf("control client argv is %q — a bare control-mode invocation.\n"+
				"On macOS the server auto-started by such a client keeps this argv, so "+
				"`pgrep -fx \"tmux -C\"` reaps the MAIN server (2026-07-26 fleet death). "+
				"argv must name its command, e.g. `tmux -C attach-session -t <target>`.", a)
		}
		if !strings.Contains(a, "attach-session") {
			t.Errorf("control client argv is %q — want an explicit `attach-session -t %s`", a, target)
		}
	}
}

// TestOpenKeySender_RejectsMissingTarget pins the failure mode the explicit
// attach introduces: there is no session to attach to. Open must report it so
// the UI degrades to per-call send-keys instead of holding a dead client.
func TestOpenKeySender_RejectsMissingTarget(t *testing.T) {
	requireTmux(t)
	socket, _ := makeIsolatedServer(t)

	sender, err := OpenKeySender(socket, "no-such-session")
	if err == nil {
		_ = sender.Close()
		t.Fatal("OpenKeySender on a nonexistent target should fail so callers can fall back")
	}
	if !strings.Contains(err.Error(), "no-such-session") {
		t.Errorf("error = %v, want it to name the missing target", err)
	}
	// The failed attach must not have left a session behind either.
	if got := listSessionNames(t, socket); len(got) != 1 {
		t.Errorf("failed attach left the server at %v, want the original session only", got)
	}
}

// TestOpenKeySender_StillTypesAfterAttach guards against the obvious way to
// satisfy the tests above: attaching correctly but breaking the actual feature.
func TestOpenKeySender_StillTypesAfterAttach(t *testing.T) {
	requireTmux(t)
	socket, target := makeIsolatedServer(t)

	sender, err := OpenKeySender(socket, target)
	if err != nil {
		t.Fatalf("OpenKeySender: %v", err)
	}
	defer sender.Close()

	const want = "attached-and-typing"
	if err := sender.SendKeys(want); err != nil {
		t.Fatalf("SendKeys: %v", err)
	}
	require.Eventuallyf(t, func() bool {
		pane, err := exec.Command("tmux", "-L", socket, "capture-pane", "-t", target, "-p").Output()
		return err == nil && strings.Contains(unwrapPane(string(pane)), want)
	}, 3*time.Second, 100*time.Millisecond, "pane should show %q typed over the attached control client", want)
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
