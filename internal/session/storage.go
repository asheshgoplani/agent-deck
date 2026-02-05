package session

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

var storageLog = logging.ForComponent(logging.CompStorage)

const (
	// maxBackupGenerations is the number of rolling backups to keep
	maxBackupGenerations = 3
)

// expandTilde expands ~ to the user's home directory with path traversal protection
// It also fixes malformed paths that have ~ in the middle (e.g., "/some/path~/actual/path")
func expandTilde(path string) string {
	// Fix malformed paths that have ~ in the middle
	// This can happen when textinput suggestion appends instead of replaces
	if idx := strings.Index(path, "~/"); idx > 0 {
		path = path[idx:]
	}

	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			// Clean the path to resolve .. and other special sequences
			expanded := filepath.Join(home, path[2:])
			cleaned := filepath.Clean(expanded)
			// Verify the cleaned path is still under home directory (prevent path traversal)
			if strings.HasPrefix(cleaned, home) {
				return cleaned
			}
			// Path traversal detected - log and return original
			storageLog.Warn("path_traversal_detected", slog.String("path", path))
		}
	} else if path == "~" {
		home, err := os.UserHomeDir()
		if err == nil {
			return home
		}
	}
	return path
}

// StorageData represents the JSON structure for persistence
type StorageData struct {
	Instances []*InstanceData `json:"instances"`
	Groups    []*GroupData    `json:"groups,omitempty"` // Persist empty groups
	UpdatedAt time.Time       `json:"updated_at"`
}

// InstanceData represents the serializable session data
type InstanceData struct {
	ID              string    `json:"id"`
	Title           string    `json:"title"`
	ProjectPath     string    `json:"project_path"`
	GroupPath       string    `json:"group_path"`
	Order           int       `json:"order"`
	ParentSessionID string    `json:"parent_session_id,omitempty"` // Links to parent session (sub-session support)
	Command         string    `json:"command"`
	Wrapper         string    `json:"wrapper,omitempty"`
	Tool            string    `json:"tool"`
	Status          Status    `json:"status"`
	CreatedAt       time.Time `json:"created_at"`
	LastAccessedAt  time.Time `json:"last_accessed_at,omitempty"`
	TmuxSession     string    `json:"tmux_session"`

	// Worktree support
	WorktreePath     string `json:"worktree_path,omitempty"`
	WorktreeRepoRoot string `json:"worktree_repo_root,omitempty"`
	WorktreeBranch   string `json:"worktree_branch,omitempty"`

	// Claude session (persisted for resume after app restart)
	ClaudeSessionID  string    `json:"claude_session_id,omitempty"`
	ClaudeDetectedAt time.Time `json:"claude_detected_at,omitempty"`

	// Gemini session (persisted for resume after app restart)
	GeminiSessionID  string    `json:"gemini_session_id,omitempty"`
	GeminiDetectedAt time.Time `json:"gemini_detected_at,omitempty"`
	GeminiYoloMode   *bool     `json:"gemini_yolo_mode,omitempty"`
	GeminiModel      string    `json:"gemini_model,omitempty"`

	// OpenCode session (persisted for resume after app restart)
	OpenCodeSessionID  string    `json:"opencode_session_id,omitempty"`
	OpenCodeDetectedAt time.Time `json:"opencode_detected_at,omitempty"`

	// Codex session (persisted for resume after app restart)
	CodexSessionID  string    `json:"codex_session_id,omitempty"`
	CodexDetectedAt time.Time `json:"codex_detected_at,omitempty"`

	// Latest user input for context
	LatestPrompt string `json:"latest_prompt,omitempty"`

	// Tool-specific launch options (generic for all tools: claude, codex, etc.)
	ToolOptionsJSON json.RawMessage `json:"tool_options,omitempty"`

	// MCP tracking (persisted for sync status display)
	LoadedMCPNames []string `json:"loaded_mcp_names,omitempty"`
}

// GroupData represents serializable group data
type GroupData struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Expanded    bool   `json:"expanded"`
	Order       int    `json:"order"`
	DefaultPath string `json:"default_path,omitempty"`
}

// Storage handles persistence of session data
// Thread-safe with mutex protection for concurrent access
type Storage struct {
	path    string
	profile string     // The profile this storage is for
	mu      sync.Mutex // Protects all file operations
}

