package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/update"
)

// Exit codes of `agent-deck update --unattended`.
const (
	exitUpdateOK       = 0
	exitUpdateFailed   = 1
	exitUpdateHomebrew = 2 // brew is never run unattended; the command is printed
)

var updateCLILog = logging.ForComponent(logging.CompUpdate)

// updateTrigger resolves who started this run, for the audit trail only:
// the --trigger flag wins, then AGENTDECK_UPDATE_TRIGGER (set by the timer
// units), then "manual".
func updateTrigger(flagValue string) string {
	if v := strings.TrimSpace(flagValue); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv(update.TriggerEnv)); v != "" {
		return v
	}
	return "manual"
}

// updateCheckJSON is the --check --json document.
type updateCheckJSON struct {
	Current     string             `json:"current"`
	Latest      string             `json:"latest"`
	Available   bool               `json:"available"`
	Publishing  string             `json:"publishing,omitempty"`
	AutoInstall bool               `json:"auto_install"`
	AutoRestart bool               `json:"auto_restart"`
	Timer       update.TimerStatus `json:"timer"`
}

func buildUpdateCheckJSON(info *update.UpdateInfo, settings session.UpdateSettings, timer update.TimerStatus) updateCheckJSON {
	return updateCheckJSON{
		Current:     info.CurrentVersion,
		Latest:      info.LatestVersion,
		Available:   info.Available,
		Publishing:  info.PublishingVersion,
		AutoInstall: settings.GetAutoInstall(),
		AutoRestart: settings.GetAutoRestart(),
		Timer:       timer,
	}
}

func printUpdateCheckJSON(w io.Writer, doc updateCheckJSON) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}

// unattendedDeps are the collaborators of the unattended flow, split out so
// the decision tree is testable without GitHub, the binary or launchctl.
type unattendedDeps struct {
	version     string
	trigger     string
	autoInstall bool
	lockDir     string
	out         io.Writer

	check          func() (*update.UpdateInfo, error)
	detectHomebrew func() (execPath, upgradeCmd string, managed bool, err error)
	preflight      func() error
	install        func(latest string) error
	updateBridge   func() error
	hygiene        func() error
}

