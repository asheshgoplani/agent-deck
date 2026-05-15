// Production-wiring regression test for issue #965 (PR #1000 follow-up).
//
// PR #1000 shipped Instance.RegisterMCPChild / reapTrackedMCPChildren but
// left wiring to a follow-up. Without wiring, no production code path
// populates TrackedMCPPIDs, so on session stop the reaper still has
// nothing to target and stdio MCP children continue to reparent to PID 1.
//
// agent-deck never has a direct exec.Command for stdio MCP servers —
// they're spawned by claude/codex/gemini when those tools read
// .mcp.json. The only place agent-deck can observe their PIDs is at
// stop time, by walking the pane process tree. This test pins that
// behavior end-to-end on a real tmux session.
package session

import (
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// TestKillInternal_DiscoversAndReapsMcpChildrenFromPaneTree proves
// killInternal walks the tmux pane process tree and SIGTERMs descendants
// so non-tmux-whitelist processes (e.g. `uvx`, `python`, `bun`-based
// MCP servers — substituted here by `sleep`, which also misses tmux's
// {claude,node,zsh,bash,sh,cat,npm} whitelist in
// internal/tmux/tmux.go:isOurProcess) don't reparent to PID 1.
func TestKillInternal_DiscoversAndReapsMcpChildrenFromPaneTree_RegressionFor965(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("unix-only: relies on syscall.Kill semantics")
	}
	skipIfNoTmuxBinary(t)
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Skip("pgrep not available")
	}
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid not available (needed to model issue #965 — a detached MCP child")
	}

	sessName := tmux.SessionPrefix + "issue965-wiring-" + strconv.Itoa(int(time.Now().UnixNano()))
	if err := exec.Command("tmux", "new-session", "-d", "-s", sessName, "sh", "-c", "sleep 3600").Run(); err != nil {
		t.Fatalf("create tmux session: %v", err)
	}
	t.Cleanup(func() {
		_ = exec.Command("tmux", "kill-session", "-t", sessName).Run()
	})

	// Model the issue-#965 shape on a realistic depth-2 process tree:
	//
	//   pane shell (sh, depth 0)
	//   ├── intermediate sh (depth 1, models claude/codex/gemini)
	//   │     └── setsid sleep (depth 2, models a detached MCP server
	//   │                       such as @upstash/context7-mcp that
	//   │                       npx-wraps into its own session)
	//   └── foreground sleep (depth 1, models the tool main loop —
	//                         keeps the pane alive)
	//
	// The setsid is the leaker: it detaches from the pane's process
	// group, so tmux's pgroup-wide kill-session misses it. Only
	// explicit per-PID SIGTERM (the PR-#1000 reaper) reaches it.
	pidFile := t.TempDir() + "/mcp.pid"
	if err := exec.Command(
		"tmux", "respawn-pane", "-k", "-t", sessName,
		"sh", "-c",
		"sh -c \"setsid sleep 3600 < /dev/null > /dev/null 2>&1 & echo \\$! > "+pidFile+" ; sleep 3600\" & sleep 3600",
	).Run(); err != nil {
		t.Fatalf("respawn-pane with detached fake child: %v", err)
	}

	// Read the pane PID for diagnostics.
	panePIDOut, err := exec.Command("tmux", "list-panes", "-t", sessName+":", "-F", "#{pane_pid}").Output()
	if err != nil {
		t.Fatalf("list-panes: %v", err)
	}
	panePID, err := strconv.Atoi(strings.TrimSpace(string(panePIDOut)))
	if err != nil || panePID <= 0 {
		t.Fatalf("parse pane pid %q: %v", string(panePIDOut), err)
	}

	// Read the fake MCP child PID from the file written by the shell.
	var mcpPID int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, rerr := exec.Command("cat", pidFile).Output()
		if rerr == nil {
			if pid, cerr := strconv.Atoi(strings.TrimSpace(string(data))); cerr == nil && pid > 0 {
				mcpPID = pid
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if mcpPID == 0 {
		t.Fatalf("could not read fake MCP child pid from %s (pane pid was %d)", pidFile, panePID)
	}
	t.Cleanup(func() {
		// Belt-and-suspenders: if the production code failed to kill
		// the child, the test must still reap it so we don't leak.
		_ = syscall.Kill(mcpPID, syscall.SIGKILL)
	})

	// Sanity: the fake MCP child is alive AND its name does NOT match
	// tmux's isOurProcess whitelist — so only the new PR-#1000 reaper
	// can rescue it. If tmux's whitelist ever grows to include "sleep"
	// this test would silently start passing for the wrong reason.
	if err := syscall.Kill(mcpPID, syscall.Signal(0)); err != nil {
		t.Fatalf("fake MCP child pid %d not alive: %v", mcpPID, err)
	}

	// Build an Instance pointing to the running tmux session. We can't
	// use NewInstanceWithTool().Start() here because that would spawn a
	// real claude/codex pane and we'd lose control of the descendant
	// shape. Construct directly and attach via ReconnectSessionLazy,
	// the same path the TUI uses on cold start.
	inst := &Instance{
		ID:    "issue965-wiring",
		Title: "issue965 wiring",
	}
	inst.tmuxSession = tmux.ReconnectSessionLazy(sessName, "issue965-wiring", "/tmp", "sh", "running")

	// Sanity: session is alive immediately before Kill.
	if has := exec.Command("tmux", "has-session", "-t", sessName).Run(); has != nil {
		t.Fatalf("session %s gone before inst.Kill: %v", sessName, has)
	}

	if err := inst.Kill(); err != nil {
		t.Fatalf("inst.Kill: %v", err)
	}

	// After Kill(), the fake MCP child must be dead.
	if !waitChildDead(t, mcpPID, 5*time.Second) {
		t.Fatalf("fake MCP child pid %d still alive after Instance.Kill — production wiring missing for #965 (PR #1000 follow-up)", mcpPID)
	}
}
