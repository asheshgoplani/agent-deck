package tmux

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSession_Exists_ProbeKilledIsNotTreatedAsAbsent is the regression for a
// has-session probe that never reaches the tmux server at all: an external
// SIGKILL (e.g. a macOS security-policy rejection killing freshly spawned
// children) makes the client die by signal, which previously returned
// err != nil and reported the session as gone — flipping live sessions to
// tmux_missing and letting restart machinery act on panes still running.
// Like the timeout branch, a probe without an answer is indeterminate and
// must assume the session still exists.
func TestSession_Exists_ProbeKilledIsNotTreatedAsAbsent(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "tmux")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nkill -9 $$\n"), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// Non-default socket skips the session cache; a unique name guarantees no
	// live pipe connection — so Exists() reaches the subprocess probe.
	s := &Session{Name: "killed-probe-session", SocketName: "agent-deck-killed-probe-test"}

	if !s.Exists() {
		t.Fatalf("Exists() returned false when the has-session probe was killed; " +
			"a probe that never completes is indeterminate and must not be treated as a dead session")
	}
}

// TestSession_Exists_CompletedNegativeProbeStaysAbsent is the control for the
// killed-probe fix: a client that runs to a real non-zero exit DID reach the
// server and was told the session is gone — that answer must stay
// authoritative. The fix only rescues probes that never completed.
func TestSession_Exists_CompletedNegativeProbeStaysAbsent(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "tmux")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho \"can't find session: x\" >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	s := &Session{Name: "completed-probe-session", SocketName: "agent-deck-completed-probe-test"}

	if s.Exists() {
		t.Fatalf("Exists() returned true for a completed negative probe; " +
			"a client that reaches the server and is refused means the session is really gone")
	}
}

// TestTmuxSessionExistsOnSocket_ProbeKilledIsNotTreatedAsAbsent applies the
// same indeterminate-probe contract to the pipe-manager variant used by the
// watchPipe reconnect loop: a SIGKILLed has-session client previously returned
// false here, which made the loop conclude a live session was gone and tear
// down its control pipe.
func TestTmuxSessionExistsOnSocket_ProbeKilledIsNotTreatedAsAbsent(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "tmux")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nkill -9 $$\n"), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if !tmuxSessionExistsOnSocket("agent-deck-killed-pipe-probe-test", "killed-probe-session") {
		t.Fatalf("tmuxSessionExistsOnSocket returned false when the has-session probe was killed; " +
			"the reconnect loop would then conclude a live session is gone and drop its pipe")
	}
}

// TestTmuxSessionExistsOnSocket_CompletedNegativeProbeStaysAbsent is the
// control: a client that completes with a real non-zero exit reached the
// server and was refused — still authoritative absence.
func TestTmuxSessionExistsOnSocket_CompletedNegativeProbeStaysAbsent(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "tmux")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho \"can't find session: x\" >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if tmuxSessionExistsOnSocket("agent-deck-completed-pipe-probe-test", "completed-probe-session") {
		t.Fatalf("tmuxSessionExistsOnSocket returned true for a completed negative probe")
	}
}
