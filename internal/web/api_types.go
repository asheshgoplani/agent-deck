package web

import "github.com/asheshgoplani/agent-deck/internal/session"

// Error code constants for API error responses.
const (
	ErrCodeUnauthorized     = "UNAUTHORIZED"
	ErrCodeForbidden        = "MUTATIONS_DISABLED"
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
