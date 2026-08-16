package tmux

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// This file is the FOURTH and structural layer of tmux test isolation, and the
// only one that refuses based on where a spawn will actually land rather than
// on which env vars happen to be set.
//
// The three older layers all reason about $TMUX:
//
//  1. testutil.IsolateTmuxSocket unsets TMUX/TMUX_PANE and repoints TMUX_TMPDIR.
//  2. testutil's repo audit fails any TestMain that forgets to call it.
//  3. assertTestTmuxIsolation panics at Session.Start when $TMUX still points
//     at the user's default socket.
//
// They share a blind spot, and it is the dangerous one. tmux does not need
// $TMUX to find the user's live server: with no -S, no -L, and no TMUX_TMPDIR,
// it resolves to <tmpdir>/tmux-<uid>/default — the default socket — all on its
// own. Layer 3 explicitly treats "$TMUX is unset" as safe ("there's no
// inherited socket to leak onto") and only logs a warning. So a test binary in
// a package whose TestMain never isolated, or a test that scrubbed TMUX* from
// its own env to simulate a detached child, reaches the REAL server while every
// existing guard reports everything is fine. One `kill-server` on that path
// takes down every live session on the machine — which is the 2026-04-17 /
// 2026-07-26 fleet-death class from a different direction.
//
// The guard here closes that by mirroring tmux's own resolution and refusing
// the spawn when the resolved socket path is the user's default server. It runs
// at the argv factory (tmuxExec / tmuxExecContext), so it covers every
// socket-aware spawn in the package — the lint in tmux_exec_lint_test.go is
// what keeps call sites funnelled through there — and it fires BEFORE the
// subprocess starts, never after.
//
// There is deliberately no opt-out env var. A test that believes it needs the
// user's default server is the exact thing this refuses; give it an isolated
// socket instead.

// defaultTmuxSocketName is the socket name tmux uses when neither -L nor -S is
// given.
const defaultTmuxSocketName = "default"

// assertTmuxSpawnIsolated refuses to build a tmux command that a Go test binary
// would run against the user's real default server. It is a no-op outside test
// binaries: production is entitled to the default socket, which is the whole
// point of the empty-socket default (#697).
func assertTmuxSpawnIsolated(socketName string, args []string) {
	assertTmuxSpawnIsolatedFor(os.Getenv, looksLikeGoTestBinary(), os.Getuid(), socketName, args)
}

// assertTmuxSpawnIsolatedFor is the injectable core of assertTmuxSpawnIsolated.
// Taking the env lookup, the test-binary verdict, and the uid as parameters lets
// the guard's own tests exercise every resolution branch without mutating the
// test process's environment — mutating TMUX_TMPDIR process-wide to rehearse a
// leak is itself a way to cause one.
func assertTmuxSpawnIsolatedFor(lookupEnv func(string) string, isTestBinary bool, uid int, socketName string, args []string) {
	if !isTestBinary {
		return
	}
	socket := resolveTmuxSocketPath(lookupEnv, uid, socketName, args)
	if !isUserDefaultTmuxSocketPath(lookupEnv, uid, socket) {
		return
	}
	panic(fmt.Sprintf(
		"tmux isolation guard: test binary %q is about to run `tmux %s` against the "+
			"user's DEFAULT tmux server (socket %q). Every live tmux session on this "+
			"machine lives there, and a single kill-server on that socket ends all of "+
			"them — the 2026-04-17 and 2026-07-26 fleet-death incidents. Refusing to "+
			"spawn.\n"+
			"FIX: isolate the socket for this test. Package-wide, add "+
			"`cleanup := testutil.IsolateTmuxSocket(); defer cleanup()` to TestMain "+
			"(see internal/tmux/testmain_test.go). For one server, take a private "+
			"socket path from `testutil.ShortTmuxSocket()`. If the code under test "+
			"scrubs TMUX* from a subprocess env, give that subprocess an explicit "+
			"TMUX_TMPDIR under the isolated dir — stripping TMUX* does not isolate, "+
			"it de-isolates.",
		os.Args[0], strings.Join(args, " "), socket))
}

