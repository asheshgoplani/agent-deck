package ui

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

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
// been replaced by a newer release or distinct local build while the TUI runs: `agent-deck update`
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

	// installedVersion is the replacement version found on disk, or "" while the
	// file still matches the running build (or was replaced by an older one).
	installedVersion string
	// installedSince is when a newer build was first seen on disk; it
	// survives a later, even newer, build (the wait for a restart started
	// then) and clears with installedVersion.
	installedSince time.Time
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
// version or a distinct local build of equal precedence becomes installed;
// the same build or an older version clears it.
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
	if update.InstalledVersionNeedsRestart(version, w.runningVersion) {
		w.installedVersion = version
		if w.installedSince.IsZero() {
			w.installedSince = time.Now()
		}
	} else {
		w.installedVersion = ""
		w.installedSince = time.Time{}
	}
}

// startBinaryWatch fingerprints the running executable at exe and starts
// the watch. When the file cannot be fingerprinted (deleted before the
// deck started) no watch is created, the orphan notice is raised, and the
// path is kept so pollBinaryChange can start the watch once it is back.
func (h *Home) startBinaryWatch(exe, runningVersion string) {
	if exe == "" {
		return
	}
	h.binaryExecPath = exe
	fp, err := statBinary(exe)
	if err != nil {
		h.setBinaryOrphanReason(orphanedBinaryReason(exe))
		return
	}
	h.binaryWatch = newBinaryWatch(exe, runningVersion, fp)
	h.setBinaryOrphanReason(orphanedBinaryReason(exe))
}

// binaryVersionProbedMsg carries the result of probeBinaryVersion back to the
// event loop.
type binaryVersionProbedMsg struct {
	fingerprint binaryFingerprint
	version     string
	err         error
}

// statBinary and probeBinaryVersion are the per-tick stat and the version
// probe; seams so tests need no real binary.
var (
	statBinary         = update.StatBinary
	probeBinaryVersion = update.ProbeBinaryVersion
)

// orphanedBinaryReason reports why a process running from execPath can
// neither install an update into it nor re-exec from it: the path no
// longer exists (deleted; Linux reports "/proc/self/exe (deleted)") or it
// is in a Trash folder (a binary moved there is not where an installer or
// `agent-deck` from PATH will look). "" when the path is fine or unknown.
// A file merely replaced in place keeps its path and is not orphaned.
func orphanedBinaryReason(execPath string) string {
	if execPath == "" {
		return ""
	}
	path, deleted := strings.CutSuffix(execPath, " (deleted)")
	if deleted {
		return fmt.Sprintf("its executable no longer exists at %s", path)
	}
	if reason := trashedBinaryReason(path); reason != "" {
		return reason
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Sprintf("its executable no longer exists at %s", path)
	}
	return ""
}

// trashedBinaryReason is the path-only half of orphanedBinaryReason: a
// binary inside a Trash folder is orphaned even though it stats fine.
func trashedBinaryReason(path string) string {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".Trash" || part == "Trash" {
			return fmt.Sprintf("its executable is in the Trash (%s)", path)
		}
	}
	return ""
}

// pollBinaryChange is the per-tick check: one os.Stat, and a probe command
// only when the fingerprint moved. Returns nil when there is nothing to do.
// The same stat keeps binaryOrphanReason current (see orphanedBinaryReason).
func (h *Home) pollBinaryChange() tea.Cmd {
	w := h.binaryWatch
	if w == nil {
		// No watch because the file was missing at startup: keep looking
		// for it so a reinstall by hand starts the watch and clears the
		// notice.
		if h.binaryExecPath != "" {
			h.startBinaryWatch(h.binaryExecPath, Version)
		}
		return nil
	}
	fp, err := statBinary(w.execPath)
	if err != nil {
		// Mid-swap (installer renamed the old file away) or unreadable:
		// wait for the next tick. Gone for good is the orphan case.
		h.setBinaryOrphanReason(orphanedBinaryReason(w.execPath))
		return nil
	}
	// The file is there; only its location can still make it an orphan.
	h.setBinaryOrphanReason(trashedBinaryReason(w.execPath))
	if !w.observe(fp) {
		return nil
	}
	exe := w.execPath
	return func() tea.Msg {
		version, err := probeBinaryVersion(exe)
		return binaryVersionProbedMsg{fingerprint: fp, version: version, err: err}
	}
}

// installedUpdateVersion returns the replacement version found on disk, or "" when
// the running build is still the one installed.
func (h *Home) installedUpdateVersion() string {
	if h.binaryWatch == nil {
		return ""
	}
	return h.binaryWatch.installedVersion
}

// setBinaryOrphanReason records the orphan state, logging each change.
func (h *Home) setBinaryOrphanReason(reason string) {
	if reason == h.binaryOrphanReason {
		return
	}
	if reason != "" {
		uiLog.Warn("binary_orphaned", slog.String("reason", reason))
	} else {
		uiLog.Info("binary_orphan_cleared")
	}
	h.binaryOrphanReason = reason
}
