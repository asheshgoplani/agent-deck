package update

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/childenv"
)

// Self-restart after an in-place update.
//
// Every long-running agent-deck process (the TUI, `web --no-tui`, the
// remote agent) keeps running the old code after `agent-deck update` or the
// update timer replaces the binary. The pieces here let each of them notice
// the new file cheaply (one os.Stat per tick, a `<exe> version` probe only
// when the file changed) and hand over to it at a moment of its choosing.
// The TUI drives the same primitives from its Bubble Tea tick loop (see
// internal/ui/binary_watch.go); headless processes run a Watcher.

// Fingerprint is the cheap identity of the executable on disk: the mtime
// and size from one os.Stat. No hashing and no exec, so comparing it every
// tick costs nothing noticeable.
type Fingerprint struct {
	ModTime time.Time
	Size    int64
}

// Equal reports whether both fingerprints describe the same file content
// as far as stat can tell.
func (f Fingerprint) Equal(o Fingerprint) bool {
	return f.Size == o.Size && f.ModTime.Equal(o.ModTime)
}

// StatBinary returns the fingerprint of the file at path.
func StatBinary(path string) (Fingerprint, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Fingerprint{}, err
	}
	return Fingerprint{ModTime: info.ModTime(), Size: info.Size()}, nil
}

var versionOutputPattern = regexp.MustCompile(`\bv(\d+\.\d+\.\d+[0-9A-Za-z.+-]*)`)

// ParseVersionOutput extracts the version from `agent-deck version` output,
// e.g. "Agent Deck v1.16.1 (update available: v1.16.2)" yields "1.16.1".
func ParseVersionOutput(out string) string {
	m := versionOutputPattern.FindStringSubmatch(out)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// BinaryProbeTimeout bounds `<exe> version`, which is offline (it only
// reads the update cache) and normally returns in well under a second.
const BinaryProbeTimeout = 10 * time.Second

// ProbeBinaryVersion runs `<exe> version` once and parses the result. It is
// the only exec in the detection path and callers run it only after
// os.Stat says the file changed.
func ProbeBinaryVersion(exe string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), BinaryProbeTimeout)
	defer cancel()
	// #nosec G204 -- exe is the path os.Executable() returned at startup
	// (our own binary), and "version" is a fixed argument.
	cmd := exec.CommandContext(ctx, exe, "version")
	cmd.Env = append(childenv.ForLaunch(""), SkipUpdateCheckEnv+"=1")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	version := ParseVersionOutput(strings.TrimSpace(string(out)))
	if version == "" {
		return "", fmt.Errorf("no version in output %q", strings.TrimSpace(string(out)))
	}
	return version, nil
}

// CheckExecutable is the pre-exec sanity check: the target must exist, be
// a regular non-empty file and carry an exec bit. It cannot prove the exec
// will succeed, but it catches the common ways it would not (installer
// mid-write, wrong path) before the caller has torn its own state down.
func CheckExecutable(exe string) error {
	if exe == "" {
		return errors.New("executable path unknown")
	}
	info, err := os.Stat(exe)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", exe)
	}
	if info.Size() == 0 {
		return fmt.Errorf("%s is empty", exe)
	}
	if info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("%s is not executable", exe)
	}
	return nil
}

// ExecSelf replaces the current process with exe, keeping os.Args. env is
// the environment for the new image; nil means the current one unchanged.
// It only returns on failure (or on platforms without exec, see
// execself_windows.go). Callers must have released everything the new
// process needs first: the terminal, listeners, file locks.
func ExecSelf(exe string, env []string) error {
	if err := CheckExecutable(exe); err != nil {
		return err
	}
	if env == nil {
		// A self re-exec, not a child spawn: the new image must see the
		// exact environment the user launched the old one with, so the
		// childenv filter (which strips CLAUDE_CONFIG_DIR for claude
		// workers) does not apply here.
		env = os.Environ() //nolint:forbidigo // self re-exec, not a child launch (#1163 is about claude workers)
	}
	return execSelf(exe, os.Args, env)
}

// WatcherDefaultInterval is how often a Watcher stats the executable.
const WatcherDefaultInterval = 30 * time.Second

// watcherMaxProbeFailures caps how many times a version probe is retried
// for the same on-disk fingerprint. A half-written file usually changes
// its fingerprint again once the installer finishes, which resets the
// budget.
const watcherMaxProbeFailures = 3

