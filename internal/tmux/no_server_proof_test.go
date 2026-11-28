package tmux

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These tests pin the rules of the no-server proof (no_server_proof.go)
// through the probes that answer from it, counting tmux starts. They use only
// APIs that also exist before the proof, so the package's tests still build
// on the base (the fix-PR regression gate copies every changed test file
// there).

// serverlessSocket points TMUX_TMPDIR at a fresh short directory and returns
// a socket name on which no tmux server is listening: with stale=false there
// is no socket file at all (tmux: "error connecting to ... (No such file or
// directory)", the state after a reboot), with stale=true a socket file is
// left behind by a server that died (tmux: "no server running on ...").
// The proof is keyed by TMUX_TMPDIR, so each call starts with none.
func serverlessSocket(t *testing.T, stale bool) (socket, path string) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	tmp, err := os.MkdirTemp("", "adnsp")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(tmp, fmt.Sprintf("tmux-%d", os.Getuid()))
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX_TMPDIR", tmp)
	socket = "nsp"
	path = filepath.Join(dir, socket)
	if stale {
		leaveStaleSocketFile(t, path)
	}
	resetSocketMismatchCacheForTest()
	t.Cleanup(func() {
		// Only this test's private server, if one was started.
		_ = exec.Command("tmux", "-L", socket, "kill-server").Run()
		resetSocketMismatchCacheForTest()
		_ = os.RemoveAll(tmp)
	})
	return socket, path
}

// leaveStaleSocketFile creates a unix socket file at path that nothing
// accepts on, as a dead tmux server leaves behind.
func leaveStaleSocketFile(t *testing.T, path string) {
	t.Helper()
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = l.Close()
}

func serverlessKinds() []struct {
	name  string
	stale bool
} {
	return []struct {
		name  string
		stale bool
	}{{"socket file missing", false}, {"stale socket file", true}}
}

// existsStarts runs s.Exists and returns its result and how many tmux
// processes it started. Zero starts means a no-server proof answered.
func existsStarts(s *Session) (bool, int64) {
	before := SubprocessStarts()
	ok := s.Exists()
	return ok, SubprocessStarts() - before
}

// A server started after the proof, by any process, must be seen: the proof
// is re-checked against the socket file on every use, so the next probe goes
// to tmux again.
func TestNoServerProof_ServerStartedAfterProofIsSeen(t *testing.T) {
	for _, kind := range serverlessKinds() {
		t.Run(kind.name, func(t *testing.T) {
			socket, _ := serverlessSocket(t, kind.stale)
			_, _ = ListSessionNamesOnSocket(socket)
			if ok, n := existsStarts(&Session{Name: "agentdeck_nspprobe_0123abcd", SocketName: socket}); ok || n != 0 {
				t.Fatalf("after a serverless listing Exists() = %v with %d tmux starts, want false with 0", ok, n)
			}

			// Another process starts the server: nothing in this process
			// invalidates the proof explicitly.
			if out, err := exec.Command("tmux", "-L", socket, "new-session", "-d", "-s", "late", "sleep 300").CombinedOutput(); err != nil {
				t.Fatalf("start tmux server: %v: %s", err, out)
			}
			if !(&Session{Name: "late", SocketName: socket}).Exists() {
				t.Fatal("Exists() = false for a session on a server started after the no-server proof")
			}
			if _, n := existsStarts(&Session{Name: "agentdeck_nspprobe_0123abcd", SocketName: socket}); n == 0 {
				t.Fatal("a session on a now-live server was answered from the old no-server proof")
			}
		})
	}
}

