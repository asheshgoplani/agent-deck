package menudata

import (
	"encoding/json"
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
	// DefaultPath is the group's EXPLICITLY configured default_path — the
	// folder new sessions in this group start in. Empty (and omitted) when
	// the user has not configured one; the client then derives its own
	// fallback from the group's newest session.
	//
	// Read straight off the field. BuildMenuSnapshot's tree comes from
	// NewGroupTreeWithGroups, which already normalizes this through the
	// CACHED resolver (internal/session/groups.go:386-388 ->
	// updateGroupDefaultPath). Do NOT call ExplicitDefaultPathForGroup or
	// DefaultPathForGroup here: they run the uncached resolveGroupDefaultPath
	// (os.Stat + up to 3 git subprocesses, ~21ms per group) and this builder
	// runs on every /api/menu request AND every SSE menu event. See the
	// header comment at internal/session/groups.go:1711-1731.
	DefaultPath string `json:"defaultPath,omitempty"`
}

// MenuSession contains metadata for a session item.
type MenuSession struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Tool         string `json:"tool"`
	ModelID      string `json:"modelId,omitempty"`
	Model        string `json:"model,omitempty"`
	ModelVersion string `json:"modelVersion,omitempty"`
	CanFork      bool   `json:"canFork"`
	// MCPSupported mirrors session.ToolSupportsMCPManager for this session's
	// tool. It is computed server-side on purpose: the predicate is
	// config-driven (a user tool can declare compatible_with = "claude"), so a
	// duplicated client-side list would be wrong for custom tools and would
	// drift. The web MCP pane reads this instead of guessing from the name.
	MCPSupported bool           `json:"mcpSupported"`
	Status       session.Status `json:"status"`
	// Substate is the additive Honest-Status-v2 refinement of Status
	// (e.g. "model-unavailable", "auth-401", "idle-at-empty-prompt"). It
	// explains WHY a session is in its coarse status so consumers like the
	// Command Center can surface model-unavailable/401 distinctly instead of
	// hiding them as plain "running". Empty when there is no refinement.
	Substate string `json:"substate,omitempty"`
	// SubstateDetail is free text for the substate — the codex usage-limit
	// retry time ("try again at Oct 10th, 2026 8:03 AM"), or for a transcript
	// usage-limit / model-limit / account-blocked verdict its reason. Same
	// omitempty contract as Substate.
	SubstateDetail  string `json:"substateDetail,omitempty"`
	GroupPath       string `json:"groupPath"`
	ProjectPath     string `json:"projectPath"`
	ParentSessionID string `json:"parentSessionId,omitempty"`
	Order           int    `json:"order"`
	TmuxSession     string `json:"tmuxSession,omitempty"`
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
	// ConductorName is the session's resolved conductor name
	// (Instance.ResolveConductorName): stable across a decorated or renamed
	// title. Empty for non-conductor sessions.
	ConductorName string `json:"conductorName,omitempty"`

	ClaudeSessionID   string `json:"claudeSessionId,omitempty"`
	GeminiSessionID   string `json:"geminiSessionId,omitempty"`
	GeminiModel       string `json:"geminiModel,omitempty"`
	GeminiYoloMode    *bool  `json:"geminiYoloMode,omitempty"`
	CodexSessionID    string `json:"codexSessionId,omitempty"`
	OpenCodeSessionID string `json:"opencodeSessionId,omitempty"`

	LatestPrompt string `json:"latestPrompt,omitempty"`
	Notes        string `json:"notes,omitempty"`

	// Hints and Tags are the session's recall annotations from state.db
	// (`agent-deck session annotate`): open-keyed, e.g. headline, status,
	// ticket, purpose. Filled by the GET handlers, not BuildMenuSnapshot —
	// see snapshot_annotations.go.
	Hints map[string]string `json:"hints,omitempty"`
	Tags  []string          `json:"tags,omitempty"`

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

// MenuDataLoader provides menu snapshots for web APIs and push notifications.
type MenuDataLoader interface {
	LoadMenuSnapshot() (*MenuSnapshot, error)
}

// ToMenuSession converts one instance into its web DTO.
func ToMenuSession(inst *session.Instance) *MenuSession {
	tmuxName := ""
	if tmuxSess := inst.GetTmuxSession(); tmuxSess != nil {
		tmuxName = tmuxSess.TmuxName()
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
		MCPSupported:       session.ToolSupportsMCPManager(inst.GetToolThreadSafe()),
		Status:             inst.GetStatusThreadSafe(),
		Substate:           string(inst.CachedSubstate()),
		SubstateDetail:     inst.SubstateDetail(),
		GroupPath:          inst.GroupPath,
		ProjectPath:        inst.ProjectPath,
		ParentSessionID:    inst.ParentSessionID,
		Order:              inst.Order,
		TmuxSession:        tmuxName,
		TmuxSocketName:     inst.TmuxSocketName,
		CreatedAt:          inst.CreatedAt,
		LastAccessedAt:     inst.LastAccessedAt,
		ArchivedAt:         inst.ArchivedAtThreadSafe(),
		IsConductor:        inst.IsConductor,
		ConductorName:      inst.ResolveConductorNameThreadSafe(),
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