// Watcher polls the running executable's fingerprint and, once a newer
// version is on disk, calls Restart at the first tick where Idle reports
// true. Every collaborator is a plain func so tests never touch a real
// binary; the zero values use the real filesystem, a real `<exe> version`
// probe, "always idle" and the syscall.Exec self re-exec.
type Watcher struct {
	// Exe is the path to watch and re-exec: os.Executable() at startup.
	Exe string
	// RunningVersion is the version compiled into this process.
	RunningVersion string
	// Interval between stats. Zero means WatcherDefaultInterval.
	Interval time.Duration

	// Stat fingerprints the file (default StatBinary).
	Stat func(path string) (Fingerprint, error)
	// Probe returns the version of the file (default ProbeBinaryVersion).
	Probe func(exe string) (string, error)
	// Idle reports whether the process may hand over right now (default:
	// always). It is asked once per tick while a restart is pending.
	Idle func() bool
	// Restart performs the hand-over (default: ExecSelf with the current
	// environment). Returning nil without exec'ing is fine for a process
	// whose supervisor restarts it: the watcher then simply stops.
	Restart func(exe string) error
	// Log receives one line per state change (default: slog.Default()).
	Log *slog.Logger

	probed    Fingerprint
	failed    Fingerprint
	failures  int
	installed string
	announced string
}

// Run blocks until ctx is done or Restart succeeded (returned nil). It is
// safe to run on its own goroutine; the Watcher is not safe for concurrent
// use beyond that.
func (w *Watcher) Run(ctx context.Context) {
	w.fillDefaults()
	if w.Exe == "" {
		return
	}
	if fp, err := w.Stat(w.Exe); err == nil {
		w.probed = fp
	}
	ticker := time.NewTicker(w.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if w.tick() {
			return
		}
	}
}

// tick is one poll: stat, probe on change, restart when newer and idle.
// It returns true once the hand-over succeeded.
func (w *Watcher) tick() bool {
	fp, err := w.Stat(w.Exe)
	if err != nil {
		// Mid-swap (installer renamed the old file away) or unreadable:
		// wait for the next tick.
		return false
	}
	if !fp.Equal(w.probed) && !(fp.Equal(w.failed) && w.failures >= watcherMaxProbeFailures) {
		version, err := w.Probe(w.Exe)
		if err != nil || version == "" {
			if fp.Equal(w.failed) {
				w.failures++
			} else {
				w.failed, w.failures = fp, 1
			}
			w.Log.Debug("binary_version_probe_failed", slog.String("exe", w.Exe), slog.Any("error", err))
			return false
		}
		w.probed, w.failed, w.failures = fp, Fingerprint{}, 0
		if CompareVersions(version, w.RunningVersion) > 0 {
			w.installed = version
		} else {
			w.installed = ""
		}
	}
	if w.installed == "" {
		return false
	}
	if w.announced != w.installed {
		w.announced = w.installed
		w.Log.Info("update_installed_on_disk",
			slog.String("running", w.RunningVersion),
			slog.String("installed", w.installed),
			slog.String("exe", w.Exe))
	}
	if !w.Idle() {
		// Quiet while busy: the log line above already said what is
		// pending, and busy ticks would repeat it every interval.
		return false
	}
	w.Log.Info("self_restart",
		slog.String("running", w.RunningVersion),
		slog.String("installed", w.installed),
		slog.String("exe", w.Exe))
	if err := w.Restart(w.Exe); err != nil {
		// Give up on this file; a later install changes the fingerprint
		// and re-arms the watch.
		w.Log.Warn("self_restart_failed", slog.String("exe", w.Exe), slog.String("error", err.Error()))
		w.installed = ""
		return false
	}
	return true
}

func (w *Watcher) fillDefaults() {
	if w.Interval <= 0 {
		w.Interval = WatcherDefaultInterval
	}
	if w.Stat == nil {
		w.Stat = StatBinary
	}
	if w.Probe == nil {
		w.Probe = ProbeBinaryVersion
	}
	if w.Idle == nil {
		w.Idle = func() bool { return true }
	}
	if w.Restart == nil {
		w.Restart = func(exe string) error { return ExecSelf(exe, nil) }
	}
	if w.Log == nil {
		w.Log = slog.Default()
	}
}