// runUnattendedUpdate is `agent-deck update --unattended`: no prompts, no
// changelog, no stdin. Returns the process exit code. Every branch logs with
// the trigger so the debug log shows what a timer or TUI run did.
func runUnattendedUpdate(d unattendedDeps) int {
	log := unattendedLogger(d.trigger)
	log.Info("unattended_update_start", slog.String("current", d.version))

	info, err := d.check()
	if err != nil {
		fmt.Fprintf(d.out, "Update check failed: %v\n", err)
		log.Error("unattended_check_failed", slog.String("err", err.Error()))
		return exitUpdateFailed
	}
	if info.PublishingVersion != "" {
		fmt.Fprintf(d.out, "v%s is still publishing (no binary for this platform yet); nothing installed\n", info.PublishingVersion)
		log.Info("unattended_skipped", slog.String("reason", "publishing"), slog.String("publishing", info.PublishingVersion))
		return exitUpdateOK
	}
	if !info.Available {
		fmt.Fprintf(d.out, "v%s is current; nothing to do\n", d.version)
		log.Info("unattended_skipped", slog.String("reason", "current"))
		return exitUpdateOK
	}
	if !d.autoInstall {
		fmt.Fprintf(d.out, "v%s available but auto_install is off in config.toml, nothing installed (run `agent-deck update` to install by hand)\n", info.LatestVersion)
		log.Info("unattended_skipped", slog.String("reason", "auto_install_off"), slog.String("latest", info.LatestVersion))
		return exitUpdateOK
	}

	execPath, upgradeCmd, managed, err := d.detectHomebrew()
	if err == nil && managed {
		fmt.Fprintf(d.out, "Homebrew-managed install at %s; brew is never run unattended. Run: brew update && %s\n", execPath, upgradeCmd)
		log.Info("unattended_skipped", slog.String("reason", "homebrew"), slog.String("path", execPath))
		return exitUpdateHomebrew
	}

	release, busy, err := update.AcquireUpdateLock(d.lockDir, update.UpdateLockStaleAfter)
	if err != nil {
		fmt.Fprintf(d.out, "Cannot take update lock: %v\n", err)
		log.Error("unattended_lock_failed", slog.String("err", err.Error()))
		return exitUpdateFailed
	}
	if busy {
		fmt.Fprintln(d.out, "Another agent-deck update is already running; nothing to do")
		log.Info("unattended_skipped", slog.String("reason", "lock_busy"))
		return exitUpdateOK
	}
	defer release()

	if err := d.preflight(); err != nil {
		fmt.Fprintf(d.out, "Refusing to install v%s: post-install launchd hygiene cannot run (%v)\n", info.LatestVersion, err)
		log.Error("unattended_preflight_failed", slog.String("err", err.Error()))
		return exitUpdateFailed
	}

	log.Info("unattended_install_start", slog.String("latest", info.LatestVersion))
	if err := d.install(info.LatestVersion); err != nil {
		fmt.Fprintf(d.out, "Error installing v%s: %v\n", info.LatestVersion, err)
		log.Error("unattended_install_failed", slog.String("err", err.Error()))
		return exitUpdateFailed
	}
	log.Info("unattended_install_done", slog.String("installed", info.LatestVersion))

	if err := d.updateBridge(); err != nil {
		fmt.Fprintf(d.out, "Warning: failed to update bridge.py: %v\n", err)
		log.Warn("unattended_bridge_failed", slog.String("err", err.Error()))
	}

	if err := d.hygiene(); err != nil {
		fmt.Fprintf(d.out, "Installed v%s but %v\n", info.LatestVersion, err)
		log.Error("unattended_hygiene_failed", slog.String("err", err.Error()))
		return exitUpdateFailed
	}

	fmt.Fprintf(d.out, "✓ Updated to v%s (unattended); running agent-deck processes restart themselves\n", info.LatestVersion)
	log.Info("unattended_update_done", slog.String("installed", info.LatestVersion))
	return exitUpdateOK
}

// unattendedLogger tags every line of an unattended run with its trigger.
func unattendedLogger(trigger string) *slog.Logger {
	return updateCLILog.With(slog.String("trigger", trigger), slog.String("mode", "unattended"))
}

// realUnattendedDeps wires runUnattendedUpdate to GitHub, the binary and
// launchd.
func realUnattendedDeps(trigger string) unattendedDeps {
	log := unattendedLogger(trigger)
	lockDir, err := ensureEffectiveCacheDir()
	if err != nil {
		lockDir = os.TempDir()
	}
	return unattendedDeps{
		version:        Version,
		trigger:        trigger,
		autoInstall:    session.GetUpdateSettings().GetAutoInstall(),
		lockDir:        lockDir,
		out:            os.Stdout,
		check:          func() (*update.UpdateInfo, error) { return update.CheckForUpdate(Version, true) },
		detectHomebrew: update.DetectHomebrewManagedInstall,
		preflight: func() error {
			return update.PreflightLaunchctl(update.RebootstrapOptions{Logger: log})
		},
		install: func(latest string) error {
			release, err := update.FetchReleaseByTag(latest)
			if err != nil {
				return fmt.Errorf("failed to fetch release info: %w", err)
			}
			return update.PerformVerifiedUpdate(release, runtime.GOOS, runtime.GOARCH)
		},
		updateBridge: update.UpdateBridgePy,
		hygiene:      func() error { return rebootstrapLaunchAgentsAfterInstall(log) },
	}
}

// rebootstrapLaunchAgentsAfterInstall is the post-install hygiene shared by
// every install path. On macOS it re-registers the com.agentdeck.* launch
// agents that run this binary (see update.RebootstrapLaunchAgents); elsewhere
// it is a no-op. The returned error already names the agent and the repair
// commands.
func rebootstrapLaunchAgentsAfterInstall(log *slog.Logger) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	fmt.Println("Re-registering launchd agents that run agent-deck...")
	res, err := update.RebootstrapLaunchAgents(update.RebootstrapOptions{Logger: log})
	if err != nil {
		return err
	}
	if len(res.Restarted) == 0 {
		fmt.Println("  (none run this binary)")
	}
	return nil
}

