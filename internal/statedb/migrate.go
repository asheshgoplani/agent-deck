package statedb

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// jsonStorageData mirrors session.StorageData for migration (avoids circular import).
type jsonStorageData struct {
	Instances []*jsonInstanceData `json:"instances"`
	Groups    []*jsonGroupData    `json:"groups,omitempty"`
	UpdatedAt time.Time           `json:"updated_at"`
}

// jsonInstanceData mirrors session.InstanceData for migration.
type jsonInstanceData struct {
	ID              string    `json:"id"`
	Title           string    `json:"title"`
	ProjectPath     string    `json:"project_path"`
	GroupPath       string    `json:"group_path"`
	Order           int       `json:"order"`
	ParentSessionID string    `json:"parent_session_id,omitempty"`
	Command         string    `json:"command"`
	Wrapper         string    `json:"wrapper,omitempty"`
	Tool            string    `json:"tool"`
	Status          string    `json:"status"`
	CreatedAt       time.Time `json:"created_at"`
	LastAccessedAt  time.Time `json:"last_accessed_at,omitempty"`
	TmuxSession     string    `json:"tmux_session"`

	WorktreePath     string `json:"worktree_path,omitempty"`
	WorktreeRepoRoot string `json:"worktree_repo_root,omitempty"`
	WorktreeBranch   string `json:"worktree_branch,omitempty"`

	ClaudeSessionID  string    `json:"claude_session_id,omitempty"`
	ClaudeDetectedAt time.Time `json:"claude_detected_at,omitempty"`

	GeminiSessionID  string    `json:"gemini_session_id,omitempty"`
	GeminiDetectedAt time.Time `json:"gemini_detected_at,omitempty"`
	GeminiYoloMode   *bool     `json:"gemini_yolo_mode,omitempty"`
	GeminiModel      string    `json:"gemini_model,omitempty"`

	OpenCodeSessionID  string    `json:"opencode_session_id,omitempty"`
	OpenCodeDetectedAt time.Time `json:"opencode_detected_at,omitempty"`

	CodexSessionID  string    `json:"codex_session_id,omitempty"`
	CodexDetectedAt time.Time `json:"codex_detected_at,omitempty"`

	LatestPrompt    string          `json:"latest_prompt,omitempty"`
	ToolOptionsJSON json.RawMessage `json:"tool_options,omitempty"`
	LoadedMCPNames  []string        `json:"loaded_mcp_names,omitempty"`
}

// jsonGroupData mirrors session.GroupData for migration.
type jsonGroupData struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Expanded    bool   `json:"expanded"`
	Order       int    `json:"order"`
	DefaultPath string `json:"default_path,omitempty"`
}

// toolDataBlob is the JSON structure stored in the tool_data column.
type toolDataBlob struct {
	ClaudeSessionID    string          `json:"claude_session_id,omitempty"`
	ClaudeDetectedAt   int64           `json:"claude_detected_at,omitempty"`
	GeminiSessionID    string          `json:"gemini_session_id,omitempty"`
	GeminiDetectedAt   int64           `json:"gemini_detected_at,omitempty"`
	GeminiYoloMode     *bool           `json:"gemini_yolo_mode,omitempty"`
	GeminiModel        string          `json:"gemini_model,omitempty"`
	OpenCodeSessionID  string          `json:"opencode_session_id,omitempty"`
	OpenCodeDetectedAt int64           `json:"opencode_detected_at,omitempty"`
	CodexSessionID     string          `json:"codex_session_id,omitempty"`
	CodexDetectedAt    int64           `json:"codex_detected_at,omitempty"`
	LatestPrompt       string          `json:"latest_prompt,omitempty"`
	LoadedMCPNames     []string        `json:"loaded_mcp_names,omitempty"`
	ToolOptions        json.RawMessage `json:"tool_options,omitempty"`
	SSHHost            string          `json:"ssh_host,omitempty"`
	SSHRemotePath      string          `json:"ssh_remote_path,omitempty"`
}

