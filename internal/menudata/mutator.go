package menudata

import (
	"errors"
	"time"
)

// DefaultUndoWindow is the default Chrome-style undo grace period for
// session deletes (POST /api/sessions/undelete). Mirrors the TUI ctrl+z
// in-memory undo stack window.
const DefaultUndoWindow = 30 * time.Second

// ErrUndoNothing is returned by SessionMutator.UndoDelete when the undo
// stack is empty.
var ErrUndoNothing = errors.New("nothing to undo")

// ErrUndoExpired is returned by SessionMutator.UndoDelete when the most
// recent delete is older than the configured undo window.
var ErrUndoExpired = errors.New("undo window expired")

// ErrSessionNotFound is returned by SessionMutator.FinishWorktree when the
// target session id does not resolve to a live instance. The handler maps
// this to 404. See issue #1126.
var ErrSessionNotFound = errors.New("session not found")

// ErrGroupNotFound is returned by SessionMutator.SetGroupExpanded when the
// path does not resolve to a group. The handler maps this to 404. A stale
// browser tab can easily PATCH a group the TUI has since deleted, and a
// silent no-op there would leave the sidebar asserting a collapse that was
// never stored.
var ErrGroupNotFound = errors.New("group not found")

// ErrNotAWorktree is returned by SessionMutator.FinishWorktree when the
// target session exists but is not in a git/jujutsu worktree (so there is
// nothing to merge or clean up). The handler maps this to 400. See issue
// #1126.
var ErrNotAWorktree = errors.New("session is not in a worktree")

// WorktreeFinishOptions configures a SessionMutator.FinishWorktree call.
// All fields are optional; the zero value asks the implementation to
// auto-detect the target branch, perform the merge, delete the source
// branch, and refuse if the worktree is dirty.
type WorktreeFinishOptions struct {
	// Into is the target branch to merge the worktree branch into. When
	// empty the backend's default branch is used (matches the
	// `agent-deck worktree finish --into` flag).
	Into string
	// NoMerge skips the merge step (mirrors --no-merge). The branch is
	// still removed (unless KeepBranch) and the worktree torn down.
	NoMerge bool
	// KeepBranch leaves the source branch in place after finishing
	// (mirrors --keep-branch). Useful when the branch already lives on a
	// remote PR.
	KeepBranch bool
	// Force skips the dirty-worktree safety check and forces branch
	// deletion even if the merge fast-forward didn't succeed.
	Force bool
}

// WorktreeFinishResult is what SessionMutator.FinishWorktree returns on
// success. Mirrors the JSON-output payload of `agent-deck worktree finish
// --json`.
type WorktreeFinishResult struct {
	SessionID     string
	Branch        string
	MergedInto    string
	Merged        bool
	BranchDeleted bool
}

// SessionMutator is implemented by internal/ui.WebMutator and injected at startup.
// It bridges web HTTP handlers to the TUI session/group management methods.
type SessionMutator interface {
	CreateSession(title, tool, projectPath, groupPath, modelID, reasoningEffort string) (string, error)
	StartSession(sessionID string) error
	StopSession(sessionID string) error
	RestartSession(sessionID string) error
	DeleteSession(sessionID string) error
	// CloseSession stops the session process while keeping its metadata
	// in storage (TUI Shift+D — non-destructive close).
	CloseSession(sessionID string) error
	ArchiveSession(sessionID string) error
	UnarchiveSession(sessionID string) error
	ForkSession(sessionID string) (string, error)
	// UndoDelete restores the most-recently deleted session if it was
	// deleted within the implementation's undo window. Returns the
	// restored session id. Implementations should return ErrUndoNothing
	// when the stack is empty and ErrUndoExpired when the most recent
	// entry is older than the window — the handler maps both to 404.
	UndoDelete() (string, error)
	// UpdateSession applies one or more field edits to a session. updates maps
	// session.Field* constants (raw strings — see internal/session/mutators.go)
	// to their string-encoded new values; bools are "true"/"false". Returns the
	// list of fields that actually changed (a no-op subset is permitted; only
	// fields whose new value differs from the stored value are reported) and
	// whether any updated field requires a restart to take effect. Validation
	// errors (unknown field, invalid value) leave the session unchanged.
	UpdateSession(sessionID string, updates map[string]string) (updatedFields []string, restartRequired bool, err error)
	// CreateGroup returns ErrGroupNotFound when parentPath names no group.
	CreateGroup(name, parentPath string) (string, error)
	// RenameGroup renames the group at groupPath and returns the path the
	// group lives at afterwards. A rename can move the group (and its
	// subtree) to a new path, so callers that hold the old path, such as a
	// browser with the group selected, need it to follow along (issue #2555).
	RenameGroup(groupPath, newName string) (string, error)
	// MoveSessionToGroup moves a session to another group with the
	// `agent-deck group move` semantics (see session.GroupTree.
	// ResolveMoveTargetGroup): "" or "root" is the default group, and a
	// missing group is created. Returns the group path the session landed in
	// and whether its resolved Claude config dir changed, which only takes
	// effect on the next restart (no conversation migration, like the CLI).
	// Returns ErrSessionNotFound when the id doesn't resolve. See issue #2368.
	MoveSessionToGroup(sessionID, groupPath string) (movedTo string, restartRequired bool, err error)
	DeleteGroup(groupPath string) error
	// SetGroupExpanded persists a group's collapsed/expanded state, the same
	// flag the TUI writes on its Enter/Tab toggle (home.go saveGroupState ->
	// Storage.SaveGroupsOnly). The web sidebar keeps its own collapse state,
	// so without this the two views drift apart permanently. Returns
	// ErrGroupNotFound when the path does not resolve to a group.
	SetGroupExpanded(groupPath string, expanded bool) error
	// FinishWorktree merges (or skips), removes the worktree, optionally
	// deletes the source branch, kills the tmux session, and removes the
	// session from storage. Mirrors the TUI W/shift+w hotkey and the
	// `agent-deck worktree finish` CLI. Returns ErrSessionNotFound when
	// the id doesn't resolve and ErrNotAWorktree when the session exists
	// but lacks worktree metadata. See issue #1126.
	FinishWorktree(sessionID string, opts WorktreeFinishOptions) (WorktreeFinishResult, error)
}
