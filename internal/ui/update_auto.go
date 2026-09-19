package ui

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/update"
	tea "github.com/charmbracelet/bubbletea"
)

// Periodic update check and unattended install from the TUI.
//
// The tick loop re-asks update.CheckForUpdate every update.RecheckInterval
// whether or not a banner is showing. The answer is the shared on-disk
// cache until that is older than the check interval, so an open deck
// notices a release within one check interval of it landing instead of on
// its next start (before this, the loop only re-checked while a banner was
// already up, so a deck opened before a release never asked again).
//
// With [updates].auto_install (default true) the TUI does not wait for the
// user to press the install key: when a check says a release is available
// and installable it runs `<exe> update --unattended --trigger tui` in the
// background. That subcommand never prompts, takes the cross-process
// install lock (so a concurrent timer run or a second TUI cannot install
// twice), and prints one summary line. The binary watch then notices the
// new file and auto_restart (or the restart key) takes it from there. The
// user-triggered install key keeps its interactive path.

// autoInstallRetryAfter is how long a version that failed (or was just
// attempted) is left alone before the periodic check may try it again.
const autoInstallRetryAfter = update.InstallRetryAfter

// autoInstallTimeout bounds one unattended run.
const autoInstallTimeout = update.UnattendedInstallTimeout

// unattendedInstallFinishedMsg is delivered when the background updater
// exits.
type unattendedInstallFinishedMsg struct {
	version string
	output  string
	err     error
}

// Seams so tests never run a real process, hit GitHub or read the user's
// config.
var (
	// checkUpdate asks for the latest release (cache-backed unless forced).
	checkUpdate = update.CheckForUpdate
	// runUnattendedUpdate runs the updater for exe and returns the tail of
	// its combined output.
	runUnattendedUpdate = runUnattendedUpdateProcess
	// loadUpdateSettings reads [updates] from config.toml.
	loadUpdateSettings = session.GetUpdateSettings
	// detectHomebrewManaged reports whether this binary is Homebrew's;
	// then `brew upgrade` owns it and the TUI never installs.
	detectHomebrewManaged = func() bool {
		_, _, managed, _ := update.DetectHomebrewManagedInstall()
		return managed
	}
	// autoUpdateSuppressed reports why this process must not install or
	// restart on its own (go test, CI, skip env, test markers, no TTY;
	// issue #2251). Evaluated once at startup into Home.autoUpdateSuppressed.
	autoUpdateSuppressed = update.TUIAutoUpdateSuppressed
)

// runUnattendedUpdateProcess is the production runUnattendedUpdate.
func runUnattendedUpdateProcess(ctx context.Context, exe string) (string, error) {
	return update.RunUnattendedInstall(ctx, exe, "tui")
}

// checkForUpdate asks for the latest release asynchronously.
func (h *Home) checkForUpdate() tea.Cmd {
	return func() tea.Msg {
		info, err := checkUpdate(Version, false)
		return updateCheckMsg{info: info, err: err}
	}
}

// requestUpdateCheck is the one gate every check the TUI starts on its
// own goes through (startup, the periodic tick, after an install): at most
// one in flight, and each one stamps lastUpdateCheck so the periodic
// schedule counts from the latest. Returns nil while one is running.
func (h *Home) requestUpdateCheck(now time.Time) tea.Cmd {
	if h.updateCheckInFlight {
		return nil
	}
	h.lastUpdateCheck = now
	h.updateCheckInFlight = true
	return h.checkForUpdate()
}

// periodicUpdateCheck is called on every tick. It starts a check when one
// is due (update.NextRecheck: every update.RecheckInterval, longer after a
// failure), through requestUpdateCheck, and not at all with check_enabled
// = false or when the process is test-, CI- or script-driven (issue
// #2251: such a process must not start polling GitHub on its own either).
func (h *Home) periodicUpdateCheck(now time.Time) tea.Cmd {
	if h.updateCheckInFlight || now.Before(update.NextRecheck(h.lastUpdateCheck, h.lastUpdateCheckFailed)) {
		return nil
	}
	if h.autoUpdateSuppressedReason != "" || !loadUpdateSettings().GetCheckEnabled() {
		return nil
	}
	return h.requestUpdateCheck(now)
}

