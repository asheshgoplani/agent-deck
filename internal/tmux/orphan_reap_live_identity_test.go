package tmux

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// --- candidateSocketName: pure parsing, no tmux server needed -------------

func TestCandidateSocketName(t *testing.T) {
	tests := []struct {
		name          string
		cmdlineFields []string
		wantName      string
		wantOK        bool
	}{
		{"no socket flag at all, default server", []string{"tmux", "list-clients", "-t", "agentdeck_x"}, "", true},
		{"-L name", []string{"tmux", "-L", "ad1031-xxx", "list-clients", "-t", "agentdeck_x"}, "ad1031-xxx", true},
		{"-C before -L", []string{"tmux", "-C", "-L", "ad1031-xxx", "attach-session", "-t", "agentdeck_x"}, "ad1031-xxx", true},
		{"-S path is unresolvable via the -L-only factory", []string{"tmux", "-S", "/tmp/some/sock", "list-clients"}, "", false},
		{"-S wins over -L per tmux's own precedence, still unresolvable", []string{"tmux", "-S", "/tmp/some/sock", "-L", "name", "list-clients"}, "", false},
		{"empty argv", []string{}, "", true},
		{"argv0 only", []string{"tmux"}, "", true},
		// getopt's attached and bundled spellings. A candidate that carries
		// one of these is talking to a socket this parser cannot see: the
		// separated-form-only reader answers "no selector", the live-identity
		// query then goes to the DEFAULT server, and a client that is alive on
		// its own server reads as not-live — the kill verdict.
		{"attached -L name", []string{"tmux", "-Lad1031-xxx", "list-clients", "-t", "agentdeck_x"}, "ad1031-xxx", true},
		{"-C bundled with an attached -L", []string{"tmux", "-CLad1031-xxx", "attach-session", "-t", "agentdeck_x"}, "ad1031-xxx", true},
		{"bundled booleans before a separated -L", []string{"tmux", "-uL", "ad1031-xxx", "list-clients"}, "ad1031-xxx", true},
		{"attached -S path is unresolvable via the -L-only factory", []string{"tmux", "-S/tmp/some/sock", "list-clients"}, "", false},
		{"-C bundled with an attached -S", []string{"tmux", "-CS/tmp/some/sock", "attach-session"}, "", false},
		// Repeated flags: tmux keeps the last. Reading the first names a server
		// the candidate is not on — and since the query side would mis-read it
		// the SAME way, the socket comparison in isLiveTmuxClientOrServer would
		// agree with itself and prove nothing.
		{"the LAST of a repeated -L wins", []string{"tmux", "-L", "first", "-L", "second", "list-clients"}, "second", true},
		{"a -S anywhere in the run is still unresolvable", []string{"tmux", "-L", "name", "-S", "/tmp/s", "list-clients"}, "", false},
		{"-- ends the global flags", []string{"tmux", "--", "-Lfoo", "list-clients"}, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			name, ok := candidateSocketName(tc.cmdlineFields)
			if name != tc.wantName || ok != tc.wantOK {
				t.Errorf("candidateSocketName(%q) = (%q, %v), want (%q, %v)",
					tc.cmdlineFields, name, ok, tc.wantName, tc.wantOK)
			}
		})
	}
}

// --- isLiveTmuxClientOrServer: real tmux server, real processes -----------
//
// These tests spawn an actual tmux server on an isolated socket (per-test -L
// name, under the package TestMain's IsolateTmuxSocket-scoped TMUX_TMPDIR) and
// exercise isLiveTmuxClientOrServer against real pids, not synthetic argv
// strings — the whole point of this function is that it does not trust argv
// text, so a test that only feeds it strings would not prove anything.

// orphanLiveTestSocket returns a deterministic -L socket name for this test
// and registers teardown that kills the server on the SAME socket — an env
// mismatch between spawn and cleanup makes kill-server a silent no-op and
// leaks a server plus its ptys (the 2026-07-18 incident class).
func orphanLiveTestSocket(t *testing.T) string {
	t.Helper()
	socket := "ad-orphlive-" + strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())
	if len(socket) > 40 {
		socket = socket[:40]
	}
	kill := func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() }
	kill() // clear anything a previously aborted run stranded
	t.Cleanup(kill)
	return socket
}

// startOrphanLiveSession creates a long-lived session on socket and disables
// exit-empty so the server outlives the session's own command for the
// duration of the test (default tmux exits the server once its last session
// closes, which would race every assertion below).
func startOrphanLiveSession(t *testing.T, socket, session string) {
	t.Helper()
	if out, err := exec.Command("tmux", "-L", socket, "new-session", "-d", "-s", session, "sleep 6000").CombinedOutput(); err != nil {
		t.Fatalf("new-session: %v: %s", err, out)
	}
	if out, err := exec.Command("tmux", "-L", socket, "set-option", "-g", "exit-empty", "off").CombinedOutput(); err != nil {
		t.Fatalf("set-option exit-empty off: %v: %s", err, out)
	}
}

