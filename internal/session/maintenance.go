package session

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// MaintenanceResult contains the statistics of a maintenance run.
type MaintenanceResult struct {
	PrunedLogs       int
	PrunedBackups    int
	ArchivedSessions int
	Duration         time.Duration
}

// Maintenance performs a full maintenance run across all tasks.
// Returns a MaintenanceResult and any errors encountered.
func Maintenance() (MaintenanceResult, error) {
	start := time.Now()
	// First, restore anything mistakenly archived
	_, _ = RestoreFromArchive("")

	prunedLogs, _ := pruneGeminiLogs("")
	prunedBackups, _ := cleanupDeckBackups("")
	archivedSessions, _ := archiveBloatedSessions("", 0)
	_, _ = cleanupProjectTempFiles()

	return MaintenanceResult{
		PrunedLogs:       prunedLogs,
		PrunedBackups:    prunedBackups,
		ArchivedSessions: archivedSessions,
		Duration:         time.Since(start),
	}, nil
}

// StartMaintenanceWorker starts a background goroutine that performs periodic cleanup.
// Runs immediately on startup, then every 15 minutes, provided maintenance is enabled in config.
func StartMaintenanceWorker(ctx context.Context) {
	log.Printf("[MAINTENANCE] Starting background maintenance worker")

	// Helper to run all maintenance tasks
	runAllTasks := func() {
		// Check if maintenance is enabled in config
		settings := GetMaintenanceSettings()
		if !settings.Enabled {
			return
		}

		log.Printf("[MAINTENANCE] Running scheduled maintenance...")
		result, _ := Maintenance()

		log.Printf("[MAINTENANCE] Maintenance complete in %v. Pruned: %d logs, %d backups. Archived: %d sessions.",
			result.Duration.Round(time.Millisecond), result.PrunedLogs, result.PrunedBackups, result.ArchivedSessions)
	}

	// Run immediately on startup
	go runAllTasks()

	// Schedule recurring runs
	go func() {
		ticker := time.NewTicker(15 * time.Minute)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				log.Printf("[MAINTENANCE] Background maintenance worker stopping")
				return
			case <-ticker.C:
				runAllTasks()
			}
		}
	}()
}

// pruneGeminiLogs deletes all .txt files in Gemini project directories
// that are NOT within the chats/ subdirectory.
// These files are transient logs and tool outputs that accumulate over time.
func pruneGeminiLogs(baseDir string) (int, error) {
	if baseDir == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return 0, err
		}
		baseDir = filepath.Join(homeDir, ".gemini", "tmp")
	}

	// Verify directory exists
	if _, err := os.Stat(baseDir); os.IsNotExist(err) {
		return 0, nil
	}

	prunedCount := 0

	// Walk project directories (one level deep)
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return 0, err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		projectDir := filepath.Join(baseDir, entry.Name())
		
		// Skip special directories like 'bin'
		if entry.Name() == "bin" {
			continue
		}

		// List all files in the project directory
		files, err := os.ReadDir(projectDir)
		if err != nil {
			log.Printf("[MAINTENANCE] Failed to read project dir %s: %v", projectDir, err)
			continue
		}

		for _, f := range files {
			// Only prune .txt files in the root of the project directory
			if !f.IsDir() && strings.HasSuffix(f.Name(), ".txt") {
				path := filepath.Join(projectDir, f.Name())
				if err := os.Remove(path); err == nil {
					prunedCount++
				} else {
					log.Printf("[MAINTENANCE] Failed to remove log file %s: %v", path, err)
				}
			}
		}
	}

	if prunedCount > 0 {
		log.Printf("[MAINTENANCE] Pruned %d Gemini log files from %s", prunedCount, baseDir)
	}

	return prunedCount, nil
}

// cleanupDeckBackups removes old backup files, keeping only the 3 most recent.
func cleanupDeckBackups(profilesDir string) (int, error) {
	if profilesDir == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return 0, err
		}
		profilesDir = filepath.Join(homeDir, ".agent-deck", "profiles")
	}

	if _, err := os.Stat(profilesDir); os.IsNotExist(err) {
		return 0, nil
	}

	prunedTotal := 0

	// Walk profiles
	profiles, err := os.ReadDir(profilesDir)
	if err != nil {
		return 0, err
	}

	for _, p := range profiles {
		if !p.IsDir() {
			continue
		}

		profileDir := filepath.Join(profilesDir, p.Name())
		files, err := os.ReadDir(profileDir)
		if err != nil {
			continue
		}

		// Find all backup files
		var backups []os.FileInfo
		for _, f := range files {
			if !f.IsDir() && strings.HasPrefix(f.Name(), "sessions.json.bak.") {
				info, err := f.Info()
				if err == nil {
					backups = append(backups, info)
				}
			}
		}

		// If more than 3 backups, prune the oldest ones
		if len(backups) > 3 {
			// Sort by modification time (newest first)
			sort.Slice(backups, func(i, j int) bool {
				return backups[i].ModTime().After(backups[j].ModTime())
			})

			// Delete backups after the first 3
			for _, b := range backups[3:] {
				path := filepath.Join(profileDir, b.Name())
				if err := os.Remove(path); err == nil {
					prunedTotal++
				}
			}
		}
	}

	if prunedTotal > 0 {
		log.Printf("[MAINTENANCE] Pruned %d old sessions.json backups", prunedTotal)
	}

	return prunedTotal, nil
}

