package session

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
)

// Review round 2 (P1-A/P1-B): a pinned hook entry can go stale on its own.
// A package upgrade removes the file it names, a stale keg or PATH shadow
// runs an older version than the daemon, and a Stop entry installed before
// the sync marker existed lacks it. None of that used to be repaired unless
// the operator ran `hooks install` by hand. HealClaudeHooks is the self-heal:
// the daemon runs it at startup and `hooks status` runs it before reporting.

// ClaudeHooksHealResult says whether HealClaudeHooks rewrote settings.json
// and why.
type ClaudeHooksHealResult struct {
	Healed  bool     `json:"healed"`
	Reasons []string `json:"reasons,omitempty"`
}

var healLogOnce sync.Once

// HealClaudeHooks repairs an EXISTING agent-deck hook install under configDir
// whose entries no longer run this binary correctly: a program that no longer
// exists, a resolved binary reporting a different version than currentVersion,
// or config drift (a missing event, an async flag, the Stop sync marker). It
// never installs hooks where none are present. The rewrite goes through
// InjectClaudeHooks (atomic settings.json write), is idempotent, and is logged
// once per process.
func HealClaudeHooks(configDir, currentVersion string) (ClaudeHooksHealResult, error) {
	var res ClaudeHooksHealResult
	hooks := readClaudeHooksSection(configDir)
	commands := distinctAgentDeckHookCommands(hooks)
	if len(commands) == 0 {
		return res, nil
	}
	res.Reasons = claudeHooksHealReasons(hooks, commands, currentVersion)
	if len(res.Reasons) == 0 {
		return res, nil
	}
	installed, err := InjectClaudeHooks(configDir)
	if err != nil {
		return res, fmt.Errorf("heal hooks: %w", err)
	}
	res.Healed = installed
	if installed {
		healLogOnce.Do(func() {
			sessionLog.Info("claude_hooks_healed",
				slog.String("config_dir", configDir),
				slog.String("reasons", strings.Join(res.Reasons, "; ")))
		})
	}
	return res, nil
}

// claudeHooksHealReasons lists why the install under hooks (whose agent-deck
// entries run commands) needs a rewrite, empty when it is healthy for this
// binary.
func claudeHooksHealReasons(hooks map[string]json.RawMessage, commands []string, currentVersion string) []string {
	var reasons []string
	executable, _ := hookExecutablePath()
	for _, command := range commands {
		if hookCommandProgramMissing(command) {
			reasons = append(reasons, "hook program no longer exists: "+command)
			continue
		}
		b := resolveHookBinary(command, executable, currentVersion)
		switch {
		case b.ResolveError != "":
			reasons = append(reasons, "hook command "+command+" cannot be resolved: "+b.ResolveError)
		case b.Shadowed && b.VersionMismatch:
			reasons = append(reasons, "hook binary "+b.ResolvedPath+" is v"+b.Version+", this binary is v"+currentVersion)
		}
	}
	if len(reasons) == 0 && !hooksAlreadyInstalled(hooks) {
		reasons = append(reasons, "hook config drifted (missing event, async flag or Stop sync marker)")
	}
	return reasons
}