// NewStorageWithProfile creates a storage instance for a specific profile.
// If profile is empty, uses the effective profile (from env var or config).
// Automatically runs migration from old layout if needed.
func NewStorageWithProfile(profile string) (*Storage, error) {
	// Run migration if needed (safe to call multiple times)
	needsMigration, err := NeedsMigration()
	if err != nil {
		storageLog.Warn("migration_check_failed", slog.String("error", err.Error()))
	} else if needsMigration {
		result, err := MigrateToProfiles()
		if err != nil {
			return nil, fmt.Errorf("migration failed: %w", err)
		}
		if result.Migrated {
			storageLog.Info("migration_complete", slog.String("message", result.Message))
		}
	}

	// Get effective profile
	effectiveProfile := GetEffectiveProfile(profile)

	// Get storage path for this profile
	path, err := GetStoragePathForProfile(effectiveProfile)
	if err != nil {
		return nil, err
	}

	// Ensure directory exists with secure permissions (0700 = owner only)
	// This protects session data on shared systems
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create storage directory: %w", err)
	}

	s := &Storage{
		path:    path,
		profile: effectiveProfile,
	}

	// Clean up any leftover temp files from previous crashes
	s.cleanupTempFiles()

	return s, nil
}

// Profile returns the profile name this storage is using
func (s *Storage) Profile() string {
	return s.profile
}

// Path returns the file path this storage is using
func (s *Storage) Path() string {
	return s.path
}

// cleanupTempFiles removes any leftover .tmp files from previous crashes
func (s *Storage) cleanupTempFiles() {
	tmpPath := s.path + ".tmp"
	if _, err := os.Stat(tmpPath); err == nil {
		if err := os.Remove(tmpPath); err != nil {
			storageLog.Warn("temp_file_cleanup_failed", slog.String("path", tmpPath), slog.String("error", err.Error()))
		} else {
			storageLog.Info("temp_file_cleaned_up")
		}
	}
}

// Save persists instances to JSON file
// DEPRECATED: Use SaveWithGroups to ensure groups are not lost
func (s *Storage) Save(instances []*Instance) error {
	return s.SaveWithGroups(instances, nil)
}

// SaveWithGroups persists instances and groups to JSON file
// Uses atomic write pattern with:
// - Mutex for thread safety
// - Rolling backups (3 generations)
// - fsync for durability
// - Data validation
func (s *Storage) SaveWithGroups(instances []*Instance, groupTree *GroupTree) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Convert instances to serializable format
	data := StorageData{
		Instances: make([]*InstanceData, len(instances)),
		UpdatedAt: time.Now(),
	}

	for i, inst := range instances {
		tmuxName := ""
		if inst.tmuxSession != nil {
			tmuxName = inst.tmuxSession.Name
		}
		data.Instances[i] = &InstanceData{
			ID:                 inst.ID,
			Title:              inst.Title,
			ProjectPath:        inst.ProjectPath,
			GroupPath:          inst.GroupPath,
			Order:              inst.Order,
			ParentSessionID:    inst.ParentSessionID,
			Command:            inst.Command,
			Wrapper:            inst.Wrapper,
			Tool:               inst.Tool,
			Status:             inst.Status,
			CreatedAt:          inst.CreatedAt,
			LastAccessedAt:     inst.LastAccessedAt,
			TmuxSession:        tmuxName,
			WorktreePath:       inst.WorktreePath,
			WorktreeRepoRoot:   inst.WorktreeRepoRoot,
			WorktreeBranch:     inst.WorktreeBranch,
			ClaudeSessionID:    inst.ClaudeSessionID,
			ClaudeDetectedAt:   inst.ClaudeDetectedAt,
			GeminiSessionID:    inst.GeminiSessionID,
			GeminiDetectedAt:   inst.GeminiDetectedAt,
			GeminiYoloMode:     inst.GeminiYoloMode,
			GeminiModel:        inst.GeminiModel,
			OpenCodeSessionID:  inst.OpenCodeSessionID,
			OpenCodeDetectedAt: inst.OpenCodeDetectedAt,
			CodexSessionID:     inst.CodexSessionID,
			CodexDetectedAt:    inst.CodexDetectedAt,
			ToolOptionsJSON:    inst.ToolOptionsJSON,
			LatestPrompt:       inst.LatestPrompt,
			LoadedMCPNames:     inst.LoadedMCPNames,
		}
	}

	// Save groups (including empty ones)
	if groupTree != nil {
		data.Groups = make([]*GroupData, 0, len(groupTree.GroupList))
		for _, g := range groupTree.GroupList {
			data.Groups = append(data.Groups, &GroupData{
				Name:        g.Name,
				Path:        g.Path,
				Expanded:    g.Expanded,
				Order:       g.Order,
				DefaultPath: g.DefaultPath,
			})
		}
	}

	// Validate data before saving
	if err := validateStorageData(&data); err != nil {
		return fmt.Errorf("data validation failed: %w", err)
	}

	// Marshal to JSON
	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}

	// ═══════════════════════════════════════════════════════════════════
	// ATOMIC WRITE PATTERN: Prevents data corruption on crash/power loss
	// 1. Write to temporary file
	// 2. fsync the temp file (ensures data reaches disk)
	// 3. Rotate backups (rolling 3 generations)
	// 4. Atomic rename temp to final
	// ═══════════════════════════════════════════════════════════════════

	tmpPath := s.path + ".tmp"

	// Step 1: Write to temporary file (0600 = owner read/write only for security)
	if err := os.WriteFile(tmpPath, jsonData, 0600); err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}

	// Step 2: fsync the temp file to ensure data reaches disk before rename
	// This is critical for crash safety - without fsync, data could be lost
	if err := syncFile(tmpPath); err != nil {
		// Log but don't fail - atomic rename still provides some safety
		storageLog.Warn("fsync_failed", slog.String("path", tmpPath), slog.String("error", err.Error()))
	}

	// Step 3: Rotate backups before overwriting
	if _, err := os.Stat(s.path); err == nil {
		s.rotateBackups()
	}

	// Step 4: Atomic rename (this is atomic on POSIX systems)
	if err := os.Rename(tmpPath, s.path); err != nil {
		return fmt.Errorf("failed to finalize save: %w", err)
	}

	return nil
}

