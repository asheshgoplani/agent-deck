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
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

var storageLog = logging.ForComponent(logging.CompStorage)

// fixMalformedTildePath fixes paths where the UI textinput suggestion appended
// instead of replacing, producing paths like "/some/path~/actual/path".
// Returns the path starting from the last "~/" occurrence.
func fixMalformedTildePath(path string) string {
	if idx := strings.Index(path, "~/"); idx > 0 {
		return path[idx:]
	}
	return path
}

// StorageData represents the JSON structure for persistence (kept for migration/compat)
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

// Storage handles persistence of session data via SQLite.
// Thread-safe with mutex protection for concurrent access within a single process.
// Multiple processes share data via SQLite WAL mode.
type Storage struct {
	db      *statedb.StateDB
	dbPath  string     // Path to state.db (for change detection)
	profile string     // The profile this storage is for
	mu      sync.Mutex // Protects operations during transition
}

// NewStorageWithProfile creates a storage instance for a specific profile.
// If profile is empty, uses the effective profile (from env var or config).
// Automatically runs migration from old layout if needed, then opens SQLite.
// If sessions.json exists and state.db is empty, auto-migrates data.
func NewStorageWithProfile(profile string) (*Storage, error) {
	// Run profile layout migration if needed (safe to call multiple times)
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

	// Get profile directory
	profileDir, err := GetProfileDir(effectiveProfile)
	if err != nil {
		return nil, err
	}

	// Ensure directory exists with secure permissions (0700 = owner only)
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create storage directory: %w", err)
	}

	// Open SQLite database
	dbPath := filepath.Join(profileDir, "state.db")
	db, err := statedb.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open state database: %w", err)
	}

	// Create tables if they don't exist
	if err := db.Migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to migrate state database: %w", err)
	}

	// Auto-migrate from sessions.json if state.db is empty
	jsonPath := filepath.Join(profileDir, "sessions.json")
	if _, jsonErr := os.Stat(jsonPath); jsonErr == nil {
		empty, emptyErr := db.IsEmpty()
		if emptyErr == nil && empty {
			nInst, nGroups, migrateErr := statedb.MigrateFromJSON(jsonPath, db)
			if migrateErr != nil {
				storageLog.Warn("json_migration_failed", slog.String("error", migrateErr.Error()))
				// Continue with empty database rather than failing completely
			} else {
				storageLog.Info("migrated_from_json",
					slog.Int("instances", nInst),
					slog.Int("groups", nGroups))
				// Rename sessions.json to sessions.json.migrated as safety backup
				migratedPath := jsonPath + ".migrated"
				if renameErr := os.Rename(jsonPath, migratedPath); renameErr != nil {
					storageLog.Warn("json_rename_failed", slog.String("error", renameErr.Error()))
				}
			}
		}
	}

	return &Storage{
		db:      db,
		dbPath:  dbPath,
		profile: effectiveProfile,
	}, nil
}

// Profile returns the profile name this storage is using
func (s *Storage) Profile() string {
	return s.profile
}

// Path returns the database path this storage is using
func (s *Storage) Path() string {
	return s.dbPath
}

// GetDB returns the underlying StateDB for direct access (status writes, heartbeat, etc.)
func (s *Storage) GetDB() *statedb.StateDB {
	return s.db
}

// Close closes the underlying database connection.
func (s *Storage) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// Save persists instances to SQLite
// DEPRECATED: Use SaveWithGroups to ensure groups are not lost
func (s *Storage) Save(instances []*Instance) error {
	return s.SaveWithGroups(instances, nil)
}

