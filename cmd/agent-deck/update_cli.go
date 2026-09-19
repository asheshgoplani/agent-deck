package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/procowner"
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
	// OnDisk is the version of the binary at this executable's path (what
	// a TUI restarts into); RunningTUIs lists every TUI with a heartbeat,
	// outdated when it runs something older than OnDisk, with the reason
	// it has not restarted. Empty when no TUI reports.
	OnDisk      string             `json:"on_disk,omitempty"`
	RunningTUIs []update.TUIReport `json:"running_tuis"`
	// PendingLaunchAgents lists the launch agents no run has managed to
	// re-register yet (deferred by a run inside them, or booted out and
	// never accepted back), with the attempts so far. Empty when none.
	PendingLaunchAgents []update.PendingAgent `json:"pending_launch_agents"`
}

func buildUpdateCheckJSON(info *update.UpdateInfo, settings session.UpdateSettings, timer update.TimerStatus, onDisk string, tuis []update.TUIReport, pending []update.PendingAgent) updateCheckJSON {
	if tuis == nil {
		tuis = []update.TUIReport{}
	}
	if pending == nil {
		pending = []update.PendingAgent{}
	}
	return updateCheckJSON{
		Current:     info.CurrentVersion,
		Latest:      info.LatestVersion,
		Available:   info.Available,
		Publishing:  info.PublishingVersion,
		AutoInstall: settings.GetAutoInstall(),
		AutoRestart: settings.GetAutoRestart(),
		Timer:       timer,
		OnDisk:      onDisk,
		RunningTUIs: tuis,

		PendingLaunchAgents: pending,
	}
}

// runningTUIReports reads the TUI heartbeats in the cache dir and reports
// them against onDisk. Any failure yields an empty list: the check must
// never fail because of a heartbeat file.
func runningTUIReports(onDisk string) []update.TUIReport {
	dir, err := ensureEffectiveCacheDir()
	if err != nil {
		return nil
	}
	hbs, err := update.ListTUIHeartbeats(dir, procowner.Alive)
	if err != nil {
		return nil
	}
	return update.ReportTUIs(hbs, onDisk, time.Now())
}

// onDiskVersion probes the binary at this executable's path: after an
// in-place install the file is newer than the running process.
func onDiskVersion() string {
	exe, err := os.Executable()
	if err != nil {
		return Version
	}
	v, err := update.ProbeBinaryVersion(exe)
	if err != nil || v == "" {
		return Version
	}
	return v
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
	// log is the run's logger (update.log + debug log, tagged with the
	// run's identity); nil falls back to the debug log tagged with trigger.
	log *slog.Logger

	check          func() (*update.UpdateInfo, error)
	detectHomebrew func() (execPath, upgradeCmd string, managed bool, err error)
	preflight      func() error
	install        func(latest string) error
	updateBridge   func() error
	hygiene        func() error
	// drainPending re-registers launch agents an earlier run left pending
	// (update.DrainPendingRebootstrap: deferred because it ran inside
	// them, or booted out and never accepted back); it runs whenever the
	// run does not install, since an install's own hygiene covers every
	// agent anyway.
	drainPending func() error
	// sweepRemotes pushes the new version to older remotes when
	// [updates] auto_update_remotes is on (#2166); it never prompts and
	// its failures are per-remote, never this run's.
	sweepRemotes func(latest string)
}

