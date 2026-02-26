package session

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/docker"
	"github.com/asheshgoplani/agent-deck/internal/logging"
)

var maintLog = logging.ForComponent(logging.CompSession)

// MaintenanceResult holds the outcome of a maintenance run.
type MaintenanceResult struct {
	PrunedLogs       int
	PrunedBackups    int
	ArchivedSessions int
	OrphanContainers int
	Duration         time.Duration
}

// RunMaintenance executes all maintenance tasks and returns the result.
func RunMaintenance(ctx context.Context) MaintenanceResult {
	start := time.Now()

	deckDir, err := GetAgentDeckDir()
	if err != nil {
		maintLog.Warn("maintenance_dir_lookup_failed", slog.String("error", err.Error()))
		return MaintenanceResult{Duration: time.Since(start)}
	}
	geminiDir := GetGeminiConfigDir()

	prunedLogs := pruneGeminiLogs(geminiDir)
	prunedBackups := cleanupDeckBackups(filepath.Join(deckDir, "profiles"))
	archivedSessions := archiveBloatedSessions(deckDir)
	orphanContainers := cleanupOrphanContainers(ctx)

	return MaintenanceResult{
		PrunedLogs:       prunedLogs,
		PrunedBackups:    prunedBackups,
		ArchivedSessions: archivedSessions,
		OrphanContainers: orphanContainers,
		Duration:         time.Since(start),
	}
}

// StartMaintenanceWorker launches a background goroutine that runs maintenance
// on a 15-minute ticker with an immediate first run. It checks
// GetMaintenanceSettings().Enabled before each run.
func StartMaintenanceWorker(ctx context.Context, onComplete func(MaintenanceResult)) {
	go func() {
		// Immediate first run.
		if GetMaintenanceSettings().Enabled {
			result := RunMaintenance(ctx)
			if onComplete != nil {
				onComplete(result)
			}
		}

		ticker := time.NewTicker(15 * time.Minute)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if GetMaintenanceSettings().Enabled {
					result := RunMaintenance(ctx)
					if onComplete != nil {
						onComplete(result)
					}
				}
			}
		}
	}()
}

// pruneGeminiLogs deletes .txt files found directly inside ~/.gemini/tmp/*/
// directories, but NOT inside chats/ subdirectories.
func pruneGeminiLogs(baseDir string) int {
	pruned := 0

	dirs, err := filepath.Glob(filepath.Join(baseDir, "tmp", "*"))
	if err != nil {
		maintLog.Warn("prune_gemini_logs_glob_error", slog.String("error", err.Error()))
		return 0
	}

	for _, dir := range dirs {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}

		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}

		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			if !strings.HasSuffix(entry.Name(), ".txt") {
				continue
			}
			fullPath := filepath.Join(dir, entry.Name())
			if err := os.Remove(fullPath); err != nil {
				maintLog.Warn(
					"maintenance_file_remove_failed",
					slog.String("path", fullPath),
					slog.String("error", err.Error()),
				)
			} else {
				pruned++
			}
		}
	}

	return pruned
}

// cleanupDeckBackups keeps only the 3 most recent .bak.* files per profile
// directory, deleting the rest.
func cleanupDeckBackups(profilesDir string) int {
	pruned := 0

	matches, err := filepath.Glob(filepath.Join(profilesDir, "*", "sessions.json.bak.*"))
	if err != nil {
		maintLog.Warn("cleanup_backups_glob_error", slog.String("error", err.Error()))
		return 0
	}

	// Group files by parent directory.
	groups := make(map[string][]string)
	for _, m := range matches {
		dir := filepath.Dir(m)
		groups[dir] = append(groups[dir], m)
	}

	for _, files := range groups {
		if len(files) <= 3 {
			continue
		}

		// Sort by modification time, most recent first.
		type fileWithMtime struct {
			path  string
			mtime time.Time
		}
		var sorted []fileWithMtime
		for _, f := range files {
			info, err := os.Stat(f)
			if err != nil {
				continue
			}
			sorted = append(sorted, fileWithMtime{path: f, mtime: info.ModTime()})
		}
		sort.Slice(sorted, func(i, j int) bool {
			return sorted[i].mtime.After(sorted[j].mtime)
		})

		// Delete everything after the first 3.
		for i := 3; i < len(sorted); i++ {
			if err := os.Remove(sorted[i].path); err != nil {
				maintLog.Warn(
					"maintenance_backup_remove_failed",
					slog.String("path", sorted[i].path),
					slog.String("error", err.Error()),
				)
			} else {
				pruned++
			}
		}
	}

	return pruned
}

