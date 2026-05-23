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

const claudeConfigDirPrefix = "CLAUDE_CONFIG_DIR="

// FilterEnv returns env with every TELEGRAM_* var and any inherited
// CLAUDE_CONFIG_DIR removed. If childConfigDir is non-empty, a single
// CLAUDE_CONFIG_DIR=<childConfigDir> is appended so the child is pinned to its
// own config dir. The input slice is not mutated.
func FilterEnv(env []string, childConfigDir string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if strings.HasPrefix(kv, "TELEGRAM_") { // #1152 logic
			continue
		}
		if strings.HasPrefix(kv, claudeConfigDirPrefix) { // #1163: never inherit parent CCD
			continue
		}
		out = append(out, kv)
	}
	if childConfigDir != "" {
		out = append(out, claudeConfigDirPrefix+childConfigDir)
	}
	return out
}

// ForLaunch is FilterEnv applied to the current process environment. This is
// the only place os.Environ() is called from a spawn path.
func ForLaunch(childConfigDir string) []string {
	return FilterEnv(os.Environ(), childConfigDir)
}
