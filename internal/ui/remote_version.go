package ui

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// remoteVersionCheckInterval bounds how often the remote poll asks a remote
// for `agent-deck version`: once per hour per remote, not per tick (#2164).
const remoteVersionCheckInterval = time.Hour

// remoteVersionStale reports whether a remote's version should be re-asked
// on this poll: never checked, or checked longer ago than the interval.
func remoteVersionStale(state session.RemoteVersionState, ok bool, now time.Time) bool {
	return !ok || state.CheckedAt.IsZero() || now.Sub(state.CheckedAt) >= remoteVersionCheckInterval
}

// remoteVersionMarker is the ` v1.15.0 ↑` header suffix shown when the remote
// runs an older release than this controller. Empty when the version is
// unknown, current, newer, or the controller is not a release build.
func remoteVersionMarker(state session.RemoteVersionState, controller string) string {
	if !state.Outdated(controller) {
		return ""
	}
	return " v" + state.Version + " ↑"
}

// renderRemoteVersionMarker styles remoteVersionMarker for the header row.
func renderRemoteVersionMarker(state session.RemoteVersionState, controller string, selected bool) string {
	text := remoteVersionMarker(state, controller)
	if text == "" {
		return ""
	}
	style := lipgloss.NewStyle().Foreground(lipgloss.Color("3")) // yellow: drift, not failure
	if selected {
		style = style.Bold(true)
	}
	return style.Render(text)
}

// remoteVersionChecker is the optional part of a remote fetch runner that
// can ask the remote for `agent-deck version`; session.SSHRunner satisfies
// it, test stubs need not.
type remoteVersionChecker interface {
	CheckBinary(ctx context.Context) (string, bool)
}

// remoteVersionNeedsCheck reports whether this poll should ask remoteName
// for its version (see remoteVersionStale).
func (h *Home) remoteVersionNeedsCheck(remoteName string, now time.Time) bool {
	h.remoteSessionsMu.RLock()
	defer h.remoteSessionsMu.RUnlock()
	state, ok := h.remoteVersions[remoteName]
	return remoteVersionStale(state, ok, now)
}

// remoteVersionState returns the cached version state for a remote.
func (h *Home) remoteVersionState(remoteName string) (session.RemoteVersionState, bool) {
	h.remoteSessionsMu.RLock()
	defer h.remoteSessionsMu.RUnlock()
	state, ok := h.remoteVersions[remoteName]
	return state, ok
}

// remoteUpdatedMsg reports the outcome of a TUI-driven remote update.
type remoteUpdatedMsg struct {
	remoteName string
	from, to   string
	err        error
}

// deployRemoteUpdate performs the verified binary deploy for one remote. A
// package variable so tests substitute a stub and count calls instead of
// opening SSH; production uses the same path as `remote update <name>`.
var deployRemoteUpdate = func(ctx context.Context, name string, rc session.RemoteConfig, target string) (string, error) {
	return session.DeployRemoteBinary(ctx, session.NewSSHRunner(name, rc), target, session.RemoteUpdateOptions{})
}

// updateRemote runs the confirmed update for one remote header.
func (h *Home) updateRemote(remoteName, from, to string) tea.Cmd {
	return func() tea.Msg {
		config, err := session.LoadUserConfig()
		if err != nil || config == nil {
			return remoteUpdatedMsg{remoteName: remoteName, from: from, to: to, err: fmt.Errorf("failed to load remote config")}
		}
		rc, ok := config.Remotes[remoteName]
		if !ok {
			return remoteUpdatedMsg{remoteName: remoteName, from: from, to: to, err: fmt.Errorf("remote '%s' not found", remoteName)}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		deployed, err := deployRemoteUpdate(ctx, remoteName, rc, to)
		if err == nil && deployed != "" {
			to = deployed
		}
		return remoteUpdatedMsg{remoteName: remoteName, from: from, to: to, err: err}
	}
}

// recordRemoteVersions merges freshly observed states into the in-memory map
// and the shared on-disk cache (so `remote list` shows the same answer).
func (h *Home) recordRemoteVersions(states map[string]session.RemoteVersionState) {
	if len(states) == 0 {
		return
	}
	h.remoteSessionsMu.Lock()
	if h.remoteVersions == nil {
		h.remoteVersions = make(map[string]session.RemoteVersionState)
	}
	for name, state := range states {
		h.remoteVersions[name] = state
	}
	h.remoteSessionsMu.Unlock()
	if err := session.RecordRemoteVersions(states); err != nil {
		uiLog.Warn("save_remote_versions_failed", slog.String("error", err.Error()))
	}
}
