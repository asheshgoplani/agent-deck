package tmux

import (
	"context"
	"sync"
	"time"
)

// Per-socket viewer cache behind the TUI's row badge and preview line.
//
// Mirrors the per-socket session cache: stale-while-revalidate, one
// `list-clients` subprocess per distinct socket per TTL, refreshed on a
// background goroutine. Readers never block and never spawn, so the render
// path can ask for every visible row's viewers each frame at zero cost.
const viewersCacheTTL = 2 * time.Second

type viewersEntry struct {
	bySession   map[string][]Viewer
	refreshedAt time.Time
	refreshing  bool
	warm        bool // a listing has completed at least once
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
// from the cache, never blocking. known is false until the socket's first
// listing completes (render "unknown", not "nobody"). A cold or stale entry
// kicks one background refresh for that socket.
func ViewersCached(socketName, sessionName string) (viewers []Viewer, known bool) {
	viewersCacheMu.Lock()
	entry, ok := viewersCache[socketName]
	if !ok {
		entry = &viewersEntry{}
		viewersCache[socketName] = entry
	}
	stale := !entry.warm || time.Since(entry.refreshedAt) >= viewersCacheTTL
	if stale && !entry.refreshing {
		entry.refreshing = true
		go refreshViewersOnSocket(socketName)
	}
	viewers, known = entry.bySession[sessionName], entry.warm
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
		// Keep what was known rather than flapping every row to "nobody".
		return
	}
	entry.bySession = bySession
	entry.warm = true
}

// ResetViewersCacheForTest clears the cache so a test starts cold.
func ResetViewersCacheForTest() {
	viewersCacheMu.Lock()
	defer viewersCacheMu.Unlock()
	viewersCache = map[string]*viewersEntry{}
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
	viewersCache[socketName] = &viewersEntry{bySession: bySession, refreshedAt: time.Now().Add(time.Hour), warm: true}
}