// handleUpdateCheck records a check result: the banner state, what the
// scheduler needs (in flight, failed), then the unattended install.
func (h *Home) handleUpdateCheck(msg updateCheckMsg) tea.Cmd {
	h.updateCheckInFlight = false
	h.lastUpdateCheckFailed = msg.err != nil
	if msg.err != nil {
		uiLog.Debug("update_check_failed", slog.String("error", msg.err.Error()))
	}
	if msg.info != nil && !msg.info.Available {
		// Update is no longer available (e.g., user updated via terminal) — dismiss banner
		h.updateInfo = nil
	} else {
		h.updateInfo = msg.info
	}
	// The nudge banner takes a row from the embedded pane.
	h.syncEmbeddedTerminalGeometry()
	// auto_install: start the unattended updater in the background.
	return h.maybeAutoInstall(msg.info)
}

// autoInstallSkipReason returns "" when the periodic check may start an
// unattended install of info now, else why not (for the debug log).
func (h *Home) autoInstallSkipReason(info *update.UpdateInfo) string {
	switch {
	case info == nil || !info.Available || info.LatestVersion == "":
		return "no update available"
	case info.PublishingVersion != "":
		return "release still publishing"
	case h.autoUpdateSuppressedReason != "":
		return h.autoUpdateSuppressedReason
	case h.homebrewManaged:
		return "homebrew-managed install"
	case h.binaryOrphanReason != "":
		return h.binaryOrphanReason
	case h.autoInstallInFlight != "":
		return "install already running"
	case update.CompareVersions(h.installedUpdateVersion(), info.LatestVersion) >= 0:
		return "already installed on disk"
	case time.Since(h.autoInstallAttempts[info.LatestVersion]) < autoInstallRetryAfter:
		return "attempted recently"
	case !loadUpdateSettings().GetAutoInstall():
		return "auto_install is off"
	case h.restartExecutable() == "":
		return "executable path unknown"
	}
	return ""
}

// maybeAutoInstall is called with every update check result. It starts at
// most one unattended install per version per hour, on its own goroutine,
// and never blocks the event loop.
func (h *Home) maybeAutoInstall(info *update.UpdateInfo) tea.Cmd {
	if reason := h.autoInstallSkipReason(info); reason != "" {
		// Say why once per change of mind at Info, so the default log
		// shows the decision; the per-minute repeats stay at Debug.
		if info != nil && info.Available {
			log := uiLog.Debug
			if reason != h.autoInstallLastSkip {
				log = uiLog.Info
			}
			log("tui_auto_install_skipped", slog.String("latest", info.LatestVersion), slog.String("reason", reason))
		}
		h.autoInstallLastSkip = reason
		return nil
	}
	h.autoInstallLastSkip = ""
	version := info.LatestVersion
	exe := h.restartExecutable()
	if h.autoInstallAttempts == nil {
		h.autoInstallAttempts = map[string]time.Time{}
	}
	h.autoInstallAttempts[version] = time.Now()
	h.autoInstallInFlight = version
	uiLog.Info("tui_auto_install_started", slog.String("exe", exe), slog.String("latest", version))
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), autoInstallTimeout)
		defer cancel()
		out, err := runUnattendedUpdate(ctx, exe)
		return unattendedInstallFinishedMsg{version: version, output: out, err: err}
	}
}

// handleUnattendedInstallFinished logs the outcome, shows one footer line
// on failure, and re-checks both the binary on disk (so the banner flips
// to "installed" now rather than on the next tick) and the update cache
// the installer just invalidated.
func (h *Home) handleUnattendedInstallFinished(msg unattendedInstallFinishedMsg) tea.Cmd {
	h.autoInstallInFlight = ""
	tail := strings.TrimSpace(msg.output)
	if msg.err != nil {
		uiLog.Warn("tui_auto_install_failed",
			slog.String("latest", msg.version),
			slog.String("error", msg.err.Error()),
			slog.String("output", tail))
		h.setError(fmt.Errorf("auto-update to v%s failed: %s; run agent-deck update", msg.version, firstLine(tail, msg.err.Error())))
	} else {
		uiLog.Info("tui_auto_install_finished", slog.String("latest", msg.version), slog.String("output", tail))
	}
	return tea.Batch(h.pollBinaryChange(), h.requestUpdateCheck(time.Now()))
}

// firstLine returns the first non-blank line of text, or fallback.
func firstLine(text, fallback string) string {
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return fallback
}

// applyAutoUpdateSuppression records, once at startup, whether this
// process may install or restart on its own (issue #2251). Both auto
// paths read the stored reason on every decision.
func (h *Home) applyAutoUpdateSuppression() {
	h.autoUpdateSuppressedReason = autoUpdateSuppressed()
	if h.autoUpdateSuppressedReason != "" {
		uiLog.Info("auto_update_suppressed", slog.String("reason", h.autoUpdateSuppressedReason))
	}
}
