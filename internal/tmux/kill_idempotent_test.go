package tmux

import "testing"

// Kill's job is "make the session dead". A session that is already gone (or
// whose whole server is down) is that outcome, not a failure — callers like
// the archive flow persist StatusStopped only when Kill returns nil, so a
// "can't find session" error left archived sessions stuck in error status.
func TestKill_NonexistentSessionIsNotAnError(t *testing.T) {
	s := &Session{Name: "agentdeck-kill-idemp-nonexistent", SocketName: "agent-deck-kill-idemp-test"}
	if err := s.Kill(); err != nil {
		t.Fatalf("Kill on nonexistent session should be a no-op success, got: %v", err)
	}
}

func TestKillAndWait_NonexistentSessionIsNotAnError(t *testing.T) {
	s := &Session{Name: "agentdeck-killwait-idemp-nonexistent", SocketName: "agent-deck-kill-idemp-test"}
	if err := s.KillAndWait(); err != nil {
		t.Fatalf("KillAndWait on nonexistent session should be a no-op success, got: %v", err)
	}
}
