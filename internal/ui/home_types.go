package ui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/costs"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/update"
	"github.com/asheshgoplani/agent-deck/internal/watcher"
)

// deletedSessionEntry holds a deleted session for undo restore
type deletedSessionEntry struct {
	instance    *session.Instance
	deletedAt   time.Time
	cleanupDone <-chan struct{}
}

// Messages
type loadSessionsMsg struct {
	loadSequence      uint64
	loadWatcher       *StorageWatcher
	watcherTicket     *storageLoadTicket
	persistedSnapshot *statedb.RegistrySnapshotResult
	instances         []*session.Instance
	groups            []*session.GroupData
	err               error
	restoreState      *reloadState // Optional state to restore after reload
	poolProxies       int          // Number of socket proxies started
	poolError         error        // Pool initialization error
	loadMtime         time.Time    // File mtime at load time (for external change detection)
}

type sessionCreatedMsg struct {
	instance     *session.Instance
	err          error
	tempID       string // matches creatingSessions key for placeholder removal
	setupWarning string // non-fatal worktree setup-script failure, shown after a successful create
}

type sessionForkedMsg struct {
	instance     *session.Instance
	sourceID     string // ID of the source session that was forked (for cleanup)
	notice       string // non-fatal degradation notice shown after a successful fork
	err          error
	setupWarning string // non-fatal worktree setup-script failure, shown after a successful fork
}

type refreshMsg struct{}
type importReloadMsg struct{}

type statusUpdateMsg struct {
	attachedSessionID string    // Session that just returned from attach (if local attach)
	attachedWorkDir   string    // pane_current_path captured after attach returns
	detachedAt        time.Time // tui.detach stamp; zero unless timings are on
} // Triggers immediate status update without reloading

// storageChangedMsg signals that state.db was modified externally
type storageChangedMsg struct{}

// openCodeDetectionCompleteMsg signals that OpenCode session detection finished
// Used to trigger a save after async detection completes
type openCodeDetectionCompleteMsg struct {
	instanceID string
	sessionID  string // The detected session ID (may be empty if detection failed)
}

type updateCheckMsg struct {
	info *update.UpdateInfo
	err  error
}

type (
	tickMsg        time.Time
	quitMsg        bool
	reviverTickMsg struct{}
)

// previewFetchedMsg is sent when async preview content is ready
type previewFetchedMsg struct {
	previewKey string // cache key: sessionID or sessionID:windowIndex
	content    string
	err        error
	started    time.Time // tui.preview stamp; zero unless timings are on
}

// remotePaneWatchTarget names the remote session whose pane the TUI asked
// the remote agent to push ("" fields when none).
type remotePaneWatchTarget struct {
	remote  string
	session string
}

// remotePaneWatchMsg reports the outcome of a Watch request on a remote
// channel; on failure the preview poll takes over for that session.
type remotePaneWatchMsg struct {
	target remotePaneWatchTarget
	err    error
}

// previewDebounceMsg signals debounce period elapsed for preview fetch
// PERFORMANCE: Delays preview fetch during rapid navigation
type previewDebounceMsg struct {
	previewKey  string // cache key
	sessionID   string // parent session ID (for instance lookup)
	windowIndex int    // -1 for session, >= 0 for specific window
	remoteName  string // remote name for remote session preview
}

// analyticsFetchedMsg is sent when async analytics parsing is complete
type analyticsFetchedMsg struct {
	sessionID       string
	analytics       *session.SessionAnalytics
	geminiAnalytics *session.GeminiSessionAnalytics
	err             error
}

// MaintenanceCompleteMsg is the exported type for sending from main.go via p.Send()
type MaintenanceCompleteMsg struct {
	Result session.MaintenanceResult
}

// maintenanceCompleteMsg is the internal message handled in Update()
type maintenanceCompleteMsg struct {
	result session.MaintenanceResult
}

// remoteMoveResultMsg reports the outcome of an SSH-routed "move to group"
// for a remote session (M key → GroupDialogMove). The session's row lives in
// the remote's own state DB, so the move is executed there and the local
// fleet cache is patched only after the remote confirms.
type remoteMoveResultMsg struct {
	remoteName string
	sessionID  string
	groupPath  string
	err        error
}

// remoteGroupResultMsg reports the outcome of an SSH-routed "create group"
// for a remote context (g key on a remote row → GroupDialogCreate). The
// group must be created in the remote's own state DB; groupPath is the full
// path the remote assigned (parent/name, or name for a root-level group).
type remoteGroupResultMsg struct {
	remoteName string
	groupPath  string
	err        error
}

// remoteGroupDeleteResultMsg reports the outcome of an SSH-routed group
// delete (TUI 'd' on a remote group header).
type remoteGroupDeleteResultMsg struct {
	remoteName string
	groupPath  string
	err        error
}

// remoteGroupReorderResultMsg reports the outcome of an SSH-routed group
// reorder (shift+up/down on a remote group header → `group reorder` on the
// remote). delta is -1 for up and +1 for down; moved is the remote's own
// verdict, false when the group was already at the edge of its siblings.
type remoteGroupReorderResultMsg struct {
	remoteName string
	groupPath  string
	delta      int
	moved      bool
	err        error
}

// clearMaintenanceMsg signals auto-clear of maintenance banner
type clearMaintenanceMsg struct{}

