package web

import (
	"encoding/json"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Error code constants for API error responses.
const (
	ErrCodeUnauthorized     = "UNAUTHORIZED"
	ErrCodeForbidden        = "MUTATIONS_DISABLED"
	ErrCodeCSRF             = "CROSS_ORIGIN_BLOCKED"
	ErrCodeNotFound         = "NOT_FOUND"
	ErrCodeBadRequest       = "INVALID_REQUEST"
	ErrCodeMethodNotAllowed = "METHOD_NOT_ALLOWED"
	ErrCodeRateLimited      = "RATE_LIMITED"
	ErrCodeInternalError    = "INTERNAL_ERROR"
	ErrCodeNotImplemented   = "NOT_IMPLEMENTED"
	ErrCodeReadOnly         = "READ_ONLY"
)

// CreateSessionRequest is the body for POST /api/sessions.
type CreateSessionRequest struct {
	Title       string `json:"title"`
	Tool        string `json:"tool"`
	ProjectPath string `json:"projectPath"`
	GroupPath   string `json:"groupPath,omitempty"`
	ModelID     string `json:"modelId,omitempty"`
}

// SessionPatch is the body for PATCH /api/sessions/{id}. All fields optional;
// a nil pointer means "leave unchanged". Setting Title also clears AutoName
// (mirrors the TUI rename — a user-chosen name replaces the auto handle).
// GroupPath moves the session to another group (group membership lives in the
// group tree, not the instance, so it is applied via MoveSessionToGroup).
type SessionPatch struct {
	Title       *string          `json:"title,omitempty"`
	Color       *string          `json:"color,omitempty"`
	Notes       *string          `json:"notes,omitempty"`
	Channels    *[]string        `json:"channels,omitempty"`
	ExtraArgs   *[]string        `json:"extraArgs,omitempty"`
	ToolOptions *json.RawMessage `json:"toolOptions,omitempty"`
	GeminiModel *string          `json:"geminiModel,omitempty"`
	GeminiYolo  *bool            `json:"geminiYolo,omitempty"`
	GroupPath   *string          `json:"groupPath,omitempty"`
}

// CreateGroupRequest is the body for POST /api/groups.
type CreateGroupRequest struct {
	Name       string `json:"name"`
	ParentPath string `json:"parentPath,omitempty"`
}

// RenameGroupRequest is the body for PATCH /api/groups/{path}. Either renames
// the group ({name}) OR reparents it ({parentPath}) — the two are mutually
// exclusive in a single request because a reparent changes the group's path,
// which would stale a same-request rename's path lookup. parentPath is a pointer
// so "" (move to root) is distinguishable from absent.
type RenameGroupRequest struct {
	Name       string  `json:"name,omitempty"`
	ParentPath *string `json:"parentPath,omitempty"`
}

// UpdateSessionRequest is the body for PATCH /api/sessions/{id}. Every field
// is optional; only the fields present in the request body are updated.
// Pointer types let the handler distinguish "not supplied" from "set to zero
// value" — important for booleans, where a missing field must not silently
// clear the flag.
//
// Field names mirror session.Field* constants so the handler can dispatch
// directly through session.SetField without a translation table.
//
// The SetField-backed fields (above the divider) flow through session.SetField,
// which validates them and reports a restart policy. The fields below the
// divider are applied directly to the instance (session.SetField has no entry
// for them): GroupPath moves the session in the group tree (MoveSessionToGroup),
// while ToolOptions/GeminiModel/GeminiYolo are written via SessionPatch/
// EditSession. ToolOptions is applied BEFORE SkipPermissions/AutoMode so a
// request carrying both composes correctly — the typed bools flip their key
// inside the freshly-set ClaudeOptions blob rather than being clobbered by it.
type UpdateSessionRequest struct {
	Title           *string `json:"title,omitempty"`
	Notes           *string `json:"notes,omitempty"`
	Color           *string `json:"color,omitempty"`
	Tool            *string `json:"tool,omitempty"`
	ExtraArgs       *string `json:"extraArgs,omitempty"`
	Plugins         *string `json:"plugins,omitempty"`
	Channels        *string `json:"channels,omitempty"`
	SkipPermissions *bool   `json:"skipPermissions,omitempty"`
	AutoMode        *bool   `json:"autoMode,omitempty"`

	// Applied outside session.SetField (see doc comment above).
	GroupPath   *string          `json:"groupPath,omitempty"`
	ToolOptions *json.RawMessage `json:"toolOptions,omitempty"`
	GeminiModel *string          `json:"geminiModel,omitempty"`
	GeminiYolo  *bool            `json:"geminiYolo,omitempty"`
}

// UpdateSessionResponse confirms a PATCH succeeded. RestartRequired is true
// when any updated field only takes effect on next launch (tool, extra-args,
// plugins, skip-permissions, auto-mode). Clients use it to prompt before/after
// issuing a separate POST .../restart.
type UpdateSessionResponse struct {
	SessionID       string   `json:"sessionId"`
	UpdatedFields   []string `json:"updatedFields"`
	RestartRequired bool     `json:"restartRequired"`
}

// SessionActionResponse is returned by session action endpoints.
type SessionActionResponse struct {
	SessionID string         `json:"sessionId"`
	Status    session.Status `json:"status"`
}

// WorktreeFinishRequest is the body for POST /api/sessions/{id}/worktree/finish.
// All fields are optional. Mirrors `agent-deck worktree finish` CLI flags.
// See issue #1126.
type WorktreeFinishRequest struct {
	Into       string `json:"into,omitempty"`
	NoMerge    bool   `json:"noMerge,omitempty"`
	KeepBranch bool   `json:"keepBranch,omitempty"`
	Force      bool   `json:"force,omitempty"`
}

// WorktreeFinishResponse is returned by POST /api/sessions/{id}/worktree/finish.
type WorktreeFinishResponse struct {
	SessionID     string `json:"sessionId"`
	Branch        string `json:"branch"`
	MergedInto    string `json:"mergedInto,omitempty"`
	Merged        bool   `json:"merged"`
	BranchDeleted bool   `json:"branchDeleted"`
}

// SettingsResponse is returned by GET /api/settings.
type SettingsResponse struct {
	Profile      string `json:"profile"`
	ReadOnly     bool   `json:"readOnly"`
	WebMutations bool   `json:"webMutations"`
	Version      string `json:"version"`
	Commit       string `json:"commit"`

	// show_only_installed_tools filter (issue #1259). ToolFilter reports the
	// flag is on; VisibleTools lists the tool names that resolved on PATH (the
	// web dialog intersects its static list against this); ToolFilterFallback
	// reports the empty-fallback so the dialog shows a "showing all" hint. With
	// the flag off ToolFilter is false and the dialog ignores the other fields.
	ToolFilter         bool     `json:"toolFilter"`
	VisibleTools       []string `json:"visibleTools"`
	ToolFilterFallback bool     `json:"toolFilterFallback"`
}

// ProfilesResponse is returned by GET /api/profiles.
type ProfilesResponse struct {
	Current  string   `json:"current"`
	Profiles []string `json:"profiles"`
}

// SSESessionEvent is emitted on session:created and session:updated events.
type SSESessionEvent struct {
	EventType string       `json:"eventType"`
	Session   *MenuSession `json:"session"`
}

// SSEDeleteEvent is emitted on session:deleted events.
type SSEDeleteEvent struct {
	EventType string `json:"eventType"`
	ID        string `json:"id"`
}

// SSEGroupEvent is emitted on group:created and group:updated events.
type SSEGroupEvent struct {
	EventType string     `json:"eventType"`
	Group     *MenuGroup `json:"group"`
}

// SSEGroupDeleteEvent is emitted on group:deleted events.
type SSEGroupDeleteEvent struct {
	EventType string `json:"eventType"`
	Path      string `json:"path"`
}

// SSECostEvent is emitted on cost:updated events.
type SSECostEvent struct {
	EventType string  `json:"eventType"`
	SessionID string  `json:"sessionId"`
	Cost      float64 `json:"cost"`
}
