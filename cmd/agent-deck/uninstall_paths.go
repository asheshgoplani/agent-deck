package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
)

type uninstallFoundItem struct {
	itemType    string
	path        string
	description string
}

func collectUninstallDataLocations() []uninstallFoundItem {
	type candidate struct {
		itemType string
		label    string
		path     string
	}

	var candidates []candidate
	if path, err := agentpaths.ConfigDir(); err == nil {
		candidates = append(candidates, candidate{itemType: "config", label: "Config directory", path: path})
	}
	if path, err := agentpaths.DataDir(); err == nil {
		candidates = append(candidates, candidate{itemType: "data", label: "Data directory", path: path})
	}
	if path, err := agentpaths.CacheDir(); err == nil {
		candidates = append(candidates, candidate{itemType: "cache", label: "Cache directory", path: path})
	}
	if path, err := agentpaths.LegacyDir(); err == nil {
		candidates = append(candidates, candidate{itemType: "legacy", label: "Legacy directory", path: path})
	}

	seen := make(map[string]struct{}, len(candidates))
	items := make([]uninstallFoundItem, 0, len(candidates))
	for _, c := range candidates {
		cleanPath := filepath.Clean(c.path)
		if _, ok := seen[cleanPath]; ok {
			continue
		}
		seen[cleanPath] = struct{}{}

		info, err := os.Lstat(cleanPath)
		if err != nil {
			continue
		}
		desc := describeUninstallLocation(cleanPath, info)
		items = append(items, uninstallFoundItem{
			itemType:    c.itemType,
			path:        cleanPath,
			description: desc,
		})
		fmt.Printf("Found: %s at %s\n", c.label, cleanPath)
		if desc != "" {
			fmt.Printf("       %s\n", desc)
		}
	}
	return items
}

func describeUninstallLocation(path string, info os.FileInfo) string {
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			return "symlink"
		}
		return fmt.Sprintf("symlink -> %s", target)
	}

	sessionCount := 0
	profileCount := 0
	profilesDir := filepath.Join(path, "profiles")
	if entries, err := os.ReadDir(profilesDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			dbFile := filepath.Join(profilesDir, entry.Name(), "state.db")
			jsonFile := filepath.Join(profilesDir, entry.Name(), "sessions.json")
			if _, err := os.Stat(dbFile); err == nil {
				profileCount++
			} else if data, err := os.ReadFile(jsonFile); err == nil {
				profileCount++
				sessionCount += strings.Count(string(data), `"id"`)
			}
		}
	}

	var totalSize int64
	_ = filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			totalSize += info.Size()
		}
		return nil
	})

	if profileCount > 0 || sessionCount > 0 {
		return fmt.Sprintf("%d profiles, %d sessions, %s", profileCount, sessionCount, formatSize(totalSize))
	}
	return formatSize(totalSize)
}

func isUninstallDataLocation(itemType string) bool {
	return itemType == "config" || itemType == "data" || itemType == "cache" || itemType == "legacy"
}

func removeUninstallLocation(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return os.Remove(path)
	}
	return os.RemoveAll(path)
}