// validateStorageData checks data integrity before saving
func validateStorageData(data *StorageData) error {
	if data == nil {
		return fmt.Errorf("data is nil")
	}

	// Check for duplicate session IDs
	seenIDs := make(map[string]bool)
	for _, inst := range data.Instances {
		if inst.ID == "" {
			return fmt.Errorf("instance has empty ID")
		}
		if seenIDs[inst.ID] {
			return fmt.Errorf("duplicate instance ID: %s", inst.ID)
		}
		seenIDs[inst.ID] = true
	}

	// Check for duplicate group paths
	seenPaths := make(map[string]bool)
	for _, g := range data.Groups {
		if g.Path == "" {
			return fmt.Errorf("group has empty path")
		}
		if seenPaths[g.Path] {
			return fmt.Errorf("duplicate group path: %s", g.Path)
		}
		seenPaths[g.Path] = true
	}

	return nil
}

// syncFile calls fsync on a file to ensure data is written to disk
func syncFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// rotateBackups maintains rolling backups: .bak, .bak.1, .bak.2
func (s *Storage) rotateBackups() {
	bakPath := s.path + ".bak"

	// Shift existing backups: .bak.2 <- .bak.1 <- .bak <- current
	for i := maxBackupGenerations - 1; i > 0; i-- {
		oldPath := fmt.Sprintf("%s.%d", bakPath, i-1)
		if i == 1 {
			oldPath = bakPath
		}
		newPath := fmt.Sprintf("%s.%d", bakPath, i)

		// Remove the oldest backup to make room
		if i == maxBackupGenerations-1 {
			os.Remove(newPath)
		}

		// Rename to shift
		if _, err := os.Stat(oldPath); err == nil {
			if err := os.Rename(oldPath, newPath); err != nil {
				storageLog.Warn("backup_rotation_failed", slog.String("old_path", oldPath), slog.String("new_path", newPath), slog.String("error", err.Error()))
			}
		}
	}

	// Copy current file to .bak
	if err := copyFile(s.path, bakPath); err != nil {
		storageLog.Warn("backup_creation_failed", slog.String("path", bakPath), slog.String("error", err.Error()))
	}
}

// copyFile copies a file from src to dst (0600 = owner read/write only for security)
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0600)
}

// Load reads instances from JSON file
func (s *Storage) Load() ([]*Instance, error) {
	instances, _, err := s.LoadWithGroups()
	return instances, err
}

// LoadLite reads session data from JSON without tmux reconnection
// This is a fast path for operations that only need to read session metadata
// (e.g., finding current session by tmux name) without initializing full Instance objects.
// Returns raw InstanceData and GroupData without any subprocess calls.
func (s *Storage) LoadLite() ([]*InstanceData, []*GroupData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if file exists
	if _, err := os.Stat(s.path); os.IsNotExist(err) {
		return []*InstanceData{}, nil, nil
	}

	// Load from file (no backup recovery in lite mode - that's for full load)
	data, err := s.loadFromFile(s.path)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load storage file: %w", err)
	}

	return data.Instances, data.Groups, nil
}

