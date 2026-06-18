package session

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// Marketplace-plugin loadout: bring `claude plugin install`-ed marketplace
// plugins under the same per-agent (cwd-scoped) control as berg-store skills.
//
// MECHANISM — project-scope enabledPlugins (NOT a skills symlink): a marketplace
// plugin's FULL surface (skills + MCP servers + hooks) loads per-agent only when
// the plugin is enabled in the agent's PROJECT settings.json
// (<project>/.claude/settings.json) under enabledPlugins["<name>@<marketplace>"].
// A bare @skills-dir symlink loads the plugin's skills and hooks but NOT its MCP
// servers — verified live: cloudflare's MCP came alive from project-scope
// enabledPlugins while the plugin was globally disabled. So the control point is
// settings.json, not .claude/skills/.
//
// The pattern remains: disable globally (so a plugin doesn't bleed into every
// session) + enable per-agent here. enabledPlugins entries are merged into the
// existing settings.json — never clobbering permissions or any other key, and
// preserving plugin entries the agent already had.
//
// AUTO-FIND SOURCE: ~/.claude/plugins/installed_plugins.json maps
// "<plugin>@<marketplace>" -> []installation. We resolve the bare config name to
// that full key (the enabledPlugins map is keyed by the full "<name>@<market>").

const marketplacePluginPrefix = "installed/"

// installedPlugin mirrors one entry in installed_plugins.json's per-key array.
type installedPlugin struct {
	Scope       string `json:"scope"`
	InstallPath string `json:"installPath"`
	Version     string `json:"version"`
}

// installedPluginsFile is the top-level shape of installed_plugins.json.
type installedPluginsFile struct {
	Plugins map[string][]installedPlugin `json:"plugins"`
}

// marketplacePluginRef reports whether a loadout entry references a marketplace
// plugin (the "installed/" source prefix) and, if so, returns the bare plugin
// reference (which may itself carry a "@<marketplace>" disambiguator).
func marketplacePluginRef(entry string) (string, bool) {
	trimmed := strings.TrimSpace(entry)
	if !strings.HasPrefix(trimmed, marketplacePluginPrefix) {
		return "", false
	}
	ref := strings.TrimSpace(strings.TrimPrefix(trimmed, marketplacePluginPrefix))
	if ref == "" {
		return "", false
	}
	return ref, true
}

// installedPluginsPath returns the path to installed_plugins.json under the
// active Claude config dir.
func installedPluginsPath() string {
	return filepath.Join(GetClaudeConfigDir(), "plugins", "installed_plugins.json")
}

