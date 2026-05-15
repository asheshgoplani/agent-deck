// Pin refresh — issue #960.
//
// Claude Code plugins install under <profile>/plugins/cache/<source>/<name>/<version>/,
// and any .mcp.json entry whose command/args bake an absolute path into that
// versioned directory goes stale the moment the user runs `claude plugin upgrade`.
// agent-deck's existing merge logic (#146) preserves such entries verbatim, so
// regeneration never refreshes them — /mcp reconnect silently keeps loading the
// outdated plugin.
//
// RefreshStalePluginPins rewrites stale version segments in-place. It's called
// from WriteMCPJsonFromConfig before the merge step so the existing-entry
// preservation path sees the refreshed paths, and is also exported for the
// regression test.

package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RefreshStalePluginPins scans mcpFile for mcpServers entries whose `command`
// or `args` strings reference <profileDir>/plugins/cache/<source>/<name>/<version>/...
// where <version> no longer exists on disk. Each stale reference is rewritten
// to point at the most recently installed version directory under the same
// <source>/<name>. Returns the number of entries that were modified.
//
// If mcpFile does not exist, returns (0, nil). If no profileDirs are supplied
// or no pins are stale, returns (0, nil) without touching the file.
func RefreshStalePluginPins(mcpFile string, profileDirs []string) (int, error) {
	if len(profileDirs) == 0 {
		return 0, nil
	}
	data, err := os.ReadFile(mcpFile)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}

	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return 0, fmt.Errorf("parse .mcp.json: %w", err)
	}
	servers, ok := root["mcpServers"].(map[string]any)
	if !ok {
		return 0, nil
	}

	refreshed := 0
	for _, entryRaw := range servers {
		entry, ok := entryRaw.(map[string]any)
		if !ok {
			continue
		}
		changed := false
		if cmd, ok := entry["command"].(string); ok {
			if newCmd, did := refreshPluginCachePath(cmd, profileDirs); did {
				entry["command"] = newCmd
				changed = true
			}
		}
		if argsRaw, ok := entry["args"].([]any); ok {
			for i, a := range argsRaw {
				s, ok := a.(string)
				if !ok {
					continue
				}
				if newArg, did := refreshPluginCachePath(s, profileDirs); did {
					argsRaw[i] = newArg
					changed = true
				}
			}
		}
		if changed {
			refreshed++
		}
	}

	if refreshed == 0 {
		return 0, nil
	}

	newData, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return 0, fmt.Errorf("marshal refreshed .mcp.json: %w", err)
	}
	tmp := mcpFile + ".tmp"
	if err := os.WriteFile(tmp, newData, 0o644); err != nil {
		return 0, fmt.Errorf("write refreshed .mcp.json: %w", err)
	}
	if err := os.Rename(tmp, mcpFile); err != nil {
		os.Remove(tmp)
		return 0, fmt.Errorf("save refreshed .mcp.json: %w", err)
	}
	return refreshed, nil
}

// refreshPluginCachePath returns (rewritten, true) if s contains a stale
// <profileDir>/plugins/cache/<source>/<name>/<version>/... pin and we can
// resolve a current version to replace <version> with. Otherwise returns
// (s, false).
func refreshPluginCachePath(s string, profileDirs []string) (string, bool) {
	for _, prof := range profileDirs {
		if prof == "" {
			continue
		}
		marker := filepath.Join(prof, "plugins", "cache") + string(filepath.Separator)
		idx := strings.Index(s, marker)
		if idx < 0 {
			continue
		}
		rest := s[idx+len(marker):]
		parts := strings.SplitN(rest, string(filepath.Separator), 4)
		if len(parts) < 3 {
			continue
		}
		source, name, version := parts[0], parts[1], parts[2]
		if source == "" || name == "" || version == "" {
			continue
		}
		verDir := filepath.Join(prof, "plugins", "cache", source, name, version)
		if _, err := os.Stat(verDir); err == nil {
			// Version still installed — pin is fresh, leave alone.
			continue
		}
		newest := newestPluginVersionDir(filepath.Join(prof, "plugins", "cache", source, name))
		if newest == "" || newest == version {
			continue
		}
		newPath := s[:idx+len(marker)] + source + string(filepath.Separator) + name + string(filepath.Separator) + newest
		if len(parts) == 4 {
			newPath += string(filepath.Separator) + parts[3]
		}
		return newPath, true
	}
	return s, false
}

// newestPluginVersionDir returns the name of the subdirectory under base with
// the most recent ModTime, or "" if base does not exist or has no subdirs.
// Picks by mtime (not lex/semver) because `claude plugin install` always
// touches the freshly installed version last, regardless of the version
// scheme the plugin author uses.
func newestPluginVersionDir(base string) string {
	entries, err := os.ReadDir(base)
	if err != nil {
		return ""
	}
	var bestName string
	var bestMtime time.Time
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if bestName == "" || info.ModTime().After(bestMtime) {
			bestName = e.Name()
			bestMtime = info.ModTime()
		}
	}
	return bestName
}