// resolveTmuxSocketPath returns the socket path tmux will use for a spawn, and
// mirrors tmux's own precedence:
//
//	-S <path>  >  -L <name> (or the factory's socketName)  >  $TMUX  >  default
//
// with the -L/default forms resolving under $TMUX_TMPDIR, falling back to /tmp.
func resolveTmuxSocketPath(lookupEnv func(string) string, uid int, socketName string, args []string) string {
	if path := socketPathFlag(args); path != "" {
		return filepath.Clean(path)
	}
	name := strings.TrimSpace(socketName)
	if flagName := socketNameFlag(args); flagName != "" {
		name = flagName
	}
	if name == "" {
		// No explicit socket selector at all: tmux takes the path from $TMUX
		// when it is set (i.e. when running inside a tmux pane), otherwise the
		// default socket under the tmp base.
		if envPath := socketPathFromTmuxEnv(lookupEnv("TMUX")); envPath != "" {
			return filepath.Clean(envPath)
		}
		name = defaultTmuxSocketName
	}
	base := strings.TrimSpace(lookupEnv("TMUX_TMPDIR"))
	if base == "" {
		base = "/tmp"
	}
	return filepath.Join(base, fmt.Sprintf("tmux-%d", uid), name)
}

// isUserDefaultTmuxSocketPath reports whether socket names the user's real
// default server.
//
// Only the socket literally named "default" under a tmp base counts. A
// name-keyed socket that happens to sit in the same directory (the `-L
// ad1031-*` servers the launch-race tests spawn, say) is a distinct server:
// leaking one wastes ptys, which is its own bug with its own teardown, but it
// cannot kill a live session. This guard is about the socket whose loss is
// unrecoverable.
func isUserDefaultTmuxSocketPath(lookupEnv func(string) string, uid int, socket string) bool {
	if socket == "" {
		return false
	}
	target := normalizeTmpPath(socket)
	suffix := filepath.Join(fmt.Sprintf("tmux-%d", uid), defaultTmuxSocketName)
	// Every base tmux might resolve the default socket under. os.TempDir()
	// honours $TMPDIR, which on darwin is a per-user /var/folders/... path.
	bases := []string{"/tmp", "/private/tmp", os.TempDir()}
	for _, base := range bases {
		if target == normalizeTmpPath(filepath.Join(base, suffix)) {
			return true
		}
	}
	// $TMUX names the server of the pane the test was launched from, which is a
	// live server full of the user's sessions no matter where its socket lives
	// (a user may run tmux under a custom TMUX_TMPDIR entirely outside /tmp).
	//
	// This branch requires the socket to EXIST and be a socket, because $TMUX is
	// also a value tests fabricate to exercise parsing ("/tmp/tmux-1000/default",
	// "/tmp/tmux-test.sock"). A path with no listener cannot be a live server, so
	// demanding a real socket keeps the guard from refusing those. The shape
	// check above deliberately does NOT require existence: a spawn that would
	// CREATE the user's default server must be refused too.
	if envPath := socketPathFromTmuxEnv(lookupEnv("TMUX")); envPath != "" {
		if target == normalizeTmpPath(envPath) && isExistingSocket(envPath) {
			return true
		}
	}
	return false
}

// isExistingSocket reports whether path is a Unix socket present on disk, i.e.
// whether some server could be listening there right now.
func isExistingSocket(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeSocket != 0
}

// socketPathFlag returns the value of a leading global `-S <path>` flag, in any
// of the spellings tmux's getopt accepts. See leadingFlagValue for those, and
// for why the scan stops at the tmux command.
func socketPathFlag(args []string) string {
	return leadingFlagValue(args, 'S')
}

// socketNameFlag returns the value of a leading global `-L <name>` flag. The
// factory inserts -L from its socketName argument, but a call site may also
// pass one through directly.
func socketNameFlag(args []string) string {
	return leadingFlagValue(args, 'L')
}

// tmuxValueFlags are the global flags that consume a value: `-c shell-command`,
// `-f file`, `-L socket-name`, `-S socket-path`, `-T features`. Every other
// global tmux accepts (-2 -C -l -N -q -u -v) is a boolean.
const tmuxValueFlags = "cfLST"