// resolveProjectSettingsPath builds <projectPath>/.claude/settings.json and
// fails closed unless BOTH the lexical string AND the real filesystem parent
// stay strictly within projectPath. projectPath is operator-controlled (CLI
// --path / config default_path), so CodeQL flags it flowing into the
// os.ReadFile / os.MkdirAll / atomicWriteFile path sinks in
// enableMarketplacePlugin (alerts 55, 56).
//
// Lexical containment alone is escapable (the #1429 lexical-vs-filesystem
// containment class): a project-local `.claude` SYMLINK makes the lexically
// contained "<project>/.claude/settings.json" read/temp/rename land in the
// symlink target OUTSIDE the project — atomicWriteFile only protects a symlinked
// FINAL file, NOT a symlinked PARENT directory (os.CreateTemp + os.Rename run
// inside filepath.Dir(settingsPath), which is the symlink). So this also:
//
//   - canonicalizes the project root (EvalSymlinks) as the trust root, and
//   - NO-FOLLOWS a `.claude` component that is a symlink (rejects it outright),
//     and verifies a real `.claude` dir resolves inside the canonical root, and
//   - rejects a `settings.json` that is itself a symlink resolving outside the
//     project (the os.ReadFile sink would otherwise follow it).
//
// The project directory is the trust root by construction — it is where the
// agent runs — but it must be an absolute, traversal-free path and the actual
// parent dir used for read/temp/write must resolve inside it. Boundary-aware +
// fail-closed, mirroring ValidateTranscriptPath (#1435) and the
// conductor_migrate_dir.go resolveCanonical/pathContains precedent.
func resolveProjectSettingsPath(projectPath string) (string, error) {
	base := filepath.Clean(projectPath)
	if base == "" || base == "." || !filepath.IsAbs(base) {
		return "", fmt.Errorf("project path %q is not an absolute directory", projectPath)
	}
	if base == ".." || strings.HasPrefix(base, ".."+string(os.PathSeparator)) ||
		strings.Contains(base, string(os.PathSeparator)+".."+string(os.PathSeparator)) ||
		strings.HasSuffix(base, string(os.PathSeparator)+"..") {
		return "", fmt.Errorf("project path %q contains a traversal segment", projectPath)
	}

	settingsPath := filepath.Clean(filepath.Join(base, ".claude", "settings.json"))
	// Lexical containment under the cleaned base — preserves behavior for a
	// project dir that does not yet exist on disk (a fresh project where the
	// .claude dir will be created), where there is no symlink to follow.
	if settingsPath != base && !strings.HasPrefix(settingsPath, base+string(os.PathSeparator)) {
		return "", fmt.Errorf("settings path %q escapes project dir %q", settingsPath, base)
	}

	// Filesystem containment: defend the symlinked-parent escape that lexical
	// containment cannot see. Resolve the real project root and check the real
	// `.claude` parent (and a symlinked settings.json) against it.
	canonicalBase := resolveCanonical(base)
	claudeDir := filepath.Join(base, ".claude")
	if info, lerr := os.Lstat(claudeDir); lerr == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			// No-follow: a symlinked `.claude` is the exact parent-dir escape
			// (atomicWriteFile temp+rename would land in the symlink target).
			return "", fmt.Errorf("project %q has a symlinked .claude; refusing to follow it outside the project", base)
		}
		realClaude, rerr := filepath.EvalSymlinks(claudeDir)
		if rerr != nil {
			return "", fmt.Errorf("resolve project .claude dir for %q: %w", base, rerr)
		}
		if !dirContainsOrEqual(canonicalBase, realClaude) {
			return "", fmt.Errorf("project .claude dir %q escapes project root %q", realClaude, canonicalBase)
		}
	}
	if info, lerr := os.Lstat(settingsPath); lerr == nil && info.Mode()&os.ModeSymlink != 0 {
		// A symlinked settings.json is followed by the os.ReadFile sink; allow
		// it only if its real target stays inside the project.
		realSettings, rerr := filepath.EvalSymlinks(settingsPath)
		if rerr != nil {
			return "", fmt.Errorf("resolve settings.json symlink for %q: %w", base, rerr)
		}
		if !dirContainsOrEqual(canonicalBase, realSettings) {
			return "", fmt.Errorf("settings.json %q resolves outside project root %q", realSettings, canonicalBase)
		}
	}

	return settingsPath, nil
}

// dirContainsOrEqual reports whether child equals parent or is nested under it.
// Both must be cleaned absolute paths. Boundary-aware (parent+separator) so a
// sibling whose string prefix matches parent is not treated as contained.
func dirContainsOrEqual(parent, child string) bool {
	return child == parent || strings.HasPrefix(child, parent+string(os.PathSeparator))
}

// marketplacePluginName is the plugin name with any "@<marketplace>"
// disambiguator stripped — the marketplace is only a selector, not part of the
// plugin's identity.
func marketplacePluginName(ref string) string {
	if idx := strings.Index(ref, "@"); idx >= 0 {
		return strings.TrimSpace(ref[:idx])
	}
	return strings.TrimSpace(ref)
}

// splitMarketplaceKey splits an installed_plugins.json key "<name>@<marketplace>"
// into its parts. A key without "@" yields an empty marketplace.
func splitMarketplaceKey(key string) (name, marketplace string) {
	if idx := strings.Index(key, "@"); idx >= 0 {
		return strings.TrimSpace(key[:idx]), strings.TrimSpace(key[idx+1:])
	}
	return strings.TrimSpace(key), ""
}

// resolveMarketplacePluginKeyFrom resolves a plugin reference to its full
// installed_plugins.json key ("<name>@<marketplace>") using the supplied file
// path. The ref is either a bare plugin name ("loom") or a marketplace-qualified
// key ("loom@berg-plugins"). A bare name that resolves to more than one
// marketplace is an error (Go map iteration order is non-deterministic, so
// silently picking one would be unstable) — qualify it as "<name>@<marketplace>".
//
// The full key is what Claude's enabledPlugins map is keyed by, so that is what
// we return (not the installPath — enablement is by key, not by path).
//
// Split out from ResolveMarketplacePluginKey so the resolution logic is unit
// testable against a fixture file without touching the real Claude config dir.
func resolveMarketplacePluginKeyFrom(installedPath, ref string) (string, error) {
	data, err := os.ReadFile(installedPath)
	if err != nil {
		return "", fmt.Errorf("read installed_plugins.json: %w", err)
	}
	var file installedPluginsFile
	if err := json.Unmarshal(data, &file); err != nil {
		return "", fmt.Errorf("parse installed_plugins.json: %w", err)
	}

	wantName := marketplacePluginName(ref)
	wantMarketplace := ""
	if idx := strings.Index(ref, "@"); idx >= 0 {
		wantMarketplace = strings.TrimSpace(ref[idx+1:])
	}

	var matchedKeys []string
	for key := range file.Plugins {
		name, marketplace := splitMarketplaceKey(key)
		if name != wantName {
			continue
		}
		if wantMarketplace != "" && marketplace != wantMarketplace {
			continue
		}
		matchedKeys = append(matchedKeys, key)
	}

	switch len(matchedKeys) {
	case 0:
		return "", fmt.Errorf("plugin %q not found in installed_plugins.json", ref)
	case 1:
		return matchedKeys[0], nil
	default:
		return "", fmt.Errorf("plugin %q is ambiguous across marketplaces %v — qualify as <name>@<marketplace>", wantName, matchedKeys)
	}
}

