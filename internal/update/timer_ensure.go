package update

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Installing and healing the update timer without a separate step (#2472).
//
// Three of four Linux remotes once had no canonical agent-deck-autoupdate
// timer at all, only a hand-made agentdeck-autoupdate pair (no hyphen) that
// `update --check` did not recognise, so their timely update depended on the
// controller's SSH nudge alone. EnsureTimer is the one decision every path
// shares: the explicit `update --install-timer`, and the automatic install
// and heal (`update --unattended`, the TUI's periodic check, the notify
// daemon at start, `update --ensure-timer` that `remote update` runs). It
// leaves an active, current canonical timer alone, installs one where none
// is active, and migrates the legacy pair: the canonical pair is installed
// and verified first, then the legacy timer is disabled and both legacy
// unit files are moved to a backup next to them, never deleted.

// Timer ensure actions, as TimerEnsureResult.Action reports them.
const (
	// TimerActionNone: an active, current timer was left alone.
	TimerActionNone = "none"
	// TimerActionInstalled: the canonical timer was installed (or its
	// units rewritten and re-enabled).
	TimerActionInstalled = "installed"
	// TimerActionMigrated: the legacy pair was retired in favour of the
	// canonical timer.
	TimerActionMigrated = "migrated"
	// TimerActionLoaded: an installed but unloaded launchd timer was
	// bootstrapped again (automatic mode on macOS).
	TimerActionLoaded = "loaded"
	// TimerActionSkipped: nothing could or may be done here; Reason says
	// why (no systemd user session, unpinnable build, manage_timer off).
	TimerActionSkipped = "skipped"
)

// legacyBackupSuffix is appended (with a timestamp) to a retired legacy
// unit file.
const legacyBackupSuffix = ".bak-agentdeck-"

// TimerEnsureResult is what EnsureTimer did, for the one output line, the
// --json document and the logs.
type TimerEnsureResult struct {
	Action string `json:"action"`
	Reason string `json:"reason,omitempty"`
	// Migrated names the legacy unit retired by this run.
	Migrated string `json:"migrated,omitempty"`
	// Backups lists where the retired legacy unit files were moved.
	Backups []string `json:"backups,omitempty"`
	// Status is the timer state after the run (before it, for a dry run).
	Status TimerStatus `json:"status"`
}

// Changed reports whether the run installed, migrated or loaded anything.
func (r TimerEnsureResult) Changed() bool {
	switch r.Action {
	case TimerActionInstalled, TimerActionMigrated, TimerActionLoaded:
		return true
	}
	return false
}

// Line is the one-line summary every caller prints.
func (r TimerEnsureResult) Line() string {
	switch r.Action {
	case TimerActionMigrated:
		return "migrated legacy timer " + r.Migrated + " -> " + SystemdTimerTimer
	case TimerActionInstalled:
		return fmt.Sprintf("installed update timer (%s): %s", r.Status.Kind, r.Status.Path)
	case TimerActionLoaded:
		return fmt.Sprintf("loaded update timer (%s): %s", r.Status.Kind, r.Status.Path)
	case TimerActionNone:
		return fmt.Sprintf("update timer already active (%s): %s", r.Status.Kind, r.Status.Path)
	case TimerActionSkipped:
		return "update timer not managed: " + r.Reason
	}
	return "update timer: " + r.Action
}

// TimerEnsurePlan is the plan EnsureTimer would run and the result it
// predicts; an empty plan means nothing to do (Result says why).
type TimerEnsurePlan struct {
	Plan
	Result TimerEnsureResult
}

// PlanEnsureTimer decides what bringing the timer to "installed, active,
// current, no legacy unit" takes on this host. auto is the automatic
// install/heal: it never pins an unpinnable build, needs a reachable init
// system first, and on macOS never replaces a loaded plist (the run may be
// inside it). The explicit form (auto false) rewrites a stale timer.
// Read-only: it queries the init system through r but changes nothing.
func PlanEnsureTimer(c TimerConfig, r Runner, auto bool) (TimerEnsurePlan, error) {
	switch c.GOOS {
	case "linux":
		return planEnsureSystemd(c, r, auto), nil
	case "darwin":
		return planEnsureLaunchd(c, r, auto), nil
	}
	return TimerEnsurePlan{}, fmt.Errorf("update timer is not supported on %s", c.GOOS)
}

// EnsureTimer plans and runs PlanEnsureTimer. A skip is not an error; a
// failed step is, with Result still naming what was attempted.
func EnsureTimer(c TimerConfig, r Runner, auto bool, log *slog.Logger) (TimerEnsureResult, error) {
	if log == nil {
		log = updateLog
	}
	p, err := PlanEnsureTimer(c, r, auto)
	if err != nil {
		return TimerEnsureResult{Action: TimerActionSkipped, Reason: err.Error(), Status: QueryTimerStatus(c, nil)}, err
	}
	return RunEnsurePlan(c, r, p, log)
}

// RunEnsurePlan executes a plan from PlanEnsureTimer and reports the timer
// state afterwards.
func RunEnsurePlan(c TimerConfig, r Runner, p TimerEnsurePlan, log *slog.Logger) (TimerEnsureResult, error) {
	if log == nil {
		log = updateLog
	}
	res := p.Result
	if len(p.Steps) == 0 {
		return res, nil
	}
	if err := p.Execute(r, log); err != nil {
		res.Status = QueryTimerStatus(c, r)
		log.Error("update_timer_ensure_failed", slog.String("action", res.Action), slog.String("err", err.Error()))
		return res, err
	}
	res.Status = QueryTimerStatus(c, r)
	log.Info("update_timer_ensured", slog.String("action", res.Action), slog.String("kind", res.Status.Kind), slog.String("migrated", res.Migrated))
	return res, nil
}

func planEnsureSystemd(c TimerConfig, r Runner, auto bool) TimerEnsurePlan {
	st := QueryTimerStatus(c, r)
	p := TimerEnsurePlan{Result: TimerEnsureResult{Status: st}}
	if auto && c.Unpinnable != "" {
		p.Result.Action, p.Result.Reason = TimerActionSkipped, c.Unpinnable
		return p
	}
	if reason := systemdUserBusUnavailable(r); reason != "" {
		p.Result.Action, p.Result.Reason = TimerActionSkipped, reason
		return p
	}
	canonicalCurrent := st.Kind == TimerKindSystemd && st.Active &&
		fileHasContent(c.ServicePath(), c.SystemdService()) && fileHasContent(c.TimerPath(), c.SystemdTimer())
	if !canonicalCurrent {
		p.Steps = append(p.Steps, systemdInstallSteps(c)...)
	}
	switch {
	case st.LegacyPath != "":
		steps, backups := legacyRetireSteps(c, st.LegacyPath)
		p.Steps = append(p.Steps, steps...)
		p.Result.Action, p.Result.Migrated, p.Result.Backups = TimerActionMigrated, LegacySystemdTimerTimer, backups
	case !canonicalCurrent:
		p.Result.Action = TimerActionInstalled
	default:
		p.Result.Action = TimerActionNone
	}
	return p
}

// legacyRetireSteps disables the hand-made timer and moves its unit files to
// a timestamped backup beside them. It runs after the canonical timer is
// verified active, so a failure here never leaves the host with no timer.
func legacyRetireSteps(c TimerConfig, legacyTimer string) ([]Step, []string) {
	stamp := c.now().UTC().Format("20060102-150405")
	legacyService := filepath.Join(filepath.Dir(legacyTimer), LegacySystemdTimerService)
	steps := []Step{
		{Desc: "disable legacy timer", Argv: []string{"systemctl", "--user", "disable", "--now", LegacySystemdTimerTimer}, Tolerate: func(string, error) bool { return true }},
	}
	var backups []string
	for _, f := range []struct{ desc, path string }{{"back up legacy timer", legacyTimer}, {"back up legacy service", legacyService}} {
		if !fileExists(f.path) {
			continue
		}
		to := uniqueBackupPath(f.path + legacyBackupSuffix + stamp)
		steps = append(steps, Step{Desc: f.desc, MovePath: f.path, MoveTo: to})
		backups = append(backups, to)
	}
	steps = append(steps, Step{Desc: "reload systemd", Argv: []string{"systemctl", "--user", "daemon-reload"}})
	return steps, backups
}

// uniqueBackupPath returns p, or p.N for the first N that does not exist, so
// a backup never overwrites an earlier one.
func uniqueBackupPath(p string) string {
	if !fileExists(p) {
		return p
	}
	for i := 1; ; i++ {
		candidate := p + "." + strconv.Itoa(i)
		if !fileExists(candidate) {
			return candidate
		}
	}
}

func planEnsureLaunchd(c TimerConfig, r Runner, auto bool) TimerEnsurePlan {
	// Keep the schedule an existing plist chose: re-rendering with a fresh
	// random minute would make every check think the file is stale.
	if data, err := os.ReadFile(c.PlistPath()); err == nil {
		if minute, ok := launchdScheduleMinute(data); ok {
			c.Minute = minute
		}
	}
	st := QueryTimerStatus(c, r)
	p := TimerEnsurePlan{Result: TimerEnsureResult{Status: st}}
	if auto {
		if c.Unpinnable != "" {
			p.Result.Action, p.Result.Reason = TimerActionSkipped, c.Unpinnable
			return p
		}
		if out, err := r.Run("launchctl", "print", launchctlDomain(c.UID)); err != nil {
			p.Result.Action, p.Result.Reason = TimerActionSkipped, "launchd gui domain unavailable: "+firstLine(out)
			return p
		}
		switch {
		case !st.Installed:
			install, _ := InstallTimerPlan(c)
			p.Plan, p.Result.Action = install, TimerActionInstalled
		case !st.Active:
			// Installed but not loaded, so this run cannot be inside it:
			// bootstrap it again, never rewrite it.
			target := launchctlTarget(c.UID, AutoupdateLabel)
			p.Steps = []Step{
				{Desc: "load timer", Argv: []string{"launchctl", "bootstrap", launchctlDomain(c.UID), c.PlistPath()}},
				{Desc: "verify timer", Argv: []string{"launchctl", "print", target}},
			}
			p.Result.Action = TimerActionLoaded
		default:
			p.Result.Action = TimerActionNone
		}
		return p
	}
	if st.Installed && st.Active && fileHasContent(c.PlistPath(), c.LaunchdPlist()) {
		p.Result.Action = TimerActionNone
		return p
	}
	install, _ := InstallTimerPlan(c)
	p.Plan, p.Result.Action = install, TimerActionInstalled
	return p
}

// launchdScheduleMinute reads StartCalendarInterval.Minute from a plist.
func launchdScheduleMinute(data []byte) (int, bool) {
	detail := launchdScheduleDetail(data)
	var hour, minute int
	if _, err := fmt.Sscanf(detail, "daily at %d:%d", &hour, &minute); err != nil {
		return 0, false
	}
	return minute, true
}

func fileHasContent(path string, want []byte) bool {
	got, err := os.ReadFile(path)
	return err == nil && string(got) == string(want)
}

// findLegacySystemdTimer locates a hand-made agentdeck-autoupdate.timer: in
// the user unit dir, else wherever `systemctl --user cat` says it lives.
// Returns its path and contents, both empty when there is none.
func findLegacySystemdTimer(c TimerConfig, r Runner) (path, content string) {
	p := filepath.Join(c.SystemdUserDir, LegacySystemdTimerTimer)
	if data, err := os.ReadFile(p); err == nil {
		return p, string(data)
	}
	if r == nil {
		return "", ""
	}
	out, err := r.Run("systemctl", "--user", "cat", LegacySystemdTimerTimer)
	if err != nil {
		return "", ""
	}
	// `systemctl cat` prints "# <fragment path>" before the unit body.
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# /") && strings.HasSuffix(line, "/"+LegacySystemdTimerTimer) {
			return strings.TrimPrefix(line, "# "), out
		}
	}
	return "", ""
}

