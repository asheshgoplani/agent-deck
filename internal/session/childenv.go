package session

import "github.com/asheshgoplani/agent-deck/internal/childenv"

// ChildLaunchEnv returns the environment for a child claude process,
// guaranteed NOT to inherit the conductor's telegram pollution: every
// TELEGRAM_* var and any inherited CLAUDE_CONFIG_DIR are stripped, and the
// child's own config dir is pinned when childConfigDir is non-empty.
//
// Issue #1163: all child-claude spawn paths in internal/session and
// cmd/agent-deck/launch*.go MUST use this — raw os.Environ() is forbidden by
// the forbidigo lint rule in .golangci.yml so a future caller cannot
// reintroduce the CLAUDE_CONFIG_DIR leak (EVIDENCE-env.md). inst is accepted
// for call-site clarity and future per-instance policy.
func ChildLaunchEnv(inst *Instance, childConfigDir string) []string {
	return childenv.ForLaunch(childConfigDir)
}