// shQuote single-quotes s for safe embedding in a `sh -c` string, escaping any
// literal single quotes. Used so a verb argument that is itself a shell
// metacharacter to sh (the literal ";" tmux uses for command chaining) is
// passed through as one opaque argv element rather than parsed by sh.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// reparentedControlClient spawns `tmux -L <socket> -C <verbArgs...>` — control
// mode, so no real tty is required — via an intermediate `sh -c "... & echo
// $!; exit 0"` wrapper that backgrounds the tmux invocation and exits
// immediately. When sh exits, the kernel reparents the still-running tmux
// client to init/launchd, exactly the shape a crashed or detached shell
// leaves behind in production (and exactly what isControlClientOrphan's PPID
// check is watching for).
//
// This matters for what the test proves: a pid that is merely a DIRECT child
// of the test binary would itself satisfy looksLikeAgentDeckBinary (the test
// binary's own exe path) and get preserved by isControlClientOrphan for the
// wrong reason — parentage, not liveness — which would make the test pass
// even with isLiveTmuxClientOrServer stubbed out entirely. Forcing a genuine
// parentage orphan here means the ONLY thing standing between this pid and
// SIGKILL, in the real reapOrphanedPollClients pipeline, is
// isLiveTmuxClientOrServer.
//
// Returns the reparented client's pid and a cleanup that force-kills it
// directly (bypassing tmux, since the whole point of the test is not to rely
// on tmux protocol niceties for teardown either).
func reparentedControlClient(t *testing.T, socket, session string, verbArgs ...string) int {
	t.Helper()
	dir := t.TempDir()
	fifoPath := filepath.Join(dir, "stdin.fifo")
	if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	// O_RDWR on a FIFO never blocks (unlike O_RDONLY/O_WRONLY, which each wait
	// for a peer to open the other end). Holding this fd open for the test's
	// duration keeps the client's stdin from seeing EOF without needing a
	// separate background writer process.
	keepOpen, err := os.OpenFile(fifoPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open fifo O_RDWR: %v", err)
	}
	t.Cleanup(func() { _ = keepOpen.Close() })

	pidPath := filepath.Join(dir, "pid")
	logPath := filepath.Join(dir, "out.log")

	// -L, not -S: candidateSocketName only resolves -L (or no socket flag at
	// all) because that is the only shape agent-deck's own tmuxExecContext
	// factory ever produces (see candidateSocketName's doc comment) — a -S
	// candidate is deliberately refused as unresolvable. Using -S here would
	// also point this client at a different, nonexistent socket path than the
	// one startOrphanLiveSession created with -L, so it would never actually
	// reach the real server at all.
	argv := append([]string{"tmux", "-L", socket, "-C"}, verbArgs...)
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = shQuote(a)
	}
	script := strings.Join(quoted, " ") +
		" < " + shQuote(fifoPath) + " > " + shQuote(logPath) + " 2>&1 & echo $! > " + shQuote(pidPath) + "; exit 0"

	wrapper := exec.Command("sh", "-c", script)
	if out, err := wrapper.CombinedOutput(); err != nil {
		t.Fatalf("sh wrapper: %v: %s", err, out)
	}
	wrapperPID := wrapper.Process.Pid

	var pidBytes []byte
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		pidBytes, err = os.ReadFile(pidPath)
		if err == nil && strings.TrimSpace(string(pidBytes)) != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		logContents, _ := os.ReadFile(logPath)
		t.Fatalf("read pid file: %v (log: %s)", err, logContents)
	}

	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	// Confirm the reparent actually happened before handing the pid back —
	// otherwise a slow kernel scheduler could let a test proceed against a
	// pid that is still parented to the wrapper's own shell, silently
	// weakening the guarantee the test exists to establish.
	//
	// "Reparented" means "no longer the wrapper's child", not "ppid == 1": the
	// kernel hands an orphan to the nearest SUBREAPER, and under a
	// `systemd --user` session — the normal shape of a Linux desktop, and of
	// this repo's own dev hosts — that is the user manager, not init. Requiring
	// pid 1 makes this helper fail everywhere except a CI runner that happens
	// not to have one, and it proves nothing extra: what the test needs is a
	// parent that is not an agent-deck-shaped binary, which is exactly what
	// isControlClientOrphan goes on to check.
	deadline = time.Now().Add(2 * time.Second)
	reparented := false
	for time.Now().Before(deadline) {
		ppid, err := readParentPID(pid)
		if err == nil && ppid != wrapperPID {
			reparented = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !reparented {
		t.Fatalf("pid %d never left the wrapper shell within the deadline — cannot prove this is a genuine orphan", pid)
	}

	// Wait for the server to actually register the client before handing the
	// pid back. Registration happens asynchronously after exec — which is the
	// whole reason isLiveTmuxClientOrServer asks the server instead of reading
	// argv — so a caller that queried immediately could see a not-yet-registered
	// client and read it as "not live", failing for a timing reason rather than
	// a classification one. (Setup, not the assertion: what the tests check is
	// that the aliased/abbreviated spellings resolve to a real attach at all.)
	deadline = time.Now().Add(5 * time.Second)
	target := strconv.Itoa(pid)
	for time.Now().Before(deadline) {
		out, err := exec.Command("tmux", "-L", socket, "list-clients", "-F", "#{client_pid}").Output()
		if err == nil {
			for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				if strings.TrimSpace(line) == target {
					return pid
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	logContents, _ := os.ReadFile(logPath)
	t.Fatalf("client pid %d never registered with the server on socket %s (log: %s)", pid, socket, logContents)
	return 0
}

// TestIsLiveTmuxClientOrServer_ChainedAliasRepro is the direct regression test
// for the #1832-supersede vulnerability: reapOrphanedPollClients' previous
// safety gate (neverReapVerbs, matching only the literal strings
// "attach-session" / "new-session") missed tmux's built-in alias "attach" and
// every unambiguous abbreviation of it, so
//
//	tmux attach -t agentdeck_x \; set-option status on
//
// walked straight through: "attach" cleared the denylist (wrong spelling) and
// "set-option" satisfied the cadence allowlist, so the whole chained command
// was ruled reapable — while the attach half means a real interactive/control
// client sits behind it. isLiveTmuxClientOrServer replaces that argv-text
// check with a live query to the tmux server itself, which cannot be
// abbreviated around: registration happens on connect, not by re-parsing argv.
//
// The client here is spawned via a genuine parentage orphan (see
// reparentedControlClient) so this test cannot pass merely because
// isControlClientOrphan happens to protect a same-parent test process — only
// isLiveTmuxClientOrServer stands between this pid and a kill verdict.
func TestIsLiveTmuxClientOrServer_ChainedAliasRepro(t *testing.T) {
	socket := orphanLiveTestSocket(t)
	const session = "agentdeck_1832_repro"
	startOrphanLiveSession(t, socket, session)

	pid := reparentedControlClient(t, socket, session,
		"attach", "-t", session, ";", "set-option", "status", "on")

	live, ok := isLiveTmuxClientOrServer(context.Background(), pid, []string{
		"tmux", "-L", socket, "-C", "attach", "-t", session, ";", "set-option", "status", "on",
	})
	if !ok {
		t.Fatalf("isLiveTmuxClientOrServer could not classify pid %d — the query itself must succeed against a live server", pid)
	}
	if !live {
		t.Fatalf("isLiveTmuxClientOrServer(%d) = live=false — VULNERABLE: a live client attached via the "+
			"chained alias verb 'attach ; set-option' was not recognised as live and would be reaped", pid)
	}
}

// TestIsLiveTmuxClientOrServer_AbbreviatedAttach pins the same guarantee for
// an even shorter abbreviation than the alias: tmux resolves command names by
// unambiguous prefix, and "attach-session" is the only tmux command starting
// with "a", so bare "a" alone resolves to it (verified live on tmux 3.7b —
// `tmux -C a -t sess` attaches). No finite denylist of spelled-out verbs can
// anticipate every such prefix across tmux versions; the server-identity
// query does not need to.
func TestIsLiveTmuxClientOrServer_AbbreviatedAttach(t *testing.T) {
	socket := orphanLiveTestSocket(t)
	const session = "agentdeck_1832_abbrev"
	startOrphanLiveSession(t, socket, session)

	pid := reparentedControlClient(t, socket, session, "a", "-t", session)

	live, ok := isLiveTmuxClientOrServer(context.Background(), pid, []string{"tmux", "-L", socket, "-C", "a", "-t", session})
	if !ok {
		t.Fatalf("isLiveTmuxClientOrServer could not classify pid %d", pid)
	}
	if !live {
		t.Fatalf("isLiveTmuxClientOrServer(%d) = live=false — a live client attached via the single-letter "+
			"abbreviation 'a' was not recognised as live and would be reaped", pid)
	}
}

// TestIsLiveTmuxClientOrServer_FullVerbAttach is the control case: the
// long-form "attach-session" verb the original (superseded) denylist did
// catch, kept so a future edit cannot silently regress the common case while
// fixing the alias/abbreviation gap.
func TestIsLiveTmuxClientOrServer_FullVerbAttach(t *testing.T) {
	socket := orphanLiveTestSocket(t)
	const session = "agentdeck_1832_fullverb"
	startOrphanLiveSession(t, socket, session)

	pid := reparentedControlClient(t, socket, session, "attach-session", "-t", session)

	live, ok := isLiveTmuxClientOrServer(context.Background(), pid, []string{"tmux", "-L", socket, "-C", "attach-session", "-t", session})
	if !ok {
		t.Fatalf("isLiveTmuxClientOrServer could not classify pid %d", pid)
	}
	if !live {
		t.Fatalf("isLiveTmuxClientOrServer(%d) = live=false for a full-verb attach-session client", pid)
	}
}

// TestIsLiveTmuxClientOrServer_ServerItself pins the other half of the
// guarantee: the server's own pid must never be classified as reapable, which
// protects the nascent-server race (a server mid-startup still carries its
// creating client's comm and argv — see reapOrphanedPollClients' doc comment)
// by asking the server for its own identity rather than inferring role from
// argv at all.
func TestIsLiveTmuxClientOrServer_ServerItself(t *testing.T) {
	socket := orphanLiveTestSocket(t)
	const session = "agentdeck_1832_serverpid"
	startOrphanLiveSession(t, socket, session)

	out, err := exec.Command("tmux", "-L", socket, "display-message", "-p", "#{pid}").Output()
	if err != nil {
		t.Fatalf("display-message #{pid}: %v", err)
	}
	serverPID, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("parse server pid: %v", err)
	}

	live, ok := isLiveTmuxClientOrServer(context.Background(), serverPID, []string{"tmux", "-L", socket, "new-session", "-d", "-s", session})
	if !ok {
		t.Fatalf("isLiveTmuxClientOrServer could not classify the server pid %d", serverPID)
	}
	if !live {
		t.Fatalf("isLiveTmuxClientOrServer(%d) = live=false for the SERVER's own pid — "+
			"this is the nascent-server kill class the sweep must never reach", serverPID)
	}
}

// TestIsLiveTmuxClientOrServer_OneShotClient_NotLive confirms the sweep's
// actual target class — a genuinely leaked one-shot query client — still
// classifies as reapable (live=false, ok=true). Without this, an
// overcautious implementation could satisfy every "never reap a live attach"
// test by simply always returning live=true, which would silently restore
// the original 2026-08-01 bug (the sweep reaps nothing).
//
// The client is frozen with SIGSTOP immediately after starting, mid-flight,
// to stand in for the tmux-3.0a fd leak this sweep exists to mop up: verified
// live that a SIGSTOPped one-shot list-clients client does not appear in the
// server's own list-clients output even while its process is still alive —
// one-shot cadence commands never register as persistent clients the way an
// attach does, whether or not they are hung.
func TestIsLiveTmuxClientOrServer_OneShotClient_NotLive(t *testing.T) {
	socket := orphanLiveTestSocket(t)
	const session = "agentdeck_1832_oneshot"
	startOrphanLiveSession(t, socket, session)

	cmd := exec.Command("tmux", "-L", socket, "list-clients", "-F", "#{client_pid}")
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open devnull: %v", err)
	}
	defer devnull.Close()
	cmd.Stdin = devnull
	if err := cmd.Start(); err != nil {
		t.Fatalf("start one-shot client: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGCONT)
		_, _ = cmd.Process.Wait()
	})
	if err := cmd.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatalf("SIGSTOP one-shot client: %v", err)
	}

	live, ok := isLiveTmuxClientOrServer(context.Background(), pid, []string{"tmux", "-L", socket, "list-clients", "-F", "#{client_pid}"})
	if !ok {
		t.Fatalf("isLiveTmuxClientOrServer could not classify the frozen one-shot client %d", pid)
	}
	if live {
		t.Fatalf("isLiveTmuxClientOrServer(%d) = live=true for a one-shot query client — "+
			"this would make the sweep unable to reap the exact orphan class it exists for", pid)
	}
}

// TestIsLiveTmuxClientOrServer_NoServerAtSocket pins the fail-closed rule: no
// server reachable at the resolved socket must return ok=false (unclassifiable,
// refuse to kill), never live=false (safe to kill). A candidate whose target
// server already exited is exactly the ambiguous case
// isKnownCadenceArgv/isLiveTmuxClientOrServer's doc comments require refusing
// rather than guessing about.
func TestIsLiveTmuxClientOrServer_NoServerAtSocket(t *testing.T) {
	socket := "ad-orphlive-no-such-server-" + strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())
	if len(socket) > 40 {
		socket = socket[:40]
	}

	live, ok := isLiveTmuxClientOrServer(context.Background(), 999999, []string{"tmux", "-L", socket, "list-clients"})
	if ok {
		t.Fatalf("isLiveTmuxClientOrServer against a nonexistent server returned ok=true (live=%v) — "+
			"want ok=false (unclassifiable)", live)
	}
}
