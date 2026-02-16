package web

import (
	"fmt"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
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
	profile     string
	openStorage storageOpener
	now         func() time.Time
}

// NewSessionDataService creates a SessionDataService for a profile.
func NewSessionDataService(profile string) *SessionDataService {
	return &SessionDataService{
		profile:     session.GetEffectiveProfile(profile),
		openStorage: defaultStorageOpener,
		now:         time.Now,
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

	groupTree := session.NewGroupTreeWithGroups(instances, groupsData)
	flat := groupTree.Flatten()

	items := make([]MenuItem, 0, len(flat))
	totalGroups := 0
	totalSessions := 0

	for i, item := range flat {
		if item.Type == session.ItemTypeGroup && item.Group != nil {
			totalGroups++
			items = append(items, MenuItem{
				Index: i,
				Type:  MenuItemTypeGroup,
				Level: item.Level,
				Path:  item.Path,
				Group: &MenuGroup{
					Name:         item.Group.Name,
					Path:         item.Group.Path,
					Expanded:     item.Group.Expanded,
					Order:        item.Group.Order,
					SessionCount: groupTree.SessionCountForGroup(item.Group.Path),
				},
			})
			continue
		}

		if item.Type == session.ItemTypeSession && item.Session != nil {
			totalSessions++
			items = append(items, MenuItem{
				Index:               i,
				Type:                MenuItemTypeSession,
				Level:               item.Level,
				Path:                item.Path,
				IsLastInGroup:       item.IsLastInGroup,
				IsSubSession:        item.IsSubSession,
				IsLastSubSession:    item.IsLastSubSession,
				ParentIsLastInGroup: item.ParentIsLastInGroup,
				Session:             toMenuSession(item.Session),
			})
		}
	}

	return &MenuSnapshot{
		Profile:       s.profile,
		GeneratedAt:   s.now().UTC(),
		TotalGroups:   totalGroups,
		TotalSessions: totalSessions,
		Items:         items,
	}, nil
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