// ResolveMarketplacePluginKey resolves a marketplace plugin reference to its
// full "<name>@<marketplace>" key using the active Claude config dir's
// installed_plugins.json.
func ResolveMarketplacePluginKey(ref string) (string, error) {
	return resolveMarketplacePluginKeyFrom(installedPluginsPath(), ref)
}

// marketplaceEnableAction describes what enableMarketplacePlugin did, for
// caller-side logging.
type marketplaceEnableAction string

const (
	marketplaceEnableNoop    marketplaceEnableAction = "healthy" // already enabled
	marketplaceEnableEnabled marketplaceEnableAction = "enabled" // added/flipped to true
)

// enableMarketplacePlugin resolves a marketplace plugin ref to its full key and
// merges enabledPlugins["<name>@<marketplace>"] = true into the agent's project
// settings.json (<projectPath>/.claude/settings.json). Floor semantics:
//
//   - key already present and true   -> no-op (healthy)
//   - key absent, or present false   -> set true
//
// The merge is non-destructive: every other settings.json key (permissions,
// model, hooks, …) and every other enabledPlugins entry is preserved verbatim.
// A wrong-shaped enabledPlugins value (e.g. a legacy array) is refused rather
// than clobbered — the caller surfaces it as a warning and the entry is skipped.
func enableMarketplacePlugin(projectPath, ref string) (key string, action marketplaceEnableAction, err error) {
	key, err = ResolveMarketplacePluginKey(ref)
	if err != nil {
		return "", "", err
	}

	// CodeQL alerts 55/56 (uncontrolled data in path expression): projectPath is
	// operator-controlled (CLI --path / config default_path) and flows into the
	// os.ReadFile / os.MkdirAll / atomicWriteFile path sinks below. Resolve the
	// settings path through a boundary-aware, fail-closed guard so the sinks only
	// ever touch <project>/.claude/settings.json strictly inside the operator's
	// own project tree — never a path that escapes it via traversal. A path that
	// cannot be proven contained is refused, surfaced to the caller as a per-entry
	// loadout warning (the spawn is never blocked).
	settingsPath, err := resolveProjectSettingsPath(projectPath)
	if err != nil {
		return key, "", err
	}

	settings := map[string]interface{}{}
	if data, readErr := os.ReadFile(settingsPath); readErr == nil {
		if jsonErr := json.Unmarshal(data, &settings); jsonErr != nil {
			return key, "", fmt.Errorf("parse %s: %w", settingsPath, jsonErr)
		}
	} else if !os.IsNotExist(readErr) {
		return key, "", fmt.Errorf("read %s: %w", settingsPath, readErr)
	}

	plugins, ok := settings["enabledPlugins"].(map[string]interface{})
	if !ok {
		if raw, present := settings["enabledPlugins"]; present && raw != nil {
			// Never clobber an unexpected shape — refuse and let the caller warn.
			return key, "", fmt.Errorf("enabledPlugins in %s is %T, not an object; refusing to overwrite", settingsPath, raw)
		}
		plugins = map[string]interface{}{}
	}

	if cur, exists := plugins[key].(bool); exists && cur {
		return key, marketplaceEnableNoop, nil
	}

	plugins[key] = true
	settings["enabledPlugins"] = plugins

	out, marshalErr := json.MarshalIndent(settings, "", "  ")
	if marshalErr != nil {
		return key, "", fmt.Errorf("marshal settings: %w", marshalErr)
	}
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		return key, "", fmt.Errorf("create .claude dir: %w", err)
	}
	if err := atomicWriteFile(settingsPath, out, 0o600); err != nil {
		return key, "", fmt.Errorf("write %s: %w", settingsPath, err)
	}
	sessionLog.Debug("loadout_marketplace_enabled",
		slog.String("project", projectPath),
		slog.String("plugin_key", key))
	return key, marketplaceEnableEnabled, nil
}