// legacyScheduleDetail renders the legacy timer's OnCalendar lines.
func legacyScheduleDetail(unit string) string {
	var cal []string
	for _, line := range strings.Split(unit, "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "OnCalendar="); ok {
			cal = append(cal, v)
		}
	}
	if len(cal) == 0 {
		return "hand-made legacy unit"
	}
	return strings.Join(cal, ", ") + " (hand-made legacy unit)"
}

// systemdUnitActive asks `systemctl --user is-active unit`; note is set when
// the answer says there is no user manager to ask.
func systemdUnitActive(r Runner, unit string) (active bool, note string) {
	out, err := r.Run("systemctl", "--user", "is-active", unit)
	if reason := noUserBusReason(out, err); reason != "" {
		return false, reason
	}
	return err == nil && strings.TrimSpace(out) == "active", ""
}

// systemdUserBusUnavailable returns why `systemctl --user` cannot manage
// units here (no systemctl, no user bus, no user manager), "" when it can.
// `is-system-running` exits non-zero for a merely degraded manager, so only
// the answer's text decides.
func systemdUserBusUnavailable(r Runner) string {
	out, err := r.Run("systemctl", "--user", "is-system-running")
	if reason := noUserBusReason(out, err); reason != "" {
		return reason
	}
	if strings.TrimSpace(out) == "offline" {
		return "no systemd user session: user manager offline"
	}
	return ""
}

