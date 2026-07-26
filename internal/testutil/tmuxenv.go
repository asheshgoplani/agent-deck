package testutil

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Name of the marker env var set during test isolation. Runtime guards in
// internal/tmux use this to detect a test context so they can panic loudly
// on an isolation leak instead of silently attacking the user's real
// tmux server.
const TestIsolationMarkerEnv = "AGENT_DECK_TEST_ISOLATED"

// IsolateTmuxSocket makes it safe to spawn real tmux servers from tests
// even when `go test` is invoked from inside a live tmux session (the
// default on every developer host that uses agent-deck).
//
// The helper does THREE things:
//
//  1. Unsets TMUX and TMUX_PANE. Tmux's client discovery order is:
//     `$TMUX → -S path → -L name → $TMUX_TMPDIR`. If TMUX is set, every
//     later step is ignored — so setting TMUX_TMPDIR alone provides zero
//     isolation when the test process inherits TMUX from a parent tmux
//     pane. This was the 2026-04-17 three-cascade bug: v1.7.3 set
//     TMUX_TMPDIR but left TMUX set, so every test-spawned tmux session
//     joined the user's real server and eventually destabilised it.
//
//  2. Sets TMUX_TMPDIR to a fresh per-call temp dir. Tests that use
//     `-L <name>` or `$TMUX_TMPDIR`-derived sockets will land here,
//     never at /tmp/tmux-<uid>/default.
//
//  3. Sets AGENT_DECK_TEST_ISOLATED=1. Production code paths in
//     internal/tmux read this marker at tmux-spawn time and panic with
//     a clear message if TMUX is still set and points to a non-isolated
//     socket — the "make the failure loud, not silent" belt to the
//     TMUX-unset suspender.
//
// Call this from every package-level TestMain that transitively spawns
// tmux:
//
//	func TestMain(m *testing.M) {
//	    cleanup := testutil.IsolateTmuxSocket()
//	    defer cleanup()
//	    os.Exit(m.Run())
//	}
//
// Returns a cleanup function that kills every server it spawned, removes the
// temp dir, and leaves the process ISOLATED for the rest of its life — TMUX /
// TMUX_PANE stay unset and TMUX_TMPDIR is parked somewhere unusable, so
// goroutines that outlive the suite cannot reach the developer's real tmux
// server. See the note inside the cleanup func.
func IsolateTmuxSocket() func() {
	// TMUX / TMUX_PANE are never restored; TMUX_TMPDIR is restored only when it
	// already pointed somewhere isolated. See the sticky-isolation note in the
	// returned cleanup func.
	origTmuxTmpdir, hadTmuxTmpdir := os.LookupEnv("TMUX_TMPDIR")
	origMarker, hadMarker := os.LookupEnv(TestIsolationMarkerEnv)

	// CRITICAL: unset BEFORE setting TMUX_TMPDIR. TMUX takes precedence
	// in tmux client discovery, so leaving it set makes TMUX_TMPDIR
	// ignored. This single line is the 2026-04-17 fix.
	_ = os.Unsetenv("TMUX")
	_ = os.Unsetenv("TMUX_PANE")

	dir, err := os.MkdirTemp(shortTmuxTmpBase(), "ad-tmux-")
	if err != nil {
		// If we can't isolate via MkdirTemp, we still want tests to
		// run — but we REALLY don't want them on the default socket.
		// Fall back to a PID-keyed path that won't collide with other
		// test runs or the user's real sessions.
		dir = fmt.Sprintf("/tmp/agent-deck-test-tmux-fallback-%d", os.Getpid())
		_ = os.MkdirAll(dir, 0o700)
	}
	assertIsolatedTmuxTmpdir(dir)
	_ = os.Setenv("TMUX_TMPDIR", dir)
	_ = os.Setenv(TestIsolationMarkerEnv, "1")

	return func() {
		// KILL BEFORE REMOVE. A tmux server does not die when its socket is
		// unlinked — it keeps running, keeps its panes, keeps a pty each, and
		// is now unreachable by every socket path in existence, so nothing can
		// ever reap it again. This cleanup used to RemoveAll straight away
		// under a comment claiming the kernel tidied up after the server; it
		// does not. ~50 servers accumulated that way and took the machine's
		// pty pool to 507/511 on 2026-07-18, after which no process could
		// attach to anything. See the incident log in CLAUDE.md.
		KillTmuxServersUnder(dir)

		// ISOLATION IS STICKY. Cleanup does NOT hand TMUX / TMUX_PANE /
		// TMUX_TMPDIR back, and that is deliberate.
		//
		// Cleanup runs while the test binary is still alive, and agent-deck
		// spawns background goroutines that outlive the test that started them
		// (status pollers, Instance.watchForFastDeath). Restoring the caller's
		// values re-points those late tmux calls at the developer's REAL default
		// server — the precise hazard this helper exists to prevent. It is
		// observable: a leaked watchForFastDeath goroutine was caught running
		// `tmux has-session` against /tmp/tmux-<uid>/default after its package's
		// suite had already printed PASS.
		//
		// Restoring protected nothing in exchange. A child process cannot alter
		// its parent shell's environment, so `go test` could never leak these
		// vars back to the developer's terminal; the only reader of the restored
		// values is the dying test binary itself.
		//
		// TMUX_TMPDIR is parked on a path that can never BE a directory, so a
		// late tmux call fails instantly ("no server running") instead of
		// reaching the live fleet — and cannot create a fresh server that no
		// teardown would ever reap, which parking it on the removed dir would
		// allow. Tests needing the original values back capture and restore them
		// themselves; this package's own tests do exactly that.
		//
		// The one value worth handing back is an OUTER isolated dir: a nested
		// call (a test isolating on top of its package's TestMain) must leave
		// the package's own isolation intact, and that dir is by definition
		// already safe. An outer value that is unset or points at tmux's default
		// base is exactly what must not come back.
		_ = os.Unsetenv("TMUX")
		_ = os.Unsetenv("TMUX_PANE")
		if hadTmuxTmpdir && !isDefaultTmuxBase(origTmuxTmpdir) {
			_ = os.Setenv("TMUX_TMPDIR", origTmuxTmpdir)
		} else {
			_ = os.Setenv("TMUX_TMPDIR", tornDownTmuxTmpdir)
		}
		restoreEnv(TestIsolationMarkerEnv, origMarker, hadMarker)
		_ = os.RemoveAll(dir)
	}
}