// copyResultMsg is sent when async clipboard copy completes
type copyResultMsg struct {
	sessionTitle string
	lineCount    int
	err          error
}

// sendOutputResultMsg is sent when async inter-session send completes
type sendOutputResultMsg struct {
	sourceTitle string
	targetTitle string
	lineCount   int
	err         error
}

// remoteFetchRoundMsg starts one fleet poll: the config was read and every
// remote gets its own fetch Cmd. Each Cmd answers with a
// remoteSessionsFetchedMsg for that remote alone, as soon as that host
// replies, so a slow or wedged host never delays the others (#2177).
type remoteFetchRoundMsg struct {
	gen     uint64
	fetches []tea.Cmd
}

// remoteFetchRunner is what one per-remote fetch needs from an SSHRunner.
type remoteFetchRunner interface {
	FetchSessions(context.Context) ([]session.RemoteSessionInfo, *session.ListStats, error)
	FetchCostSummary(context.Context) (*costs.RemoteCostSummary, error)
	FetchGroupPaths(context.Context) ([]string, error)
}

// remoteSessionsFetchedMsg carries one remote's fetch result (or a pushed
// change shaped like one). Every other configured remote is listed in
// failed/groupsFailed so the merge keeps their rows untouched.
type remoteSessionsFetchedMsg struct {
	pollName   string
	pollConfig session.RemoteConfig
	// gen is the fetch's sequence number (see Home.remoteFetchSeq).
	gen uint64
	// pushed marks a result that did not come from a poll round (a change
	// the remote pushed over its channel), so it is not counted against the
	// round's outstanding fetches and never touches the in-flight guard.
	pushed bool
	// inRound marks one of the per-remote results a remoteFetchRoundMsg
	// fanned out; only those count down remoteFetchOutstanding. A message
	// from outside a round (config unreadable, no remotes configured) must
	// not steal a slot from a round still in flight.
	inRound bool
	// configErr is set when the user config could not be read; the handler
	// then keeps every cached remote instead of treating them as removed.
	configErr error
	sessions  map[string][]session.RemoteSessionInfo
	// #1101: per-remote cost summary collected on the same SSH fanout.
	costs map[string]*costs.RemoteCostSummary
	// groups holds each remote's FULL group path list (incl. empty groups)
	// from `group list --json`, so the move/create dialogs can offer folders
	// that currently hold no sessions. Only remotes that reported a fresh
	// list appear here.
	groups map[string][]string
	// groupsFailed marks remotes whose group-list fetch errored this round;
	// the handler keeps their last-good cached group paths.
	groupsFailed map[string]bool
	// failed marks remotes whose fetch errored this round (issue #1170).
	// The handler keeps their last-good sessions instead of wiping them,
	// so one slow/offline remote can't flicker the whole list.
	failed map[string]bool
	// versions carries the `agent-deck version` answers collected this
	// round, only for remotes whose cached answer was stale (#2164).
	versions map[string]session.RemoteVersionState
	// stats carries the remote's live load/memory/disk snapshot collected
	// this round, keyed by remote name. Unlike versions, it is gathered
	// every round (not throttled), but never blocks the round: a remote
	// without the `system stats` subcommand or an unreachable one is simply
	// absent, and the preview panel renders "stats unknown".
	stats map[string]remoteHostStatsResult
}

// remoteHostStatsResult is one remote's stats poll outcome: the snapshot
// (Stats.Ok is false when the remote could not answer), how long the SSH
// round trip took, and when it landed — the preview panel's "last poll"
// line.
type remoteHostStatsResult struct {
	Stats     session.RemoteHostStats
	Latency   time.Duration
	FetchedAt time.Time
}

// remoteStatsFetcher is the optional part of a remote fetch runner that can
// ask the remote for its `system stats --json` snapshot; session.SSHRunner
// satisfies it, test stubs need not.
type remoteStatsFetcher interface {
	FetchSystemStats(ctx context.Context) (session.RemoteHostStats, error)
}

// remoteLatenciesFetchedMsg is sent when an async batch of latency
// measurements completes. Keyed by remote name. See issue #1103.
type remoteLatenciesFetchedMsg struct {
	latencies map[string]session.RemoteLatency
}

// systemThemeMsg is sent when the OS dark mode setting changes.
type systemThemeMsg struct {
	dark bool
}

// worktreeDirtyCheckMsg is sent when an async worktree dirty check completes
type worktreeDirtyCheckMsg struct {
	sessionID string
	isDirty   bool
	err       error
}

// worktreeSetupResultMsg is sent when re-running the worktree setup script completes
type worktreeSetupResultMsg struct {
	sessionID    string
	sessionTitle string
	err          error
}

// worktreeFinishResultMsg is sent when the worktree finish operation completes
type worktreeFinishResultMsg struct {
	sessionID    string
	sessionTitle string
	targetBranch string
	merged       bool
	err          error
}

// watcherEventMsg is produced by listenForWatcherEvent when a new event arrives from the engine.
type watcherEventMsg struct{ event watcher.Event }

// watcherHealthMsg is produced by listenForWatcherHealth when the engine emits a health state update.
type watcherHealthMsg struct{ state watcher.HealthState }

// statusUpdateRequest is sent to the background worker with current viewport info
type statusUpdateRequest struct {
	viewOffset    int      // Current scroll position
	visibleHeight int      // How many items fit on screen
	flatItemIDs   []string // IDs of sessions in current flatItems order (for visible detection)
}
