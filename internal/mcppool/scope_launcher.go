package mcppool

// MCP-per-scope cascade prevention (v1.9).
//
// On Linux+systemd, each MCP child process is launched inside its own
// transient user scope (`mcp-<owner>-<mcp>-<ts>.scope` under
// `mcp-pool.slice`). When systemd-oomd ranks cgroups for kill selection
// it walks per-scope memory, pressure, and pgscan deltas — so a
// misbehaving MCP becomes its own kill target rather than dragging the
// conductor or session scope down with it.
//
// Background: 2026-05-08 cascade — 43 simultaneous duplicate
// `@upstash/context7-mcp` instances accumulated inside the conductor's
// tmux scope (58.2 GB resident). When user@1000 pressure crossed 50%,
// systemd-oomd picked the conductor scope (largest by RSS) and SIGKILLed
// 604 processes in one shot. Per-MCP scopes prevent the kill from
// targeting the orchestrator. See worker-root-cause/RESULTS.md.

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// lookupSystemdRun returns the absolute path to systemd-run, or "" if it
// is not on PATH. Overridable in tests.
var lookupSystemdRun = func() string {
	p, err := exec.LookPath("systemd-run")
	if err != nil {
		return ""
	}
	return p
}

// runtimeGOOS shadows runtime.GOOS so tests can simulate non-Linux hosts
// without needing a real cross-build.
var runtimeGOOS = runtime.GOOS

// scopeIsolationEnabled reports whether MCP-per-scope wrapping is active.
// Default: ON on Linux, OFF elsewhere. Overridable via the
// AGENT_DECK_MCP_ISOLATION env var: 1/true/on/yes enable, 0/false/off/no
// disable.
func scopeIsolationEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("AGENT_DECK_MCP_ISOLATION"))) {
	case "1", "true", "on", "yes":
		return true
	case "0", "false", "off", "no":
		return false
	}
	return runtimeGOOS == "linux"
}

// sanitizeScopeToken keeps systemd-acceptable characters (alnum, dash,
// underscore) and replaces the rest with '-'. Empty input becomes "x".
// Truncates to 32 chars so the assembled unit name stays well under the
// 256-char systemd unit-name limit.
func sanitizeScopeToken(s string) string {
	if s == "" {
		return "x"
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := b.String()
	if len(out) > 32 {
		out = out[:32]
	}
	if out == "" {
		return "x"
	}
	return out
}

// scopeUnitName builds a unique transient .scope name for an MCP child.
// Format: mcp-<owner>-<mcp>-<utc-timestamp>.scope
func scopeUnitName(ownerID, mcpName string) string {
	ts := time.Now().UTC().Format("20060102T150405Z")
	return fmt.Sprintf("mcp-%s-%s-%s.scope",
		sanitizeScopeToken(ownerID),
		sanitizeScopeToken(mcpName),
		ts)
}

// wrapMCPCommand returns the (cmd, args) to use when starting an MCP
// child process. When scope isolation is enabled and supported, the
// returned command runs the original command inside a transient
// per-MCP systemd user scope. When isolation is disabled, missing
// systemd-run, or running on a non-Linux host, the original command
// and args are returned untouched and wrapped is false.
//
// ownerID is a short, stable identifier for the owning agent-deck
// instance (typically the process PID). It exists only to disambiguate
// scope names when multiple agent-deck instances coexist on the same
// host. mcpName is the user-facing MCP server name.
func wrapMCPCommand(ownerID, mcpName, origCommand string, origArgs []string) (cmd string, args []string, wrapped bool, unit string) {
	if !scopeIsolationEnabled() {
		return origCommand, origArgs, false, ""
	}
	if runtimeGOOS != "linux" {
		return origCommand, origArgs, false, ""
	}
	sr := lookupSystemdRun()
	if sr == "" {
		return origCommand, origArgs, false, ""
	}

	unit = scopeUnitName(ownerID, mcpName)
	wrapArgs := []string{
		"--user",
		"--scope",
		"--quiet",
		"--collect",
		"--unit=" + unit,
		"--slice=mcp-pool.slice",
		"-p", "MemoryMax=1G",
		"-p", "CPUWeight=50",
		"-p", "TasksMax=200",
		"--",
		origCommand,
	}
	wrapArgs = append(wrapArgs, origArgs...)
	return sr, wrapArgs, true, unit
}
