package session

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

var migrationLog = logging.ForComponent(logging.CompSession)

// MigrationResult contains information about the migration outcome
type MigrationResult struct {
	Migrated     bool   // True if migration was performed
	ProfilePath  string // Path to the migrated profile
	SessionCount int    // Number of sessions migrated
	Message      string // Human-readable message
}

// MigrateToProfiles migrates from the old single-file layout to the profiles layout.
// This is safe to call multiple times - it will only migrate once.
//
// Old layout:
//
//	~/.agent-deck/sessions.json
//	~/.agent-deck/sessions.json.bak
//	~/.agent-deck/sessions.json.bak.1
//	~/.agent-deck/sessions.json.bak.2
//
// New layout:
//
//	~/.agent-deck/config.json
//	~/.agent-deck/profiles/default/sessions.json
//	~/.agent-deck/profiles/default/sessions.json.bak
//	~/.agent-deck/profiles/default/sessions.json.bak.1
//	~/.agent-deck/profiles/default/sessions.json.bak.2
//	~/.agent-deck/logs/ (unchanged)
func MigrateToProfiles() (*MigrationResult, error) {
	agentDeckDir, err := profileDataRootDir()
	if err != nil {
		return nil, err
	}

	oldSessionsPath := filepath.Join(agentDeckDir, "sessions.json")
	profilesDir := filepath.Join(agentDeckDir, ProfilesDirName)
	defaultProfileDir := filepath.Join(profilesDir, DefaultProfile)
	newSessionsPath := filepath.Join(defaultProfileDir, "sessions.json")

	// Check if migration is needed
	oldExists := fileExists(oldSessionsPath)
	newExists := fileExists(newSessionsPath)

	// Case 1: New layout already exists - no migration needed
	if newExists {
		return &MigrationResult{
			Migrated:    false,
			ProfilePath: defaultProfileDir,
			Message:     "Already using profiles layout",
		}, nil
	}

	// Case 2: No old file exists - fresh install, no migration needed
	if !oldExists {
		return &MigrationResult{
			Migrated:    false,
			ProfilePath: defaultProfileDir,
			Message:     "Fresh install, no migration needed",
		}, nil
	}

	// Case 3: Old file exists, new doesn't - perform migration
	migrationLog.Info("migrating_to_profiles_layout")

	// Step 1: Read and validate old sessions file
	oldData, err := os.ReadFile(oldSessionsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read old sessions file: %w", err)
	}

	// Validate JSON structure
	var storageData StorageData
	if err := json.Unmarshal(oldData, &storageData); err != nil {
		return nil, fmt.Errorf("old sessions file is corrupted: %w", err)
	}

	sessionCount := len(storageData.Instances)
	migrationLog.Info("sessions_found_for_migration", slog.Int("count", sessionCount))

	// Step 2: Create profiles/default directory
	if err := os.MkdirAll(defaultProfileDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create default profile directory: %w", err)
	}

	// Step 3: Copy files (not move - safer, we delete after verification)
	filesToMigrate := []string{
		"sessions.json",
		"sessions.json.bak",
		"sessions.json.bak.1",
		"sessions.json.bak.2",
	}

	for _, filename := range filesToMigrate {
		oldPath := filepath.Join(agentDeckDir, filename)
		newPath := filepath.Join(defaultProfileDir, filename)

		if fileExists(oldPath) {
			if err := copyFileSafe(oldPath, newPath); err != nil {
				// Rollback: remove the new directory
				os.RemoveAll(defaultProfileDir)
				return nil, fmt.Errorf("failed to copy %s: %w", filename, err)
			}
			migrationLog.Info("file_copied_to_profile", slog.String("filename", filename))
		}
	}

	// Step 4: Verify the new file is valid
	newData, err := os.ReadFile(newSessionsPath)
	if err != nil {
		os.RemoveAll(defaultProfileDir)
		return nil, fmt.Errorf("failed to verify migrated file: %w", err)
	}

	var verifyData StorageData
	if err := json.Unmarshal(newData, &verifyData); err != nil {
		os.RemoveAll(defaultProfileDir)
		return nil, fmt.Errorf("migrated file is corrupted: %w", err)
	}

	if len(verifyData.Instances) != sessionCount {
		os.RemoveAll(defaultProfileDir)
		return nil, fmt.Errorf("session count mismatch after migration: expected %d, got %d",
			sessionCount, len(verifyData.Instances))
	}

	// Step 5: Create config.json
	config := &Config{
		DefaultProfile: DefaultProfile,
		Version:        1,
	}
	if err := SaveConfig(config); err != nil {
		// Non-fatal - config can be recreated
		migrationLog.Warn("config_json_creation_failed", slog.String("error", err.Error()))
	}

	// Step 6: Remove old files (only after successful verification)
	for _, filename := range filesToMigrate {
		oldPath := filepath.Join(agentDeckDir, filename)
		if fileExists(oldPath) {
			if err := os.Remove(oldPath); err != nil {
				// Non-fatal - just log it
				migrationLog.Warn("old_file_removal_failed", slog.String("filename", filename), slog.String("error", err.Error()))
			}
		}
	}

	migrationLog.Info("migration_complete", slog.Int("session_count", sessionCount))

	return &MigrationResult{
		Migrated:     true,
		ProfilePath:  defaultProfileDir,
		SessionCount: sessionCount,
		Message:      fmt.Sprintf("Migrated %d sessions to profiles/default/", sessionCount),
	}, nil
}

// NeedsMigration checks if migration from old layout is needed
func NeedsMigration() (bool, error) {
	agentDeckDir, err := profileDataRootDir()
	if err != nil {
		return false, err
	}

	oldSessionsPath := filepath.Join(agentDeckDir, "sessions.json")
	profilesDir := filepath.Join(agentDeckDir, ProfilesDirName)

	// Migration needed if old file exists AND profiles directory doesn't
	oldExists := fileExists(oldSessionsPath)
	profilesDirExists := dirExists(profilesDir)

	return oldExists && !profilesDirExists, nil
}

// fileExists checks if a file exists
func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// dirExists checks if a directory exists
func dirExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}

// copyFileSafe copies a file with verification
func copyFileSafe(src, dst string) error {
	// Read source
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("failed to read source: %w", err)
	}

	// Write to destination
	if err := os.WriteFile(dst, data, 0600); err != nil {
		return fmt.Errorf("failed to write destination: %w", err)
	}

	// Verify by reading back
	verifyData, err := os.ReadFile(dst)
	if err != nil {
		os.Remove(dst)
		return fmt.Errorf("failed to verify destination: %w", err)
	}

	if len(data) != len(verifyData) {
		os.Remove(dst)
		return fmt.Errorf("size mismatch: source %d, destination %d", len(data), len(verifyData))
	}

	return nil
}
