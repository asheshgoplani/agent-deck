package tmux

import (
	"testing"
	"time"
)

// Session.KillAndWait ran `tmux kill-session` with a BARE argv — no
// `-L <socket>` — while every other command the same function issues
// (getPaneProcessTree) went through the session's own socket. With
// `[tmux] socket_name` set, every live session lives on that named server, so
// the kill was addressed to the DEFAULT server, exited 1 ("can't find
// session"), and KillAndWait returned that error while the tmux session was
// still there.
//
// Its callers read that error as "the stop failed" and roll back:
// archiveSession (internal/ui/home.go) reverts ArchivedAt and reports
// "failed to archive: stop archived session: failed to kill tmux session:
// exit status 1" — the user-visible bug, an archive that has to be pressed 3-4
// times. It "eventually works" because EnsurePIDsDead *is* socket-correct: it
// SIGTERM/SIGKILLs the pane PIDs, tmux tears the session down a moment later,
// and the NEXT attempt finds nothing to kill and takes the already-gone escape
// hatch. In between, the session is a killed-but-unarchived row: the "goes to
// error" half of the report.
//
// The bare argv is also a cross-server hazard in its own right: a same-named
// session on the user's default server would have been killed instead.
func TestKillAndWait_NamedSocketSessionIsKilled(t *testing.T) {
	skipIfNoTmuxBinary(t)

	const socket = "agent-deck-killwait-socket-test"
	const name = "agent-deck-killwait-live"

	// TestMain isolates TMUX_TMPDIR, so this `-L` server is created under the
	// test's private tmpdir and can never be the user's real default server.
	//
	// The pane runs `tail`, deliberately: it is NOT in isOurProcessLoose's
	// allowlist, so EnsurePIDsDead leaves it alone and the tmux session is
	// still standing when KillAndWait decides whether the kill worked. That is
	// what makes this test see the bug at all. With a `sh`/`sleep` pane the
	// PID reap tears the session down as a side effect within ~250ms and the
	// already-gone escape hatch hides the misaddressed kill — the same
	// accident that makes the real archive succeed on the 3rd or 4th press.
	if out, err := tmuxExec(socket, "new-session", "-d", "-s", name, "tail", "-f", "/dev/null").CombinedOutput(); err != nil {
		t.Skipf("could not start tmux session on socket %q: %v (%s)", socket, err, out)
	}
	t.Cleanup(func() {
		_ = tmuxExec(socket, "kill-server").Run()
	})

	s := &Session{Name: name, SocketName: socket}
	if !tmuxSessionExistsOnSocket(socket, name) {
		t.Fatalf("setup: session %q not found on socket %q", name, socket)
	}

	if err := s.KillAndWait(); err != nil {
		t.Fatalf("KillAndWait() on a live session on socket %q returned %v; "+
			"the kill must target the session's OWN socket, not the default server", socket, err)
	}

	// The session must be genuinely gone, not merely reported dead: a kill sent
	// to the wrong server leaves the pane running.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !tmuxSessionExistsOnSocket(socket, name) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("session %q still exists on socket %q after KillAndWait()", name, socket)
}

// A second KillAndWait of a dead named-socket session must still be success.
// This is the path archiveSession takes after Unarchive (which clears the flag
// without restarting tmux) and the one `agent-deck session remove` takes on an
// already-stopped session.
func TestKillAndWait_NamedSocketAlreadyGoneIsSuccess(t *testing.T) {
	skipIfNoTmuxBinary(t)

	const socket = "agent-deck-killwait-absent-test"
	s := &Session{Name: "agent-deck-killwait-absent", SocketName: socket}
	if err := s.KillAndWait(); err != nil {
		t.Fatalf("KillAndWait() on a nonexistent session on socket %q should return nil, got: %v", socket, err)
	}
}

// The post-kill "is it actually gone?" verification must not be answered from
// the default-socket session cache, exactly as Session.Kill's was fixed to do
// (TestKill_NonexistentSessionIgnoresStalePositiveCache). A stale positive
// entry naming this session would otherwise turn a successful kill into the
// same spurious archive failure.
func TestKillAndWait_IgnoresStalePositiveDefaultCache(t *testing.T) {
	skipIfNoTmuxBinary(t)

	const name = "agent-deck-killwait-stale"

	previousSocket := DefaultSocketName()
	SetDefaultSocketName("")
	sessionCacheMu.Lock()
	previousCache := sessionCacheData
	previousCacheTime := sessionCacheTime
	sessionCacheData = map[string]int64{name: time.Now().Unix()}
	sessionCacheTime = time.Now()
	sessionCacheMu.Unlock()
	t.Cleanup(func() {
		SetDefaultSocketName(previousSocket)
		sessionCacheMu.Lock()
		sessionCacheData = previousCache
		sessionCacheTime = previousCacheTime
		sessionCacheMu.Unlock()
	})

	s := &Session{Name: name} // SocketName "" == default: the cache applies
	if err := s.KillAndWait(); err != nil {
		t.Fatalf("KillAndWait() trusted a stale positive default-socket cache entry: %v", err)
	}
}
