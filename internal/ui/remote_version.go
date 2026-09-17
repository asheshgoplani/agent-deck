package ui

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/sysinfo"
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

// recordRemoteStats merges freshly observed stats snapshots into the
// in-memory map. Unlike recordRemoteVersions there is no on-disk cache: a
// live snapshot from a poll round ago is not worth persisting across a
// restart, and an absent entry already renders as "stats unknown".
func (h *Home) recordRemoteStats(stats map[string]remoteHostStatsResult) {
	if len(stats) == 0 {
		return
	}
	h.remoteSessionsMu.Lock()
	if h.remoteHostStats == nil {
		h.remoteHostStats = make(map[string]remoteHostStatsResult)
	}
	for name, result := range stats {
		h.remoteHostStats[name] = result
	}
	h.remoteSessionsMu.Unlock()
}

// remoteHostStatsState returns the cached stats snapshot for a remote.
func (h *Home) remoteHostStatsState(remoteName string) (remoteHostStatsResult, bool) {
	h.remoteSessionsMu.RLock()
	defer h.remoteSessionsMu.RUnlock()
	result, ok := h.remoteHostStats[remoteName]
	return result, ok
}

// remoteVersionPreviewLine renders the remote preview panel's version-compare
// line for its four states: same, older (with the update hint), newer, and
// unknown (never a guess — it says so and when it was last asked).
func remoteVersionPreviewLine(state session.RemoteVersionState, controller string) string {
	switch state.Compare(controller) {
	case session.RemoteVersionSame:
		if state.BuildDiffers(controller) {
			return "agent-deck v" + truncateRemoteVersionDisplay(state.Version) + " · same release, different build"
		}
		return "agent-deck v" + truncateRemoteVersionDisplay(state.Version) + " · same as here"
	case session.RemoteVersionOlder:
		return "agent-deck v" + truncateRemoteVersionDisplay(state.Version) + " · older than here (update available)"
	case session.RemoteVersionNewer:
		return "agent-deck v" + truncateRemoteVersionDisplay(state.Version) + " · newer than here"
	default:
		return "version unknown (last checked " + remoteVersionCheckedLabel(state) + ")"
	}
}

// remoteVersionCheckedLabel is the "(last checked ...)" clause: "never" when
// the remote has not been asked yet, else a relative time.
func remoteVersionCheckedLabel(state session.RemoteVersionState) string {
	if state.CheckedAt.IsZero() {
		return "never"
	}
	return humanizeSince(time.Since(state.CheckedAt))
}

// truncateRemoteVersionDisplay caps a reported version string (which may
// carry long build metadata, e.g. "1.16.10+local.a1b2c3d4e5f6") so the
// preview line never wraps a narrow pane.
func truncateRemoteVersionDisplay(v string) string {
	const maxLen = 24
	if len(v) <= maxLen {
		return v
	}
	return v[:maxLen-1] + "…"
}

// remoteStatsUnknownLine renders the "stats unknown" fallback honestly: the
// "runs an older agent-deck" clause is only ever true when the remote is, in
// fact, older by CompareVersions — a same-release remote (even on a
// different build) simply doesn't report stats for some other reason
// (walk defect #1's stats-line half).
func remoteStatsUnknownLine(state session.RemoteVersionState, controller string) string {
	if state.Compare(controller) == session.RemoteVersionOlder {
		return "stats unknown (remote runs an older agent-deck)"
	}
	return "stats unknown (remote does not report stats)"
}

// remoteStatsPreviewLines renders the stats block below the version line:
// sessions by status and running harnesses (both derived from the sessions
// this poll already fetched — no extra round trip), then the remote host's
// own load/memory/disk and the last poll's latency, or one line saying the
// stats are unknown when the remote never answered (or cannot: an older
// agent-deck without `system stats`).
func remoteStatsPreviewLines(sessions []session.RemoteSessionInfo, result remoteHostStatsResult, hasResult bool, state session.RemoteVersionState, controller string) []string {
	lines := []string{remoteSessionStatusLine(sessions), remoteHarnessLine(sessions)}
	if !hasResult || !result.Stats.Ok {
		return append(lines, remoteStatsUnknownLine(state, controller))
	}
	lines = append(lines, remoteHostLoadLine(result.Stats, state, controller))
	lines = append(lines, fmt.Sprintf("Last poll %s · %s", formatPollLatency(result.Latency), remoteStatsPolledLabel(result.FetchedAt)))
	return lines
}

// remoteStatsPolledLabel is the "last poll" relative time; a zero FetchedAt
// (no poll has landed yet) reads "never" rather than a huge duration.
func remoteStatsPolledLabel(fetchedAt time.Time) string {
	if fetchedAt.IsZero() {
		return "never"
	}
	return humanizeSince(time.Since(fetchedAt))
}

