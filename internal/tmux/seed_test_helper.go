package tmux

import (
	"testing"
	"time"
)

// SeedPaneInfoCacheForTest replaces the package's pane info cache with the
// supplied data and marks it fresh. Test cleanup wipes the cache back to its
// pristine zero state so concurrent or follow-on tests do not see seeded data.
//
// Production callers must use RefreshPaneInfoCache; this exists so packages
// outside internal/tmux (notably internal/ui) can drive snapshot/render tests
// without standing up a real tmux server.
func SeedPaneInfoCacheForTest(t testing.TB, info map[string]PaneInfo) {
	t.Helper()
	paneCacheMu.Lock()
	paneCacheData = info
	paneCacheTime = time.Now()
	paneCacheMu.Unlock()
	t.Cleanup(func() {
		paneCacheMu.Lock()
		paneCacheData = nil
		paneCacheTime = time.Time{}
		paneCacheMu.Unlock()
	})
}

// ExpirePaneInfoCacheForTest leaves the cache contents intact but rewinds the
// timestamp past the freshness threshold so GetCachedPaneInfo treats it as
// stale. Used to model the case where backgroundStatusUpdate hasn't run for a
// while (e.g. navigation hot-window) and the snapshot rebuild path must not
// blow away previously-known pane titles. t.Cleanup restores the timestamp
// so calling Expire alone (without a prior Seed that owns its own cleanup)
// is also safe.
func ExpirePaneInfoCacheForTest(t testing.TB) {
	t.Helper()
	paneCacheMu.Lock()
	paneCacheTime = time.Now().Add(-1 * time.Hour)
	paneCacheMu.Unlock()
	t.Cleanup(func() {
		paneCacheMu.Lock()
		paneCacheTime = time.Time{}
		paneCacheMu.Unlock()
	})
}
