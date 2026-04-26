package tmux

import (
	"testing"
	"time"
)

// TestSession_Exists_DoesNotTrustDefaultCacheForNonDefaultSocket is the
// RED regression for #755. Session.Exists() reads sessionCacheData, which
// is populated by RefreshSessionCache against DefaultSocketName() only.
// A session that lives on an isolated socket (Session.SocketName != default)
// MUST NOT trust that cache: a same-named entry on the default server is
// not the same session, and absence from the cache is not absence on the
// other socket. Either error causes UpdateStatus to stamp StatusError on
// every poll, which is exactly the user-visible failure in #755.
//
// This test exercises the false-positive path because it is reachable
// without a running tmux server: prime the default-socket cache with the
// session's name and assert Exists() does not return true based on that
// cached entry alone. The correct fall-through (tmux -L <isolated>
// has-session) fails inside the test process, so the right answer is
// false.
func TestSession_Exists_DoesNotTrustDefaultCacheForNonDefaultSocket(t *testing.T) {
	t.Cleanup(func() {
		SetDefaultSocketName("")
		sessionCacheMu.Lock()
		sessionCacheData = nil
		sessionCacheTime = time.Time{}
		sessionCacheMu.Unlock()
	})

	// Default socket = "" (the user's normal server). RefreshSessionCache
	// would have built sessionCacheData from `tmux list-windows -a` on
	// that server, so any entry here describes only the default socket.
	SetDefaultSocketName("")

	sessionCacheMu.Lock()
	sessionCacheData = map[string]int64{"iso-foo": time.Now().Unix()}
	sessionCacheTime = time.Now()
	sessionCacheMu.Unlock()

	// A Session living on a non-default socket must not be answered from
	// the default-socket cache. The socket-aware fall-through probe will
	// fail (no real tmux server is running on that socket in the test
	// process), so the only correct answer is false.
	s := &Session{Name: "iso-foo", SocketName: "agent-deck-iso-test-not-real"}
	if s.Exists() {
		t.Fatalf("Session.Exists() trusted default-socket cache for a session on socket %q (#755): "+
			"cache hit on the default server is not evidence the session exists on a different socket",
			s.SocketName)
	}
}

// TestSession_Exists_DefaultSocketStillUsesCache pins the backwards-compat
// path: when a Session's SocketName matches DefaultSocketName(), the cache
// is the right answer source and the fast path must still fire. Without
// this guard, the #755 fix would degrade every default-socket session to
// a fresh subprocess per Exists() call (the regression that the cache
// was introduced to prevent — see RefreshSessionCache header comment).
func TestSession_Exists_DefaultSocketStillUsesCache(t *testing.T) {
	t.Cleanup(func() {
		SetDefaultSocketName("")
		sessionCacheMu.Lock()
		sessionCacheData = nil
		sessionCacheTime = time.Time{}
		sessionCacheMu.Unlock()
	})

	SetDefaultSocketName("")

	sessionCacheMu.Lock()
	sessionCacheData = map[string]int64{"default-foo": time.Now().Unix()}
	sessionCacheTime = time.Now()
	sessionCacheMu.Unlock()

	s := &Session{Name: "default-foo"} // SocketName == "" == default
	if !s.Exists() {
		t.Fatalf("Session.Exists() must trust the cache when the session's socket matches DefaultSocketName(); got false despite a fresh cache hit")
	}
}
