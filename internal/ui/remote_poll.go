package ui

import (
	"fmt"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// seedRemotePolls publishes configured hosts even before their first successful
// listing. Observations are identity-bound; a repointed name starts unknown.
func (h *Home) seedRemotePolls(remotes map[string]session.RemoteConfig) {
	h.remotePollMu.Lock()
	defer h.remotePollMu.Unlock()
	states := session.LoadRemotePolls()
	h.remoteSessionsMu.Lock()
	defer h.remoteSessionsMu.Unlock()
	if h.remotePolls == nil {
		h.remotePolls = make(map[string]session.RemotePollState)
	}
	if h.remoteSessions == nil {
		h.remoteSessions = make(map[string][]session.RemoteSessionInfo)
	}
	for name, rc := range remotes {
		if old, exists := h.remotePollConfig[name]; exists && (old.Host != rc.Host || old.GetProfile() != rc.GetProfile() || old.GetAgentDeckPath() != rc.GetAgentDeckPath()) {
			delete(h.remoteSessions, name)
			delete(h.remoteGroups, name)
			delete(h.remoteFromCache, name)
		}
		state := states[name]
		if !state.Matches(rc) {
			state = session.RemotePollState{LastPollStatus: "unknown"}
		}
		h.remotePolls[name] = state
		if _, exists := h.remoteSessions[name]; !exists {
			h.remoteSessions[name] = nil
		}
	}
	h.remotePollConfig = make(map[string]session.RemoteConfig, len(remotes))
	for name, rc := range remotes {
		h.remotePollConfig[name] = rc
	}
	for name := range h.remotePolls {
		if _, exists := remotes[name]; !exists {
			delete(h.remotePolls, name)
		}
	}
}

func (h *Home) remoteAuthBlocked(name string, rc session.RemoteConfig) bool {
	h.remoteSessionsMu.RLock()
	defer h.remoteSessionsMu.RUnlock()
	state := h.remotePolls[name]
	return state.Matches(rc) && state.LastPollStatus == "auth_failed"
}

func (h *Home) beginRemotePoll(name string, rc session.RemoteConfig) bool {
	h.remoteSessionsMu.Lock()
	defer h.remoteSessionsMu.Unlock()
	state := h.remotePolls[name]
	if h.ctx.Err() != nil || h.remotePollActive[name] || (state.Matches(rc) && state.LastPollStatus == "auth_failed") {
		return false
	}
	if h.remotePollActive == nil {
		h.remotePollActive = make(map[string]bool)
	}
	h.remotePollActive[name] = true
	return true
}

func (h *Home) finishRemotePoll(name string, rc session.RemoteConfig, started time.Time, err error) {
	h.remotePollMu.Lock()
	defer h.remotePollMu.Unlock()
	configured := h.remotePollConfigMatches(name, rc)
	// Shutdown cancellation is not a new observation about the host.
	if h.ctx.Err() == nil && configured {
		state := session.RecordRemotePoll(name, rc, time.Since(started), err)
		h.remoteSessionsMu.Lock()
		if h.remotePolls == nil {
			h.remotePolls = make(map[string]session.RemotePollState)
		}
		h.remotePolls[name] = state
		h.remoteSessionsMu.Unlock()
	}
	h.remoteSessionsMu.Lock()
	delete(h.remotePollActive, name)
	h.remoteSessionsMu.Unlock()
}

func (h *Home) remotePollUnavailable(name string) bool {
	h.remoteSessionsMu.RLock()
	defer h.remoteSessionsMu.RUnlock()
	state, known := h.remotePolls[name]
	return h.remoteFromCache[name] || (known && state.LastPollStatus != "ok")
}

// remoteRowStaleAge threshold (#2331): a poll that succeeds slowly (the
// remote's own status pass ran long under load) still returns a snapshot
// that is up to that long old by the time it renders, and remotePollUnavailable
// (above) says nothing about it — it only flags a poll that failed outright.
// remoteRowAge fills that gap using the same h.remoteFetchedAt stamp the
// on-disk startup cache already relies on for its own staleness check
// (remoteSessionsCacheMaxAge in remote_cache.go), just applied live instead
// of only at load time. remoteSessionsCacheSaveInterval (30s) is the
// shortest gap between two on-disk cache writes; a row is called stale once
// its in-memory fetch stamp is older than that same cadence, so the age
// marker appears no later than a healthy remote's own steady-state refresh
// rate would explain.
const remoteRowStaleAge = remoteSessionsCacheSaveInterval

// remoteRowAge reports how long ago name's rows last came from a live fetch
// (poll or push), and whether that is known at all — a remote polled since
// this process started always knows; one seeded purely from the on-disk
// startup cache reports its cached age via the same map (applyRemoteSessionsSnapshot
// sets remoteFetchedAt too), so a cold-start row and a slow-poll row render
// through the same "how old is this" path instead of two separate labels.
func (h *Home) remoteRowAge(name string) (time.Duration, bool) {
	h.remoteSessionsMu.RLock()
	fetched, known := h.remoteFetchedAt[name]
	h.remoteSessionsMu.RUnlock()
	if !known || fetched.IsZero() {
		return 0, false
	}
	return time.Since(fetched), true
}

// formatRemoteAge renders a stale-row age as "47s old" / "3m old", unlike
// humanizeSince (which floors anything under a minute to "just now" — wrong
// here, since remoteRowStaleAge itself is 30s, so every row this badge ever
// shows would otherwise read as "just now").
func formatRemoteAge(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d/time.Second))
	}
	return fmt.Sprintf("%dm", int(d/time.Minute))
}

