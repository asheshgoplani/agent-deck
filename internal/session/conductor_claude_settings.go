package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Conductor .claude/settings.json permission allowlist (issue #1358).
//
// A Claude conductor's core read loop (status -> list -> session output) fires
// on every heartbeat and at startup. With dangerous_mode=false this produces a
// wall of permission prompts. We auto-allow genuinely read-only and safe
// lifecycle CLI commands, and scope file writes to the conductor's own data
// files — deliberately NOT the executable/config files in its directory.
//
// Security tightenings applied (maintainer ruling on #1358):
//   - session start/stop/restart are NOT silently auto-allowed. They replay the
//     session's stored command (which can be a permission-skipping wrapper) and
//     start/restart accept -m/--message prompt injection. They go to the ask
//     list so the user still confirms.
//   - The write-allow is NOT a recursive /** over the conductor dir. A conductor
//     dir holds executables/config that run on spawn/hook (.claude/settings.json
//     = arbitrary hooks, .mcp.json = arbitrary stdio servers, .envrc = injected
//     env, *.sh = scripts). A recursive write-allow would let the conductor
//     silently rewrite any of these for arbitrary code execution on the next
//     hook/spawn, defeating dangerous_mode=false. Writes are scoped to data
//     files (*.md, state.json), and explicit deny rules guard the executable/
//     config paths (deny takes precedence over any broader glob).

// conductorAutoAllowCommands are the read-only / safe CLI commands a conductor
// may run without a prompt. Pure reads plus restart (replays the stored command
// but has no -m injection flag — acceptable per the ruling).
//
// Shell commands must be wrapped in Bash(...) — Claude Code interprets a bare
// string as a tool name, not a command, so an unwrapped entry would silently
// fail to match and the policy would not apply.
var conductorAutoAllowCommands = []string{
	"Bash(agent-deck status *)",
	"Bash(agent-deck list *)",
	"Bash(agent-deck session output *)",
	"Bash(agent-deck session show *)",
	"Bash(agent-deck session current)",
	"Bash(agent-deck inbox *)",
	"Bash(agent-deck mcp list)",
	"Bash(agent-deck version)",
	"Bash(agent-deck session restart *)",
}

// conductorAskCommands are mutating / lifecycle / replay-risk commands that must
// still prompt. Listed explicitly so the policy is auditable and so the user is
// asked rather than silently denied. Wrapped in Bash(...) for the same reason as
// the allow list.
var conductorAskCommands = []string{
	"Bash(agent-deck session start *)",
	"Bash(agent-deck session stop *)",
	"Bash(agent-deck session send *)",
	"Bash(agent-deck launch *)",
	"Bash(agent-deck add *)",
	"Bash(agent-deck mcp attach *)",
	"Bash(agent-deck mcp detach *)",
	"Bash(agent-deck session set *)",
	"Bash(agent-deck session move *)",
	"Bash(agent-deck session switch-account *)",
	"Bash(agent-deck session fork *)",
	"Bash(agent-deck session remove *)",
}

// conductorWriteAllowPatterns builds the scoped file-write allowlist for a
// conductor directory. Narrow on purpose: only the conductor's data files, never
// a recursive /** over the dir.
func conductorWriteAllowPatterns(dir string) []string {
	return []string{
		"Edit(//" + dir + "/*.md)",
		"Write(//" + dir + "/*.md)",
		"Write(//" + dir + "/state.json)",
		"Write(//" + dir + "/task-log.md)",
	}
}

// conductorWriteDenyPatterns builds deny rules for the executable/config paths
// inside a conductor directory. Deny takes precedence over allow, so even if a
// broader allow glob is ever introduced these stay protected from
// self-escalation.
func conductorWriteDenyPatterns(dir string) []string {
	return []string{
		"Write(//" + dir + "/.claude/**)",
		"Edit(//" + dir + "/.claude/**)",
		"Write(//" + dir + "/.mcp.json)",
		"Edit(//" + dir + "/.mcp.json)",
		"Write(//" + dir + "/.envrc)",
		"Edit(//" + dir + "/.envrc)",
		"Write(//" + dir + "/*.sh)",
		"Edit(//" + dir + "/*.sh)",
	}
}

// WriteConductorClaudeSettings writes (or merges into) the conductor's
// .claude/settings.json with the auto-allow/ask/deny permission policy from
// #1358. Claude-only; other agents don't use this file.
//
// Existing unmanaged keys in settings.json are preserved. Managed permission
// entries are merged in idempotently: re-running setup keeps the policy current
// without duplicating entries or discarding user-added permissions.
func WriteConductorClaudeSettings(name string) error {
	dir, err := ConductorNameDir(name)
	if err != nil {
		return fmt.Errorf("failed to get conductor dir: %w", err)
	}
	return writeConductorClaudeSettingsAt(dir)
}

func writeConductorClaudeSettingsAt(dir string) error {
	claudeDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", claudeDir, err)
	}
	settingsPath := filepath.Join(claudeDir, "settings.json")

	// Preserve any existing top-level keys (and any user-added permissions).
	root := map[string]json.RawMessage{}
	if data, err := os.ReadFile(settingsPath); err == nil {
		if uErr := json.Unmarshal(data, &root); uErr != nil {
			// Don't clobber a file we can't parse — surface the error instead.
			return fmt.Errorf("existing %s is not valid JSON: %w", settingsPath, uErr)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", settingsPath, err)
	}

	// Preserve every nested permissions key (defaultMode, additionalDirectories,
	// disableBypassPermissionsMode, etc.). We only touch the three string arrays
	// we manage — allow, deny, ask — and write the rest back untouched.
	permsObj := map[string]json.RawMessage{}
	if raw, ok := root["permissions"]; ok {
		if err := json.Unmarshal(raw, &permsObj); err != nil {
			return fmt.Errorf("existing permissions in %s is not valid JSON: %w", settingsPath, err)
		}
	}

	allow, err := mergeUniqueRaw(permsObj["allow"], conductorAutoAllowCommands, conductorWriteAllowPatterns(dir))
	if err != nil {
		return fmt.Errorf("merge permissions.allow in %s: %w", settingsPath, err)
	}
	ask, err := mergeUniqueRaw(permsObj["ask"], conductorAskCommands)
	if err != nil {
		return fmt.Errorf("merge permissions.ask in %s: %w", settingsPath, err)
	}
	deny, err := mergeUniqueRaw(permsObj["deny"], conductorWriteDenyPatterns(dir))
	if err != nil {
		return fmt.Errorf("merge permissions.deny in %s: %w", settingsPath, err)
	}
	permsObj["allow"] = allow
	permsObj["ask"] = ask
	permsObj["deny"] = deny

	permsJSON, err := json.Marshal(permsObj)
	if err != nil {
		return fmt.Errorf("marshal permissions: %w", err)
	}
	root["permissions"] = permsJSON

	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	out = append(out, '\n')
	if err := os.WriteFile(settingsPath, out, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", settingsPath, err)
	}
	return nil
}

// mergeUniqueRaw decodes an existing JSON string array (which may be nil/absent,
// preserving any user-added entries), appends the managed additions skipping
// duplicates, and returns a sorted JSON array for deterministic output. Returns
// an error if the existing value is present but not a JSON string array.
func mergeUniqueRaw(existing json.RawMessage, additions ...[]string) (json.RawMessage, error) {
	var base []string
	if len(existing) > 0 {
		if err := json.Unmarshal(existing, &base); err != nil {
			return nil, fmt.Errorf("not a JSON string array: %w", err)
		}
	}
	seen := map[string]bool{}
	merged := []string{}
	appendUnique := func(s string) {
		if !seen[s] {
			seen[s] = true
			merged = append(merged, s)
		}
	}
	for _, b := range base {
		appendUnique(b)
	}
	for _, add := range additions {
		for _, a := range add {
			appendUnique(a)
		}
	}
	sort.Strings(merged)
	return json.Marshal(merged)
}