// SaveWithGroups persists instances and groups to SQLite.
// Converts Instance objects to database rows, then batch-inserts in a transaction.
func (s *Storage) SaveWithGroups(instances []*Instance, groupTree *GroupTree) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.db == nil {
		return fmt.Errorf("storage database not initialized")
	}

	// Convert instances to database rows
	rows := make([]*statedb.InstanceRow, len(instances))
	for i, inst := range instances {
		tmuxName := ""
		if inst.tmuxSession != nil {
			tmuxName = inst.tmuxSession.Name
		}

		toolData := statedb.MarshalToolData(
			inst.ClaudeSessionID, inst.ClaudeDetectedAt,
			inst.GeminiSessionID, inst.GeminiDetectedAt,
			inst.GeminiYoloMode, inst.GeminiModel,
			inst.OpenCodeSessionID, inst.OpenCodeDetectedAt,
			inst.CodexSessionID, inst.CodexDetectedAt,
			inst.LatestPrompt, inst.LoadedMCPNames,
			inst.ToolOptionsJSON,
		)

		rows[i] = &statedb.InstanceRow{
			ID:              inst.ID,
			Title:           inst.Title,
			ProjectPath:     inst.ProjectPath,
			GroupPath:       inst.GroupPath,
			Order:           inst.Order,
			Command:         inst.Command,
			Wrapper:         inst.Wrapper,
			Tool:            inst.Tool,
			Status:          string(inst.Status),
			TmuxSession:     tmuxName,
			CreatedAt:       inst.CreatedAt,
			LastAccessed:    inst.LastAccessedAt,
			ParentSessionID: inst.ParentSessionID,
			WorktreePath:    inst.WorktreePath,
			WorktreeRepo:    inst.WorktreeRepoRoot,
			WorktreeBranch:  inst.WorktreeBranch,
			ToolData:        toolData,
		}
	}

	if err := s.db.SaveInstances(rows); err != nil {
		return fmt.Errorf("failed to save instances: %w", err)
	}

	// Save groups (including empty ones)
	if groupTree != nil {
		groupRows := make([]*statedb.GroupRow, 0, len(groupTree.GroupList))
		for _, g := range groupTree.GroupList {
			groupRows = append(groupRows, &statedb.GroupRow{
				Path:        g.Path,
				Name:        g.Name,
				Expanded:    g.Expanded,
				Order:       g.Order,
				DefaultPath: g.DefaultPath,
			})
		}
		if err := s.db.SaveGroups(groupRows); err != nil {
			return fmt.Errorf("failed to save groups: %w", err)
		}
	}

	// Touch metadata for change detection by other instances
	_ = s.db.Touch()

	return nil
}

// DeleteInstance removes a single instance from the database by ID.
// This ensures the row is immediately removed, preventing resurrection on reload.
func (s *Storage) DeleteInstance(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.db == nil {
		return fmt.Errorf("storage database not initialized")
	}

	if err := s.db.DeleteInstance(id); err != nil {
		return fmt.Errorf("failed to delete instance %s: %w", id, err)
	}

	_ = s.db.Touch()
	return nil
}

// SaveGroupsOnly persists only the groups table to SQLite.
// This is a lightweight save for visual state like group expanded/collapsed.
// It does NOT call Touch() to avoid triggering StorageWatcher reloads on other instances.
func (s *Storage) SaveGroupsOnly(groupTree *GroupTree) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.db == nil {
		return fmt.Errorf("storage database not initialized")
	}

	if groupTree == nil {
		return nil
	}

	groupRows := make([]*statedb.GroupRow, 0, len(groupTree.GroupList))
	for _, g := range groupTree.GroupList {
		groupRows = append(groupRows, &statedb.GroupRow{
			Path:        g.Path,
			Name:        g.Name,
			Expanded:    g.Expanded,
			Order:       g.Order,
			DefaultPath: g.DefaultPath,
		})
	}

	if err := s.db.SaveGroups(groupRows); err != nil {
		return fmt.Errorf("failed to save groups: %w", err)
	}

	return nil
}

// Load reads instances from SQLite
func (s *Storage) Load() ([]*Instance, error) {
	instances, _, err := s.LoadWithGroups()
	return instances, err
}

