package update

import (
	"context"
	"log/slog"
	"os/exec"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/childenv"
)

// Unattended install from a long-running process.
//
// A release that lands while agent-deck is already running is only ever
// noticed by a process that keeps asking. CheckForUpdate is cache-backed
// (one network fetch per check interval, shared by every process on the
// machine), so asking often is a disk read; the pieces here set the cadence
// and, for headless daemons, run the loop. The TUI drives the same cadence
// from its tick loop (internal/ui/update_auto.go); `web --no-tui` runs an
// Installer.

// RecheckInterval is how often a long-running process re-asks
// CheckForUpdate. Far finer than the hourly cache on purpose: the answer
// comes from the cache until that is older than the check interval, so
// a release is noticed within one check interval of landing, not on the
// next start.
const RecheckInterval = time.Minute

// RecheckBackoff is how long a process waits after a failed check (GitHub
// unreachable, rate limited) before asking again. A failed check saves no
// cache, so without this every RecheckInterval would be a network call.
const RecheckBackoff = 5 * time.Minute

// InstallRetryAfter is how long a version that was attempted (installed
// or failed) is left alone before an unattended run may try it again.
const InstallRetryAfter = time.Hour

// UnattendedInstallTimeout bounds one unattended run: a download on a slow
// link plus the install, with plenty of slack.
const UnattendedInstallTimeout = 10 * time.Minute

// NextRecheck returns when a process that last asked at last (zero: never)
// should ask again; failed says whether that attempt errored.
func NextRecheck(last time.Time, failed bool) time.Time {
	if last.IsZero() {
		return time.Time{}
	}
	if failed {
		return last.Add(RecheckBackoff)
	}
	return last.Add(RecheckInterval)
}

// RunUnattendedInstall runs `<exe> update --unattended --trigger <trigger>`
// and returns the tail of its combined output. That subcommand never
// prompts, takes the cross-process install lock (so a concurrent timer run,
// TUI or daemon cannot install twice) and prints one summary line.
func RunUnattendedInstall(ctx context.Context, exe, trigger string) (string, error) {
	// #nosec G204 -- exe is our own executable path (os.Executable) and
	// every argument is fixed.
	cmd := exec.CommandContext(ctx, exe, "update", "--unattended", "--trigger", trigger)
	cmd.Env = append(childenv.ForLaunch(""), TriggerEnv+"="+trigger)
	cmd.Stdin = nil
	tail := &TailBuffer{Max: unattendedOutputTail}
	cmd.Stdout = tail
	cmd.Stderr = tail
	err := cmd.Run()
	return tail.String(), err
}

// unattendedOutputTail is how much of the updater's combined output is
// kept for the log.
const unattendedOutputTail = 4 * 1024

// TailBuffer keeps the last Max bytes written to it.
type TailBuffer struct {
	Max int
	buf []byte
}

func (t *TailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.Max {
		t.buf = t.buf[len(t.buf)-t.Max:]
	}
	return len(p), nil
}

func (t *TailBuffer) String() string { return string(t.buf) }

// Installer is the headless counterpart of the TUI's periodic check plus
// auto_install: every Interval it asks Check and, when an installable
// release is available and Enabled says so, runs Install once per version
// per InstallRetryAfter. Every collaborator is a plain func so tests never
// touch GitHub or a real binary; the zero values use CheckForUpdate and
// RunUnattendedInstall. The caller decides the process-wide gates
// (AutoUpdateSuppressed, Homebrew) before building one.
type Installer struct {
	// Exe is the executable to update: os.Executable() at startup.
	Exe string
	// RunningVersion is the version compiled into this process.
	RunningVersion string
	// Trigger is recorded in the updater's audit trail ("web").
	Trigger string
	// Interval between asks. Zero means RecheckInterval.
	Interval time.Duration

	// Enabled reports whether unattended installs are wanted right now
	// ([updates].auto_install). Nil means always.
	Enabled func() bool
	// Check asks for the latest release (default: CheckForUpdate with the
	// cache).
	Check func() (*UpdateInfo, error)
	// Install runs the unattended updater (default: RunUnattendedInstall).
	Install func(ctx context.Context, exe, trigger string) (string, error)
	// Pending reports whether a launch agent waits to be re-registered
	// (default: HasPendingRebootstrap). With nothing to install, a tick
	// still runs the updater to retry it, once per InstallRetryAfter.
	Pending func() bool
	// Log receives one line per decision (default: slog.Default()).
	Log *slog.Logger

	// ticks replaces the interval ticker in tests; the tick time is the
	// clock the schedule runs on.
	ticks <-chan time.Time
	// handled, when set, receives after every tick (tests).
	handled chan struct{}

	lastCheck  time.Time
	lastFailed bool
	attempts   map[string]time.Time
	lastSkip   string
}

