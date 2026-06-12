package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

var hookStatusLog = logging.ForComponent(logging.CompWeb)

const (
	MenuItemTypeGroup   = "group"
	MenuItemTypeSession = "session"
)

// MenuSnapshot is a flattened, ordered representation of session navigation data.
type MenuSnapshot struct {
	Profile       string     `json:"profile"`
	GeneratedAt   time.Time  `json:"generatedAt"`
	TotalGroups   int        `json:"totalGroups"`
	TotalSessions int        `json:"totalSessions"`
	Items         []MenuItem `json:"items"`
}

// MenuItem represents one row in the flattened navigation list.
type MenuItem struct {
	Index               int          `json:"index"`
	Type                string       `json:"type"`
	Level               int          `json:"level"`
	Path                string       `json:"path,omitempty"`
	Group               *MenuGroup   `json:"group,omitempty"`
	Session             *MenuSession `json:"session,omitempty"`
	IsLastInGroup       bool         `json:"isLastInGroup,omitempty"`
	IsSubSession        bool         `json:"isSubSession,omitempty"`
	IsLastSubSession    bool         `json:"isLastSubSession,omitempty"`
	ParentIsLastInGroup bool         `json:"parentIsLastInGroup,omitempty"`
}

// MenuGroup contains metadata for a group item.
type MenuGroup struct {
	Name         string `json:"name"`
	Path         string `json:"path"`
	Expanded     bool   `json:"expanded"`
	Order        int    `json:"order"`
	SessionCount int    `json:"sessionCount"`
}

