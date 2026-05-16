package tmux

import (
	"testing"
	"time"
)

// TestSessionCacheTTLConsistency asserts that both
// sessionExistsFromCache and sessionActivityFromCache invalidate the
// cache at the EXACT same age. They share a single global cache
// (sessionCacheData / sessionCacheTime) — if their TTL checks drift
// (the bug pattern arch-review S2 catalogues), one reader will report a
// session as alive while the other reports it as gone. That divergence
// is the #886 heartbeat-parity family of bugs.
//
// The drift-detection here is structural: both helpers must consult
// sessionCacheTTL, the single named constant. If a future PR replaces
// the constant in only one site, this test will fail because it asserts
// behavior at the precise boundary (sessionCacheTTL - 10ms vs
// sessionCacheTTL + 10ms).
func TestSessionCacheTTLConsistency(t *testing.T) {
	// Save and restore global cache state so we don't leak into other tests.
	sessionCacheMu.Lock()
	prevData := sessionCacheData
	prevTime := sessionCacheTime
	sessionCacheMu.Unlock()
	t.Cleanup(func() {
		sessionCacheMu.Lock()
		sessionCacheData = prevData
		sessionCacheTime = prevTime
		sessionCacheMu.Unlock()
	})

	// Seed: cache age = 0, contents include "alive" with activity=42.
	sessionCacheMu.Lock()
	sessionCacheData = map[string]int64{"alive": 42}
	sessionCacheTime = time.Now()
	sessionCacheMu.Unlock()

	// Both readers should report valid at age 0.
	if exists, valid := sessionExistsFromCache("alive"); !valid || !exists {
		t.Fatalf("at age 0: existsFromCache=(exists=%v, valid=%v), want (true, true)", exists, valid)
	}
	if act, valid := sessionActivityFromCache("alive"); !valid || act != 42 {
		t.Fatalf("at age 0: activityFromCache=(activity=%d, valid=%v), want (42, true)", act, valid)
	}

	// Roll the cache time backwards so the cache is exactly 10ms past the TTL.
	sessionCacheMu.Lock()
	sessionCacheTime = time.Now().Add(-(sessionCacheTTL + 10*time.Millisecond))
	sessionCacheMu.Unlock()

	// Both readers MUST report invalid past TTL. If they drift, one will
	// still report valid — that's the bug.
	_, existsValid := sessionExistsFromCache("alive")
	_, activityValid := sessionActivityFromCache("alive")
	if existsValid || activityValid {
		t.Fatalf(
			"DRIFT past TTL=%s: existsFromCache.valid=%v activityFromCache.valid=%v; both must be false",
			sessionCacheTTL, existsValid, activityValid,
		)
	}

	// Likewise, both must agree the cache is valid just inside the TTL.
	sessionCacheMu.Lock()
	sessionCacheTime = time.Now().Add(-(sessionCacheTTL - 100*time.Millisecond))
	sessionCacheMu.Unlock()
	_, existsValid = sessionExistsFromCache("alive")
	_, activityValid = sessionActivityFromCache("alive")
	if !existsValid || !activityValid {
		t.Fatalf(
			"DRIFT inside TTL=%s: existsFromCache.valid=%v activityFromCache.valid=%v; both must be true",
			sessionCacheTTL, existsValid, activityValid,
		)
	}
}

// TestSessionCacheTTLValue pins the TTL to its v1.x value (2s, == 4
// ticks at 500ms). Bumping it requires a deliberate edit of the
// constant; this test catches accidental changes.
func TestSessionCacheTTLValue(t *testing.T) {
	if sessionCacheTTL != 2*time.Second {
		t.Errorf("sessionCacheTTL = %s, want 2s", sessionCacheTTL)
	}
}