// Session.Start and any tmux command that can start a server drop the proof
// before they run.
func TestNoServerProof_StartingCommandsInvalidate(t *testing.T) {
	socket, _ := serverlessSocket(t, false)
	probe := &Session{Name: "agentdeck_nspinval_0123abcd", SocketName: socket}
	for _, args := range [][]string{
		{"new-session", "-d", "-s", "x"},
		{"-u", "new", "-d"},
		{"start-server"},
	} {
		_, _ = ListSessionNamesOnSocket(socket)
		if _, n := existsStarts(probe); n != 0 {
			t.Fatalf("no proof recorded from a serverless listing: Exists() started %d tmux clients", n)
		}
		_ = tmuxExec(socket, args...) // built, not run
		if _, n := existsStarts(probe); n == 0 {
			t.Errorf("proof survived building tmux %v", args)
		}
	}

	_, _ = ListSessionNamesOnSocket(socket)
	s := NewSession("proofstart", t.TempDir())
	s.SocketName = socket
	if err := s.Start(""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !s.Exists() {
		t.Fatal("Exists() = false right after Start on a socket that had a no-server proof")
	}
}

// The proof is bounded in time: past its TTL (250ms, the age a status pass
// trusts one socket listing for) the probe runs again.
func TestNoServerProof_Expires(t *testing.T) {
	socket, _ := serverlessSocket(t, false)
	_, _ = ListSessionNamesOnSocket(socket)
	s := &Session{Name: "agentdeck_nspaged_0123abcd", SocketName: socket}
	if ok, n := existsStarts(s); ok || n != 0 {
		t.Fatalf("fresh proof: Exists() = %v with %d tmux starts, want false with 0", ok, n)
	}
	time.Sleep(300 * time.Millisecond)
	if ok, n := existsStarts(s); ok || n == 0 {
		t.Fatalf("expired proof: Exists() = %v with %d tmux starts, want false with a probe", ok, n)
	}
}

// The proof is keyed by the environment tmux resolves the socket from: a
// proof taken under one TMUX_TMPDIR says nothing about another.
func TestNoServerProof_KeyedByTmuxTmpdir(t *testing.T) {
	socket, _ := serverlessSocket(t, false)
	_, _ = ListSessionNamesOnSocket(socket)
	s := &Session{Name: "agentdeck_nspkey_0123abcd", SocketName: socket}
	if _, n := existsStarts(s); n != 0 {
		t.Fatalf("no proof recorded from a serverless listing: Exists() started %d tmux clients", n)
	}
	t.Setenv("TMUX_TMPDIR", t.TempDir())
	if _, n := existsStarts(s); n == 0 {
		t.Fatal("a proof taken under another TMUX_TMPDIR answered for this one")
	}
}

// A fresh cached protocol-mismatch verdict means a server answered: its
// "assume alive" reading wins over a proof.
func TestNoServerProof_MismatchVerdictStandsAside(t *testing.T) {
	socket, _ := serverlessSocket(t, false)
	_, _ = ListSessionNamesOnSocket(socket)
	socketMismatchMu.Lock()
	socketMismatchCache[socket] = socketMismatchVerdict{mismatched: true, checkedAt: time.Now()}
	socketMismatchMu.Unlock()
	s := &Session{Name: "agentdeck_nspmm_0123abcd", SocketName: socket}
	if !s.Exists() {
		t.Fatal("Exists() = false on a socket with a fresh protocol-mismatch verdict")
	}
}

// Only tmux's exact no-server diagnostics, from a client that ran to an exit,
// are a proof. A fake tmux prints each stderr for every command; after one
// listing, Exists answers without a client only for the two proving cases.
// A timeout, a killed client, a mismatch, a missing session on a live server
// or any other connection error stays indeterminate and records nothing.
func TestNoServerProof_OnlyNoServerDiagnosticsRecord(t *testing.T) {
	restoreTimeout := hasSessionProbeTimeout
	hasSessionProbeTimeout = 300 * time.Millisecond
	t.Cleanup(func() { hasSessionProbeTimeout = restoreTimeout })

	for _, tc := range []struct {
		name string
		// stderr is printed by the fake; MISSING and STALE are replaced by an
		// absolute path with no file and one with a stale socket file.
		stderr string
		// tail ends the fake: "exit 1" by default.
		tail   string
		proves bool
	}{
		{name: "no server running, stale file", stderr: "no server running on STALE", proves: true},
		{name: "no socket file", stderr: "error connecting to MISSING (No such file or directory)", proves: true},
		{name: "permission denied", stderr: "error connecting to MISSING (Permission denied)"},
		{name: "connection refused text", stderr: "error connecting to STALE (Connection refused)"},
		{name: "missing session on a live server", stderr: "can't find session: foo"},
		{name: "protocol mismatch", stderr: "protocol version mismatch (client 8, server 7)"},
		{name: "relative path", stderr: "no server running on relative/path"},
		{name: "two lines", stderr: "no server running on STALE\nno server running on STALE"},
		{name: "empty stderr", stderr: ""},
		{name: "ENOENT text but the file exists", stderr: "error connecting to STALE (No such file or directory)"},
		{name: "no server running but no file", stderr: "no server running on MISSING"},
		{name: "killed client", stderr: "error connecting to MISSING (No such file or directory)", tail: "kill -9 $$"},
		{name: "deadline", stderr: "error connecting to MISSING (No such file or directory)", tail: "exec sleep 5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			t.Setenv("TMUX_TMPDIR", tmp) // fresh proof key per case
			resetSocketMismatchCacheForTest()
			t.Cleanup(resetSocketMismatchCacheForTest)
			stale := filepath.Join(tmp, "stale")
			leaveStaleSocketFile(t, stale)
			missing := filepath.Join(tmp, "missing")
			stderr := tc.stderr
			for _, r := range [][2]string{{"STALE", stale}, {"MISSING", missing}} {
				stderr = strings.ReplaceAll(stderr, r[0], r[1])
			}
			tail := tc.tail
			if tail == "" {
				tail = "exit 1"
			}
			bin := filepath.Join(tmp, "bin")
			if err := os.Mkdir(bin, 0o755); err != nil {
				t.Fatal(err)
			}
			writeFakeTmux(t, bin, "for a in \"$@\"; do [ \"$a\" = -V ] && { echo 'tmux 3.6a'; exit 0; }; done\n"+
				"printf '%s\\n' "+shellQuote(stderr)+" >&2\n"+tail+"\n")

			const socket = "nspfake"
			_, _ = ListSessionNamesOnSocket(socket)
			_, n := existsStarts(&Session{Name: "agentdeck_nspdiag_0123abcd", SocketName: socket})
			if tc.proves && n != 0 {
				t.Fatalf("stderr %q: Exists() started %d tmux clients after the listing, want 0 (a no-server proof)", stderr, n)
			}
			if !tc.proves && n == 0 {
				t.Fatalf("stderr %q (%s) was taken as a no-server proof: Exists() started no tmux client", stderr, tail)
			}
		})
	}
}

// A live server's listing proves nothing: the negative of a listing of a live
// server is still not trusted for a session it omits (#755 multi-socket
// aliasing), and such a session still gets its own has-session.
func TestNoServerProof_LiveServerRecordsNothing(t *testing.T) {
	socket, _ := serverlessSocket(t, false)
	if out, err := exec.Command("tmux", "-L", socket, "new-session", "-d", "-s", "live", "sleep 300").CombinedOutput(); err != nil {
		t.Fatalf("start tmux server: %v: %s", err, out)
	}
	if _, err := ListSessionNamesOnSocket(socket); err != nil {
		t.Fatalf("ListSessionNamesOnSocket: %v", err)
	}
	ok, n := existsStarts(&Session{Name: "other", SocketName: socket})
	if ok {
		t.Fatal("Exists() = true for a session the live server does not have")
	}
	if n == 0 {
		t.Fatal("a session missing from a live server's listing was answered without its own probe")
	}
}
