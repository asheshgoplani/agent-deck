package session

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

// RemotePollState describes the last session-list poll. Identity prevents a
// renamed or reconfigured remote from inheriting another host's auth block.
type RemotePollState struct {
	Host           string    `json:"host"`
	Profile        string    `json:"profile"`
	AgentDeckPath  string    `json:"agent_deck_path"`
	LastPollMS     *int64    `json:"last_poll_ms"`
	LastPollStatus string    `json:"last_poll_status"`
	LastPollError  string    `json:"last_poll_error"`
	CheckedAt      time.Time `json:"checked_at"`
}

func (s RemotePollState) Matches(rc RemoteConfig) bool {
	return s.Host == rc.Host && s.Profile == rc.GetProfile() && s.AgentDeckPath == rc.GetAgentDeckPath()
}

// LoadRemotePolls reads locally cached observations without contacting SSH.
// Writers replace the cache atomically, so readers need not wait on the writer
// mutex or its cross-process claim lock.
func LoadRemotePolls() map[string]RemotePollState {
	polls := loadRemoteVersionCache().Polls
	if polls == nil {
		return map[string]RemotePollState{}
	}
	return polls
}

// RecordRemotePoll stores a safe reason, never raw SSH stderr. Cache writes use
// the existing cross-process lock and atomic replacement of the version cache.
func RecordRemotePoll(name string, rc RemoteConfig, elapsed time.Duration, err error) RemotePollState {
	ms := max(int64(0), elapsed.Milliseconds())
	status, reason := remotePollError(err)
	state := RemotePollState{Host: rc.Host, Profile: rc.GetProfile(), AgentDeckPath: rc.GetAgentDeckPath(), LastPollMS: &ms, LastPollStatus: status, LastPollError: reason, CheckedAt: time.Now()}
	saveErr := updateRemoteVersionCache(func(cache *remoteVersionCache) {
		if cache.Polls == nil {
			cache.Polls = map[string]RemotePollState{}
		}
		cache.Polls[name] = state
	})
	if saveErr != nil {
		logging.ForComponent(logging.CompSession).Warn("remote_poll_cache_write_failed", slog.String("remote", name), slog.String("error", saveErr.Error()))
	}
	return state
}

// ResetRemotePoll explicitly releases an authentication block for a new attempt.
func ResetRemotePoll(name string) error {
	return updateRemoteVersionCache(func(cache *remoteVersionCache) { delete(cache.Polls, name) })
}

func remotePollError(err error) (string, string) {
	if err == nil {
		return "ok", ""
	}
	if errors.Is(err, context.Canceled) {
		return "unknown", "poll canceled"
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "permission denied ("), strings.Contains(message, "authentication failed"), strings.Contains(message, "tailscale ssh requires"), strings.Contains(message, "reauthentication"), strings.Contains(message, "authenticate to continue"):
		return "auth_failed", "auth failed"
	case errors.Is(err, context.DeadlineExceeded), strings.Contains(message, "timed out"), strings.Contains(message, "timeout"):
		return "timeout", "timeout"
	case strings.Contains(message, "connection refused"), strings.Contains(message, "no route to host"), strings.Contains(message, "network is unreachable"), strings.Contains(message, "could not resolve hostname"), strings.Contains(message, "host is down"):
		return "host_down", "host down"
	default:
		return "error", "poll failed"
	}
}
