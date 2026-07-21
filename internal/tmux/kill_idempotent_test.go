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

// TestKill_NonexistentSessionIgnoresStalePositiveCache reproduces the archive
// race where the first archive stops tmux but does not persist ArchivedAt. A
// fresh positive entry can outlive the tmux session briefly; post-kill error
// verification must use live socket state instead of that cache entry.
func TestKill_NonexistentSessionIgnoresStalePositiveCache(t *testing.T) {
	skipIfNoTmuxBinary(t)
	previousSocket := DefaultSocketName()
	SetDefaultSocketName("")
	sessionCacheMu.Lock()
	previousCache := sessionCacheData
	previousCacheTime := sessionCacheTime
	sessionCacheMu.Unlock()
	t.Cleanup(func() {
		SetDefaultSocketName(previousSocket)
		sessionCacheMu.Lock()
		sessionCacheData = previousCache
		sessionCacheTime = previousCacheTime
		sessionCacheMu.Unlock()
	})

	s := NewSession("agent-deck-kill-stale-positive-absent", t.TempDir())
	sessionCacheMu.Lock()
	sessionCacheData = map[string]int64{s.Name: time.Now().Unix()}
	sessionCacheTime = time.Now()
	sessionCacheMu.Unlock()

	if err := s.Kill(); err != nil {
		t.Fatalf("Kill() trusted a stale positive cache entry: %v", err)
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
