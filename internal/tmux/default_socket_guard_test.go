package tmux

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// fakeEnv builds an env lookup from a map so these tests can rehearse every
// resolution branch — including the unisolated ones — WITHOUT mutating the test
// process's TMUX_TMPDIR. Mutating it process-wide to simulate a leak is itself a
// way to cause one, and it would race any concurrently running test in this
// package that spawns tmux.
func fakeEnv(kv map[string]string) func(string) string {
	return func(key string) string { return kv[key] }
}

const guardUID = 501

func defaultSocketFor(uid int) string {
	return filepath.Join("/tmp", fmt.Sprintf("tmux-%d", uid), "default")
}

func TestResolveTmuxSocketPath(t *testing.T) {
	isolated := "/tmp/ad-tmux-abc123"

	cases := []struct {
		name       string
		env        map[string]string
		socketName string
		args       []string
		want       string
	}{
		{
			name: "no selector, nothing set: the user's default socket",
			env:  nil,
			args: []string{"list-sessions"},
			want: defaultSocketFor(guardUID),
		},
		{
			name: "no selector, TMUX set: the pane's server",
			env:  map[string]string{"TMUX": "/private/tmp/tmux-501/default,4242,0"},
			args: []string{"list-sessions"},
			want: "/private/tmp/tmux-501/default",
		},
		{
			name: "isolated TMUX_TMPDIR redirects the default name",
			env:  map[string]string{"TMUX_TMPDIR": isolated},
			args: []string{"list-sessions"},
			want: filepath.Join(isolated, "tmux-501", "default"),
		},
		{
			name:       "factory socketName becomes -L, resolved under the base",
			env:        map[string]string{"TMUX_TMPDIR": isolated},
			socketName: "adeck",
			args:       []string{"list-sessions"},
			want:       filepath.Join(isolated, "tmux-501", "adeck"),
		},
		{
			name: "explicit -L in args wins over an empty socketName",
			env:  nil,
			args: []string{"-L", "ad1031-cafe", "list-sessions"},
			want: filepath.Join("/tmp", "tmux-501", "ad1031-cafe"),
		},
		{
			name: "explicit -S path wins over everything",
			env:  map[string]string{"TMUX": "/tmp/tmux-501/default,1,0", "TMUX_TMPDIR": isolated},
			args: []string{"-S", "/tmp/ad-sock-xyz/s", "list-sessions"},
			want: "/tmp/ad-sock-xyz/s",
		},
		{
			name: "an -S that belongs to the COMMAND is not a socket path",
			env:  map[string]string{"TMUX_TMPDIR": isolated},
			args: []string{"capture-pane", "-t", "sess", "-p", "-e", "-S", "-2000"},
			want: filepath.Join(isolated, "tmux-501", "default"),
		},
		{
			name: "leading -C does not shadow the command boundary",
			env:  map[string]string{"TMUX_TMPDIR": isolated},
			args: []string{"-C", "attach-session", "-t", "sess"},
			want: filepath.Join(isolated, "tmux-501", "default"),
		},
		// tmux parses its global flags with getopt(3), so every separated form
		// below has an attached and/or bundled spelling that means exactly the
		// same thing to tmux. Verified live on tmux 3.0a: `tmux -Lfoo ls`,
		// `tmux -CLfoo ls` and `tmux -uLfoo ls` all report the same
		// "error connecting to /tmp/tmux-1000/foo". A resolver that only
		// understands `-L foo` reads all three as "no socket selector" and
		// answers with the DEFAULT socket — the wrong server.
		{
			name: "attached -L value",
			env:  nil,
			args: []string{"-Lad1031-cafe", "list-sessions"},
			want: filepath.Join("/tmp", "tmux-501", "ad1031-cafe"),
		},
		{
			name: "attached -S value",
			env:  map[string]string{"TMUX_TMPDIR": isolated},
			args: []string{"-S/tmp/ad-sock-xyz/s", "list-sessions"},
			want: "/tmp/ad-sock-xyz/s",
		},
		{
			name: "boolean flags bundled ahead of an attached -L",
			env:  nil,
			args: []string{"-CLad1031-cafe", "attach-session", "-t", "sess"},
			want: filepath.Join("/tmp", "tmux-501", "ad1031-cafe"),
		},
		{
			name: "boolean flags bundled ahead of a separated -L",
			env:  nil,
			args: []string{"-uL", "ad1031-cafe", "list-sessions"},
			want: filepath.Join("/tmp", "tmux-501", "ad1031-cafe"),
		},
		{
			// The command boundary still rules: capture-pane's own -S means
			// "start line". Its attached spelling must stay invisible here for
			// the same reason the separated one is.
			name: "an attached -S that belongs to the COMMAND is not a socket path",
			env:  map[string]string{"TMUX_TMPDIR": isolated},
			args: []string{"capture-pane", "-t", "sess", "-p", "-e", "-S-2000"},
			want: filepath.Join(isolated, "tmux-501", "default"),
		},
		// getopt assigns as it goes, so a repeated flag ends up holding its LAST
		// value. Verified live on tmux 3.0a: `tmux -L zzza1 -L zzzb2 ls` reports
		// "error connecting to /tmp/tmux-1000/zzzb2", and the same for -S.
		// Answering with the first value names a server tmux will not talk to.
		{
			name: "the LAST of a repeated -L wins",
			env:  nil,
			args: []string{"-L", "first-name", "-L", "second-name", "list-sessions"},
			want: filepath.Join("/tmp", "tmux-501", "second-name"),
		},
		{
			name: "the LAST of a repeated -S wins",
			env:  map[string]string{"TMUX_TMPDIR": isolated},
			args: []string{"-S", "/tmp/first.sock", "-S", "/tmp/second.sock", "list-sessions"},
			want: "/tmp/second.sock",
		},
		{
			// `tmux -- -Lfoo ls` reports "unknown command: -Lfoo" on 3.0a: getopt
			// stopped at --, so -Lfoo is the COMMAND and the socket is the
			// default one. Reading a selector out of it invents a server.
			name: "-- ends the global flags",
			env:  map[string]string{"TMUX_TMPDIR": isolated},
			args: []string{"--", "-Lfoo", "list-sessions"},
			want: filepath.Join(isolated, "tmux-501", "default"),
		},
		{
			// `tmux -L ' zzzspace' ls` reports "error connecting to
			// /tmp/tmux-1000/ zzzspace" — the space is part of the name.
			name: "whitespace in a socket name is significant",
			env:  nil,
			args: []string{"-L", " spaced", "list-sessions"},
			want: filepath.Join("/tmp", "tmux-501", " spaced"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveTmuxSocketPath(fakeEnv(tc.env), guardUID, tc.socketName, tc.args)
			if got != tc.want {
				t.Fatalf("resolveTmuxSocketPath(%q, %v) = %q, want %q",
					tc.socketName, tc.args, got, tc.want)
			}
		})
	}
}

