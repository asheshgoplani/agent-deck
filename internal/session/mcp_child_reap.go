package session

import (
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// mcpReapGracePeriod is how long we wait after SIGTERM before escalating
// to SIGKILL on a tracked MCP child. Stdio MCP children generally exit
// immediately when their stdio is closed, so anything that survives this
// window is almost certainly stuck and needs the harder signal.
//
// Raised from 500ms → 1s in the issue #1086 fix: on CI runners under
// the race detector, scheduler latency between the SIGTERM and the
// child's libc signal handler can exceed 500ms, causing a spurious
// SIGKILL escalation that races with the child's already-in-flight
// exit.
const mcpReapGracePeriod = 1 * time.Second

// mcpReapVerifyTimeout is how long we wait AFTER SIGKILL for the child
// to actually be gone (reaped by init or exit'd by the kernel). Without
// this verification, killInternal returns while children are still
// transitioning, which leaves a window where callers (and tests) can
// observe live PIDs even though the kernel has accepted SIGKILL. See
// issue #1086.
const mcpReapVerifyTimeout = 2 * time.Second

// RegisterMCPChild records the OS PID of a stdio MCP child spawned for
// this session. Session stop iterates these PIDs and signals each
// (SIGTERM → SIGKILL) to prevent the issue-#965 orphan accumulation
// where MCP children get reparented to PID 1.
//
// Safe to call concurrently. Passing pid <= 0 is a no-op.
func (i *Instance) RegisterMCPChild(pid int) {
	if pid <= 0 {
		return
	}
	i.mcpPIDsMu.Lock()
	defer i.mcpPIDsMu.Unlock()
	for _, existing := range i.TrackedMCPPIDs {
		if existing == pid {
			return
		}
	}
	i.TrackedMCPPIDs = append(i.TrackedMCPPIDs, pid)
}

// UnregisterMCPChild removes a previously registered MCP child PID,
// e.g. when the child has been observed exiting cleanly.
func (i *Instance) UnregisterMCPChild(pid int) {
	if pid <= 0 {
		return
	}
	i.mcpPIDsMu.Lock()
	defer i.mcpPIDsMu.Unlock()
	out := i.TrackedMCPPIDs[:0]
	for _, p := range i.TrackedMCPPIDs {
		if p != pid {
			out = append(out, p)
		}
	}
	i.TrackedMCPPIDs = out
}

// discoverMCPChildrenFromPaneTree walks this Instance's tmux pane
// process tree and registers depth >= 2 descendants as tracked MCP
// children. Stdio MCP servers are spawned by claude/codex/gemini
// reading .mcp.json — agent-deck never holds the exec.Cmd handle
// directly, so this discovery is the only point at which their PIDs
// become known to a per-session lifecycle hook.
//
// Filtering rules:
//   - Pane PID itself is skipped: tmux teardown signals it directly.
//   - Direct children of the pane PID (typically the tool process —
//     claude/codex/gemini) are also skipped: tmux's pgroup-wide
//     kill-session is the right path for them, and pre-empting it
//     with SIGTERM causes the session to auto-destroy before
//     kill-session runs, which surfaces a cosmetic teardown error.
//   - Everything deeper IS registered: this is where stdio MCPs and
//     their helpers (uvx, python, node, bun) live. Some MCPs setsid
//     into their own session, escaping tmux's pgroup kill — those
//     are exactly the leakers from issue #965.
//
// Issue #965 wiring follow-up to PR #1000. Hardened in issue #1086
// to use a single ps snapshot (was: two snapshots, which could
// disagree under load on CI runners and skip a depth-2 child whose
// intermediate parent had just exec-optimized).
func (i *Instance) discoverMCPChildrenFromPaneTree() {
	if i.tmuxSession == nil || !i.tmuxSession.Exists() {
		return
	}
	panePID := i.readPanePID()
	if panePID <= 0 {
		return
	}

	procTable, err := exec.Command("ps", "-eo", "pid=,ppid=").Output()
	if err != nil || len(procTable) == 0 {
		return
	}
	childrenByParent := parsePSParentChildMap(procTable)
	if len(childrenByParent) == 0 {
		return
	}

	// BFS from pane PID with depth tracking. Single snapshot — every
	// classification decision (depth-1 skip vs depth-2+ register) is
	// against the same ps output, removing the two-snapshot race the
	// previous implementation had between collectTmuxPaneProcessTreePIDs
	// and the per-pid parent lookup.
	type queued struct {
		pid   int
		depth int
	}
	seen := map[int]bool{panePID: true}
	queue := []queued{{pid: panePID, depth: 0}}
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]

		for _, child := range childrenByParent[node.pid] {
			if child <= 0 || seen[child] {
				continue
			}
			seen[child] = true
			childDepth := node.depth + 1
			if childDepth >= 2 {
				i.RegisterMCPChild(child)
			}
			queue = append(queue, queued{pid: child, depth: childDepth})
		}
	}
}

