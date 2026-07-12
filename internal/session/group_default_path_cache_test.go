package session

import (
	"testing"
	"time"
)

// resolveGroupDefaultPathCached is the stale-while-revalidate variant used by
// updateGroupDefaultPath on the reload hot path (see resolveGroupDefaultPath's
// header). These tests pin its contract: a warm entry is served WITHOUT
// re-resolving (the whole point — the reload path must not touch git), and a
// stale entry is served immediately while a background refresh corrects it.

func resetDefaultPathCache(t *testing.T) {
	t.Helper()
	defaultPathCacheMu.Lock()
	defaultPathCache = map[string]*resolvedDefaultPathEntry{}
	defaultPathCacheMu.Unlock()
	t.Cleanup(func() {
		defaultPathCacheMu.Lock()
		defaultPathCache = map[string]*resolvedDefaultPathEntry{}
		defaultPathCacheMu.Unlock()
	})
}

// An empty path resolves to "" and never creates a cache entry.
func TestResolveGroupDefaultPathCached_EmptyNoEntry(t *testing.T) {
	resetDefaultPathCache(t)
	if got := resolveGroupDefaultPathCached("   "); got != "" {
		t.Fatalf("empty/whitespace path: got %q, want \"\"", got)
	}
	defaultPathCacheMu.Lock()
	n := len(defaultPathCache)
	defaultPathCacheMu.Unlock()
	if n != 0 {
		t.Fatalf("empty path must not create a cache entry; map has %d entries", n)
	}
}

// A warm (fresh) entry is served verbatim WITHOUT re-resolving. Seeding a bogus
// result the real resolver would never produce proves the cache short-circuits
// the git subprocesses — the property that makes reloads cheap.
func TestResolveGroupDefaultPathCached_WarmHitSkipsResolve(t *testing.T) {
	resetDefaultPathCache(t)
	const key = "/no/such/path/for/this/test"
	const sentinel = "SENTINEL-cached-result"

	defaultPathCacheMu.Lock()
	defaultPathCache[key] = &resolvedDefaultPathEntry{result: sentinel, computedAt: time.Now()}
	defaultPathCacheMu.Unlock()

	if got := resolveGroupDefaultPathCached(key); got != sentinel {
		t.Fatalf("warm cache hit re-resolved instead of serving the cached value: got %q, want %q", got, sentinel)
	}
}

// A stale entry is served immediately (stale-while-revalidate) and a background
// refresh replaces it with the truly-resolved value. For a nonexistent path,
// resolveGroupDefaultPath returns the path verbatim (os.Stat fails), so the
// background refresh must overwrite the sentinel with the key itself.
func TestResolveGroupDefaultPathCached_StaleServesThenRefreshes(t *testing.T) {
	resetDefaultPathCache(t)
	const key = "/no/such/path/stale/test"
	const sentinel = "SENTINEL-stale-result"

	defaultPathCacheMu.Lock()
	defaultPathCache[key] = &resolvedDefaultPathEntry{
		result:     sentinel,
		computedAt: time.Now().Add(-2 * defaultPathCacheTTL), // past TTL
	}
	defaultPathCacheMu.Unlock()

	// Immediate call serves the stale value (no blocking on the refresh).
	if got := resolveGroupDefaultPathCached(key); got != sentinel {
		t.Fatalf("stale entry should be served immediately: got %q, want %q", got, sentinel)
	}

	// The background refresh eventually replaces it with the real resolution
	// (the verbatim path for a nonexistent dir).
	deadline := time.Now().Add(2 * time.Second)
	for {
		defaultPathCacheMu.Lock()
		entry := defaultPathCache[key]
		result := ""
		if entry != nil {
			result = entry.result
		}
		defaultPathCacheMu.Unlock()
		if result == key {
			break // refreshed
		}
		if time.Now().After(deadline) {
			t.Fatalf("background refresh did not update the stale entry within 2s (result=%q, want %q)", result, key)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
