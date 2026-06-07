package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
)

type uninstallFoundItem struct {
	itemType    string
	path        string
	description string
}

// uninstallDataCandidate is a data-location path that a real uninstall would
// consider for removal (XDG config/data/cache + legacy). Candidates are
// resolved from the SAME source whether or not they exist on disk, so the
// not-installed "Checked locations" preview and the actual removal stay in
// sync (Codex round-2 P2).
type uninstallDataCandidate struct {
	itemType string
	label    string
	path     string
}

// uninstallDataCandidates resolves every data-location path an uninstall would
// remove, independent of whether the path currently exists. The not-installed
// dry-run preview uses this so it accurately lists XDG config/data/cache for
// XDG-only installs (not just legacy + ~/.tmux.conf).
func uninstallDataCandidates() []uninstallDataCandidate {
	var candidates []uninstallDataCandidate
	if path, err := agentpaths.ConfigDir(); err == nil {
		candidates = append(candidates, uninstallDataCandidate{itemType: "config", label: "Config directory", path: path})
	}
	if path, err := agentpaths.DataDir(); err == nil {
		candidates = append(candidates, uninstallDataCandidate{itemType: "data", label: "Data directory", path: path})
	}
	if path, err := agentpaths.CacheDir(); err == nil {
		candidates = append(candidates, uninstallDataCandidate{itemType: "cache", label: "Cache directory", path: path})
	}
	if path, err := agentpaths.LegacyDir(); err == nil {
		candidates = append(candidates, uninstallDataCandidate{itemType: "legacy", label: "Legacy directory", path: path})
	}
	return candidates
}

func collectUninstallDataLocations() []uninstallFoundItem {
	candidates := uninstallDataCandidates()

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

// backupUninstallDataLocations archives EVERY data location that will be
// deleted (XDG config + data + cache + legacy) into a single tarball before
// any removal happens. It returns the backup file path on success.
//
// Data-safety contract (Blocker 1, 2026-06-04 incident family): the old code
// tar'd only legacy ~/.agent-deck, then deleted XDG locations too — so an
// XDG-only install lost data with no backup. This function backs up ALL the
// real, existing data-location paths so nothing is removed un-backed-up.
//
// Symlinks and non-existent paths are skipped (nothing irreplaceable to
// archive). If there is no real data to back up, it returns ("", nil) so the
// caller can proceed (deleting empty/symlink locations is safe).
func backupUninstallDataLocations(items []uninstallFoundItem, homeDir string) (string, error) {
	// Collect the real (non-symlink, existing) data directories to archive.
	var paths []string
	for _, item := range items {
		if !isUninstallDataLocation(item.itemType) {
			continue
		}
		info, err := os.Lstat(item.path)
		if err != nil {
			continue // already gone — nothing to back up
		}
		if info.Mode()&os.ModeSymlink != 0 {
			continue // symlink: target lives elsewhere, don't archive blindly
		}
		paths = append(paths, filepath.Clean(item.path))
	}
	if len(paths) == 0 {
		return "", nil
	}

	backupFile := filepath.Join(
		homeDir,
		fmt.Sprintf("agent-deck-backup-%s.tar.gz", time.Now().Format("20060102-150405")),
	)

	// Archive each path by its full path. -C / keeps absolute-ish layout
	// (tar strips the leading slash) so config/data/cache/legacy all coexist
	// in one archive without collisions, even across different roots.
	args := []string{"-czf", backupFile, "-C", "/"}
	for _, p := range paths {
		rel := strings.TrimPrefix(p, "/")
		args = append(args, rel)
	}

	cmd := exec.Command("tar", args...)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		// Don't leave a partial/corrupt archive masquerading as a good backup.
		_ = os.Remove(backupFile)
		return "", fmt.Errorf("failed to create backup of data locations: %w", err)
	}
	return backupFile, nil
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
