package web

import "github.com/asheshgoplani/agent-deck/internal/session"

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

// CreateGroupRequest is the body for POST /api/groups.
type CreateGroupRequest struct {
	Name       string `json:"name"`
	ParentPath string `json:"parentPath,omitempty"`
}

// RenameGroupRequest is the body for PATCH /api/groups/:path.
type RenameGroupRequest struct {
	Name string `json:"name"`
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