// archiveBloatedSessions finds .json files larger than 30MB and older than 24h
// in baseDir/profiles/*/ and moves them to an archive/ subdirectory. It skips
// directories that have fewer than 5 .json files.
func archiveBloatedSessions(baseDir string) int {
	archived := 0
	threshold := int64(30 * 1024 * 1024) // 30MB
	maxAge := 24 * time.Hour

	matches, err := filepath.Glob(filepath.Join(baseDir, "profiles", "*", "*.json"))
	if err != nil {
		maintLog.Warn("archive_bloated_sessions_glob_error", slog.String("error", err.Error()))
		return 0
	}

	// Group by parent directory.
	groups := make(map[string][]string)
	for _, m := range matches {
		dir := filepath.Dir(m)
		groups[dir] = append(groups[dir], m)
	}

	now := time.Now()

	for dir, files := range groups {
		// Skip directories with fewer than 5 json files.
		if len(files) < 5 {
			continue
		}

		for _, f := range files {
			info, err := os.Stat(f)
			if err != nil {
				continue
			}

			if info.Size() <= threshold {
				continue
			}
			if now.Sub(info.ModTime()) < maxAge {
				continue
			}

			archiveDir := filepath.Join(dir, "archive")
			if err := os.MkdirAll(archiveDir, 0o755); err != nil {
				maintLog.Warn(
					"archive_dir_creation_failed",
					slog.String("path", archiveDir),
					slog.String("error", err.Error()),
				)
				continue
			}

			dest := filepath.Join(archiveDir, filepath.Base(f))
			if err := os.Rename(f, dest); err != nil {
				maintLog.Warn("archive_file_failed", slog.String("path", f), slog.String("error", err.Error()))
			} else {
				archived++
			}
		}
	}

	return archived
}

// RestoreFromArchive moves all files from archive/ subdirectories back to
// their parent directories under baseDir/profiles/*/.
func RestoreFromArchive(baseDir string) error {
	archiveDirs, err := filepath.Glob(filepath.Join(baseDir, "profiles", "*", "archive"))
	if err != nil {
		return fmt.Errorf("glob error: %w", err)
	}

	for _, archiveDir := range archiveDirs {
		info, err := os.Stat(archiveDir)
		if err != nil || !info.IsDir() {
			continue
		}

		parentDir := filepath.Dir(archiveDir)

		entries, err := os.ReadDir(archiveDir)
		if err != nil {
			return fmt.Errorf("reading archive dir %s: %w", archiveDir, err)
		}

		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			src := filepath.Join(archiveDir, entry.Name())
			dest := filepath.Join(parentDir, entry.Name())
			if err := os.Rename(src, dest); err != nil {
				return fmt.Errorf("restoring %s: %w", src, err)
			}
		}

		// Remove the now-empty archive directory.
		_ = os.Remove(archiveDir)
	}

	return nil
}

// cleanupOrphanContainers removes stopped agent-deck containers.
// Running containers are left alone — they may belong to active sessions.
//
// Safety: stopped containers are safe to remove because ensureContainerRunning
// performs create+start atomically (a container is never left in a created-but-
// not-started state intentionally), container names are derived from session IDs
// (not persisted independently), and running containers are always skipped.
func cleanupOrphanContainers(parent context.Context) int {
	if !docker.IsDockerAvailable() {
		return 0
	}

	dockerCfg := GetDockerSettings()
	if !dockerCfg.GetAutoCleanup() {
		return 0
	}

	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()

	names, err := docker.ListManagedContainers(ctx)
	if err != nil {
		maintLog.Warn("orphan_container_list_failed", slog.String("error", err.Error()))
		return 0
	}

	removed := 0
	for _, name := range names {
		// Per-container timeout prevents one slow Docker call from consuming
		// the entire budget and skipping remaining containers.
		perCtx, perCancel := context.WithTimeout(ctx, 5*time.Second)
		ctr := docker.FromName(name)
		running, err := ctr.IsRunning(perCtx)
		if err != nil {
			perCancel()
			maintLog.Warn("orphan_container_check_failed",
				slog.String("container", name), slog.String("error", err.Error()))
			continue
		}
		if running {
			perCancel()
			continue // Active session — leave it alone.
		}
		if err := ctr.Remove(perCtx, false); err != nil {
			perCancel()
			maintLog.Warn("orphan_container_remove_failed",
				slog.String("container", name), slog.String("error", err.Error()))
			continue
		}
		perCancel()
		maintLog.Info("orphan_container_removed", slog.String("container", name))
		removed++
	}

	return removed
}
