package tmux

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// serverlessSocket points TMUX_TMPDIR at a fresh short directory and returns
// a socket name on which no tmux server is listening: with stale=false there
// is no socket file at all (tmux: "error connecting to ... (No such file or
// directory)", the state after a reboot), with stale=true a socket file is
// left behind by a server that died (tmux: "no server running on ...").
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
		l, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		l.(*net.UnixListener).SetUnlinkOnClose(false)
		_ = l.Close()
	}
	resetNoServerProofsForTest()
	resetSocketMismatchCacheForTest()
	t.Cleanup(func() {
		// Only this test's private server, if one was started.
		_ = exec.Command("tmux", "-L", socket, "kill-server").Run()
		resetNoServerProofsForTest()
		resetSocketMismatchCacheForTest()
		_ = os.RemoveAll(tmp)
	})
	return socket, path
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

// With no tmux server on a socket, one listing of it proves every session
// there absent. Before the proof, each stored session paid its own
// has-session per status read (twice per row in `list --json`: the status
// pass and the substate read), so 200 stopped sessions cost 400 forks.
func TestNoServerProof_OneListingAnswersEverySessionOnTheSocket(t *testing.T) {
	for _, kind := range serverlessKinds() {
		t.Run(kind.name, func(t *testing.T) {
			socket, _ := serverlessSocket(t, kind.stale)

			names, err := ListSessionNamesOnSocket(socket)
			if kind.stale && (err != nil || len(names) != 0) {
				t.Fatalf("ListSessionNamesOnSocket on a stale socket = (%v, %v), want an empty listing", names, err)
			}
			if !kind.stale && err == nil {
				t.Fatalf("ListSessionNamesOnSocket with no socket file = (%v, nil), want an error (unknown, not empty)", names)
			}

			sessions := make([]*Session, 20)
			for i := range sessions {
				sessions[i] = &Session{Name: fmt.Sprintf("agentdeck_row%d_0123abcd", i), SocketName: socket}
			}
			before := SnapshotStarts()
			for _, s := range sessions {
				if s.Exists() {
					t.Fatalf("Exists(%s) = true on a socket with no server", s.Name)
				}
				if exists, listedAbsent := s.ExistsListed(func() map[string]struct{} { return nil }); exists || listedAbsent {
					t.Fatalf("ExistsListed(%s) = (%v, %v), want (false, false): a proof is not a listing", s.Name, exists, listedAbsent)
				}
				if code, ok := s.PaneDeadExitStatus(); ok {
					t.Fatalf("PaneDeadExitStatus(%s) = (%d, true) with no server", s.Name, code)
				}
				if HasSessionOnSocket(socket, s.TmuxName()) {
					t.Fatalf("HasSessionOnSocket(%s) = true with no server", s.Name)
				}
			}
			if primed := PrimePaneCaptures(sessions); primed != 0 {
				t.Fatalf("PrimePaneCaptures primed %d panes with no server", primed)
			}
			if n := before.Charged(); n != 0 {
				t.Errorf("status reads of %d sessions on a serverless socket started %d tmux clients after the listing, want 0", len(sessions), n)
			}
		})
	}
}

// A server started after the proof, by any process, must be seen: the proof
// is re-checked against the socket file on every use, so the next probe goes
// to tmux again.
func TestNoServerProof_ServerStartedAfterProofIsSeen(t *testing.T) {
	for _, kind := range serverlessKinds() {
		t.Run(kind.name, func(t *testing.T) {
			socket, _ := serverlessSocket(t, kind.stale)
			_, _ = ListSessionNamesOnSocket(socket)
			if !socketProvenServerless(socket) {
				t.Fatal("no proof recorded from a serverless listing")
			}

			// Another process starts the server: nothing in this process
			// invalidates the proof explicitly.
			if out, err := exec.Command("tmux", "-L", socket, "new-session", "-d", "-s", "late", "sleep 300").CombinedOutput(); err != nil {
				t.Fatalf("start tmux server: %v: %s", err, out)
			}
			s := &Session{Name: "late", SocketName: socket}
			if !s.Exists() {
				t.Fatal("Exists() = false for a session on a server started after the no-server proof")
			}
			if socketProvenServerless(socket) {
				t.Fatal("proof still holds after a server came up on the socket")
			}
		})
	}
}

