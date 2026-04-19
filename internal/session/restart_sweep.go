package session

// Issue #666: cross-tmux duplicate-session sweep for the respawn-pane
// restart path.
//
// Background: the fallback restart branch at instance.go already calls
// tmux.KillSessionsWithEnvValue after recreating its tmux session to kill
// any OTHER agentdeck tmux session that holds the same Claude session id
// (issue #596 guard against double `claude --resume` on one conversation).
// The primary respawn-pane branches did not run that sweep, so a user
// who ended up with two agentdeck tmux sessions referencing the same
// tool session id (fork-then-edit path, or manual `session set
// claude-session-id` collision) could restart one while the other's
// claude process kept running — compounding the telegram 409 conflict
// users were hitting on conductor hosts.
//
// The hook var makes the sweep testable without a live tmux server.

import (
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// killDuplicateSessionsFn is indirected so tests can substitute a spy.
// Production calls flow to tmux.KillSessionsWithEnvValue which shells out
// to `tmux list-sessions` + `tmux show-environment` + `tmux kill-session`.
var killDuplicateSessionsFn = tmux.KillSessionsWithEnvValue

// sweepDuplicateToolSessions kills agentdeck tmux sessions (other than
// this instance's) that duplicate this instance. It runs up to two sweeps:
//
//  1. Tool-session-id sweep (issue #596/#666 guard). Kills sessions
//     sharing the same CLAUDE_/GEMINI_/OPENCODE_/CODEX_SESSION_ID, so a
//     fork-then-edit collision doesn't leave two `claude --resume`
//     processes fighting over one conversation.
//
//  2. Instance-id sweep (issue #678 guard). Kills sessions sharing the
//     same AGENTDECK_INSTANCE_ID. This covers shell / placeholder
//     sessions that have no tool-level session id — the tool-sweep
//     above is a no-op for them, and without the instance-id sweep
//     every SSH respawn race on Linux+systemd accumulated a new
//     duplicate tmux session (10 observed after a 2-week run with 30
//     shell projects in @bautrey's v0.28.3 fork).
//
// Running both is safe: the second sweep is a no-op if the first
// already killed the stale session. Both exclude our own tmux session
// by name, so this instance is never the target.
func (i *Instance) sweepDuplicateToolSessions() {
	if i.tmuxSession == nil {
		return
	}
	keepName := i.tmuxSession.Name

	switch {
	case IsClaudeCompatible(i.Tool) && i.ClaudeSessionID != "":
		killDuplicateSessionsFn("CLAUDE_SESSION_ID", i.ClaudeSessionID, keepName)
	case i.Tool == "gemini" && i.GeminiSessionID != "":
		killDuplicateSessionsFn("GEMINI_SESSION_ID", i.GeminiSessionID, keepName)
	case i.Tool == "opencode" && i.OpenCodeSessionID != "":
		killDuplicateSessionsFn("OPENCODE_SESSION_ID", i.OpenCodeSessionID, keepName)
	case i.Tool == "codex" && i.CodexSessionID != "":
		killDuplicateSessionsFn("CODEX_SESSION_ID", i.CodexSessionID, keepName)
	}

	if i.ID != "" {
		killDuplicateSessionsFn("AGENTDECK_INSTANCE_ID", i.ID, keepName)
	}
}