// noUserBusReason classifies a systemctl --user answer that never reached a
// user manager.
func noUserBusReason(out string, err error) string {
	if err != nil && exitCode(err) == -1 {
		return "no systemd user session: " + err.Error()
	}
	lower := strings.ToLower(out)
	if strings.Contains(lower, "failed to connect to bus") || strings.Contains(lower, "failed to connect to user scope bus") ||
		strings.Contains(lower, "dbus_session_bus_address") {
		return "no systemd user session: " + firstLine(out)
	}
	return ""
}

// systemdTimerTimes reads a timer's last trigger and next elapse. UTC
// timestamps are asked for first; a systemd too old for --timestamp gets
// the plain query, parsed in local time.
func systemdTimerTimes(r Runner, unit string) (last, next string) {
	props := []string{"-p", "LastTriggerUSec", "-p", "NextElapseUSecRealtime", unit}
	out, err := r.Run(append([]string{"systemctl", "--user", "show", "--timestamp=utc"}, props...)...)
	if err != nil {
		if out, err = r.Run(append([]string{"systemctl", "--user", "show"}, props...)...); err != nil {
			return "", ""
		}
	}
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "LastTriggerUSec":
			last = systemdTimestamp(value)
		case "NextElapseUSecRealtime":
			next = systemdTimestamp(value)
		}
	}
	return last, next
}

// systemdTimestamp normalises a `systemctl show` timestamp to RFC 3339 UTC,
// keeping the raw text when it does not parse; "n/a" and zero become "".
func systemdTimestamp(v string) string {
	v = strings.TrimSpace(v)
	switch v {
	case "", "n/a", "0":
		return ""
	}
	for _, layout := range []string{"Mon 2006-01-02 15:04:05 MST", "Mon 2006-01-02 15:04:05.000000 MST"} {
		if t, err := time.ParseInLocation(layout, v, time.Local); err == nil {
			return t.UTC().Format(time.RFC3339)
		}
	}
	return v
}
