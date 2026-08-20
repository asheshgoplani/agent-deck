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
// Each PID is identified by the process start time captured when this function
// begins. A caller that captures identities before tearing down a tmux session
// should use EnsureProcessIdentitiesDead so it also covers the small interval
// between the tmux kill and this cleanup.
func EnsurePIDsDead(pids []int, timeout time.Duration) {
	EnsureProcessIdentitiesDead(CaptureProcessIdentities(pids), timeout)
}

// ProcessIdentity is a PID together with the start time reported by ps. The
// pairing lets teardown distinguish the original pane descendant from an
// unrelated process that reused its PID.
type ProcessIdentity struct {
	PID       int
	StartedAt string
}

// CaptureProcessIdentities snapshots the identities of the supplied PIDs.
// Missing PIDs are omitted; they are already gone and need no cleanup.
func CaptureProcessIdentities(pids []int) []ProcessIdentity {
	identities := make([]ProcessIdentity, 0, len(pids))
	seen := make(map[int]struct{}, len(pids))
	for _, pid := range pids {
		if pid <= 0 {
			continue
		}
		if _, duplicate := seen[pid]; duplicate {
			continue
		}
		seen[pid] = struct{}{}
		if startedAt := processStartTime(pid); startedAt != "" {
			identities = append(identities, ProcessIdentity{PID: pid, StartedAt: startedAt})
		}
	}
	return identities
}

// EnsureProcessIdentitiesDead blocks until every captured process is gone or
// the timeout elapses. It only signals a PID while its start time still matches
// the original snapshot, so it never kills a recycled unrelated PID.
func EnsureProcessIdentitiesDead(identities []ProcessIdentity, timeout time.Duration) {
	if len(identities) == 0 {
		return
	}
	if timeout <= 0 {
		timeout = 3 * time.Second
	}

	deadline := time.Now().Add(timeout)

	// Stage 1: brief settle — give whatever earlier signal (SIGHUP from
	// `tmux kill-session`) a chance to take effect before we escalate.
	sleepUntilOrDuration(deadline, 250*time.Millisecond)

	alive := filterAliveProcessIdentities(identities)
	if len(alive) == 0 {
		return
	}

	respawnLog.Info("ensure_pids_dead_sigterm",
		slog.Int("count", len(alive)),
		slog.Any("pids", identityPIDs(alive)))
	for _, process := range alive {
		if proc, err := os.FindProcess(process.PID); err == nil {
			_ = proc.Signal(syscall.SIGTERM)
		}
	}

	// Stage 2: give SIGTERM time to propagate.
	sleepUntilOrDuration(deadline, 750*time.Millisecond)

	stubborn := filterAliveProcessIdentities(alive)
	if len(stubborn) == 0 {
		return
	}

	respawnLog.Info("ensure_pids_dead_sigkill",
		slog.Int("count", len(stubborn)),
		slog.Any("pids", identityPIDs(stubborn)))
	for _, process := range stubborn {
		if proc, err := os.FindProcess(process.PID); err == nil {
			_ = proc.Signal(syscall.SIGKILL)
		}
	}

	// Stage 3: wait for SIGKILL to complete, polling signal-0 so we
	// return as soon as they're all gone rather than sleeping blindly.
	for time.Now().Before(deadline) {
		if len(filterAliveProcessIdentities(stubborn)) == 0 {
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

// filterAliveProcessIdentities returns process identities that still refer to
// the original live process. No executable-name allowlist is needed: callers
// only pass descendants captured from the session pane tree.
func filterAliveProcessIdentities(identities []ProcessIdentity) []ProcessIdentity {
	var alive []ProcessIdentity
	for _, process := range identities {
		if process.PID <= 0 || process.StartedAt == "" {
			continue
		}
		proc, err := os.FindProcess(process.PID)
		if err != nil {
			continue
		}
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			continue // already dead
		}
		if processStartTime(process.PID) != process.StartedAt {
			continue
		}
		alive = append(alive, process)
	}
	return alive
}

func processStartTime(pid int) string {
	// #nosec G204 -- "ps" is a fixed binary and the only varying arg is
	// strconv.Itoa(int), never external input.
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "lstart=").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func identityPIDs(identities []ProcessIdentity) []int {
	pids := make([]int, 0, len(identities))
	for _, process := range identities {
		pids = append(pids, process.PID)
	}
	return pids
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
	oldProcesses := CaptureProcessIdentities(oldPIDs)

	// Bounded — see tmuxMutationTimeout. This is the CLI path (`agent-deck
	// remove`), where an unbounded wedge hangs the user's terminal outright
	// rather than a background goroutine.
	//
	// The argv MUST carry the session's own socket, exactly like Session.Kill.
	// It used to be a bare `tmux kill-session` through the execCommandContext
	// seam, which addressed the DEFAULT server: with `[tmux] socket_name` set,
	// every kill exited 1 ("can't find session") while the session stood
	// untouched on its real server. Callers read that as a failed stop —
	// archiveSession rolled the archive back and reported "failed to archive:
	// stop archived session: failed to kill tmux session: exit status 1", so
	// archiving took several presses. (It appeared to work eventually only
	// because the EnsurePIDsDead reap below IS socket-correct and tears the
	// session down as a side effect, after which the already-gone branch
	// applies.) A bare kill is also a cross-server hazard: a same-named session
	// on the user's default server would be killed in its place.
	killErr := s.runBoundedMutation("kill-session", "-t", s.Name)

	if len(oldProcesses) > 0 {
		EnsureProcessIdentitiesDead(oldProcesses, 3*time.Second)
	}

	// Killing an already-dead session is success (see Session.Kill): tmux
	// `kill-session` exits non-zero for a session that no longer exists. CLI
	// callers (`agent-deck remove` of a stopped session) must not fail on that.
	//
	// The re-probe bypasses Session.Exists() for the same reason Session.Kill's
	// does: Exists() trusts a positive session-cache entry (which can outlive
	// the session it describes) and a PipeManager connection that the reconnect
	// loop may have re-established since the Disconnect above. Either would
	// report a successfully killed session as still alive and turn this into a
	// spurious failure. tmuxSessionExistsOnSocket asks the session's own server
	// directly.
	if killErr != nil && !tmuxSessionExistsOnSocket(s.SocketName, s.Name) {
		return nil
	}

	return killErr
}
