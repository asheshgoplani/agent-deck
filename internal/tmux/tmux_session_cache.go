package tmux

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

// serverAlive tracks whether the tmux server is responsive.
// When the server is dead, all subprocess calls take ~3s to fail.
// This flag short-circuits expensive status loops to prevent UI freezes.
var (
	serverAliveMu   sync.RWMutex
	serverAliveVal  = true
	serverAliveTime time.Time
)

// IsServerAlive returns whether the tmux server was recently reachable.
// Result is cached for 5 seconds to avoid redundant checks.
func IsServerAlive() bool {
	serverAliveMu.RLock()
	if !serverAliveTime.IsZero() && time.Since(serverAliveTime) < 5*time.Second {
		alive := serverAliveVal
		serverAliveMu.RUnlock()
		return alive
	}
	serverAliveMu.RUnlock()

	// Quick probe: 1-second timeout
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	out, err := commandCombinedOutput(tmuxExecContext(ctx, DefaultSocketName(), "list-sessions", "-F", "#{session_name}"))
	alive := err == nil || (!strings.Contains(string(out), "server exited") &&
		!strings.Contains(string(out), "lost server") &&
		ctx.Err() != context.DeadlineExceeded)

	// "no server running" with quick response is fine - server just has no sessions
	if err != nil && strings.Contains(string(out), "no server running") {
		alive = true
	}

	serverAliveMu.Lock()
	serverAliveVal = alive
	serverAliveTime = time.Now()
	serverAliveMu.Unlock()

	if !alive {
		perfLog.Warn("tmux_server_dead")
	}
	return alive
}

// Session cache - reduces subprocess spawns from O(n) to O(1) per tick
// Instead of calling `tmux has-session` and `tmux display-message` for each session,
// we call `tmux list-sessions` ONCE and cache both existence and activity timestamps
var (
	sessionCacheMu   sync.RWMutex
	sessionCacheData map[string]int64 // session_name -> activity_timestamp (0 if not in cache)
	sessionCacheTime time.Time
)

// sessionCacheTTL is the single TTL governing both sessionExistsFromCache
// and sessionActivityFromCache. 2 seconds = 4 ticks at 500ms. Both readers
// MUST consult this constant — splitting the TTL between them produces the
// "session is alive but has no activity" parity bug (#886 family).
const sessionCacheTTL = 2 * time.Second

// sessionCacheStale reports whether the shared session cache is past TTL
// or empty. Caller must hold sessionCacheMu (read or write). Centralizing
// the check ensures both existence and activity readers expire the cache
// together — see arch-review S2 for why two divergent in-line checks
// caused #886-class drift.
func sessionCacheStale() bool {
	return sessionCacheData == nil || time.Since(sessionCacheTime) > sessionCacheTTL
}

// RefreshSessionCache updates the cache of existing tmux sessions and their activity
// Call this ONCE per tick, then use Session.Exists() and Session.GetWindowActivity()
// which read from cache. This reduces 30+ subprocess spawns to just 1 per tick cycle.
//
// Tries PipeManager first (zero subprocess), falls back to subprocess.
//
// NOTE: We use window_activity (not session_activity) because window_activity updates
// when there's actual terminal output, while session_activity only updates on
// session-level events. This is critical for detecting when Claude is actively working.
func RefreshSessionCache() {
	finish := logging.TraceOp(perfLog, "refresh_session_cache", 100*time.Millisecond)
	defer finish()

	// Try control mode pipe first (zero subprocess)
	if pm := GetPipeManager(); pm != nil {
		if activities, windows, err := pm.RefreshAllActivities(); err == nil && len(activities) > 0 {
			sessionCacheMu.Lock()
			sessionCacheData = activities
			sessionCacheTime = time.Now()
			sessionCacheMu.Unlock()

			windowCacheMu.Lock()
			windowCacheData = windows
			windowCacheTime = time.Now()
			windowCacheMu.Unlock()
			return
		}
		// Pipe failed: log it so we can verify zero subprocess usage
		statusLog.Debug("refresh_cache_subprocess_fallback")
	}

	// Subprocess fallback: list-windows -a (3s timeout to prevent freeze when server is dead)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := tmuxExecContext(ctx, DefaultSocketName(), "list-windows", "-a", "-F", tmuxFmt("#{session_name}", "#{window_activity}", "#{window_index}", "#{window_id}", "#{window_name}"))
	output, err := commandOutput(cmd)
	// "no server running" is an authoritative empty listing, not a failed
	// one. Caching it as invalid sent every session's status check to its
	// own has-session fork each pass (~37 forks/s with 492 dead rows).
	if err != nil && ctx.Err() == nil && isEmptyTmuxServerResult(err) {
		output, err = nil, nil
	}
	if err != nil {
		sessionCacheMu.Lock()
		sessionCacheData = nil
		sessionCacheTime = time.Time{}
		sessionCacheMu.Unlock()
		return
	}

	newSessionCache, newWindowCache := parseListWindowsOutput(string(output))

	sessionCacheMu.Lock()
	sessionCacheData = newSessionCache
	sessionCacheTime = time.Now()
	sessionCacheMu.Unlock()

	windowCacheMu.Lock()
	windowCacheData = newWindowCache
	windowCacheTime = time.Now()
	windowCacheMu.Unlock()
}

