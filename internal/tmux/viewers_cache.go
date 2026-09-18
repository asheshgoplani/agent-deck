package tmux

import (
	"context"
	"sync"
	"time"
)

// Per-socket viewer cache behind the TUI's row badge and preview line.
//
// Mirrors the per-socket session cache: stale-while-revalidate, at most one
// `list-clients` subprocess per distinct socket per TTL, refreshed on a
// background goroutine. Readers never block and never spawn, so the render
// path can ask for every visible row's viewers each frame at zero cost.
//
// A listing that fails is cached too (negative caching): the entry answers
// "unknown" until the next refresh is due, and each further failure doubles
// the wait up to viewersCacheMaxBackoff. Without that a socket whose server
// is down (after a reboot, or a profile whose sessions are all stopped) would
// start a listing on every render, because the failed one leaves nothing to
// answer from.
const (
	viewersCacheTTL        = 2 * time.Second
	viewersCacheMaxBackoff = 30 * time.Second
)

type viewersEntry struct {
	bySession   map[string][]Viewer
	refreshedAt time.Time // when the last listing (success or failure) landed
	nextRefresh time.Time // earliest time a new listing may start
	refreshing  bool      // a listing is in flight; never start a second one
	known       bool      // the last listing succeeded, so bySession is the answer
	failures    int       // consecutive failed listings, for the backoff
}

var (
	viewersCacheMu sync.Mutex
	viewersCache   = map[string]*viewersEntry{}

	// listAllViewersOnSocket is a package var so tests can drive the cache
	// without a live tmux server.
	listAllViewersOnSocket = func(socketName string) (map[string][]Viewer, error) {
		ctx, cancel := context.WithTimeout(context.Background(), tmuxPollTimeout)
		defer cancel()
		return ListAllViewers(ctx, socketName)
	}
)

// ViewersCached answers "who is attached to sessionName on socketName?"
// from the cache, never blocking. known is false until the socket's last
// listing succeeded (render "unknown", not "nobody"). A cold or due entry
// kicks one background refresh for that socket; a refresh already in flight
// is never doubled.
func ViewersCached(socketName, sessionName string) (viewers []Viewer, known bool) {
	viewersCacheMu.Lock()
	entry, ok := viewersCache[socketName]
	if !ok {
		entry = &viewersEntry{}
		viewersCache[socketName] = entry
	}
	if !entry.refreshing && !time.Now().Before(entry.nextRefresh) {
		entry.refreshing = true
		go refreshViewersOnSocket(socketName)
	}
	viewers, known = entry.bySession[sessionName], entry.known
	viewersCacheMu.Unlock()
	return viewers, known
}

func refreshViewersOnSocket(socketName string) {
	bySession, err := listAllViewersOnSocket(socketName)
	viewersCacheMu.Lock()
	defer viewersCacheMu.Unlock()
	entry := viewersCache[socketName]
	if entry == nil {
		return
	}
	entry.refreshing = false
	entry.refreshedAt = time.Now()
	if err != nil {
		entry.bySession, entry.known = nil, false
		entry.failures++
		entry.nextRefresh = entry.refreshedAt.Add(viewersRetryDelay(entry.failures))
		return
	}
	entry.bySession, entry.known = bySession, true
	entry.failures = 0
	entry.nextRefresh = entry.refreshedAt.Add(viewersCacheTTL)
}

// viewersRetryDelay is the wait after the n-th consecutive failed listing:
// one TTL, then doubling, capped.
func viewersRetryDelay(failures int) time.Duration {
	delay := viewersCacheTTL
	for i := 1; i < failures && delay < viewersCacheMaxBackoff; i++ {
		delay *= 2
	}
	return min(delay, viewersCacheMaxBackoff)
}

// ResetViewersCacheForTest clears the cache so a test starts cold.
func ResetViewersCacheForTest() {
	viewersCacheMu.Lock()
	defer viewersCacheMu.Unlock()
	viewersCache = map[string]*viewersEntry{}
}

// expireViewersCacheForTest makes the socket's next refresh due now.
func expireViewersCacheForTest(socketName string) {
	viewersCacheMu.Lock()
	defer viewersCacheMu.Unlock()
	if entry := viewersCache[socketName]; entry != nil {
		entry.nextRefresh = time.Time{}
	}
}

// SeedViewersCacheForTest fills the socket's entry as if a listing had
// completed, so render tests can show viewers without a tmux server. A nil
// bySession seeds the unknown state (a listing in flight that never lands),
// so the render path neither knows the viewers nor spawns tmux.
func SeedViewersCacheForTest(socketName string, bySession map[string][]Viewer) {
	viewersCacheMu.Lock()
	defer viewersCacheMu.Unlock()
	if bySession == nil {
		viewersCache[socketName] = &viewersEntry{refreshing: true}
		return
	}
	viewersCache[socketName] = &viewersEntry{bySession: bySession, nextRefresh: time.Now().Add(time.Hour), known: true}
}