// cleanupProjectTempFiles cleans up stale files in the Agent Deck project's temporary directory.
func cleanupProjectTempFiles() (int, error) {
	// The project's temp dir is ~/.gemini/tmp/<project_hash>
	// We already prune .txt files there in pruneGeminiLogs.
	// This function serves as a placeholder for other temp file cleanup if needed.
	return 0, nil
}

// archiveBloatedSessions identifies Gemini JSON session files exceeding the threshold
// and moves them to an archive/ subdirectory.
// threshold is in bytes. If 0, uses the default 30MB.
func archiveBloatedSessions(baseDir string, threshold int64) (int, error) {
	if baseDir == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return 0, err
		}
		baseDir = filepath.Join(homeDir, ".gemini", "tmp")
	}

	if threshold <= 0 {
		threshold = 30 * 1024 * 1024 // 30MB default
	}

	if _, err := os.Stat(baseDir); os.IsNotExist(err) {
		return 0, nil
	}

	archivedCount := 0

	// Walk project directories
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return 0, err
	}

	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "bin" {
			continue
		}

		projectDir := filepath.Join(baseDir, entry.Name())
		chatsDir := filepath.Join(projectDir, "chats")
		if _, err := os.Stat(chatsDir); os.IsNotExist(err) {
			continue
		}

		files, err := os.ReadDir(chatsDir)
		if err != nil {
			continue
		}

		// SAFETY: Skip directories with very few files (likely active/new)
		if len(files) < 5 {
			continue
		}

		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
				continue
			}

			info, err := f.Info()
			if err != nil {
				continue
			}

			// SAFETY: NEVER archive sessions modified in the last 24 hours
			if time.Since(info.ModTime()) < 24*time.Hour {
				continue
			}

			if info.Size() > threshold {
				// Move to archive
				archiveDir := filepath.Join(chatsDir, "archive")
				if err := os.MkdirAll(archiveDir, 0755); err != nil {
					log.Printf("[MAINTENANCE] Failed to create archive dir %s: %v", archiveDir, err)
					continue
				}

				oldPath := filepath.Join(chatsDir, f.Name())
				newPath := filepath.Join(archiveDir, f.Name())

				if err := os.Rename(oldPath, newPath); err == nil {
					archivedCount++
					log.Printf("[MAINTENANCE] Archived bloated session: %s (%.1f MB)", f.Name(), float64(info.Size())/(1024*1024))
				} else {
					log.Printf("[MAINTENANCE] Failed to archive session %s: %v", f.Name(), err)
				}
			}
		}
	}

	return archivedCount, nil
}

// RestoreFromArchive moves all files from archive/ subdirectories back to their parent chats/ folder.
// This is used to undo accidental archiving.
func RestoreFromArchive(baseDir string) (int, error) {
	if baseDir == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return 0, err
		}
		baseDir = filepath.Join(homeDir, ".gemini", "tmp")
	}

	restoredCount := 0
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return 0, err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		archiveDir := filepath.Join(baseDir, entry.Name(), "chats", "archive")
		chatsDir := filepath.Join(baseDir, entry.Name(), "chats")

		if _, err := os.Stat(archiveDir); os.IsNotExist(err) {
			continue
		}

		files, err := os.ReadDir(archiveDir)
		if err != nil {
			continue
		}

		for _, f := range files {
			if f.IsDir() {
				continue
			}
			oldPath := filepath.Join(archiveDir, f.Name())
			newPath := filepath.Join(chatsDir, f.Name())
			if err := os.Rename(oldPath, newPath); err == nil {
				restoredCount++
			}
		}
	}

	if restoredCount > 0 {
		log.Printf("[MAINTENANCE] Restored %d sessions from archive", restoredCount)
	}
	return restoredCount, nil
}
