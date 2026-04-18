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
// this instance's) whose tool session environment variable matches this
// instance's tool session id. Safe no-op when we have no tmux session to
// exclude, no session id to match, or the tool has no known env var.
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
}