// TestAssertTmuxSpawnIsolated_RefusesUserDefaultSocket is the regression for the
// blind spot the older guard had: with $TMUX unset and no isolation, tmux
// resolves to the user's default server on its own, and every pre-existing
// check reported that as safe.
func TestAssertTmuxSpawnIsolated_RefusesUserDefaultSocket(t *testing.T) {
	cases := []struct {
		name       string
		env        map[string]string
		socketName string
		args       []string
	}{
		{
			name: "TMUX unset and no isolation (the blind spot)",
			env:  nil,
			args: []string{"kill-server"},
		},
		{
			name: "TMUX inherited from the user's pane",
			env:  map[string]string{"TMUX": defaultSocketFor(guardUID) + ",9,0"},
			args: []string{"list-sessions"},
		},
		{
			name: "TMUX_TMPDIR set, but to tmux's own default base",
			env:  map[string]string{"TMUX_TMPDIR": "/tmp"},
			args: []string{"new-session", "-d", "-s", "x"},
		},
		{
			name: "explicit -S at the default socket, darwin spelling",
			env:  map[string]string{"TMUX_TMPDIR": "/tmp/ad-tmux-abc"},
			args: []string{"-S", "/private/tmp/tmux-501/default", "kill-server"},
		},
		{
			name: "keysender-shaped control client on the default socket",
			env:  nil,
			args: []string{"-C", "attach-session", "-t", "agentdeck_x"},
		},
		{
			// The guard exists to keep a test binary off the user's real
			// server. An -S it cannot parse resolves to the isolated
			// TMUX_TMPDIR instead and reads as safe, so the guard waves
			// through the exact spawn it was written to refuse.
			name: "explicit -S at the default socket, attached spelling",
			env:  map[string]string{"TMUX_TMPDIR": "/tmp/ad-tmux-abc"},
			args: []string{"-S/private/tmp/tmux-501/default", "kill-server"},
		},
		{
			name: "control-mode flag bundled ahead of an attached -S at the default socket",
			env:  map[string]string{"TMUX_TMPDIR": "/tmp/ad-tmux-abc"},
			args: []string{"-CS/tmp/tmux-501/default", "attach-session", "-t", "agentdeck_x"},
		},
		{
			// The isolated -S first, the user's real server second. tmux takes
			// the LAST one, so this argv kill-servers the user's fleet while a
			// first-match parser reads the isolated path and calls it safe.
			name: "an isolated -S followed by the default socket",
			env:  map[string]string{"TMUX_TMPDIR": "/tmp/ad-tmux-abc"},
			args: []string{"-S", "/tmp/ad-sock-xyz/s", "-S", "/private/tmp/tmux-501/default", "kill-server"},
		},
		{
			name: "a name-keyed -L followed by the default socket name",
			env:  nil,
			args: []string{"-L", "ad1031-cafe", "-L", "default", "kill-server"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("assertTmuxSpawnIsolatedFor(%v) did not refuse a spawn on the "+
						"user's default tmux server — this is the condition that killed "+
						"every live session on 2026-04-17 and 2026-07-26", tc.args)
				}
				msg, ok := r.(string)
				if !ok || !strings.Contains(msg, "DEFAULT tmux server") {
					t.Fatalf("panic message does not explain the refusal: %v", r)
				}
				if !strings.Contains(msg, "IsolateTmuxSocket") {
					t.Fatalf("panic message does not tell the developer how to fix it: %v", r)
				}
			}()
			assertTmuxSpawnIsolatedFor(fakeEnv(tc.env), true, guardUID, tc.socketName, tc.args)
		})
	}
}