// readPanePID returns the pane PID for this Instance's tmux session,
// or 0 if it cannot be determined. Extracted so discovery can take a
// single ps snapshot rather than indirecting through
// collectTmuxPaneProcessTreePIDs (which also takes its own snapshot).
func (i *Instance) readPanePID() int {
	target := i.tmuxSession.Name + ":"
	out, err := tmux.Exec(i.TmuxSocketName, "list-panes", "-t", target, "-F", "#{pane_pid}").Output()
	if err != nil {
		return 0
	}
	pidStr := strings.TrimSpace(string(out))
	if idx := strings.IndexByte(pidStr, '\n'); idx >= 0 {
		pidStr = pidStr[:idx]
	}
	pid, err := strconv.Atoi(pidStr)
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}

// reapTrackedMCPChildren SIGTERMs every PID in TrackedMCPPIDs, waits a
// short grace window, then SIGKILLs any that are still alive. The list
// is cleared on return so a subsequent stop is a no-op.
//
// Errors signaling a single PID are logged and swallowed: a missing
// child (ESRCH) is the success case, and we never want a single stuck
// PID to block tmux teardown.
//
// Issue #1086: after SIGKILL, this function now blocks until every PID
// has actually been reaped (ESRCH or zombie) or mcpReapVerifyTimeout
// elapses. Previously it returned immediately after sending SIGKILL,
// which allowed callers (and the issue #965 regression test) to
// observe live PIDs in the brief window before the kernel reaped
// them — flaky on CI runners under -race.
func (i *Instance) reapTrackedMCPChildren() {
	i.mcpPIDsMu.Lock()
	pids := append([]int(nil), i.TrackedMCPPIDs...)
	i.TrackedMCPPIDs = nil
	i.mcpPIDsMu.Unlock()

	if len(pids) == 0 {
		return
	}

	for _, pid := range pids {
		if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && err != syscall.ESRCH {
			mcpLog.Debug("mcp_child_sigterm_failed", slog.Int("pid", pid), slog.Any("error", err))
		}
	}

	if waitPIDsGone(pids, mcpReapGracePeriod) {
		return
	}

	for _, pid := range pids {
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
			mcpLog.Debug("mcp_child_sigkill_failed", slog.Int("pid", pid), slog.Any("error", err))
		}
	}

	if !waitPIDsGone(pids, mcpReapVerifyTimeout) {
		var survivors []int
		for _, pid := range pids {
			if syscall.Kill(pid, syscall.Signal(0)) == nil {
				survivors = append(survivors, pid)
			}
		}
		if len(survivors) > 0 {
			mcpLog.Warn("mcp_child_sigkill_unverified",
				slog.Any("survivors", survivors),
				slog.Duration("waited", mcpReapVerifyTimeout))
		}
	}
}

// waitPIDsGone polls until every PID in the slice is gone (ESRCH on
// signal-0 probe) or the deadline elapses. Returns true when all PIDs
// are confirmed gone, false on timeout.
func waitPIDsGone(pids []int, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for {
		anyAlive := false
		for _, pid := range pids {
			if syscall.Kill(pid, syscall.Signal(0)) == nil {
				anyAlive = true
				break
			}
		}
		if !anyAlive {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}