// formatPollLatency renders a poll round trip in whole milliseconds or
// seconds, matching how the header shows remote latency elsewhere.
func formatPollLatency(d time.Duration) string {
	if d >= time.Second {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%dms", d.Milliseconds())
}

// remoteSessionStatusLine tallies the given sessions into the same five
// buckets the controller's own header uses: running, waiting, idle, stopped,
// error. sessions is expected to already match the active/archived view
// (Home.remoteSessionsInView), so every row lands in exactly one bucket;
// "starting" counts as running and "queued" as idle, keeping the five
// buckets exhaustive without adding categories the header doesn't have.
func remoteSessionStatusLine(sessions []session.RemoteSessionInfo) string {
	var running, waiting, idle, stopped, errored int
	for _, s := range sessions {
		switch s.Status {
		case "running", "starting":
			running++
		case "waiting":
			waiting++
		case "idle", "queued":
			idle++
		case "stopped":
			stopped++
		case "error":
			errored++
		}
	}
	return fmt.Sprintf("Sessions  %d running · %d waiting · %d idle · %d stopped · %d error", running, waiting, idle, stopped, errored)
}

// remoteHarnessLine counts sessions per tool ("claude", "codex", "pi", ...),
// sorted by name so the line is stable across polls.
func remoteHarnessLine(sessions []session.RemoteSessionInfo) string {
	counts := make(map[string]int)
	var order []string
	for _, s := range sessions {
		if s.Tool == "" {
			continue
		}
		if _, seen := counts[s.Tool]; !seen {
			order = append(order, s.Tool)
		}
		counts[s.Tool]++
	}
	if len(order) == 0 {
		return "Harnesses  none running"
	}
	sort.Strings(order)
	parts := make([]string, 0, len(order))
	for _, tool := range order {
		parts = append(parts, fmt.Sprintf("%s:%d", tool, counts[tool]))
	}
	return "Harnesses  " + strings.Join(parts, " · ")
}

// remoteHostLoadLine renders the remote host's CPU/memory/disk the same
// shape as the controller's own header for this Mac, e.g.
// "28% · 38.2G/48.0G · 715G/926G". A stat the remote could not collect
// (wrong platform, missing /proc) is simply left out.
func remoteHostLoadLine(stats session.RemoteHostStats, state session.RemoteVersionState, controller string) string {
	return remoteHostLoadLineFiltered(stats, []string{session.PreviewFieldLoad, session.PreviewFieldMemory, session.PreviewFieldDisk}, state, controller)
}

// remoteHostLoadLineFiltered is remoteHostLoadLine restricted to the given
// subset of {load, memory, disk} fields, in the order given — used when
// [ui.remote_preview].fields (or [ui.header].fields) asks for only some of
// the three host-stats sub-fields.
func remoteHostLoadLineFiltered(stats session.RemoteHostStats, fields []string, state session.RemoteVersionState, controller string) string {
	var parts []string
	for _, f := range fields {
		switch f {
		case session.PreviewFieldLoad:
			if stats.CPUAvailable {
				parts = append(parts, fmt.Sprintf("%.0f%%", stats.CPUUsagePercent))
			}
		case session.PreviewFieldMemory:
			if stats.MemAvailable {
				parts = append(parts, sysinfo.FormatBytes(stats.MemUsedBytes)+"/"+sysinfo.FormatBytes(stats.MemTotalBytes))
			}
		case session.PreviewFieldDisk:
			if stats.DiskAvailable {
				parts = append(parts, sysinfo.FormatBytes(stats.DiskUsedBytes)+"/"+sysinfo.FormatBytes(stats.DiskTotalBytes))
			}
		}
	}
	if len(parts) == 0 {
		return remoteStatsUnknownLine(state, controller)
	}
	return strings.Join(parts, " · ")
}

// hostStatsFieldSet is the subset of the shared field vocabulary that
// remoteHostLoadLineFiltered understands; used by remotePreviewFieldLines to
// group consecutive load/memory/disk entries into a single combined line,
// matching the panel's historical one-line stats display.
var hostStatsFieldSet = map[string]bool{
	session.PreviewFieldLoad:   true,
	session.PreviewFieldMemory: true,
	session.PreviewFieldDisk:   true,
}

// remotePreviewFieldLines renders the remote preview panel's body (version
// line + stats block) driven by an ordered field list, honoring
// [ui.remote_preview].fields (UISettings.GetRemotePreviewFields). Consecutive
// load/memory/disk entries collapse into one combined line — the historical
// shape — so the default field order renders byte-identical to before this
// config block existed.
func remotePreviewFieldLines(versionState session.RemoteVersionState, controller string, sessions []session.RemoteSessionInfo, result remoteHostStatsResult, hasResult bool, fields []string) []string {
	statsKnown := hasResult && result.Stats.Ok
	consumed := make(map[int]bool, len(fields))
	var lines []string
	for i, f := range fields {
		if consumed[i] {
			continue
		}
		switch f {
		case session.PreviewFieldVersion:
			lines = append(lines, remoteVersionPreviewLine(versionState, controller))
		case session.PreviewFieldSessionsByStatus:
			lines = append(lines, remoteSessionStatusLine(sessions))
		case session.PreviewFieldHarnesses:
			lines = append(lines, remoteHarnessLine(sessions))
		case session.PreviewFieldLoad, session.PreviewFieldMemory, session.PreviewFieldDisk:
			group := []string{f}
			consumed[i] = true
			for j := i + 1; j < len(fields) && hostStatsFieldSet[fields[j]]; j++ {
				group = append(group, fields[j])
				consumed[j] = true
			}
			if !statsKnown {
				lines = append(lines, remoteStatsUnknownLine(versionState, controller))
			} else {
				lines = append(lines, remoteHostLoadLineFiltered(result.Stats, group, versionState, controller))
			}
		case session.PreviewFieldLastPoll:
			if statsKnown {
				lines = append(lines, fmt.Sprintf("Last poll %s · %s", formatPollLatency(result.Latency), remoteStatsPolledLabel(result.FetchedAt)))
			}
		}
	}
	return lines
}