// MenuSession contains metadata for a session item.
type MenuSession struct {
	ID              string         `json:"id"`
	Title           string         `json:"title"`
	Tool            string         `json:"tool"`
	ModelID         string         `json:"modelId,omitempty"`
	Model           string         `json:"model,omitempty"`
	ModelVersion    string         `json:"modelVersion,omitempty"`
	CanFork         bool           `json:"canFork"`
	Status          session.Status `json:"status"`
	GroupPath       string         `json:"groupPath"`
	ProjectPath     string         `json:"projectPath"`
	ParentSessionID string         `json:"parentSessionId,omitempty"`
	Order           int            `json:"order"`
	TmuxSession     string         `json:"tmuxSession,omitempty"`
	// TmuxSocketName is the tmux -L selector captured at session creation
	// (Instance.TmuxSocketName). Surfaced so the web PTY bridge can reach
	// sessions running on an isolated socket (issue #687, v1.7.50).
	TmuxSocketName string    `json:"tmuxSocketName,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
	LastAccessedAt time.Time `json:"lastAccessedAt,omitempty"`
	ArchivedAt     time.Time `json:"archivedAt,omitempty"`

	// Fields below mirror *session.Instance state visible in the TUI
	// EditSessionDialog. Promoted from MISSING in tests/web/PARITY_MATRIX.md
	// so a web client can render the same edit form as the TUI without a
	// secondary lookup. All omit on zero-value so the wire stays compact.

	IsConductor bool `json:"isConductor,omitempty"`

	ClaudeSessionID   string `json:"claudeSessionId,omitempty"`
	GeminiSessionID   string `json:"geminiSessionId,omitempty"`
	GeminiModel       string `json:"geminiModel,omitempty"`
	GeminiYoloMode    *bool  `json:"geminiYoloMode,omitempty"`
	CodexSessionID    string `json:"codexSessionId,omitempty"`
	OpenCodeSessionID string `json:"opencodeSessionId,omitempty"`

	LatestPrompt string `json:"latestPrompt,omitempty"`
	Notes        string `json:"notes,omitempty"`

	Color string `json:"color,omitempty"`

	Command         string          `json:"command,omitempty"`
	Wrapper         string          `json:"wrapper,omitempty"`
	Channels        []string        `json:"channels,omitempty"`
	ExtraArgs       []string        `json:"extraArgs,omitempty"`
	ToolOptionsJSON json.RawMessage `json:"toolOptions,omitempty"`

	Sandbox          *session.SandboxConfig `json:"sandbox,omitempty"`
	SandboxContainer string                 `json:"sandboxContainer,omitempty"`
	SSHHost          string                 `json:"sshHost,omitempty"`
	SSHRemotePath    string                 `json:"sshRemotePath,omitempty"`

	MultiRepoEnabled   bool                        `json:"multiRepoEnabled,omitempty"`
	AdditionalPaths    []string                    `json:"additionalPaths,omitempty"`
	MultiRepoTempDir   string                      `json:"multiRepoTempDir,omitempty"`
	MultiRepoWorktrees []session.MultiRepoWorktree `json:"multiRepoWorktrees,omitempty"`

	WorktreePath     string `json:"worktreePath,omitempty"`
	WorktreeRepoRoot string `json:"worktreeRepoRoot,omitempty"`
	WorktreeBranch   string `json:"worktreeBranch,omitempty"`

	TitleLocked        bool `json:"titleLocked,omitempty"`
	NoTransitionNotify bool `json:"noTransitionNotify,omitempty"`

	LoadedMCPNames []string `json:"loadedMcpNames,omitempty"`

	// claude_analytics has no underlying struct on *Instance so the matrix
	// keeps it MISSING; only gemini is exposed today.
	GeminiAnalytics *session.GeminiSessionAnalytics `json:"geminiAnalytics,omitempty"`
}

type storageLoader interface {
	LoadWithGroups() ([]*session.Instance, []*session.GroupData, error)
	Close() error
}

type storageOpener func(profile string) (storageLoader, error)

// SessionDataService loads profile session data and transforms it into web-friendly DTOs.
type SessionDataService struct {
	profile          string
	openStorage      storageOpener
	now              func() time.Time
	refreshLiveState bool
	loadHookStatuses func() map[string]*session.HookStatus
}

// NewSessionDataService creates a SessionDataService for a profile.
func NewSessionDataService(profile string) *SessionDataService {
	return &SessionDataService{
		profile:          session.GetEffectiveProfile(profile),
		openStorage:      defaultStorageOpener,
		now:              time.Now,
		refreshLiveState: true,
		loadHookStatuses: defaultLoadHookStatuses,
	}
}

func defaultStorageOpener(profile string) (storageLoader, error) {
	return session.NewStorageWithProfile(profile)
}

// Profile returns the effective profile this service reads from.
func (s *SessionDataService) Profile() string {
	return s.profile
}

// LoadMenuSnapshot loads sessions/groups and returns a deterministic flattened menu DTO.
func (s *SessionDataService) LoadMenuSnapshot() (*MenuSnapshot, error) {
	if s == nil {
		return nil, fmt.Errorf("session data service is nil")
	}
	if s.openStorage == nil {
		return nil, fmt.Errorf("storage opener is not configured")
	}
	if s.now == nil {
		s.now = time.Now
	}

	storage, err := s.openStorage(s.profile)
	if err != nil {
		return nil, fmt.Errorf("open storage for profile %q: %w", s.profile, err)
	}
	defer func() { _ = storage.Close() }()

	instances, groupsData, err := storage.LoadWithGroups()
	if err != nil {
		return nil, fmt.Errorf("load sessions for profile %q: %w", s.profile, err)
	}
	if s.refreshLiveState {
		s.refreshStatuses(instances)
	}

	active := session.FilterInstancesByArchive(instances, false)
	return BuildMenuSnapshot(s.profile, active, groupsData, s.now()), nil
}

// LoadArchivedMenuSnapshot returns a menu containing only archived sessions.
func (s *SessionDataService) LoadArchivedMenuSnapshot() (*MenuSnapshot, error) {
	if s == nil {
		return nil, fmt.Errorf("session data service is nil")
	}
	if s.openStorage == nil {
		return nil, fmt.Errorf("storage opener is not configured")
	}
	if s.now == nil {
		s.now = time.Now
	}

	storage, err := s.openStorage(s.profile)
	if err != nil {
		return nil, fmt.Errorf("open storage for profile %q: %w", s.profile, err)
	}
	defer func() { _ = storage.Close() }()

	instances, groupsData, err := storage.LoadWithGroups()
	if err != nil {
		return nil, fmt.Errorf("load sessions for profile %q: %w", s.profile, err)
	}
	archived := session.FilterInstancesByArchive(instances, true)
	return BuildMenuSnapshot(s.profile, archived, groupsData, s.now()), nil
}

func toMenuSession(inst *session.Instance) *MenuSession {
	tmuxName := ""
	if tmuxSess := inst.GetTmuxSession(); tmuxSess != nil {
		tmuxName = tmuxSess.Name
	}
	modelInfo := inst.LaunchModelInfo()

	return &MenuSession{
		ID:                 inst.ID,
		Title:              inst.Title,
		Tool:               inst.GetToolThreadSafe(),
		ModelID:            modelInfo.ModelID,
		Model:              modelInfo.Model,
		ModelVersion:       modelInfo.Version,
		CanFork:            inst.CanFork(),
		Status:             inst.GetStatusThreadSafe(),
		GroupPath:          inst.GroupPath,
		ProjectPath:        inst.ProjectPath,
		ParentSessionID:    inst.ParentSessionID,
		Order:              inst.Order,
		TmuxSession:        tmuxName,
		TmuxSocketName:     inst.TmuxSocketName,
		CreatedAt:          inst.CreatedAt,
		LastAccessedAt:     inst.LastAccessedAt,
		ArchivedAt:         inst.ArchivedAt,
		IsConductor:        inst.IsConductor,
		ClaudeSessionID:    inst.ClaudeSessionID,
		GeminiSessionID:    inst.GeminiSessionID,
		GeminiModel:        inst.GeminiModel,
		GeminiYoloMode:     inst.GeminiYoloMode,
		CodexSessionID:     inst.CodexSessionID,
		OpenCodeSessionID:  inst.OpenCodeSessionID,
		LatestPrompt:       inst.LatestPrompt,
		Notes:              inst.Notes,
		Color:              inst.Color,
		Command:            inst.Command,
		Wrapper:            inst.Wrapper,
		Channels:           inst.Channels,
		ExtraArgs:          inst.ExtraArgs,
		ToolOptionsJSON:    inst.ToolOptionsJSON,
		Sandbox:            inst.Sandbox,
		SandboxContainer:   inst.SandboxContainer,
		SSHHost:            inst.SSHHost,
		SSHRemotePath:      inst.SSHRemotePath,
		MultiRepoEnabled:   inst.MultiRepoEnabled,
		AdditionalPaths:    inst.AdditionalPaths,
		MultiRepoTempDir:   inst.MultiRepoTempDir,
		MultiRepoWorktrees: inst.MultiRepoWorktrees,
		WorktreePath:       inst.WorktreePath,
		WorktreeRepoRoot:   inst.WorktreeRepoRoot,
		WorktreeBranch:     inst.WorktreeBranch,
		TitleLocked:        inst.TitleLocked,
		NoTransitionNotify: inst.NoTransitionNotify,
		LoadedMCPNames:     inst.LoadedMCPNames,
		GeminiAnalytics:    inst.GeminiAnalytics,
	}
}

type rawHookStatus struct {
	Status    string `json:"status"`
	SessionID string `json:"session_id"`
	Event     string `json:"event"`
	Timestamp int64  `json:"ts"`
}

func defaultLoadHookStatuses() map[string]*session.HookStatus {
	hooksByInstance := make(map[string]*session.HookStatus)
	hooksDir := session.GetHooksDir()

	entries, err := os.ReadDir(hooksDir)
	if err != nil {
		// ENOENT is benign — directory simply hasn't been created yet
		// because no hook event has fired in this profile. Anything else
		// (perm denied, IO error) is a real signal worth surfacing.
		if !errors.Is(err, os.ErrNotExist) {
			hookStatusLog.Warn("hook_status_dir_read_failed",
				slog.String("dir", hooksDir),
				slog.String("error", err.Error()),
			)
		}
		return hooksByInstance
	}

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		instanceID := strings.TrimSuffix(entry.Name(), ".json")
		if instanceID == "" {
			continue
		}

		raw, err := os.ReadFile(filepath.Join(hooksDir, entry.Name()))
		if err != nil {
			hookStatusLog.Warn("hook_status_read_failed",
				slog.String("file", entry.Name()),
				slog.String("instance", instanceID),
				slog.String("error", err.Error()),
			)
			continue
		}

		var parsed rawHookStatus
		if err := json.Unmarshal(raw, &parsed); err != nil {
			hookStatusLog.Warn("hook_status_unmarshal_failed",
				slog.String("file", entry.Name()),
				slog.String("instance", instanceID),
				slog.String("error", err.Error()),
			)
			continue
		}
		if parsed.Status == "" {
			continue
		}

		updatedAt := time.Unix(parsed.Timestamp, 0)
		if parsed.Timestamp <= 0 {
			if info, err := entry.Info(); err == nil {
				updatedAt = info.ModTime()
			}
		}

		hooksByInstance[instanceID] = &session.HookStatus{
			Status:    parsed.Status,
			SessionID: parsed.SessionID,
			Event:     parsed.Event,
			UpdatedAt: updatedAt,
		}
	}

	return hooksByInstance
}

func (s *SessionDataService) refreshStatuses(instances []*session.Instance) {
	// Keep tmux caches warm so per-instance status checks reflect current pane state.
	tmux.RefreshExistingSessions()
	tmux.RefreshPaneInfoCache()

	var hooksByInstance map[string]*session.HookStatus
	if s.loadHookStatuses != nil {
		hooksByInstance = s.loadHookStatuses()
	}

	for _, inst := range instances {
		if inst == nil {
			continue
		}

		haveHookStatus := false
		if hooksByInstance != nil {
			if hs := hooksByInstance[inst.ID]; hs != nil {
				inst.UpdateHookStatus(hs)
				haveHookStatus = true
			}
		}

		// Without fresh hook data, force a full tmux status check for Claude
		// sessions. This avoids staying in stale idle state when hooks are not
		// emitting for an existing long-running session.
		if inst.GetToolThreadSafe() == "claude" && !haveHookStatus {
			inst.ForceNextStatusCheck()
		}

		if inst.GetTmuxSession() == nil {
			continue
		}
		_ = inst.UpdateStatus()
	}
}