// LoadWithGroups reads instances and groups from JSON file
// Automatically recovers from backup if main file is corrupted
func (s *Storage) LoadWithGroups() ([]*Instance, []*GroupData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if file exists
	if _, err := os.Stat(s.path); os.IsNotExist(err) {
		storageLog.Debug("load_file_not_found", slog.String("profile", s.profile), slog.String("path", s.path))
		return []*Instance{}, nil, nil
	}

	// Try to load from main file first
	data, err := s.loadFromFile(s.path)
	if err != nil {
		// Main file is corrupted - try to recover from backups
		storageLog.Warn("storage_file_corrupted", slog.String("error", err.Error()))
		data, err = s.recoverFromBackups()
		if err != nil {
			return nil, nil, fmt.Errorf("failed to load and no valid backup found: %w", err)
		}
		storageLog.Info("recovered_from_backup")

		// Save the recovered data back to main file
		// (this will create a new backup of the corrupted file first)
		// Note: We don't call SaveWithGroups here to avoid deadlock (we hold the mutex)
		// Instead, we'll just write directly
	}

	return s.convertToInstances(data)
}

// loadFromFile reads and parses a storage file
func (s *Storage) loadFromFile(path string) (*StorageData, error) {
	jsonData, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	var data StorageData
	if err := json.Unmarshal(jsonData, &data); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON: %w", err)
	}

	return &data, nil
}

// recoverFromBackups tries to load data from backup files in order
func (s *Storage) recoverFromBackups() (*StorageData, error) {
	bakPath := s.path + ".bak"

	// Try backups in order: .bak, .bak.1, .bak.2
	backupPaths := []string{bakPath}
	for i := 1; i < maxBackupGenerations; i++ {
		backupPaths = append(backupPaths, fmt.Sprintf("%s.%d", bakPath, i))
	}

	for _, tryPath := range backupPaths {
		if _, err := os.Stat(tryPath); os.IsNotExist(err) {
			continue
		}

		data, err := s.loadFromFile(tryPath)
		if err != nil {
			storageLog.Warn("backup_also_corrupted", slog.String("path", tryPath), slog.String("error", err.Error()))
			continue
		}

		storageLog.Info("recovered_from_backup_file", slog.String("path", tryPath))
		return data, nil
	}

	return nil, fmt.Errorf("all backups corrupted or missing")
}

