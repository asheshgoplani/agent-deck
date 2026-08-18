package tmux

import (
	"testing"
	"time"
)

// SessionLivenessCached is ExistsCached with the cache's confidence exposed.
// The UI's 500ms pipe reconciler uses it to stop dialling control pipes at
// sessions whose tmux session is gone, so the "known" bit has to be exact: a
// cold cache answering "dead" would starve a healthy session of its pipe, and
// a warm cache answering "unknown" would let the dial storm right back in.

// A live session on the default socket is alive AND known.
func TestSessionLivenessCached_DefaultSocketLive(t *testing.T) {
	withCleanSessionCache(t)
	primeSessionCache("live-one")

	alive, known := SessionLivenessCached("", "live-one")
	if !alive || !known {
		t.Fatalf("SessionLivenessCached(live) = (%v, %v), want (true, true)", alive, known)
	}
}

// The case the reconciler needs: a warm default-socket cache that does not list
// the session is an authoritative "gone".
func TestSessionLivenessCached_DefaultSocketAbsentIsKnownDead(t *testing.T) {
	withCleanSessionCache(t)
	primeSessionCache("someone-else")

	alive, known := SessionLivenessCached("", "ghost")
	if alive || !known {
		t.Fatalf("SessionLivenessCached(absent, warm cache) = (%v, %v), want (false, true)", alive, known)
	}
}

// A stale/empty cache must report known=false. Callers MUST NOT read that as
// death — same indeterminate rule ListSessionNamesOnSocket documents.
func TestSessionLivenessCached_StaleDefaultCacheIsUnknown(t *testing.T) {
	withCleanSessionCache(t)
	primeSessionCache("live-one")
	sessionCacheMu.Lock()
	sessionCacheTime = time.Now().Add(-(sessionCacheTTL + time.Second))
	sessionCacheMu.Unlock()

	alive, known := SessionLivenessCached("", "live-one")
	if known {
		t.Fatalf("SessionLivenessCached on a stale cache = (%v, known=%v), want known=false", alive, known)
	}
}

// A cold isolated socket is unknown, not dead, and the lookup must not block
// waiting for the background refresh it kicks.
func TestSessionLivenessCached_ColdIsolatedSocketIsUnknown(t *testing.T) {
	withCleanSessionCache(t)

	done := make(chan bool, 1)
	go func() {
		_, known := SessionLivenessCached("iso-sock", "iso-one")
		done <- known
	}()
	select {
	case known := <-done:
		if known {
			t.Fatal("a cold isolated-socket entry must report known=false, not a dead verdict")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SessionLivenessCached blocked on a cold cache — it must never probe inline")
	}
	drainSocketRefreshes(t)
}

// Once the isolated socket's background refresh lands, absence there is
// authoritative: warm cache, session not listed => known dead.
func TestSessionLivenessCached_WarmIsolatedSocketAbsentIsKnownDead(t *testing.T) {
	withCleanSessionCache(t)
	listSessionsOnSocket = func(string) (map[string]struct{}, error) {
		return map[string]struct{}{"other": {}}, nil
	}

	_, _ = SessionLivenessCached("iso-sock", "iso-ghost") // kick the refresh
	waitForSocketCache(t, func() bool {
		_, known := SessionLivenessCached("iso-sock", "iso-ghost")
		return known
	}, "isolated-socket cache to warm up")

	alive, known := SessionLivenessCached("iso-sock", "iso-ghost")
	if alive || !known {
		t.Fatalf("SessionLivenessCached(iso ghost, warm) = (%v, %v), want (false, true)", alive, known)
	}
	drainSocketRefreshes(t)
}
