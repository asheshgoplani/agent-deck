package testutil_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestTestMainDoesNotLeakBootstrapServer is the behavioral guard for the
// 2026-06-07 macOS pty-exhaustion incident.
//
// internal/tmux and internal/session TestMains bootstrap a detached tmux server
// (a `sleep 3600` pane = one pty) so `tmux list-sessions` succeeds for the
// lifetime of the test binary, and register `defer kill-server` to tear it down.
// Because those TestMains ended in os.Exit(code), the defer never ran — os.Exit
// does not run deferred functions — so EVERY `go test` of those packages leaked
// the server. Accumulated across runs this exhausted the pty pool
// (kern.tty.ptmx_max=511) and denied new terminals.
//
// A pure unit test of the cleanup function cannot catch this: calling cleanup()
// directly passes green while the real exit path still leaks (the "unit green,
// reality broken" trap). This test instead spawns the REAL package TestMain as a
// child process and asserts that no bootstrap server survives on the isolated
// socket it created. -count=1 forces the child to actually run; a cached result
// would execute no TestMain and leak nothing, passing falsely.
func TestTestMainDoesNotLeakBootstrapServer(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns child `go test`; skipped in -short")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not on PATH")
	}

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed — cannot locate repo root")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))

	// Both packages call bootstrapTmuxServer() and both define the fast
	// TestTmuxBootstrap_ServerIsRunning test, which still drives the full
	// TestMain exit path.
	for _, pkg := range []string{"./internal/tmux/", "./internal/session/"} {
		pkg := pkg
		t.Run(pkg, func(t *testing.T) {
			before := snapshotADTmuxDirs()

			// Bound the child run so a hung child (e.g. a stalled tmux call)
			// fails this guard with a clear message instead of pinning the
			// whole suite until the parent `go test` timeout. -timeout makes
			// the child itself fail fast; the context is the outer backstop
			// (it also covers the one-time compile).
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, "go", "test", "-count=1", "-timeout", "60s",
				"-run", "TestTmuxBootstrap_ServerIsRunning", pkg)
			cmd.Dir = repoRoot
			cmd.Env = os.Environ()
			out, err := cmd.CombinedOutput()
			if ctx.Err() == context.DeadlineExceeded {
				t.Fatalf("child `go test %s` did not finish within 2m (hung?); output:\n%s", pkg, out)
			}
			if err != nil {
				t.Fatalf("child `go test %s` failed: %v\n%s", pkg, err, out)
			}

			// CombinedOutput has returned, so the child test binary has fully
			// exited. The bootstrap tmux server is a separate daemon: if the
			// kill-server defer ran it is gone; if os.Exit skipped it, it is
			// still alive on its isolated socket.
			newDirs := diffDirs(before, snapshotADTmuxDirs())

			// Defense-in-depth: strip TMUX/TMUX_PANE from the probe/kill calls.
			// `-S <path>` already pins the socket (verified: it is NOT overridden
			// by an ambient $TMUX, unlike the bare/`-L` discovery documented in
			// tmuxenv.go), but `go test` is routinely run from inside a tmux pane,
			// so we belt-and-suspender against any tmux-version edge case ever
			// letting these calls touch the developer's live server.
			probeEnv := envWithoutTmux()

			var leaked []string
			for _, dir := range newDirs {
				for _, sock := range socketsUnder(dir) {
					list := exec.Command("tmux", "-S", sock,
						"list-sessions", "-F", "#{session_name}")
					list.Env = probeEnv
					out, err := list.Output()
					if err != nil {
						continue // server already gone — not a leak
					}
					if strings.Contains(string(out), "bootstrap") {
						leaked = append(leaked, sock)
						// Never let the guard itself leak: tear down what we found.
						kill := exec.Command("tmux", "-S", sock, "kill-server")
						kill.Env = probeEnv
						_ = kill.Run()
					}
				}
			}
			if len(leaked) > 0 {
				t.Fatalf("%s leaked %d bootstrap tmux server(s) that survived TestMain "+
					"exit (os.Exit skipped the kill-server defer): %s\n\n"+
					"Fix: route TestMain through "+
					"`func runTestMain(m *testing.M) int { defer cleanup(); return m.Run() }` "+
					"so the cleanup defers actually run.",
					pkg, len(leaked), strings.Join(leaked, ", "))
			}
		})
	}
}

// envWithoutTmux returns os.Environ() with TMUX and TMUX_PANE removed, mirroring
// what testutil.IsolateTmuxSocket does so socket-scoped tmux calls can never be
// influenced by an ambient tmux client.
func envWithoutTmux() []string {
	env := os.Environ()
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, "TMUX=") || strings.HasPrefix(kv, "TMUX_PANE=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// adTmuxBases returns the candidate base dirs where IsolateTmuxSocket creates
// its per-run TMUX_TMPDIR (shortTmuxTmpBase prefers /tmp, else os.TempDir()).
func adTmuxBases() []string {
	bases := map[string]struct{}{"/tmp": {}, os.TempDir(): {}}
	out := make([]string, 0, len(bases))
	for b := range bases {
		out = append(out, b)
	}
	return out
}

// snapshotADTmuxDirs returns the set of existing ad-tmux-* dirs across the
// candidate bases.
func snapshotADTmuxDirs() map[string]struct{} {
	set := map[string]struct{}{}
	for _, base := range adTmuxBases() {
		matches, _ := filepath.Glob(filepath.Join(base, "ad-tmux-*"))
		for _, m := range matches {
			set[m] = struct{}{}
		}
	}
	return set
}

// diffDirs returns dirs present in after but not before.
func diffDirs(before, after map[string]struct{}) []string {
	var out []string
	for d := range after {
		if _, seen := before[d]; !seen {
			out = append(out, d)
		}
	}
	return out
}

// socketsUnder returns the tmux socket paths a server would bind under an
// isolated TMUX_TMPDIR: <dir>/tmux-<uid>/<sock>.
func socketsUnder(dir string) []string {
	matches, _ := filepath.Glob(filepath.Join(dir, "tmux-*", "*"))
	return matches
}