// warnIfLaunchctlUnavailable is the interactive counterpart of the
// unattended preflight: a failure is printed, never fatal.
func warnIfLaunchctlUnavailable() {
	if runtime.GOOS != "darwin" {
		return
	}
	if err := update.PreflightLaunchctl(update.RebootstrapOptions{}); err != nil {
		fmt.Printf("Warning: launchd agents cannot be re-registered after this install (%v)\n", err)
		fmt.Println("  If com.agentdeck.* agents crash-loop afterwards, bootout and bootstrap them by hand.")
	}
}

// finishInstallHygiene runs the hygiene after an interactive install and
// prints the failure the way the spec words it. Returns false on failure so
// the caller can exit non-zero: the binary is already updated at that point
// and the message says so.
func finishInstallHygiene(version string) bool {
	if err := rebootstrapLaunchAgentsAfterInstall(updateCLILog); err != nil {
		fmt.Printf("\nInstalled v%s but %v\n", version, err)
		return false
	}
	return true
}

// runTimerCommand implements --install-timer / --uninstall-timer /
// --timer-status. Returns the exit code.
func runTimerCommand(action string, dryRun bool, out io.Writer) int {
	cfg, err := update.DefaultTimerConfig()
	if err != nil {
		fmt.Fprintf(out, "Error: %v\n", err)
		return 1
	}
	return runTimerCommandWith(cfg, update.ExecRunner{}, action, dryRun, out)
}

func runTimerCommandWith(cfg update.TimerConfig, r update.Runner, action string, dryRun bool, out io.Writer) int {
	log := updateCLILog.With(slog.String("action", action), slog.Bool("dry_run", dryRun))
	switch action {
	case "status":
		st := update.QueryTimerStatus(cfg, r)
		if !st.Installed {
			fmt.Fprintf(out, "Update timer: not installed (run `agent-deck update --install-timer`)\n")
			return 0
		}
		state := "installed but not loaded"
		if st.Active {
			state = "active"
		}
		fmt.Fprintf(out, "Update timer: %s (%s)\n  unit: %s\n  schedule: %s\n", state, st.Kind, st.Path, st.Detail)
		return 0
	case "install", "uninstall":
		var plan update.Plan
		var err error
		if action == "install" {
			plan, err = update.InstallTimerPlan(cfg)
		} else {
			plan, err = update.UninstallTimerPlan(cfg)
		}
		if err != nil {
			fmt.Fprintf(out, "Error: %v\n", err)
			log.Error("timer_plan_failed", slog.String("err", err.Error()))
			return 1
		}
		if len(plan.Steps) == 0 {
			fmt.Fprintln(out, "Update timer is not installed; nothing to remove")
			return 0
		}
		if dryRun {
			fmt.Fprintf(out, "Dry run: would %s the update timer with these steps (nothing executed):\n\n", action)
			fmt.Fprint(out, plan.Describe())
			return 0
		}
		if err := plan.Execute(r, log); err != nil {
			fmt.Fprintf(out, "Error: %v\n", err)
			return 1
		}
		if action == "install" {
			st := update.QueryTimerStatus(cfg, nil)
			fmt.Fprintf(out, "✓ Update timer installed (%s): %s\n  runs `agent-deck update --unattended` %s\n", st.Kind, st.Path, st.Detail)
		} else {
			fmt.Fprintln(out, "✓ Update timer removed")
		}
		return 0
	}
	fmt.Fprintf(out, "Error: unknown timer action %q\n", action)
	return 1
}

// initUpdateCommandLogging routes the update command's log lines to the
// cache debug.log regardless of AGENTDECK_DEBUG: an unattended run has no
// terminal and the owner audits it after the fact. Same policy as the
// notify daemon, hence the shared setup.
func initUpdateCommandLogging() func() {
	return initDaemonLogging()
}
