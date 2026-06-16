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

	settingsPath := filepath.Join(projectPath, ".claude", "settings.json")

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
