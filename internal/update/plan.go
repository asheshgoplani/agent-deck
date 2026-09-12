package update

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

// updateLog is the component logger for everything the update command does
// (timer management, launchd hygiene, the unattended flow). It lands in the
// debug log so the owner can audit what an unattended run did.
var updateLog = logging.ForComponent(logging.CompUpdate)

// Runner executes an external command and returns its combined output.
// Production code uses ExecRunner; tests inject a fake that records argv and
// returns canned output, so no test ever touches launchctl or systemctl.
type Runner interface {
	Run(argv ...string) (string, error)
}

// ExecRunner runs commands with os/exec. argv[0] is resolved on PATH.
type ExecRunner struct{}

// Run implements Runner.
func (ExecRunner) Run(argv ...string) (string, error) {
	if len(argv) == 0 {
		return "", errors.New("empty command")
	}
	// #nosec G204 -- argv comes from the fixed plans in this package
	// (launchctl / systemctl with paths we generated), never from user input.
	out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput()
	return string(out), err
}

// exitCoder is satisfied by *exec.ExitError and by the fake runner's errors.
type exitCoder interface{ ExitCode() int }

// exitCode extracts the process exit status from a Runner error, or -1 when
// the error carries none (command not found, nil error, ...).
func exitCode(err error) int {
	var ec exitCoder
	if errors.As(err, &ec) {
		return ec.ExitCode()
	}
	return -1
}

// ShellQuote renders argv as a copy-pasteable shell line for messages.
func ShellQuote(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		if a == "" || strings.ContainsAny(a, " \t\"'$`\\*?[]{}()<>|&;") {
			parts[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		} else {
			parts[i] = a
		}
	}
	return strings.Join(parts, " ")
}

// Step is one unit of a Plan: exactly one of WritePath, RemovePath or Argv is
// set. Plans are pure data so --dry-run can print them and tests can assert
// the exact sequence without executing anything.
type Step struct {
	// Desc is a short human-readable label for dry-run and log output.
	Desc string

	// WritePath + Content + Mode: write a file atomically (temp + rename).
	WritePath string
	Content   []byte
	Mode      os.FileMode

	// RemovePath: delete a file we own; a missing file is not an error.
	RemovePath string

	// Argv: run a command through the Runner.
	Argv []string
	// Tolerate, when set, is consulted on a non-zero exit; returning true
	// downgrades the failure to a logged note (e.g. bootout of a service
	// that is not loaded).
	Tolerate func(out string, err error) bool
}

// Plan is an ordered list of steps.
type Plan struct {
	Steps []Step
}

// Describe renders the plan for --dry-run: full file contents and the exact
// commands, in order.
func (p Plan) Describe() string {
	var b strings.Builder
	for i, s := range p.Steps {
		switch {
		case s.WritePath != "":
			fmt.Fprintf(&b, "[%d] write %s (mode %04o):\n", i+1, s.WritePath, s.Mode)
			content := string(s.Content)
			for _, line := range strings.Split(strings.TrimRight(content, "\n"), "\n") {
				b.WriteString("    " + line + "\n")
			}
		case s.RemovePath != "":
			fmt.Fprintf(&b, "[%d] remove %s\n", i+1, s.RemovePath)
		default:
			fmt.Fprintf(&b, "[%d] run   %s\n", i+1, ShellQuote(s.Argv))
		}
	}
	return b.String()
}

// Execute runs every step in order and stops at the first failure. Each step
// is logged with its result.
func (p Plan) Execute(r Runner, log *slog.Logger) error {
	if log == nil {
		log = updateLog
	}
	for _, s := range p.Steps {
		switch {
		case s.WritePath != "":
			if err := writeFileAtomic(s.WritePath, s.Content, s.Mode); err != nil {
				log.Error("plan_write_failed", slog.String("path", s.WritePath), slog.String("err", err.Error()))
				return fmt.Errorf("%s: write %s: %w", s.Desc, s.WritePath, err)
			}
			log.Info("plan_write", slog.String("path", s.WritePath), slog.Int("bytes", len(s.Content)))
		case s.RemovePath != "":
			if err := os.Remove(s.RemovePath); err != nil && !os.IsNotExist(err) {
				log.Error("plan_remove_failed", slog.String("path", s.RemovePath), slog.String("err", err.Error()))
				return fmt.Errorf("%s: remove %s: %w", s.Desc, s.RemovePath, err)
			}
			log.Info("plan_remove", slog.String("path", s.RemovePath))
		default:
			out, err := r.Run(s.Argv...)
			if err != nil && s.Tolerate != nil && s.Tolerate(out, err) {
				log.Info("plan_run_tolerated", slog.String("cmd", ShellQuote(s.Argv)), slog.String("out", strings.TrimSpace(out)))
				continue
			}
			if err != nil {
				log.Error("plan_run_failed", slog.String("cmd", ShellQuote(s.Argv)), slog.Int("exit", exitCode(err)), slog.String("out", strings.TrimSpace(out)))
				return fmt.Errorf("%s: `%s` failed (%v): %s", s.Desc, ShellQuote(s.Argv), err, strings.TrimSpace(out))
			}
			log.Info("plan_run", slog.String("cmd", ShellQuote(s.Argv)))
		}
	}
	return nil
}

// writeFileAtomic writes content to a temp file next to path and renames it
// into place so a reader (launchd, systemd) never sees a partial file.
func writeFileAtomic(path string, content []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// AcquireUpdateLock takes the cross-process lock that keeps the TUI's
// unattended install and the timer's run from replacing the binary at the
// same time. The lock is a file created with O_EXCL under dir; a lock older
// than stale is treated as abandoned (crashed process) and taken over.
// busy is true when another live run holds it; release is non-nil only when
// the lock was acquired.
func AcquireUpdateLock(dir string, stale time.Duration) (release func(), busy bool, err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, false, err
	}
	path := filepath.Join(dir, UpdateLockFileName)
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			fmt.Fprintf(f, "%d %s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339))
			f.Close()
			return func() { _ = os.Remove(path) }, false, nil
		}
		if !os.IsExist(err) {
			return nil, false, err
		}
		info, statErr := os.Stat(path)
		if statErr != nil {
			if os.IsNotExist(statErr) {
				continue // released between our open and stat; retry
			}
			return nil, false, statErr
		}
		if time.Since(info.ModTime()) < stale {
			return nil, true, nil
		}
		updateLog.Warn("update_lock_stale_removed", slog.String("path", path), slog.Time("mtime", info.ModTime()))
		if rmErr := os.Remove(path); rmErr != nil && !os.IsNotExist(rmErr) {
			return nil, false, rmErr
		}
	}
	return nil, true, nil
}

// UpdateLockFileName is the lock file taken in the cache dir during an
// unattended install.
const UpdateLockFileName = "update.lock"

// UpdateLockStaleAfter is how old an update.lock must be before a new run
// treats it as abandoned.
const UpdateLockStaleAfter = 15 * time.Minute
