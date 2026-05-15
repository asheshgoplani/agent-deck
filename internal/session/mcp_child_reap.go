package session

import (
	"log/slog"
	"syscall"
	"time"
)

// mcpReapGracePeriod is how long we wait after SIGTERM before escalating
// to SIGKILL on a tracked MCP child. Kept short — stdio MCP children
// generally exit immediately when their stdio is closed, so anything
// that survives 500ms is almost certainly stuck.
const mcpReapGracePeriod = 500 * time.Millisecond

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

// reapTrackedMCPChildren SIGTERMs every PID in TrackedMCPPIDs, waits a
// short grace window, then SIGKILLs any that are still alive. The list
// is cleared on return so a subsequent stop is a no-op.
//
// Errors signaling a single PID are logged and swallowed: a missing
// child (ESRCH) is the success case, and we never want a single stuck
// PID to block tmux teardown.
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

	deadline := time.Now().Add(mcpReapGracePeriod)
	for time.Now().Before(deadline) {
		anyAlive := false
		for _, pid := range pids {
			if syscall.Kill(pid, syscall.Signal(0)) == nil {
				anyAlive = true
				break
			}
		}
		if !anyAlive {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}

	for _, pid := range pids {
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
			mcpLog.Debug("mcp_child_sigkill_failed", slog.Int("pid", pid), slog.Any("error", err))
		}
	}
}