func TestAssertTmuxSpawnIsolated_AllowsIsolatedSockets(t *testing.T) {
	isolated := "/tmp/ad-tmux-abc123"

	cases := []struct {
		name       string
		env        map[string]string
		socketName string
		args       []string
	}{
		{
			name: "IsolateTmuxSocket-style TMUX_TMPDIR",
			env:  map[string]string{"TMUX_TMPDIR": isolated},
			args: []string{"kill-server"},
		},
		{
			name: "ShortTmuxSocket-style -S path",
			env:  nil,
			args: []string{"-S", "/tmp/ad-sock-xyz/s", "kill-server"},
		},
		{
			// A name-keyed socket in the default base is a DISTINCT server.
			// Leaking one wastes a pty (its own bug, with its own teardown);
			// it cannot end a live session, so it is not this guard's business.
			name: "name-keyed socket in the default base",
			env:  nil,
			args: []string{"-L", "ad1031-cafe", "kill-server"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("guard refused an isolated spawn (%v): %v", tc.args, r)
				}
			}()
			assertTmuxSpawnIsolatedFor(fakeEnv(tc.env), true, guardUID, tc.socketName, tc.args)
		})
	}
}

// TestAssertTmuxSpawnIsolated_RefusesLiveTmuxEnvSocket covers the server a
// developer's shell is actually inside — which may live under a custom
// TMUX_TMPDIR, entirely outside /tmp, so the shape check above cannot see it.
// A real listening socket stands in for that server here; no tmux involved, and
// nothing outside the test's own temp dir is touched.
func TestAssertTmuxSpawnIsolated_RefusesLiveTmuxEnvSocket(t *testing.T) {
	socket, cleanup := testutil.ShortTmuxSocket()
	t.Cleanup(cleanup)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Skipf("cannot create a unix socket at %q: %v", socket, err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	env := fakeEnv(map[string]string{"TMUX": socket + ",4242,0"})

	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("guard allowed a spawn against the live server named by $TMUX (%q)", socket)
		}
	}()
	assertTmuxSpawnIsolatedFor(env, true, guardUID, "", []string{"kill-server"})
}

