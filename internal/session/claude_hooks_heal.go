package session

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/atomicfile"
	"github.com/asheshgoplani/agent-deck/internal/update"
)

// Review round 2 (P1-A/P1-B): a pinned hook entry can go stale on its own.
// A package upgrade removes the file it names, a stale keg or PATH shadow
// runs an older version than the daemon, and a Stop entry installed before
// the sync marker existed lacks it. None of that used to be repaired unless
// the operator ran `hooks install` by hand. HealClaudeHooks is the self-heal:
// the daemon runs it at startup (`hooks install` is the explicit form;
// `hooks status` only reports, review round 3 finding 2).

// ClaudeHooksHealResult says whether HealClaudeHooks rewrote settings.json
// and why, or why it refused to.
type ClaudeHooksHealResult struct {
	Healed  bool     `json:"healed"`
	Reasons []string `json:"reasons,omitempty"`
	// Skipped is set when the install needs a repair this binary must not
	// make: it is an unpinnable dev build, or a hook binary is NEWER than it
	// (an older binary must never "heal" the fleet back to itself).
	Skipped string `json:"skipped,omitempty"`
}

var healLogOnce sync.Once

// healBackupPrefix is the name prefix of the one-time backup the first heal
// leaves next to settings.json.
const healBackupPrefix = "settings.json.bak-agentdeck-"

// HealClaudeHooks repairs an EXISTING agent-deck hook install under configDir
// whose entries no longer run this binary correctly: a program that no longer
// exists, a resolved binary reporting a different version than currentVersion,
// or config drift (a missing event, an async flag, the Stop sync marker). It
// never installs hooks where none are present, never writes from an
// unpinnable binary or over a newer one, and never writes malformed JSON
// back (the parse error is returned instead). The rewrite goes through
// InjectClaudeHooks (lossless, atomic settings.json write, no-op when nothing
// changes), keeps a one-time backup of the pre-heal file, is idempotent, and
// is logged once per process.
func HealClaudeHooks(configDir, currentVersion string) (ClaudeHooksHealResult, error) {
	var res ClaudeHooksHealResult
	hooks, err := readClaudeHooksSection(configDir)
	if err != nil {
		return res, fmt.Errorf("heal hooks: %w", err)
	}
	commands := distinctAgentDeckHookCommands(hooks)
	if len(commands) == 0 {
		return res, nil
	}
	executable, err := hookExecutablePath()
	if err != nil {
		return res, fmt.Errorf("heal hooks: resolve executable: %w", err)
	}
	if executable == "" {
		res.Skipped = unpinnableHookExecutableReason
		logHealOnce("claude_hooks_heal_skipped", configDir, res.Skipped)
		return res, nil
	}
	res.Reasons, res.Skipped = claudeHooksHealReasons(hooks, commands, executable, currentVersion)
	if res.Skipped != "" {
		logHealOnce("claude_hooks_heal_skipped", configDir, res.Skipped)
		return res, nil
	}
	if len(res.Reasons) == 0 {
		return res, nil
	}
	if err := backupSettingsOnce(configDir); err != nil {
		return res, fmt.Errorf("heal hooks: %w", err)
	}
	installed, err := InjectClaudeHooks(configDir)
	if err != nil {
		return res, fmt.Errorf("heal hooks: %w", err)
	}
	res.Healed = installed
	if installed {
		logHealOnce("claude_hooks_healed", configDir, strings.Join(res.Reasons, "; "))
	}
	return res, nil
}

func logHealOnce(msg, configDir, detail string) {
	healLogOnce.Do(func() {
		sessionLog.Info(msg,
			slog.String("config_dir", configDir),
			slog.String("reasons", detail))
	})
}

// backupSettingsOnce copies settings.json to settings.json.bak-agentdeck-<ts>
// unless a backup from an earlier heal already exists.
func backupSettingsOnce(configDir string) error {
	if existing, _ := filepath.Glob(filepath.Join(configDir, healBackupPrefix+"*")); len(existing) > 0 {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(configDir, "settings.json"))
	if err != nil {
		return fmt.Errorf("read settings.json for backup: %w", err)
	}
	backup := filepath.Join(configDir, healBackupPrefix+time.Now().Format("20060102-150405"))
	if err := atomicfile.WriteFile(backup, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", backup, err)
	}
	return nil
}

// claudeHooksHealReasons lists why the install under hooks (whose agent-deck
// entries run commands) needs a rewrite, empty when it is healthy for this
// binary at executable. skipped is non-empty when a hook binary reports a
// NEWER version than currentVersion: this binary is the stale one and must
// not rewrite the install (review round 3, finding 2).
func claudeHooksHealReasons(hooks map[string]json.RawMessage, commands []string, executable, currentVersion string) (reasons []string, skipped string) {
	for _, command := range commands {
		if hookCommandProgramMissing(command) {
			reasons = append(reasons, "hook program no longer exists: "+command)
			continue
		}
		b := resolveHookBinary(command, executable, currentVersion)
		switch {
		case b.ResolveError != "":
			reasons = append(reasons, "hook command "+command+" cannot be resolved: "+b.ResolveError)
		case b.Shadowed && b.VersionMismatch && update.CompareVersions(b.Version, currentVersion) > 0:
			return nil, "hook binary " + b.ResolvedPath + " is v" + b.Version + ", newer than this binary v" + currentVersion + ": not healed"
		case b.Shadowed && b.VersionMismatch:
			reasons = append(reasons, "hook binary "+b.ResolvedPath+" is v"+b.Version+", this binary is v"+currentVersion)
		}
	}
	if len(reasons) == 0 && !hooksAlreadyInstalled(hooks) {
		reasons = append(reasons, "hook config drifted (missing event, async flag or Stop sync marker)")
	}
	return reasons, ""
}
