package session

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
)

// Transition log retention (issue #2481 item 7). transition-notifier.log is the
// per-record JSONL history of every committed transition. It used to grow
// without bound; it now rotates by size and keeps a fixed number of rotated
// files, so its history spans weeks instead of "since someone deleted it".
//
// Layout: <log>, <log>.1 (newest rotated) ... <log>.<Keep> (oldest). Rotated
// files are plain JSONL in the same record format, so any reader (old or new)
// can read them. Each record is still opened, appended and closed on its own,
// so a writer never holds a stale descriptor: a log that was rotated or
// unlinked is simply recreated by the next record.
type logRotation struct {
	MaxBytes int64 // rotate before an append would grow the live file past this
	Keep     int   // rotated files kept; the oldest beyond Keep is removed
}

// transitionLogRotation is the retention for transition-notifier.log: 8 MiB
// live plus 4 rotated files, about eight months at the busiest measured host's
// rate (~170 KB/day). A var so tests can shrink it.
// Retention is deliberately size-only: quiet installations keep sparse forensic
// history without an age deadline, while disk usage remains bounded. Using mtime
// as file age would never rotate a continuously appended file; a creation-age
// policy would need extra persistent metadata. The issue permits size or age.
// Diagnostic notifier logs share this policy. Rotation failures are best-effort:
// appending the record takes precedence over the size bound.
var transitionLogRotation = logRotation{MaxBytes: 8 << 20, Keep: 4}

// appendRotatingLogLine appends line plus a newline to path, first rotating
// path when the append would take it past r.MaxBytes. Rotation renames files
// (never truncates or rewrites them), so a record a concurrent writer appends
// to the file being rotated lands in <path>.1 and is kept, not lost.
func appendRotatingLogLine(path string, line []byte, r logRotation) error {
	need := int64(len(line)) + 1
	if r.due(path, need) {
		if err := r.rotate(path, need); err != nil {
			commsLog.Debug("log_rotation_failed", slog.String("path", path), slog.String("error", err.Error()))
		}
	}
	return appendLogLine(path, line)
}

// rotate shifts <path>.N -> <path>.N+1 (dropping the oldest beyond
// r.Keep) and <path> -> <path>.1. It holds the cross-process lock beside path
// and re-checks the size under it, so two writers that both saw the file over
// the limit rotate it once, not twice.
func (r logRotation) rotate(path string, need int64) error {
	lock, err := AcquireConfigFileLock(path)
	if err != nil {
		return err
	}
	defer lock.Release()

	if !r.due(path, need) {
		return nil // another writer rotated it while we waited for the lock
	}
	if err := os.Remove(rotatedLogPath(path, r.Keep)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for i := r.Keep - 1; i >= 1; i-- {
		if err := os.Rename(rotatedLogPath(path, i), rotatedLogPath(path, i+1)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return os.Rename(path, rotatedLogPath(path, 1))
}

// due reports whether appending need bytes would take a non-empty path past
// r.MaxBytes. A missing or empty file, or a disabled rotation, never rotates.
func (r logRotation) due(path string, need int64) bool {
	if r.MaxBytes <= 0 || r.Keep <= 0 {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Size() > 0 && info.Size()+need > r.MaxBytes
}

// rotatedLogPath names the i-th rotated copy of path (1 = newest).
func rotatedLogPath(path string, i int) string {
	return fmt.Sprintf("%s.%d", path, i)
}

// RotateAutoUpdateLog bounds launchd's stdout/stderr log after an update command
// finishes. The next scheduled process reopens the active path. Unlike per-line
// notifier logs, this cap is checked per run, so one run may exceed MaxBytes.
func RotateAutoUpdateLog() error {
	path, err := logDataPath("auto-update.log")
	if err != nil {
		return err
	}
	if !transitionLogRotation.due(path, 0) {
		return nil
	}
	return transitionLogRotation.rotate(path, 0)
}
