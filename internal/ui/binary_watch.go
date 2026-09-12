package ui

import (
	"os"

	"github.com/asheshgoplani/agent-deck/internal/update"
	tea "github.com/charmbracelet/bubbletea"
)

// binaryFingerprint is update.Fingerprint: mtime and size from one os.Stat.
// The stat, probe and exec primitives live in internal/update so the
// headless entrypoints share them; only the state machine below is
// TUI-specific.
type binaryFingerprint = update.Fingerprint

// binaryProbeMaxFailures caps how many times a version probe is retried for
// the same on-disk fingerprint. A half-written file usually changes its
// fingerprint again once the installer finishes, which resets the budget.
const binaryProbeMaxFailures = 3

// binaryWatch tracks whether the executable this process started from has
// been replaced by a newer release while the TUI runs: `agent-deck update`
// from another terminal, the auto-update job, or a manual install. It is a
// pure state machine. Callers feed it fingerprints and probe results; it
// never touches the filesystem or runs anything itself, which keeps it
// testable without a real binary.
type binaryWatch struct {
	execPath       string
	runningVersion string

	probed   binaryFingerprint // fingerprint the last successful probe ran against
	probing  bool              // a probe command is in flight
	failed   binaryFingerprint // fingerprint the last failed probe ran against
	failures int               // consecutive failures for `failed`

	// installedVersion is the newer version found on disk, or "" while the
	// file still matches the running build (or was replaced by an older one).
	installedVersion string
}

// newBinaryWatch starts a watch that treats initial as the running build, so
// nothing is probed until the file actually changes.
func newBinaryWatch(execPath, runningVersion string, initial binaryFingerprint) *binaryWatch {
	return &binaryWatch{
		execPath:       execPath,
		runningVersion: runningVersion,
		probed:         initial,
	}
}

// observe records a fresh fingerprint and reports whether the caller should
// probe the file's version now. It returns true at most once per distinct
// change, never while a probe is already running, and stops retrying a
// fingerprint after binaryProbeMaxFailures failed probes.
func (w *binaryWatch) observe(fp binaryFingerprint) bool {
	if w == nil || w.probing {
		return false
	}
	if fp.Equal(w.probed) {
		return false
	}
	if fp.Equal(w.failed) && w.failures >= binaryProbeMaxFailures {
		return false
	}
	w.probing = true
	return true
}

// recordProbe stores the outcome of a probe started by observe. A newer
// version than the running one moves the watch into the "installed" state;
// the same or an older version clears it.
func (w *binaryWatch) recordProbe(fp binaryFingerprint, version string, err error) {
	if w == nil {
		return
	}
	w.probing = false
	if err != nil || version == "" {
		if fp.Equal(w.failed) {
			w.failures++
		} else {
			w.failed = fp
			w.failures = 1
		}
		return
	}
	w.probed = fp
	w.failed = binaryFingerprint{}
	w.failures = 0
	if update.CompareVersions(version, w.runningVersion) > 0 {
		w.installedVersion = version
	} else {
		w.installedVersion = ""
	}
}

// startBinaryWatch resolves the running executable and fingerprints it.
// Returns nil when either step fails; the TUI then simply never reports an
// installed update, exactly as before this feature existed.
func startBinaryWatch(runningVersion string) *binaryWatch {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		return nil
	}
	fp, err := update.StatBinary(exe)
	if err != nil {
		return nil
	}
	return newBinaryWatch(exe, runningVersion, fp)
}

// binaryVersionProbedMsg carries the result of probeBinaryVersion back to the
// event loop.
type binaryVersionProbedMsg struct {
	fingerprint binaryFingerprint
	version     string
	err         error
}

// pollBinaryChange is the per-tick check: one os.Stat, and a probe command
// only when the fingerprint moved. Returns nil when there is nothing to do.
func (h *Home) pollBinaryChange() tea.Cmd {
	w := h.binaryWatch
	if w == nil {
		return nil
	}
	fp, err := update.StatBinary(w.execPath)
	if err != nil {
		// Mid-swap (installer renamed the old file away) or unreadable:
		// wait for the next tick.
		return nil
	}
	if !w.observe(fp) {
		return nil
	}
	exe := w.execPath
	return func() tea.Msg {
		version, err := update.ProbeBinaryVersion(exe)
		return binaryVersionProbedMsg{fingerprint: fp, version: version, err: err}
	}
}

// installedUpdateVersion returns the newer version found on disk, or "" when
// the running build is still the one installed.
func (h *Home) installedUpdateVersion() string {
	if h.binaryWatch == nil {
		return ""
	}
	return h.binaryWatch.installedVersion
}
