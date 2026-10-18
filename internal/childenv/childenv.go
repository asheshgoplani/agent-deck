// Package childenv builds the environment for a child process that
// agent-deck spawns (claude workers, pooled MCP servers), guaranteed not to
// inherit the conductor's telegram pollution.
//
// It lives in its own leaf package — not in internal/session — because
// internal/session imports internal/mcppool, so mcppool cannot import session.
// Both packages (and cmd/agent-deck) import this leaf so a single filter is the
// only way to construct a child env. Direct os.Environ() in the spawn-path
// packages is forbidden by golangci forbidigo (see .golangci.yml); this is the
// one allowlisted home for it.
//
// Issue #1163: a child must NEVER inherit the parent's CLAUDE_CONFIG_DIR. The
// conductor's config dir points at a worker-scratch profile whose settings.json
// enables the telegram plugin; inheriting it makes the child load telegram and
// spawn a duplicate poller. #1152 stripped TELEGRAM_* but not CLAUDE_CONFIG_DIR
// — this closes that gap structurally.
package childenv

import (
	"os"
	"strings"
)

const (
	claudeConfigDirPrefix = "CLAUDE_CONFIG_DIR="
	// claudeJobDirPrefix marks a process started inside a Claude Code
	// background job (agent view). The hook handler treats it as "this hook
	// came from a background worker" (#2683), so no child agent-deck spawns
	// may inherit it. FilterEnv covers claude workers and MCP servers; the
	// tmux spawn path does not go through FilterEnv (it must keep the
	// conductor's TELEGRAM_* / CLAUDE_CONFIG_DIR semantics) and uses
	// WithoutClaudeJobDir instead, plus a process-level unset at agent-deck
	// startup and an explicit unset in the claude pane spawn command.
	claudeJobDirPrefix = "CLAUDE_JOB_DIR="

	// ClaudeJobDirVar is the env var name behind claudeJobDirPrefix.
	ClaudeJobDirVar = "CLAUDE_JOB_DIR"
)

// FilterEnv returns env with every TELEGRAM_* var, any inherited
// CLAUDE_CONFIG_DIR and any inherited CLAUDE_JOB_DIR removed. If
// childConfigDir is non-empty, a single CLAUDE_CONFIG_DIR=<childConfigDir> is
// appended so the child is pinned to its own config dir. The input slice is
// not mutated.
func FilterEnv(env []string, childConfigDir string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if strings.HasPrefix(kv, "TELEGRAM_") { // #1152 logic
			continue
		}
		if strings.HasPrefix(kv, claudeConfigDirPrefix) { // #1163: never inherit parent CCD
			continue
		}
		if strings.HasPrefix(kv, claudeJobDirPrefix) { // #2683: never inherit a bg job marker
			continue
		}
		out = append(out, kv)
	}
	if childConfigDir != "" {
		out = append(out, claudeConfigDirPrefix+childConfigDir)
	}
	return out
}

// WithoutClaudeJobDir returns the env for a tmux client/server spawn: the
// current process environment minus CLAUDE_JOB_DIR (#2683). A tmux server
// first started from inside a Claude Code background job would otherwise put
// that marker into its global environment and hand it to every pane, and the
// hook handler would then drop every hook on that server. Returns nil (inherit
// unchanged, exec.Cmd's default) when the variable is absent, so the common
// path keeps the exact pre-#2683 behaviour.
func WithoutClaudeJobDir() []string {
	if _, ok := os.LookupEnv(ClaudeJobDirVar); !ok {
		return nil
	}
	env := os.Environ()
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, claudeJobDirPrefix) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// ForLaunch is FilterEnv applied to the current process environment. This is
// the only place os.Environ() is called from a spawn path.
func ForLaunch(childConfigDir string) []string {
	return FilterEnv(os.Environ(), childConfigDir)
}