// TestAssertTmuxSpawnIsolated_AllowsFabricatedTmuxEnvValues keeps the guard from
// refusing the many tests that set $TMUX to a made-up value to exercise parsing.
// Nothing is listening there, so nothing can be killed.
func TestAssertTmuxSpawnIsolated_AllowsFabricatedTmuxEnvValues(t *testing.T) {
	for _, raw := range []string{
		"/tmp/tmux-test.sock,12345,0",
		filepath.Join(t.TempDir(), "nope") + ",12345,0",
	} {
		t.Run(raw, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("guard refused a fabricated $TMUX value %q: %v", raw, r)
				}
			}()
			assertTmuxSpawnIsolatedFor(fakeEnv(map[string]string{
				"TMUX":        raw,
				"TMUX_TMPDIR": "/tmp/ad-tmux-abc123",
			}), true, guardUID, "", []string{"list-sessions"})
		})
	}
}

// TestAssertTmuxSpawnIsolated_NoOpOutsideTestBinaries pins the scope: the
// default socket is exactly where production belongs (the empty-socket default
// from #697), so the guard must never fire outside a test binary.
func TestAssertTmuxSpawnIsolated_NoOpOutsideTestBinaries(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("guard fired for a non-test binary: %v", r)
		}
	}()
	assertTmuxSpawnIsolatedFor(fakeEnv(nil), false, guardUID, "", []string{"kill-server"})
}

// TestTmuxExecUnderPackageIsolationDoesNotPanic is the wiring canary. The guard
// lives in the argv factory, so a false positive would not fail one test — it
// would panic every tmux-touching test in the repo. This asserts the real
// factory, under this package's real (isolated) env, builds a command bound to
// the isolated socket and refuses nothing.
func TestTmuxExecUnderPackageIsolationDoesNotPanic(t *testing.T) {
	if os.Getenv("TMUX_TMPDIR") == "" {
		t.Fatal("package TestMain did not isolate TMUX_TMPDIR — testutil.IsolateTmuxSocket() is missing")
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("tmuxExec panicked under proper package isolation: %v", r)
		}
	}()
	if cmd := tmuxExec("", "list-sessions"); cmd == nil {
		t.Fatal("tmuxExec returned nil")
	}
	if cmd := tmuxExec("adeck", "list-sessions"); cmd == nil {
		t.Fatal("tmuxExec returned nil")
	}
	// The capture-pane shape whose command-level -S must not be mistaken for a
	// socket path; if it were, this would resolve to "-2000" and pass for the
	// wrong reason, so assert the resolution too.
	args := []string{"capture-pane", "-t", "x", "-p", "-e", "-S", "-2000"}
	if cmd := tmuxExec("", args...); cmd == nil {
		t.Fatal("tmuxExec returned nil")
	}
	got := resolveTmuxSocketPath(os.Getenv, os.Getuid(), "", args)
	if want := filepath.Join(os.Getenv("TMUX_TMPDIR"), fmt.Sprintf("tmux-%d", os.Getuid()), "default"); got != want {
		t.Fatalf("capture-pane resolved to %q, want the isolated socket %q", got, want)
	}
}

func TestNormalizeTmpPathAliasesDarwinPrivateTmp(t *testing.T) {
	if got, want := normalizeTmpPath("/private/tmp/tmux-501/default"), "/tmp/tmux-501/default"; got != want {
		t.Fatalf("normalizeTmpPath = %q, want %q", got, want)
	}
	if got, want := normalizeTmpPath("/private/tmpfoo/x"), "/private/tmpfoo/x"; got != want {
		t.Fatalf("normalizeTmpPath must not rewrite an unrelated prefix: got %q, want %q", got, want)
	}
}
