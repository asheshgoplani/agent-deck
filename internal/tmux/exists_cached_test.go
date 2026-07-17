package tmux

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

// ExistsCached() is the cheap liveness check used by hot periodic loops (the
// background configure loop and theme propagation). It must answer ONLY from
// in-process state — a positive session-cache hit on the session's own socket
// or a live pipe — and NEVER spawn a `tmux has-session` subprocess. That
// subprocess, multiplied over ~1000 sessions per tick, is the exact main-loop
// storm the nav-freeze fix eliminated. These cases pin the contract.

func withCleanSessionCache(t *testing.T) {
	t.Helper()
	origLister := listSessionsOnSocket
	t.Cleanup(func() {
		SetDefaultSocketName("")
		sessionCacheMu.Lock()
		sessionCacheData = nil
		sessionCacheTime = time.Time{}
		sessionCacheMu.Unlock()
		// Drain any in-flight background refresh before swapping the lister
		// back, or a goroutine from this test writes into the next test's cache.
		drainSocketRefreshes(t)
		listSessionsOnSocket = origLister
		ResetSocketSessionCacheForTest()
	})
	SetDefaultSocketName("")
	drainSocketRefreshes(t)
	ResetSocketSessionCacheForTest()
	// Default: no isolated socket has any session, and never touch real tmux.
	listSessionsOnSocket = func(string) (map[string]struct{}, error) {
		return map[string]struct{}{}, nil
	}
}

// stubSocketSessions makes the per-socket lister return a fixed live set and
// signals (via the returned channel) each time it is invoked, so tests can wait
// for the asynchronous stale-while-revalidate refresh to land.
func stubSocketSessions(t *testing.T, names map[string][]string) <-chan string {
	t.Helper()
	calls := make(chan string, 16)
	listSessionsOnSocket = func(socket string) (map[string]struct{}, error) {
		set := map[string]struct{}{}
		for _, n := range names[socket] {
			set[n] = struct{}{}
		}
		select {
		case calls <- socket:
		default:
		}
		return set, nil
	}
	return calls
}

// drainSocketRefreshes blocks until no per-socket refresh goroutine is in
// flight, so one test's background probe cannot land in the next test's cache.
func drainSocketRefreshes(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		socketSessionCacheMu.Lock()
		inFlight := false
		for _, e := range socketSessionCache {
			if e.refreshing {
				inFlight = true
				break
			}
		}
		socketSessionCacheMu.Unlock()
		if !inFlight {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out draining in-flight per-socket refreshes")
}

// waitForSocketCache polls cond until true or the deadline elapses.
func waitForSocketCache(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", msg)
}

func primeSessionCache(names ...string) {
	sessionCacheMu.Lock()
	m := make(map[string]int64, len(names))
	for _, n := range names {
		m[n] = time.Now().Unix()
	}
	sessionCacheData = m
	sessionCacheTime = time.Now()
	sessionCacheMu.Unlock()
}

func TestIsEmptyTmuxServerResult_ClassifiesOnlyExpectedTmuxErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		stderr string
		want   bool
	}{
		{name: "no server", stderr: "no server running on /tmp/tmux.sock", want: true},
		{name: "no sessions", stderr: "no sessions", want: true},
		{name: "unexpected failure", stderr: "permission denied", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command("sh", "-c", "printf '%s' \"$1\" >&2; exit 1", "sh", tt.stderr)
			_, err := cmd.Output()
			if err == nil {
				t.Fatal("fixture command unexpectedly succeeded")
			}
			if got := isEmptyTmuxServerResult(err); got != tt.want {
				t.Fatalf("isEmptyTmuxServerResult() = %v, want %v for stderr %q", got, tt.want, tt.stderr)
			}
		})
	}
}

// A fresh positive cache hit on the default socket means the session is live.
func TestExistsCached_PositiveDefaultSocketHit(t *testing.T) {
	withCleanSessionCache(t)
	primeSessionCache("cached-foo")

	s := &Session{Name: "cached-foo"} // SocketName == "" == default
	if !s.ExistsCached() {
		t.Fatalf("ExistsCached() must return true for a fresh positive cache hit on the default socket")
	}
}