// LoadLite reads session data from SQLite without tmux reconnection.
// This is a fast path for operations that only need to read session metadata
// (e.g., finding current session by tmux name) without initializing full Instance objects.
// Returns raw InstanceData and GroupData without any subprocess calls.
func (s *Storage) LoadLite() ([]*InstanceData, []*GroupData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.db == nil {
		return []*InstanceData{}, nil, nil
	}

	// Load from SQLite
	dbRows, err := s.db.LoadInstances()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load instances: %w", err)
	}

	dbGroups, err := s.db.LoadGroups()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load groups: %w", err)
	}

	// Convert to InstanceData format (for backward compat with CLI commands)
	instances := make([]*InstanceData, len(dbRows))
	for i, r := range dbRows {
		claudeSID, claudeAt,
			geminiSID, geminiAt,
			geminiYolo, geminiModel,
			opencodeSID, opencodeAt,
			codexSID, codexAt,
			latestPrompt, loadedMCPs,
			toolOpts := statedb.UnmarshalToolData(r.ToolData)

		instances[i] = &InstanceData{
			ID:                 r.ID,
			Title:              r.Title,
			ProjectPath:        r.ProjectPath,
			GroupPath:          r.GroupPath,
			Order:              r.Order,
			ParentSessionID:    r.ParentSessionID,
			Command:            r.Command,
			Wrapper:            r.Wrapper,
			Tool:               r.Tool,
			Status:             Status(r.Status),
			CreatedAt:          r.CreatedAt,
			LastAccessedAt:     r.LastAccessed,
			TmuxSession:        r.TmuxSession,
			WorktreePath:       r.WorktreePath,
			WorktreeRepoRoot:   r.WorktreeRepo,
			WorktreeBranch:     r.WorktreeBranch,
			ClaudeSessionID:    claudeSID,
			ClaudeDetectedAt:   claudeAt,
			GeminiSessionID:    geminiSID,
			GeminiDetectedAt:   geminiAt,
			GeminiYoloMode:     geminiYolo,
			GeminiModel:        geminiModel,
			OpenCodeSessionID:  opencodeSID,
			OpenCodeDetectedAt: opencodeAt,
			CodexSessionID:     codexSID,
			CodexDetectedAt:    codexAt,
			LatestPrompt:       latestPrompt,
			ToolOptionsJSON:    toolOpts,
			LoadedMCPNames:     loadedMCPs,
		}
	}

	// Convert groups
	groups := make([]*GroupData, len(dbGroups))
	for i, g := range dbGroups {
		groups[i] = &GroupData{
			Path:        g.Path,
			Name:        g.Name,
			Expanded:    g.Expanded,
			Order:       g.Order,
			DefaultPath: g.DefaultPath,
		}
	}

	return instances, groups, nil
}

