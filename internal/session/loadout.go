package session

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// ApplyConfiguredLoadout materializes the declarative per-group /
// per-conductor skill, plugin, and MCP loadout
// ([groups.X.claude].skills/.plugins/.mcps and the conductor mirror) for a
// claude-compatible session, by
// driving the existing project-skills attach machinery and local .mcp.json
// writer — exactly as if the user had run `skill attach` / `mcp attach` by
// hand. Called at session create (add/launch) and re-asserted before every
// Start/StartWithMessage/Restart spawn, so a config edit takes effect on
// the next start and a healthy state is a cheap no-op.
//
// Semantics (the loadout is an attach-only FLOOR):
//
//   - already attached (manifest entry + healthy target) → silent no-op
//   - manifest entry whose target went missing → re-materialized (heal)
//   - target exists as a real dir or foreign symlink (not manifest-managed)
//     → skip + warning, never clobber; a human-placed dir beats config
//   - entry not resolvable in the skill-source registry → skip + warning
//   - removing an entry from the config list does NOT detach — subtraction
//     is a deliberate `skill detach` (a config typo must not strip a live
//     session's skills)
//
// MCP entries are [mcps.X] catalog names appended to the session's local
// .mcp.json (never removed); unknown names skip + warn.
//
// The effective lists are the union of the group ancestor chain and, for
// conductor sessions, the conductor block (group floor + conductor extras).
//
// Returns the warnings (also slog-warned) so CLI call sites can print them;
// a nil return means nothing to do or everything healthy. Failures never
// block the spawn — the loadout is provisioning, not a launch gate.
func ApplyConfiguredLoadout(inst *Instance) []string {
	if inst == nil || !IsClaudeCompatible(inst.Tool) {
		return nil
	}
	if inst.ProjectPath == "" || inst.SSHHost != "" {
		// No local project path to materialize into (SSH sessions run on a
		// remote working dir agent-deck cannot symlink into).
		return nil
	}

	config, cfgErr := LoadUserConfig()
	if cfgErr != nil {
		w := fmt.Sprintf("config.toml error — declarative skill/mcp loadout inactive: %v", cfgErr)
		sessionLog.Warn("loadout_config_unreadable",
			slog.String("session", sanitizeLoadoutWarning(inst.Title)),
			slog.String("error", sanitizeLoadoutWarning(cfgErr.Error())))
		return []string{w}
	}
	if config == nil {
		return nil
	}

	skills := unionLoadoutEntries(
		config.GetGroupClaudeSkills(inst.GroupPath),
		config.GetConductorClaudeSkills(conductorNameFromInstance(inst)),
	)
	plugins := unionLoadoutEntries(
		config.GetGroupClaudePlugins(inst.GroupPath),
		config.GetConductorClaudePlugins(conductorNameFromInstance(inst)),
	)
	mcps := unionLoadoutEntries(
		config.GetGroupClaudeMCPs(inst.GroupPath),
		config.GetConductorClaudeMCPs(conductorNameFromInstance(inst)),
	)
	if len(skills) == 0 && len(plugins) == 0 && len(mcps) == 0 {
		return nil
	}

	var warnings []string
	warn := func(format string, args ...interface{}) {
		w := sanitizeLoadoutWarning(fmt.Sprintf(format, args...))
		warnings = append(warnings, w)
		sessionLog.Warn("loadout_entry_skipped",
			slog.String("session", sanitizeLoadoutWarning(inst.Title)),
			slog.String("group", sanitizeLoadoutWarning(inst.GroupPath)),
			slog.String("detail", w))
	}

	attachedSkill := false
	for _, entry := range skills {
		attachment, err := AttachSkillToProject(inst.ProjectPath, inst.Tool, entry, "")
		switch {
		case err == nil:
			attachedSkill = true
			sessionLog.Info("loadout_skill_attached",
				slog.String("session", sanitizeLoadoutWarning(inst.Title)),
				slog.String("skill", sanitizeLoadoutWarning(entry)),
				slog.String("target", sanitizeLoadoutWarning(attachment.TargetPath)))
		case errors.Is(err, ErrSkillAlreadyAttached) && healthyManagedSkillAttachment(inst.ProjectPath, entry):
			// Healthy manifest-managed floor — nothing to do.
		case errors.Is(err, ErrSkillAlreadyAttached):
			warn("skill %q: existing target is not a healthy manifest-managed attachment", entry)
		case errors.Is(err, ErrSkillNotFound) || errors.Is(err, ErrSkillSourceNotFound):
			warn("skill %q: not found in the skill-source registry (register the store with `agent-deck skill source add`)", entry)
		case errors.Is(err, ErrSkillAmbiguous):
			warn("skill %q: ambiguous — qualify as <source>/<name>: %v", entry, err)
		case errors.Is(err, ErrSkillUnsupportedKind):
			warn("skill %q: not an attachable directory skill: %v", entry, err)
		default:
			// Covers the never-clobber conflicts ("target already exists and
			// is not managed", "target already managed by …") and IO errors.
			warn("skill %q: %v", entry, err)
		}
	}

	// Catalog plugins are persisted on the instance and resolved by the
	// existing scratch-config path. Manual plugins stay in place; configured
	// entries only append. Unknown and refused catalog entries warn.
	pluginSet := make(map[string]bool, len(inst.Plugins)+len(plugins))
	for _, name := range inst.Plugins {
		pluginSet[name] = true
	}
	for _, name := range plugins {
		if pluginSet[name] {
			continue
		}
		if GetPluginDef(name) == nil {
			warn("plugin %q: not available in config.toml [plugins.%s] or refused by policy", name, name)
			continue
		}
		inst.Plugins = append(inst.Plugins, name)
		pluginSet[name] = true
	}
	syncPluginChannels(inst)

	// Project-scope plugins only load when the cwd's realpath is trusted in
	// ~/.claude.json (projects[<realpath>].hasTrustDialogAccepted). Seed it
	// here — the same one-key trust the conductor setup pre-accepts
	// (PreAcceptClaudeTrust) — so a materialized skill loadout, which is what
	// carries plugins/hooks, actually loads instead of being silently skipped
	// at spawn. Only when plugins were materialized (mcps load via .mcp.json
	// regardless of trust). Keyed by realpath: Claude resolves the cwd through
	// symlinks, and agent homes are commonly reached via synced/symlinked paths.
	// Empirically one key is sufficient; hasCompletedProjectOnboarding is not
	// required for plugin loading.
	if attachedSkill && inst.Tool == "claude" {
		trustDir := inst.ProjectPath
		if real, err := filepath.EvalSymlinks(trustDir); err == nil {
			trustDir = real
		}
		if err := PreAcceptClaudeTrust(GetUserMCPRootPath(), trustDir); err != nil {
			warn("workspace trust seed for %q failed (plugins may not load): %v", trustDir, err)
		} else {
			sessionLog.Info("loadout_trust_seeded",
				slog.String("session", sanitizeLoadoutWarning(inst.Title)),
				slog.String("dir", sanitizeLoadoutWarning(trustDir)))
		}
	}

	if len(mcps) > 0 {
		available := GetAvailableMCPs()
		info := inst.MCPInfoForLocalAttach()
		if info == nil {
			info = &MCPInfo{}
		}
		current := info.Local()
		attached := make(map[string]bool, len(current))
		for _, name := range current {
			attached[name] = true
		}
		newLocal := append([]string{}, current...)
		added := false
		for _, name := range mcps {
			if attached[name] {
				continue
			}
			if _, ok := available[name]; !ok {
				warn("mcp %q: not defined in config.toml [mcps.%s]", name, name)
				continue
			}
			newLocal = append(newLocal, name)
			attached[name] = true
			added = true
			sessionLog.Info("loadout_mcp_attached",
				slog.String("session", sanitizeLoadoutWarning(inst.Title)),
				slog.String("mcp", sanitizeLoadoutWarning(name)))
		}
		if added {
			if err := inst.WriteLocalMCPConfig(newLocal); err != nil {
				warn("mcp loadout: failed to write local .mcp.json: %v", err)
			} else {
				inst.InvalidateProjectMCPIntegrationsCache()
			}
		}
	}

	return warnings
}

