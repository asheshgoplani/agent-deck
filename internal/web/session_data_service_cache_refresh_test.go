package web

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func menuRefreshTestInstances(n int) []*session.Instance {
	out := make([]*session.Instance, n)
	for i := range out {
		out[i] = &session.Instance{ID: fmt.Sprintf("refresh-%d", i)}
	}
	return out
}

// TestRefreshStatuses_RelistsTmuxCachesDuringLongPass pins the mid-pass
// re-list: once the shared tmux caches are older than the refresh age, the
// dispatch loop re-lists them, so rows late in a long pass (hundreds of
// sessions) read fresh caches instead of probing tmux one session at a time.
// The refresh age is forced to 1 ns so every dispatch sees an aged cache.
func TestRefreshStatuses_RelistsTmuxCachesDuringLongPass(t *testing.T) {
	const n = 40
	var refreshes atomic.Int64
	svc := &SessionDataService{
		refreshTmuxCaches: func() { refreshes.Add(1) },
		cacheRefreshAge:   time.Nanosecond,
	}
	svc.refreshStatuses(menuRefreshTestInstances(n))
	if got := refreshes.Load(); got < n/2 {
		t.Fatalf("tmux caches listed %d times in a pass over %d sessions with aged caches, want at least %d", got, n, n/2)
	}
}

// TestRefreshStatuses_ShortPassListsTmuxCachesOnce pins the default refresh
// age: a quick pass lists tmux once, and the age stays well inside the tmux
// session cache TTL (2 s, pinned by tmux's session_liveness_cache_test.go) so
// a refresh lands before late rows would fall back to per-session probes.
func TestRefreshStatuses_ShortPassListsTmuxCachesOnce(t *testing.T) {
	if menuCacheRefreshAge <= 0 || menuCacheRefreshAge > time.Second {
		t.Fatalf("menuCacheRefreshAge = %v, want in (0, 1s]: at most half the 2s tmux session cache TTL", menuCacheRefreshAge)
	}
	var refreshes atomic.Int64
	svc := &SessionDataService{refreshTmuxCaches: func() { refreshes.Add(1) }}
	svc.refreshStatuses(menuRefreshTestInstances(40))
	if got := refreshes.Load(); got != 1 {
		t.Fatalf("tmux caches listed %d times in a quick pass, want 1", got)
	}
}
