package tmux

import (
	"testing"
	"time"
)

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
	for _, method := range []struct {
		name string
		kill func(*Session) error
	}{{"Kill", (*Session).Kill}, {"KillAndWait", (*Session).KillAndWait}} {
		t.Run(method.name, func(t *testing.T) {
			s := NewSession("agent-deck-kill-idempotent-live", t.TempDir())
			if err := s.Start(""); err != nil {
				t.Fatalf("could not start isolated tmux session: %v", err)
			}
			t.Cleanup(func() { _ = s.Kill() })
			if err := method.kill(s); err != nil {
				t.Fatalf("first kill of a live session should return nil, got: %v", err)
			}
			// A recent activity sample can still list the killed session. Make
			// this deterministic instead of depending on another test's cache.
			registerSessionInCache(s.Name)
			sessionCacheMu.Lock()
			sessionCacheTime = time.Now()
			sessionCacheMu.Unlock()
			t.Cleanup(func() {
				sessionCacheMu.Lock()
				delete(sessionCacheData, s.Name)
				sessionCacheMu.Unlock()
			})
			if !s.Exists() {
				t.Fatal("fixture must retain a positive cached existence result")
			}
			if err := method.kill(s); err != nil {
				t.Fatalf("second kill of an already-dead session should return nil, got: %v", err)
			}
		})
	}
}

func TestKill_IndeterminateExistenceRemainsAnError(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, method := range []struct {
		name string
		kill func(*Session) error
	}{{"Kill", (*Session).Kill}, {"KillAndWait", (*Session).KillAndWait}} {
		t.Run(method.name, func(t *testing.T) {
			s := NewSession("kill-unknown", t.TempDir())
			if err := method.kill(s); err == nil {
				t.Fatal("a client that cannot run must not prove the session is gone")
			}
		})
	}
}