// parseListWindowsOutput parses the output of `tmux list-windows -a` with the
// extended format tmuxFmt("#{session_name}", "#{window_activity}",
// "#{window_index}", "#{window_id}", "#{window_name}"). window_name is last
// so a tmuxFieldSep inside it survives SplitN.
// Returns session-level max activity and per-session window info.
func parseListWindowsOutput(output string) (map[string]int64, map[string][]WindowInfo) {
	sessionCache := make(map[string]int64)
	windowCache := make(map[string][]WindowInfo)

	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, tmuxFieldSep, 5)
		if len(parts) < 2 {
			continue
		}
		name := parts[0]
		var activity int64
		_, _ = fmt.Sscanf(parts[1], "%d", &activity)

		// Session-level: keep max activity
		if existing, ok := sessionCache[name]; !ok || activity > existing {
			sessionCache[name] = activity
		}

		// Window-level: only if we have index, id and name fields
		if len(parts) == 5 {
			var idx int
			_, _ = fmt.Sscanf(parts[2], "%d", &idx)
			windowCache[name] = append(windowCache[name], WindowInfo{
				Index:    idx,
				ID:       parts[3],
				Name:     parts[4],
				Activity: activity,
			})
		}
	}

	return sessionCache, windowCache
}

// RefreshExistingSessions is an alias for RefreshSessionCache for backwards compatibility
func RefreshExistingSessions() {
	RefreshSessionCache()
}

// HasSessionOnSocket probes for a session on an explicit tmux server. Pass
// Instance.TmuxSocketName (or Session.SocketName) verbatim; empty means the
// user's default server.
func HasSessionOnSocket(socketName, name string) bool {
	return tmuxSessionExistsOnSocket(socketName, name)
}

// Per-socket session cache — backs ExistsCached() for sessions that live on an
// ISOLATED socket (SocketName != DefaultSocketName()).
//
// The shared sessionCache above describes DefaultSocketName() only, and #755
// forbids answering an isolated-socket session from it (a same-named entry is a
// different session). PipeManager only holds control connections for wanted
// (focused/attached) sessions. So without this cache, ExistsCached() had no
// source of truth at all for an isolated-socket session and returned false on
// every tick forever — silently starving those sessions of EnsureConfigured /
// SyncSessionIDsToTmux and of COLORFGBG theme propagation.
//
// Refresh is stale-while-revalidate and keyed by SOCKET: one `list-sessions`
// subprocess per distinct socket per TTL, spawned on a background goroutine.
// Readers never block and never spawn — preserving the invariant that made
// ExistsCached() safe for loops that walk every session each tick. Socket count
// is O(1)-ish (a handful), never O(sessions), so this cannot recreate the
// subprocess storm.
const socketSessionCacheTTL = 5 * time.Second

type socketSessionsEntry struct {
	names       map[string]struct{}
	refreshedAt time.Time
	refreshing  bool
	warm        bool // a probe has completed at least once (success or failure)
}

var (
	socketSessionCacheMu sync.Mutex
	socketSessionCache   = map[string]*socketSessionsEntry{}

	// listSessionsOnSocket is a package var so tests can exercise the cache
	// without a live tmux server.
	listSessionsOnSocket = defaultListSessionsOnSocket
)

// ListSessionNamesOnSocket returns the set of live session names on one tmux
// socket using a SINGLE bounded `list-sessions`. It is the socket-complete
// alternative to probing each session with `has-session`: callers that need
// liveness for N sessions should group them by socket and call this once per
// distinct socket (O(sockets) subprocesses instead of O(sessions)).
//
// An error means the probe was indeterminate (timed out). Callers MUST treat
// that as "assume alive" — never as "no sessions" — or a briefly-wedged server
// will look like a pile of dead sessions. A successful probe returning an empty
// set is authoritative (server present, no sessions / no server running).
func ListSessionNamesOnSocket(socketName string) (map[string]struct{}, error) {
	return listSessionsOnSocket(socketName)
}

// defaultListSessionsOnSocket returns the set of session names on the given
// tmux socket via a single bounded `list-sessions`. A server with no sessions
// (or no server at all) is an empty set, not an error.
func defaultListSessionsOnSocket(socketName string) (map[string]struct{}, error) {
	ctx, cancel := context.WithTimeout(context.Background(), hasSessionProbeTimeout)
	defer cancel()

	out, err := commandOutput(tmuxExecContext(ctx, socketName, "list-sessions", "-F", "#{session_name}"))
	if err != nil {
		// "no server running" / "no sessions" are legitimate empty results; any
		// other failure (timeout, exec error) is reported so the caller keeps
		// the previous entry instead of flapping every session to "gone".
		if ctx.Err() == context.DeadlineExceeded {
			return nil, ctx.Err()
		}
		if isEmptyTmuxServerResult(err) {
			return map[string]struct{}{}, nil
		}
		return nil, err
	}

	names := map[string]struct{}{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			names[line] = struct{}{}
		}
	}
	return names, nil
}

