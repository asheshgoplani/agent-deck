package ui

import (
	"encoding/json"
	"hash/fnv"
	"log/slog"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// remoteSessionsCacheKey is the meta-table key holding the last-known remote
// session lists. Large remote fleets can take tens of seconds to answer
// `list --json`, and until this cache existed the TUI rendered NOTHING for a
// remote until the first live fetch returned — on every single startup. The
// cache renders the last-known fleet instantly; the normal fetch cycle then
// overwrites it with live data (stale-while-refresh).
const remoteSessionsCacheKey = "remote_sessions_cache"

// remoteSessionsCacheMaxAge bounds how old a cached snapshot may be before it
// is ignored: a week-old fleet is more misleading than an empty group.
const remoteSessionsCacheMaxAge = 24 * time.Hour

// remoteSessionsCacheEnabled gates the on-disk half of the cache (the save on
// fetch completion and the load in NewHome). Every test in this package shares
// one storage profile, so a test that pumps a remote fetch persists a snapshot
// that every later NewHome() silently inherits — those phantom remotes then
// land in countSessionStatuses and inflate the status counters of unrelated
// tests. TestMain turns this off, in the same spirit as
// homeBackgroundWorkersEnabled; the round-trip test re-enables it for its own
// scope. Production never flips it.
var remoteSessionsCacheEnabled = true

type remoteSessionsCache struct {
	// SavedAt is kept for backward compatibility with early snapshots; the
	// authoritative freshness signal is per-remote FetchedAt.
	SavedAt  time.Time                              `json:"saved_at"`
	Sessions map[string][]session.RemoteSessionInfo `json:"sessions"`
	// FetchedAt records when each remote's sessions last came from a LIVE
	// fetch. Without it, every save cycle re-stamped the whole snapshot, so a
	// continuously-failing remote's stale sessions never hit the age cutoff.
	FetchedAt map[string]time.Time `json:"fetched_at,omitempty"`
}

// remoteSessionsCacheSaveInterval is the shortest gap between two writes of
// the snapshot. A pushing remote can deliver several listings a second and
// each used to JSON-encode the whole fleet and rewrite it into state.db on
// the UI goroutine; the on-disk copy only has to be right at the next start.
const remoteSessionsCacheSaveInterval = 30 * time.Second

// remoteSessionsCacheRefreshAge bounds how long an unchanged fleet goes
// without a rewrite, so the on-disk stamps keep ahead of the load-time
// expiry (remoteSessionsCacheMaxAge) on a quiet fleet.
const remoteSessionsCacheRefreshAge = time.Hour

// remoteSessionsCacheDoc is the snapshot as written: the sessions are
// encoded once and hashed, so an unchanged fleet is not rewritten.
type remoteSessionsCacheDoc struct {
	SavedAt   time.Time            `json:"saved_at"`
	Sessions  json.RawMessage      `json:"sessions"`
	FetchedAt map[string]time.Time `json:"fetched_at,omitempty"`
}

// saveRemoteSessionsCache records that the remote session map changed and
// writes it when the debounce allows (see flushRemoteSessionsCache; a dropped
// save is recoverable on the next fetch). liveFetched names the remotes whose
// data came from a successful fetch THIS cycle — only their freshness stamps
// advance. Failed or merely-carried-over remotes keep their previous stamp so
// stale data still ages out.
func (h *Home) saveRemoteSessionsCache(liveFetched map[string][]session.RemoteSessionInfo) {
	if !remoteSessionsCacheEnabled || h.storage == nil {
		return
	}
	h.remoteSessionsMu.Lock()
	if h.remoteFetchedAt == nil {
		h.remoteFetchedAt = make(map[string]time.Time)
	}
	for name := range liveFetched {
		h.remoteFetchedAt[name] = time.Now()
	}
	h.remoteCacheDirty = true
	h.remoteSessionsMu.Unlock()
	h.flushRemoteSessionsCache(false)
}

// flushRemoteSessionsCache writes the pending snapshot if there is one and
// the last write is at least remoteSessionsCacheSaveInterval old (force skips
// the wait: quit). The sessions are hashed so a fleet that has not changed
// since the last write is not rewritten until remoteSessionsCacheRefreshAge.
func (h *Home) flushRemoteSessionsCache(force bool) {
	if !remoteSessionsCacheEnabled || h.storage == nil {
		return
	}
	db := h.storage.GetDB()
	if db == nil {
		return
	}
	now := time.Now()
	h.remoteSessionsMu.Lock()
	if !h.remoteCacheDirty || (!force && now.Sub(h.remoteCacheLastSave) < remoteSessionsCacheSaveInterval) {
		h.remoteSessionsMu.Unlock()
		return
	}
	sessions, err := json.Marshal(h.remoteSessions)
	if err != nil {
		h.remoteSessionsMu.Unlock()
		uiLog.Warn("save_remote_cache_marshal_failed", slog.String("error", err.Error()))
		return
	}
	hash := fnv.New64a()
	_, _ = hash.Write(sessions)
	sum := hash.Sum64()
	h.remoteCacheDirty = false
	unchanged := sum == h.remoteCacheLastHash && now.Sub(h.remoteCacheLastSave) < remoteSessionsCacheRefreshAge
	if unchanged {
		h.remoteSessionsMu.Unlock()
		return
	}
	h.remoteCacheLastSave = now
	h.remoteCacheLastHash = sum
	doc := remoteSessionsCacheDoc{
		SavedAt:   now,
		Sessions:  sessions,
		FetchedAt: h.remoteFetchedAt,
	}
	data, err := json.Marshal(doc)
	h.remoteSessionsMu.Unlock()
	if err != nil {
		uiLog.Warn("save_remote_cache_marshal_failed", slog.String("error", err.Error()))
		return
	}
	if err := db.SetMeta(remoteSessionsCacheKey, string(data)); err != nil {
		uiLog.Warn("save_remote_cache_failed", slog.String("error", err.Error()))
	}
}

// loadRemoteSessionsCache seeds the remote session map from the last-known
// snapshot so remotes render on the first paint. Live fetches overwrite it.
func (h *Home) loadRemoteSessionsCache() {
	if !remoteSessionsCacheEnabled || h.storage == nil {
		return
	}
	db := h.storage.GetDB()
	if db == nil {
		return
	}
	val, err := db.GetMeta(remoteSessionsCacheKey)
	if err != nil || val == "" {
		return
	}
	var snap remoteSessionsCache
	if err := json.Unmarshal([]byte(val), &snap); err != nil {
		uiLog.Warn("load_remote_cache_unmarshal_failed", slog.String("error", err.Error()))
		return
	}
	if len(snap.Sessions) == 0 {
		return
	}
	h.applyRemoteSessionsSnapshot(snap)
}

// applyRemoteSessionsSnapshot seeds cached sessions for remotes that have no
// live data yet and flags them so the header can disclose the staleness.
func (h *Home) applyRemoteSessionsSnapshot(snap remoteSessionsCache) {
	h.remoteSessionsMu.Lock()
	if h.remoteSessions == nil {
		h.remoteSessions = make(map[string][]session.RemoteSessionInfo)
	}
	if h.remoteFromCache == nil {
		h.remoteFromCache = make(map[string]bool)
	}
	if h.remoteFetchedAt == nil {
		h.remoteFetchedAt = make(map[string]time.Time)
	}
	for name, sessions := range snap.Sessions {
		if _, live := h.remoteSessions[name]; live {
			continue
		}
		// Per-remote freshness: fall back to the snapshot stamp for early
		// caches written before FetchedAt existed.
		fetched := snap.FetchedAt[name]
		if fetched.IsZero() {
			fetched = snap.SavedAt
		}
		if fetched.IsZero() || time.Since(fetched) > remoteSessionsCacheMaxAge {
			continue
		}
		// RemoteSessionInfo.RemoteName is `json:"-"` — it is assigned locally
		// from the remote's config name, never carried in the payload (see
		// internal/session/ssh.go). It therefore does not survive this
		// snapshot's own round trip either, so restore it the same way the
		// live fetch path does. Without this, every cached session renders
		// and routes with an empty remote name until live data lands, which
		// is exactly the startup window this cache exists to cover.
		for i := range sessions {
			sessions[i].RemoteName = name
		}
		h.remoteSessions[name] = sessions
		h.remoteFromCache[name] = true
		h.remoteFetchedAt[name] = fetched
	}
	h.remoteSessionsMu.Unlock()
}