// Session.Start and any tmux command that can start a server drop the proof
// before they run.
func TestNoServerProof_StartingCommandsInvalidate(t *testing.T) {
	socket, _ := serverlessSocket(t, false)
	for _, args := range [][]string{
		{"new-session", "-d", "-s", "x"},
		{"-u", "new", "-d"},
		{"start-server"},
	} {
		_, _ = ListSessionNamesOnSocket(socket)
		if !socketProvenServerless(socket) {
			t.Fatal("no proof recorded from a serverless listing")
		}
		_ = tmuxExec(socket, args...) // built, not run
		if socketProvenServerless(socket) {
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

// The proof is bounded in time: past noServerProofTTL the probe runs again.
func TestNoServerProof_Expires(t *testing.T) {
	socket, _ := serverlessSocket(t, false)
	now := time.Now()
	noServerProofNow = func() time.Time { return now }
	t.Cleanup(func() { noServerProofNow = time.Now })

	_, _ = ListSessionNamesOnSocket(socket)
	s := &Session{Name: "agentdeck_aged_0123abcd", SocketName: socket}
	before := SubprocessStarts()
	if s.Exists() {
		t.Fatal("Exists() = true with no server")
	}
	if n := SubprocessStarts() - before; n != 0 {
		t.Fatalf("fresh proof: %d tmux starts, want 0", n)
	}
	now = now.Add(noServerProofTTL)
	before = SubprocessStarts()
	if s.Exists() {
		t.Fatal("Exists() = true with no server")
	}
	if n := SubprocessStarts() - before; n == 0 {
		t.Fatal("an expired proof still answered Exists() without a probe")
	}
}

// The proof is keyed by the environment tmux resolves the socket from: a
// proof taken under one TMUX_TMPDIR says nothing about another.
func TestNoServerProof_KeyedByTmuxTmpdir(t *testing.T) {
	socket, _ := serverlessSocket(t, false)
	_, _ = ListSessionNamesOnSocket(socket)
	if !socketProvenServerless(socket) {
		t.Fatal("no proof recorded from a serverless listing")
	}
	t.Setenv("TMUX_TMPDIR", t.TempDir())
	if socketProvenServerless(socket) {
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
	if hasSessionProvenGone(socket) {
		t.Fatal("hasSessionProvenGone = true while the socket's mismatch verdict is fresh")
	}
	s := &Session{Name: "agentdeck_mm_0123abcd", SocketName: socket}
	if !s.Exists() {
		t.Fatal("Exists() = false on a socket with a fresh protocol-mismatch verdict")
	}
}

// Only tmux's exact no-server diagnostics are a proof. A timeout, a killed
// client, a mismatch, a missing session on a live server or any other
// connection error stays indeterminate and records nothing.
func TestNoServerProof_OnlyNoServerDiagnosticsRecord(t *testing.T) {
	for _, tc := range []struct {
		stderr, path string
		missing, ok  bool
	}{
		{"no server running on /tmp/tmux-1/default\n", "/tmp/tmux-1/default", false, true},
		{"error connecting to /tmp/tmux-1/x (No such file or directory)", "/tmp/tmux-1/x", true, true},
		{"error connecting to /tmp/tmux-1/x (Permission denied)", "", false, false},
		{"error connecting to /tmp/tmux-1/x (Connection refused)", "", false, false},
		{"can't find session: foo", "", false, false},
		{"protocol version mismatch (client 8, server 7)", "", false, false},
		{"no server running on relative/path", "", false, false},
		{"no server running on /a\nno server running on /b", "", false, false},
		{"", "", false, false},
	} {
		path, missing, ok := parseNoServerStderr(tc.stderr)
		if path != tc.path || missing != tc.missing || ok != tc.ok {
			t.Errorf("parseNoServerStderr(%q) = (%q, %v, %v), want (%q, %v, %v)", tc.stderr, path, missing, ok, tc.path, tc.missing, tc.ok)
		}
	}

	socket, path := serverlessSocket(t, false)
	// Not a completed client: a deadline or a signal never records.
	noteNoServer(socket, context.DeadlineExceeded)
	noteNoServer(socket, &exec.ExitError{})
	// The diagnostic names a socket file that exists now: a server may have
	// come up since the client looked, so nothing is recorded.
	stalePath := filepath.Join(t.TempDir(), "s")
	if l, err := net.Listen("unix", stalePath); err == nil {
		defer l.Close()
		noteNoServerStderr(socket, "error connecting to "+stalePath+" (No such file or directory)")
	}
	// "no server running" with no socket file left is inconsistent: nothing.
	noteNoServerStderr(socket, "no server running on "+path)
	if socketProvenServerless(socket) {
		t.Fatal("a proof was recorded from a non-proving outcome")
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
	if socketProvenServerless(socket) {
		t.Fatal("a live server's listing recorded a no-server proof")
	}
	s := &Session{Name: "other", SocketName: socket}
	before := SubprocessStarts()
	if s.Exists() {
		t.Fatal("Exists() = true for a session the live server does not have")
	}
	if n := SubprocessStarts() - before; n == 0 {
		t.Fatal("a session missing from a live server's listing was answered without its own probe")
	}
}