// LoadWithGroups reads instances and groups from SQLite, reconnects tmux sessions.
func (s *Storage) LoadWithGroups() ([]*Instance, []*GroupData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.db == nil {
		storageLog.Debug("load_db_not_initialized", slog.String("profile", s.profile))
		return []*Instance{}, nil, nil
	}

	// Load from SQLite
	dbRows, err := s.db.LoadInstances()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load instances: %w", err)
	}

	dbGroups, err := s.db.LoadGroups()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load groups: %w", err)
	}

	// Convert to InstanceData for the existing convertToInstances pipeline
	data := &StorageData{
		Instances: make([]*InstanceData, len(dbRows)),
	}
	for i, r := range dbRows {
		claudeSID, claudeAt,
			geminiSID, geminiAt,
			geminiYolo, geminiModel,
			opencodeSID, opencodeAt,
			codexSID, codexAt,
			latestPrompt, loadedMCPs,
			toolOpts := statedb.UnmarshalToolData(r.ToolData)

		data.Instances[i] = &InstanceData{
			ID:                 r.ID,
			Title:              r.Title,
			ProjectPath:        r.ProjectPath,
			GroupPath:          r.GroupPath,
			Order:              r.Order,
			ParentSessionID:    r.ParentSessionID,
			Command:            r.Command,
			Wrapper:            r.Wrapper,
			Tool:               r.Tool,
			Status:             Status(r.Status),
			CreatedAt:          r.CreatedAt,
			LastAccessedAt:     r.LastAccessed,
			TmuxSession:        r.TmuxSession,
			WorktreePath:       r.WorktreePath,
			WorktreeRepoRoot:   r.WorktreeRepo,
			WorktreeBranch:     r.WorktreeBranch,
			ClaudeSessionID:    claudeSID,
			ClaudeDetectedAt:   claudeAt,
			GeminiSessionID:    geminiSID,
			GeminiDetectedAt:   geminiAt,
			GeminiYoloMode:     geminiYolo,
			GeminiModel:        geminiModel,
			OpenCodeSessionID:  opencodeSID,
			OpenCodeDetectedAt: opencodeAt,
			CodexSessionID:     codexSID,
			CodexDetectedAt:    codexAt,
			LatestPrompt:       latestPrompt,
			ToolOptionsJSON:    toolOpts,
			LoadedMCPNames:     loadedMCPs,
		}
	}

	// Convert groups
	data.Groups = make([]*GroupData, len(dbGroups))
	for i, g := range dbGroups {
		data.Groups[i] = &GroupData{
			Path:        g.Path,
			Name:        g.Name,
			Expanded:    g.Expanded,
			Order:       g.Order,
			DefaultPath: g.DefaultPath,
		}
	}

	return s.convertToInstances(data)
}

// GetDBPathForProfile returns the path to the state.db file for a specific profile.
func GetDBPathForProfile(profile string) (string, error) {
	if profile == "" {
		profile = DefaultProfile
	}

	profileDir, err := GetProfileDir(profile)
	if err != nil {
		return "", err
	}

	return filepath.Join(profileDir, "state.db"), nil
}

// GetUpdatedAt returns the last modification timestamp from SQLite metadata.
func (s *Storage) GetUpdatedAt() (time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.db == nil {
		return time.Time{}, fmt.Errorf("database not initialized")
	}

	ts, err := s.db.LastModified()
	if err != nil {
		return time.Time{}, err
	}
	if ts == 0 {
		return time.Time{}, nil
	}
	return time.Unix(0, ts), nil
}

// GetFileMtime returns the filesystem modification time of the database file.
// This is useful for detecting external changes when polling.
func (s *Storage) GetFileMtime() (time.Time, error) {
	info, err := os.Stat(s.dbPath)
	if err != nil {
		if os.IsNotExist(err) {
			return time.Time{}, nil
		}
		return time.Time{}, err
	}
	return info.ModTime(), nil
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
		// Configuration (EnableMouseMode, ConfigureStatusBar) runs
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
			tmuxSess.SetInjectStatusLine(GetTmuxSettings().GetInjectStatusLine())
			// Note: EnableMouseMode is now deferred to EnsureConfigured()
			// Called automatically when user attaches to session
		}

		// Migrate old sessions without GroupPath
		groupPath := instData.GroupPath
		if groupPath == "" {
			groupPath = extractGroupPath(instData.ProjectPath)
		}

		// Expand tilde in project path (handles paths like ~/project saved from UI)
		// fixMalformedTildePath handles the case where the textinput suggestion
		// appended instead of replacing, producing "/some/path~/actual/path".
		projectPath := ExpandPath(fixMalformedTildePath(instData.ProjectPath))

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

		// PERFORMANCE: Skip UpdateStatus at load time - use cached status from SQLite
		// The background worker will update status on first tick.
		// This saves one subprocess call per session at startup.

		// PERFORMANCE: Skip session ID sync at load time
		// Session ID syncing (SetEnvironment calls) will happen on EnsureConfigured()
		// or when the session is restarted. This saves 0-4 subprocess calls per session.

		instances[i] = inst
	}

	return instances, data.Groups, nil
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