// A cache miss (session absent from a fresh cache) must return false WITHOUT
// probing — the deliberate under-report that keeps the periodic loops cheap.
func TestExistsCached_MissReturnsFalseNoProbe(t *testing.T) {
	withCleanSessionCache(t)
	primeSessionCache("someone-else")

	s := &Session{Name: "not-in-cache"}
	// If this ever fell through to a has-session subprocess it would hang/slow;
	// the cache-only contract guarantees an immediate false.
	done := make(chan bool, 1)
	go func() { done <- s.ExistsCached() }()
	select {
	case got := <-done:
		if got {
			t.Fatalf("ExistsCached() must return false on a cache miss")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("ExistsCached() blocked on a cache miss — it must never subprocess-probe")
	}
}

// A stale/empty cache is not evidence of anything; return false, never probe.
func TestExistsCached_StaleCacheReturnsFalse(t *testing.T) {
	withCleanSessionCache(t)
	sessionCacheMu.Lock()
	sessionCacheData = map[string]int64{"cached-foo": time.Now().Unix()}
	sessionCacheTime = time.Now().Add(-1 * time.Hour) // past TTL
	sessionCacheMu.Unlock()

	s := &Session{Name: "cached-foo"}
	if s.ExistsCached() {
		t.Fatalf("ExistsCached() must return false when the cache is stale")
	}
}

// Socket guard (#755 / multi-socket aliasing): a same-named entry on the
// default-socket cache is not evidence a session on a DIFFERENT socket exists.
// ExistsCached must ignore the cache for a non-default socket and, when that
// socket genuinely has no such session, return false — without a subprocess on
// the calling goroutine.
func TestExistsCached_NonDefaultSocketIgnoresDefaultCache(t *testing.T) {
	withCleanSessionCache(t)
	primeSessionCache("iso-foo")
	stubSocketSessions(t, nil) // isolated socket has no sessions

	s := &Session{Name: "iso-foo", SocketName: "agent-deck-iso-not-real"}
	if s.ExistsCached() {
		t.Fatalf("ExistsCached() trusted the default-socket cache for a session on socket %q — "+
			"a same-named default-socket entry is not evidence of a session on another socket", s.SocketName)
	}
}

// Regression: a LIVE session on an isolated socket must eventually read true.
// Before the per-socket cache, ExistsCached() had no source of truth for such a
// session (the default cache is off-limits per #755, and PipeManager only
// connects wanted sessions), so it returned false on EVERY tick forever —
// silently starving those sessions of EnsureConfigured and theme propagation.
func TestExistsCached_LiveSessionOnIsolatedSocketBecomesTrue(t *testing.T) {
	withCleanSessionCache(t)
	stubSocketSessions(t, map[string][]string{"iso-sock": {"iso-live"}})

	s := &Session{Name: "iso-live", SocketName: "iso-sock"}

	// Cold cache: the first call under-reports (false) but kicks a background
	// refresh. It must not block on the probe.
	done := make(chan bool, 1)
	go func() { done <- s.ExistsCached() }()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("ExistsCached() blocked on a cold isolated-socket cache — it must never probe inline")
	}

	// Once the background refresh lands, the live session reads true and STAYS
	// true. A permanent false here is the bug this test pins.
	waitForSocketCache(t, s.ExistsCached, "live isolated-socket session to be seen after background refresh")
	if !s.ExistsCached() {
		t.Fatalf("ExistsCached() must stay true for a live session on an isolated socket")
	}
}

// A dead session on an isolated socket stays false even after the cache warms.
func TestExistsCached_DeadSessionOnIsolatedSocketStaysFalse(t *testing.T) {
	withCleanSessionCache(t)
	calls := stubSocketSessions(t, map[string][]string{"iso-sock": {"some-other-session"}})

	s := &Session{Name: "iso-gone", SocketName: "iso-sock"}
	_ = s.ExistsCached() // kick the refresh

	select {
	case <-calls:
	case <-time.After(2 * time.Second):
		t.Fatalf("background per-socket refresh never ran")
	}
	waitForSocketCache(t, func() bool {
		socketSessionCacheMu.Lock()
		defer socketSessionCacheMu.Unlock()
		e, ok := socketSessionCache["iso-sock"]
		return ok && e.warm
	}, "per-socket cache to warm")

	if s.ExistsCached() {
		t.Fatalf("ExistsCached() must stay false for a session absent from its own socket")
	}
}

// An indeterminate probe (timeout) must not flap a previously-live session to
// false: the last known name set is retained.
func TestExistsCached_IndeterminateProbeKeepsLastKnownSet(t *testing.T) {
	withCleanSessionCache(t)
	stubSocketSessions(t, map[string][]string{"iso-sock": {"iso-live"}})

	s := &Session{Name: "iso-live", SocketName: "iso-sock"}
	_ = s.ExistsCached()
	waitForSocketCache(t, s.ExistsCached, "initial warm cache showing the live session")

	// Now every probe fails. Force the entry stale so the next read re-probes.
	listSessionsOnSocket = func(string) (map[string]struct{}, error) {
		return nil, context.DeadlineExceeded
	}
	socketSessionCacheMu.Lock()
	socketSessionCache["iso-sock"].refreshedAt = time.Now().Add(-1 * time.Hour)
	socketSessionCacheMu.Unlock()

	if !s.ExistsCached() {
		t.Fatalf("a stale entry must still answer from the last known set, not false")
	}
	// Let the failing refresh land; the previous set must survive it.
	waitForSocketCache(t, func() bool {
		socketSessionCacheMu.Lock()
		defer socketSessionCacheMu.Unlock()
		return !socketSessionCache["iso-sock"].refreshing
	}, "failing refresh to complete")

	if !s.ExistsCached() {
		t.Fatalf("an indeterminate probe must retain the last known set, not report the session gone")
	}
}
