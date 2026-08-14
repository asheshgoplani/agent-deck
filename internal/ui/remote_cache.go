package ui

import (
	"encoding/json"
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

type remoteSessionsCache struct {
	SavedAt  time.Time                              `json:"saved_at"`
	Sessions map[string][]session.RemoteSessionInfo `json:"sessions"`
}

// saveRemoteSessionsCache persists the current remote session map. Called from
// the fetch-completion path; a dropped save is recoverable on the next fetch.
func (h *Home) saveRemoteSessionsCache() {
	if h.storage == nil {
		return
	}
	db := h.storage.GetDB()
	if db == nil {
		return
	}
	h.remoteSessionsMu.RLock()
	snap := remoteSessionsCache{SavedAt: time.Now(), Sessions: h.remoteSessions}
	data, err := json.Marshal(snap)
	h.remoteSessionsMu.RUnlock()
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
	if h.storage == nil {
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
	if snap.SavedAt.IsZero() || time.Since(snap.SavedAt) > remoteSessionsCacheMaxAge {
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
	for name, sessions := range snap.Sessions {
		if _, live := h.remoteSessions[name]; !live {
			h.remoteSessions[name] = sessions
			h.remoteFromCache[name] = true
		}
	}
	h.remoteSessionsMu.Unlock()
}
