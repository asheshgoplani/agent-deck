package web

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

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
	Status          session.Status `json:"status"`
	GroupPath       string         `json:"groupPath"`
	ProjectPath     string         `json:"projectPath"`
	ParentSessionID string         `json:"parentSessionId,omitempty"`
	Order           int            `json:"order"`
	TmuxSession     string         `json:"tmuxSession,omitempty"`
	CreatedAt       time.Time      `json:"createdAt"`
	LastAccessedAt  time.Time      `json:"lastAccessedAt,omitempty"`
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

	return BuildMenuSnapshot(s.profile, instances, groupsData, s.now()), nil
}

func toMenuSession(inst *session.Instance) *MenuSession {
	tmuxName := ""
	if tmuxSess := inst.GetTmuxSession(); tmuxSess != nil {
		tmuxName = tmuxSess.Name
	}

	return &MenuSession{
		ID:              inst.ID,
		Title:           inst.Title,
		Tool:            inst.GetToolThreadSafe(),
		Status:          inst.GetStatusThreadSafe(),
		GroupPath:       inst.GroupPath,
		ProjectPath:     inst.ProjectPath,
		ParentSessionID: inst.ParentSessionID,
		Order:           inst.Order,
		TmuxSession:     tmuxName,
		CreatedAt:       inst.CreatedAt,
		LastAccessedAt:  inst.LastAccessedAt,
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
			continue
		}

		var parsed rawHookStatus
		if err := json.Unmarshal(raw, &parsed); err != nil {
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