// convertToInstances converts StorageData to Instance slice
func (s *Storage) convertToInstances(data *StorageData) ([]*Instance, []*GroupData, error) {

	// ═══════════════════════════════════════════════════════════════════
	// MIGRATION: Convert old "My Sessions" paths to normalized "my-sessions"
	// Old versions used DefaultGroupName ("My Sessions") as both name AND path.
	// This caused the group to be undeletable since path matched the protection check.
	// Now we use DefaultGroupPath ("my-sessions") for paths, keeping name as display.
	// ═══════════════════════════════════════════════════════════════════
	migratedGroups := false
	for i, g := range data.Groups {
		if g.Path == DefaultGroupName {
			data.Groups[i].Path = DefaultGroupPath
			migratedGroups = true
			storageLog.Info("group_path_migrated", slog.String("old_path", DefaultGroupName), slog.String("new_path", DefaultGroupPath))
		}
	}
	for i, inst := range data.Instances {
		if inst.GroupPath == DefaultGroupName {
			data.Instances[i].GroupPath = DefaultGroupPath
			migratedGroups = true
		}
	}
	if migratedGroups {
		storageLog.Info("default_group_paths_migrated", slog.String("old_name", DefaultGroupName), slog.String("new_path", DefaultGroupPath))
	}

	// Convert to instances
	instances := make([]*Instance, len(data.Instances))
	for i, instData := range data.Instances {
		// PERFORMANCE: Use lazy reconnect to defer tmux configuration until first attach
		// This reduces TUI startup from ~6s to ~2s by avoiding subprocess overhead.
		// Configuration (EnableMouseMode, ConfigureStatusBar, EnablePipePane) runs
		// on-demand via EnsureConfigured() when user interacts with the session.
		var tmuxSess *tmux.Session
		if instData.TmuxSession != "" {
			// Convert Status enum to string for tmux package
			// This restores the exact status across app restarts
			previousStatus := statusToString(instData.Status)
			tmuxSess = tmux.ReconnectSessionLazy(
				instData.TmuxSession,
				instData.Title,
				instData.ProjectPath,
				instData.Command,
				previousStatus,
			)
			// Pass instance ID for activity hooks (enables real-time status updates)
			tmuxSess.InstanceID = instData.ID
			// Note: EnableMouseMode is now deferred to EnsureConfigured()
			// Called automatically when user attaches to session
		}

		// Migrate old sessions without GroupPath
		groupPath := instData.GroupPath
		if groupPath == "" {
			groupPath = extractGroupPath(instData.ProjectPath)
		}

		// Expand tilde in project path (handles paths like ~/project saved from UI)
		projectPath := expandTilde(instData.ProjectPath)

		inst := &Instance{
			ID:                 instData.ID,
			Title:              instData.Title,
			ProjectPath:        projectPath,
			GroupPath:          groupPath,
			Order:              instData.Order,
			ParentSessionID:    instData.ParentSessionID,
			Command:            instData.Command,
			Wrapper:            instData.Wrapper,
			Tool:               instData.Tool,
			Status:             instData.Status,
			CreatedAt:          instData.CreatedAt,
			LastAccessedAt:     instData.LastAccessedAt,
			WorktreePath:       instData.WorktreePath,
			WorktreeRepoRoot:   instData.WorktreeRepoRoot,
			WorktreeBranch:     instData.WorktreeBranch,
			ClaudeSessionID:    instData.ClaudeSessionID,
			ClaudeDetectedAt:   instData.ClaudeDetectedAt,
			GeminiSessionID:    instData.GeminiSessionID,
			GeminiDetectedAt:   instData.GeminiDetectedAt,
			GeminiYoloMode:     instData.GeminiYoloMode,
			GeminiModel:        instData.GeminiModel,
			OpenCodeSessionID:  instData.OpenCodeSessionID,
			OpenCodeDetectedAt: instData.OpenCodeDetectedAt,
			CodexSessionID:     instData.CodexSessionID,
			CodexDetectedAt:    instData.CodexDetectedAt,
			ToolOptionsJSON:    instData.ToolOptionsJSON,
			LatestPrompt:       instData.LatestPrompt,
			LoadedMCPNames:     instData.LoadedMCPNames,
			tmuxSession:        tmuxSess,
		}

		// PERFORMANCE: Skip UpdateStatus at load time - use cached status from JSON
		// The background worker will update status on first tick.
		// This saves one subprocess call per session at startup.

		// PERFORMANCE: Skip session ID sync at load time
		// Session ID syncing (SetEnvironment calls) will happen on EnsureConfigured()
		// or when the session is restarted. This saves 0-4 subprocess calls per session.
		// Note: Session IDs are still preserved in sessions.json and work correctly
		// for restart/resume operations that read from the Instance directly.

		instances[i] = inst
	}

	return instances, data.Groups, nil
}

// GetStoragePathForProfile returns the path to the sessions.json file for a specific profile.
func GetStoragePathForProfile(profile string) (string, error) {
	if profile == "" {
		profile = DefaultProfile
	}

	profileDir, err := GetProfileDir(profile)
	if err != nil {
		return "", err
	}

	return filepath.Join(profileDir, "sessions.json"), nil
}

// GetUpdatedAt returns the last modification timestamp of the storage file
// This is read from the UpdatedAt field in the JSON file.
// Returns an error if the file doesn't exist or can't be read.
func (s *Storage) GetUpdatedAt() (time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if file exists
	if _, err := os.Stat(s.path); os.IsNotExist(err) {
		return time.Time{}, err
	}

	// Load and parse the file
	data, err := s.loadFromFile(s.path)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to load storage file: %w", err)
	}

	return data.UpdatedAt, nil
}

// GetFileMtime returns the filesystem modification time of the storage file.
// Unlike GetUpdatedAt which reads JSON content, this uses os.Stat for the filesystem mtime.
// This is useful for detecting external changes when fsnotify may not work (e.g., on 9p/NFS).
func (s *Storage) GetFileMtime() (time.Time, error) {
	info, err := os.Stat(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return time.Time{}, nil
		}
		return time.Time{}, err
	}
	return info.ModTime(), nil
}

// statusToString converts a Status enum to the string expected by tmux.ReconnectSessionWithStatus
func statusToString(s Status) string {
	switch s {
	case StatusRunning:
		return "active"
	case StatusWaiting:
		return "waiting"
	case StatusIdle:
		return "idle"
	case StatusError:
		return "waiting" // Treat errors as needing attention
	default:
		return "waiting"
	}
}
