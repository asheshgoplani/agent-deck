package session

import (
	"log/slog"

	"github.com/asheshgoplani/agent-deck/internal/update"
)

// The automatic install and heal of the update timer (#2472). Every
// automatic path (`update --unattended`, the TUI's periodic check, the
// notify daemon at start) goes through AutoEnsureUpdateTimer, so they share
// the gates: [updates] manage_timer, the auto-update suppression a test,
// CI or script-driven process carries, and update.PlanEnsureTimer's own
// (unpinnable build, no systemd user session or launchd domain).

// Seams so tests never touch the host's init system or read its env.
var (
	// updateTimerSettings reads [updates] from config.toml.
	updateTimerSettings = GetUpdateSettings
	// updateTimerSuppressed is update.AutoUpdateSuppressed.
	updateTimerSuppressed = update.AutoUpdateSuppressed
	// ensureUpdateTimerOnHost runs the automatic ensure against this
	// host's real launchd/systemd.
	ensureUpdateTimerOnHost = func(log *slog.Logger) (update.TimerEnsureResult, error) {
		cfg, err := update.DefaultTimerConfig()
		if err != nil {
			return update.TimerEnsureResult{Action: update.TimerActionSkipped, Reason: err.Error()}, err
		}
		return update.EnsureTimer(cfg, update.ExecRunner{}, true, log)
	}
)

// AutoEnsureUpdateTimer installs or heals this host's update timer when
// [updates] manage_timer allows it and nothing suppresses automatic update
// work here. A skip is reported in the result, never as an error.
func AutoEnsureUpdateTimer(log *slog.Logger) (update.TimerEnsureResult, error) {
	if !updateTimerSettings().GetManageTimer() {
		return update.TimerEnsureResult{Action: update.TimerActionSkipped, Reason: "[updates] manage_timer = false"}, nil
	}
	if reason := updateTimerSuppressed(); reason != "" {
		return update.TimerEnsureResult{Action: update.TimerActionSkipped, Reason: "automatic update work suppressed: " + reason}, nil
	}
	return ensureUpdateTimerOnHost(log)
}