// MigrateFromJSON reads a sessions.json file and inserts all data into the StateDB.
// Returns the number of instances and groups migrated.
func MigrateFromJSON(jsonPath string, db *StateDB) (int, int, error) {
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		return 0, 0, fmt.Errorf("read json: %w", err)
	}

	var storage jsonStorageData
	if err := json.Unmarshal(data, &storage); err != nil {
		return 0, 0, fmt.Errorf("parse json: %w", err)
	}

	// Convert instances
	rows := make([]*InstanceRow, 0, len(storage.Instances))
	for _, inst := range storage.Instances {
		td := toolDataBlob{
			ClaudeSessionID:   inst.ClaudeSessionID,
			GeminiSessionID:   inst.GeminiSessionID,
			GeminiYoloMode:    inst.GeminiYoloMode,
			GeminiModel:       inst.GeminiModel,
			OpenCodeSessionID: inst.OpenCodeSessionID,
			CodexSessionID:    inst.CodexSessionID,
			LatestPrompt:      inst.LatestPrompt,
			LoadedMCPNames:    inst.LoadedMCPNames,
			ToolOptions:       inst.ToolOptionsJSON,
		}
		if !inst.ClaudeDetectedAt.IsZero() {
			td.ClaudeDetectedAt = inst.ClaudeDetectedAt.Unix()
		}
		if !inst.GeminiDetectedAt.IsZero() {
			td.GeminiDetectedAt = inst.GeminiDetectedAt.Unix()
		}
		if !inst.OpenCodeDetectedAt.IsZero() {
			td.OpenCodeDetectedAt = inst.OpenCodeDetectedAt.Unix()
		}
		if !inst.CodexDetectedAt.IsZero() {
			td.CodexDetectedAt = inst.CodexDetectedAt.Unix()
		}

		tdJSON, err := json.Marshal(td)
		if err != nil {
			return 0, 0, fmt.Errorf("marshal tool_data for %s: %w", inst.ID, err)
		}

		rows = append(rows, &InstanceRow{
			ID:              inst.ID,
			Title:           inst.Title,
			ProjectPath:     inst.ProjectPath,
			GroupPath:       inst.GroupPath,
			Order:           inst.Order,
			Command:         inst.Command,
			Wrapper:         inst.Wrapper,
			Tool:            inst.Tool,
			Status:          inst.Status,
			TmuxSession:     inst.TmuxSession,
			CreatedAt:       inst.CreatedAt,
			LastAccessed:    inst.LastAccessedAt,
			ParentSessionID: inst.ParentSessionID,
			WorktreePath:    inst.WorktreePath,
			WorktreeRepo:    inst.WorktreeRepoRoot,
			WorktreeBranch:  inst.WorktreeBranch,
			ToolData:        tdJSON,
		})
	}

	if err := db.SaveInstances(rows); err != nil {
		return 0, 0, fmt.Errorf("save instances: %w", err)
	}

	// Convert groups
	groupRows := make([]*GroupRow, 0, len(storage.Groups))
	for _, g := range storage.Groups {
		groupRows = append(groupRows, &GroupRow{
			Path:        g.Path,
			Name:        g.Name,
			Expanded:    g.Expanded,
			Order:       g.Order,
			DefaultPath: g.DefaultPath,
		})
	}

	if len(groupRows) > 0 {
		if err := db.SaveGroups(groupRows); err != nil {
			return 0, 0, fmt.Errorf("save groups: %w", err)
		}
	}

	return len(rows), len(groupRows), nil
}

// MarshalToolData creates a tool_data JSON blob from individual fields.
// This is the forward path: Instance fields -> JSON blob for SQLite storage.
func MarshalToolData(
	claudeSessionID string, claudeDetectedAt time.Time,
	geminiSessionID string, geminiDetectedAt time.Time,
	geminiYoloMode *bool, geminiModel string,
	openCodeSessionID string, openCodeDetectedAt time.Time,
	codexSessionID string, codexDetectedAt time.Time,
	latestPrompt string, loadedMCPNames []string,
	toolOptionsJSON json.RawMessage,
	sshHost string, sshRemotePath string,
) json.RawMessage {
	td := toolDataBlob{
		ClaudeSessionID:   claudeSessionID,
		GeminiSessionID:   geminiSessionID,
		GeminiYoloMode:    geminiYoloMode,
		GeminiModel:       geminiModel,
		OpenCodeSessionID: openCodeSessionID,
		CodexSessionID:    codexSessionID,
		LatestPrompt:      latestPrompt,
		LoadedMCPNames:    loadedMCPNames,
		ToolOptions:       toolOptionsJSON,
		SSHHost:           sshHost,
		SSHRemotePath:     sshRemotePath,
	}
	if !claudeDetectedAt.IsZero() {
		td.ClaudeDetectedAt = claudeDetectedAt.Unix()
	}
	if !geminiDetectedAt.IsZero() {
		td.GeminiDetectedAt = geminiDetectedAt.Unix()
	}
	if !openCodeDetectedAt.IsZero() {
		td.OpenCodeDetectedAt = openCodeDetectedAt.Unix()
	}
	if !codexDetectedAt.IsZero() {
		td.CodexDetectedAt = codexDetectedAt.Unix()
	}
	data, _ := json.Marshal(td)
	return data
}

// UnmarshalToolData extracts individual fields from the tool_data JSON blob.
// This is the reverse path: JSON blob from SQLite -> individual Instance fields.
func UnmarshalToolData(data json.RawMessage) (
	claudeSessionID string, claudeDetectedAt time.Time,
	geminiSessionID string, geminiDetectedAt time.Time,
	geminiYoloMode *bool, geminiModel string,
	openCodeSessionID string, openCodeDetectedAt time.Time,
	codexSessionID string, codexDetectedAt time.Time,
	latestPrompt string, loadedMCPNames []string,
	toolOptionsJSON json.RawMessage,
	sshHost string, sshRemotePath string,
) {
	if len(data) == 0 {
		return
	}
	var td toolDataBlob
	if err := json.Unmarshal(data, &td); err != nil {
		return
	}
	claudeSessionID = td.ClaudeSessionID
	if td.ClaudeDetectedAt > 0 {
		claudeDetectedAt = time.Unix(td.ClaudeDetectedAt, 0)
	}
	geminiSessionID = td.GeminiSessionID
	if td.GeminiDetectedAt > 0 {
		geminiDetectedAt = time.Unix(td.GeminiDetectedAt, 0)
	}
	geminiYoloMode = td.GeminiYoloMode
	geminiModel = td.GeminiModel
	openCodeSessionID = td.OpenCodeSessionID
	if td.OpenCodeDetectedAt > 0 {
		openCodeDetectedAt = time.Unix(td.OpenCodeDetectedAt, 0)
	}
	codexSessionID = td.CodexSessionID
	if td.CodexDetectedAt > 0 {
		codexDetectedAt = time.Unix(td.CodexDetectedAt, 0)
	}
	latestPrompt = td.LatestPrompt
	loadedMCPNames = td.LoadedMCPNames
	toolOptionsJSON = td.ToolOptions
	sshHost = td.SSHHost
	sshRemotePath = td.SSHRemotePath
	return
}