// leadingFlagValue finds the value of a single-letter global flag within the
// run of global flags that precedes the tmux command.
//
// tmux parses its globals with getopt(3), so one flag has three spellings that
// all mean the same thing, and the socket flags reach this function in all of
// them:
//
//	tmux -L work ls      separated
//	tmux -Lwork ls       attached
//	tmux -CLwork attach  bundled behind booleans, then attached
//
// Verified live on tmux 3.0a: all three report the same "error connecting to
// /tmp/tmux-1000/work". Matching whole argv words (`arg == "-L"`) sees only the
// first, and answers "no socket selector" for the other two — which resolves to
// the DEFAULT socket. That is the wrong server for both callers: for
// assertTmuxSpawnIsolated a spawn aimed at the user's real server reads as
// isolated, and for the orphan sweep a client that is alive on its own server
// reads as not-live. So the scan walks each word one flag character at a time.
//
// A repeated flag keeps its LAST value, because that is what getopt leaves
// behind: tmux assigns as it parses, so the second `-L` overwrites the first.
// Verified live on tmux 3.0a — `tmux -L zzza1 -L zzzb2 ls` reports "error
// connecting to /tmp/tmux-1000/zzzb2", and `-S` behaves the same. Answering
// with the FIRST value is not a harmless imprecision: it names a server tmux
// will not talk to, so `-S <isolated> -S <the user's default>` reads as an
// isolated spawn to assertTmuxSpawnIsolated while tmux kill-servers the user's
// real fleet. It is also invisible to a socket comparison, since both sides of
// that comparison would mis-read the same argv the same way and agree.
//
// Scanning stops at `--`, and at the first argument that is not a flag, i.e. at
// the tmux command itself. Both boundaries are load-bearing:
//
//   - `--` ends the globals for getopt, so `tmux -- -Lfoo ls` treats -Lfoo as
//     the COMMAND ("unknown command: -Lfoo" on 3.0a). Reading a selector out of
//     it invents a server that was never named.
//   - `-S` means "socket path" only as a global; it means "start line" to
//     capture-pane (`capture-pane -p -S -2000`, or `-S-2000`) and "status line"
//     to refresh-client. Treating a command-level -S as a socket would mistake
//     "-2000" for a socket path.
//
// Values are returned verbatim. tmux does not trim them — `tmux -L ' name' ls`
// really does resolve to a socket whose name begins with a space — and a parser
// that trims names a different server than the one the argv asks for.
func leadingFlagValue(args []string, flag byte) string {
	value := ""
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return value // getopt stops here; the rest is the command
		}
		if len(arg) < 2 || arg[0] != '-' {
			return value // the tmux command; anything after belongs to it
		}
		for j := 1; j < len(arg); j++ {
			c := arg[j]
			if !strings.ContainsRune(tmuxValueFlags, rune(c)) {
				continue // a boolean; keep reading the bundle
			}
			// Everything after the flag letter is its value; an empty
			// remainder means the value is the next word.
			v := arg[j+1:]
			if v == "" {
				if i+1 >= len(args) {
					return value
				}
				v = args[i+1]
				i++
			}
			if c == flag {
				value = v // last occurrence wins, as getopt's assignment does
			}
			// A different value flag. Its value is consumed either way, so it
			// can never be mistaken for a flag or for the command.
			break
		}
	}
	return value
}

// socketPathFromTmuxEnv extracts the socket path from a $TMUX value, which tmux
// formats as "<socket-path>,<server-pid>,<session-id>".
func socketPathFromTmuxEnv(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if comma := strings.IndexByte(raw, ','); comma >= 0 {
		raw = raw[:comma]
	}
	return strings.TrimSpace(raw)
}

// normalizeTmpPath makes darwin's two spellings of /tmp comparable. /tmp is a
// symlink to /private/tmp there, so the same socket is reachable under both and
// a plain string compare would miss half the aliases. filepath.EvalSymlinks is
// not usable here: the guard must decide before the socket exists.
func normalizeTmpPath(path string) string {
	clean := filepath.Clean(path)
	if clean == "/private/tmp" {
		return "/tmp"
	}
	// Match on the trailing separator so "/private/tmpfoo" is left alone.
	if trimmed, ok := strings.CutPrefix(clean, "/private/tmp/"); ok {
		return filepath.Clean("/tmp/" + trimmed)
	}
	return clean
}