// runUnattendedUpdate is `agent-deck update --unattended`: no prompts, no
// changelog, no stdin. Returns the process exit code. Every branch logs with
// the trigger so the debug log shows what a timer or TUI run did.
func runUnattendedUpdate(d unattendedDeps) int {
	log := d.log
	if log == nil {
		log = unattendedLogger(d.trigger)
	}
	log.Info("unattended_update_start", slog.String("current", d.version))

	info, err := d.check()
	if err != nil {
		fmt.Fprintf(d.out, "Update check failed: %v\n", err)
		log.Error("unattended_check_failed", slog.String("err", err.Error()))
		return exitUpdateFailed
	}
	// Whatever stops the install, the pending launch agents are retried:
	// a launch agent left unloaded must not wait for the next release.
	drain := func() int {
		if d.drainPending == nil {
			return exitUpdateOK
		}
		if err := d.drainPending(); err != nil {
			fmt.Fprintf(d.out, "Launch agent still not re-registered: %v\n", err)
			log.Error("unattended_pending_drain_failed", slog.String("err", err.Error()))
			return exitUpdateFailed
		}
		return exitUpdateOK
	}
	if info.PublishingVersion != "" {
		fmt.Fprintf(d.out, "v%s is still publishing (no binary for this platform yet); nothing installed\n", info.PublishingVersion)
		log.Info("unattended_skipped", slog.String("reason", "publishing"), slog.String("publishing", info.PublishingVersion))
		return drain()
	}
	if !info.Available {
		fmt.Fprintf(d.out, "v%s is current; nothing to do\n", d.version)
		log.Info("unattended_skipped", slog.String("reason", "current"))
		return drain()
	}
	if !d.autoInstall {
		fmt.Fprintf(d.out, "v%s available but auto_install is off in config.toml, nothing installed (run `agent-deck update` to install by hand)\n", info.LatestVersion)
		log.Info("unattended_skipped", slog.String("reason", "auto_install_off"), slog.String("latest", info.LatestVersion))
		return drain()
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

	if d.sweepRemotes != nil {
		d.sweepRemotes(info.LatestVersion)
	}
	return exitUpdateOK
}

// unattendedLogger tags every line of an unattended run with its trigger.
func unattendedLogger(trigger string) *slog.Logger {
	return updateCLILog.With(slog.String("trigger", trigger), slog.String("mode", "unattended"))
}

// realUnattendedDeps wires runUnattendedUpdate to GitHub, the binary and
// launchd. The returned func closes the audit log (update.log) after the
// run.
func realUnattendedDeps(trigger string) (unattendedDeps, func()) {
	lockDir, err := ensureEffectiveCacheDir()
	if err != nil {
		lockDir = os.TempDir()
	}
	// Every line of the run goes to update.log too, so the audit trail
	// survives the shared debug.log being rotated by another process.
	log, closeLog, auditErr := update.OpenAuditLog(lockDir, logging.Logger(), update.NewAuditIdentity(trigger, Version))
	if auditErr != nil {
		log.Warn("unattended_audit_log_unavailable", slog.String("err", auditErr.Error()))
	}
	return unattendedDeps{
		version:        Version,
		trigger:        trigger,
		autoInstall:    session.GetUpdateSettings().GetAutoInstall(),
		lockDir:        lockDir,
		out:            os.Stdout,
		log:            log,
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
		drainPending: func() error { return drainPendingLaunchAgents(log) },
		sweepRemotes: func(latest string) { sweepRemotesUnattended(latest, log) },
	}, closeLog
}

// drainPendingLaunchAgents re-registers the launch agents a previous run
// left in the pending marker (it ran inside them, or booted them out and
// launchd never accepted them back). No marker: no-op.
func drainPendingLaunchAgents(log *slog.Logger) error {
	if runtime.GOOS != "darwin" || !update.HasPendingRebootstrap() {
		return nil
	}
	fmt.Println("Re-registering launchd agents a previous update left pending...")
	res, err := update.DrainPendingRebootstrap(update.RebootstrapOptions{Logger: log})
	if err != nil {
		return err
	}
	for _, label := range res.Deferred {
		fmt.Printf("  ⏸ %s: still deferred, this run is inside it\n", label)
	}
	return nil
}

// sweepRemotesUnattended is the unattended counterpart of
// updateRemotesAfterLocalUpdate: with auto_update_remotes on it runs the
// same no-prompt sweep (#2166); with it off there is nobody to answer the
// Y/n prompt, so the remotes are left alone. Every way the sweep does not
// run is logged with its reason (unattended_remote_sweep_skipped or
// _deferred), so "the remotes are still old" is never a silent outcome.
func sweepRemotesUnattended(latest string, log *slog.Logger) {
	config, err := session.LoadUserConfig()
	sweep, running := session.RemoteSweepInProgress()
	d := unattendedSweepDecision(config, err, session.GetUpdateSettings(), sweep, running)
	switch {
	case d.deferred:
		fmt.Printf("remote sweep deferred: %s\n", d.reason)
		log.Info("unattended_remote_sweep_deferred", slog.String("reason", d.reason), slog.Int("remotes", d.remotes), slog.Int("sweep_pid", sweep.PID))
		return
	case d.reason != "":
		if d.remotes > 0 {
			fmt.Printf("remote sweep skipped: %s\n", d.reason)
		}
		log.Info("unattended_remote_sweep_skipped", slog.String("reason", d.reason), slog.Int("remotes", d.remotes))
		return
	}
	fmt.Printf("auto_update_remotes is on: updating %d remote(s) to v%s\n", d.remotes, latest)
	log.Info("unattended_remote_sweep_start", slog.Int("remotes", d.remotes), slog.String("latest", latest))
	results := runPostUpdateRemoteSweep(context.Background(), config.Remotes, latest, true)
	if results == nil {
		log.Info("unattended_remote_sweep_deferred", slog.String("reason", "another sweep took the marker first"))
		return
	}
	fmt.Printf("\n%s\n", remoteUpdateSummary(results))
	log.Info("unattended_remote_sweep_done", slog.Int("remotes", len(results)), slog.String("summary", remoteUpdateSummary(results)))
}

// sweepDecision is why an unattended sweep does not run ("" runs it).
type sweepDecision struct {
	reason   string
	deferred bool // another sweep is running; this one is not lost, just not now
	remotes  int
}

// unattendedSweepDecision is the pure decision behind sweepRemotesUnattended.
func unattendedSweepDecision(config *session.UserConfig, loadErr error, settings session.UpdateSettings, sweep session.RemoteSweep, running bool) sweepDecision {
	if loadErr != nil {
		return sweepDecision{reason: "config unreadable: " + loadErr.Error()}
	}
	if config == nil || len(config.Remotes) == 0 {
		return sweepDecision{reason: "no remotes configured"}
	}
	d := sweepDecision{remotes: len(config.Remotes)}
	switch {
	case !settings.GetAutoUpdateRemotes():
		d.reason = "auto_update_remotes is off (run `agent-deck remote update --all` to update them)"
	case running:
		d.reason = fmt.Sprintf("a sweep from pid %d (started %s) is still running; the next start of a newer controller sweeps again", sweep.PID, sweep.StartedAt.Format("15:04:05"))
		d.deferred = true
	}
	return d
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

// printPendingLaunchAgents lists the launch agents still waiting to be
// re-registered, one line each, for `update --check`.
func printPendingLaunchAgents() {
	pending := update.ListPendingRebootstrap()
	if len(pending) == 0 {
		return
	}
	lines := make([]string, 0, len(pending))
	for _, p := range pending {
		lines = append(lines, "  "+update.DescribePendingAgent(p))
	}
	fmt.Printf("\nLaunch agents still waiting to be re-registered with launchd:\n%s\n", strings.Join(lines, "\n"))
}

// printOutdatedTUIs lists the TUIs still running an image older than the
// binary on disk, one line each, for `update --check`.
func printOutdatedTUIs(onDisk string) {
	var lines []string
	for _, r := range runningTUIReports(onDisk) {
		if r.Outdated {
			lines = append(lines, "  "+update.DescribeTUIReport(r))
		}
	}
	if len(lines) == 0 {
		return
	}
	fmt.Printf("\nTUIs still running an older image than v%s on disk:\n%s\n", onDisk, strings.Join(lines, "\n"))
}