// Run blocks until ctx is done. It is safe to run on its own goroutine;
// the Installer is not safe for concurrent use beyond that.
func (in *Installer) Run(ctx context.Context) {
	in.fillDefaults()
	if in.Exe == "" {
		return
	}
	ticks := in.ticks
	if ticks == nil {
		ticker := time.NewTicker(in.Interval)
		defer ticker.Stop()
		ticks = ticker.C
	}
	for {
		var now time.Time
		select {
		case <-ctx.Done():
			return
		case now = <-ticks:
		}
		in.tick(ctx, now)
		if in.handled != nil {
			select {
			case in.handled <- struct{}{}:
			case <-ctx.Done():
				return
			}
		}
	}
}

// tick is one ask: skip while the last check is still fresh (or backing
// off), otherwise check and install when everything lines up.
func (in *Installer) tick(ctx context.Context, now time.Time) {
	if now.Before(NextRecheck(in.lastCheck, in.lastFailed)) {
		return
	}
	in.lastCheck = now
	info, err := in.Check()
	in.lastFailed = err != nil
	if err != nil {
		in.Log.Debug("headless_update_check_failed", slog.String("error", err.Error()))
		return
	}
	if reason := in.skipReason(info, now); reason != "" {
		if info != nil && info.Available && reason != in.lastSkip {
			in.Log.Info("headless_auto_install_skipped", slog.String("latest", info.LatestVersion), slog.String("reason", reason))
		}
		in.lastSkip = reason
		in.maybeDrainPending(ctx, now)
		return
	}
	in.lastSkip = ""
	in.attempts[info.LatestVersion] = now
	in.Log.Info("headless_auto_install_started", slog.String("exe", in.Exe), slog.String("latest", info.LatestVersion), slog.String("trigger", in.Trigger))
	out, err := in.runUpdater(ctx)
	if err != nil {
		in.Log.Warn("headless_auto_install_failed", slog.String("latest", info.LatestVersion), slog.String("error", err.Error()), slog.String("output", out))
		return
	}
	// The binary watch (Watcher) sees the new file and re-execs at the
	// next idle point; nothing more to do here.
	in.Log.Info("headless_auto_install_finished", slog.String("latest", info.LatestVersion), slog.String("output", out))
}

// pendingDrainKey is the attempts key of a run started only to retry
// pending launch agents.
const pendingDrainKey = "launchd-pending"

// maybeDrainPending runs the updater for a pending launch agent when
// nothing is being installed: the run re-registers every pending agent
// this process is not inside (its own service stays deferred for the
// timer or a TUI), at most once per InstallRetryAfter.
func (in *Installer) maybeDrainPending(ctx context.Context, now time.Time) {
	if !in.Pending() || now.Sub(in.attempts[pendingDrainKey]) < InstallRetryAfter {
		return
	}
	in.attempts[pendingDrainKey] = now
	in.Log.Info("headless_launchd_pending_drain_started", slog.String("exe", in.Exe), slog.String("trigger", in.Trigger))
	out, err := in.runUpdater(ctx)
	if err != nil {
		in.Log.Warn("headless_launchd_pending_drain_failed", slog.String("error", err.Error()), slog.String("output", out))
		return
	}
	in.Log.Info("headless_launchd_pending_drain_finished", slog.String("output", out))
}

// runUpdater runs one bounded unattended updater.
func (in *Installer) runUpdater(ctx context.Context) (string, error) {
	runCtx, cancel := context.WithTimeout(ctx, UnattendedInstallTimeout)
	defer cancel()
	return in.Install(runCtx, in.Exe, in.Trigger)
}

// skipReason returns "" when info may be installed now, else why not.
func (in *Installer) skipReason(info *UpdateInfo, now time.Time) string {
	switch {
	case info == nil || !info.Available || info.LatestVersion == "":
		return "no update available"
	case info.PublishingVersion != "":
		return "release still publishing"
	case in.Enabled != nil && !in.Enabled():
		return "auto_install is off"
	case now.Sub(in.attempts[info.LatestVersion]) < InstallRetryAfter:
		return "attempted recently"
	}
	return ""
}

func (in *Installer) fillDefaults() {
	if in.Interval <= 0 {
		in.Interval = RecheckInterval
	}
	if in.Check == nil {
		version := in.RunningVersion
		in.Check = func() (*UpdateInfo, error) { return CheckForUpdate(version, false) }
	}
	if in.Install == nil {
		in.Install = RunUnattendedInstall
	}
	if in.Pending == nil {
		in.Pending = HasPendingRebootstrap
	}
	if in.Log == nil {
		in.Log = slog.Default()
	}
	if in.attempts == nil {
		in.attempts = map[string]time.Time{}
	}
}