// isEmptyTmuxServerResult distinguishes tmux's expected empty-server exits
// from launch, permission, and other probe failures. exec.Cmd.Output stores
// the command's stderr on ExitError; err.Error() alone does not include it.
func isEmptyTmuxServerResult(err error) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	stderr := strings.ToLower(strings.TrimSpace(string(exitErr.Stderr)))
	// "error connecting to <path> (<reason>)" is never an empty result, not
	// even for ENOENT: a missing socket file is indistinguishable from a live
	// server whose socket was unlinked (tmp cleaners) or that lives under
	// another TMUX_TMPDIR. Reporting it as empty would mark healthy sessions
	// absent, so it stays unknown. The prefix check also keeps a socket path
	// that merely contains "no server running" from matching below.
	if strings.HasPrefix(stderr, "error connecting to ") {
		return false
	}
	return strings.Contains(stderr, "no server running") || strings.Contains(stderr, "no sessions")
}

// sessionExistsOnSocketCached answers "is <name> live on <socketName>?" from
// the per-socket cache, never blocking. On a cold or stale entry it kicks a
// single background refresh for that socket (at most one in flight per socket)
// and answers from whatever it currently knows: false when cold.
func sessionExistsOnSocketCached(socketName, name string) bool {
	socket := strings.TrimSpace(socketName)

	socketSessionCacheMu.Lock()
	defer socketSessionCacheMu.Unlock()

	entry, ok := socketSessionCache[socket]
	if !ok {
		entry = &socketSessionsEntry{names: map[string]struct{}{}}
		socketSessionCache[socket] = entry
	}

	stale := !entry.warm || time.Since(entry.refreshedAt) > socketSessionCacheTTL
	if stale && !entry.refreshing {
		entry.refreshing = true
		// Snapshot the lister under the lock: the background goroutine must not
		// read the package var concurrently with a test swapping it.
		go refreshSocketSessionCache(socket, listSessionsOnSocket)
	}

	_, exists := entry.names[name]
	return exists
}

// refreshSocketSessionCache re-lists one socket off the caller's goroutine and
// replaces its entry. A probe error (timeout) leaves the previous name set in
// place so a briefly-wedged server does not flap every session to "not found";
// a successful probe is authoritative, including an empty result.
func refreshSocketSessionCache(socket string, list func(string) (map[string]struct{}, error)) {
	names, err := list(socket)

	socketSessionCacheMu.Lock()
	defer socketSessionCacheMu.Unlock()

	entry, ok := socketSessionCache[socket]
	if !ok {
		entry = &socketSessionsEntry{names: map[string]struct{}{}}
		socketSessionCache[socket] = entry
	}
	entry.refreshing = false
	entry.refreshedAt = time.Now()
	entry.warm = true
	if err == nil {
		entry.names = names
	}
}

// ResetSocketSessionCacheForTest clears the per-socket cache between tests.
func ResetSocketSessionCacheForTest() {
	socketSessionCacheMu.Lock()
	defer socketSessionCacheMu.Unlock()
	socketSessionCache = map[string]*socketSessionsEntry{}
}

// sessionExistsFromCache checks if a session exists using the cached data
// Returns (exists, cacheValid) - if cache is stale/empty, cacheValid is false
func sessionExistsFromCache(name string) (bool, bool) {
	sessionCacheMu.RLock()
	defer sessionCacheMu.RUnlock()

	if sessionCacheStale() {
		return false, false
	}

	_, exists := sessionCacheData[name]
	return exists, true
}

// registerSessionInCache adds a newly created session to the cache
// This prevents the race condition where a new session isn't found
// because the cache was refreshed before the session was created
func registerSessionInCache(name string) {
	sessionCacheMu.Lock()
	defer sessionCacheMu.Unlock()

	// Initialize cache if nil
	if sessionCacheData == nil {
		sessionCacheData = make(map[string]int64)
	}

	// Add session with current time as activity
	sessionCacheData[name] = time.Now().Unix()
}

// sessionActivityFromCache gets session activity timestamp from cache
// Returns (activity, cacheValid) - if cache is stale/empty, cacheValid is false
func sessionActivityFromCache(name string) (int64, bool) {
	sessionCacheMu.RLock()
	defer sessionCacheMu.RUnlock()

	if sessionCacheStale() {
		return 0, false
	}

	activity, exists := sessionCacheData[name]
	if !exists {
		return 0, false // Session not in cache (doesn't exist)
	}
	return activity, true
}