func sanitizeLoadoutWarning(value string) string {
	// strings.ReplaceAll for \r and \n first: it is the newline-strip form
	// CodeQL models as a go/log-injection sanitizer, and these values also
	// feed the structured loadout log records below.
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\u0085' || r == '\u2028' || r == '\u2029':
			return ' '
		case r < 0x20 || (r >= 0x7f && r <= 0x9f):
			return -1
		default:
			return r
		}
	}, value)
}

// healthyManagedSkillAttachment verifies that ErrSkillAlreadyAttached came
// from the manifest-managed target and, for symlink mode, that the link still
// resolves to the recorded source. A foreign replacement must never be
// accepted as a healthy declarative floor.
func healthyManagedSkillAttachment(projectPath, skillID string) bool {
	// The project path can reach here from operator input (the web
	// create-session request body carries it). Instance project paths are
	// always absolute and clean, so refuse relative or dot-dot forms
	// outright before any filesystem access (CodeQL go/path-injection).
	if !filepath.IsAbs(projectPath) || strings.Contains(projectPath, "..") {
		return false
	}
	manifest, err := LoadProjectSkillsManifest(projectPath)
	if err != nil {
		return false
	}
	for _, attachment := range manifest.Skills {
		if normalizeSkillToken(skillIDForAttachment(attachment)) != normalizeSkillToken(skillID) {
			continue
		}
		// Same audit-M3 containment guard as safeRemoveManagedTarget: a
		// tampered manifest TargetPath that is absolute, non-managed, or
		// "../"-escaping is refused before any filesystem access.
		target := resolveTargetPath(projectPath, attachment.TargetPath)
		skillDir, dirOK := managedProjectSkillsDirForTarget(attachment.TargetPath)
		if !dirOK {
			return false
		}
		base := filepath.Join(projectPath, filepath.FromSlash(skillDir))
		if !isContainedIn(base, target) {
			return false
		}
		if _, err := os.Lstat(target); err != nil {
			return false
		}
		if attachment.Mode != "symlink" {
			return true
		}
		actual, actualErr := filepath.EvalSymlinks(target)
		expected, expectedErr := filepath.EvalSymlinks(attachment.SourcePath)
		return actualErr == nil && expectedErr == nil && actual == expected
	}
	return false
}

// unionLoadoutEntries merges loadout lists preserving order (group floor
// first, conductor extras after), deduplicated, blanks dropped.
func unionLoadoutEntries(lists ...[]string) []string {
	seen := make(map[string]bool)
	var union []string
	for _, list := range lists {
		for _, entry := range list {
			entry = strings.TrimSpace(entry)
			if entry == "" || seen[entry] {
				continue
			}
			seen[entry] = true
			union = append(union, entry)
		}
	}
	return union
}