// tornDownTmuxTmpdir is where TMUX_TMPDIR points after cleanup. /dev/null is a
// character device on every supported platform, so no component beneath it can
// be created or opened: any tmux invocation resolving here fails immediately and
// spawns nothing.
const tornDownTmuxTmpdir = "/dev/null/agent-deck-tmux-isolation-torn-down"

// assertIsolatedTmuxTmpdir refuses to hand back an isolation that does not
// isolate.
//
// Every layer downstream trusts TMUX_TMPDIR to be a private directory: tests
// spawn servers under it, and cleanup runs kill-server against everything it
// finds there. Point it at tmux's own default base and both halves turn
// hostile — tests join the user's live server, and cleanup kills it. So the
// post-condition is checked here, once, at the only place that sets the value,
// and a violation stops the test binary before it can spawn anything.
//
// Panic rather than an error return: callers are TestMains that would have to
// ignore an error to stay one line long, and "isolation silently didn't happen"
// is precisely the failure mode of the 2026-04-17 cascade.
func assertIsolatedTmuxTmpdir(dir string) {
	if isDefaultTmuxBase(dir) {
		panic(fmt.Sprintf(
			"testutil.IsolateTmuxSocket: refusing to set TMUX_TMPDIR=%q — that is tmux's "+
				"DEFAULT socket base, so tests would spawn on the user's live server and "+
				"cleanup would kill-server it. The isolated dir must be a private "+
				"subdirectory (ad-tmux-*).", dir))
	}
}

// isDefaultTmuxBase reports whether dir is where tmux keeps the user's real
// sockets, rather than a private directory above them.
//
// tmux nests <TMUX_TMPDIR>/tmux-<uid>/<socket>, so a tmp root IS the default
// base, and a tmux-<uid> directory is the default base one level in. Both
// spellings of /tmp count on darwin, where /tmp is a symlink to /private/tmp.
func isDefaultTmuxBase(dir string) bool {
	clean := filepath.Clean(dir)
	for _, base := range []string{"/tmp", "/private/tmp", filepath.Clean(os.TempDir())} {
		if clean == base {
			return true
		}
	}
	return strings.HasPrefix(filepath.Base(clean), "tmux-")
}

