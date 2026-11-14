package web

import (
	"time"

	"github.com/asheshgoplani/agent-deck/internal/menudata"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// The menu snapshot DTOs, the in-memory menu store and the SessionMutator
// contract live in internal/menudata so internal/ui can use them without
// importing this package. These aliases keep the web.* names working.

const (
	MenuItemTypeGroup   = menudata.MenuItemTypeGroup
	MenuItemTypeSession = menudata.MenuItemTypeSession
	DefaultUndoWindow   = menudata.DefaultUndoWindow
)

type (
	MenuSnapshot            = menudata.MenuSnapshot
	MenuItem                = menudata.MenuItem
	MenuGroup               = menudata.MenuGroup
	MenuSession             = menudata.MenuSession
	MenuDataLoader          = menudata.MenuDataLoader
	MenuDataRevisionLoader  = menudata.MenuDataRevisionLoader
	LiveStateMenuDataLoader = menudata.LiveStateMenuDataLoader
	MenuSessionState        = menudata.MenuSessionState
	MemoryMenuData          = menudata.MemoryMenuData
	SessionMutator          = menudata.SessionMutator
	WorktreeFinishOptions   = menudata.WorktreeFinishOptions
	WorktreeFinishResult    = menudata.WorktreeFinishResult
)

// MemoryMenuData finds these optional interfaces on its fallback by type
// assertion. Assert them here so renaming RefreshesLiveState or
// MenuDataRevision fails to compile instead of silently turning off the
// live-state and revision refresh paths.
var (
	_ menudata.MenuDataLoader          = (*SessionDataService)(nil)
	_ menudata.MenuDataRevisionLoader  = (*SessionDataService)(nil)
	_ menudata.LiveStateMenuDataLoader = (*SessionDataService)(nil)
)

// Same error values (not copies), so errors.Is matches either name.
var (
	ErrUndoNothing     = menudata.ErrUndoNothing
	ErrUndoExpired     = menudata.ErrUndoExpired
	ErrSessionNotFound = menudata.ErrSessionNotFound
	ErrGroupNotFound   = menudata.ErrGroupNotFound
	ErrNotAWorktree    = menudata.ErrNotAWorktree
)

// NewMemoryMenuData creates an in-memory menu data store.
func NewMemoryMenuData(fallback MenuDataLoader) *MemoryMenuData {
	return menudata.NewMemoryMenuData(fallback)
}

// BuildMenuSnapshot converts in-memory session/group state into a flattened web DTO.
func BuildMenuSnapshot(profile string, instances []*session.Instance, groupsData []*session.GroupData, generatedAt time.Time) *MenuSnapshot {
	return menudata.BuildMenuSnapshot(profile, instances, groupsData, generatedAt)
}

func toMenuSession(inst *session.Instance) *MenuSession { return menudata.ToMenuSession(inst) }