// remoteRowStale reports whether name's rows are old enough to need the "·
// status Ns old" marker: known-stale-by-age, OR the poll itself is flagged
// unavailable/failed (remotePollUnavailable) but with no age on record yet
// (e.g. a remote configured this session that has never had a live fetch
// land — falls back to the plain "last known" wording callers already use).
func (h *Home) remoteRowStale(name string) (time.Duration, bool) {
	age, known := h.remoteRowAge(name)
	if known && age >= remoteRowStaleAge {
		return age, true
	}
	return 0, false
}

func (h *Home) remotePollConfigMatches(name string, rc session.RemoteConfig) bool {
	h.remoteSessionsMu.RLock()
	defer h.remoteSessionsMu.RUnlock()
	current, exists := h.remotePollConfig[name]
	return h.remotePollConfig == nil || (exists && current.Host == rc.Host && current.GetProfile() == rc.GetProfile() && current.GetAgentDeckPath() == rc.GetAgentDeckPath())
}

// retryRemotePoll resets the selected host's persisted pause off the UI loop,
// then schedules its normal fetch with the complete configured host list so
// applying the result preserves the other hosts.
func (h *Home) retryRemotePoll(name string) tea.Cmd {
	return func() tea.Msg {
		config, err := session.LoadUserConfig()
		if err != nil {
			return remotePollRetryFailedMsg{err}
		}
		rc, exists := config.Remotes[name]
		if !exists {
			return remotePollRetryFailedMsg{fmt.Errorf("remote %q is no longer configured", name)}
		}
		h.remotePollMu.Lock()
		h.remoteSessionsMu.RLock()
		active := h.remotePollActive[name]
		h.remoteSessionsMu.RUnlock()
		if active {
			h.remotePollMu.Unlock()
			return remotePollRetryFailedMsg{fmt.Errorf("remote %q poll is already in progress", name)}
		}
		err = session.ResetRemotePoll(name)
		h.remotePollMu.Unlock()
		if err != nil {
			return remotePollRetryFailedMsg{err}
		}
		h.seedRemotePolls(config.Remotes)
		names := make([]string, 0, len(config.Remotes))
		for configured := range config.Remotes {
			names = append(names, configured)
		}
		gen := atomic.AddUint64(&h.remoteFetchSeq, 1)
		return remoteFetchRoundMsg{gen: gen, fetches: []tea.Cmd{
			func() tea.Msg { return h.fetchOneRemote(gen, name, rc, names) },
		}}
	}
}

type remotePollRetryFailedMsg struct{ err error }
