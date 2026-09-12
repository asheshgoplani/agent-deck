package ui

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/childenv"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/update"
	tea "github.com/charmbracelet/bubbletea"
)

// Unattended install from the TUI's periodic update check.
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
const autoInstallRetryAfter = time.Hour

// autoInstallTimeout bounds one unattended run: a download on a slow link
// plus the install, with plenty of slack.
const autoInstallTimeout = 10 * time.Minute

// autoInstallOutputTail is how much of the updater's combined output is
// kept for the log and the footer.
const autoInstallOutputTail = 4 * 1024

// unattendedInstallFinishedMsg is delivered when the background updater
// exits.
type unattendedInstallFinishedMsg struct {
	version string
	output  string
	err     error
}

// Seams so tests never run a real process or read the user's config.
var (
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
)

// runUnattendedUpdateProcess is the production runUnattendedUpdate.
func runUnattendedUpdateProcess(ctx context.Context, exe string) (string, error) {
	// #nosec G204 -- exe is our own executable path (os.Executable) and
	// every argument is fixed.
	cmd := exec.CommandContext(ctx, exe, "update", "--unattended", "--trigger", "tui")
	cmd.Env = append(childenv.ForLaunch(""), "AGENTDECK_UPDATE_TRIGGER=tui")
	cmd.Stdin = nil
	tail := &tailBuffer{max: autoInstallOutputTail}
	cmd.Stdout = tail
	cmd.Stderr = tail
	err := cmd.Run()
	return tail.String(), err
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return string(t.buf) }

// autoInstallSkipReason returns "" when the periodic check may start an
// unattended install of info now, else why not (for the debug log).
func (h *Home) autoInstallSkipReason(info *update.UpdateInfo) string {
	switch {
	case info == nil || !info.Available || info.LatestVersion == "":
		return "no update available"
	case info.PublishingVersion != "":
		return "release still publishing"
	case os.Getenv(update.SkipUpdateCheckEnv) != "":
		return update.SkipUpdateCheckEnv + " set"
	case h.homebrewManaged:
		return "homebrew-managed install"
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
		if info != nil && info.Available {
			uiLog.Debug("tui_auto_install_skipped", slog.String("latest", info.LatestVersion), slog.String("reason", reason))
		}
		return nil
	}
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
	return tea.Batch(h.pollBinaryChange(), h.checkForUpdate())
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