// KillTmuxServersUnder kills every tmux server whose socket lives anywhere
// under dir, and must be called before dir is removed.
//
// Servers are addressed by absolute `-S <path>`, never `-L <name>`: a name is
// resolved against $TMUX_TMPDIR, so a caller whose env has drifted from the
// spawn's silently kills nothing and reports success. An absolute path cannot
// drift. The command's env is scrubbed of TMUX* for the same reason.
//
// Best-effort by design — it runs from cleanup paths that must not fail the
// test — but "best effort" here means every socket present gets a kill
// attempt, not that the attempt is optional.
func KillTmuxServersUnder(dir string) {
	if dir == "" {
		return
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		return
	}
	// tmux nests sockets one level down as <TMUX_TMPDIR>/tmux-<uid>/<name>;
	// ShortTmuxSocket-style callers place theirs directly in dir.
	patterns := []string{
		filepath.Join(dir, "*"),
		filepath.Join(dir, "tmux-*", "*"),
	}
	seen := make(map[string]bool)
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		for _, socket := range matches {
			if seen[socket] {
				continue
			}
			seen[socket] = true
			info, err := os.Stat(socket)
			if err != nil || info.Mode()&os.ModeSocket == 0 {
				continue
			}
			cmd := exec.Command("tmux", "-S", socket, "kill-server")
			cmd.Env = envWithoutTmuxVars()
			_ = cmd.Run()
		}
	}
}

// envWithoutTmuxVars returns the process environment with every TMUX*
// variable dropped, so a tmux invocation cannot be redirected by an inherited
// $TMUX, $TMUX_PANE, or $TMUX_TMPDIR.
func envWithoutTmuxVars() []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "TMUX") {
			continue
		}
		env = append(env, kv)
	}
	return env
}

// ShortTmuxSocket returns a tmux -S socket path under a short base dir (/tmp
// when writable, via shortTmuxTmpBase) that fits the darwin sun_path 104-byte
// limit regardless of $TMPDIR length or test name, plus a cleanup func that
// removes the dir. Use for any test that passes its own `tmux -S <path>`;
// t.TempDir() on darwin resolves under /var/folders/<hash>/T/<TestName>... and
// overshoots the limit for long test names ("File name too long").
//
//	socket, cleanup := testutil.ShortTmuxSocket()
//	t.Cleanup(cleanup)
func ShortTmuxSocket() (socket string, cleanup func()) {
	dir, err := os.MkdirTemp(shortTmuxTmpBase(), "ad-sock-")
	if err != nil {
		// Primary MkdirTemp failed. Retry directly under /tmp so each call
		// still gets a UNIQUE dir (MkdirTemp's random suffix) that fits
		// sun_path; a PID-keyed path would be process-constant and collide
		// across calls, racing one call's cleanup against another's socket.
		// A static PID path remains only as an absolute last resort.
		if dir, err = os.MkdirTemp("/tmp", "agent-deck-test-sock-"); err != nil {
			dir = fmt.Sprintf("/tmp/agent-deck-test-sock-%d", os.Getpid())
			_ = os.MkdirAll(dir, 0o700)
		}
	}
	// Same kill-before-remove rule as IsolateTmuxSocket: unlinking the socket
	// strands the server instead of ending it.
	return filepath.Join(dir, "s"), func() {
		KillTmuxServersUnder(dir)
		_ = os.RemoveAll(dir)
	}
}

// shortTmuxTmpBase returns a short base directory for the per-test TMUX_TMPDIR.
//
// UNIX-domain socket paths are capped at sockaddr_un.sun_path (104 chars on
// darwin, 108 on linux). tmux appends "/tmux-<uid>/<sock>" (~17 chars) to
// TMUX_TMPDIR, and MkdirTemp's random suffix eats ~10 more, so the base must
// stay well under ~75 chars. On darwin, os.TempDir() returns
// /var/folders/<aa>/<32-char-hash>/T (resolved through /private/...) which is
// ~56 chars and immediately overshoots the limit. /tmp is the well-known short
// path on every Unix-like OS, matches tmux's own default location, and matches
// the existing failure-mode fallback at the bottom of IsolateTmuxSocket.
//
// Returns "/tmp" when writable; otherwise returns "" so os.MkdirTemp falls
// back to os.TempDir() (preserving the prior behavior on hosts that remap
// TMPDIR but leave /tmp unwritable — rare under sandboxes/SELinux/AppArmor).
func shortTmuxTmpBase() string {
	const candidate = "/tmp"
	info, err := os.Stat(candidate)
	if err != nil || !info.IsDir() {
		return ""
	}
	probe, err := os.CreateTemp(candidate, ".ad-tmux-probe-")
	if err != nil {
		return ""
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	return candidate
}

// restoreEnv puts an env var back to its original state. If it wasn't set
// before IsolateTmuxSocket ran, unset it; otherwise set it to the original.
func restoreEnv(key, orig string, had bool) {
	if had {
		_ = os.Setenv(key, orig)
	} else {
		_ = os.Unsetenv(key)
	}
}
