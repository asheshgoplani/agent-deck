package tmux

import "testing"

// TestKill_NonexistentSessionReturnsNil pins that killing a tmux session that
// no longer exists is treated as success, not failure.
//
// tmux `kill-session` exits non-zero ("can't find session") for an
// already-dead session. Surfacing that as an error made the TUI's
// archiveSession (and WebMutator.ArchiveSession) abort and silently fail to
// persist the archive when re-archiving a session whose tmux was already gone
// — the exact path hit after Unarchive, which clears the flag without
// restarting tmux. See archiveSession in internal/ui/home.go.
func TestKill_NonexistentSessionReturnsNil(t *testing.T) {
	skipIfNoTmuxBinary(t)
	s := NewSession("agent-deck-kill-idempotent-absent", t.TempDir())
	if err := s.Kill(); err != nil {
		t.Fatalf("Kill() on a nonexistent session should return nil, got: %v", err)
	}
}

// TestKillAndWait_NonexistentSessionReturnsNil mirrors the above for the
// synchronous CLI path (`agent-deck remove`), which also must not fail just
// because the session was already stopped.
func TestKillAndWait_NonexistentSessionReturnsNil(t *testing.T) {
	skipIfNoTmuxBinary(t)
	s := NewSession("agent-deck-killandwait-idempotent-absent", t.TempDir())
	if err := s.KillAndWait(); err != nil {
		t.Fatalf("KillAndWait() on a nonexistent session should return nil, got: %v", err)
	}
}

// TestKill_LiveSessionThenSecondKillBothSucceed verifies the first kill of a
// live session still works (returns nil) and that an immediately-repeated kill
// of the now-dead session is also nil — the idempotency the archive flow relies
// on.
func TestKill_LiveSessionThenSecondKillBothSucceed(t *testing.T) {
	skipIfNoTmuxBinary(t)
	s := NewSession("agent-deck-kill-idempotent-live", t.TempDir())
	if err := s.Start(""); err != nil {
		t.Skipf("could not start tmux session in this environment: %v", err)
	}
	if err := s.Kill(); err != nil {
		t.Fatalf("first Kill() of a live session should return nil, got: %v", err)
	}
	if err := s.Kill(); err != nil {
		t.Fatalf("second Kill() of an already-dead session should return nil, got: %v", err)
	}
}
