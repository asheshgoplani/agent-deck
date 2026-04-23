// Package tmux — synchronous process-tree reap primitives (issue #59,
// v1.7.68).
//
// Session.Kill always ran the SIGTERM→SIGKILL escalation in a
// background goroutine. In short-lived CLI processes (`agent-deck
// remove`, `agent-deck session remove --force`) the goroutine was
// aborted when the CLI exited, leaving any SIGHUP-immune child (e.g.
// claude 2.1.27+) running indefinitely. The orphan observed
// 2026-04-22 (PID 321456, 33 hours old, AGENTDECK_INSTANCE_ID set,
// registry row gone) is the production manifestation.
//
// EnsurePIDsDead is the synchronous companion: when it returns, all
// given PIDs are dead (or the timeout has fired). Session.KillAndWait
// wraps that behaviour at the tmux-session level for callers that
// want a one-shot "kill everything and be sure".

package tmux

import (
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// EnsurePIDsDead blocks until every pid in `pids` is dead (signal-0
// probe fails) or `timeout` elapses. Escalates SIGTERM → SIGKILL with
// a 500ms pause between stages. A zero-length slice is a no-op.
//
// Callers in CLI processes should use this instead of scheduling
// ensureProcessesDead on a goroutine — see issue #59.
//
// The PID-reuse guard in isOurProcess is preserved: if a captured PID
// has been recycled into an unrelated process, it's skipped.
func EnsurePIDsDead(pids []int, timeout time.Duration) {
	if len(pids) == 0 {
		return
	}
	if timeout <= 0 {
		timeout = 3 * time.Second
	}

	deadline := time.Now().Add(timeout)

	// Stage 1: brief settle — give whatever earlier signal (SIGHUP from
	// `tmux kill-session`) a chance to take effect before we escalate.
	sleepUntilOrDuration(deadline, 250*time.Millisecond)

	alive := filterAliveOurProcesses(pids)
	if len(alive) == 0 {
		return
	}

	respawnLog.Info("ensure_pids_dead_sigterm",
		slog.Int("count", len(alive)),
		slog.Any("pids", alive))
	for _, pid := range alive {
		if proc, err := os.FindProcess(pid); err == nil {
			_ = proc.Signal(syscall.SIGTERM)
		}
	}

	// Stage 2: give SIGTERM time to propagate.
	sleepUntilOrDuration(deadline, 750*time.Millisecond)

	stubborn := filterAliveOurProcesses(alive)
	if len(stubborn) == 0 {
		return
	}

	respawnLog.Info("ensure_pids_dead_sigkill",
		slog.Int("count", len(stubborn)),
		slog.Any("pids", stubborn))
	for _, pid := range stubborn {
		if proc, err := os.FindProcess(pid); err == nil {
			_ = proc.Signal(syscall.SIGKILL)
		}
	}

	// Stage 3: wait for SIGKILL to complete, polling signal-0 so we
	// return as soon as they're all gone rather than sleeping blindly.
	for time.Now().Before(deadline) {
		if len(filterAliveOurProcesses(stubborn)) == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// sleepUntilOrDuration sleeps for min(d, until-now). Never past the
// deadline. Callers use this to respect an overall timeout budget
// while still pausing long enough for signals to settle.
func sleepUntilOrDuration(deadline time.Time, d time.Duration) {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return
	}
	if d > remaining {
		d = remaining
	}
	time.Sleep(d)
}

// filterAliveOurProcesses returns the subset of `pids` that are still
// alive AND match one of our known-process binaries (PID reuse guard).
func filterAliveOurProcesses(pids []int) []int {
	var alive []int
	for _, pid := range pids {
		if pid <= 0 {
			continue
		}
		proc, err := os.FindProcess(pid)
		if err != nil {
			continue
		}
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			continue // already dead
		}
		if !isOurProcessLoose(pid) {
			continue
		}
		alive = append(alive, pid)
	}
	return alive
}

// isOurProcessLoose mirrors isOurProcess (tmux.go) with a broader
// allowlist so EnsurePIDsDead can reap any pane-descendant process the
// callers hand it (the existing tmux.go guard is narrower because it
// runs on a hot-path during respawn). Includes "dash" / "sleep" so the
// test harness (sh -c 'trap "" HUP; sleep …', where /bin/sh is dash
// on Debian/Ubuntu) exercises the real primitive.
//
// The PID-reuse concern is bounded by the short capture→kill window;
// callers that care about stricter filtering can pre-filter before
// handing PIDs in.
func isOurProcessLoose(pid int) bool {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil {
		return false
	}
	name := strings.ToLower(strings.TrimSpace(string(out)))
	for _, known := range []string{"claude", "node", "zsh", "bash", "sh", "dash", "ash", "fish", "cat", "npm", "bun", "uv", "python", "sleep"} {
		if strings.Contains(name, known) {
			return true
		}
	}
	return false
}

// KillAndWait is the synchronous variant of Session.Kill. When it
// returns, tmux kill-session has been run AND every pane process we
// captured before the kill has been verified dead (or reaped via
// SIGTERM/SIGKILL). Intended for short-lived CLI processes where the
// goroutine scheduled by Kill would be aborted on exit.
//
// See issue #59 and the package-level docs above.
func (s *Session) KillAndWait() error {
	if pm := GetPipeManager(); pm != nil {
		pm.Disconnect(s.Name)
	}
	_ = os.Remove(s.LogFile())

	_, oldPIDs := s.getPaneProcessTree()

	cmd := execCommand("tmux", "kill-session", "-t", s.Name)
	killErr := cmd.Run()

	if len(oldPIDs) > 0 {
		EnsurePIDsDead(oldPIDs, 3*time.Second)
	}

	return killErr
}
