package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"al.essio.dev/pkg/shellescape"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/sync/errgroup"

	"github.com/asheshgoplani/agent-deck/internal/agents"
	"github.com/asheshgoplani/agent-deck/internal/costs"
	"github.com/asheshgoplani/agent-deck/internal/docker"
	"github.com/asheshgoplani/agent-deck/internal/feedback"
	"github.com/asheshgoplani/agent-deck/internal/git"
	"github.com/asheshgoplani/agent-deck/internal/health"
	"github.com/asheshgoplani/agent-deck/internal/intervalhook"
	"github.com/asheshgoplani/agent-deck/internal/jujutsu"
	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/menudata"
	"github.com/asheshgoplani/agent-deck/internal/optiming"
	"github.com/asheshgoplani/agent-deck/internal/safego"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/sysinfo"
	"github.com/asheshgoplani/agent-deck/internal/telemetry"
	"github.com/asheshgoplani/agent-deck/internal/terminal"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
	"github.com/asheshgoplani/agent-deck/internal/triggers"
	"github.com/asheshgoplani/agent-deck/internal/update"
	"github.com/asheshgoplani/agent-deck/internal/vcs"
	"github.com/asheshgoplani/agent-deck/internal/vcsbackend"
	"github.com/asheshgoplani/agent-deck/internal/watcher"
)

// Version is set by main.go for update checking
var Version = "0.0.0"

// SetVersion sets the current version for update checking
func SetVersion(v string) {
	Version = v
}

// errSessionStillCreating is the shared user-facing message when a mutation
// (move/rename/fork) is attempted on a still-creating placeholder row (#1540).
var errSessionStillCreating = errors.New("session is still being created, try again in a moment")

// CreatingSession is a lightweight placeholder shown in the UI while
// a worktree + session is being created asynchronously.
// It is NOT a real session.Instance — it is excluded from save, polling, and search.
type CreatingSession struct {
	ID        string // Temporary ID for tracking
	Title     string
	Tool      string
	GroupPath string
	StartTime time.Time
}

// Structured loggers for UI components
var (
	uiLog     = logging.ForComponent(logging.CompUI)
	perfLog   = logging.ForComponent(logging.CompPerf)
	notifLog  = logging.ForComponent(logging.CompNotif)
	mcpUILog  = logging.ForComponent(logging.CompMCP)
	statusLog = logging.ForComponent(logging.CompStatus)
	// watcherHostLog is the watcher engine host's logger; tests replace it.
	watcherHostLog = logging.ForComponent(logging.CompWatcher)
)

const (
	// tickInterval for UI refresh and status updates
	// Background worker polls at 2s intervals for status detection
	// At 2s: 2-5 CapturePane() calls/sec = minimal CPU overhead
	tickInterval = 2 * time.Second

	// hoverDividerTTL bounds how long the divider's hover highlight may survive
	// without a mouse event confirming it. Checked on the 2s tick, so expiry
	// lands 3-5s after the last confirmation — far past any approach-and-click
	// pause, and short enough that a stranded highlight reads as transient.
	hoverDividerTTL = 3 * time.Second

	// logOutputDebounce limits how often a single session can trigger
	// UpdateStatus() from tmux %output events.
	// A higher value avoids expensive status parsing loops for very chatty sessions
	logOutputDebounce = 2 * time.Second

	// Launch animation minimum durations.
	// Claude/Gemini get longer feedback because startup UI is richer and may settle asynchronously.
	minLaunchAnimationDurationDefault = 500 * time.Millisecond
	minLaunchAnimationDurationClaude  = 800 * time.Millisecond

	// logCheckInterval - how often to check for oversized logs (fast check, just file stats)
	// This catches runaway logs before they cause high CPU
	logCheckInterval = 10 * time.Second

	// logMaintenanceInterval - how often to do full log maintenance (orphan cleanup, etc)
	// Prevents runaway log growth that can crash the system
	logMaintenanceInterval = 5 * time.Minute

	// analyticsCacheTTL - how long analytics data remains valid before refresh
	// Analytics don't change frequently, so 5s is a good balance between freshness and performance
	analyticsCacheTTL = 5 * time.Second

	// clearOnCompactThreshold - context usage % at which conductor sessions get /clear
	// Triggers before Claude's auto-compact (~95-98%), giving a clean slate via /clear
	clearOnCompactThreshold = 80.0

	// clearOnCompactCooldown - minimum time between /clear sends for the same session
	// Prevents repeated /clear if context fills up again quickly
	clearOnCompactCooldown = 60 * time.Second

	// attach-return grace periods keep the main menu responsive right after tea.Exec returns.
	attachReturnHotDuration  = 1200 * time.Millisecond
	attachReturnRefreshDelay = 350 * time.Millisecond
	attachReturnPreviewGrace = 1500 * time.Millisecond
)

// UI spacing constants (2-char grid system)
// These provide consistent spacing throughout the UI for a polished look
const (
	spacingTight  = 1 // Between related items (e.g., icon and label)
	spacingNormal = 2 // Between sections (e.g., list items, panel margins)
	spacingLarge  = 4 // Between major areas (e.g., info sections in preview)
)

// leftGutterWidth is the fixed cell width reserved at the start of every
// left-panel row for a root group's hotkey number ("N·"). Reserving it on all
// rows — even those without a number — keeps per-level indentation honest: a
// numbered root no longer steals an indent level from its children, so nesting
// reads consistently whether or not the root carries a hotkey. Must equal the
// rendered width of the "N·" hotkey label.
const leftGutterWidth = 2

// minSessionTitleWidth is the floor the session title cell keeps in narrow
// list columns. The account badge (" [account:...]") is the first thing
// shortened, then dropped, when the row runs out of room — never the title,
// which is the one piece of the row a user can't do without (#2201).
const minSessionTitleWidth = 12

// Minimum terminal size requirements (reduced for mobile support)
const (
	minTerminalWidth  = 40 // Reduced from 80 - supports mobile terminals
	minTerminalHeight = 12 // Reduced from 20 - supports smaller screens
)

// FilterKeyActive is the keyboard shortcut for the "open" status filter
// (shows all sessions except error/stopped). Change this constant to rebind.
const FilterKeyActive = "%"

// FilterKeyError is the keyboard shortcut for the error-only status filter.
// Keep it distinct from CostDashboardKey: advertised keys must have exactly
// one meaning in the overview context, regardless of whether cost tracking is
// available.
const FilterKeyError = "&"

// CostDashboardKey opens the cost dashboard. It is deliberately not reused as
// a conditional fallback for another action.
const CostDashboardKey = "$"

// FilterKeyArchived toggles the archived-sessions list view.
const FilterKeyArchived = "^"

// FilterModeActive is the filter value for "open" sessions: excludes the
// configured set of statuses (see DisplaySettings.ActiveFilterExcludes; default
// {error}). This is NOT a session status (never assigned to a session), just a
// filter mode.
const FilterModeActive session.Status = "active"

// FilterModeArchived shows only user-archived sessions (not a real session status).
const FilterModeArchived session.Status = "archived"

// Mouse interaction thresholds
const doubleClickThreshold = 500 * time.Millisecond

// Layout mode breakpoints for responsive design
const (
	layoutBreakpointSingle  = 50 // Below: single column, no preview
	layoutBreakpointStacked = 80 // Below: stacked layout (list above preview)
	// At or above 80: dual column (current side-by-side layout)
)

// Layout mode names
const (
	LayoutModeSingle  = "single"  // <50 cols: list only
	LayoutModeStacked = "stacked" // 50-79 cols: vertical stack
	LayoutModeDual    = "dual"    // 80+ cols: side-by-side
)

// PreviewMode defines what to show in the preview pane
type PreviewMode int

const (
	PreviewModeBoth      PreviewMode = iota // Show both analytics and output (default)
	PreviewModeOutput                       // Show output only (content preview)
	PreviewModeAnalytics                    // Show analytics only
)

// Responsive breakpoints for empty state content tiers
// These define when to show full/compact/minimal content
const (
	// Width breakpoints (for left panel after 35% split)
	emptyStateWidthFull    = 45 // Full content with all hints
	emptyStateWidthCompact = 35 // Compact: fewer hints, shorter text
	// Below 35: minimal mode (icon + title + 1 hint)

	// Height breakpoints (for content area)
	emptyStateHeightFull    = 18 // Full content with generous spacing
	emptyStateHeightCompact = 12 // Compact: reduced spacing
	// Below 12: minimal mode
)

// pendingTitle is a title change queued to survive a storage-watcher reload
// swap (see Home.pendingTitleChanges). It carries the intended lock state
// alongside the title so the reapply can restore both: a user rename is locked,
// a Claude-name sync stays unlocked.
type pendingTitle struct {
	title  string
	locked bool
}

// Home is the main application model
type Home struct {
	// Dimensions
	width  int
	height int

	// Profile
	profile string // The profile this Home is displaying

	// Data (protected by instancesMu for background worker access)
	instances          []*session.Instance
	instanceByID       map[string]*session.Instance // O(1) instance lookup by ID
	instancesMu        sync.RWMutex                 // Protects instances slice for thread-safe background access
	switchGenerations  map[string]uint64            // owns async harness-switch completions per instance
	storage            *session.Storage
	groupTree          *session.GroupTree
	flatItems          []session.Item // Flattened view for cursor navigation
	keepEmptyFilter    bool           // Set by filter key presses; restored filters retain the old fallback.
	liveSet            *pipeLiveSet   // sessions that should hold a live control pipe
	focusedSessionName string         // tmux name of the cursor-selected session (focusMu)
	focusMu            sync.Mutex     // protects focusedSessionName for the reconciler goroutine

	// headless is true when running `web --no-tui`: no bubbletea loop ever
	// boots, so the in-memory instances/groupTree are never populated by the
	// TUI's loadSessions cycle. The WebMutator hydrates them from storage on
	// each mutation instead (#1397). In live-TUI mode this stays false and the
	// bubbletea loop owns this state.
	headless bool

	// Components
	search               *Search
	globalSearch         *GlobalSearch // Recall search over recall.db (the G key)
	recallSource         RecallSource  // the index behind it (nil when [recall] enabled = false)
	recallOff            string        // why the index is not open ("" when it is); G shows it in the local search
	newDialog            *NewDialog
	pendingRemoteName    string                // #1353: remote target for the open new-session dialog ("" = local)
	groupDialog          *GroupDialog          // For creating/renaming groups
	forkDialog           *ForkDialog           // For forking sessions
	confirmDialog        *ConfirmDialog        // For confirming destructive actions
	hookTrustDialog      *HookTrustDialog      // First-use / changed worktree hook approval
	helpOverlay          *HelpOverlay          // For showing keyboard shortcuts
	mcpDialog            *MCPDialog            // For managing MCPs
	pluginDialog         *PluginDialog         // For managing per-session Claude Code plugins (RFC PLUGIN_ATTACH.md)
	editPathsDialog      *EditPathsDialog      // For editing multi-repo paths
	editSessionDialog    *EditSessionDialog    // For editing session settings (title/color/notes/command/...)
	skillDialog          *SkillDialog          // For managing project skills
	setupWizard          *SetupWizard          // For first-run setup
	settingsPanel        *SettingsPanel        // For editing settings
	analyticsPanel       *AnalyticsPanel       // For displaying session analytics
	geminiModelDialog    *GeminiModelDialog    // For selecting Gemini model
	promptInputDialog    *PromptInputDialog    // For prompting the highlighted session from the list without attaching (#1410)
	sessionPickerDialog  *SessionPickerDialog  // For sending output to another session
	codeBlockDialog      *CodeBlockDialog      // For copying a fenced code block from session output (#1412)
	sessionSwitcher      *SessionSwitcher      // In-attach session switcher (Ctrl+Tab / Ctrl+S)
	scrollbackPager      *ScrollbackPager      // In-attach scrollback pager for the deck's control-mode view (#1491)
	contextPager         *ContextPager         // Full-context inspector overlay (what the agent is being sent)
	worktreeFinishDialog *WorktreeFinishDialog // For finishing worktree sessions (merge + cleanup)
	feedbackDialog       *FeedbackDialog       // For in-app feedback popup (Phase 2)
	reportDialog         *ReportDialog         // "Report a problem" form: bug/idea -> GitHub issue (internal/issuereport)
	telemetryDialog      *TelemetryDialog      // One-time opt-in telemetry consent prompt (TELEMETRY.md)
	tel                  homeTelemetry         // Opt-in telemetry sampler and TUI lifetime (home_telemetry.go)
	zoxidePicker         *ZoxidePicker         // Quick-open picker backed by the zoxide DB
	feedbackState        *feedback.State       // Loaded at first show, avoids repeated disk I/O
	feedbackSender       *feedback.Sender      // Sender constructed once in NewHome (Phase 3, per D-05)
	watcherPanel         *WatcherPanel         // For showing watcher status and events
	agentsPanel          *AgentsPanel          // Agents tab: adopted agents, grouped by machine
	deadLetterPanel      *DeadLetterPanel      // Inspect, retry, and explicitly purge delivery failures
	triggersPanel        *TriggersPanel        // What wakes which session (#2702); read-only in P1
	// Triggers read model (triggers_live.go). A background tick reads the
	// state DB's triggers_rev stamp and reloads only when it moved; the
	// panel and the ⚡ row badge render from these cached values only.
	triggersFeed     triggersFeed
	triggerViews     []triggers.View
	triggerBadges    map[string]triggers.Badge
	triggersLoaded   bool
	triggersLoadedAt time.Time
	triggersRev      string
	triggersKey      string
	triggersPollGen  int
	// agentsView is the last built fleet view; agentBySession indexes its
	// rows by adopted session id so the session list can mark agent-owned
	// rows and the preview pane can render their card. Both are empty for a
	// user who has adopted nothing, which is what keeps those surfaces
	// invisible by default.
	agentsView          agents.View
	agentBySession      map[string]agents.AgentRow
	agentsLastRefresh   time.Time
	agentsLoaded        bool
	agentsLoadError     string
	toolVisibilityPanel *ToolVisibilityPanel // Edits [ui].hidden_tools
	// watcherHost runs the profile's watcher engine if this process owns it,
	// and otherwise waits to take it over (#2530). Set once by
	// StartWatcherEngine; read by the signal handler too, hence atomic.
	watcherHost atomic.Pointer[watcher.EngineHost]
	// spawnSendWorker starts the send worker that delivers a routed event to
	// its conductor; nil runs this binary's `session send-worker`. Tests
	// replace it before StartWatcherEngine.
	spawnSendWorker func(profile, sessionID string) error

	// Configurable hotkeys
	hotkeys        map[string]string // action -> configured key
	hotkeyLookup   map[string]string // pressed key -> canonical key used by switch cases
	blockedHotkeys map[string]bool   // canonical keys disabled via remap/unbind

	// mru backs the alternate-session toggle and MRU walk (#2058): a bounded,
	// in-memory ring of visited session IDs, seeded at startup from the
	// persisted last_accessed column so it survives a restart. See
	// internal/ui/mru_nav.go and internal/session/mru.go.
	mru *session.MRUHistory

	// Inline preview notes editing
	notesEditor           textarea.Model
	notesEditing          bool
	notesEditingSessionID string

	// Analytics cache (async fetching with TTL)
	currentAnalytics       *session.SessionAnalytics                  // Current analytics for selected session (Claude)
	currentGeminiAnalytics *session.GeminiSessionAnalytics            // Current analytics for selected session (Gemini)
	analyticsSessionID     string                                     // Session ID for current analytics
	analyticsFetchingID    string                                     // ID currently being fetched (prevents duplicates)
	analyticsCacheMu       sync.RWMutex                               // Protects analytics cache maps across UI + background workers
	analyticsCache         map[string]*session.SessionAnalytics       // TTL cache: sessionID -> analytics (Claude)
	geminiAnalyticsCache   map[string]*session.GeminiSessionAnalytics // TTL cache: sessionID -> analytics (Gemini)
	analyticsCacheTime     map[string]time.Time                       // TTL cache: sessionID -> cache timestamp

	// Context-inspection cache (async, same 5s TTL as analytics). Inspection is
	// a bounded head-read of the harness's own records, but it is still disk
	// I/O and must never happen on the render path.
	contextReportMu    sync.RWMutex
	contextReportCache map[string]contextReportEntry

	// State
	cursor              int                    // Selected item index in flatItems
	viewOffset          int                    // First visible item index (for scrolling)
	previewScrollOffset int                    // Lines scrolled up from tail in the preview pane (#574). 0 = tail (default). Reset on cursor move.
	isAttaching         atomic.Bool            // Prevents View() output during attach (fixes Bubble Tea Issue #431) - atomic for thread safety
	lastRenderedFrame   string                 // Last non-empty frame View produced; re-served while isAttaching so no frame is ever black (#1753). Event-loop goroutine only.
	pendingFrameOp      string                 // optiming span (tui.first_frame, tui.detach) that ends at the next list frame; set only while timings are on. Event-loop goroutine only.
	pendingFrameStart   time.Time              // start of pendingFrameOp
	statusFilter        session.Status         // Filter sessions by status ("" = all, or specific status)
	groupScope          string                 // Limit TUI to a specific group path ("" = all groups)
	initialSelect       string                 // Session ID or title to preselect on first load (#709). Does NOT scope groups.
	initialSelectDone   bool                   // Guard so preselection only fires once
	previewMode         PreviewMode            // What to show in preview pane (both, output-only, analytics-only)
	groupViewMode       session.GroupViewMode  // List partition: normal, active-on-top, populated-on-top (cycled by hotkey 't')
	timeFilter          session.TimeFilterMode // Recency filter: all, today, 3 days, 7 days, 30 days (cycled by hotkey '*')
	sidebarMode         sidebarPresentation    // Session navigation: grouped (default) or flat
	compactSidebar      bool                   // Narrow session rail; manual split resizing opts out
	embeddedLayout      bool                   // Embedded terminal layout; false preserves classic interaction
	sidebarDensity      string                 // Embedded sidebar rows per session: full (3), compact (2), minimal (1), auto
	err                 error
	errTime             time.Time  // When error occurred (for auto-dismiss)
	isReloading         bool       // Visual feedback during auto-reload
	initialLoading      bool       // True until first loadSessionsMsg received (shows splash screen)
	isQuitting          bool       // True when user pressed q, shows quitting splash
	restartRequested    bool       // True when the quit sequence should end in an in-place exec (restart.go)
	reloadVersion       uint64     // Incremented on each reload to prevent stale background saves
	reloadMu            sync.Mutex // Protects reloadVersion, isReloading, and lastLoadMtime for thread-safe access
	lastLoadMtime       time.Time  // File mtime when we last loaded (for external change detection)

	// Session reference pick/drop (ASK-111, session_ref_pick.go).
	sessionRef sessionRefState

	// Earliest eligible membership expiry; ordinary ticks only compare clocks.
	nextTimeFilterExpiry time.Time

	// Preview cache (async fetching - View() must be pure, no blocking I/O)
	previewCache      map[string]string    // previewKey -> cached preview content
	previewCacheTime  map[string]time.Time // previewKey -> when cached (for expiration)
	previewCacheMu    sync.RWMutex         // Protects previewCache for thread-safety
	previewFetchingID string               // ID currently being fetched (prevents duplicate fetches)

	// Preview debouncing (PERFORMANCE: prevents subprocess spawn on every keystroke)
	// During rapid navigation, we delay preview fetch by 150ms to let navigation settle
	pendingPreviewKey string     // Preview key waiting for debounced fetch
	previewDebounceMu sync.Mutex // Protects pendingPreviewKey

	// Remote preview over the channel (#2177 follow-up): the remote agent
	// pushes the focused remote session's pane, so no ssh poll runs for it
	// while the watch holds. remotePaneWatch is what the TUI last asked a
	// remote to watch; it is read and written only from Update.
	remotePaneWatch remotePaneWatchTarget
	// remotePaneUnsupported remembers, per remote, that `session output
	// --pane` is unknown to that remote's build, so the poll path takes the
	// transcript fallback directly instead of trying --pane every cycle.
	remotePaneUnsupported   map[string]bool
	remotePaneUnsupportedMu sync.Mutex

	// Round-robin status updates (Priority 1A optimization)
	// Instead of updating ALL sessions every tick, we update batches of 5-10 sessions
	// This reduces CPU usage by 90%+ while maintaining responsiveness
	statusUpdateIndex     atomic.Int32 // Current position in round-robin cycle (atomic for thread safety)
	fullStatusUpdateIndex atomic.Int32 // Next session in the periodic status sweep
	// sweepVisibleIDs is the UI tick's latest view of which session rows are
	// on screen; the periodic sweep refreshes those every pass, outside the
	// rotation budget, so a visible row is never older than one sweep.
	sweepVisibleMu  sync.Mutex
	sweepVisibleIDs map[string]bool
	// sweepEvidence records, per instance, the hook/SSE evidence the sweep
	// last applied. A row whose evidence moved since is refreshed on the next
	// pass regardless of rotation. Touched only by backgroundStatusUpdate.
	sweepEvidence map[string]session.StatusEvidence
	// Visible-row round-robin state (#1753): with a large group expanded the
	// visible set approaches fleet size, so "always refresh every visible row"
	// degenerates into the same per-row storm the off-screen batching exists to
	// prevent. Visible rows get their own cursor and per-pass budget instead.
	visibleStatusUpdateIndex atomic.Int32
	// visibleRefreshFingerprint remembers, per session, the cached tmux window
	// activity at the last visible-row refresh, so an unchanged idle row costs
	// zero. Owned exclusively by the status worker goroutine (processStatusUpdate)
	// — no lock needed.
	visibleRefreshFingerprint map[string]int64

	// Background status worker (Priority 1C optimization)
	// Moves status updates to a separate goroutine, completely decoupling from UI
	statusTrigger       chan statusUpdateRequest // Triggers background status update
	statusWorkerDone    chan struct{}            // Signals worker has stopped
	lastFullStatusSweep atomic.Int64             // UnixNano timestamp of last periodic status batch (legacy field name)
	lastPersistedStatus map[string]string        // instanceID -> last status written to SQLite
	// lastPersistedAutoNameDesc tracks the last auto-name description written to
	// SQLite per instance, so the background loop only issues a targeted write
	// when the live Claude task description actually changes (mirrors
	// lastPersistedStatus). Keyed by instance ID.
	lastPersistedAutoNameDesc map[string]string

	// Issue #1143: auto-stop dormant child sessions via central poll.
	// Coalesced into the existing 2-second statusWorker tick by way of
	// idleTimeoutLastTick, so we don't burn extra goroutines and the watcher
	// only runs every ~60s.
	idleTimeoutWatcher  *session.IdleTimeoutWatcher
	idleTimeoutLastTick atomic.Int64 // UnixNano

	// PERFORMANCE: Worker pool for output-driven status updates (Priority 2)
	// Caps the number of goroutines spawned for %output events from control pipes
	logUpdateChan chan *session.Instance // Buffers status update requests from PipeManager
	logWorkerWg   sync.WaitGroup         // Tracks log worker goroutines for clean shutdown

	// PERFORMANCE: Debounce output activity status updates
	lastLogActivity map[string]time.Time // sessionID -> last update time
	logActivityMu   sync.Mutex           // Protects lastLogActivity map

	// Window toggle state — sessions with collapsed window sub-items
	windowsCollapsed map[string]bool // sessionID -> true if windows hidden
	// Parent-child fold state (#2631): parent session ID -> child list folded
	parentChildrenCollapsed map[string]bool

	// Remote tree fold state — headers the user collapsed, keyed by Item.Path
	// ("remotes/<name>" or "remotes/<name>/<group>"). Remote groups are
	// synthetic UI buckets, not rows in groupTree, so they need their own
	// store rather than groupTree's expanded flags.
	remoteGroupsCollapsed map[string]bool

	// A `session focus --remote` request whose session the remote list does
	// not hold yet (#2704); retried after each remote refresh until its TTL.
	pendingRemoteFocus *pendingRemoteFocus

	// Manual order of remote session rows (#1875): remote -> group path ->
	// session IDs. Remote rows are not Instances in groupTree, so shift+up/down
	// cannot move them there; this local overlay carries the order instead and
	// is persisted in ui_state (see applyRemoteSessionOrder for the drift
	// rules and remoteOrder for why the scoping is nested).
	remoteSessionOrder remoteOrder

	// Worktree dirty status cache (lazy, 10s TTL)
	worktreeDirtyCache   map[string]bool      // sessionID -> isDirty
	worktreeDirtyCacheTs map[string]time.Time // sessionID -> cache timestamp
	worktreeDirtyMu      sync.Mutex           // Protects dirty cache maps

	// Memory management: periodic cache pruning
	lastCachePrune time.Time

	// Hook-based status detection (Claude Code lifecycle hooks)
	hookWatcher              *session.StatusFileWatcher
	pendingHooksPrompt       bool // True if user should be prompted to install Claude hooks
	pendingHermesHooksPrompt bool // True if user should be prompted to install Hermes hooks

	// SSE-based status detection for OpenCode sessions (issue #1614)
	sseWatcher *session.OpenCodeSSEWatcher

	// Test seams for the visible-pane clipboard action. Production leaves both
	// nil and uses fresh tmux capture plus the shared clipboard fallback chain.
	paneCapture   paneCaptureFunc
	paneClipboard paneClipboardFunc

	// Context-% based /clear for conductor sessions with clear_on_compact
	clearOnCompactSent map[string]time.Time // instanceID -> last /clear send time (debounce)
	// clearOnCompactDisarmed holds the last logged disarm reason per instance,
	// so the fail-closed gate says why once rather than every tick.
	clearOnCompactDisarmed map[string]string

	// File watcher for external changes (auto-reload)
	storageWatcher      *StorageWatcher
	sessionLoadSequence uint64 // issued only by the UI event loop

	// Optional in-memory web menu data sink for web mode.
	webMenuData   *menudata.MemoryMenuData
	webMenuDataMu sync.RWMutex

	// System theme watcher (active when theme="system"; nil otherwise)
	themeWatcher *ThemeWatcher

	// Storage warning (shown if storage initialization failed)
	storageWarning string

	// Watcher warning (shown if fsnotify may not work, e.g., on 9p/NFS)
	watcherWarning string

	// Update notification (async check on startup, periodic re-check)
	updateInfo      *update.UpdateInfo
	lastUpdateCheck time.Time
	// lastUpdateCheckFailed holds the periodic check back for
	// update.RecheckBackoff; updateCheckInFlight keeps it to one at a time.
	lastUpdateCheckFailed bool
	updateCheckInFlight   bool
	// updateNudgeDismissed suppresses the >5-releases-behind nudge for
	// the rest of the process. Reset on restart. Conductor task #45.
	updateNudgeDismissed bool
	// binaryWatch notices when the executable on disk is replaced by a
	// newer release while the TUI runs (see binary_watch.go). Nil when the
	// executable could not be resolved at startup.
	binaryWatch *binaryWatch
	// attachUpdate is the binary watch as the attach worker sees it, so an
	// attached deck can say an update waits (attached_update.go).
	attachUpdate attachUpdate
	// homebrewManaged is cached once at startup: a Homebrew binary is never
	// installed over by the TUI (brew owns it). See update_auto.go.
	homebrewManaged bool
	// autoInstallInFlight is the version an unattended install is running
	// for ("" when none); autoInstallAttempts is when each version was last
	// tried, so a failure is not retried every check.
	autoInstallInFlight string
	// autoInstallProgress is the remote-phase progress of that run (nil
	// when none); restartQueued is set by a restart key press that arrived
	// while it ran: the restart fires as soon as the run ends (restart.go).
	autoInstallProgress *update.UnattendedProgress
	restartQueued       bool
	autoInstallAttempts map[string]time.Time
	// restartWaitReason is why the last tick did not restart into the
	// newer build ("" when it could); restartOverdueReason is the same
	// once the wait passed restartOverdueAfter (banner + heartbeat), and
	// restartOverdueLoggedAt rate-limits tui_restart_overdue.
	restartWaitReason      string
	restartOverdueReason   string
	restartOverdueLoggedAt time.Time
	// autoRestartHoldReason is why the auto path is holding off (a target
	// that failed its dry run) until autoRestartHoldUntil.
	autoRestartHoldReason string
	// heartbeatDir is the cache dir the TUI heartbeat is written under
	// ("" disables it); heartbeatWrittenAt is the last write.
	heartbeatDir       string
	heartbeatWrittenAt time.Time
	// heartbeatFile is what the event loop and the attach worker share
	// (see heartbeat.go); attachedSince belongs to the worker goroutine.
	heartbeatFile heartbeatFile
	attachedSince time.Time
	// binaryOrphanReason is set while the executable this process started
	// from is gone or in the Trash: the deck cannot update or restart
	// itself then, says so in the banner, and both auto paths stay off.
	binaryOrphanReason string
	// binaryExecPath is os.Executable() at startup, kept even when the
	// file could not be fingerprinted then, so a later tick can start the
	// watch once the path is back.
	binaryExecPath string
	// autoInstallLastSkip is the last reason the periodic check left the
	// updater alone, so the log says it once per change, not per minute.
	autoInstallLastSkip string
	// updateTimerEnsureStarted is set once this TUI has asked for the
	// update timer to be installed or healed (#2472): once per process.
	updateTimerEnsureStarted bool
	// autoRestartLoggedAt rate-limits the "waiting for idle" log line.
	autoRestartLoggedAt time.Time
	// autoRestartHoldUntil pauses the auto path after the pre-arm check of
	// the new binary failed (see restartTargetProblem).
	autoRestartHoldUntil time.Time
	// autoUpdateSuppressedReason is non-empty when neither auto_install nor
	// auto_restart may act in this process (go test, CI, skip env, test
	// markers, no terminal; issue #2251). Set once in Init.
	autoUpdateSuppressedReason string
	// restartHandoff is what the previous process left in the environment
	// when it exec'd this one (restart.go); applied after the first load.
	restartHandoff RestartHandoff

	// Launching animation state (for newly created sessions)
	launchingSessions    map[string]time.Time        // sessionID -> creation time
	resumingSessions     map[string]time.Time        // sessionID -> resume time (for restart/resume)
	remoteRestarting     map[string]struct{}         // remote restart operation ID -> in flight
	remoteForking        map[string]struct{}         // remote fork operation ID -> in flight
	mcpLoadingSessions   map[string]time.Time        // sessionID -> MCP reload time
	forkingSessions      map[string]time.Time        // sessionID -> fork start time (fork in progress)
	setupRunningSessions map[string]time.Time        // sessionID -> setup script start time
	creatingSessions     map[string]*CreatingSession // tempID -> placeholder for worktree creation in progress
	animationFrame       int                         // Current frame for spinner animation

	// Context for cleanup
	ctx    context.Context
	cancel context.CancelFunc

	// Periodic log maintenance (prevents runaway log growth)
	lastLogMaintenance time.Time
	lastLogCheck       time.Time // Fast 10-second check for oversized logs

	// SQLite heartbeat: tracks when we last cleaned dead instances
	lastDeadInstanceCleanup time.Time

	// User activity tracking for adaptive status updates
	// PERFORMANCE: Only update statuses when user is actively interacting
	lastUserInputTime time.Time // When user last pressed a key

	// Double ESC to quit (#28) - for non-English keyboard users
	lastEscTime time.Time // When ESC was last pressed (double-tap within 500ms quits)

	// Mouse double-click tracking
	lastClickTime   time.Time // When left button was last pressed
	lastClickIndex  int       // flatItems index of last click (-1 = none)
	lastClickItemID string    // Session ID or group path at last click (guards against stale index)

	// Navigation tracking (PERFORMANCE: suspend background updates during rapid navigation)
	lastNavigationTime time.Time // When user last navigated (up/down/j/k)
	isNavigating       bool      // True if user is rapidly navigating
	lastAttachReturn   time.Time // When we returned from tea.Exec attach/detach
	navigationHotUntil atomic.Int64
	// Snapshot of status/tool used by render path to avoid per-row lock contention.
	sessionRenderSnapshot atomic.Value // map[string]sessionRenderState
	// Serializes snapshot publication with account gate transitions. Readers stay lock-free.
	sessionRenderSnapshotMu sync.Mutex

	// Jump mode (vimium-style hint navigation)
	jumpMode   bool   // True when jump mode is active
	jumpBuffer string // Characters typed so far in jump mode

	// Cached status counts (invalidated on instance changes)
	cachedStatusCounts struct {
		running, waiting, idle, stopped, errored int
		valid                                    atomic.Bool // THREAD-SAFE: accessed from main and worker goroutines
		timestamp                                time.Time   // For time-based expiration
	}

	// Status-transition tracker: emits enriched status_changed INFO,
	// flicker_detected WARN, and session_status_cascade INFO.
	// Lazy-initialized via getTransitionTracker().
	transitionTrackerOnce sync.Once
	transitionTracker     *transitionTracker

	// The watcher panel's feed: routed events and health states that the
	// watcher host's relay has already dispatched to conductor panes, so the
	// TUI only refreshes its panel from them (#2524). Nil until Init starts
	// the watcher host; quiet while another process owns the engine (#2530).
	watcherPanelEvents <-chan watcher.Event
	watcherPanelHealth <-chan watcher.HealthState
	// conductorDeliveries delivers watcher messages to conductor panes, one
	// at a time per conductor and in order (see conductorQueue). Created on
	// first use by deliveryQueue.
	conductorDeliveries     *conductorQueue
	conductorDeliveriesOnce sync.Once
	// watcherHealth remembers each watcher's last health status, so its
	// conductor hears about a warning or error once per transition (#2531).
	watcherHealth watcherHealthMemo

	// Full repaint mode: issue tea.ClearScreen every tick to avoid
	// incremental redraw drift in terminals with unicode grapheme widths
	fullRepaint          bool
	defaultFilter        string                  // from config.toml [display] default_filter
	activeFilterLabel    string                  // from config.toml [display] active_filter_label
	activeFilterExcludes map[session.Status]bool // from config.toml [display] active_filter_excludes; default {error}

	// activeFilterHideStopped is set by a second % press when the exclude set
	// keeps stopped sessions visible: Open then also hides stopped.
	activeFilterHideStopped bool

	// hiddenToolBadge is the effective default tool (default_tool, or claude
	// when unset) when [display] hide_default_tool_badge is on: rows running
	// it skip the tool badge. Empty means show every badge. Recomputed when
	// the settings panel saves.
	hiddenToolBadge string

	// showSessionTimestamps gates the dim "Nm ago" badge on each session row.
	// Cached here so all rows of a single frame see the same value even if
	// the user toggles the setting mid-frame. Reloaded after the panel saves.
	showSessionTimestamps bool

	// showPaneTitles, when true, renders the dim tmux pane-title (task
	// description) suffix on every session row instead of only the selected
	// one. Cached here so all rows of a frame agree; reloaded after panel save.
	showPaneTitles bool

	// accountSlotsConfigured mirrors len(session.ConfiguredAccountNames(cfg)) > 0,
	// the same gate the New/Edit Session dialogs use to hide their account rows
	// (#2152). A machine with no [profiles.<name>.claude].config_dir block has
	// one login, so "which slot is this session on" is not a question that can
	// have two answers — the inherited badge would be dead width on every row.
	// Atomic because refreshSessionRenderSnapshot reads it from the background
	// refresher goroutine while the settings panel writes it from the Bubble Tea
	// event loop. Explicit slots ignore this gate; see newAccountPresentation.
	accountSlotsConfigured atomic.Bool

	// Sessions/Preview split (issue #1092): percentage of width allocated to
	// preview pane. Loaded from config.toml [ui] preview_pct, adjustable
	// live via < and > keybindings, persisted back to config on adjustment.
	previewPct          int       // 10-90, default 65
	previewPctOverlayAt time.Time // when to hide the split overlay (zero = hidden)
	draggingDivider     bool      // true while the mouse is dragging the Sessions/Preview divider
	hoverDivider        bool      // true while the mouse rests on the divider's grab zone
	hoverDividerAt      time.Time // when hoverDivider was last confirmed by a mouse event

	// previewOrientation places the PREVIEW pane "right" (side-by-side) or
	// "below" (stacked) on wide terminals. Loaded from config.toml [ui]
	// preview_orientation, toggled live via the `O` keybinding, persisted
	// back to config on toggle.
	previewOrientation string

	// footerMode selects the bottom hint-bar style (config.toml [ui] footer).
	// One of session.FooterCurated (default), FooterFull, FooterCompact, or
	// FooterMinimal. Cached so every render of a frame agrees. Additive/opt-in:
	// it only changes WHAT the footer advertises, never a keybinding.
	footerMode           string
	healthWarningPending atomic.Pointer[string]
	healthWarningText    string
	healthWarningAt      time.Time

	// attachOnCreate, when true, makes creating a session via the new-session
	// dialog attach to the new session's pane immediately instead of only
	// moving the cursor to it (config.toml [ui] attach_on_create). Default
	// false: today's select-only behavior. See sessionCreatedMsg handling.
	attachOnCreate bool

	// viewModeOpts tunes the `t` view-mode partition from config.toml [ui]
	// (active_includes_idle, #2452). Zero value: today's split.
	viewModeOpts session.ViewModeOptions

	// Performance observability (debug mode only, zero cost when off)
	debugMode          bool         // true when AGENTDECK_DEBUG=1, enables perf overlay
	lastRenderDuration atomic.Int64 // microseconds, for debug status bar

	// Reusable string builder for View() to reduce allocations
	viewBuilder strings.Builder

	// Notification bar (tmux status-left for waiting sessions)
	notificationManager     *session.NotificationManager
	notificationsEnabled    bool
	manageTmuxNotifications bool
	boundKeys               map[string]string // Track which key is bound (key -> "sessionID:tmuxName")
	boundKeysMu             sync.Mutex        // Protects boundKeys for background worker access
	lastBarText             string            // Cache to avoid updating all sessions every tick
	lastBarTextMu           sync.Mutex        // Protects lastBarText for background worker access

	// Maintenance banner (shown after background maintenance completes)
	maintenanceMsg     string
	maintenanceMsgTime time.Time

	// navHintActive: true while the v1.7.60 group-navigation discoverability
	// hint is the current occupant of the maintenanceMsg slot. Dismissed on
	// the first keypress (handleMainKey).
	navHintActive bool

	// Cursor sync: track last notification bar switch during attach
	// When user switches sessions via Ctrl+b N while attached (tea.Exec),
	// we record the target session ID so cursor can follow after detach
	lastNotifSwitchID string
	lastNotifSwitchMu sync.Mutex

	// Undo delete stack (Chrome-style: Ctrl+Z restores in reverse order).
	// undoMu guards its mutations so a signal-driven exit can take what is
	// left (FinalizeUndoableDeletes); reads on the Update goroutine need no
	// lock, since only that goroutine writes.
	undoStack []deletedSessionEntry
	undoMu    sync.Mutex

	// Pending title changes: survives reload races.
	// When a rename save is skipped (isReloading=true), the title change is
	// stored here and re-applied after the reload completes. The lock state is
	// carried alongside the title: a user rename is locked (so the #572
	// Claude-name sync can't revert it to the cwd-folder default), while a
	// sync-sourced title stays unlocked so it keeps syncing. Storing only the
	// string lost that intent and left reapplied user renames unlocked (#697).
	pendingTitleChanges map[string]pendingTitle

	// Pending group operations: survive the save-abort → reload race.
	// Group create/rename/move persist via the non-force saveInstances(),
	// whose external-change guard aborts and triggers a reload when the on-disk
	// mtime is newer than our last load. The reload rebuilds groupTree from
	// disk, discarding the just-applied mutation. These are re-applied after
	// the reload (mirrors pendingTitleChanges for session renames).
	pendingGroupOps []pendingGroupOp

	// Duplicate-row warning throttle and dump-error latch (flat_items_dump.go).
	dupWarnKey    string
	dupWarnAt     time.Time
	dumpErrLogged bool

	// UI state persistence across restarts
	pendingCursorRestore *uiState // Consumed on first loadSessionsMsg to restore cursor
	uiStateSaveTicks     int      // Counter for periodic UI state saves in tick handler

	// Remote sessions (Phase 2: Agent-Deck Remotes)
	remoteSessions map[string][]session.RemoteSessionInfo // remoteName -> sessions
	// remoteGroups holds each remote's FULL group path list as reported by
	// its own state DB (`agent-deck group list --json`, fetched on the same
	// fleet poll). Unlike session-derived buckets it includes EMPTY groups,
	// so the M move dialog can still offer a remote folder after every
	// session has been moved out of it. Guarded by remoteSessionsMu.
	remoteGroups    map[string][]string  // remoteName -> group paths in the remote's own order (incl. empty groups)
	remoteFromCache map[string]bool      // remoteName -> data is a startup cache snapshot, not live yet
	remoteFetchedAt map[string]time.Time // remoteName -> when its sessions last came from a live fetch
	// remoteVersions is what each remote last reported for `agent-deck
	// version`, asked at most once per remoteVersionCheckInterval on the
	// session poll and seeded from the shared cache file (#2164). Guarded
	// by remoteSessionsMu.
	remoteVersions map[string]session.RemoteVersionState
	// remoteHostStats is each remote's most recent `system stats --json`
	// poll outcome (see remoteHostStatsResult). Unlike remoteVersions, it is
	// live-only: never persisted, and simply absent for a remote that has
	// not answered yet or cannot (older agent-deck). Guarded by
	// remoteSessionsMu.
	remoteHostStats    map[string]remoteHostStatsResult
	remoteSessionsMu   sync.RWMutex
	lastRemoteFetch    time.Time // When remote sessions were last fetched
	remotesFetchActive bool      // Prevents overlapping fetches
	// remoteFetchOutstanding counts the per-remote fetches of the running
	// round(s) that have not answered yet; remotesFetchActive clears when
	// the last one lands, so one wedged host cannot hold the others back
	// but the "refreshing…" trailer still tells the truth for the round.
	remoteFetchOutstanding int
	// remoteFetchSeq numbers every fleet fetch as it starts; the handler
	// applies a remote's result only if no newer fetch has been applied for
	// THAT remote already, so a slow periodic poll cannot overwrite the
	// refresh a delete, move or archive just triggered (a stale snapshot
	// would resurrect the row). Generations are tracked per remote because
	// each remote's result now arrives on its own: a newer answer from a
	// fast host must not make a slower host's still-valid answer look stale.
	remoteFetchSeq     uint64
	remoteFetchApplied map[string]uint64 // remoteName -> last applied generation
	// remoteGroupsGen records which generation each remote's held group
	// list came from. A poll result superseded by a push still carries the
	// remote's cost summary and group list, which the push may not have;
	// costs always apply, and the group list applies unless a newer message
	// already brought one.
	remoteGroupsGen map[string]uint64
	// remoteActionSettled is when each remote last confirmed a user action
	// (delete, close, archive, restart, fork) and remoteActionRemoved lists
	// the sessions those actions removed and when. For a short grace after
	// a confirmation a push from that remote is treated as suspect: the
	// agent's probe may have listed before the action ran, and applying it
	// would resurrect the row the user just watched disappear (finding 10).
	remoteActionSettled map[string]time.Time
	remoteActionRemoved map[string]map[string]remoteActionRecord
	// remoteHeaderCounts holds the session and status counts of every remote
	// header row, keyed by Item.Path and rebuilt with the rows, so a frame
	// does not rescan every remote session for every visible header.
	remoteHeaderCounts map[string]remoteHeaderCount
	// Remote snapshot save debounce (see flushRemoteSessionsCache). Guarded
	// by remoteSessionsMu.
	remoteCacheDirty    bool
	remoteCacheLastSave time.Time
	remoteCacheLastHash uint64
	// newRemoteFetchRunner builds the runner one per-remote fetch talks to.
	// nil means a real SSHRunner; tests inject stubs to prove delivery
	// timing without ssh.
	newRemoteFetchRunner func(name string, rc session.RemoteConfig) remoteFetchRunner
	// remoteActionStarted records when each remote action was requested
	// (keyed by remote, verb and target) so its footer line can report the
	// time from keypress to the remote's confirmation, the number that
	// tells whether a remote deck feels local.
	remoteActionStarted map[string]time.Time
	// remoteRefetchWanted is set when a remote pushed "changed" while a
	// fetch was already in flight: that fetch may predate the change, so
	// another one starts as soon as it lands.
	remoteRefetchWanted bool
	// remoteConfigured is the set of remote names the last successfully
	// read config listed (nil before the first read). A push from a name
	// outside it is from a remote that was removed from the config and is
	// dropped; every name in it other than the pusher is kept as is when a
	// push is applied. Guarded by remoteSessionsMu.
	remoteConfigured map[string]struct{}
	remotePolls      map[string]session.RemotePollState // guarded by remoteSessionsMu
	remotePollMu     sync.Mutex                         // serializes poll cache reads/writes without holding the render lock
	remotePollConfig map[string]session.RemoteConfig
	remotePollActive map[string]bool // one session poll per remote, across overlapping rounds
	// remotePollBackoffs holds a host whose last poll was slow or failed out
	// of automatic polling for an exponentially growing while
	// (remote_poll_backoff.go). Guarded by remoteSessionsMu.
	remotePollBackoffs map[string]remotePollBackoff
	// remoteNow is the clock poll timing and backoff read; nil means
	// time.Now. Tests inject a fake one.
	remoteNow func() time.Time
	// remoteMutationStamp returns the stamp (remote DB mtime, unix ns) of
	// the last mutating command confirmed over a remote's channel, 0 when
	// unknown. nil means ask the channel; tests inject a stub.
	remoteMutationStamp func(remoteName string) int64
	// remotePending marks remote session rows with an action underway
	// (sessionID -> "deleting…"), drawn on the row so the screen says what
	// is happening while the remote answers instead of freezing.
	remotePending map[string]string
	// remoteSessionRefreshSec is the poll cadence (seconds) for re-fetching
	// the remote session list, resolved once at construction from
	// [ui] remote_session_refresh_secs. Issue #1170.
	remoteSessionRefreshSec int

	// Remote latency (issue #1103) — measured per remote host on the same
	// cadence as CPU/RAM (see UISettings.GetRemoteLatencyRefreshSecs).
	remoteLatency           map[string]session.RemoteLatency
	remoteLatencyMu         sync.RWMutex
	lastRemoteLatencyFetch  time.Time
	remoteLatencyFetchBusy  bool
	remoteLatencyRefreshSec int // resolved once at construction
	// #1101: remote cost summaries fetched alongside session listings so the
	// status-line cost segment reflects spend on every configured remote, not
	// just events written to the local cost_events table.
	remoteCosts   map[string]*costs.RemoteCostSummary // remoteName -> summary
	remoteCostsMu sync.RWMutex

	// Claim polling ([performance] claim_polling): ownership gating for
	// background work in multi-instance setups.
	claimPolling  bool
	ownedMu       sync.RWMutex
	ownedSessions map[string]bool
	// orphanPolled holds sessions the primary is slow-polling this sweep
	// because no scoped instance claims them (nil when no orphan sweep is
	// due). Deliberately separate from ownedSessions: orphans must never be
	// claimed, pinned to pipes, idle-watched, or revived, and must never
	// poison the next sweep's prevOwned snapshot. Replaced wholesale
	// (copy-on-write) under ownedMu, same discipline as ownedSessions.
	orphanPolled    map[string]bool
	groupScopeMu    sync.RWMutex // Guards groupScope for cross-goroutine read in reconcileClaims
	lastOrphanSweep time.Time    // last time the primary polled for orphaned sessions
	// Cost tracking
	costStore            *costs.Store
	costPricer           *costs.Pricer
	costBudget           *costs.BudgetChecker
	costToday            atomic.Int64 // microdollars
	costYesterday        atomic.Int64 // microdollars
	costWeek             atomic.Int64 // microdollars
	costLastWeek         atomic.Int64 // microdollars
	costThisMonth        atomic.Int64 // microdollars
	costLastMonth        atomic.Int64 // microdollars
	costProjected        atomic.Int64 // microdollars
	costRefreshTime      time.Time
	costLineTemplate     string // resolved at construction; see session.ResolveCostLineTemplate
	costLineHideWhenZero bool
	showCostDashboard    bool
	costDashboard        costDashboard

	// System stats collector (CPU, RAM, disk, etc.)
	sysStatsCollector *sysinfo.Collector
	sysStatsConfig    session.SystemStatsSettings

	// sshCollector backs the opt-in "ssh" header field (who is connected to
	// this host over SSH); nil unless [ui.header].fields lists it.
	sshCollector *sysinfo.SSHCollector

	// accountsUsageCache backs the "accounts" header field: an mtime-cached
	// reader over this host's own quota files (see session.AccountUsageCache)
	// so the header render path, called every draw, re-parses a slot's usage
	// JSON only when it actually changed on disk.
	accountsUsageCache *session.AccountUsageCache

	// Interval-hook runner: user-configured shell commands run on a wall-clock
	// cadence ([interval_hooks] in config.toml). nil when none are configured.
	intervalHookRunner *intervalhook.Runner

	// Insert mode (#1069, feature 1): vim-style modal type-through. When
	// active, printable runes, Space, and Enter are routed directly to the
	// focused session's tmux pane instead of being interpreted as TUI
	// commands. Toggled with `I` (enter) and `Esc` (exit).
	insertMode          bool
	embeddedMode        bool
	insertModeSessionID string
	// Full-fidelity embedded session state. Production wires sessionInput and
	// sessionOutput from main; package tests that omit them keep exercising the
	// legacy capture/send-keys path until their focused seam is migrated.
	sessionInput       *SessionInputRouter
	sessionOutput      *SessionOutput
	embeddedTerminal   *embeddedTerminal
	embeddedRequest    terminal.AttachRequest
	embeddedGeneration uint64
	embeddedInput      *sessionInputQueue
	// embeddedAppliedRect is the pane rectangle the child PTY was last sized
	// to. Dashboard chrome (update nudge, maintenance banner) changes the pane
	// without a WindowSizeMsg, so geometry is re-derived on those events and
	// on the embedded refresh tick, and only a changed rectangle reaches the
	// PTY as a resize.
	embeddedAppliedRect terminalCellRect
	// embeddedSidebarHidden maximizes the PTY inside the dashboard while
	// preserving the previous sidebar width for the next restore.
	embeddedSidebarHidden bool
	// insertKeySink is an optional override used by tests to capture keys
	// without running real tmux. When nil, keys are sent via the session's
	// tmux pane (SendKeys / SendEnter).
	insertKeySink func(inst *session.Instance, text string, sendEnter bool) error
	// insertNamedKeySink is the test override for forwarded named keys
	// (Backspace, arrows, Tab, Ctrl-C, Ctrl-D — #1094). When nil, named keys
	// are sent via the session's tmux pane (SendNamedKey).
	insertNamedKeySink func(inst *session.Instance, key string) error
	// remoteCreateSink is an optional override used by tests to capture what
	// the new-session dialog forwards to the remote-create path (#1353) without
	// opening an SSH connection. When nil, createRemoteSessionWithOptions runs.
	remoteCreateSink             func(remoteName string, opts session.RemoteAddOptions) tea.Cmd
	remoteCreationCatalogFetcher func(remoteName string) tea.Cmd
	// remoteAccountsGen numbers each opening of the remote new-session dialog;
	// an account or MCP fetch answers for the opening that requested it and
	// is dropped otherwise, so a slow answer for an earlier opening can never
	// replace the list the user is choosing from now.
	remoteAccountsGen uint64
	// pendingRemoteCreate holds the remote create whose path the server
	// reported missing, while the create-directory confirmation is open.
	pendingRemoteCreate *remoteCreateDirNeededMsg
	// insertKeySender is the persistent dispatch path opened on
	// enterInsertMode and closed on exitInsertMode (#1102 perf fix +
	// remote support). Local sessions get a tmux.KeySender (control-mode
	// client, no per-keystroke fork+exec); remote sessions get a
	// session.RemoteKeySender (SSH RPC to the remote agent-deck). When
	// insertKeySink/insertNamedKeySink are set (test mode) they win;
	// when neither is set, dispatch falls back to per-call SendKeys
	// (the legacy path, ~50× slower but unconditional).
	insertKeySender insertKeySender
	// insertOpenKeySender creates a persistent KeySender for the given
	// insert target. Defaulted to the production opener at construction
	// time; tests override to inject a mock without real tmux/SSH.
	insertOpenKeySender func(target insertTargetRef) (insertKeySender, error)
	// insertModeRemoteName / insertModeRemoteID identify the remote
	// agent-deck and session ID when insert mode targets a remote session
	// (ItemTypeRemoteSession). Empty for local sessions, which use
	// insertModeSessionID instead.
	insertModeRemoteName string
	insertModeRemoteID   string
	sessionExists        func(*session.Instance) bool

	// Insert-mode keystroke batching (#1094). Per-keystroke tmux send-keys
	// invocations are too slow when typing fast. Runes are accumulated in
	// insertBuf and flushed together after insertBatchDuration, or
	// immediately on Enter / Esc / a named key. insertBatchDuration <= 0
	// disables batching (each rune flushes synchronously) and is used by
	// tests that want to assert call counts deterministically.
	insertBuf           strings.Builder
	insertFlushPending  bool
	insertBatchDuration time.Duration
	// insertTool and insertTypedChars feed send.daily (via=tui): the
	// target's tool and how many characters were typed since the last
	// Enter. Only the length bucket is ever recorded, never the text.
	insertTool       string
	insertTypedChars int
	// insertPreviewRefreshPending guards the fast preview-refresh tick armed
	// after an insert keystroke (#1131). Only one tick is in flight at a time;
	// see scheduleInsertPreviewRefresh.
	insertPreviewRefreshPending bool
	embeddedRefreshPending      bool
	// openInNewWindowSink is an optional override used by tests to capture
	// Shift+Enter dispatches without spawning a real iTerm2 window. When
	// nil, the dispatch calls terminal.OpenSessionInNewWindow directly.
	// See issue #1093.
	openInNewWindowSink func(req terminal.AttachRequest) error
	// openInSplitPaneSink is an optional override used by tests to capture
	// open_shell_here dispatches without spawning a real iTerm2 split pane.
	// When nil, the dispatch calls terminal.OpenSessionInSplitPane directly.
	// See issue #1470.
	openInSplitPaneSink func(req terminal.AttachRequest) error
	// quickApproveSink is an optional override used by tests to capture the
	// quick-approve (`a`) dispatch — the (instance, windowIndex) it would send
	// "1"+Enter to — without driving real tmux. windowIndex < 0 means the
	// session's active window; >= 0 targets that specific window. When nil, the
	// dispatch calls the tmux session directly. See issue #1369.
	quickApproveSink func(inst *session.Instance, windowIndex int) error
}

// reloadState preserves UI state during storage reload
type reloadState struct {
	cursorSessionID  string          // ID of session at cursor (if cursor on session)
	cursorGroupPath  string          // Path of group at cursor (if cursor on group)
	cursorCreatingID string          // tempID of creating placeholder at cursor (if cursor on placeholder)
	expandedGroups   map[string]bool // Expanded group paths
	viewOffset       int             // Scroll position
}

// uiState persists cursor, preview mode, and status filter across restarts
type uiState struct {
	CursorSessionID         string `json:"cursor_session_id,omitempty"`
	CursorGroupPath         string `json:"cursor_group_path,omitempty"`
	PreviewMode             int    `json:"preview_mode"`
	StatusFilter            string `json:"status_filter,omitempty"`
	ActiveFilterHideStopped bool   `json:"active_filter_hide_stopped,omitempty"`
	GroupViewMode           int    `json:"group_view_mode,omitempty"`
	TimeFilterMode          int    `json:"time_filter_mode,omitempty"`
	// Collapsed remote headers, keyed like Item.Path. Local group folds live in
	// groupTree, which is persisted separately by saveGroupState; remote groups
	// are synthetic UI rows and have no home there.
	RemoteGroupsCollapsed []string `json:"remote_groups_collapsed,omitempty"`

	// Folded parent-child lists (#2631), as parent session IDs. Local group
	// folds live in groupTree (persisted by saveGroupState) and window folds
	// are session-scoped UI state that is not persisted at all; a parent fold is
	// a view preference like the remote folds above, so it rides here.
	ParentChildrenCollapsed []string `json:"parent_children_collapsed,omitempty"`

	// RemoteSessionOrder is the manual order of remote session rows (#1875):
	// remote -> group path -> session IDs. It rides in ui_state because it is
	// a view preference of this machine, exactly like the preview mode and
	// status filter above. Nested rather than flat-keyed so a remote name or
	// group path containing "/" cannot alias another bucket (see remoteOrder).
	RemoteSessionOrder map[string]map[string][]string `json:"remote_session_order,omitempty"`

	SidebarMode            string `json:"sidebar_mode,omitempty"`
	SidebarWidthCustomized bool   `json:"sidebar_width_customized,omitempty"`
}

type selectedItemIdentity struct {
	groupPath       string
	sessionID       string
	creatingID      string
	windowSessionID string
	windowIndex     int
	remoteName      string
	remoteSessionID string
	remoteGroupPath string
}

func (h *Home) saveToolVisibilityConfig() error {
	if h.toolVisibilityPanel == nil {
		return nil
	}
	cfg, err := session.LoadUserConfig()
	if err != nil {
		return err
	}
	if cfg == nil {
		cfg = &session.UserConfig{}
	}
	merged := *cfg
	merged.UI.HiddenTools = h.toolVisibilityPanel.HiddenTools()
	return session.SaveUserConfig(&merged)
}

func (h *Home) reloadHotkeysFromConfig() {
	h.setHotkeys(resolveHotkeys(session.GetHotkeyOverrides()))
}

func (h *Home) detachByte() byte {
	return ResolvedDetachByte(session.GetHotkeyOverrides())
}

// openScrollbackOnPageUp is the alternate-screen gate decision for a bare
// PageUp: open the pager on a normal-screen pane (real tmux scrollback exists),
// pass PageUp through on an alternate-screen pane (a full-screen app such as
// Claude fullscreen scrolls itself and keeps no tmux history), and preserve the
// pager on a query error rather than silently disabling a configured feature.
func openScrollbackOnPageUp(alt bool, err error) bool {
	return err != nil || !alt
}

// attachOptions resolves the detach key plus the in-attach session-switcher
// key for the current hotkey configuration. The detach key always wins: a
// switch byte that collides with it is dropped so it can never shadow detach.
// sess is the tmux session being attached; it is used to gate the bare-PageUp
// scrollback trigger so a full-screen app keeps its own PageUp (#1491 fix).
func (h *Home) attachOptions(sess *tmux.Session) tmux.AttachOptions {
	overrides := session.GetHotkeyOverrides()
	detach := ResolvedDetachByte(overrides)
	switchByte := ResolvedSwitchByte(overrides)
	if switchByte == detach {
		switchByte = 0
	}
	// Resolve the in-attach scrollback trigger (#1491). A control-byte trigger
	// that collides with the detach or switch key is dropped so it can never
	// shadow them; the PageUp trigger is independent and always safe.
	scroll := ResolvedScrollbackTrigger(overrides)
	scrollByte := scroll.KeyByte
	if scrollByte == detach || (switchByte != 0 && scrollByte == switchByte) {
		scrollByte = 0
	}
	opts := tmux.AttachOptions{
		DetachByte:         detach,
		SwitchKeyByte:      switchByte,
		ScrollbackKeyByte:  scrollByte,
		ScrollbackOnPageUp: scroll.OnPageUp,
	}
	// Gate the bare-PageUp trigger on the pane's screen state: when the attached
	// app is in the alternate screen (Claude fullscreen), leave PageUp for the
	// app instead of opening an empty pager over its (non-existent) tmux history.
	// Only wired for the PageUp trigger — an explicit control-byte chord is a
	// deliberate opt-in and is never gated. The gate is consulted at most once
	// per PageUp press (see AttachOptions.ScrollbackGate).
	if scroll.OnPageUp && sess != nil {
		opts.ScrollbackGate = func() bool { return openScrollbackOnPageUp(sess.IsAltScreen()) }
	}
	return opts
}

// withSSHPasteHint arms the TUI attach's image-paste hint (ASK-92): over
// plain ssh, Ctrl+V with no image here says once where images can be pasted
// from, through the live attach's notice sink (tmux.ShowAttachNotice). Only
// this attach sets OnCtrlV; the CLI attach a remote attach runs on the box
// (#2775) never does.
func withSSHPasteHint(opts tmux.AttachOptions, tool string, pane func() string, notice func(string)) tmux.AttachOptions {
	if hint := session.NewSSHPasteHint(tool, pane, notice); hint != nil {
		opts.OnCtrlV = hint.CtrlV
	}
	return opts
}

func (h *Home) setHotkeys(bindings map[string]string) {
	if bindings == nil {
		bindings = resolveHotkeys(nil)
	}
	h.hotkeys = bindings
	h.hotkeyLookup, h.blockedHotkeys = buildHotkeyLookup(bindings)
	if h.helpOverlay != nil {
		h.helpOverlay.SetHotkeys(bindings)
	}
	h.syncStatusHints(bindings)
}

// syncStatusHints pushes the resolved detach/switch key labels into the tmux
// package so the attached status bar reflects the user's [hotkeys] config
// instead of the hardcoded "ctrl+q"/"ctrl+s". The switch hint mirrors
// attachOptions: it is suppressed when the switch key has no portable control
// byte or collides with detach, since the attach loop drops it in those cases.
func (h *Home) syncStatusHints(bindings map[string]string) {
	detachByte := DetachByteFromBinding(actionHotkey(bindings, hotkeyDetach))
	switchByte := ctrlByteFromBinding(actionHotkey(bindings, hotkeySwitchSession))
	switchEnabled := switchByte != 0 && switchByte != detachByte
	tmux.SetStatusHints(
		strings.ToLower(DetachByteLabel(detachByte)),
		strings.ToLower(DetachByteLabel(switchByte)),
		switchEnabled,
	)
}

// openInNewWindow dispatches the Shift+Enter new-window launch through an
// optional test sink, or falls back to the real terminal launcher.
//
// The sessionExists flag short-circuits the real launcher for dead sessions
// — opening a fresh iTerm2 window only to land on a "tmux: no such session"
// error is worse UX than a silent no-op. The sink path skips this guard so
// tests can pin the dispatch without faking tmux state. Issue #1093.
func (h *Home) openInNewWindow(req terminal.AttachRequest, sessionExists bool) error {
	if h.openInNewWindowSink != nil {
		return h.openInNewWindowSink(req)
	}
	if !sessionExists {
		return nil
	}
	if req.Remote == nil {
		// The new window is one more viewer of a possibly shared session
		// (internal/tmux sharedview.go), with the user's [tmux.options]
		// honoured as on every other attach path; a remote one gets this
		// from its own `session attach`.
		tmux.ApplySharedViewSize(req.SocketName, req.Name, session.SharedViewOverrides())
	}
	return terminal.OpenSessionInNewWindow(req)
}

// quickApprove delivers "1"+Enter to approve a Claude permission prompt without
// attaching. windowIndex < 0 targets the session's active window (the
// session-row path); >= 0 targets that specific tmux window (the window-row
// path, #1369). Routed through quickApproveSink in tests. A nil instance or
// absent tmux session is a silent no-op.
func (h *Home) quickApprove(inst *session.Instance, windowIndex int) {
	if inst == nil {
		return
	}
	if h.quickApproveSink != nil {
		_ = h.quickApproveSink(inst, windowIndex)
		return
	}
	tmuxSess := inst.GetTmuxSession()
	if tmuxSess == nil {
		return
	}
	if windowIndex < 0 {
		_ = tmuxSess.SendKeysAndEnter("1")
		return
	}
	_ = tmuxSess.SendKeysAndEnterToWindow(windowIndex, "1")
}

// openPromptInput opens the inline one-line prompt input bound to inst (#1410).
// The prompt is delivered to the session's live tmux pane on submit, so a
// session that isn't running is rejected up front with a clear message rather
// than silently dropping the prompt.
func (h *Home) openPromptInput(inst *session.Instance) {
	if inst == nil {
		return
	}
	if ts := inst.GetTmuxSession(); ts == nil || ts.TmuxName() == "" {
		h.setError(fmt.Errorf("session %q is not running; start it before prompting", inst.Title))
		return
	}
	if err := inst.PromptDeliveryError(); err != nil {
		h.setError(err)
		return
	}
	h.promptInputDialog.SetSize(h.width, h.height)
	h.promptInputDialog.Show(inst.ID, inst.Title)
	h.syncViewport()
}

// resolveITermOpenAs reads the [ui] iterm_open_as setting from the user
// config, returning "tab" by default if the config can't be loaded or
// the value is unset/unknown. Issue #1100.
func resolveITermOpenAs() string {
	cfg, err := session.LoadUserConfig()
	if err != nil || cfg == nil {
		return session.DefaultITermOpenAs
	}
	return cfg.UI.GetITermOpenAs()
}

// openInSplitPane dispatches the open_shell_here iTerm2 split pane launch
// through an optional test sink, or falls back to the real terminal launcher.
// Issue #1470.
func (h *Home) openInSplitPane(req terminal.AttachRequest) error {
	if h.openInSplitPaneSink != nil {
		return h.openInSplitPaneSink(req)
	}
	return terminal.OpenSessionInSplitPane(req)
}

// resolveShellSplitMode returns session.ShellSplitITerm when an iTerm2 split
// should be used, session.ShellSplitWindow for a tmux window (tab), and
// session.ShellSplitTmux otherwise. Reads [ui].shell_split first; falls back
// to auto-detection via TERM_PROGRAM / LC_TERMINAL. Issue #1470.
func resolveShellSplitMode() string {
	cfg, _ := session.LoadUserConfig()
	if cfg != nil {
		mode := cfg.UI.GetShellSplit()
		if mode == session.ShellSplitITerm || mode == session.ShellSplitTmux || mode == session.ShellSplitWindow {
			return mode
		}
	}
	if os.Getenv("LC_TERMINAL") == "iTerm2" || os.Getenv("TERM_PROGRAM") == "iTerm.app" {
		return session.ShellSplitITerm
	}
	return session.ShellSplitTmux
}

// openShellHere adds a vertical shell pane to the focused session's tmux
// session (split-window -h), then opens the session in an iTerm2 split pane
// or attaches inline depending on resolveShellSplitMode. The shell lands
// in the session's worktree (or project path), so the user sees [agent | shell]
// side-by-side without detaching from agent-deck. Issue #1470.
func (h *Home) openShellHere(inst *session.Instance) tea.Cmd {
	tmuxSess := inst.GetTmuxSession()
	if tmuxSess == nil {
		return nil
	}
	workdir := inst.WorktreePath
	if workdir == "" {
		workdir = inst.ProjectPath
	}
	req := terminal.AttachRequest{
		Name:       tmuxSess.Name,
		SocketName: tmuxSess.SocketName,
	}
	mode := resolveShellSplitMode()
	if mode == session.ShellSplitWindow {
		// Shell as a tmux window (tab) instead of a split pane; attach so
		// the new window is visible immediately.
		if err := tmuxSess.NewShellWindow(workdir); err != nil {
			h.setError(fmt.Errorf("open shell here: %w", err))
			return nil
		}
		return h.attachSession(inst)
	}
	if mode == session.ShellSplitITerm {
		// Launch iTerm2 split before mutating tmux so a failed osascript
		// call does not leave an orphaned pane. Issue #1470.
		if err := h.openInSplitPane(req); err != nil {
			h.setError(fmt.Errorf("open shell here: iterm split: %w", err))
			return nil
		}
		if err := tmuxSess.SplitShellPane(workdir); err != nil {
			h.setError(fmt.Errorf("open shell here: %w", err))
		}
		return nil
	}
	// Default (tmux): split first, then attach so the split is visible.
	if err := tmuxSess.SplitShellPane(workdir); err != nil {
		h.setError(fmt.Errorf("open shell here: %w", err))
		return nil
	}
	return h.attachSession(inst)
}

// buildRemoteAttachRequest constructs a terminal.AttachRequest that
// runs `agent-deck session attach <id>` over SSH on the named remote.
// Returns ok=false when the remote can't be resolved from user config or
// is missing a host. Issue #1100.
func buildRemoteAttachRequest(remoteName, sessionID, openAs string) (terminal.AttachRequest, bool) {
	if remoteName == "" || sessionID == "" {
		return terminal.AttachRequest{}, false
	}
	cfg, err := session.LoadUserConfig()
	if err != nil || cfg == nil || cfg.Remotes == nil {
		return terminal.AttachRequest{}, false
	}
	rc, ok := cfg.Remotes[remoteName]
	if !ok || rc.Host == "" {
		return terminal.AttachRequest{}, false
	}
	// A mistyped transport must not quietly fall back to ssh.
	if t := rc.GetTransport(); t != session.RemoteTransportSSH && t != session.RemoteTransportMosh {
		return terminal.AttachRequest{}, false
	}
	return terminal.AttachRequest{
		Name:   sessionID,
		OpenAs: openAs,
		Remote: &terminal.RemoteAttach{
			Host:          rc.Host,
			AgentDeckPath: rc.GetAgentDeckPath(),
			Profile:       rc.GetProfile(),
			Transport:     rc.GetTransport(),
			MoshServer:    rc.MoshServer,
		},
	}, true
}

// openSwitcherMsg is emitted when the user pressed the session-switch key while
// attached. It carries the same post-attach reconciliation data as
// statusUpdateMsg; the switcher always opens pre-highlighted on the session we
// came from.
type openSwitcherMsg struct {
	fromSessionID   string // session we just detached from
	attachedWorkDir string // pane_current_path captured after attach returns
}

// openScrollbackMsg is emitted when the user pressed the scrollback trigger
// while attached (#1491). It carries the same post-attach reconciliation data
// as statusUpdateMsg; the pager opens bound to the session we came from and the
// history capture runs asynchronously.
type openScrollbackMsg struct {
	fromSessionID   string // session we just detached from
	attachedWorkDir string // pane_current_path captured after attach returns
}

// scrollbackContentMsg carries the captured pane history back to the pager. It
// is stale-guarded by sessionID so a capture that completes after the user has
// closed or re-opened the pager on a different session is ignored.
type scrollbackContentMsg struct {
	sessionID string
	content   string
	err       error
}

// switcherCommitMsg fires after the switcher has been idle for switcherIdleCommit.
// gen guards against stale timers: only a message whose gen matches the
// switcher's current generation commits (see handleSwitcherCommit).
type switcherCommitMsg struct {
	gen int
}

type attachReturnRefreshMsg struct{}

// attachReturnSyncedMsg lands when the attach-return reconciliation that used to
// run inline on the Bubble Tea event loop (tmux cache refresh + a forced status
// check for the session we just left) has finished on its own goroutine. The
// handler only re-derives the list rows from the already-updated snapshot, which
// is pure in-memory work. See attachReturnSyncCmd for why the split exists.
type attachReturnSyncedMsg struct{}

// NOTE: syncNotifications (foreground) was removed in v0.9.2 as a CPU optimization.
// All notification sync is now handled by syncNotificationsBackground() which runs
// every 2s in the background worker, including during tea.Exec pauses.

// getAttachedSessionID returns the instance ID of the currently attached agentdeck session.
// This detects which session the user is viewing, even if they switched via tmux directly.
func (h *Home) getAttachedSessionID() string {
	attachedSessions, err := tmux.GetAttachedSessions()
	if err != nil || len(attachedSessions) == 0 {
		return ""
	}

	h.instancesMu.RLock()
	defer h.instancesMu.RUnlock()

	// Find the first attached agentdeck session
	for _, sessName := range attachedSessions {
		for _, inst := range h.instances {
			if ts := inst.GetTmuxSession(); ts != nil && ts.TmuxName() == sessName {
				return inst.ID
			}
		}
	}
	return ""
}

// setError sets an error with timestamp for auto-dismiss
func (h *Home) setError(err error) {
	h.err = err
	if err != nil {
		h.errTime = time.Now()
		// Footer errors are otherwise invisible in debug.log, making
		// post-hoc diagnosis of failed UI actions impossible.
		uiLog.Error("ui_error", slog.String("err", err.Error()))
	}
}

// clearError clears the current error
func (h *Home) clearError() {
	h.err = nil
	h.errTime = time.Time{}
}

const (
	remotePreviewMaxLines = 200
	remotePreviewMaxBytes = 16 * 1024
)

// getSelectedSession returns the currently selected session, or nil if a group is selected
func (h *Home) getSelectedSession() *session.Instance {
	if len(h.flatItems) == 0 || h.cursor >= len(h.flatItems) {
		return nil
	}
	item := h.flatItems[h.cursor]
	if item.Type == session.ItemTypeSession {
		return item.Session
	}
	if item.Type == session.ItemTypeWindow {
		return h.getInstanceByID(item.WindowSessionID)
	}
	return nil
}

func (h *Home) beginAttachReturnGrace(now time.Time) {
	h.lastAttachReturn = now
	h.lastNavigationTime = now
	h.isNavigating = true
	h.navigationHotUntil.Store(now.Add(attachReturnHotDuration).UnixNano())
}

// Status-sweep cadence (issue #1366). The sweep normally runs every
// baseStatusInterval. When a sweep overruns that interval — which happens at
// large session counts when the tmux control-mode pipe is unavailable/degraded
// and capture-pane falls back to subprocesses — sweeps would otherwise pile up
// and pin the tmux server. nextStatusInterval backs the cadence off so the
// gap between sweeps is at least twice the last sweep's duration (≈50% duty
// cycle), capped at maxStatusInterval. Fast sweeps keep the base cadence, so
// there is no behaviour change in the common (piped) case.
const (
	baseStatusInterval = 2 * time.Second
	maxStatusInterval  = 10 * time.Second
)

// backgroundStatusUpdate runs independently of the TUI
// Updates session statuses and syncs notification bar directly to tmux
// This is called by the internal ticker even when TUI is paused (tea.Exec)
func (h *Home) backgroundStatusUpdate() {
	defer func() {
		if r := recover(); r != nil {
			notifLog.Error("background_update_panic", slog.Any("panic", r))
		}
	}()

	// Heartbeat first, before any early return below (navigation hot window,
	// tmux server down, no sessions): it is how other instances see this one
	// as alive, and a primary whose heartbeat goes stale for 30s loses the
	// allow_multiple=false gate to a second TUI. One UPDATE per tick.
	if db := statedb.GetGlobal(); db != nil {
		_ = db.Heartbeat()
	}

	totalStart := time.Now()
	if hotUntil := h.navigationHotUntil.Load(); hotUntil > 0 && time.Now().UnixNano() < hotUntil {
		return
	}

	// Get instances snapshot
	h.instancesMu.RLock()
	instances := make([]*session.Instance, len(h.instances))
	copy(instances, h.instances)
	h.instancesMu.RUnlock()

	// Charged omits RunUncounted work: the detach reconciliation and the
	// per-client status-bar refresh. Those scale with viewers (a web
	// terminal is another tmux client), not with the session count, and
	// counting them is what flashed the footer on Ctrl+Q.
	tmuxBefore := tmux.SnapshotStarts()
	defer func() {
		elapsed := time.Since(totalStart)
		calls := tmuxBefore.Charged()
		// session_count must agree with list --json's tracked set (the same
		// filter countByStatus/countSessionStatuses/renderGroupPreview use),
		// not the raw snapshot: archived-cross-harness sources superseded by
		// a "Restart with new session ID" target otherwise inflate the
		// health/doctor sample even though status/list already agree.
		visibleCount := len(session.VisibleInstances(instances))
		health.RecordStatusPass(elapsed, visibleCount, calls)
		optiming.Since("tui.status_pass", totalStart)
		if health.Enabled() {
			h.queueHealthWarning(elapsed, visibleCount, calls)
		}
	}()

	// Claim reconciliation: decide which sessions THIS instance polls. This
	// runs BEFORE the tmux-alive and empty-instances early returns below:
	// claims lifecycle (heartbeats, orphan sweep, primary election) is
	// DB-only and must not freeze just because tmux is down or the instance
	// list is briefly empty.
	h.reconcileClaims(instances)

	// Fast-fail: skip entire status loop when tmux server is dead.
	// Without this, every subprocess call takes ~3s to fail, causing 30-50s UI freezes.
	if !tmux.IsServerAlive() {
		return
	}

	// Track this tick with the slow-op detector (warns if stuck >3s)
	if sod := logging.SlowOps(); sod != nil {
		opID := sod.Start("background_status_update")
		defer sod.Finish(opID)
	}

	// Refresh tmux session cache
	refreshStart := time.Now()
	tmux.RefreshExistingSessions()
	tmux.RefreshPaneInfoCache()
	refreshDur := time.Since(refreshStart)
	if refreshDur > 100*time.Millisecond {
		perfLog.Warn("slow_refresh", slog.Duration("duration", refreshDur))
	}

	if len(instances) == 0 {
		return
	}

	// Active (non-archived) subset, computed once and reused by the loops that
	// only concern live sessions. Archived sessions have torn-down panes, so
	// walking them here only burns tmux subprocesses (Exists() / Capture())
	// without changing anything the UI shows — with a large archive backlog that
	// was the dominant cost in this sweep. Loops that need the full set (status
	// skip-counting, idle lastSeen cleanup) keep using `instances`.
	activeInstances := session.FilterInstancesByArchive(instances, false)

	// Issue #1143: rate-limit the idle-timeout watcher to one tick per minute.
	// The background sweep runs every 2s; capture-pane on every session every
	// 2s would add unnecessary tmux load. 60s is the same cadence the spec
	// suggests and matches how the lifecycle log surfaces dormant workers.
	if h.idleTimeoutWatcher != nil {
		const idleTickEvery = 60 * time.Second
		nowNano := time.Now().UnixNano()
		lastNano := h.idleTimeoutLastTick.Load()
		if lastNano == 0 || time.Duration(nowNano-lastNano) >= idleTickEvery {
			if h.idleTimeoutLastTick.CompareAndSwap(lastNano, nowNano) {
				// Full set, not ownedOnly: Tick's lastSeen cleanup has a
				// documented invariant that it must see every instance to
				// prune dead entries correctly. Sessions with
				// IdleTimeoutSecs>0 are rare, so the cross-instance dedupe
				// win here was negligible against that correctness leak.
				h.idleTimeoutWatcher.Tick(instances)
			}
		}
	}

	// PERFORMANCE: Gradually configure unconfigured sessions in background
	// Configure one session per tick to avoid blocking the status update
	// This ensures all sessions get configured within ~1 minute even without user interaction
	for _, inst := range activeInstances {
		if tmuxSess := inst.GetTmuxSession(); tmuxSess != nil {
			// ExistsCached() (cache/pipe only, never a has-session subprocess):
			// this loop evaluates the liveness check for EVERY unconfigured
			// session each tick until it finds one to configure. With hundreds
			// of unconfigured dead sessions, Exists() here would subprocess-probe
			// each one — the same storm the tickMsg fix kills. A live-but-uncached
			// session simply gets configured a tick later.
			if !tmuxSess.IsConfigured() && tmuxSess.ExistsCached() {
				tmuxSess.EnsureConfigured()
				inst.SyncSessionIDsToTmux()
				break // Only one per tick to avoid blocking
			}
		}
	}

	// Feed hook statuses from watcher to instances (enables hook fast path in UpdateStatus)
	h.feedHookStatuses(instances)

	// Reconcile OpenCode SSE connections and feed derived statuses to
	// instances (enables the SSE fast path in UpdateStatus, issue #1614).
	if h.sseWatcher != nil {
		var targets []session.SSETarget
		for _, inst := range instances {
			if inst.Tool != "opencode" {
				continue
			}
			st := inst.GetStatusThreadSafe()
			if st == session.StatusStopped {
				continue
			}
			if target, ok := inst.OpenCodeSSETarget(); ok {
				targets = append(targets, target)
				if ss := h.sseWatcher.GetStatus(inst.ID); ss != nil {
					inst.UpdateOpenCodeSSEStatus(ss.Status, ss.UpdatedAt)
				}
			}
		}
		h.sseWatcher.Sync(targets)
	}

	// Proactive context-% monitoring: send /clear before auto-compact triggers
	// For conductor sessions with clear_on_compact enabled, check cached analytics
	for _, inst := range instances {
		if !session.IsClaudeCompatible(inst.Tool) || inst.GroupPath != "conductor" {
			continue
		}
		if !inst.ConductorClearOnCompact() {
			continue
		}
		// Debounce: skip if /clear was recently sent for this session
		if lastSent, ok := h.clearOnCompactSent[inst.ID]; ok {
			if time.Since(lastSent) < clearOnCompactCooldown {
				continue
			}
		}
		// Check cached analytics for context usage. The gate fails closed on
		// a window that is inferred, unknown or disproved (issue #2026); the
		// reason is logged once per session so a disarmed trigger is visible.
		cached := h.getAnalyticsForSession(inst)
		if cached == nil {
			continue
		}
		due, reason := clearOnCompactDue(cached)
		if !due {
			h.noteClearOnCompactDisarmed(inst.ID, reason)
			continue
		}
		if tmuxSess := inst.GetTmuxSession(); tmuxSess != nil {
			h.scheduleClearAndHeartbeat(inst, instances, tmuxSess)
		}
	}

	// Two tiers per periodic pass. Tier one, unbudgeted but screen-bounded:
	// every visible or focused row, and every row whose hook/SSE evidence
	// moved since the sweep last applied it, so a Stop or a permission prompt
	// renders on the next tick rather than when its rotation slot comes up.
	// Tier two, budgeted: a fixed-size slice of the remaining fleet. A
	// full-fleet pass launched hundreds of probes when the list held stopped
	// or disconnected sessions, starving tmux and status readers; the budget
	// bounds that tail without delaying the rows that just changed.
	forced, evidence := h.sweepPriorityRows(instances)
	startIndex := 0
	if len(instances) > 0 {
		startIndex = int(h.fullStatusUpdateIndex.Load()) % len(instances)
	}
	nextIndex := startIndex
	scheduled := 0

	// Update the selected sessions in parallel. With PipeManager, skip sessions
	// idle for >5s (no %output events = no status change).
	statusStart := time.Now()
	var statusChanged atomic.Bool
	var slowMu sync.Mutex
	var slowSessions []string
	pm := tmux.GetPipeManager()
	var skipped int // sessions not polled this tick (archived + idle fast-path)

	tracker := h.getTransitionTracker()

	var statusPass session.StatusUpdatePass
	g := new(errgroup.Group)
	g.SetLimit(10) // Pool of 10 workers (tmux server serializes, more doesn't help)

	pipeIdle := func(inst *session.Instance) bool {
		if pm == nil {
			return false
		}
		ts := inst.GetTmuxSession()
		if ts == nil || !pm.IsConnected(ts.TmuxName()) {
			return false
		}
		lastOut := pm.LastOutputTime(ts.TmuxName())
		return !lastOut.IsZero() && time.Since(lastOut) > 5*time.Second
	}
	poll := func(inst *session.Instance) {
		h.sweepEvidence[inst.ID] = evidence[inst.ID]
		g.Go(func() error {
			oldStatus := inst.GetStatusThreadSafe()
			instStart := time.Now()
			_ = statusPass.UpdateStatus(inst)
			instDur := time.Since(instStart)

			if instDur > 50*time.Millisecond {
				slowMu.Lock()
				slowSessions = append(slowSessions, fmt.Sprintf("%s=%v", inst.Title, instDur.Round(time.Millisecond)))
				slowMu.Unlock()
			}
			newStatus := inst.GetStatusThreadSafe()
			if newStatus != oldStatus {
				statusChanged.Store(true)
				notifLog.Debug(
					"status_changed",
					slog.String("title", inst.Title),
					slog.String("old", string(oldStatus)),
					slog.String("new", string(newStatus)),
				)
				// T1+T3: synthesize a flicker_detected WARN if this session
				// has oscillated >3 times within 60s. One alert per burst.
				session.GlobalFlickerDetector().Observe(inst.ID, string(newStatus))
				tracker.record(inst.ID, inst.Title, inst.Tool, string(oldStatus), string(newStatus))
			}
			return nil
		})
	}

	// Tier one. A row with moved evidence is polled even when its pipe is
	// quiet: the hook is the newer signal. A merely visible row keeps the
	// pipe-idle skip, its pipe events refresh it.
	for idx, why := range forced {
		inst := instances[idx]
		if why == sweepRowVisible && pipeIdle(inst) {
			skipped++
			continue
		}
		poll(inst)
	}

	// Tier two: the rotation.
	for scan := 0; scan < len(instances); scan++ {
		idx := (startIndex + scan) % len(instances)
		inst := instances[idx]

		// Skip archived sessions: their tmux pane is torn down and their row
		// status is display-frozen (rowStatusGlyph forces the stopped glyph
		// regardless of Status), so UpdateStatus can only burn a serialized tmux
		// subprocess without changing anything the UI shows. With a large archive
		// backlog this dominated the loop (observed: 723 archived of 742 total
		// pushed the sweep to multi-second spikes). Unarchiving runs its own
		// refresh, so the periodic loop never needs to poll archived sessions.
		if !h.shouldSweepInstance(inst) || forced[idx] != 0 {
			if forced[idx] == 0 {
				skipped++
			}
			nextIndex = (idx + 1) % len(instances)
			continue
		}
		if scheduled == fullStatusBatchSize {
			skipped += len(instances) - scan
			break
		}
		scheduled++
		nextIndex = (idx + 1) % len(instances)

		// Skip idle sessions when PipeManager knows they haven't produced output.
		// Only skip if pipe is alive (otherwise we need UpdateStatus for Error detection).
		if pipeIdle(inst) {
			skipped++
			continue
		}
		poll(inst)
	}
	h.fullStatusUpdateIndex.Store(int32(nextIndex)) // #nosec G115 -- bounded by instance count
	_ = g.Wait()                                    // Errors are logged within each goroutine

	statusDur := time.Since(statusStart)
	tracker.tickEnd(statusStart, time.Now())
	if skipped > 0 {
		perfLog.Debug(
			"idle_sessions_skipped",
			slog.Int("skipped", skipped),
			slog.Int("checked", len(instances)-skipped),
		)
	}
	if statusDur > 500*time.Millisecond {
		perfLog.Info("slow_status_loop", slog.Duration("duration", statusDur), slog.Int("sessions", len(instances)))
		slowMu.Lock()
		if len(slowSessions) > 0 {
			perfLog.Info("slow_sessions", slog.String("details", strings.Join(slowSessions, ", ")))
		}
		slowMu.Unlock()
	}

	// SQLite reads: shared statuses from other instances, read once and
	// reused by the acknowledgment loop at its original end-of-sweep
	// position below (no second DB read). Applying the owner's status for
	// non-owned sessions happens here, before the render snapshot, so it
	// lands in the same sweep. With claim polling off this block applies
	// nothing, and writes stay after the snapshot so the auto-name persist
	// keeps reading the freshly refreshed snapshot — flag-off ordering is
	// identical to before claim polling existed.
	var sharedStatuses map[string]statedb.StatusRow
	if db := statedb.GetGlobal(); db != nil {
		if statuses, err := db.ReadAllStatuses(); err == nil {
			sharedStatuses = statuses
			for _, inst := range instances {
				s, ok := sharedStatuses[inst.ID]
				if !ok {
					continue
				}
				// Hook-lag samples a CLI pass persisted for this session (no
				// extra read: they ride the status row). The next sweep's
				// hook fast path consults them (session/hook_lag.go).
				inst.ApplyPersistedHookLag(s.HookLag)
				// Sessions NOT freshly polled by this instance this sweep
				// (neither owned nor orphan-due) render the owner's status
				// from the shared row. Gated on isPolledByMe, not isOwned:
				// an orphan we just polled above must keep its fresh
				// UpdateStatus result — applying the older DB row here would
				// clobber it, the WriteStatus loop below would persist the
				// stale value, and the lastPersistedStatus dedup would then
				// freeze it. Tool is NOT imported from the shared row: it's
				// hydrated once from the instances table and effectively
				// immutable mid-session, and render paths read inst.Tool
				// lock-free, so writing it here from a background goroutine
				// would be a data race.
				if h.claimPolling && !h.isPolledByMe(inst.ID) && s.Status != "" {
					if session.Status(s.Status) != inst.GetStatusThreadSafe() {
						statusChanged.Store(true)
					}
					inst.SetStatusThreadSafe(session.Status(s.Status))
				}
			}
		}
	}

	// Invalidate cache if status changed
	if statusChanged.Load() {
		h.cachedStatusCounts.valid.Store(false)
		h.publishWebSessionStates(instances)
	}
	h.refreshSessionRenderSnapshot(instances)

	// SQLite writes: dead-instance cleanup, status writes (enables
	// multi-instance coordination). The heartbeat runs at the top of the sweep.
	if db := statedb.GetGlobal(); db != nil {
		// Clean dead instances every ~20s (not every tick)
		if time.Since(h.lastDeadInstanceCleanup) > 20*time.Second {
			_ = db.CleanDeadInstances(30 * time.Second)
			if h.claimPolling {
				_ = db.PruneStaleSessionClaims()
			}
			h.lastDeadInstanceCleanup = time.Now()
		}

		// Write statuses only when changed to reduce SQLite write pressure.
		// Sessions neither owned nor orphan-polled this sweep are not written:
		// the owning instance (or, for orphans, this primary's orphan sweep)
		// is the source of truth for that session's status row. Orphans MUST
		// be written here — that's the entire point of polling them above.
		currentIDs := h.persistPolledStatuses(db, instances)
		for id := range h.lastPersistedStatus {
			if _, ok := currentIDs[id]; !ok {
				delete(h.lastPersistedStatus, id)
			}
		}

		// Persist the live Claude task description for auto-named sessions so the
		// meaningful name survives an app reopen (it would otherwise live only in
		// the in-memory render snapshot). The snapshot was refreshed just above,
		// so getSessionRenderState returns the freshly-cleaned pane title. Only
		// write on change (mirrors the status loop) and only when non-empty — an
		// empty/idle pane must not clobber a previously captured description.
		for _, inst := range instances {
			desc, write := shouldPersistAutoNameDesc(
				inst.GetAutoName(),
				h.getSessionRenderState(inst).paneTitle,
				h.lastPersistedAutoNameDesc[inst.ID],
			)
			if !write {
				continue
			}
			inst.SetAutoNameDescription(desc)
			if err := db.WriteAutoNameDescription(inst.ID, desc); err != nil {
				uiLog.Warn("autoname_persist_failed",
					slog.String("id", inst.ID),
					slog.String("error", err.Error()))
				continue
			}
			h.lastPersistedAutoNameDesc[inst.ID] = desc
		}
		for id := range h.lastPersistedAutoNameDesc {
			if _, ok := currentIDs[id]; !ok {
				delete(h.lastPersistedAutoNameDesc, id)
			}
		}

		// Read acknowledgments from SQLite (picks up acks from other instances).
		// Reuses the sharedStatuses map read before the render snapshot.
		if sharedStatuses != nil {
			for _, inst := range instances {
				if s, ok := sharedStatuses[inst.ID]; ok && s.Acknowledged {
					inst.SetAcknowledgedFromShared(true)
				}
			}
		}
	}

	// Always sync notification bar - must check for signal file (Ctrl+b N acknowledgments)
	// even when no status changes occurred
	notifStart := time.Now()
	h.syncNotificationsBackground()

	totalDur := time.Since(totalStart)
	notifDur := time.Since(notifStart)
	if totalDur > 1*time.Second {
		perfLog.Warn("background_status_update_slow",
			slog.Duration("total", totalDur),
			slog.Duration("status", time.Since(statusStart)),
			slog.Duration("notif", notifDur),
			slog.Duration("refresh", refreshDur),
			slog.Int("sessions", len(instances)))
	}
	h.lastFullStatusSweep.Store(time.Now().UnixNano())
}

// attachReturnSyncCmd reconciles the session the user just detached from, OFF the
// Bubble Tea event loop.
//
// Issue #1753: refreshAttachedSessionStatus was called inline from the attach-return
// message handler, so the first repaint of the list waited on two tmux control-mode
// round-trips (list-windows -a, list-panes -a — both O(fleet)), a forced capture-pane
// for the session we left, a hook-status file removal, a possible SQLite status write,
// and a full render-snapshot rebuild. Until that chain finished the event loop could
// not run Update/View, and View() returns "" while isAttaching is set, so the user
// looked at a blank screen for the whole duration. At ~70 sessions that is exactly the
// "Ctrl+Q takes noticeably long to come back" report.
//
// The split: the event loop repaints the list immediately from the last snapshot, this
// Cmd does the tmux/disk work on its own goroutine, and attachReturnSyncedMsg triggers
// one more cheap repaint with the reconciled row. Worst case a row shows its pre-attach
// status for a few frames instead of the list being frozen. Every call in the body
// already runs on the background status worker (backgroundStatusUpdate /
// processStatusUpdate), so no new concurrency contract is introduced — see
// refreshAttachedSessionStatus.
func (h *Home) attachReturnSyncCmd(sessionID string) tea.Cmd {
	if strings.TrimSpace(sessionID) == "" {
		return nil
	}
	return func() tea.Msg {
		// Not a status-pass probe. Charging it made one Ctrl+Q exceed the
		// per-session tmux budget and paint the footer warning.
		tmux.RunUncounted(func() { h.refreshAttachedSessionStatus(sessionID) })
		return attachReturnSyncedMsg{}
	}
}

// attachReturnRefreshCmd is the fleet-wide half of the same split: the delayed
// post-attach catch-up (attachReturnRefreshMsg) refreshed both tmux caches inline,
// which is a second event-loop stall ~350ms after the list came back. Same contract
// as attachReturnSyncCmd: tmux work here, row rebuild on the event loop.
func (h *Home) attachReturnRefreshCmd() tea.Cmd {
	return func() tea.Msg {
		tmux.RunUncounted(func() {
			tmux.RefreshSessionCache()
			tmux.RefreshPaneInfoCache()
			h.refreshSessionRenderSnapshot(nil)
		})
		return attachReturnSyncedMsg{}
	}
}

func (h *Home) refreshAttachedSessionStatus(sessionID string) {
	if strings.TrimSpace(sessionID) == "" {
		return
	}

	h.instancesMu.RLock()
	inst := h.instanceByID[sessionID]
	h.instancesMu.RUnlock()
	if inst == nil {
		return
	}

	// Attach return is the one moment where stale hook files are most visible:
	// Claude/Codex may have exited via /q without writing a fresh "dead" hook.
	// Force the attached session through the live tmux path before the list is
	// redrawn so the status icon reflects a dead pane immediately.
	//
	// Only Claude/Codex (#2436): the other hook tools (pi, gemini, cursor,
	// hermes) map their exit event to a "dead" hook, and they emit sparsely
	// (pi only at turn boundaries), so wiping their hook here dropped them to
	// pane heuristics on every attach/detach until the next hook event.
	if tool := inst.GetToolThreadSafe(); session.IsClaudeCompatible(tool) || session.IsCodexCompatible(tool) {
		inst.ClearHookStatus()
		if h.hookWatcher != nil {
			h.hookWatcher.ClearHookStatus(inst.ID)
		}
	}
	inst.ForceNextStatusCheck()

	if inst.GetTmuxSession() != nil {
		tmux.RefreshSessionCache()
		tmux.RefreshPaneInfoCache()
	}

	oldStatus := inst.GetStatusThreadSafe()
	_ = inst.UpdateStatus()
	newStatus := inst.GetStatusThreadSafe()
	if newStatus != oldStatus {
		h.cachedStatusCounts.valid.Store(false)
		h.publishCurrentSessionStates()
		if db := statedb.GetGlobal(); db != nil {
			committed, applied := writeStatusIfCurrent(db, inst.ID, string(oldStatus), string(newStatus), inst.GetToolThreadSafe())
			if !applied && committed != "" && committed != string(newStatus) {
				inst.SetStatusThreadSafe(session.Status(committed))
				h.publishCurrentSessionStates()
			}
		}
	}
	h.refreshSessionRenderSnapshot(nil)
}

// updateInner is the core Bubble Tea update routine for Home. It dispatches a
// single tea.Msg (key, mouse, tick, or async command result) to the focused
// component or the appropriate handler and returns the updated model plus any
// commands to run. Update wraps it to add cross-cutting concerns.
func (h *Home) updateInner(msg tea.Msg) (tea.Model, tea.Cmd) {
	h.consumeHealthWarning()
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case quitMsg:
		// Execute final shutdown logic after splash delay
		return h, h.performFinalShutdown(bool(msg))

	case tea.BlurMsg:
		// The terminal reports motion only while the pointer is inside its
		// window, so once focus leaves there is no event coming that would
		// correct a stale hover. Losing focus IS that event. (Focus reporting
		// is enabled only for the embedded layout; classic mode never sees
		// this message.)
		h.clearDividerHover()
		return h, nil

	case tea.WindowSizeMsg:
		h.width = msg.Width
		h.height = msg.Height
		// The divider moves when the frame does, so a hover measured against
		// the old geometry is about a column that no longer exists.
		h.clearDividerHover()
		h.updateSizes()
		h.syncViewport() // Recalculate viewport when window size changes
		h.setupWizard.SetSize(msg.Width, msg.Height)
		h.settingsPanel.SetSize(msg.Width, msg.Height)
		h.watcherPanel.SetSize(msg.Width, msg.Height)
		h.agentsPanel.SetSize(msg.Width, msg.Height)
		if h.deadLetterPanel != nil {
			h.deadLetterPanel.SetSize(msg.Width, msg.Height)
		}
		if h.triggersPanel != nil {
			h.triggersPanel.SetSize(msg.Width, msg.Height)
		}
		if h.toolVisibilityPanel != nil {
			h.toolVisibilityPanel.SetSize(msg.Width, msg.Height)
		}
		h.geminiModelDialog.SetSize(msg.Width, msg.Height)
		h.promptInputDialog.SetSize(msg.Width, msg.Height)
		h.scrollbackPager.SetSize(msg.Width, msg.Height)
		h.contextPager.SetSize(msg.Width, msg.Height)
		if h.embeddedMode && h.embeddedTerminal != nil {
			h.syncEmbeddedTerminalGeometry()
			return h, nil
		}
		// Issue #1366: a resize can reveal the preview pane (single -> stacked/dual).
		// fetchSelectedPreview self-guards to nil in single-column, so this only
		// fetches when a preview pane is actually visible.
		return h, h.fetchSelectedPreview()

	case tea.MouseMsg:
		if h.telemetryDialog != nil && h.telemetryDialog.IsVisible() {
			return h, nil
		}
		// Every mouse event updates the hover state, including the wheel events
		// routed away below. Scrolling the list is the ordinary way to move the
		// pointer off the divider, and a wheel notch that skipped this left the
		// separator lit with nothing under it.
		h.syncDividerHover(msg.X)

		// Route mouse wheel events to the active scrollable area.
		// Priority: setup wizard > settings > help > global search > MCP dialog > new/fork dialogs > main list.
		// Non-wheel events are silently ignored (O(1), no blocking I/O).
		switch msg.Button {
		case tea.MouseButtonWheelUp, tea.MouseButtonWheelDown:
			// A wheel notch means the user moved on to something else; never
			// let it slip past the drag handler and strand a grabbed divider.
			if h.draggingDivider {
				h.endDividerDrag()
				h.syncPointerShape()
			}
			if h.setupWizard.IsVisible() {
				return h, nil
			}
			if h.toolVisibilityPanel != nil && h.toolVisibilityPanel.IsVisible() {
				return h, nil
			}
			if h.settingsPanel.IsVisible() {
				if msg.Button == tea.MouseButtonWheelUp {
					h.settingsPanel.ScrollUp()
				} else {
					h.settingsPanel.ScrollDown()
				}
				return h, nil
			}
			if h.helpOverlay.IsVisible() {
				return h, nil
			}
			if h.scrollbackPager.IsVisible() {
				// #1491: the wheel the deck-attach couldn't deliver now scrolls
				// the pager. 3 lines per notch matches typical terminal wheel UX.
				if msg.Button == tea.MouseButtonWheelUp {
					h.scrollbackPager.ScrollUp(3)
				} else {
					h.scrollbackPager.ScrollDown(3)
				}
				return h, nil
			}
			if h.contextPager.IsVisible() {
				// Wheel scrolls the inspector body without moving the
				// selection, matching the scrollback pager above.
				if msg.Button == tea.MouseButtonWheelUp {
					h.contextPager.ScrollUp(3)
				} else {
					h.contextPager.ScrollDown(3)
				}
				return h, nil
			}
			if h.globalSearch.IsVisible() {
				var cmd tea.Cmd
				h.globalSearch, cmd = h.globalSearch.Update(msg)
				return h, cmd
			}
			if h.mcpDialog.IsVisible() {
				if msg.Button == tea.MouseButtonWheelUp {
					h.mcpDialog.ScrollUp()
				} else {
					h.mcpDialog.ScrollDown()
				}
				return h, nil
			}
			if h.newDialog.IsVisible() || h.forkDialog.IsVisible() {
				return h, nil
			}
			// Preview pane scroll (#574): when the wheel event lands in the
			// preview region, scroll preview content instead of moving the
			// list cursor. Dual layout routes by X (preview is the right
			// column); stacked layout routes by Y (preview is the lower
			// region, below the horizontal divider). Single layout has no
			// preview target, so it keeps the legacy list-scroll behaviour.
			switch h.getLayoutMode() {
			case LayoutModeDual:
				leftWidth := h.sessionsPaneWidth()
				if msg.X >= leftWidth {
					if msg.Button == tea.MouseButtonWheelUp {
						h.previewScrollOffset++
					} else if h.previewScrollOffset > 0 {
						h.previewScrollOffset--
					}
					return h, nil
				}
			case LayoutModeStacked:
				if top := h.stackedPreviewTopY(); top >= 0 && msg.Y >= top {
					if msg.Button == tea.MouseButtonWheelUp {
						h.previewScrollOffset++
					} else if h.previewScrollOffset > 0 {
						h.previewScrollOffset--
					}
					return h, nil
				}
			}
			// Main session list scroll (cursor movement also resets any
			// stale preview offset so the new session starts at its tail).
			if msg.Button == tea.MouseButtonWheelUp {
				if h.cursor > 0 {
					h.cursor--
					h.skipDivider(-1)
					h.previewScrollOffset = 0
					h.syncViewport()
					h.markNavigationActivity()
					return h, h.fetchSelectedPreview()
				}
			} else {
				if h.cursor < len(h.flatItems)-1 {
					h.cursor++
					h.skipDivider(1)
					h.previewScrollOffset = 0
					h.syncViewport()
					h.markNavigationActivity()
					return h, h.fetchSelectedPreview()
				}
			}
			return h, nil
		default:
			return h.handleMouse(msg)
		}

	case insertFlushMsg:
		// Drain any buffered runes from the insert-mode batch (#1094). The
		// flag is cleared inside flushInsertBuf so a new batch can be
		// scheduled. If the user already left insert mode we silently drop.
		if h.insertMode {
			h.flushInsertBuf()
		} else {
			h.insertFlushPending = false
			h.insertBuf.Reset()
		}
		return h, nil

	case insertPreviewRefreshMsg:
		// #1131: fast echo path. After an insert keystroke this fires ~60ms
		// later and re-fetches the focused session's preview, BYPASSING the
		// 2s previewCacheTTL gate in the tickMsg handler — that gate was why a
		// typed character could take up to ~2s to appear. Local sessions only;
		// remote previews stay on their SSH-throttled cadence to avoid
		// hammering the link per keystroke.
		h.insertPreviewRefreshPending = false
		if !h.insertMode {
			return h, nil
		}
		inst, key, winIdx := h.selectedPreviewTarget()
		if inst == nil || key == "" {
			return h, nil
		}
		h.previewCacheMu.Lock()
		alreadyFetching := h.previewFetchingID == key
		if !alreadyFetching {
			h.previewFetchingID = key
		}
		h.previewCacheMu.Unlock()
		if alreadyFetching {
			return h, nil
		}
		return h, h.fetchPreview(inst, key, winIdx)

	case embeddedInputErrorMsg:
		if msg.generation == h.embeddedGeneration && msg.err != nil {
			h.exitInsertMode()
			h.setError(fmt.Errorf("embedded session input: %w", msg.err))
		}
		return h, nil

	case embeddedStartMsg:
		return h, h.installEmbeddedTerminal(msg)

	case embeddedFrameMsg:
		return h, h.updateEmbeddedFrame(msg)

	case embeddedRefreshMsg:
		h.embeddedRefreshPending = false
		if !h.embeddedMode {
			return h, nil
		}
		// Backstop for chrome that changes without an explicit message (the
		// nudge's own dismissal rules, a debug bar): a no-op when nothing moved.
		h.syncEmbeddedTerminalGeometry()
		inst, key, winIdx := h.selectedPreviewTarget()
		if inst == nil || key == "" {
			remoteName, remoteSessionID, remoteKey, ok := h.selectedRemotePreviewTarget()
			if !ok {
				return h, h.scheduleEmbeddedRefresh()
			}
			h.previewCacheMu.Lock()
			alreadyFetching := h.previewFetchingID == remoteKey
			if !alreadyFetching {
				h.previewFetchingID = remoteKey
			}
			h.previewCacheMu.Unlock()
			if alreadyFetching {
				return h, h.scheduleEmbeddedRefresh()
			}
			return h, tea.Batch(
				h.fetchRemotePreview(remoteName, remoteSessionID, remoteKey),
				h.scheduleEmbeddedRefresh(),
			)
		}
		h.previewCacheMu.Lock()
		alreadyFetching := h.previewFetchingID == key
		if !alreadyFetching {
			h.previewFetchingID = key
		}
		h.previewCacheMu.Unlock()
		if alreadyFetching {
			return h, h.scheduleEmbeddedRefresh()
		}
		return h, tea.Batch(h.fetchPreview(inst, key, winIdx), h.scheduleEmbeddedRefresh())

	case loadSessionsMsg:
		if msg.loadSequence != 0 && (msg.loadSequence != h.sessionLoadSequence || msg.loadWatcher != h.storageWatcher) {
			return h, nil
		}
		if msg.watcherTicket != nil && h.storageWatcher != nil && !h.storageWatcher.current(*msg.watcherTicket) {
			return h, nil
		}
		if msg.watcherTicket != nil && h.storageWatcher != nil {
			h.storageWatcher.acknowledge(*msg.watcherTicket, msg.persistedSnapshot, msg.err == nil)
		}
		// Clear loading indicators and store file mtime for external change detection
		h.reloadMu.Lock()
		h.isReloading = false
		if msg.err == nil && !msg.loadMtime.IsZero() {
			h.lastLoadMtime = msg.loadMtime
		}
		h.reloadMu.Unlock()
		firstLoad := h.initialLoading
		h.initialLoading = false // First load complete, hide splash
		if firstLoad && !processStart.IsZero() && optiming.Enabled() {
			h.pendingFrameOp, h.pendingFrameStart = "tui.first_frame", processStart
		}
		h.reloadHotkeysFromConfig()

		// Show hooks installation prompt (after splash screen is gone)
		if !h.setupWizard.IsVisible() {
			h.showPendingHooksPrompt()
		}

		// One-time telemetry consent prompt (TELEMETRY.md). Shown only on the
		// FIRST load of this process (never on a storage-watcher reload or
		// ctrl+r, which could pop it up while the user is typing into an
		// agent), only when telemetry.ShouldPrompt says so (undecided,
		// interactive, no kill switch), never in insert mode, and only when
		// no other dialog is on screen; otherwise it waits for a later
		// launch. It takes precedence over the feedback prompt so the two
		// never stack.
		if firstLoad && h.telemetryDialog != nil && !h.telemetryDialog.IsVisible() && !h.insertMode && !h.hasModalVisible() {
			if h.telemetryDialog.Show(Version, telemetry.LoadState()) {
				h.telemetryDialog.SetSize(h.width, h.height)
			}
		}

		// Show feedback popup if user has a new version and hasn't rated yet (D-11/D-12).
		// v1.7.38: also skip when the user's config.toml has [feedback].disabled=true,
		// so a user who opts out via config edit (not just state.json) is honoured.
		if h.feedbackDialog != nil && !h.feedbackDialog.IsVisible() && !h.reportDialog.IsVisible() && (h.telemetryDialog == nil || !h.telemetryDialog.IsVisible()) {
			fbState, _ := feedback.LoadState()
			cfg, _ := session.LoadUserConfig()
			configDisabled := cfg != nil && cfg.Feedback.Disabled
			if !configDisabled && feedback.ShouldShow(fbState, Version, time.Now()) {
				feedback.RecordShown(fbState, time.Now())
				_ = feedback.SaveState(fbState)
				h.feedbackState = fbState
				h.feedbackDialog.Show(Version, fbState, h.feedbackSender)
				h.feedbackDialog.SetSize(h.width, h.height)
			}
		}

		if msg.err != nil {
			h.setError(msg.err)
		} else {
			// Fix stale state: re-capture current cursor AND expanded groups.
			// Between storageChangedMsg (which saved restoreState) and now,
			// the user may have navigated or toggled groups.
			if msg.restoreState != nil {
				// Re-capture cursor position from OLD flatItems
				if h.cursor >= 0 && h.cursor < len(h.flatItems) {
					currentItem := h.flatItems[h.cursor]
					switch currentItem.Type {
					case session.ItemTypeSession:
						if currentItem.Session != nil {
							msg.restoreState.cursorSessionID = currentItem.Session.ID
							msg.restoreState.cursorGroupPath = ""
							msg.restoreState.cursorCreatingID = ""
						} else if currentItem.CreatingID != "" {
							msg.restoreState.cursorCreatingID = currentItem.CreatingID
							msg.restoreState.cursorSessionID = ""
							msg.restoreState.cursorGroupPath = ""
						}
					case session.ItemTypeGroup:
						msg.restoreState.cursorGroupPath = currentItem.Path
						msg.restoreState.cursorSessionID = ""
						msg.restoreState.cursorCreatingID = ""
					}
				}
				msg.restoreState.viewOffset = h.viewOffset

				// Re-capture expanded groups (user may have toggled between
				// storageChangedMsg and now)
				if h.groupTree != nil {
					msg.restoreState.expandedGroups = make(map[string]bool)
					for _, group := range h.groupTree.GroupList {
						if group.Expanded {
							msg.restoreState.expandedGroups[group.Path] = true
						}
					}
				}
			}

			h.instancesMu.Lock()
			oldCount := len(h.instances)
			h.instances = msg.instances
			newCount := len(msg.instances)
			uiLog.Debug("reload_load_sessions", slog.Int("old_count", oldCount), slog.Int("new_count", newCount), slog.String("profile", h.profile))
			// Rebuild instanceByID map for O(1) lookup
			h.instanceByID = make(map[string]*session.Instance, len(h.instances))
			for _, inst := range h.instances {
				h.instanceByID[inst.ID] = inst
			}
			// Deduplicate Claude session IDs on load to fix any existing duplicates
			// This ensures no two sessions share the same Claude session ID
			session.UpdateClaudeSessionsWithDedup(h.instances)
			// Collect OpenCode detection commands for restored sessions without IDs
			// Using tea.Cmd pattern ensures save is triggered after detection completes
			var detectionCmds []tea.Cmd
			for _, inst := range h.instances {
				if inst.Tool == "opencode" && inst.OpenCodeSessionID == "" && (msg.restoreState == nil || inst.OpenCodeDetectedAt.IsZero()) {
					detectionCmds = append(detectionCmds, h.detectOpenCodeSessionCmd(inst))
				}
			}
			h.instancesMu.Unlock()
			if firstLoad {
				// Seed the MRU ring from persisted last_accessed (#2058) once,
				// on the process's first load — a later reload (ctrl+r) or
				// storage-watcher refresh must not reset the walk/alternate
				// state the user has built up since startup.
				h.mru = session.NewMRUHistoryFromInstances(msg.instances, session.DefaultMRUCapacity)
			}
			h.refreshSessionRenderSnapshot(msg.instances)
			// Invalidate status counts cache
			h.cachedStatusCounts.valid.Store(false)
			// Sync group tree with loaded data
			if h.groupTree.GroupCount() == 0 {
				// Initial load - use stored groups if available
				if len(msg.groups) > 0 {
					h.groupTree = session.NewGroupTreeWithGroups(h.instances, msg.groups)
				} else {
					h.groupTree = session.NewGroupTree(h.instances)
				}
				// Seed groups declared in config.toml into the DB, only after a
				// successful load so a partial tree is never persisted.
				if msg.err == nil {
					if cfg, cfgErr := session.LoadUserConfig(); cfgErr == nil && cfg != nil {
						if session.ReconcileDeclarativeGroups(h.groupTree, cfg) {
							if err := h.storage.SaveGroupsOnly(h.groupTree); err != nil {
								uiLog.Warn("declarative_groups_save_failed", slog.Any("error", err))
							}
						}
					}
				}
			} else {
				// Refresh - update existing tree with loaded sessions AND groups
				// Preserve expanded state before recreating tree
				expandedState := make(map[string]bool)
				for path, group := range h.groupTree.Groups {
					expandedState[path] = group.Expanded
				}
				// Recreate tree with fresh groups from storage
				if len(msg.groups) > 0 {
					h.groupTree = session.NewGroupTreeWithGroups(h.instances, msg.groups)
				} else {
					h.groupTree = session.NewGroupTree(h.instances)
				}
				// Restore expanded state for groups that still exist
				for path, expanded := range expandedState {
					if group, exists := h.groupTree.Groups[path]; exists {
						group.Expanded = expanded
					}
				}
			}
			h.search.SetItems(h.instances)

			// Re-apply pending title changes that were lost during reload.
			// This happens when a rename's save was skipped (isReloading=true)
			// and the reload replaced instances with stale disk data.
			if len(h.pendingTitleChanges) > 0 {
				applied := false
				for id, pt := range h.pendingTitleChanges {
					if inst := h.getInstanceByID(id); inst != nil {
						if inst.GetTitleThreadSafe() != pt.title {
							inst.SetTitleThreadSafe(pt.title)
							inst.SyncTmuxDisplayName()
							applied = true
							uiLog.Info("pending_rename_reapplied",
								slog.String("session_id", id),
								slog.String("title", pt.title))
						}
						// Restore the lock state lost in the reload swap. Without
						// this a reapplied user rename stays unlocked, so the next
						// #572 Claude-name sync reverts it to the cwd-folder
						// default — the "my rename keeps disappearing" bug (#697).
						if inst.TitleLocked != pt.locked {
							inst.TitleLocked = pt.locked
							applied = true
						}
						inst.SetAutoName(false) // pending title is a genuine rename; keep the user-chosen name
					}
				}
				// Clear pending changes and persist if any were re-applied
				if applied {
					h.forceSaveInstances()
				}
			}

			// Re-apply pending group create/rename/move ops lost to the same
			// reload race (they persist via non-force saveInstances, which the
			// external-change guard can abort). See pendingGroupOps.
			if h.reapplyPendingGroupOps() {
				h.rebuildFlatItems()
				h.forceSaveInstances()
			}

			// Restore state if provided (from auto-reload)
			if msg.restoreState != nil {
				h.restoreState(*msg.restoreState)
				// #746: re-run --select after auto-reload. The very first
				// loadSessionsMsg may fire before the storage watcher has
				// observed a session that `launch --json` just persisted,
				// so applyInitialSelection returns false and the cursor
				// lands on whatever pendingCursorRestore resolves to (an
				// adjacent row). When the watcher catches up the new
				// session, loadSessionsMsg fires again with restoreState
				// populated — this is our retry window. The helper is
				// idempotent: it no-ops after the first successful match,
				// so normal navigation is not overridden.
				h.applyInitialSelection()
				h.syncViewport()
			} else {
				h.rebuildFlatItems()
				// #709: --select takes precedence over the persisted cursor for
				// the very first load so users land on the session they asked for.
				if h.applyInitialSelection() {
					h.pendingCursorRestore = nil
				}
				// Restore cursor from persisted UI state (initial load only)
				if h.pendingCursorRestore != nil {
					restored := false
					if h.pendingCursorRestore.CursorSessionID != "" {
						for i, item := range h.flatItems {
							if item.Type == session.ItemTypeSession &&
								item.Session != nil &&
								item.Session.ID == h.pendingCursorRestore.CursorSessionID {
								h.cursor = i
								restored = true
								break
							}
						}
					}
					if !restored && h.pendingCursorRestore.CursorGroupPath != "" {
						for i, item := range h.flatItems {
							if item.Type == session.ItemTypeGroup && item.Path == h.pendingCursorRestore.CursorGroupPath {
								h.cursor = i
								break
							}
						}
					}
					// The restored/leftover cursor can sit on a non-selectable
					// divider row (present only in non-Normal view modes) — e.g.
					// when the persisted session no longer exists and no group
					// path matched, leaving the cursor at a stale index. Nudge
					// off it so the first preview shows a real session. No-op
					// when already on a selectable row.
					h.skipDivider(1)
					h.pendingCursorRestore = nil
					h.syncViewport()
				}
				// Save after dedup to persist any ID changes (initial load only)
				h.saveInstances()
			}
			// Pick up where the process we were exec'd from left off.
			if firstLoad {
				if cmd := h.applyRestartHandoff(); cmd != nil {
					detectionCmds = append(detectionCmds, cmd)
				}
				if cmd := h.telemetryFirstLoad(); cmd != nil {
					detectionCmds = append(detectionCmds, cmd)
				}
			}
			// Trigger immediate preview fetch for initial selection (mutex-protected)
			if selected := h.getSelectedSession(); selected != nil {
				h.previewCacheMu.Lock()
				h.previewFetchingID = selected.ID
				h.previewCacheMu.Unlock()
				// Batch preview fetch with any OpenCode detection commands
				allCmds := append(detectionCmds, h.fetchPreview(selected, selected.ID, -1))
				return h, tea.Batch(allCmds...)
			}
			// No selection, but still run detection commands if any
			if len(detectionCmds) > 0 {
				return h, tea.Batch(detectionCmds...)
			}
		}
		return h, nil

	case sessionCreatedMsg:
		uiLog.Info("session_created_msg",
			slog.Bool("has_err", msg.err != nil),
			slog.String("temp_id", msg.tempID),
			slog.Bool("has_instance", msg.instance != nil))
		// Remove the creating placeholder (if any) — always, on success or error
		if msg.tempID != "" {
			delete(h.creatingSessions, msg.tempID)
		}

		// Handle reload scenario: session was already started in tmux, we MUST save it to JSON
		// even during reload, otherwise the session becomes orphaned (exists in tmux but not in storage)
		h.reloadMu.Lock()
		reloading := h.isReloading
		h.reloadMu.Unlock()
		if reloading && msg.err == nil && msg.instance != nil {
			// CRITICAL: Save the new session to JSON immediately to prevent orphaning
			// Skip in-memory state update (reload will handle that), but persist to disk
			uiLog.Debug("reload_save_session_created", slog.String("id", msg.instance.ID), slog.String("title", msg.instance.Title))
			h.instancesMu.Lock()
			h.instances = append(h.instances, msg.instance)
			h.instancesMu.Unlock()
			// Force save to persist the session even during reload
			h.forceSaveInstances()
			// Surface a non-fatal setup-script warning without masking any
			// persistence error forceSaveInstances may have set.
			if msg.setupWarning != "" {
				h.setError(noticeError(h.err, msg.setupWarning))
			}
			// Trigger another reload to pick up the new session in the UI
			if h.storageWatcher != nil {
				h.storageWatcher.TriggerReload()
			}
			return h, nil
		}
		if msg.err != nil {
			h.setError(msg.err)
			if msg.tempID != "" {
				h.rebuildFlatItems() // Remove placeholder from list
			}
		} else {
			h.instancesMu.Lock()
			h.instances = append(h.instances, msg.instance)
			h.instanceByID[msg.instance.ID] = msg.instance
			// Run dedup to ensure the new session doesn't have a duplicate ID
			session.UpdateClaudeSessionsWithDedup(h.instances)
			h.instancesMu.Unlock()
			// Invalidate status counts cache
			h.cachedStatusCounts.valid.Store(false)

			// Track as launching for animation
			h.launchingSessions[msg.instance.ID] = time.Now()

			// Expand the group so the session is visible
			if msg.instance.GroupPath != "" {
				h.groupTree.ExpandGroupWithParents(msg.instance.GroupPath)
			}
			h.revealParentFold(msg.instance)

			// Add to existing group tree instead of rebuilding
			h.groupTree.AddSession(msg.instance)
			h.rebuildFlatItems()
			h.search.SetItems(h.instances)

			// Auto-select the new session
			for i, item := range h.flatItems {
				if item.Type == session.ItemTypeSession && item.Session != nil && item.Session.ID == msg.instance.ID {
					h.cursor = i
					h.syncViewport()
					break
				}
			}

			// Save both instances AND groups (critical fix: was losing groups!)
			// Use forceSave to bypass the external-change abort - new session creation MUST persist
			h.forceSaveInstances()

			// Surface a non-fatal setup-script warning, folded into any
			// persistence error so an unsaved session never looks merely degraded.
			if msg.setupWarning != "" {
				h.setError(noticeError(h.err, msg.setupWarning))
			}

			// Auto-attach to the new session when [ui].attach_on_create is set,
			// so creating a session "instantly opens" it instead of only moving
			// the cursor to it. The session was just Start()ed (see
			// createSessionInGroupWithWorktreeAndOptions), so its pane is live.
			// attachSession returns nil when there is no local tmux pane to
			// attach (e.g. a session whose tmux session could not be resolved);
			// in that case we fall through to today's select-only behavior.
			// Skip auto-attach when a setup warning is pending: attaching would
			// hide the footer before the user can read it.
			if h.attachOnCreate && msg.setupWarning == "" {
				if attachTo := h.attachSession(msg.instance); attachTo != nil {
					return h, tea.Batch(h.fetchPreview(msg.instance, msg.instance.ID, -1), attachTo)
				}
			}

			// Start fetching preview for the new session
			return h, h.fetchPreview(msg.instance, msg.instance.ID, -1)
		}
		return h, nil

	case sessionForkedMsg:
		// Clean up forking state for source session
		if msg.sourceID != "" {
			delete(h.forkingSessions, msg.sourceID)
		}

		// Handle reload scenario: forked session was already started in tmux, we MUST save it
		h.reloadMu.Lock()
		reloading := h.isReloading
		h.reloadMu.Unlock()
		if reloading && msg.err == nil && msg.instance != nil {
			// CRITICAL: Save the forked session to JSON immediately to prevent orphaning
			uiLog.Debug("reload_save_session_forked", slog.String("id", msg.instance.ID), slog.String("title", msg.instance.Title))
			h.instancesMu.Lock()
			h.instances = append(h.instances, msg.instance)
			h.instancesMu.Unlock()
			h.forceSaveInstances()
			// Surface a non-fatal setup-script warning without masking any
			// persistence error forceSaveInstances may have set.
			if msg.setupWarning != "" {
				h.setError(noticeError(h.err, msg.setupWarning))
			}
			if h.storageWatcher != nil {
				h.storageWatcher.TriggerReload()
			}
			return h, nil
		}

		if msg.err != nil {
			h.setError(msg.err)
		} else {
			h.instancesMu.Lock()
			h.instances = append(h.instances, msg.instance)
			h.instanceByID[msg.instance.ID] = msg.instance
			// Run dedup to ensure the forked session doesn't have a duplicate ID
			// This is critical: fork detection may have picked up wrong session
			session.UpdateClaudeSessionsWithDedup(h.instances)
			h.instancesMu.Unlock()
			// Invalidate status counts cache
			h.cachedStatusCounts.valid.Store(false)

			// Track as launching for animation
			h.launchingSessions[msg.instance.ID] = time.Now()

			// Expand the group so the session is visible
			if msg.instance.GroupPath != "" {
				h.groupTree.ExpandGroupWithParents(msg.instance.GroupPath)
			}
			h.revealParentFold(msg.instance)

			// Add to existing group tree instead of rebuilding
			h.groupTree.AddSession(msg.instance)
			h.rebuildFlatItems()
			h.search.SetItems(h.instances)

			// Auto-select the forked session
			for i, item := range h.flatItems {
				if item.Type == session.ItemTypeSession && item.Session != nil && item.Session.ID == msg.instance.ID {
					h.cursor = i
					h.syncViewport()
					break
				}
			}

			// Save both instances AND groups
			// Use forceSave to bypass the external-change abort - forked session MUST persist
			h.forceSaveInstances()

			// forceSaveInstances can setError on a failed persist; fold the
			// non-fatal degradation notice into it rather than overwriting, so a
			// fork that wasn't actually saved doesn't look successful (#1299 review).
			if msg.notice != "" {
				h.setError(noticeError(h.err, msg.notice))
			}
			// Same for a non-fatal setup-script warning; folding composes it with
			// any existing error/notice rather than overwriting.
			if msg.setupWarning != "" {
				h.setError(noticeError(h.err, msg.setupWarning))
			}

			// Start fetching preview for the forked session
			return h, h.fetchPreview(msg.instance, msg.instance.ID, -1)
		}
		return h, nil

	case sessionDeletedMsg:
		// CRITICAL FIX: Skip processing during reload to prevent state corruption
		h.reloadMu.Lock()
		reloading := h.isReloading
		h.reloadMu.Unlock()
		if reloading {
			uiLog.Debug("reload_skip_session_deleted")
			return h, nil
		}

		// Report kill error if any (session may still be running in tmux)
		if msg.killErr != nil {
			h.setError(fmt.Errorf("warning: tmux session may still be running: %w", msg.killErr))
		}

		// Find and remove from list
		var deletedInstance *session.Instance
		h.instancesMu.Lock()
		for i, s := range h.instances {
			if s.ID == msg.deletedID {
				deletedInstance = s
				h.instances = append(h.instances[:i], h.instances[i+1:]...)
				break
			}
		}
		delete(h.instanceByID, msg.deletedID)
		h.instancesMu.Unlock()

		// Push to undo stack before removing from group tree
		var cleanupDone chan struct{}
		var evictSweep tea.Cmd
		if deletedInstance != nil {
			evictSweep = h.pushUndoStack(deletedInstance)
			cleanupDone = make(chan struct{})
			h.undoMu.Lock()
			h.undoStack[len(h.undoStack)-1].cleanupDone = cleanupDone
			h.undoMu.Unlock()
			// Save to recent sessions for quick re-creation
			if err := h.storage.SaveRecentSession(deletedInstance); err != nil {
				uiLog.Warn("save_recent_session_err", slog.String("id", msg.deletedID), slog.String("err", err.Error()))
			}
		}

		// Invalidate status counts cache
		h.cachedStatusCounts.valid.Store(false)
		// Invalidate preview cache for deleted session
		h.invalidatePreviewCache(msg.deletedID)
		// Clean up analytics caches for deleted session
		h.analyticsCacheMu.Lock()
		delete(h.analyticsCache, msg.deletedID)
		delete(h.geminiAnalyticsCache, msg.deletedID)
		delete(h.analyticsCacheTime, msg.deletedID)
		h.analyticsCacheMu.Unlock()
		h.invalidateContextReport(msg.deletedID)
		h.logActivityMu.Lock()
		delete(h.lastLogActivity, msg.deletedID)
		h.logActivityMu.Unlock()
		// Remove from group tree (preserves empty groups)
		if deletedInstance != nil {
			h.groupTree.RemoveSession(deletedInstance)
		}
		h.rebuildFlatItems()
		// Update search items
		h.search.SetItems(h.instances)
		// Undoable: record the pending sweep before the row goes, so a crash
		// after the delete still leaves it for the startup reconcile.
		if deletedInstance != nil {
			if err := h.storage.MarkPendingDeleteSweep(msg.deletedID); err != nil {
				uiLog.Warn("mark_pending_sweep_err", slog.String("id", msg.deletedID), slog.String("err", err.Error()))
			}
		}
		// Explicitly delete from database to prevent resurrection on reload
		cleanup, err := h.storage.DeleteInstanceDeferredCleanup(msg.deletedID)
		if err != nil {
			uiLog.Warn("delete_instance_db_err", slog.String("id", msg.deletedID), slog.String("err", err.Error()))
		} else if deletedInstance != nil {
			// Undoable (ctrl+z): announce it now; the inbox and notify-state
			// sweep (#910) waits until the delete is final (the entry leaves
			// the undo stack, see pushUndoStack), so an undo keeps the
			// session's pending inbox events.
			session.PublishSessionClosed(h.profile, msg.deletedID, session.SessionClosedRemoved)
		} else {
			// Nothing to undo: final now, same cleanup as `agent-deck rm`.
			logRemovedSessionCleanup("delete", msg.deletedID, session.CleanupRemovedSession(h.profile, msg.deletedID))
		}
		// Save both instances AND groups (critical fix: was losing groups!)
		// Use forceSave to bypass the external-change abort - delete MUST persist
		h.forceSaveInstances()

		// Show undo hint (using setError as a transient message)
		if deletedInstance != nil {
			if undoKey := h.actionKey(hotkeyUndoDelete); undoKey != "" {
				h.setError(fmt.Errorf("deleted '%s'. %s to undo", deletedInstance.Title, undoKey))
			} else {
				h.setError(fmt.Errorf("deleted '%s'", deletedInstance.Title))
			}
		}
		return h, tea.Batch(hookCleanupCmd(cleanup, cleanupDone), evictSweep)

	case sessionClosedMsg:
		// Keep session metadata, just reflect runtime termination state.
		if msg.killErr != nil {
			h.setError(fmt.Errorf("failed to close session: %w", msg.killErr))
			return h, nil
		}

		h.cachedStatusCounts.valid.Store(false)
		h.invalidatePreviewCache(msg.sessionID)
		h.rebuildFlatItems()
		h.saveInstances()

		restartHint := ""
		if restartKey := h.actionKey(hotkeyRestart); restartKey != "" {
			restartHint = fmt.Sprintf(". %s to restart", restartKey)
		}
		if inst := h.getInstanceByID(msg.sessionID); inst != nil {
			h.setError(fmt.Errorf("closed '%s'%s", inst.Title, restartHint))
		} else {
			h.setError(fmt.Errorf("session closed%s", restartHint))
		}
		return h, nil

	case sessionArchivedMsg:
		if msg.killErr != nil {
			h.setError(fmt.Errorf("failed to archive: %w", msg.killErr))
			return h, nil
		}
		h.cachedStatusCounts.valid.Store(false)
		h.invalidatePreviewCache(msg.sessionID)
		h.rebuildFlatItems()
		if inst := h.getInstanceByID(msg.sessionID); inst != nil {
			// Persist via a targeted UPDATE, not saveInstances(): under concurrent
			// writers the full-table save aborts on external-change and reloads,
			// silently discarding the archive (archived_at reverts to 0).
			if err := h.persistArchived(inst); err != nil {
				h.setError(fmt.Errorf("failed to persist archive: %w", err))
				return h, nil
			}
			h.setError(fmt.Errorf("archived '%s' (^ to view)", inst.Title))
		}
		return h, nil

	case sessionUnarchivedMsg:
		h.rebuildFlatItems()
		if inst := h.getInstanceByID(msg.sessionID); inst != nil {
			if err := h.persistArchived(inst); err != nil {
				h.setError(fmt.Errorf("failed to persist unarchive: %w", err))
				return h, nil
			}
			h.setError(fmt.Errorf("unarchived '%s'", inst.Title))
		}
		return h, nil

	case sessionRestoredMsg:
		h.reloadMu.Lock()
		reloading := h.isReloading
		h.reloadMu.Unlock()
		if reloading {
			uiLog.Debug("reload_skip_session_restored")
			return h, nil
		}
		if msg.err != nil {
			// The entry is off the undo stack and the session did not come
			// back: the delete is final, so its deferred sweep runs now.
			h.setError(fmt.Errorf("failed to restore session: %w", msg.err))
			if msg.instance != nil {
				return h, h.finalizeDeletesCmd([]string{msg.instance.ID})
			}
			return h, nil
		}

		// Re-add to instances (mirrors sessionCreatedMsg pattern)
		h.instancesMu.Lock()
		h.instances = append(h.instances, msg.instance)
		h.instanceByID[msg.instance.ID] = msg.instance
		session.UpdateClaudeSessionsWithDedup(h.instances)
		h.instancesMu.Unlock()
		h.cachedStatusCounts.valid.Store(false)

		// Track as launching for animation
		h.launchingSessions[msg.instance.ID] = time.Now()

		// Expand the group so the restored session is visible
		if msg.instance.GroupPath != "" {
			h.groupTree.ExpandGroupWithParents(msg.instance.GroupPath)
		}
		h.revealParentFold(msg.instance)

		// Add to group tree and rebuild
		h.groupTree.AddSession(msg.instance)
		h.rebuildFlatItems()
		h.search.SetItems(h.instances)

		// Move cursor to restored session
		for i, item := range h.flatItems {
			if item.Type == session.ItemTypeSession && item.Session != nil && item.Session.ID == msg.instance.ID {
				h.cursor = i
				h.syncViewport()
				break
			}
		}

		// Use forceSave to bypass the external-change abort - restore MUST persist
		h.forceSaveInstances()
		if msg.warning != "" {
			h.setError(fmt.Errorf("restored '%s' (%s)", msg.instance.Title, msg.warning))
		} else {
			h.setError(fmt.Errorf("restored '%s'", msg.instance.Title))
		}
		// The row is back: the delete has nothing left to sweep, so its
		// pending record goes (off the Update loop).
		return h, tea.Batch(h.fetchPreview(msg.instance, msg.instance.ID, -1), h.clearPendingSweepCmd(msg.instance.ID))

	case openCodeDetectionCompleteMsg:
		if msg.sessionID == "" {
			if inst := h.getInstanceByID(msg.instanceID); inst != nil && !inst.OpenCodeDetectedAt.IsZero() {
				return h, nil
			}
		}
		// OpenCode session detection completed
		// CRITICAL: Find the CURRENT instance by ID and update it
		// The original pointer may have been replaced by storage watcher reload
		if msg.sessionID != "" {
			uiLog.Debug("opencode_detection_complete", slog.String("instance_id", msg.instanceID), slog.String("session_id", msg.sessionID))
			// Update the CURRENT instance (not the original pointer which may be stale)
			if inst := h.getInstanceByID(msg.instanceID); inst != nil {
				inst.OpenCodeSessionID = msg.sessionID
				inst.OpenCodeDetectedAt = time.Now()
				uiLog.Debug("opencode_instance_updated", slog.String("instance_id", msg.instanceID), slog.String("session_id", msg.sessionID))
			} else {
				uiLog.Warn("opencode_instance_not_found", slog.String("instance_id", msg.instanceID))
			}
		} else {
			uiLog.Debug("opencode_detection_no_session", slog.String("instance_id", msg.instanceID))
			// Mark detection as completed even when no session found
			// This allows UI to show "No session found" instead of "Detecting..."
			if inst := h.getInstanceByID(msg.instanceID); inst != nil {
				inst.OpenCodeDetectedAt = time.Now()
				uiLog.Debug("opencode_marked_complete", slog.String("instance_id", msg.instanceID))
			}
		}
		// CRITICAL: Force save to persist the detected session ID to storage
		// This uses forceSaveInstances() to bypass isReloading check, preventing
		// the race condition where detection completes during a storage watcher reload
		h.forceSaveInstances()
		return h, nil

	case accountSwitchedMsg:
		// Completion belongs to exactly the generation and source snapshot that
		// launched it. An old completion must not clear a newer spinner or force
		// save a replacement instance loaded by the watcher.
		h.instancesMu.RLock()
		current := h.instanceByID[msg.sessionID]
		// Status belongs to independent monitor/lifecycle ownership and may change
		// while this switch is running. It must not reject the bounded registry
		// commit; account/command/path/native-ID conflicts are checked atomically
		// by Storage.CommitNativeHarnessSwitch below.
		fresh := current != nil && h.switchGenerations[msg.sessionID] == msg.generation && current.Tool == msg.sourceTool && current.Account == msg.sourceAccount && current.ProjectPath == msg.sourceProject && current.Title == msg.sourceTitle && current.GroupPath == msg.sourceGroup && current.Command == msg.sourceCommand && current.ClaudeSessionID == msg.sourceClaudeID && current.CodexSessionID == msg.sourceCodexID && current.EffectiveWorkingDir() == msg.sourceCWD
		if msg.committed {
			// A storage watcher can replace the switched pointer with the source
			// row after recording only a monitor status. Accept either in-memory
			// account representation here; the storage CAS is the authority for
			// substantive source identity conflicts and reconciles the target.
			fresh = current != nil && h.switchGenerations[msg.sessionID] == msg.generation && current.Tool == msg.sourceTool && current.ProjectPath == msg.sourceProject && current.Title == msg.sourceTitle && current.GroupPath == msg.sourceGroup && current.Command == msg.sourceCommand && current.EffectiveWorkingDir() == msg.sourceCWD && (current.Account == msg.sourceAccount || current.Account == msg.account)
		}
		h.instancesMu.RUnlock()
		if !fresh {
			uiLog.Debug("stale_harness_switch_completion", slog.String("session_id", msg.sessionID), slog.Uint64("generation", msg.generation))
			return h, nil
		}
		delete(h.resumingSessions, msg.sessionID)
		var settleCmd tea.Cmd
		// A distinct target is attachable only after an explicit positive native
		// readiness result with no execution error. Pending is not a substitute
		// for readiness, and an error after a durable target write is recovery-only.
		if msg.target != nil && msg.targetReady && msg.err == nil {
			h.instancesMu.Lock()
			if _, exists := h.instanceByID[msg.target.ID]; !exists {
				h.instances = append(h.instances, msg.target)
				h.instanceByID[msg.target.ID] = msg.target
				if h.groupTree != nil {
					h.groupTree.AddSession(msg.target)
				}
			}
			h.instancesMu.Unlock()
			h.rebuildFlatItems()
			h.invalidatePreviewCache(msg.target.ID)
		}
		if msg.committed && msg.nativeResult != nil {
			// Persist the precise post-lifecycle native mutation. A full TUI
			// snapshot here can be stale after an independent status sweep and
			// would either conflict or overwrite unrelated registry changes.
			if h.storage == nil {
				msg.err = fmt.Errorf("persist native switch: storage is unavailable")
				msg.pending = false
				msg.summary = "status=failed; recovery_required=true; storage is unavailable"
			} else if err := h.storage.CommitNativeHarnessSwitch(current, msg.nativeResult); err != nil {
				msg.err = fmt.Errorf("persist native switch: %w", err)
				msg.pending = false
				msg.summary = fmt.Sprintf("status=failed; recovery_required=true; %v", msg.err)
			} else {
				h.rebuildFlatItems()
				h.invalidatePreviewCache(msg.sessionID)
				settleCmd = h.switchSettleCmd(msg)
			}
		}
		for _, warning := range msg.warnings {
			uiLog.Warn("account_switch_warning", slog.String("session_id", msg.sessionID), slog.String("detail", warning))
		}
		// Journal persistence after a commit is a failed, recovery-required
		// operation, like the CLI. Ordinary readiness-pending results retain their
		// pending notice even though the backend uses ErrCrossHarnessPending to
		// make callers inspect the result.
		if msg.destinationDivergent && current != nil {
			// A refused-but-recoverable stale/divergent destination is never a
			// dead end: offer the explicit archive-and-retry action instead of an
			// acknowledge-only notice with no way forward.
			h.confirmDialog.ShowArchiveDestinationSwitch(current, msg.targetHarness, msg.account, msg.err.Error())
			return h, settleCmd
		}
		if msg.err != nil && (!msg.pending || errors.Is(msg.err, session.ErrCrossHarnessRecoveryRequired)) {
			// A refused switch explains why in a paragraph the footer banner
			// would clip, and the session may be left stopped — show it in the
			// modal instead, where it cannot be missed. Preserve any committed
			// metadata rather than hiding a recovery-required partial operation.
			body := msg.err.Error()
			if msg.summary != "" {
				body += "\n\n" + msg.summary
			}
			if msg.committed {
				body += "\n\nstatus=failed; recovery_required=true; committed=true"
			}
			h.confirmDialog.ShowNotice("Account switch failed", body)
			return h, settleCmd
		}
		if msg.pending {
			h.confirmDialog.ShowNotice("Account switch pending", msg.summary)
			return h, settleCmd
		}
		if msg.err != nil {
			h.confirmDialog.ShowNotice("Account switch failed", msg.err.Error())
			return h, settleCmd
		}
		if len(msg.warnings) > 0 {
			// The CLI prints these on stderr; dropping them here would leave a
			// failed folder-trust pre-seed to surface as a session that stalls
			// on a trust prompt with no explanation.
			h.confirmDialog.ShowNotice("Account switched with warnings",
				fmt.Sprintf("Switched to %q — %s\n\n%s", msg.account, msg.summary, strings.Join(msg.warnings, "\n")))
			return h, settleCmd
		}
		if msg.target != nil && msg.targetReady {
			h.confirmDialog.ShowNotice("Harness transfer verified",
				fmt.Sprintf("New %s target %q is verified and ready. %s", msg.target.Tool, msg.target.Title, msg.summary))
			return h, settleCmd
		}
		if msg.nativeResult != nil && msg.nativeResult.DestinationReady {
			h.confirmDialog.ShowNotice("Account switch verified", msg.summary)
			return h, settleCmd
		}
		if msg.nativeResult != nil {
			h.confirmDialog.ShowNotice("Account switch pending verification",
				fmt.Sprintf("Account changed to %q, but its restarted harness has not reported native readiness. %s", msg.account, msg.summary))
			return h, settleCmd
		}
		// Legacy/synthetic completions without an execution receipt retain the
		// existing footer behavior; real native and cross-harness paths above
		// always carry the receipt needed to state verified versus pending.
		h.setError(fmt.Errorf("account switched to %q — %s", msg.account, msg.summary))
		return h, settleCmd

	case switchSettledMsg:
		h.handleSwitchSettled(msg)
		h.invalidatePreviewCache(msg.sessionID)
		return h, nil

	case sessionRestartedMsg:
		if msg.err != nil {
			// Restart failed - clear resuming animation immediately so user can retry.
			delete(h.resumingSessions, msg.sessionID)
			if msg.fresh {
				h.setError(fmt.Errorf("failed to restart session fresh: %w", msg.err))
			} else {
				h.setError(fmt.Errorf("failed to restart session: %w", msg.err))
			}
		} else {
			// Find the instance and refresh its MCP state (O(1) lookup)
			if inst := h.getInstanceByID(msg.sessionID); inst != nil {
				// Refresh the loaded MCPs to match the new config
				inst.CaptureLoadedMCPs()
			}
			// Run dedup in-memory before saving, mirroring sessionCreatedMsg pattern (line ~2864)
			h.instancesMu.Lock()
			session.UpdateClaudeSessionsWithDedup(h.instances)
			h.instancesMu.Unlock()
			h.invalidatePreviewCache(msg.sessionID)
			if msg.unarchived {
				h.cachedStatusCounts.valid.Store(false)
				h.rebuildFlatItems()
			}
			// The name and status this restart produced are already durable:
			// Instance.restart records them with a targeted two-column write at
			// the moment it mints them (#1870). This save is for the rest of the
			// restart's in-memory state, and it stays a ROUTINE save on purpose.
			//
			// The first shape of this fix force-saved here, to stop the
			// external-change abort from discarding the new tmux name. That cured
			// a lost restart mutation with a much worse failure: a force save
			// pushes this TUI's whole in-memory snapshot, so an archive, rename or
			// group move another process made while this TUI was stale is silently
			// reverted -- the lost-update shape behind this repository's data-loss
			// incidents. A single wrong field is never worth a whole-snapshot write.
			//
			// adoptRestartRecord below keeps the abort from firing on this TUI's
			// OWN restart write, which is what made the abort look like the
			// problem in the first place.
			h.adoptRestartRecord(msg.sessionID)
			h.saveInstances()
			if msg.warning != "" {
				h.setError(fmt.Errorf("%s", msg.warning))
			} else if msg.unarchived {
				if inst := h.getInstanceByID(msg.sessionID); inst != nil {
					h.setError(fmt.Errorf("unarchived and restarted '%s'", inst.Title))
				}
			}
		}
		// Clear animation so ENTER can attach immediately.
		delete(h.resumingSessions, msg.sessionID)
		return h, nil

	case mcpRestartedMsg:
		if msg.err != nil {
			h.setError(fmt.Errorf("failed to restart session for MCP changes: %w", msg.err))
			return h, nil
		}
		// Refresh the loaded MCPs to match the new config
		if msg.session != nil {
			msg.session.CaptureLoadedMCPs()
			h.invalidatePreviewCache(msg.session.ID)
			h.saveInstances()
			// NOTE: Do NOT delete from mcpLoadingSessions here!
			// Animation continues until Claude is ready or timeout expires
			mcpUILog.Debug("mcp_reload_initiated", slog.String("session_id", msg.session.ID))
		}
		return h, nil

	case updateInstallFinishedMsg:
		return h, h.handleUpdateInstallFinished(msg)

	case unattendedInstallFinishedMsg:
		return h, h.handleUnattendedInstallFinished(msg)

	case updateTimerEnsuredMsg:
		h.handleUpdateTimerEnsured(msg)
		return h, nil

	case pathShadowMsg:
		h.applyPathShadow(msg)
		return h, nil

	case binaryVersionProbedMsg:
		if h.binaryWatch != nil {
			h.binaryWatch.recordProbe(msg.fingerprint, msg.version, msg.err)
			if msg.err != nil {
				// Warn, not Debug: a probe that keeps failing is why a
				// newer build on disk is never noticed, and the default
				// log must show it.
				uiLog.Warn("binary_version_probe_failed", slog.String("error", msg.err.Error()), slog.Int("failures", h.binaryWatch.failures))
			} else if v := h.binaryWatch.installedVersion; v != "" {
				uiLog.Info("update_installed_on_disk", slog.String("running", Version), slog.String("installed", v))
			}
		}
		// Restart right away when allowed; otherwise the tick loop retries.
		return h, h.maybeAutoRestart()

	case updateCheckMsg:
		return h, h.handleUpdateCheck(msg)

	case remotePollRetryFailedMsg:
		h.setError(msg.err)
		return h, nil

	case remoteFetchRoundMsg:
		// Fan out: each remote answers with its own remoteSessionsFetchedMsg.
		// Outstanding counts add up so an overlapping round (ctrl+r during
		// a poll) keeps the guard until every fetch of both has landed.
		h.remoteSessionsMu.Lock()
		h.remotesFetchActive = true
		h.remoteFetchOutstanding += len(msg.fetches)
		h.remotesFetchActive = h.remoteFetchOutstanding > 0
		h.lastRemoteFetch = time.Now()
		h.remoteSessionsMu.Unlock()
		h.cachedStatusCounts.valid.Store(false)
		h.rebuildFlatItems()
		return h, tea.Batch(msg.fetches...)

	case remoteSessionsFetchedMsg:
		return h.applyRemoteFetch(msg)

	case remoteChangedMsg:
		if msg.change.Pane != nil {
			// The focused remote session's pane, pushed by the agent: apply
			// it to the preview and re-arm. Pushes for a session no longer
			// under the cursor are ignored (the unwatch may still be in
			// flight).
			if h.remotePaneWatch.remote == msg.remoteName && h.remotePaneWatch.session == msg.change.Pane.Session {
				if h.applyRemotePane(msg.remoteName, msg.change.Pane) {
					// Nothing to show from the pane (stopped session, empty
					// screen): one poll, whose transcript fallback fills
					// the preview the way it did before pushes existed.
					return h, tea.Batch(h.waitRemoteChange, h.pollRemotePreviewIfSelected(h.remotePaneWatch))
				}
			}
			return h, h.waitRemoteChange
		}
		if !h.remoteIsConfigured(msg.remoteName) {
			// The remote was removed from the config (its channel is being
			// closed by the next fetch round): nothing it says belongs in
			// the tree any more.
			uiLog.Debug("remote_changed", slog.String("remote", msg.remoteName), slog.Bool("deconfigured_dropped", true))
			return h, h.waitRemoteChange
		}
		if msg.change.HasData && !h.stalePushedChange(msg.change, time.Now()) {
			// The event brought the listings: apply them now, no round trip.
			uiLog.Debug("remote_changed", slog.String("remote", msg.remoteName), slog.Bool("pushed_data", true))
			_, cmd := h.applyRemoteFetch(h.pushedRemoteFetch(msg.change))
			return h, tea.Batch(cmd, h.waitRemoteChange)
		}
		if msg.change.HasData {
			// The listing predates an action the user just completed on
			// this remote: applying it would bring the removed row back.
			// Drop it and let a fetch settle the truth instead.
			uiLog.Debug("remote_changed", slog.String("remote", msg.remoteName), slog.Bool("stale_push_dropped", true))
		}
		// A pushed change without data: refetch at once unless a fetch is
		// already in flight (then one more runs after it).
		h.remoteSessionsMu.Lock()
		active := h.remotesFetchActive
		if active {
			h.remoteRefetchWanted = true
		} else {
			h.remotesFetchActive = true
		}
		h.remoteSessionsMu.Unlock()
		uiLog.Debug("remote_changed", slog.String("remote", msg.remoteName), slog.Bool("fetch_in_flight", active))
		if active {
			return h, h.waitRemoteChange
		}
		return h, tea.Batch(h.fetchRemoteSessions, h.waitRemoteChange)

	case remotePaneWatchMsg:
		if msg.err == nil || h.remotePaneWatch != msg.target {
			return h, nil
		}
		// The agent refused or the channel dropped: poll this preview as
		// before. The next tick re-evaluates once the channel is back.
		h.remotePaneWatch = remotePaneWatchTarget{}
		return h, h.pollRemotePreviewIfSelected(msg.target)

	case remoteLatenciesFetchedMsg:
		h.remoteLatencyMu.Lock()
		if h.remoteLatency == nil {
			h.remoteLatency = make(map[string]session.RemoteLatency)
		}
		for name, lat := range msg.latencies {
			h.remoteLatency[name] = lat
		}
		h.lastRemoteLatencyFetch = time.Now()
		h.remoteLatencyFetchBusy = false
		h.remoteLatencyMu.Unlock()
		return h, nil

	case remoteUpdatedMsg:
		if msg.err != nil {
			// The footer is one line and cuts the remedy at the terminal
			// edge; the dialog wraps the whole message (#2244).
			h.setError(fmt.Errorf("failed to update remote %s: %w", msg.remoteName, msg.err))
			h.confirmDialog.ShowNotice(fmt.Sprintf("Remote update failed: %s", msg.remoteName), msg.err.Error())
			return h, nil
		}
		// The deploy verified the remote runs msg.to; show it now and let the
		// next poll's hourly re-check confirm it rather than trusting a stale
		// cached answer.
		h.recordRemoteVersions(map[string]session.RemoteVersionState{
			msg.remoteName: {Version: msg.to, Found: true, CheckedAt: time.Now()},
		})
		h.setError(fmt.Errorf("updated remote %s to v%s", msg.remoteName, msg.to))
		return h, h.fetchRemoteSessions

	case remoteSessionDeletedMsg:
		h.setRemotePending(msg.sessionID, "")
		h.noteRemoteActionSettled(msg.remoteName)
		if session.IsRemoteInterrupted(msg.err) {
			// The remote may have deleted it: the row stays until the
			// fetch says.
			return h, h.remoteOutcomeUnknown(msg.remoteName)
		}
		if msg.err != nil {
			h.setError(fmt.Errorf("failed to delete '%s' on %s: %w", msg.title, msg.remoteName, msg.err))
			return h, nil
		}
		h.dropRemoteSession(msg.remoteName, msg.sessionID)
		h.setError(fmt.Errorf("deleted '%s' on %s%s", msg.title, msg.remoteName, h.remoteActionTook(msg.remoteName, "delete", msg.sessionID)))
		return h, h.fetchRemoteSessions

	case remoteSessionClosedMsg:
		h.setRemotePending(msg.sessionID, "")
		h.noteRemoteActionSettled(msg.remoteName)
		if session.IsRemoteInterrupted(msg.err) {
			return h, h.remoteOutcomeUnknown(msg.remoteName)
		}
		if msg.err != nil {
			h.setError(fmt.Errorf("failed to close '%s' on %s: %w", msg.title, msg.remoteName, msg.err))
			return h, nil
		}
		h.setError(fmt.Errorf("closed '%s' on %s%s", msg.title, msg.remoteName, h.remoteActionTook(msg.remoteName, "close", msg.sessionID)))
		return h, h.fetchRemoteSessions

	case remoteSessionArchivedMsg:
		verb := "archive"
		if !msg.archived {
			verb = "unarchive"
		}
		h.setRemotePending(msg.sessionID, "")
		h.noteRemoteActionSettled(msg.remoteName)
		if session.IsRemoteInterrupted(msg.err) {
			return h, h.remoteOutcomeUnknown(msg.remoteName)
		}
		if msg.err != nil {
			h.setError(fmt.Errorf("failed to %s '%s' on %s: %w", verb, msg.title, msg.remoteName, msg.err))
			return h, nil
		}
		archived := msg.archived
		h.patchRemoteSession(msg.remoteName, msg.sessionID, func(s *session.RemoteSessionInfo) {
			s.Archived = archived
			if archived {
				s.Status = string(session.StatusStopped)
			}
		})
		h.noteRemoteSessionArchived(msg.remoteName, msg.sessionID, archived)
		h.setError(fmt.Errorf("%sd '%s' on %s%s", verb, msg.title, msg.remoteName, h.remoteActionTook(msg.remoteName, "archive", msg.sessionID)))
		return h, h.fetchRemoteSessions

	case remoteSessionRestartedMsg:
		delete(h.remoteRestarting, remoteRestartAnimationID(msg.remoteName, msg.sessionID))
		delete(h.resumingSessions, remoteRestartAnimationID(msg.remoteName, msg.sessionID))
		h.setRemotePending(msg.sessionID, "")
		h.noteRemoteActionSettled(msg.remoteName)
		if session.IsRemoteInterrupted(msg.err) {
			return h, h.remoteOutcomeUnknown(msg.remoteName)
		}
		if msg.err != nil {
			h.setError(fmt.Errorf("failed to restart '%s' on %s: %w", msg.title, msg.remoteName, msg.err))
			return h, nil
		}
		h.patchRemoteSession(msg.remoteName, msg.sessionID, func(s *session.RemoteSessionInfo) { s.Status = string(session.StatusRunning) })
		h.setError(fmt.Errorf("restarted '%s' on %s%s", msg.title, msg.remoteName, h.remoteActionTook(msg.remoteName, "restart", msg.sessionID)))
		return h, h.fetchRemoteSessions

	case remoteSessionForkedMsg:
		delete(h.remoteForking, remoteRestartAnimationID(msg.remoteName, msg.sessionID))
		delete(h.forkingSessions, remoteRestartAnimationID(msg.remoteName, msg.sessionID))
		h.setRemotePending(msg.sessionID, "")
		h.noteRemoteActionSettled(msg.remoteName)
		if session.IsRemoteInterrupted(msg.err) {
			return h, h.remoteOutcomeUnknown(msg.remoteName)
		}
		if msg.err != nil {
			h.setError(fmt.Errorf("failed to fork '%s' on %s: %w", msg.title, msg.remoteName, msg.err))
			return h, nil
		}
		h.setError(fmt.Errorf("forked '%s' on %s%s", msg.title, msg.remoteName, h.remoteActionTook(msg.remoteName, "fork", msg.sessionID)))
		return h, h.fetchRemoteSessions

	case remoteEditAccountsFetchedMsg:
		h.applyRemoteEditAccounts(msg)
		return h, nil
	case remoteSwitchPreviewMsg:
		return h, h.handleRemoteSwitchPreview(msg)
	case remoteSwitchResultMsg:
		return h, h.handleRemoteSwitchResult(msg)
	case remoteCreationCatalogFetchedMsg:
		if h.newDialog.IsVisible() && h.pendingRemoteName == msg.remoteName && h.remoteAccountsGen == msg.gen {
			if msg.err != nil {
				uiLog.Warn("remote_creation_catalog_failed", slog.String("remote", msg.remoteName), slog.String("error", msg.err.Error()))
				reason := msg.err.Error()
				if strings.ContainsAny(reason, "\r\n\x1b") || len(reason) > 160 {
					reason = "Remote creation unavailable; check SSH and the remote version"
				}
				h.newDialog.SetError(reason)
			} else {
				h.newDialog.SetRemoteCreationCatalog(msg.catalog)
			}
		}
		return h, nil

	case remoteCreateDirNeededMsg:
		// The server refused the path; ask before retrying with --create-dir
		// so a typo never silently creates a directory on the remote.
		pending := msg
		h.pendingRemoteCreate = &pending
		h.confirmDialog.ShowCreateRemoteDirectory(msg.remoteName, msg.opts.Path)
		h.beginAttachReturnGrace(time.Now())
		return h, tea.Batch(tea.EnableMouseCellMotion, RestoreLegacyKeyboardCmd(os.Stdout), tea.WindowSize())

	case remoteSessionCreatedMsg:
		if msg.err != nil {
			h.setError(msg.err)
		} else if msg.notice != "" {
			h.setError(errors.New(msg.notice))
		}
		if msg.created != nil {
			// Draw the confirmed row now; the fetch below reconciles the
			// remote's own title (a quick-create gets its name there) and
			// status.
			h.insertRemoteSession(*msg.created)
			if msg.preview != "" {
				h.seedRemotePreview(msg.created.RemoteName, msg.created.ID, msg.preview)
			}
		}
		// This message is returned after tea.Exec finishes the remote
		// create+attach. Mirror the statusUpdateMsg attach-return cleanup so
		// detaching from a newly created remote session leaves the terminal in
		// the same state as detaching from an existing one: re-enable mouse
		// reporting, restore legacy keyboard mode, force a resize, and schedule
		// the delayed repaint (see the statusUpdateMsg case for the rationale).
		h.beginAttachReturnGrace(time.Now())
		return h, tea.Batch(
			h.fetchRemoteSessions,
			tea.EnableMouseCellMotion,
			RestoreLegacyKeyboardCmd(os.Stdout),
			tea.WindowSize(),
			tea.Tick(attachReturnRefreshDelay, func(time.Time) tea.Msg { return attachReturnRefreshMsg{} }),
		)

	case hookTrustRequestMsg:
		// Only one prompter call is in flight at a time (NewHookTrustPrompter
		// serializes), so the dialog is never already open here.
		if h.hookTrustDialog == nil {
			h.hookTrustDialog = NewHookTrustDialog()
		}
		h.hookTrustDialog.Show(msg.id, msg.reply)
		h.hookTrustDialog.SetSize(h.width, h.height)
		return h, nil

	case MaintenanceCompleteMsg:
		return h, func() tea.Msg {
			return maintenanceCompleteMsg{result: msg.Result}
		}

	case maintenanceCompleteMsg:
		r := msg.result
		// Build a summary string
		var parts []string
		if r.PrunedLogs > 0 {
			parts = append(parts, fmt.Sprintf("%d logs pruned", r.PrunedLogs))
		}
		if r.PrunedBackups > 0 {
			parts = append(parts, fmt.Sprintf("%d backups cleaned", r.PrunedBackups))
		}
		if r.ArchivedSessions > 0 {
			parts = append(parts, fmt.Sprintf("%d sessions archived", r.ArchivedSessions))
		}
		if r.OrphanContainers > 0 {
			parts = append(parts, fmt.Sprintf("%d orphan containers removed", r.OrphanContainers))
		}
		if len(parts) > 0 {
			h.maintenanceMsg = "Maintenance: " + strings.Join(parts, ", ") + fmt.Sprintf(" (%s)", r.Duration.Round(time.Millisecond))
			h.maintenanceMsgTime = time.Now()
			// The banner takes a row from the embedded pane.
			h.syncEmbeddedTerminalGeometry()
			// Auto-clear after 30 seconds
			return h, tea.Tick(30*time.Second, func(_ time.Time) tea.Msg {
				return clearMaintenanceMsg{}
			})
		}
		return h, nil

	case remoteRenameResultMsg:
		if session.IsRemoteInterrupted(msg.err) {
			// Reverting is safe: the fetch brings the title the remote
			// actually has.
			h.setRemoteSessionTitle(msg.remoteName, msg.sessionID, msg.oldTitle)
			return h, h.remoteOutcomeUnknown(msg.remoteName)
		}
		if msg.err != nil {
			h.setRemoteSessionTitle(msg.remoteName, msg.sessionID, msg.oldTitle)
			h.setError(fmt.Errorf("failed to rename '%s' on %s: %v", msg.oldTitle, msg.remoteName, msg.err))
			return h, nil
		}
		h.setError(fmt.Errorf("renamed to '%s' on %s%s", msg.newTitle, msg.remoteName, h.remoteActionTook(msg.remoteName, "rename", msg.sessionID)))
		return h, nil

	case remoteMoveResultMsg:
		if session.IsRemoteInterrupted(msg.err) {
			return h, h.remoteOutcomeUnknown(msg.remoteName)
		}
		if msg.err != nil {
			h.setError(msg.err)
			return h, nil
		}
		// Remote confirmed; patch the local fleet cache so the row moves
		// groups immediately, before the next poll re-fetches the truth.
		// The persisted local order overlay for the old bucket is harmless:
		// applyRemoteSessionOrder skips overlay IDs the remote no longer
		// reports in that bucket.
		h.remoteSessionsMu.Lock()
		if sessions, ok := h.remoteSessions[msg.remoteName]; ok {
			for i := range sessions {
				if sessions[i].ID == msg.sessionID {
					sessions[i].Group = msg.groupPath
					break
				}
			}
		}
		h.remoteSessionsMu.Unlock()
		h.rebuildFlatItems()
		h.setError(fmt.Errorf("moved to '%s' on %s%s", msg.groupPath, msg.remoteName, h.remoteActionTook(msg.remoteName, "move", msg.sessionID)))
		return h, nil

	case remoteGroupResultMsg:
		if session.IsRemoteInterrupted(msg.err) {
			return h, h.remoteOutcomeUnknown(msg.remoteName)
		}
		if msg.err != nil {
			h.setError(msg.err)
			return h, nil
		}
		// Remote confirmed the group exists; add it to the cached group list
		// so the M move dialog (and subsequent subgroup creates) offer it
		// immediately, before the next fleet poll re-confirms from the
		// remote's own DB. The sessions map is untouched — creating a group
		// moves no sessions.
		h.remoteSessionsMu.Lock()
		if h.remoteGroups == nil {
			h.remoteGroups = make(map[string][]string)
		}
		seen := false
		for _, p := range h.remoteGroups[msg.remoteName] {
			if p == msg.groupPath {
				seen = true
				break
			}
		}
		if !seen {
			// Appended, not sorted: the list is in the remote's own group
			// order, and a new group joins the end of it there too.
			h.remoteGroups[msg.remoteName] = append(h.remoteGroups[msg.remoteName], msg.groupPath)
		}
		h.remoteSessionsMu.Unlock()
		// Show the new (empty) group's row now instead of after the next poll.
		h.rebuildFlatItems()
		h.setError(fmt.Errorf("created group '%s' on %s%s", msg.groupPath, msg.remoteName, h.remoteActionTook(msg.remoteName, "create-group", remoteGroupBaseName(msg.groupPath))))
		return h, nil

	case remoteGroupDeleteResultMsg:
		if session.IsRemoteInterrupted(msg.err) {
			return h, h.remoteOutcomeUnknown(msg.remoteName)
		}
		if msg.err != nil {
			h.setError(msg.err)
			return h, nil
		}
		// Drop the group (and any of its sub-groups) from the cached list so
		// its header row disappears now; the next fleet poll re-confirms
		// from the remote's own DB.
		h.remoteSessionsMu.Lock()
		cached := h.remoteGroups[msg.remoteName]
		kept := make([]string, 0, len(cached))
		for _, p := range cached {
			if p != msg.groupPath && !strings.HasPrefix(p, msg.groupPath+"/") {
				kept = append(kept, p)
			}
		}
		h.remoteGroups[msg.remoteName] = kept
		h.remoteSessionsMu.Unlock()
		h.rebuildFlatItems()
		h.setError(fmt.Errorf("deleted group '%s' on %s%s", msg.groupPath, msg.remoteName, h.remoteActionTook(msg.remoteName, "delete-group", msg.groupPath)))
		return h, h.fetchRemoteSessions

	case remoteGroupReorderResultMsg:
		if session.IsRemoteInterrupted(msg.err) {
			return h, h.remoteOutcomeUnknown(msg.remoteName)
		}
		if msg.err != nil {
			h.setError(msg.err)
			return h, nil
		}
		direction := "up"
		if msg.delta > 0 {
			direction = "down"
		}
		if !msg.moved {
			h.setError(fmt.Errorf("'%s' did not move %s on %s: it is already at the edge of its siblings there", msg.groupPath, direction, msg.remoteName))
			return h, nil
		}
		// Remote confirmed and persisted the new order; patch the cached
		// group list the same way so the header moves now, before the next
		// fleet poll re-reads the truth from `group list`.
		h.remoteSessionsMu.Lock()
		if h.remoteGroups == nil {
			h.remoteGroups = make(map[string][]string)
		}
		h.remoteGroups[msg.remoteName] = swapRemoteGroupSibling(h.remoteGroups[msg.remoteName], msg.groupPath, msg.delta)
		h.remoteSessionsMu.Unlock()
		h.clearError()
		h.rebuildFlatItemsPreservingSelection(h.captureSelectedItemIdentity())
		return h, nil

	case triggersTickMsg, triggersPolledMsg:
		return h, h.handleTriggersMsg(msg)

	case reviverTickMsg:
		// Fire-and-forget reviver sweep for instances whose tmux server
		// survived an SSH scope cleanup but whose pipe was reaped. Runs in
		// a goroutine so it never blocks the Bubble Tea update loop.
		go func(instances []*session.Instance) {
			rev := session.NewReviver()
			_ = rev.ReviveAll(instances)
		}(h.ownedOnly(append([]*session.Instance(nil), h.instances...)))
		return h, h.reviverTick()

	case clearMaintenanceMsg:
		h.maintenanceMsg = ""
		h.syncEmbeddedTerminalGeometry()
		return h, nil

	case reportPreviewMsg:
		if h.reportDialog != nil {
			h.reportDialog.OnPreview(msg)
		}
		return h, nil

	case reportFiledMsg:
		if h.reportDialog != nil {
			h.reportDialog.OnFiled(msg)
		}
		return h, nil

	case reportCopiedMsg:
		if h.reportDialog != nil {
			h.reportDialog.OnCopied(msg)
		}
		return h, nil

	case feedbackSentMsg:
		// Route the result into the dialog so stepSent renders success or an explicit
		// error line (v1.7.37, #679 TUI follow-up). Auto-dismiss still fires via timer.
		if h.feedbackDialog != nil {
			h.feedbackDialog.OnSent(msg)
		}
		return h, nil

	case feedbackDismissMsg:
		// Auto-dismiss timer fired after stepSent confirmation.
		if h.feedbackDialog != nil && h.feedbackDialog.IsVisible() {
			h.feedbackDialog.Hide()
		}
		return h, nil

	case telemetryDismissMsg:
		if h.telemetryDialog != nil && h.telemetryDialog.IsVisible() {
			h.telemetryDialog.Hide()
		}
		return h, nil

	case telemetryUploadMsg:
		// Background upload finished (or was refused by the package's own
		// gates). Deliberately silent either way.
		return h, nil

	case telemetryUploadTickMsg:
		return h, tea.Batch(telemetryUploadCmd(), telemetryUploadTick())

	case telemetryGrantedMsg:
		return h, h.telemetryGranted(msg)

	case telemetryDisabledMsg:
		if msg.err != nil {
			h.err = msg.err
			h.errTime = time.Now()
		}
		return h, nil

	case modelsFetchedMsg:
		if h.geminiModelDialog != nil && h.geminiModelDialog.IsVisible() {
			h.geminiModelDialog.HandleModelsFetched(msg)
		}
		return h, nil

	case modelSelectedMsg:
		// Find the session and set the model
		h.instancesMu.RLock()
		inst := h.instanceByID[msg.instanceID]
		h.instancesMu.RUnlock()
		if inst != nil {
			if err := inst.SetGeminiModel(msg.model); err != nil {
				h.err = fmt.Errorf("failed to set model: %w", err)
				h.errTime = time.Now()
			}
			// Force save to persist the model change
			h.forceSaveInstances()
		}
		return h, nil

	case promptEditorMsg:
		if msg.err != nil {
			h.setError(fmt.Errorf("edit message: %w", msg.err))
		} else if h.promptInputDialog.IsVisible() {
			h.promptInputDialog.input.SetValue(msg.text)
		}
		return h, nil

	case promptDeliveryMsg:
		if msg.err != nil {
			h.setError(msg.err)
		}
		return h, nil

	case promptSubmitMsg:
		// Deliver asynchronously through the native queue API or session send.
		h.instancesMu.RLock()
		inst := h.instanceByID[msg.instanceID]
		h.instancesMu.RUnlock()
		if inst == nil {
			h.setError(fmt.Errorf("prompt target session no longer exists"))
			return h, nil
		}
		ts := inst.GetTmuxSession()
		if ts == nil || ts.TmuxName() == "" {
			h.setError(fmt.Errorf("session %q is not running; start it before prompting", inst.Title))
			return h, nil
		}
		// PR #1942 review (P1a): the same refusal the CLI send path applies.
		// A pane that runs a server rather than an agent has no prompt to type
		// into, and the delivery below would report success while the text went
		// to the server's stdin. Surfaced through setError so the user sees it
		// instead of watching a prompt disappear. Nil for every other tool.
		if err := inst.PromptDeliveryError(); err != nil {
			h.setError(err)
			return h, nil
		}
		return h, quickMessageCmd(h.profile, inst, msg)

	case refreshMsg:
		return h, h.sessionLoadCmd(nil, true)
	case importReloadMsg:
		state := h.preserveState()
		return h, h.sessionLoadCmd(&state, false)

	case systemThemeMsg:
		theme := "light"
		if msg.dark {
			theme = "dark"
		}
		// Update COLORFGBG in our process environment so that all downstream
		// code (ResolveTheme, ThemeColorFGBG, themeEnvExport, currentTmuxThemeStyle)
		// sees the correct theme. Without this, the stale COLORFGBG inherited
		// from the parent terminal at launch time takes precedence over the OS
		// dark mode change, causing sessions to stay in the wrong color scheme.
		if msg.dark {
			os.Setenv("COLORFGBG", "15;0")
		} else {
			os.Setenv("COLORFGBG", "0;15")
		}
		InitTheme(theme)
		h.propagateThemeToSessions()
		// IMPORTANT: Re-issue listener to keep watching for theme changes.
		// Without this, the watcher silently disconnects.
		return h, tea.Batch(listenForThemeChange(h.themeWatcher), tea.ClearScreen)

	case storageChangedMsg:
		uiLog.Debug("reload_storage_changed", slog.String("profile", h.profile), slog.Int("instances", len(h.instances)))

		// Show reload indicator and increment version to invalidate in-flight background saves
		h.reloadMu.Lock()
		h.isReloading = true
		h.reloadVersion++
		h.reloadMu.Unlock()

		// Preserve UI state before reload
		state := h.preserveState()

		cmd := h.sessionLoadCmd(&state, false)

		// Continue listening for next change
		return h, tea.Batch(cmd, listenForReloads(h.storageWatcher))

	case statusUpdateMsg:
		// Clear attach flag - we've returned from the attached session
		h.isAttaching.Store(false) // Atomic store for thread safety
		if !msg.detachedAt.IsZero() {
			h.pendingFrameOp, h.pendingFrameStart = "tui.detach", msg.detachedAt
		}
		now := time.Now()
		h.beginAttachReturnGrace(now)
		// Reconcile the session we just left on its own goroutine (#1753). It used
		// to run inline here, which held the event loop — and therefore the first
		// repaint of the list — behind O(fleet) tmux round-trips. attachReturnSyncCmd
		// carries the rationale; attachReturnSyncedMsg repaints when it lands.
		syncCmd := tea.Batch(h.attachReturnSyncCmd(msg.attachedSessionID), h.telemetryAttachEnd())
		// Back on the home screen: a newer build waiting restarts now, not a
		// tick later that a quick re-attach would beat (defect 310).
		syncCmd = tea.Batch(syncCmd, h.restartOnDetach())

		selectedBefore := h.captureSelectedItemIdentity()
		h.rebuildFlatItemsPreservingSelection(selectedBefore)

		// Cursor sync: if user switched sessions via notification bar during attach,
		// move cursor to the session they were last viewing
		h.lastNotifSwitchMu.Lock()
		switchedID := h.lastNotifSwitchID
		h.lastNotifSwitchID = ""
		h.lastNotifSwitchMu.Unlock()

		if switchedID != "" {
			found := false
			for i, item := range h.flatItems {
				if item.Type == session.ItemTypeSession && item.Session != nil && item.Session.ID == switchedID {
					h.cursor = i
					h.syncViewport()
					found = true
					break
				}
			}
			// If session is in a collapsed group, expand it first
			if !found {
				h.instancesMu.RLock()
				inst, ok := h.instanceByID[switchedID]
				h.instancesMu.RUnlock()
				if ok && inst.GroupPath != "" && h.groupTree != nil {
					h.groupTree.ExpandGroupWithParents(inst.GroupPath)
					h.revealParentFold(inst)
					h.rebuildFlatItems()
					for i, item := range h.flatItems {
						if item.Type == session.ItemTypeSession && item.Session != nil && item.Session.ID == switchedID {
							h.cursor = i
							h.syncViewport()
							break
						}
					}
				}
			}
		}

		// Skip save during reload to avoid overwriting external changes (CLI)
		h.reloadMu.Lock()
		reloading := h.isReloading
		h.reloadMu.Unlock()
		if reloading {
			// syncCmd still has to run: the inline refresh it replaced happened
			// before this early return, so dropping it here would leave the row we
			// just detached from unreconciled.
			return h, tea.Batch(tea.EnableMouseCellMotion, syncCmd)
		}

		h.followAttachReturnCwd(msg)

		// PERFORMANCE FIX: Skip save on attach return for 10 seconds
		// Saving can also be blocking (JSON serialization + file write).
		// Combine with periodic save instead of saving on every attach/detach.
		// We'll let the next tickMsg handle background save if needed.

		// Re-enable mouse mode after returning from tea.Exec (tmux detach-client
		// resets mouse reporting), restore legacy keyboard reporting (tmux's
		// extended-keys setting leaves Kitty/modifyOtherKeys on the outer terminal;
		// see RestoreLegacyKeyboardCmd for the full rationale), force-poll
		// terminal dimensions (#936: SIGWINCH propagation through nested SSH is
		// late or lost — a host-terminal Cmd++ zoom during attach would otherwise
		// land us back in the menu with stale pre-zoom column counts, making the
		// input line render above the real viewport bottom and run off the
		// right edge), and schedule a delayed repaint for any pane-title/content
		// cache changes that settle just after tmux restores the outer client.
		return h, tea.Batch(
			tea.EnableMouseCellMotion,
			RestoreLegacyKeyboardCmd(os.Stdout),
			tea.WindowSize(),
			syncCmd,
			tea.Tick(attachReturnRefreshDelay, func(time.Time) tea.Msg { return attachReturnRefreshMsg{} }),
		)

	case openSwitcherMsg:
		// The user pressed a switch key while attached. Run the same post-attach
		// reconciliation as statusUpdateMsg, then pop the in-attach switcher
		// pre-highlighted on the neighbor session. Cycling/selection is handled
		// by handleSessionSwitcherKey.
		h.isAttaching.Store(false)
		h.beginAttachReturnGrace(time.Now())
		syncCmd := h.attachReturnSyncCmd(msg.fromSessionID)
		selectedBefore := h.captureSelectedItemIdentity()
		h.rebuildFlatItemsPreservingSelection(selectedBefore)
		h.followAttachReturnCwd(statusUpdateMsg{
			attachedSessionID: msg.fromSessionID,
			attachedWorkDir:   msg.attachedWorkDir,
		})
		// Deliberate consequence of the #1753 split: openSessionSwitcher reads the
		// dim pane-title subtitles out of the render snapshot, which syncCmd now
		// republishes a few ms AFTER this point instead of just before it. The
		// picker therefore opens with subtitles from the last background sweep (at
		// most one sweep interval old) rather than freshly captured ones. That is
		// the trade the issue asks for: the picker appears at once instead of after
		// an O(fleet) tmux stall, and the stale value is a secondary hint, never a
		// status.
		h.openSessionSwitcher(msg.fromSessionID, true)
		return h, tea.Batch(
			tea.EnableMouseCellMotion,
			RestoreLegacyKeyboardCmd(os.Stdout),
			tea.WindowSize(),
			syncCmd,
			tea.Tick(attachReturnRefreshDelay, func(time.Time) tea.Msg { return attachReturnRefreshMsg{} }),
		)

	case openScrollbackMsg:
		// The user pressed the scrollback trigger while attached (#1491). Run the
		// same post-attach reconciliation as the switcher, then open the pager
		// bound to the session we came from and kick off the async history
		// capture. Esc re-attaches; Ctrl+Q returns to the list.
		h.isAttaching.Store(false)
		h.beginAttachReturnGrace(time.Now())
		syncCmd := h.attachReturnSyncCmd(msg.fromSessionID)
		selectedBefore := h.captureSelectedItemIdentity()
		h.rebuildFlatItemsPreservingSelection(selectedBefore)
		h.followAttachReturnCwd(statusUpdateMsg{
			attachedSessionID: msg.fromSessionID,
			attachedWorkDir:   msg.attachedWorkDir,
		})
		captureCmd := h.openScrollbackPager(msg.fromSessionID)
		return h, tea.Batch(
			tea.EnableMouseCellMotion,
			RestoreLegacyKeyboardCmd(os.Stdout),
			tea.WindowSize(),
			syncCmd,
			captureCmd,
		)

	case contextReportMsg:
		// Cache first, then install: a result that arrives after the user closed
		// the overlay is still worth keeping for the next keypress, and the
		// stale guard below is what stops it being shown for the wrong session.
		h.storeContextReport(msg)
		if h.contextPager.IsVisible() && h.contextPager.SessionID() == msg.sessionID {
			if msg.err != nil {
				h.contextPager.SetError(msg.err.Error())
			} else {
				h.contextPager.SetReport(msg.report, msg.warnings)
			}
		}
		return h, nil

	case contextLeverCopiedMsg:
		if h.contextPager.IsVisible() {
			h.contextPager.SetStatus(msg.statusLine())
			return h, nil
		}
		if msg.err != nil {
			h.setError(fmt.Errorf("clipboard: %w", msg.err))
		}
		return h, nil

	case scrollbackContentMsg:
		// Ignore a capture that finished after the pager closed or moved to a
		// different session (stale guard).
		if h.scrollbackPager.IsVisible() && h.scrollbackPager.SessionID() == msg.sessionID {
			if msg.err != nil {
				h.scrollbackPager.SetError(msg.err.Error())
			} else {
				h.scrollbackPager.SetContent(msg.content)
			}
		}
		return h, nil

	case switcherCommitMsg:
		return h, h.handleSwitcherCommit(msg)

	case attachReturnRefreshMsg:
		// The tmux cache refresh moved to its own goroutine (#1753) — running it
		// here stalled the event loop a second time, ~350ms after the list came
		// back. The row rebuild happens in attachReturnSyncedMsg, on the loop,
		// where h.flatItems/h.cursor are safe to touch.
		return h, h.attachReturnRefreshCmd()

	case attachReturnSyncedMsg:
		// Async attach-return reconciliation finished: re-derive the rows from the
		// snapshot it just published. Pure in-memory work, no tmux, no disk.
		selectedBefore := h.captureSelectedItemIdentity()
		h.rebuildFlatItemsPreservingSelection(selectedBefore)
		return h, nil

	case previewDebounceMsg:
		// PERFORMANCE: Debounce period elapsed - check if this fetch is still relevant
		// If user continued navigating, pendingPreviewKey will have changed
		h.previewDebounceMu.Lock()
		isPending := h.pendingPreviewKey == msg.previewKey
		if isPending {
			h.pendingPreviewKey = "" // Clear pending state
		}
		h.previewDebounceMu.Unlock()

		if !isPending {
			return h, nil // Superseded by newer navigation
		}

		if msg.remoteName != "" {
			var cmds []tea.Cmd

			// Over a live channel the agent pushes this pane (the first
			// capture arrives as soon as the watch is set), so no fetch.
			if cmd := h.syncRemotePaneWatch(); cmd != nil {
				cmds = append(cmds, cmd)
			}
			if h.remotePaneWatch == (remotePaneWatchTarget{remote: msg.remoteName, session: msg.sessionID}) {
				return h, batchCmds(cmds)
			}

			// Preview fetch
			h.previewCacheMu.Lock()
			needsPreviewFetch := h.previewFetchingID != msg.previewKey
			if needsPreviewFetch {
				h.previewFetchingID = msg.previewKey
			}
			h.previewCacheMu.Unlock()
			if needsPreviewFetch {
				cmds = append(cmds, h.fetchRemotePreview(msg.remoteName, msg.sessionID, msg.previewKey))
			}

			if len(cmds) > 0 {
				return h, tea.Batch(cmds...)
			}
			return h, nil
		}

		// Find session and trigger actual fetch
		h.instancesMu.RLock()
		inst := h.instanceByID[msg.sessionID]
		h.instancesMu.RUnlock()

		if inst != nil {
			var cmds []tea.Cmd

			// Preview fetch
			h.previewCacheMu.Lock()
			needsPreviewFetch := h.previewFetchingID != msg.previewKey
			if needsPreviewFetch {
				h.previewFetchingID = msg.previewKey
			}
			h.previewCacheMu.Unlock()
			if needsPreviewFetch {
				cmds = append(cmds, h.fetchPreview(inst, msg.previewKey, msg.windowIndex))
			}

			// Analytics fetch (for Claude/Gemini sessions with analytics enabled)
			// Use TTL cache - only fetch if cache miss/expired and not already fetching
			tickTool := inst.GetToolThreadSafe()
			if (tickTool == "claude" || tickTool == "gemini") && h.analyticsFetchingID != inst.ID {
				switch tickTool {
				case "claude":
					cached := h.getAnalyticsForSession(inst)
					if cached != nil {
						// Use cached analytics
						if h.analyticsSessionID != inst.ID {
							h.currentAnalytics = cached
							h.currentGeminiAnalytics = nil
							h.analyticsSessionID = inst.ID
							h.analyticsPanel.SetAnalytics(cached)
						}
					} else {
						// Cache miss or expired - fetch new analytics
						config, _ := session.LoadUserConfig()
						if config != nil && config.GetShowAnalytics() {
							h.analyticsFetchingID = inst.ID
							cmds = append(cmds, h.fetchAnalytics(inst))
						}
					}
				case "gemini":
					// Check Gemini cache
					var cached *session.GeminiSessionAnalytics
					h.analyticsCacheMu.RLock()
					if c, ok := h.geminiAnalyticsCache[inst.ID]; ok {
						if time.Since(h.analyticsCacheTime[inst.ID]) < analyticsCacheTTL {
							cached = c
						}
					}
					h.analyticsCacheMu.RUnlock()

					if cached != nil {
						// Use cached analytics
						if h.analyticsSessionID != inst.ID {
							h.currentGeminiAnalytics = cached
							h.currentAnalytics = nil
							h.analyticsSessionID = inst.ID
							h.analyticsPanel.SetGeminiAnalytics(cached)
						}
					} else {
						// Cache miss or expired - fetch new analytics
						config, _ := session.LoadUserConfig()
						if config != nil && config.GetShowAnalytics() {
							h.analyticsFetchingID = inst.ID
							cmds = append(cmds, h.fetchAnalytics(inst))
						}
					}
				}
			}

			// Worktree dirty status check (lazy, 10s TTL)
			if inst.IsWorktree() && inst.WorktreePath != "" {
				h.worktreeDirtyMu.Lock()
				cacheTs, hasCached := h.worktreeDirtyCacheTs[inst.ID]
				needsCheck := !hasCached || time.Since(cacheTs) > 10*time.Second
				if needsCheck {
					h.worktreeDirtyCacheTs[inst.ID] = time.Now() // Prevent duplicate fetches
				}
				h.worktreeDirtyMu.Unlock()
				if needsCheck {
					sid := inst.ID
					wtPath := inst.WorktreePath
					cmds = append(cmds, func() tea.Msg {
						dirty, err := git.HasUncommittedChanges(wtPath)
						return worktreeDirtyCheckMsg{sessionID: sid, isDirty: dirty, err: err}
					})
				}
			}

			if len(cmds) > 0 {
				return h, tea.Batch(cmds...)
			}
		}
		return h, nil

	case previewFetchedMsg:
		optiming.Since("tui.preview", msg.started)
		// Async preview content received - always advance the TTL so failures
		// and empty responses don't trigger a fetch on every tick.
		// Protect both previewFetchingID and previewCache with the same mutex
		h.previewCacheMu.Lock()
		h.previewFetchingID = ""
		h.previewCacheTime[msg.previewKey] = time.Now()
		if msg.err == nil {
			// Tabs cannot survive into a fixed-cell frame: ansi.StringWidth
			// measures a TAB as zero cells while the terminal expands it to the
			// next multiple-of-8 column, so a captured `git status` or `ls` line
			// passes every width gate and still overflows the pane. Expand at
			// ingest — one chokepoint for local and remote previews alike — so
			// the cached content is already what the terminal will render.
			h.previewCache[msg.previewKey] = expandTabs(msg.content)
		}
		h.previewCacheMu.Unlock()
		return h, nil

	case analyticsFetchedMsg:
		// Async analytics parsing complete - update TTL cache
		h.analyticsFetchingID = ""
		if msg.err == nil && msg.sessionID != "" {
			// Update cache timestamp
			h.analyticsCacheMu.Lock()
			h.analyticsCacheTime[msg.sessionID] = time.Now()

			if msg.analytics != nil {
				// Store Claude analytics in TTL cache
				h.analyticsCache[msg.sessionID] = msg.analytics
				// Update current analytics for display
				h.currentAnalytics = msg.analytics
				h.currentGeminiAnalytics = nil
				h.analyticsSessionID = msg.sessionID
				// Update analytics panel with new data
				h.analyticsPanel.SetAnalytics(msg.analytics)
			} else if msg.geminiAnalytics != nil {
				// Store Gemini analytics in TTL cache
				h.geminiAnalyticsCache[msg.sessionID] = msg.geminiAnalytics
				// Update current analytics for display
				h.currentGeminiAnalytics = msg.geminiAnalytics
				h.currentAnalytics = nil
				h.analyticsSessionID = msg.sessionID
				// Update analytics panel with new data
				h.analyticsPanel.SetGeminiAnalytics(msg.geminiAnalytics)
			} else {
				// Both nil - clear display if it's the current session
				if h.analyticsSessionID == msg.sessionID {
					h.currentAnalytics = nil
					h.currentGeminiAnalytics = nil
					h.analyticsPanel.SetAnalytics(nil)
				}
			}
			h.analyticsCacheMu.Unlock()
		}
		return h, nil

	case worktreeDirtyCheckMsg:
		// Update worktree dirty status cache
		if msg.err == nil {
			h.worktreeDirtyMu.Lock()
			h.worktreeDirtyCache[msg.sessionID] = msg.isDirty
			h.worktreeDirtyCacheTs[msg.sessionID] = time.Now()
			h.worktreeDirtyMu.Unlock()
		}
		// Also update the finish dialog if it's open for this session
		if h.worktreeFinishDialog.IsVisible() && h.worktreeFinishDialog.GetSessionID() == msg.sessionID && msg.err == nil {
			h.worktreeFinishDialog.SetDirtyStatus(msg.isDirty)
		}
		return h, nil

	case worktreeSetupResultMsg:
		delete(h.setupRunningSessions, msg.sessionID)
		if msg.err != nil {
			// Same bounded, sanitized formatting as the create path, so the
			// script's output tail is shown instead of a bare "exit status 1".
			h.setError(errors.New(formatSetupWarning(msg.err)))
		} else {
			h.setError(fmt.Errorf("worktree setup completed for '%s'", msg.sessionTitle))
		}
		return h, nil

	case worktreeFinishResultMsg:
		if msg.err != nil {
			// Show error in dialog (user can go back or cancel)
			if h.worktreeFinishDialog.IsVisible() {
				h.worktreeFinishDialog.SetError(msg.err.Error())
			} else {
				h.setError(msg.err)
			}
			return h, nil
		}

		// Success: remove session from instances and clean up
		h.worktreeFinishDialog.Hide()

		h.instancesMu.Lock()
		for i, s := range h.instances {
			if s.ID == msg.sessionID {
				h.instances = append(h.instances[:i], h.instances[i+1:]...)
				break
			}
		}
		inst := h.instanceByID[msg.sessionID]
		delete(h.instanceByID, msg.sessionID)
		h.instancesMu.Unlock()

		// Invalidate caches
		h.cachedStatusCounts.valid.Store(false)
		h.invalidatePreviewCache(msg.sessionID)
		h.analyticsCacheMu.Lock()
		delete(h.analyticsCache, msg.sessionID)
		delete(h.geminiAnalyticsCache, msg.sessionID)
		delete(h.analyticsCacheTime, msg.sessionID)
		h.analyticsCacheMu.Unlock()
		h.invalidateContextReport(msg.sessionID)
		h.worktreeDirtyMu.Lock()
		delete(h.worktreeDirtyCache, msg.sessionID)
		delete(h.worktreeDirtyCacheTs, msg.sessionID)
		h.worktreeDirtyMu.Unlock()
		h.logActivityMu.Lock()
		delete(h.lastLogActivity, msg.sessionID)
		h.logActivityMu.Unlock()

		// Remove from group tree and rebuild
		if inst != nil {
			h.groupTree.RemoveSession(inst)
		}
		h.rebuildFlatItems()
		h.search.SetItems(h.instances)

		// Delete from database and save
		cleanup, err := h.storage.DeleteInstanceDeferredCleanup(msg.sessionID)
		if err != nil {
			uiLog.Warn("worktree_finish_delete_err", slog.String("id", msg.sessionID), slog.String("err", err.Error()))
		}
		h.forceSaveInstances()

		// Issue #1576: sweep transition-notifier state (inbox JSONL lines +
		// runtime/transition-notify-state.json dedup record) for the removed
		// session, mirroring the #910 cleanup on `agent-deck rm`, and publish
		// macapp.session.closed. Best-effort — never blocks the finish.
		logRemovedSessionCleanup("worktree_finish", msg.sessionID, session.CleanupRemovedSession(h.profile, msg.sessionID))

		// Show success message
		successMsg := fmt.Sprintf("Finished worktree '%s'", msg.sessionTitle)
		if msg.merged {
			successMsg += fmt.Sprintf(", merged into %s", msg.targetBranch)
		}
		h.setError(fmt.Errorf("%s", successMsg))
		return h, hookCleanupCmd(cleanup, nil)

	case copyResultMsg:
		if msg.err != nil {
			h.setError(msg.err)
		} else {
			h.setError(fmt.Errorf("Copied %d lines to clipboard (%s)", msg.lineCount, msg.sessionTitle))
		}
		return h, nil

	case copyPaneResultMsg:
		switch {
		case msg.err != nil:
			h.setError(fmt.Errorf("Could not copy visible terminal text: %w", msg.err))
		case msg.empty:
			h.setError(fmt.Errorf("Nothing visible to copy (%s)", msg.sessionTitle))
		default:
			h.setError(fmt.Errorf("Copied visible terminal text to clipboard (%d lines, %s)", msg.lineCount, msg.sessionTitle))
		}
		return h, nil

	case sessionRefResultMsg:
		h.handleSessionRefResult(msg)
		return h, nil

	case sendOutputResultMsg:
		if msg.err != nil {
			h.setError(fmt.Errorf("failed to send to %s: %v", msg.targetTitle, msg.err))
		} else {
			h.setError(fmt.Errorf("Sent %d lines from '%s' to '%s'", msg.lineCount, msg.sourceTitle, msg.targetTitle))
		}
		return h, nil

	case watcherEventMsg:
		// Refresh watcher panel data on new events and re-register listener.
		// The watcher host's relay already dispatched the event to its
		// conductor's pane, off this loop (#2524); dispatching it here too
		// would deliver it twice.
		h.refreshWatcherPanel()
		if h.watcherPanelEvents != nil {
			return h, listenForWatcherEvent(h.watcherPanelEvents)
		}
		return h, nil

	case watcherHealthMsg:
		// Update health display and re-register listener. Any health alert
		// was already dispatched by the watcher host's relay (#2524).
		h.refreshWatcherPanel()
		if h.watcherPanelHealth != nil {
			return h, listenForWatcherHealth(h.watcherPanelHealth)
		}
		return h, nil

	case WatcherActionMsg:
		db := statedb.GetGlobal()
		if db != nil {
			switch msg.Action {
			case "start":
				_ = db.UpdateWatcherStatus(msg.WatcherID, "running")
			case "stop":
				_ = db.UpdateWatcherStatus(msg.WatcherID, "stopped")
			}
		}
		h.refreshWatcherPanel()
		return h, nil

	case tickMsg:
		h.expireDividerHover(time.Now())
		h.telemetrySample()

		// Honor a pending `agent-deck session focus <id>` request from the CLI.
		// A non-nil cmd means the request asked to --attach the session: open it
		// now (same as Enter) and skip the rest of this tick's background work,
		// which is moot once we hand the terminal to the session. Re-arm the tick
		// (h.tick()) here too — unlike the Enter key path, returning early from the
		// tickMsg case would otherwise break the self-rescheduling tick chain.
		if focusCmd := h.consumeFocusRequest(statedb.GetGlobal()); focusCmd != nil {
			return h, tea.Batch(focusCmd, h.tick())
		}

		// Keep the agents index current so the ⚙ marker and the preview
		// card reflect live state without the panel being open. Internally
		// rate-limited, and a no-op when nothing has been adopted.
		h.refreshAgentsPanel()

		var remoteFetchCmd tea.Cmd
		var remoteLatencyCmd tea.Cmd

		now := time.Time(msg)
		if now.IsZero() {
			now = time.Now()
		}
		filterExpired := h.timeFilter != session.TimeFilterAll && !h.nextTimeFilterExpiry.IsZero() && !now.Before(h.nextTimeFilterExpiry)
		if h.groupViewMode != session.GroupViewNormal || filterExpired {
			selectedBefore := h.captureSelectedItemIdentity()
			h.rebuildFlatItemsPreservingSelectionAt(selectedBefore, now)
		}

		// Auto-dismiss errors after 5 seconds
		if h.err != nil && !h.errTime.IsZero() && time.Since(h.errTime) > 5*time.Second {
			h.clearError()
		}

		// PERFORMANCE: Detect when navigation has settled before re-enabling sync work.
		// This allows background updates to resume after rapid navigation stops
		const navigationSettleTime = 700 * time.Millisecond
		if h.isNavigating && time.Since(h.lastNavigationTime) > navigationSettleTime {
			h.isNavigating = false
		}

		// PERFORMANCE: Skip background updates during rapid navigation
		// This prevents subprocess spawning while user is scrolling through sessions
		if !h.isNavigating {
			// PERFORMANCE: Adaptive status updates - only when user is active
			// If user hasn't interacted for 2+ seconds, skip status updates.
			// This prevents background polling during idle periods.
			const userActivityWindow = 2 * time.Second
			if !h.lastUserInputTime.IsZero() && time.Since(h.lastUserInputTime) < userActivityWindow {
				// User is active - trigger status updates
				// NOTE: RefreshExistingSessions() moved to background worker (processStatusUpdate)
				// to avoid blocking the main goroutine with subprocess calls
				h.triggerStatusUpdate()
			}
			// User idle - no updates needed (cache refresh happens in background worker)
		}

		// Update animation frame for launching spinner (8 frames, cycles every tick)
		h.animationFrame = (h.animationFrame + 1) % 8

		// Refresh cost totals for header display
		h.refreshCostTotals()

		// Periodic UI state save (every 5 ticks = ~10 seconds)
		h.uiStateSaveTicks++
		if h.uiStateSaveTicks >= 5 {
			h.uiStateSaveTicks = 0
			h.saveUIState()
		}
		// A remote snapshot held back by the save debounce lands here once
		// its interval has passed.
		h.flushRemoteSessionsCache(false)

		// Periodic remote session fetch (issue #1170). Cadence is configurable
		// via [ui] remote_session_refresh_secs (default 15s); see
		// shouldFetchRemoteSessions for the stale/in-flight gating.
		if h.shouldFetchRemoteSessions(time.Now()) || h.remoteFocusWantsFetch(time.Now()) {
			h.remoteSessionsMu.Lock()
			h.remotesFetchActive = true
			h.remoteSessionsMu.Unlock()
			remoteFetchCmd = h.fetchRemoteSessions
		}

		// Periodic remote latency measurement (issue #1103). Cadence is
		// configurable via [ui] remote_latency_refresh_secs, defaulting
		// to system_stats.refresh_seconds so the marker ticks alongside
		// CPU/RAM. Fast/non-blocking — each remote runs in its own
		// goroutine inside the Cmd.
		refresh := h.remoteLatencyRefreshSec
		if refresh < 2 {
			refresh = 5
		}
		h.remoteLatencyMu.RLock()
		shouldLatency := !h.remoteLatencyFetchBusy &&
			time.Since(h.lastRemoteLatencyFetch) >= time.Duration(refresh)*time.Second
		h.remoteLatencyMu.RUnlock()
		if shouldLatency {
			h.remoteLatencyMu.Lock()
			h.remoteLatencyFetchBusy = true
			h.remoteLatencyMu.Unlock()
			remoteLatencyCmd = h.measureRemoteLatencies
		}

		// Fast log size check every 10 seconds (catches runaway logs before they cause issues)
		// This is much faster than full maintenance - just checks file sizes
		if time.Since(h.lastLogCheck) >= logCheckInterval {
			h.lastLogCheck = time.Now()
			go func() {
				logSettings := session.GetLogSettings()
				// Fast check - only truncate, no orphan cleanup
				_, _ = tmux.TruncateLargeLogFiles(logSettings.MaxSizeMB, logSettings.MaxLines)
			}()
		}

		// Prune stale caches and limiters every 20 seconds
		if time.Since(h.lastCachePrune) >= 20*time.Second {
			h.lastCachePrune = time.Now()
			h.pruneAnalyticsCache()

			// Fallback safety net for live-set pipes that died between reconciler
			// ticks. The reconciler (livePipeReconciler) is the primary path that
			// connects focused/attached sessions; this only reconnects wanted
			// sessions whose pipe dropped.
			if pm := tmux.GetPipeManager(); pm != nil {
				// Two-phase so NOTHING that can block runs on the bubbletea
				// goroutine. Phase 1 (under RLock) filters with cheap map/slice
				// lookups only: a session needs reconnecting iff it is WANTED
				// (focused/attached) and its pipe has dropped. Phase 2 does the
				// liveness probe and the reconnect off-thread.
				//
				// ts.Exists() must not run here: it falls back to a
				// `tmux has-session` subprocess (up to hasSessionProbeTimeout=2s)
				// for any session the pipe/cache doesn't cover — and a
				// wanted-but-disconnected session on a wedged server is exactly
				// the state that reaches this loop. Probing on the main goroutine
				// (while holding instancesMu, stalling writers too) is what froze
				// the UI for seconds at a time on every tick.
				type reconnectTarget struct {
					ts     *tmux.Session
					socket string
				}
				var targets []reconnectTarget
				h.instancesMu.RLock()
				for _, inst := range h.instances {
					ts := inst.GetTmuxSession()
					if ts == nil {
						continue
					}
					if !h.liveSet.want(ts.TmuxName()) || pm.IsConnected(ts.TmuxName()) {
						continue
					}
					targets = append(targets, reconnectTarget{ts: ts, socket: inst.TmuxSocketName})
				}
				h.instancesMu.RUnlock()

				for _, t := range targets {
					safego.Go(uiLog, "pipe_reconnect", func() {
						if !t.ts.Exists() {
							return
						}
						if err := pm.Connect(t.ts.TmuxName(), t.socket); err != nil {
							uiLog.Debug("pipe_reconnect_failed",
								slog.String("session", t.ts.TmuxName()),
								slog.String("socket", t.socket),
								slog.String("error", err.Error()))
						}
					})
				}
			}
		}

		// Full log maintenance (orphan cleanup, etc) every 5 minutes
		if time.Since(h.lastLogMaintenance) >= logMaintenanceInterval {
			h.lastLogMaintenance = time.Now()
			startLogMaintenance("periodic_log_maintenance")
		}

		// Clean up expired animation entries (launching, resuming, MCP loading, forking)
		// For Claude: remove after 20s timeout (animation shows for ~6-15s)
		// For others: remove after 5s timeout
		const claudeTimeout = 20 * time.Second
		const defaultTimeout = 5 * time.Second

		// Use consolidated cleanup helper for all animation maps
		// Note: cleanupExpiredAnimations accesses instanceByID which is thread-safe on main goroutine
		h.cleanupExpiredAnimations(h.launchingSessions, claudeTimeout, defaultTimeout)
		h.cleanupExpiredAnimations(h.resumingSessions, claudeTimeout, defaultTimeout)
		h.cleanupExpiredAnimations(h.mcpLoadingSessions, claudeTimeout, defaultTimeout)
		h.cleanupExpiredAnimations(h.forkingSessions, claudeTimeout, defaultTimeout)
		// setupRunningSessions is deliberately NOT timer-pruned: it doubles as
		// the b-hotkey re-entrancy lock, and the setup script may legitimately
		// run past any UI timeout (setup_timeout_seconds is user-configurable,
		// including unlimited). It is cleared only by worktreeSetupResultMsg,
		// which runWorktreeSetup always delivers; the script-runner timeout
		// bounds runaway scripts.

		// Notification bar sync handled by background worker (syncNotificationsBackground)
		// which runs even when TUI is paused during tea.Exec

		// Fetch preview for currently selected item (if stale/missing and not fetching)
		// Local previews use a short TTL for near-live terminal updates.
		const previewCacheTTL = 2 * time.Second
		// Remote previews use a longer TTL to avoid frequent SSH calls.
		const remotePreviewCacheTTL = 10 * time.Second
		var previewCmd tea.Cmd
		selectedInst, selectedKey, selectedWinIdx := h.selectedPreviewTarget()
		if selectedInst != nil && !h.shouldSuppressPreviewRefresh(time.Now()) {
			h.previewCacheMu.Lock()
			cachedTime, hasCached := h.previewCacheTime[selectedKey]
			cacheExpired := !hasCached || time.Since(cachedTime) > previewCacheTTL
			// Only fetch if cache is stale/missing AND not currently fetching this item
			if cacheExpired && h.previewFetchingID != selectedKey {
				h.previewFetchingID = selectedKey
				previewCmd = h.fetchPreview(selectedInst, selectedKey, selectedWinIdx)
			}
			h.previewCacheMu.Unlock()
		} else {
			remoteName, remoteSessionID, remoteKey, ok := h.selectedRemotePreviewTarget()
			// The remote agent pushes the focused pane over the channel;
			// the ssh poll only runs while that is not the case (channel
			// down, old remote, watch still being set up).
			if ok && !remotePaneWatchActive(remoteName, remoteSessionID) {
				h.previewCacheMu.Lock()
				cachedTime, hasCached := h.previewCacheTime[remoteKey]
				cacheExpired := !hasCached || time.Since(cachedTime) > remotePreviewCacheTTL
				if cacheExpired && h.previewFetchingID != remoteKey {
					h.previewFetchingID = remoteKey
					previewCmd = h.fetchRemotePreview(remoteName, remoteSessionID, remoteKey)
				}
				h.previewCacheMu.Unlock()
			}
		}
		// Periodic update check: notices a release that lands while the
		// deck is open (auto_install acts on the result) and dismisses a
		// stale banner after an update from a terminal.
		updateCheckCmd := h.periodicUpdateCheck(time.Now())
		// One os.Stat per tick; a version probe only when the file changed.
		binaryProbeCmd := h.pollBinaryChange()
		// auto_restart: hand over to an installed newer build once idle.
		autoRestartCmd := h.maybeAutoRestart()
		// Tell the fleet watch what this TUI runs and why it has not
		// restarted (update --check --json).
		h.maybeWriteHeartbeat(now)

		cmds := []tea.Cmd{h.tick(), previewCmd, remoteFetchCmd, remoteLatencyCmd, h.syncRemotePaneWatch(), updateCheckCmd, binaryProbeCmd, autoRestartCmd}
		if h.fullRepaint {
			cmds = append(cmds, tea.ClearScreen)
		}
		return h, tea.Batch(cmds...)

	case globalSearchDebounceMsg, globalSearchResultsMsg, recallPreviewMsg, recallRefreshMsg, recallStatusMsg, recallCatchUpMsg:
		// Route async Recall messages to the overlay whatever is on top: the
		// catch-up tick must reach it while it is open, and once it is hidden
		// the overlay drops them itself (Hide ends the catch-up chain).
		var cmd tea.Cmd
		h.globalSearch, cmd = h.globalSearch.Update(msg)
		return h, cmd

	case tea.KeyMsg:
		// Track user activity for adaptive status updates
		h.lastUserInputTime = time.Now()
		h.tel.sampler.KeyPressed()
		// Hands on the keyboard means the mouse is not about to grab anything,
		// and the terminal stops reporting motion the instant the pointer
		// leaves its window — so a keystroke is the most reliable signal we get
		// that a hover highlight has outlived its mouse.
		h.clearDividerHover()

		// A worktree hook approval blocks a background operation; it takes
		// every key until answered.
		if h.hookTrustDialog.IsVisible() {
			h.hookTrustDialog.HandleKey(msg)
			return h, nil
		}

		// Handle jump mode input (before modals)
		if h.jumpMode {
			return h.handleJumpKey(msg)
		}

		// Handle setup wizard first (modal, blocks everything)
		if h.setupWizard.IsVisible() {
			var cmd tea.Cmd
			h.setupWizard, cmd = h.setupWizard.Update(msg)
			// Check if wizard completed (Enter on final step, or Esc on welcome to use defaults)
			if h.setupWizard.IsComplete() {
				// Save config and close wizard. Merge onto disk first so
				// fields the wizard doesn't manage (Remotes, Hotkeys,
				// Plugins, etc.) survive — issue #1067.
				config := h.setupWizard.GetConfig()
				merged, mergeErr := session.MergePanelConfigOntoDisk(config)
				if mergeErr == nil && merged != nil {
					config = merged
				}
				if err := session.SaveUserConfig(config); err != nil {
					h.err = err
					h.errTime = time.Now()
				}
				h.setupWizard.Hide()
				// Reload config cache
				_, _ = session.ReloadUserConfig()
				h.reloadHotkeysFromConfig()
				// Apply default tool to new dialog
				if defaultTool := session.GetDefaultTool(); defaultTool != "" {
					h.newDialog.SetDefaultTool(defaultTool)
				}
			}
			return h, cmd
		}

		// Handle delivery and watcher panels (before settings panel)
		if h.triggersPanel.IsVisible() {
			var cmd tea.Cmd
			h.triggersPanel, cmd = h.triggersPanel.Update(msg)
			return h, cmd
		}
		if h.deadLetterPanel != nil && h.deadLetterPanel.IsVisible() {
			var cmd tea.Cmd
			h.deadLetterPanel, cmd = h.deadLetterPanel.Update(msg)
			return h, cmd
		}
		if h.agentsPanel.IsVisible() {
			var cmd tea.Cmd
			h.agentsPanel, cmd = h.agentsPanel.Update(msg)
			return h, cmd
		}
		if h.watcherPanel.IsVisible() {
			var cmd tea.Cmd
			h.watcherPanel, cmd = h.watcherPanel.Update(msg)
			return h, cmd
		}

		if h.toolVisibilityPanel != nil && h.toolVisibilityPanel.IsVisible() {
			var cmd tea.Cmd
			var shouldSave bool
			h.toolVisibilityPanel, cmd, shouldSave = h.toolVisibilityPanel.Update(msg)
			if shouldSave {
				if err := h.saveToolVisibilityConfig(); err != nil {
					h.err = err
					h.errTime = time.Now()
				} else {
					_, _ = session.ReloadUserConfig()
					if h.newDialog != nil {
						h.newDialog.RefreshPresetCommands()
					}
				}
				h.settingsPanel.Show()
				h.settingsPanel.SetSize(h.width, h.height)
			}
			return h, cmd
		}

		// Handle settings panel
		if h.settingsPanel.IsVisible() {
			var cmd tea.Cmd
			var shouldSave bool
			h.settingsPanel, cmd, shouldSave = h.settingsPanel.Update(msg)
			if h.settingsPanel.ConsumeToolVisibilityRequest() {
				h.settingsPanel.Hide()
				h.toolVisibilityPanel.Show()
				h.toolVisibilityPanel.SetSize(h.width, h.height)
				return h, cmd
			}
			if h.settingsPanel.ConsumePrivacyRequest() {
				return h, tea.Batch(cmd, h.togglePrivacyFromSettings())
			}
			if shouldSave {
				// Merge panel output onto the on-disk config so top-level
				// fields the panel does not manage (Remotes, Hotkeys,
				// Plugins, Conductors, Groups, etc.) survive — issue #1067.
				config := h.settingsPanel.GetConfig()
				merged, mergeErr := session.MergePanelConfigOntoDisk(config)
				if mergeErr == nil && merged != nil {
					config = merged
				}
				if err := session.SaveUserConfig(config); err != nil {
					h.err = err
					h.errTime = time.Now()
				}
				_, _ = session.ReloadUserConfig()
				h.reloadHotkeysFromConfig()
				h.showSessionTimestamps = config.Display.ShowSessionTimestamps
				h.showPaneTitles = config.Display.ShowPaneTitles
				h.hiddenToolBadge = hiddenToolBadgeFor(config)
				h.setAccountSlotsConfigured(len(session.ConfiguredAccountNames(config)) > 0)
				h.applySavedLayoutSettings(config.UI)
				h.rebuildFlatItemsPreservingSelection(h.captureSelectedItemIdentity())

				// Apply theme changes live
				h.stopThemeWatcher()
				resolvedTheme := session.ResolveTheme()
				InitTheme(resolvedTheme)
				h.propagateThemeToSessions()
				var themeCmd tea.Cmd
				if config.Theme == "system" {
					themeCmd = h.startThemeWatcher()
				}

				// Apply default tool to new dialog
				if defaultTool := session.GetDefaultTool(); defaultTool != "" {
					h.newDialog.SetDefaultTool(defaultTool)
				}

				if themeCmd != nil {
					return h, tea.Batch(themeCmd, tea.ClearScreen)
				}
				return h, tea.ClearScreen
			}
			return h, cmd
		}

		// Handle overlays first
		// Help overlay takes priority (any key closes it)
		if h.helpOverlay.IsVisible() {
			h.helpOverlay, _ = h.helpOverlay.Update(msg)
			return h, nil
		}
		if h.search.IsVisible() {
			return h.handleSearchKey(msg)
		}
		if h.globalSearch.IsVisible() {
			return h.handleGlobalSearchKey(msg)
		}
		if h.newDialog.IsVisible() {
			return h.handleNewDialogKey(msg)
		}
		if h.groupDialog.IsVisible() {
			return h.handleGroupDialogKey(msg)
		}
		if h.forkDialog.IsVisible() {
			return h.handleForkDialogKey(msg)
		}
		if h.confirmDialog.IsVisible() {
			return h.handleConfirmDialogKey(msg)
		}
		if h.mcpDialog.IsVisible() {
			return h.handleMCPDialogKey(msg)
		}
		if h.pluginDialog.IsVisible() {
			return h.handlePluginDialogKey(msg)
		}
		if h.editPathsDialog.IsVisible() {
			return h.handleEditPathsDialogKey(msg)
		}
		if h.editSessionDialog.IsVisible() {
			return h.handleEditSessionDialogKey(msg)
		}
		if h.skillDialog.IsVisible() {
			return h.handleSkillDialogKey(msg)
		}
		if h.geminiModelDialog.IsVisible() {
			d, cmd := h.geminiModelDialog.Update(msg)
			h.geminiModelDialog = d
			return h, cmd
		}
		if h.promptInputDialog.IsVisible() {
			d, cmd := h.promptInputDialog.Update(msg)
			h.promptInputDialog = d
			h.syncViewport()
			return h, cmd
		}
		if h.sessionSwitcher.IsVisible() {
			return h.handleSessionSwitcherKey(msg)
		}
		if h.scrollbackPager.IsVisible() {
			return h.handleScrollbackPagerKey(msg)
		}
		if h.contextPager.IsVisible() {
			return h.handleContextPagerKey(msg)
		}
		if h.sessionPickerDialog.IsVisible() {
			return h.handleSessionPickerDialogKey(msg)
		}
		if h.codeBlockDialog.IsVisible() {
			return h.handleCodeBlockDialogKey(msg)
		}
		if h.worktreeFinishDialog.IsVisible() {
			return h.handleWorktreeFinishDialogKey(msg)
		}
		if h.reportDialog.IsVisible() {
			d, cmd := h.reportDialog.Update(msg)
			h.reportDialog = d
			return h, cmd
		}
		if h.feedbackDialog.IsVisible() {
			d, cmd := h.feedbackDialog.Update(msg)
			h.feedbackDialog = d
			return h, cmd
		}
		if h.telemetryDialog != nil && h.telemetryDialog.IsVisible() {
			d, cmd := h.telemetryDialog.Update(msg)
			h.telemetryDialog = d
			return h, cmd
		}
		if h.zoxidePicker.IsVisible() {
			return h.handleZoxidePickerKey(msg)
		}

		if h.showCostDashboard {
			keyStr := msg.String()
			if keyStr == "q" || keyStr == "$" || keyStr == "esc" {
				h.showCostDashboard = false
				return h, nil
			}
			return h, nil // consume all other keys
		}

		if h.notesEditing {
			return h.handleNotesEditorKey(msg)
		}

		// Main view keys
		return h.handleMainKey(msg)
	}

	return h, tea.Batch(cmds...)
}

// createSessionFromGlobalSearch creates a new Agent Deck session from global search result
func (h *Home) createSessionFromGlobalSearch(result *GlobalSearchResult) tea.Cmd {
	return func() tea.Msg {
		// Derive title from CWD or session ID
		title := "Claude Session"
		projectPath := result.CWD
		if result.CWD != "" {
			parts := strings.Split(result.CWD, "/")
			if len(parts) > 0 {
				title = parts[len(parts)-1]
			}
		}
		if projectPath == "" {
			projectPath = "."
		}
		// #1706: "." (and any relative CWD a search result carries) must not be
		// persisted as project_path — tmux resolves it against the tmux server's
		// cwd, and the Claude project slug derived from it would point elsewhere
		// again.
		if resolved, resErr := session.ResolveProjectPath(projectPath); resErr == nil {
			projectPath = resolved
		}

		// Create instance. Issue #666: resolveNewSessionGroup rescues empty
		// cursor-group (Window / RemoteGroup / placeholder flatItems) so
		// the empty string never reaches NewInstanceWithGroupAndTool, which
		// would otherwise override the extractGroupPath default with "".
		inst := session.NewInstanceWithGroupAndTool(title, projectPath, h.resolveNewSessionGroup(), "claude")
		inst.ClaudeSessionID = result.SessionID
		// #1815: the user picked this exact conversation for this brand-new
		// instance — an explicit ownership declaration, not a disk-scan
		// guess. Route it through the chokepoint like every other explicit
		// writer (launch_cmd.go, mutators.go) instead of relying on a fresh
		// instance's taint map being empty by construction.
		session.MarkClaudeSessionIDVerified(inst)

		// Build resume command with config dir and permission flags
		userConfig, _ := session.LoadUserConfig()
		opts := session.NewClaudeOptions(userConfig)

		inst.Command = globalSearchClaudeCommand(inst, result.SessionID, opts)

		// Persist options so restarts use per-session settings
		_ = inst.SetClaudeOptions(opts)

		// Start the session
		if err := inst.Start(); err != nil {
			return sessionCreatedMsg{err: fmt.Errorf("failed to start session: %w", err)}
		}
		inst.RecordTelemetryCreate(telemetry.ViaTUINew)

		return sessionCreatedMsg{instance: inst}
	}
}

// globalSearchClaudeCommand builds the claude resume command for a global
// search hit. sessionID comes from an indexed transcript's "sessionId" field
// (recall NativeID), i.e. file data, so it is shell-quoted like every other
// claude resume id; ordinary UUIDs are left byte-identical.
func globalSearchClaudeCommand(inst *session.Instance, sessionID string, opts *session.ClaudeOptions) string {
	// Build command - only set CLAUDE_CONFIG_DIR if explicitly configured
	// If not explicit, let the tmux shell's environment handle it
	// This is critical for WSL and other environments where users have
	// CLAUDE_CONFIG_DIR set in their .bashrc/.zshrc
	var cmdBuilder strings.Builder
	if session.IsClaudeConfigDirExplicitForGroup(inst.GroupPath) {
		configDir := session.GetClaudeConfigDirForGroup(inst.GroupPath)
		// #1815 (Codex review on #1830): quote exactly as
		// Instance.buildBashExportPrefix does (instance.go, audit F2) —
		// this string is baked into inst.Command and ends up in a
		// `bash -c` payload, so an unquoted config_dir containing
		// whitespace, ;, or $(...) breaks the command or injects.
		cmdBuilder.WriteString(fmt.Sprintf("CLAUDE_CONFIG_DIR=%s ", shellescape.Quote(configDir)))
	}
	// #1815: the TUI picker builds a resume command too, so it routes
	// through the same resume-time identity guard as restart / start /
	// fork. The id was just recorded onto inst above (the user picked
	// this conversation FOR this session), so the check passes by
	// construction today — it is here so a future change that reuses an
	// existing instance here cannot resume a conversation that instance
	// does not own. Only the identity half applies: the user's explicit
	// pick must not be downgraded to a fresh session by the
	// conversation-data heuristics.
	//
	// exec so the agent replaces the wrapper shell and leads the pane,
	// matching buildClaudeResumeCommand and the fresh-start path
	if allowed, _ := session.ResumeIdentityAllowed(inst, sessionID); allowed {
		cmdBuilder.WriteString("exec claude --resume ")
		cmdBuilder.WriteString(shellescape.Quote(sessionID))
	} else {
		freshID := session.NewClaudeSessionUUID()
		inst.ClaudeSessionID = freshID
		// #1815: a freshly minted id is vouched ownership, same as every
		// other minted-id writer (Instance.replaceRefusedClaudeSessionID,
		// buildClaudeCommandWithMessage's own mint path).
		session.MarkClaudeSessionIDVerified(inst)
		cmdBuilder.WriteString("exec claude --session-id ")
		cmdBuilder.WriteString(freshID)
	}
	if opts.SkipPermissions {
		cmdBuilder.WriteString(" --dangerously-skip-permissions")
	} else if opts.AllowSkipPermissions {
		cmdBuilder.WriteString(" --allow-dangerously-skip-permissions")
	}
	return cmdBuilder.String()
}

// handleMouse handles mouse events (click to select, double-click to activate)
func (h *Home) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if h.hasModalVisible() {
		// A modal opening mid-drag shouldn't leave the divider stuck grabbed.
		// Treat it as a release so the dragged-to ratio is preserved.
		h.endDividerDrag()
		h.clearDividerHover()
		return h, nil
	}

	// Divider resize drag takes precedence over list selection (the separator
	// columns sit at x >= sessionsPaneWidth, where list clicks are ignored).
	consumed := h.handleDividerDrag(msg)
	h.syncPointerShape()
	if consumed {
		return h, nil
	}

	switch msg.Button {
	case tea.MouseButtonLeft:
		if msg.Action != tea.MouseActionPress {
			return h, nil
		}

		// Check if click is in the session list panel
		if h.getLayoutMode() == LayoutModeDual {
			leftWidth := h.sessionsPaneWidth()
			if msg.X >= leftWidth {
				return h, nil
			}
		}

		itemIndex := h.mouseYToItemIndex(msg.Y)
		if itemIndex < 0 || itemIndex >= len(h.flatItems) {
			return h, nil
		}
		// Dividers are non-selectable: clicking one does nothing.
		if h.flatItems[itemIndex].Type == session.ItemTypeDivider {
			return h, nil
		}

		h.lastUserInputTime = time.Now()

		// Double-click detection: same item within threshold, verified by stable ID
		now := time.Now()
		clickedID := h.clickedItemID(itemIndex)
		isDoubleClick := itemIndex == h.lastClickIndex &&
			clickedID == h.lastClickItemID &&
			time.Since(h.lastClickTime) < doubleClickThreshold
		h.lastClickTime = now
		h.lastClickIndex = itemIndex
		h.lastClickItemID = clickedID

		if isDoubleClick {
			h.lastClickIndex = -1 // Reset to prevent triple-click
			item := h.flatItems[itemIndex]
			if !h.embeddedLayout && (item.Type == session.ItemTypeSession || item.Type == session.ItemTypeWindow || item.Type == session.ItemTypeRemoteSession) {
				h.cursor = itemIndex
				return h, h.attachSelectedLegacy()
			}
			switch item.Type {
			case session.ItemTypeSession:
				if item.Session == nil {
					return h, nil
				}
				if h.hasActiveAnimation(item.Session.ID) {
					h.setError(fmt.Errorf("session is starting, please wait..."))
					return h, nil
				}
				if h.sessionExistsForUI(item.Session) {
					h.cursor = itemIndex
					if h.enterEmbeddedMode() {
						return h, h.startEmbeddedModeCmd()
					}
				}
			case session.ItemTypeWindow:
				// Enter accepts window and remote rows as embedded targets; a
				// double-click on the same row must land in the same place.
				parentInst := h.getInstanceByID(item.WindowSessionID)
				if parentInst != nil && h.sessionExistsForUI(parentInst) {
					h.cursor = itemIndex
					if h.enterEmbeddedMode() {
						return h, h.startEmbeddedModeCmd()
					}
				}
			case session.ItemTypeRemoteSession:
				if item.RemoteSession != nil {
					h.cursor = itemIndex
					if h.enterEmbeddedMode() {
						return h, h.startEmbeddedModeCmd()
					}
				}
			case session.ItemTypeGroup:
				groupPath := item.Path
				h.groupTree.ToggleGroup(groupPath)
				h.rebuildFlatItems()
				for i, fi := range h.flatItems {
					if fi.Type == session.ItemTypeGroup && fi.Path == groupPath {
						h.cursor = i
						break
					}
				}
				h.saveGroupState()
				h.noteGroupToggled(groupPath)
			case session.ItemTypeRemoteGroup:
				h.toggleRemoteGroup(item.RemoteName, item.Path)
			}
			return h, nil
		}

		// Single click: select item
		h.cursor = itemIndex
		h.previewScrollOffset = 0
		h.syncViewport()
		return h, h.markNavigationAndFetchPreview()
	}

	return h, nil
}

// handleMainKey handles keys in main view
func (h *Home) handleMainKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Insert mode (#1069): short-circuit before any normal-mode handling.
	// Keystrokes are sent to the focused session's tmux pane; Esc exits.
	if h.insertMode {
		model, cmd := h.handleInsertModeKey(msg)
		// #1131: after any insert keystroke, arm a fast preview refresh so the
		// user's echo appears in ~60ms instead of waiting up to the 2s
		// background tick. Skip once the keystroke exited insert mode (Esc) —
		// the normal tick cadence resumes there. scheduleInsertPreviewRefresh
		// self-guards against stacking ticks during a typing burst.
		if h2, ok := model.(*Home); ok && h2.insertMode {
			return h2, tea.Batch(cmd, h2.scheduleInsertPreviewRefresh())
		}
		return model, cmd
	}

	raw := msg.String()
	key := h.normalizeMainKey(raw)
	uiLog.Info("keypress", "raw", raw, "normalized", key, "type", msg.Type, "runes", string(msg.Runes))
	if key == "" {
		return h, nil
	}

	// v1.7.60: any keypress dismisses the one-shot nav-discoverability hint
	// (beyond the existing ESC path). The sentinel was already written at
	// NewHome, so this only has to clear the visible banner.
	if h.navHintActive {
		h.navHintActive = false
		if h.maintenanceMsg == navHintText {
			h.maintenanceMsg = ""
		}
	}

	// Session reference pick (ASK-111): while A is picked only movement,
	// drop (Enter / the pick key) and Esc do anything.
	if h.sessionRefPicking() {
		if handled, cmd := h.handleSessionRefPickKey(key); handled {
			return h, cmd
		}
	}

	switch key {
	case "q", "ctrl+c":
		return h.tryQuit()

	case "esc":
		// Dismiss the >5-releases-behind update nudge for this session.
		if h.shouldRenderUpdateNudge() {
			h.handleUpdateNudgeDismiss(msg)
			return h, nil
		}
		// Dismiss maintenance banner if visible
		if h.maintenanceMsg != "" {
			h.maintenanceMsg = ""
			return h, nil
		}
		// Double ESC to quit (#28) - for non-English keyboard users
		// If ESC pressed twice within 500ms, quit the application
		if time.Since(h.lastEscTime) < 500*time.Millisecond {
			return h.tryQuit()
		}
		// First ESC - record time, show hint in status bar
		h.lastEscTime = time.Now()
		return h, nil

	case "up", "k", "ctrl+p":
		h.previewScrollOffset = 0
		if h.cursor > 0 {
			h.cursor--
			h.skipDivider(-1)
			h.syncViewport()
			h.markNavigationActivity()
			// PERFORMANCE: Debounced preview fetch - waits 150ms for navigation to settle
			// This prevents spawning tmux subprocess on every keystroke
			return h, h.fetchSelectedPreview()
		}
		return h, nil

	case "down", "j", "ctrl+n":
		h.previewScrollOffset = 0
		if h.cursor < len(h.flatItems)-1 {
			h.cursor++
			h.skipDivider(1)
			h.syncViewport()
			h.markNavigationActivity()
			// PERFORMANCE: Debounced preview fetch - waits 150ms for navigation to settle
			// This prevents spawning tmux subprocess on every keystroke
			return h, h.fetchSelectedPreview()
		}
		return h, nil

		// Vi-style pagination (#38) - half/full page scrolling
	case "ctrl+u", "pgup": // Half page up
		pageSize := h.pageStepItems(h.getVisibleHeight()/2, -1)
		h.cursor -= pageSize
		if h.cursor < 0 {
			h.cursor = 0
		}
		h.skipDivider(-1)
		h.previewScrollOffset = 0
		h.syncViewport()
		h.markNavigationActivity()
		return h, h.fetchSelectedPreview()

	case "ctrl+d", "pgdown": // Half page down
		pageSize := h.pageStepItems(h.getVisibleHeight()/2, 1)
		h.cursor += pageSize
		if h.cursor >= len(h.flatItems) {
			h.cursor = len(h.flatItems) - 1
		}
		if h.cursor < 0 {
			h.cursor = 0
		}
		h.skipDivider(1)
		h.previewScrollOffset = 0
		h.syncViewport()
		h.markNavigationActivity()
		return h, h.fetchSelectedPreview()

	case "ctrl+b": // Full page up (backward)
		pageSize := h.pageStepItems(h.getVisibleHeight(), -1)
		h.cursor -= pageSize
		if h.cursor < 0 {
			h.cursor = 0
		}
		h.skipDivider(-1)
		h.previewScrollOffset = 0
		h.syncViewport()
		h.markNavigationActivity()
		return h, h.fetchSelectedPreview()

	case "ctrl+f": // Full page down (forward)
		pageSize := h.pageStepItems(h.getVisibleHeight(), 1)
		h.cursor += pageSize
		if h.cursor >= len(h.flatItems) {
			h.cursor = len(h.flatItems) - 1
		}
		if h.cursor < 0 {
			h.cursor = 0
		}
		h.skipDivider(1)
		h.previewScrollOffset = 0
		h.syncViewport()
		h.markNavigationActivity()
		return h, h.fetchSelectedPreview()

	case "home": // Jump to first item
		h.cursor = 0
		h.skipDivider(1)
		h.previewScrollOffset = 0
		h.syncViewport()
		h.markNavigationActivity()
		return h, h.fetchSelectedPreview()

	case "end": // Jump to last item
		h.cursor = len(h.flatItems) - 1
		if h.cursor < 0 {
			h.cursor = 0
		}
		h.skipDivider(-1)
		h.previewScrollOffset = 0
		h.syncViewport()
		h.markNavigationActivity()
		return h, h.fetchSelectedPreview()

	case "G": // Recall search over the index (local title search when recall is off)
		return h, h.openGlobalSearch()

	// Group-scoped navigation layer (v1.7.60): Alt+* keys navigate only within
	// the cursor's current group. Plain j/k/1-9/g/G// remain unchanged above.
	case "alt+j": // Next session in current group
		if target := h.nextSessionInCurrentGroup(); target >= 0 {
			h.jumpToIndex(target)
			return h, h.fetchSelectedPreview()
		}
		return h, nil

	case "alt+k": // Previous session in current group
		if target := h.prevSessionInCurrentGroup(); target >= 0 {
			h.jumpToIndex(target)
			return h, h.fetchSelectedPreview()
		}
		return h, nil

	case "alt+1", "alt+2", "alt+3", "alt+4", "alt+5", "alt+6", "alt+7", "alt+8", "alt+9":
		// Jump to Nth session in current group (1-indexed).
		n := int(key[len(key)-1] - '0')
		if target := h.nthSessionInCurrentGroup(n); target >= 0 {
			h.jumpToIndex(target)
			return h, h.fetchSelectedPreview()
		}
		return h, nil

	case "alt+g": // First session in current group
		if target := h.firstSessionInCurrentGroup(); target >= 0 {
			h.jumpToIndex(target)
			return h, h.fetchSelectedPreview()
		}
		return h, nil

	case "alt+G": // Last session in current group
		if target := h.lastSessionInCurrentGroup(); target >= 0 {
			h.jumpToIndex(target)
			return h, h.fetchSelectedPreview()
		}
		return h, nil

	case "alt+/": // In-group filter search
		h.search.SetSize(h.width, h.height)
		h.openInGroupSearch()
		return h, nil

	case h.actionKey(hotkeyOpenShellHere):
		// Open a shell sub-session in the focused session's worktree (or
		// project path) as an iTerm2 split pane or new tmux window,
		// depending on [ui].shell_split and auto-detection. Issue #1470.
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeSession && item.Session != nil {
				return h, h.openShellHere(item.Session)
			}
		}
		// Non-session item: delegate to collapse/parent navigation so the
		// default "h" binding does not swallow left-nav on group/window rows.
		h.collapseOrNavUp()
		return h, nil

	case "shift+enter":
		// Open the focused session in a new native terminal tab (or
		// window, per [ui] iterm_open_as), leaving agent-deck running
		// here. Issue #1069 feature 2 + #1100 remote-session support,
		// credit @ddorman-dn.
		//
		// Reaching this arm at all required the #1093 fix to keyboard_compat.go
		// + normalizeMainKey: Bubble Tea v1.3.10 has no shift+enter string,
		// so we relay Shift+Enter via a Private-Use rune through the input
		// reader and rewrite it to "shift+enter" before this switch sees it.
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			openAs := resolveITermOpenAs()
			switch {
			case item.Type == session.ItemTypeSession && item.Session != nil:
				tmuxSess := item.Session.GetTmuxSession()
				if tmuxSess != nil {
					req := terminal.AttachRequest{
						Name:       tmuxSess.Name,
						SocketName: tmuxSess.SocketName,
						OpenAs:     openAs,
					}
					if err := h.openInNewWindow(req, item.Session.Exists()); err != nil {
						h.setError(fmt.Errorf("open in new window: %w", err))
					}
				}
			case item.Type == session.ItemTypeRemoteSession && item.RemoteSession != nil:
				if req, ok := buildRemoteAttachRequest(item.RemoteName, item.RemoteSession.ID, openAs); ok {
					if err := h.openInNewWindow(req, true); err != nil {
						h.setError(fmt.Errorf("open remote in new window: %w", err))
					}
				}
			}
		}
		return h, nil

	case "alt+enter":
		if !h.embeddedLayout {
			return h, nil
		}
		return h, h.attachSelectedLegacy()

	case "enter":
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeRemoteSession && item.RemoteSession != nil {
				// Shared with `session focus --remote --attach` (#2704).
				return h, h.openRemoteSessionRow(item.RemoteName, item.RemoteSession)
			}
			if !h.embeddedLayout && (item.Type == session.ItemTypeSession || item.Type == session.ItemTypeWindow || item.Type == session.ItemTypeRemoteSession) {
				return h, h.attachSelectedLegacy()
			}
			if item.Type == session.ItemTypeSession && item.Session != nil {
				if h.sessionExistsForUI(item.Session) {
					// Pane dead (process exited) — restart instead of attaching to dead pane.
					tmuxSess := item.Session.GetTmuxSession()
					if tmuxSess != nil && tmuxSess.IsPaneDead() {
						if !h.hasActiveAnimation(item.Session.ID) {
							h.resumingSessions[item.Session.ID] = time.Now()
							return h, h.restartSession(item.Session)
						}
						return h, nil
					}
					if h.enterEmbeddedMode() {
						return h, h.startEmbeddedModeCmd()
					}
					return h, nil
				}
				// Session exited (tmux session gone) — auto-restart it.
				if !h.hasActiveAnimation(item.Session.ID) {
					h.resumingSessions[item.Session.ID] = time.Now()
					return h, h.restartSession(item.Session)
				}
			} else if item.Type == session.ItemTypeGroup {
				// Toggle group on enter
				groupPath := item.Path
				h.groupTree.ToggleGroup(groupPath)
				h.rebuildFlatItems()
				for i, fi := range h.flatItems {
					if fi.Type == session.ItemTypeGroup && fi.Path == groupPath {
						h.cursor = i
						break
					}
				}
				h.saveGroupState()
				h.noteGroupToggled(groupPath)
			} else if item.Type == session.ItemTypeRemoteGroup {
				// Enter on a remote header folds it, mirroring local groups.
				// There is nothing to attach to on a header row.
				h.toggleRemoteGroup(item.RemoteName, item.Path)
			} else if item.Type == session.ItemTypeWindow {
				parentInst := h.getInstanceByID(item.WindowSessionID)
				if parentInst != nil && h.sessionExistsForUI(parentInst) && h.enterEmbeddedMode() {
					return h, h.startEmbeddedModeCmd()
				}
			}
		}
		return h, nil

	case "tab", "l", "right":
		// Expand/collapse group, or toggle session windows
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeGroup {
				groupPath := item.Path
				h.groupTree.ToggleGroup(groupPath)
				h.rebuildFlatItems()
				for i, fi := range h.flatItems {
					if fi.Type == session.ItemTypeGroup && fi.Path == groupPath {
						h.cursor = i
						break
					}
				}
				h.saveGroupState()
				h.noteGroupToggled(groupPath)
			} else if item.Type == session.ItemTypeRemoteGroup {
				h.toggleRemoteGroup(item.RemoteName, item.Path)
			} else if item.Type == session.ItemTypeSession && h.sessionHasChildren(item) {
				// Parent-child fold toggle (#2631). Children are checked BEFORE
				// window sub-items: the fold hides the whole subtree, including
				// any window rows the children would show — on a row with both,
				// window folding keeps h/left.
				sid := item.Session.ID
				h.parentChildrenCollapsed[sid] = !h.parentChildrenCollapsed[sid]
				if !h.parentChildrenCollapsed[sid] {
					// Unfolding re-opens the whole subtree, window rows included:
					// with children present this key never reaches the window
					// toggle below, so windows folded by h/left would otherwise
					// stay shut for the rest of the run.
					delete(h.windowsCollapsed, sid)
				}
				h.rebuildFlatItems()
				h.moveCursorToSession(sid)
				_ = h.saveUIStateErr() // view preference; a dropped save self-heals next toggle
			} else if item.Type == session.ItemTypeSession && h.sessionHasWindows(item) {
				sid := item.Session.ID
				h.windowsCollapsed[sid] = !h.windowsCollapsed[sid]
				h.rebuildFlatItems()
				h.moveCursorToSession(sid)
			}
		}
		return h, nil

	case "h", "left":
		// Collapse group, session windows, or navigate up
		h.collapseOrNavUp()
		return h, nil

	case "shift+up", "ctrl+up", "+", "K":
		// Move item up
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			switch item.Type {
			case session.ItemTypeRemoteSession, session.ItemTypeRemoteGroup:
				// #1875: remote rows are not Instances in groupTree, so they
				// carry a local order overlay instead. Returning here also
				// skips the forceSaveInstances below, which has nothing to do
				// with a remote reorder.
				return h, h.moveRemoteItem(item, -1)
			case session.ItemTypeGroup:
				h.groupTree.MoveGroupUp(item.Path)
				h.rebuildFlatItems()
				h.moveCursorToGroup(item.Path)
				if h.cursor >= len(h.flatItems) {
					h.cursor = max(0, len(h.flatItems)-1)
				}
			case session.ItemTypeSession:
				if item.Session != nil {
					sessionID := item.Session.ID
					h.groupTree.MoveSessionUp(item.Session)
					h.rebuildFlatItems()
					h.moveCursorToSession(sessionID)
					if h.cursor >= len(h.flatItems) {
						h.cursor = max(0, len(h.flatItems)-1)
					}
				}
			}
			// Explicit user reorder MUST persist. saveInstances() (force=false)
			// silently skips during a reload and aborts when the DB was touched
			// externally (CLI hooks, status heartbeats, conductors); the ensuing
			// reload then rebuilds the tree from stale disk and the reorder snaps
			// back on screen. forceSaveInstances() bypasses those guards so the
			// manual shift+up order is honored. Fixes #1582.
			h.forceSaveInstances()
		}
		return h, nil

	case "shift+down", "ctrl+down", "-", "J":
		// Move item down
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			switch item.Type {
			case session.ItemTypeRemoteSession, session.ItemTypeRemoteGroup:
				// #1875 — see the shift+up twin above.
				return h, h.moveRemoteItem(item, 1)
			case session.ItemTypeGroup:
				h.groupTree.MoveGroupDown(item.Path)
				h.rebuildFlatItems()
				h.moveCursorToGroup(item.Path)
				if h.cursor >= len(h.flatItems) {
					h.cursor = max(0, len(h.flatItems)-1)
				}
			case session.ItemTypeSession:
				if item.Session != nil {
					sessionID := item.Session.ID
					h.groupTree.MoveSessionDown(item.Session)
					h.rebuildFlatItems()
					h.moveCursorToSession(sessionID)
					if h.cursor >= len(h.flatItems) {
						h.cursor = max(0, len(h.flatItems)-1)
					}
				}
			}
			// Explicit user reorder MUST persist — see shift+up above. Fixes #1582.
			h.forceSaveInstances()
		}
		return h, nil

	case "shift+left":
		// Promote: outdent a sub-session to top-level peer in the same group.
		// Top-level sessions and groups are unaffected. Cross-group moves
		// stay on M.
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeSession && item.Session != nil {
				sessionID := item.Session.ID
				h.groupTree.PromoteSession(item.Session)
				h.rebuildFlatItems()
				h.moveCursorToSession(sessionID)
				if h.cursor >= len(h.flatItems) {
					h.cursor = max(0, len(h.flatItems)-1)
				}
				h.saveInstances()
			}
		}
		return h, nil

	case "shift+right":
		// Demote: nest the cursor's top-level session under the previous
		// top-level peer as that peer's last child. No-op when already a
		// sub-session, when the session has its own children (single-level
		// nesting only), or when there is no previous peer in the group.
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeSession && item.Session != nil {
				sessionID := item.Session.ID
				h.groupTree.DemoteSession(item.Session)
				h.rebuildFlatItems()
				h.moveCursorToSession(sessionID)
				if h.cursor >= len(h.flatItems) {
					h.cursor = max(0, len(h.flatItems)-1)
				}
				h.saveInstances()
			}
		}
		return h, nil

	case ",":
		// Cycle pin: off → top → bottom → off (pin-sessions #1335).
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeSession && item.Session != nil {
				inst := item.Session
				switch inst.Pin {
				case session.PinNone:
					inst.Pin = session.PinTop
				case session.PinTop:
					inst.Pin = session.PinBottom
				case session.PinBottom:
					inst.Pin = session.PinNone
				}
				h.rebuildFlatItems()
				h.moveCursorToSession(inst.ID)
				h.saveInstances()
			}
		}
		return h, nil

	case "p":
		// Edit multi-repo paths
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeSession && item.Session != nil && item.Session.IsMultiRepo() {
				h.editPathsDialog.SetSize(h.width, h.height)
				h.editPathsDialog.Show(item.Session, h.newDialog.allPathSuggestions)
			}
		}
		return h, nil

	case "P", "shift+p":
		// Edit session settings. A remote row opens the same dialog bound to
		// that remote: its slots come from the remote and the harness/account
		// switch runs there (remote_switch.go).
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeSession && item.Session != nil {
				h.editSessionDialog.SetSize(h.width, h.height)
				h.editSessionDialog.Show(item.Session)
			} else if item.Type == session.ItemTypeRemoteSession && item.RemoteSession != nil {
				return h, h.openRemoteEditSession(item)
			}
		}
		return h, nil

	case "m":
		// MCP Manager — Claude, Gemini, and Cursor Agent CLI
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeSession && item.Session != nil {
				if !session.ToolSupportsMCPManager(item.Session.Tool) {
					h.setError(fmt.Errorf("MCP management is not supported for tool %q", item.Session.Tool))
					return h, nil
				}
				h.mcpDialog.SetSize(h.width, h.height)
				if err := h.mcpDialog.Show(item.Session.ProjectPath, item.Session.ID, item.Session.Tool); err != nil {
					h.setError(err)
				}
			}
		}
		return h, nil

	case "L":
		// Plugin Manager — claude-only (RFC docs/rfc/PLUGIN_ATTACH.md).
		// Mirrors the MCP-manager UX (`m`): toggleable list of catalog
		// plugins from ~/.agent-deck/config.toml. Apply persists via
		// session.SetField(FieldPlugins,...) and triggers restart.
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeSession && item.Session != nil &&
				session.IsClaudeCompatible(item.Session.Tool) {
				h.pluginDialog.SetSize(h.width, h.height)
				if err := h.pluginDialog.Show(item.Session); err != nil {
					h.setError(err)
				}
			}
		}
		return h, nil

	case "f":
		// Quick fork session (same title with " (fork)" suffix)
		// Only available when the selected tool supports Agent Deck forking
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.IsCreatingPlaceholder() {
				h.setError(errSessionStillCreating)
				return h, nil
			}
			if item.Type == session.ItemTypeSession && item.Session != nil {
				// Block fork during animations to prevent concurrent operations
				if h.hasActiveAnimation(item.Session.ID) {
					h.setError(fmt.Errorf("session is starting, please wait..."))
					return h, nil
				}
				if item.Session.CanFork() {
					return h, h.quickForkSession(item.Session)
				}
			} else if item.Type == session.ItemTypeRemoteSession && item.RemoteSession != nil {
				// Remote session: fork it on the remote itself through its own
				// `session fork`. The remote decides title, group and whether
				// the tool is forkable; the row appears on the next fetch.
				// The in-flight guard lives in remoteForking (mirrors
				// remoteRestarting): forkingSessions is an animation map whose
				// expired entries are dropped by cleanupExpiredAnimations, and
				// remote keys are never in instanceByID, so it cannot be the guard.
				forkID := remoteRestartAnimationID(item.RemoteName, item.RemoteSession.ID)
				if _, forking := h.remoteForking[forkID]; forking {
					h.setError(fmt.Errorf("remote session is forking, please wait..."))
					return h, nil
				}
				h.remoteForking[forkID] = struct{}{}
				h.forkingSessions[forkID] = time.Now()
				return h, h.forkRemoteSession(item.RemoteName, item.RemoteSession.ID, item.RemoteSession.Title)
			}
		}
		return h, nil

	case "F", "shift+f":
		// Fork with dialog (customize title and group)
		// Only available when the selected tool supports Agent Deck forking
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.IsCreatingPlaceholder() {
				h.setError(errSessionStillCreating)
				return h, nil
			}
			if item.Type == session.ItemTypeSession && item.Session != nil {
				// Block fork during animations to prevent concurrent operations
				if h.hasActiveAnimation(item.Session.ID) {
					h.setError(fmt.Errorf("session is starting, please wait..."))
					return h, nil
				}
				if item.Session.CanFork() {
					return h, h.forkSessionWithDialog(item.Session)
				}
			} else if item.Type == session.ItemTypeRemoteSession && item.RemoteSession != nil {
				// The fork dialog inspects local git state for its worktree
				// and branch fields, which does not apply to a remote checkout.
				h.setError(fmt.Errorf("fork dialog is not available for remote sessions; press f to fork on %s", item.RemoteName))
			}
		}
		return h, nil

	case defaultHotkeyBindings[hotkeySkillsManager]:
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeSession && item.Session != nil &&
				session.SupportsProjectSkills(item.Session.Tool) {
				h.skillDialog.SetSize(h.width, h.height)
				if err := h.skillDialog.Show(item.Session.ProjectPath, item.Session.ID, item.Session.Tool); err != nil {
					h.setError(err)
				}
			}
		}
		return h, nil

	case "M", "shift+m":
		// Move session to different group
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			// Refuse mutations on a still-creating placeholder — moving one
			// would deref a nil *Instance and panic (#1540).
			if item.IsCreatingPlaceholder() {
				h.setError(errSessionStillCreating)
			} else if item.Type == session.ItemTypeSession {
				h.groupDialog.ShowMove(h.scopedGroupPaths())
			} else if item.Type == session.ItemTypeRemoteSession && item.RemoteSession != nil {
				// Remote sessions live in the remote's own state DB, so the
				// local tree cannot move them. Offer the group paths currently
				// observed on that remote (the only groups the remote knows
				// about); the confirmed move is routed over SSH in
				// GroupDialogMove (see moveRemoteSessionToGroup).
				h.groupDialog.ShowMove(h.remoteGroupPaths(item.RemoteName))
			}
		}
		return h, nil

	case "b":
		// Re-run worktree setup script (bootstrap)
		if h.cursor >= len(h.flatItems) {
			return h, nil
		}
		item := h.flatItems[h.cursor]
		if item.Type != session.ItemTypeSession || item.Session == nil {
			return h, nil
		}
		inst := item.Session
		if !inst.IsWorktree() {
			h.setError(fmt.Errorf("session '%s' is not a worktree", inst.Title))
			return h, nil
		}
		if _, running := h.setupRunningSessions[inst.ID]; running {
			h.setError(fmt.Errorf("setup script already running for '%s'", inst.Title))
			return h, nil
		}
		h.setupRunningSessions[inst.ID] = time.Now()
		return h, h.runWorktreeSetup(inst)

	case "W", "shift+w":
		// Worktree finish - merge + cleanup for worktree sessions
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeSession && item.Session != nil {
				inst := item.Session
				if !inst.IsWorktree() {
					h.setError(fmt.Errorf("session '%s' is not a worktree", inst.Title))
					return h, nil
				}
				// Determine default target branch
				defaultBranch := "main"
				if detected, err := git.GetDefaultBranch(inst.WorktreeRepoRoot); err == nil {
					defaultBranch = detected
				}
				h.worktreeFinishDialog.SetSize(h.width, h.height)
				h.worktreeFinishDialog.Show(inst.ID, inst.Title, inst.WorktreeBranch, inst.WorktreeRepoRoot, inst.WorktreePath, defaultBranch)
				// Trigger async dirty check
				sid := inst.ID
				wtPath := inst.WorktreePath
				return h, func() tea.Msg {
					dirty, err := git.HasUncommittedChanges(wtPath)
					return worktreeDirtyCheckMsg{sessionID: sid, isDirty: dirty, err: err}
				}
			}
		}
		return h, nil

	case "g":
		// New group, with context-aware Tab toggle (Issue #111). 'g' always
		// opens the dialog immediately; jump-to-top lives on "home", not on
		// a "gg" chord a single-key 'g' binding can never reach.
		// - Group header: defaults to subgroup, Tab toggles to root
		// - Grouped session: defaults to root, Tab toggles to subgroup
		// - Ungrouped item: root only, no toggle
		if h.groupScope != "" {
			// Scoped mode: create subgroups under scope root or its children
			if h.cursor < len(h.flatItems) {
				item := h.flatItems[h.cursor]
				if item.Type == session.ItemTypeGroup {
					h.groupDialog.ShowCreateSubgroup(item.Group.Path, item.Group.Name)
				} else {
					h.groupDialog.ShowCreateSubgroup(h.groupScope, h.groupScopeDisplayName())
				}
			} else {
				h.groupDialog.ShowCreateSubgroup(h.groupScope, h.groupScopeDisplayName())
			}
		} else if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeRemoteGroup {
				// Remote group header (incl. the level-0 host header): create
				// the group on the remote itself. The parent defaults to the
				// header's own group path so Enter creates a remote subgroup;
				// Tab toggles to root-level remote create (issue #111 parity).
				parentPath := remoteGroupPathFromItem(item)
				parentName := parentPath
				if idx := strings.LastIndex(parentPath, "/"); idx >= 0 {
					parentName = parentPath[idx+1:]
				}
				h.groupDialog.ShowCreateRemoteWithContext(parentPath, parentName, item.RemoteName)
			} else if item.Type == session.ItemTypeRemoteSession && item.RemoteSession != nil {
				// Remote session: default to a root-level group on the remote;
				// Tab toggles to a subgroup under the session's current group.
				if gPath := item.RemoteSession.Group; gPath != "" {
					gName := gPath
					if idx := strings.LastIndex(gPath, "/"); idx >= 0 {
						gName = gPath[idx+1:]
					}
					h.groupDialog.ShowCreateRemoteWithContextDefaultRoot(gPath, gName, item.RemoteName)
				} else {
					// Ungrouped remote session: root only, no toggle (mirrors
					// the local ungrouped case below).
					h.groupDialog.ShowCreateRemoteWithContext("", "", item.RemoteName)
				}
			} else if item.Type == session.ItemTypeGroup {
				// On group header: default to subgroup mode
				h.groupDialog.ShowCreateWithContext(item.Group.Path, item.Group.Name)
			} else if item.Type == session.ItemTypeSession && item.Session != nil && item.Session.GroupPath != "" {
				// On grouped session: default to root, Tab toggles to subgroup
				gPath := item.Session.GroupPath
				gName := gPath
				if idx := strings.LastIndex(gPath, "/"); idx >= 0 {
					gName = gPath[idx+1:]
				}
				h.groupDialog.ShowCreateWithContextDefaultRoot(gPath, gName)
			} else {
				// Ungrouped: root only, no toggle
				h.groupDialog.ShowCreateWithContext("", "")
			}
		} else {
			h.groupDialog.ShowCreateWithContext("", "")
		}
		return h, nil

	case "r":
		// Rename group or session
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.IsCreatingPlaceholder() {
				h.setError(errSessionStillCreating)
				return h, nil
			}
			if item.Type == session.ItemTypeGroup {
				h.groupDialog.ShowRename(item.Path, item.Group.Name)
			} else if item.Type == session.ItemTypeSession && item.Session != nil {
				h.groupDialog.ShowRenameSession(item.Session.ID, item.Session.Title)
			} else if item.Type == session.ItemTypeRemoteSession && item.RemoteSession != nil {
				h.groupDialog.ShowRenameSession("remote:"+item.RemoteName+":"+item.RemoteSession.ID, item.RemoteSession.Title)
			}
		}
		return h, nil

	case "/":
		// The quick local title filter; Tab from it reaches Recall, and G
		// opens Recall directly.
		h.search.SetScopedGroup("")
		h.search.SetItems(h.instances)
		h.search.Show()
		return h, nil

	case "?":
		h.helpOverlay.SetEmbeddedLayout(h.embeddedLayout)
		h.helpOverlay.SetSize(h.width, h.height)
		h.helpOverlay.Show()
		return h, nil

	case "<":
		// Sessions/Preview split: shrink preview by previewPctStep (#1092).
		// Works in dual (horizontal) and stacked (vertical) layouts — the
		// same previewPct drives both splits. Single layout has nothing to
		// adjust.
		if mode := h.getLayoutMode(); mode == LayoutModeDual || mode == LayoutModeStacked {
			h.adjustPreviewPct(-previewPctStep)
		}
		return h, nil

	case ">":
		// Sessions/Preview split: grow preview by previewPctStep (#1092).
		if mode := h.getLayoutMode(); mode == LayoutModeDual || mode == LayoutModeStacked {
			h.adjustPreviewPct(previewPctStep)
		}
		return h, nil

	case "O":
		// Toggle preview-pane orientation on wide terminals: side-by-side
		// (right) <-> stacked (below). Persists to config.toml. No-op visual
		// effect on narrow terminals (always stacked) but the preference is
		// still recorded for when the terminal widens. (`v` cycles preview
		// content mode and `V` copies the visible pane — both distinct.)
		h.togglePreviewOrientation()
		return h, nil

	case "S":
		// Open settings panel
		h.settingsPanel.Show()
		h.settingsPanel.SetSize(h.width, h.height)
		return h, nil

	case "w":
		// Open watcher panel
		h.refreshWatcherPanel()
		h.watcherPanel.Show()
		h.watcherPanel.SetSize(h.width, h.height)
		return h, nil

	case defaultHotkeyBindings[hotkeyDeadLetters]:
		// Dead-letter management is always available; an empty panel states that
		// the warning is clear instead of making the shortcut mysteriously inert.
		if h.deadLetterPanel == nil {
			h.deadLetterPanel = NewDeadLetterPanel()
		}
		h.deadLetterPanel.Show()
		h.deadLetterPanel.SetSize(h.width, h.height)
		return h, nil

	case defaultHotkeyBindings[hotkeySessionRef]:
		// Pick the selected session as a reference source (ASK-111); the
		// drop runs from handleSessionRefPickKey.
		h.startSessionRefPick()
		return h, nil

	case defaultHotkeyBindings[hotkeyTriggers]:
		// What wakes sessions (#2702). On a session a trigger wakes (its row
		// shows ⚡N) this opens that session's reverse view; esc widens it to
		// the full list.
		return h, h.openTriggersPanel()

	case defaultHotkeyBindings[hotkeyAgentsPanel]:
		// Open the Agents tab. Opt-in by presence: with nothing adopted there
		// is no panel to open and the key stays inert, so a zero-config deck
		// is unchanged.
		h.refreshAgentsPanel()
		if !h.agentsPanel.HasAgents() && h.agentsLoadError == "" {
			return h, nil
		}
		h.agentsPanel.Show()
		h.agentsPanel.SetSize(h.width, h.height)
		return h, nil

	case "E":
		// Exec an interactive shell inside the sandbox container.
		if selected := h.getSelectedSession(); selected != nil && selected.IsSandboxed() &&
			selected.SandboxContainer != "" {
			// OpenContainerShell runs docker/tmux setup first; keys typed meanwhile belong to the shell (see holdInputForExec).
			defer holdInputForExec()()
			tmuxName, shellErr := selected.OpenContainerShell()
			if shellErr != nil {
				h.setError(shellErr)
				return h, nil
			}
			termSession := &tmux.Session{Name: tmuxName}
			h.isAttaching.Store(true)
			return h, execHoldingInput(attachCmd{
				session: termSession,
				opts:    tmux.AttachOptions{DetachByte: h.detachByte()},
				onExit:  func() { h.isAttaching.Store(false) },
			}, func(err error) tea.Msg {
				h.isAttaching.Store(false)
				return statusUpdateMsg{}
			})
		}
		return h, nil

	case "n":
		// Reset any stale remote target from a previously abandoned flow.
		h.pendingRemoteName = ""
		// If the cursor is on a remote group/session, open the same
		// new-session dialog as for local items but remember the remote
		// target (#1353). The submit handler routes the create to the remote
		// via SSH with the chosen tool, so the #743 invariant still holds:
		// the session is never created on localhost. (Previously this
		// quick-created a shell on the remote with no tool selection.)
		if h.cursor >= 0 && h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeRemoteGroup || item.Type == session.ItemTypeRemoteSession {
				return h, h.showRemoteNewSessionDialog(item)
			}
		}

		// Collect unique project paths sorted by most recently accessed
		type pathInfo struct {
			path           string
			lastAccessedAt time.Time
		}
		pathMap := make(map[string]*pathInfo)
		for _, inst := range h.instances {
			if inst.ProjectPath == "" {
				continue
			}
			// Prefer the original repo root over worktree paths so suggestions
			// don't show ephemeral worktree directories.
			p := inst.ProjectPath
			if inst.WorktreeRepoRoot != "" {
				p = inst.WorktreeRepoRoot
			}
			existing, ok := pathMap[p]
			if !ok {
				// First time seeing this path.
				accessTime := inst.LastAccessedAt
				if accessTime.IsZero() {
					accessTime = inst.CreatedAt // Fall back to creation time.
				}
				pathMap[p] = &pathInfo{
					path:           p,
					lastAccessedAt: accessTime,
				}
			} else {
				// Update if this instance was accessed more recently.
				accessTime := inst.LastAccessedAt
				if accessTime.IsZero() {
					accessTime = inst.CreatedAt
				}
				if accessTime.After(existing.lastAccessedAt) {
					existing.lastAccessedAt = accessTime
				}
			}
		}

		// Convert to slice and sort by most recent first
		pathInfos := make([]*pathInfo, 0, len(pathMap))
		for _, info := range pathMap {
			pathInfos = append(pathInfos, info)
		}
		sort.Slice(pathInfos, func(i, j int) bool {
			return pathInfos[i].lastAccessedAt.After(pathInfos[j].lastAccessedAt)
		})

		// Extract sorted paths
		paths := make([]string, len(pathInfos))
		for i, info := range pathInfos {
			paths[i] = info.path
		}
		h.newDialog.SetPathSuggestions(paths)

		// Load recent sessions for the picker
		if recents, err := h.storage.LoadRecentSessions(); err == nil {
			h.newDialog.SetRecentSessions(recents)
		}

		// Apply the preselected tool: explicit [default_tool] config wins,
		// otherwise fall back to the last successfully-submitted tool remembered
		// in the profile StateDB (UX top-3 #2). First run (neither set) leaves
		// shell selected, unchanged.
		h.newDialog.SetDefaultTool(resolveInitialTool(session.GetDefaultTool(), rememberedTool(h.stateDB())))

		// Auto-select parent group from current cursor position
		groupPath := session.DefaultGroupPath
		groupName := session.DefaultGroupName
		if h.groupScope != "" {
			// Scoped mode: default to scope root
			groupPath = h.groupScope
			if group, exists := h.groupTree.Groups[h.groupScope]; exists {
				groupName = group.Name
			}
		}
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			switch item.Type {
			case session.ItemTypeGroup:
				groupPath = item.Group.Path
				groupName = item.Group.Name
			case session.ItemTypeSession:
				// Use the session's group
				groupPath = item.Path
				if group, exists := h.groupTree.Groups[groupPath]; exists {
					groupName = group.Name
				}
			}
		}
		defaultPath := h.getDefaultPathForGroup(groupPath)
		conductors := h.activeConductorSessions()
		suggestedParentID := h.suggestConductorParent()
		h.newDialog.ShowInGroup(groupPath, groupName, defaultPath, conductors, suggestedParentID)
		return h, nil

	case "N":
		// Check if cursor is on a remote group/session — create on remote instead
		if h.cursor >= 0 && h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeRemoteGroup || item.Type == session.ItemTypeRemoteSession {
				return h, h.createRemoteSession(item.RemoteName)
			}
		}
		// Quick create: auto-generated name, smart defaults from group context
		return h, h.quickCreateSession()

	case "z":
		h.zoxidePicker.SetSize(h.width, h.height)
		h.zoxidePicker.SetSuggestProvider(h.pathSuggestProvider())
		h.zoxidePicker.Show()
		return h, nil

	case "d":
		// Show confirmation dialog before deletion (prevents accidental deletion)
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeSession && item.Session != nil {
				h.confirmDialog.ShowDeleteSession(item.Session.ID, item.Session.Title, item.Session.IsSandboxed(), item.Session.IsWorktree())
			} else if item.Type == session.ItemTypeWindow {
				// The dialog names exactly the row the user is looking at:
				// id and name come from the same cached WindowInfo, never a
				// live lookup by index (the cache can be up to a tick stale,
				// so the live window at this index may already be a
				// different one — see tmux.KillWindow).
				if item.WindowID == "" {
					h.setError(fmt.Errorf("kill window %d: window id not known yet, try again", item.WindowIndex))
				} else {
					h.confirmDialog.ShowKillWindow(item.WindowSessionID, item.WindowIndex, item.WindowName, item.WindowID)
				}
			} else if item.Type == session.ItemTypeRemoteSession && item.RemoteSession != nil {
				h.confirmDialog.ShowDeleteRemoteSession(item.RemoteName, item.RemoteSession.ID, item.RemoteSession.Title)
			} else if item.Type == session.ItemTypeRemoteGroup && item.Level > 0 {
				// One of the remote's own groups: delete it there, the way d on
				// a local group header does here. The Level-0 host header is a
				// local UI bucket and has nothing to delete.
				gp := remoteGroupPathFromItem(item)
				h.confirmDialog.ShowDeleteRemoteGroup(item.RemoteName, gp, displayGroupName(gp))
			} else if item.Type == session.ItemTypeGroup && item.Path == session.DefaultGroupPath {
				// Protected default group: surface the block in the same centered modal
				// used for the delete confirmation, so it can't be clamped off the bottom
				// of the viewport like a transient error banner. Checked before the
				// scoped-root case so the message stays specific even when the TUI is
				// scoped to the default group (groupScope == DefaultGroupPath), where both
				// conditions would match.
				h.confirmDialog.ShowNotice(
					"⚠  Can't Delete Group",
					fmt.Sprintf("%q is the default\ngroup and can't be deleted.\n\nSessions always need a home.", session.DefaultGroupName),
				)
			} else if item.Type == session.ItemTypeGroup && item.Path != h.groupScope {
				h.confirmDialog.ShowDeleteGroup(item.Path, item.Group.Name)
			} else if item.Type == session.ItemTypeGroup && item.Path == h.groupScope {
				h.setError(fmt.Errorf("cannot delete the scoped root group"))
			}
		}
		return h, nil

	case "D":
		// Close session process without deleting metadata from the list/storage.
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeSession && item.Session != nil {
				h.confirmDialog.ShowCloseSession(item.Session.ID, item.Session.Title, item.Session.IsSandboxed())
			} else if item.Type == session.ItemTypeRemoteSession && item.RemoteSession != nil {
				h.confirmDialog.ShowCloseRemoteSession(item.RemoteName, item.RemoteSession.ID, item.RemoteSession.Title)
			}
		}
		return h, nil

	case "A":
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeSession && item.Session != nil && !item.Session.IsArchived() {
				h.confirmDialog.ShowArchiveSession(item.Session.ID, item.Session.Title)
			} else if item.Type == session.ItemTypeRemoteSession && item.RemoteSession != nil && !item.RemoteSession.Archived {
				h.confirmDialog.ShowArchiveRemoteSession(item.RemoteName, item.RemoteSession.ID, item.RemoteSession.Title)
			}
		}
		return h, nil

	case "shift+u":
		if h.statusFilter != FilterModeArchived {
			return h, nil
		}
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeSession && item.Session != nil && item.Session.IsArchived() {
				h.confirmDialog.ShowUnarchiveSession(item.Session.ID, item.Session.Title)
			} else if item.Type == session.ItemTypeRemoteSession && item.RemoteSession != nil && item.RemoteSession.Archived {
				h.confirmDialog.ShowUnarchiveRemoteSession(item.RemoteName, item.RemoteSession.ID, item.RemoteSession.Title)
			}
		}
		return h, nil

	case "X":
		// Status-gated registry-only remove. For stopped/errored sessions only;
		// use 'd' for destructive delete (kills process + removes worktree).
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeSession && item.Session != nil {
				status := item.Session.Status
				if status == session.StatusStopped || status == session.StatusError {
					h.confirmDialog.ShowRemoveSession(item.Session.ID, item.Session.Title)
				} else {
					h.setError(fmt.Errorf("session must be stopped or errored to remove; use 'd' to destructively delete a %s session", status))
				}
			}
		}
		return h, nil

	case "ctrl+x":
		// Bulk remove all errored sessions from the registry.
		count := 0
		h.instancesMu.RLock()
		for _, inst := range h.instances {
			if inst.Status == session.StatusError {
				count++
			}
		}
		h.instancesMu.RUnlock()
		if count == 0 {
			h.setError(fmt.Errorf("no errored sessions to remove"))
			return h, nil
		}
		h.confirmDialog.ShowBulkRemoveErrored(count)
		return h, nil

	case "i":
		return h, h.importSessions

	case "I":
		// Enter insert mode (#1069 feature 1): subsequent keystrokes are
		// routed to the currently-selected session's tmux pane. Esc exits.
		// `i` is taken by import; `I` follows the vim convention (Insert).
		if h.enterInsertMode() {
			return h, nil
		}
		return h, nil

	case "u":
		// Mark session as unread (idle → waiting)
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			// #2164: on a remote host header that runs an older release, u
			// offers the update instead. A header without drift is a no-op.
			if item.Type == session.ItemTypeRemoteGroup && item.Level == 0 {
				if state, ok := h.remoteVersionState(item.RemoteName); ok && state.Outdated(Version) {
					h.confirmDialog.ShowUpdateRemote(item.RemoteName, state.Version, Version)
				}
				return h, nil
			}
			if item.Type == session.ItemTypeSession && item.Session != nil {
				tmuxSess := item.Session.GetTmuxSession()
				if tmuxSess != nil {
					tmuxSess.ResetAcknowledged()
					// Persist to SQLite so background sync doesn't overwrite.
					//
					// Adopt the write: it moves last_modified, and without that
					// the save below reads this TUI's OWN bump as an external
					// change, aborts, and reloads instead. The reload rebuilds
					// acknowledged from the stored status -- still "idle",
					// precisely because the save that would have written
					// "waiting" was the one that aborted -- so the row went
					// straight back to gray and the key looked like it did
					// nothing. Same self-inflicted false positive as #1868.
					if db := statedb.GetGlobal(); db != nil {
						if stamps, err := db.SetAcknowledgedStamped(item.Session.ID, false); err == nil {
							h.adoptOwnWrite(stamps, "mark_unread")
						}
					}
					// Clear idle optimization so UpdateStatus does a full check
					item.Session.ForceNextStatusCheck()
					_ = item.Session.UpdateStatus()
					h.saveInstances()
				}
			}
		}
		return h, nil

	case defaultHotkeyBindings[hotkeyQuickApprove]:
		// Quick approve: send "1" + Enter to the highlighted Claude session/window
		// without attaching. Gated to Claude-compatible tools so a stray press
		// on a vim/shell session cannot dump a "1" into the buffer. No status
		// guard - Bash-tool permission prompts in Claude Code don't transition
		// the session to StatusWaiting, so guarding on it would defeat the
		// most common use case. Matches the existing "agent-deck session send"
		// CLI, which has no status guard either.
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			switch item.Type {
			case session.ItemTypeWindow:
				// On a window sub-row, approve the window the cursor is on. Gate
				// on that window's detected tool (WindowTool) rather than the
				// session's stored Tool — a session created as "shell" that later
				// runs claude in an added window keeps Tool="shell", so the
				// session-level gate would wrongly reject it. Target the window by
				// index so a prompt in a non-active window is reachable. (#1369)
				if session.IsClaudeCompatible(item.WindowTool) {
					h.quickApprove(h.getInstanceByID(item.WindowSessionID), item.WindowIndex)
				}
			case session.ItemTypeSession:
				if item.Session != nil && session.IsClaudeCompatible(item.Session.Tool) {
					h.quickApprove(item.Session, -1)
				}
			}
		}
		return h, nil

	case defaultHotkeyBindings[hotkeyPromptSession], defaultHotkeyBindings[hotkeyQueueMessage]:
		// Send or queue without attaching. Window rows target the parent
		// session's managed agent pane, as session send does.
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			switch item.Type {
			case session.ItemTypeWindow:
				h.openPromptInput(h.getInstanceByID(item.WindowSessionID))
			case session.ItemTypeSession:
				h.openPromptInput(item.Session)
			}
			if h.promptInputDialog.IsVisible() {
				h.promptInputDialog.queue = key == defaultHotkeyBindings[hotkeyQueueMessage]
			}
		}
		return h, nil

	case " ":
		if len(h.flatItems) > 0 {
			h.jumpMode = true
			h.jumpBuffer = ""
		}
		return h, nil

	case "v":
		// Toggle preview mode (cycle: both → output-only → analytics-only → both)
		h.previewMode = (h.previewMode + 1) % 3
		return h, nil

	case "alt+v":
		if !h.embeddedLayout {
			return h, nil
		}
		selectedBefore := h.captureSelectedItemIdentity()
		if h.sidebarMode == sidebarGrouped {
			h.sidebarMode = sidebarFlat
		} else {
			h.sidebarMode = sidebarGrouped
		}
		h.rebuildFlatItemsPreservingSelection(selectedBefore)
		h.skipDivider(1)
		h.saveUIState()
		return h, h.fetchSelectedPreview()

	case "t":
		// Cycle list partition: normal → active-on-top → populated-on-top → normal.
		// Preserve the cursor's row identity across the rebuild.
		selectedBefore := h.captureSelectedItemIdentity()
		h.groupViewMode = session.GroupViewMode((int(h.groupViewMode) + 1) % session.GroupViewModeCount)
		h.rebuildFlatItemsPreservingSelection(selectedBefore)
		h.skipDivider(1)
		h.syncViewport()
		h.saveUIState()
		return h, h.fetchSelectedPreview()

	case "*":
		// Cycle time-range filter: all → today → 3 days → 7 days → 30 days → all.
		// Preserve the cursor's row identity across the rebuild, same as the
		// 't' view-mode cycle above.
		selectedBefore := h.captureSelectedItemIdentity()
		h.timeFilter = session.TimeFilterMode((int(h.timeFilter) + 1) % session.TimeFilterModeCount)
		h.keepEmptyFilter = true
		h.rebuildFlatItemsPreservingSelection(selectedBefore)
		h.syncViewport()
		h.saveUIState()
		return h, h.fetchSelectedPreview()

	case "y":
		// Toggle YOLO mode for Gemini or Codex sessions (requires restart)
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeSession && item.Session != nil {
				inst := item.Session
				toggled := false

				switch inst.Tool {
				case "gemini":
					currentYolo := false
					if inst.GeminiYoloMode != nil {
						currentYolo = *inst.GeminiYoloMode
					} else {
						userConfig, _ := session.LoadUserConfig()
						if userConfig != nil {
							currentYolo = userConfig.Gemini.YoloMode
						}
					}
					newYolo := !currentYolo
					inst.GeminiYoloMode = &newYolo
					toggled = true

				case "codex":
					currentYolo := false
					opts := inst.GetCodexOptions()
					if opts != nil && opts.YoloMode != nil {
						currentYolo = *opts.YoloMode
					} else {
						userConfig, _ := session.LoadUserConfig()
						if userConfig != nil {
							currentYolo = userConfig.Codex.YoloMode
						}
					}
					newYolo := !currentYolo
					if opts == nil {
						opts = &session.CodexOptions{}
					}
					opts.YoloMode = &newYolo
					_ = inst.SetCodexOptions(opts)
					toggled = true

				case "hermes":
					currentYolo := false
					opts := inst.GetHermesOptions()
					if opts != nil && opts.YoloMode != nil {
						currentYolo = *opts.YoloMode
					} else {
						userConfig, _ := session.LoadUserConfig()
						if userConfig != nil {
							currentYolo = userConfig.Hermes.YoloMode
						}
					}
					newYolo := !currentYolo
					if opts == nil {
						opts = &session.HermesOptions{}
					}
					opts.YoloMode = &newYolo
					_ = inst.SetHermesOptions(opts)
					toggled = true
				}

				if toggled {
					h.saveInstances()
					if inst.GetStatusThreadSafe() == session.StatusRunning ||
						inst.GetStatusThreadSafe() == session.StatusWaiting {
						h.resumingSessions[inst.ID] = time.Now()
						return h, h.restartSession(inst)
					}
				}
			}
		}
		return h, nil

	case "R":
		// Restart session (recreate tmux session with resume)
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeRemoteGroup && item.Level == 0 {
				return h, h.retryRemotePoll(item.RemoteName)
			}
			if item.Type == session.ItemTypeSession && item.Session != nil {
				// Block restart during animations to prevent concurrent restarts
				if h.hasActiveAnimation(item.Session.ID) {
					h.setError(fmt.Errorf("session is starting, please wait..."))
					return h, nil
				}
				if item.Session.CanRestart() {
					// Track as resuming for animation (before async call starts)
					h.resumingSessions[item.Session.ID] = time.Now()
					return h, h.restartSession(item.Session)
				}
			} else if item.Type == session.ItemTypeRemoteSession && item.RemoteSession != nil {
				restartID := remoteRestartAnimationID(item.RemoteName, item.RemoteSession.ID)
				if _, restarting := h.remoteRestarting[restartID]; restarting {
					h.setError(fmt.Errorf("remote session is restarting, please wait..."))
					return h, nil
				}
				h.remoteRestarting[restartID] = struct{}{}
				h.resumingSessions[restartID] = time.Now()
				return h, h.restartRemoteSession(item.RemoteName, item.RemoteSession.ID, item.RemoteSession.Title)
			}
		}
		return h, nil

	case "T":
		// Restart session fresh (discard current tool session binding first)
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeSession && item.Session != nil {
				if h.hasActiveAnimation(item.Session.ID) {
					h.setError(fmt.Errorf("session is starting, please wait..."))
					return h, nil
				}
				if item.Session.CanRestartFresh() {
					h.resumingSessions[item.Session.ID] = time.Now()
					return h, h.restartSessionFresh(item.Session)
				}
			}
		}
		return h, nil

	case "c":
		// Copy last AI response to system clipboard
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeSession && item.Session != nil {
				return h, h.copySessionOutput(item.Session)
			}
		}
		return h, nil

	case defaultHotkeyBindings[hotkeyContextInspector]:
		// Open the full-context inspector for the selected session: everything
		// the harness is being sent, ranked by what it costs, with a lever per
		// item. The key reaches this canonical case through the configurable
		// hotkey lookup, so a user who rebinds context_inspector lands here too.
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeSession && item.Session != nil {
				return h, h.openContextInspector(item.Session)
			}
			// The inspector reads the harness transcript from local files; a
			// remote row has none here, and a per-open SSH call would add remote
			// load (#2332). Say so instead of silently doing nothing.
			if item.Type == session.ItemTypeRemoteSession && item.RemoteSession != nil {
				h.setError(fmt.Errorf("context inspector: not available for remote sessions yet"))
			}
		}
		return h, nil

	case defaultHotkeyBindings[hotkeyCopyInfo]:
		// Copy preview pane info (Repo / Path / Branch) to system clipboard (#791).
		// Pairs with `c` (copy session output): same fallback chain, different
		// payload. It moved off "C" when the context inspector took that key and
		// is rebindable via [hotkeys].copy_info.
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeSession && item.Session != nil {
				return h, h.copySessionInfo(item.Session)
			}
		}
		return h, nil

	case defaultHotkeyBindings[hotkeyCopyPane]:
		// Copy the selected local session's current visible tmux pane. The key
		// reaches this canonical case through the configurable hotkey lookup.
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeSession && item.Session != nil {
				return h, h.copyVisiblePane(item.Session)
			}
		}
		return h, nil

	case "Y", "shift+y":
		// Extract fenced code blocks from this session's recent output and
		// copy one (OSC52, SSH-safe). Single block -> copy directly; multiple
		// -> open the picker. Pairs with `c`/`C` in the copy family (#1412).
		// `y` (lowercase) is the YOLO toggle, so this uses the shift variant.
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeSession && item.Session != nil {
				return h, h.startCodeBlockCopy(item.Session)
			}
		}
		return h, nil

	case "x":
		// Send session output to another session
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeSession && item.Session != nil {
				others := h.getOtherActiveSessions(item.Session.ID)
				if len(others) == 0 {
					h.setError(fmt.Errorf("no other sessions to send to"))
					return h, nil
				}
				h.sessionPickerDialog.SetSize(h.width, h.height)
				h.sessionPickerDialog.Show(item.Session, h.instances)
			}
		}
		return h, nil

	case "e":
		if config, _ := session.LoadUserConfig(); config != nil && !config.GetShowNotes() {
			return h, nil
		}
		if h.getLayoutMode() == LayoutModeSingle {
			h.setError(fmt.Errorf("notes editor is unavailable in single-column layout"))
			return h, nil
		}
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeSession && item.Session != nil {
				h.beginNotesEditing(item.Session)
			}
		}
		return h, nil

	case "ctrl+g":
		// Open Gemini model selection dialog (only for Gemini sessions)
		if inst := h.getSelectedSession(); inst != nil && inst.Tool == "gemini" {
			cmd := h.geminiModelDialog.Show(inst.ID, inst.GeminiModel)
			return h, cmd
		}
		return h, nil

	case "ctrl+z":
		// Undo last session delete (Chrome-style: restores in reverse order)
		if len(h.undoStack) == 0 {
			h.setError(fmt.Errorf("nothing to undo"))
			return h, nil
		}
		h.undoMu.Lock()
		entry := h.undoStack[len(h.undoStack)-1]
		h.undoStack = h.undoStack[:len(h.undoStack)-1]
		h.undoMu.Unlock()
		inst := entry.instance
		return h, func() tea.Msg {
			// Restart reuses the ID and can write anchors before the registry row
			// is restored. Finish deleting old artifacts before creating new ones.
			if entry.cleanupDone != nil {
				<-entry.cleanupDone
			}
			err := inst.Restart()
			return sessionRestoredMsg{
				instance: inst,
				err:      err,
				warning:  inst.ConsumeCodexRestartWarning(),
			}
		}

	case "ctrl+r":
		// Manual refresh follows the same ordering as initial and watcher loads.
		// Remote decks refresh in the same keystroke, so a change made on a
		// remote (or from another machine) shows up now, not at the next poll.
		state := h.preserveState()
		// The user asked for a refresh now: a host held by poll backoff is
		// polled at once (still one poll per host at a time).
		h.clearRemotePollBackoff("")
		h.remoteSessionsMu.Lock()
		h.remotesFetchActive = true // so the remote headers show "refreshing…"
		h.remoteSessionsMu.Unlock()
		return h, tea.Batch(h.sessionLoadCmd(&state, false), h.fetchRemoteSessions)

	case "ctrl+t":
		// Restart the TUI in place so an update installed while it was open
		// takes effect (restart.go). Refuses with a footer message while a
		// dialog is open or a session action is running.
		return h.tryRestartDeck()

	case "ctrl+y":
		// Run `agent-deck update` on the terminal (TUI suspended); a
		// successful install flips the banner to the restart hint.
		return h.tryInstallUpdate()

	case "ctrl+s":
		// Open the session switcher from the overview too, with the same key
		// used while attached. Pre-highlight the session under the cursor (if
		// any) so it lines up with what the user is already looking at; Esc just
		// closes (we're not attached, so there is nothing to re-attach to).
		fromID := ""
		if sel := h.getSelectedSession(); sel != nil {
			fromID = sel.ID
		}
		h.openSessionSwitcher(fromID, false)
		return h, nil

	case "`":
		// Alternate-session toggle (#2058): swap to the session you were on
		// immediately before this one, vim Ctrl+^ style.
		return h.handleAltSessionToggle()

	case "alt+left":
		// MRU walk back (#2058): step to the previous session in visit order.
		return h.handleMRUWalk(false)

	case "alt+right":
		// MRU walk forward (#2058): redo — step to the next session in visit order.
		return h.handleMRUWalk(true)

	case defaultHotkeyBindings[hotkeyReportProblem]:
		// "Report a problem": form -> full preview -> explicit confirm -> file
		// through internal/issuereport (gh when logged in, else the browser).
		if h.reportDialog != nil {
			tool := ""
			if h.cursor >= 0 && h.cursor < len(h.flatItems) {
				if inst := h.flatItems[h.cursor].Session; inst != nil {
					tool = inst.Tool
				}
			}
			h.reportDialog.Show(tool)
			h.reportDialog.SetSize(h.width, h.height)
		}
		return h, nil

	case "ctrl+e":
		// Open feedback dialog on demand (per D-11: bypasses ShouldShow -- user-initiated)
		if h.feedbackDialog != nil {
			st := h.feedbackState
			if st == nil {
				// Lazy-load state: h.feedbackState may be nil if the user already rated
				// this version (auto-popup path skips loading state in that case).
				// If load fails, create a safe default so Show() receives a non-nil pointer.
				loaded, err := feedback.LoadState()
				if err == nil {
					h.feedbackState = loaded
					st = loaded
				} else {
					uiLog.Warn("feedback: failed to load state for on-demand shortcut", "err", err)
					h.feedbackState = &feedback.State{FeedbackEnabled: true, MaxShows: 3}
					st = h.feedbackState
				}
			}
			if h.feedbackSender == nil {
				h.feedbackSender = feedback.NewSender()
			}
			// v1.7.38: ctrl+e is explicit user intent. If the user previously
			// opted out (via CLI decline or TUI stepConfirm decline), re-enable
			// the state in memory + on disk BEFORE calling Show() so the new
			// "Show() no-ops on opt-out" guard does not block this path.
			if st != nil && !st.FeedbackEnabled {
				st.FeedbackEnabled = true
				_ = feedback.SaveState(st)
			}
			h.feedbackDialog.Show(Version, st, h.feedbackSender)
			h.feedbackDialog.SetSize(h.width, h.height)
		}
		return h, nil

	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		// Quick jump to Nth root group (1-indexed)
		targetNum := int(key[0] - '0') // Convert "1" -> 1, "2" -> 2, etc.
		h.jumpToRootGroup(targetNum)
		return h, nil

	case "0":
		// Clear status filter (show all)
		return h, h.changeStatusFilter("")

	case "!", "shift+1":
		// Filter to running sessions only
		return h, h.changeStatusFilter(session.StatusRunning)

	case "@", "shift+2":
		// Filter to waiting sessions only
		return h, h.changeStatusFilter(session.StatusWaiting)

	case "#", "shift+3":
		// Filter to idle sessions only
		return h, h.changeStatusFilter(session.StatusIdle)

	case CostDashboardKey, "shift+4":
		// Cost dashboard (when cost tracking is active).
		if h.costStore == nil {
			h.setError(fmt.Errorf("Cost Dashboard unavailable: state database is missing; restart agent-deck with a writable config directory to enable it"))
			return h, nil
		}
		h.showCostDashboard = true
		h.costDashboard = newCostDashboard(h.costStore, h.width, h.height)
		return h, nil

	case FilterKeyError, "shift+7":
		// Filter to error sessions only.
		return h, h.changeStatusFilter(session.StatusError)

	case FilterKeyActive, "shift+5":
		// Filter to open sessions (excludes error/stopped)
		return h, h.changeStatusFilter(FilterModeActive)

	case FilterKeyArchived, "shift+6":
		return h, h.changeStatusFilter(FilterModeArchived)
	}

	return h, nil
}

func (h *Home) attachSelectedLegacy() tea.Cmd {
	// Keys typed while the checks below run belong to the pane (see holdInputForExec).
	defer holdInputForExec()()
	if h.cursor < 0 || h.cursor >= len(h.flatItems) {
		return nil
	}
	item := h.flatItems[h.cursor]
	switch item.Type {
	case session.ItemTypeSession:
		if item.Session == nil {
			return nil
		}
		if !h.sessionExistsForUI(item.Session) {
			if !h.hasActiveAnimation(item.Session.ID) {
				h.resumingSessions[item.Session.ID] = time.Now()
				return h.restartSession(item.Session)
			}
			return nil
		}
		if tmuxSess := item.Session.GetTmuxSession(); tmuxSess != nil && tmuxSess.IsPaneDead() {
			if !h.hasActiveAnimation(item.Session.ID) {
				h.resumingSessions[item.Session.ID] = time.Now()
				return h.restartSession(item.Session)
			}
			return nil
		}
		return h.attachSession(item.Session)
	case session.ItemTypeWindow:
		parentInst := h.getInstanceByID(item.WindowSessionID)
		if parentInst == nil || !h.sessionExistsForUI(parentInst) {
			return nil
		}
		tmuxSess := parentInst.GetTmuxSession()
		if tmuxSess == nil {
			return nil
		}
		tmuxSess.EnsureConfigured()
		parentInst.SyncSessionIDsToTmux()
		parentInst.MarkAccessed()
		if parentInst.GetStatusThreadSafe() == session.StatusWaiting {
			tmuxSess.Acknowledge()
			if db := statedb.GetGlobal(); db != nil {
				_ = db.SetAcknowledged(parentInst.ID, true)
			}
		}
		h.isAttaching.Store(true)
		return execHoldingInput(attachWindowCmd{
			session:     tmuxSess,
			windowIndex: item.WindowIndex,
			detachByte:  h.detachByte(),
			onExit:      func() { h.isAttaching.Store(false) },
		}, func(err error) tea.Msg {
			h.isAttaching.Store(false)
			parentInst.MarkAccessed()
			return statusUpdateMsg{}
		})
	case session.ItemTypeRemoteSession:
		if item.RemoteSession != nil {
			return h.attachRemoteSession(item.RemoteName, item.RemoteSession.ID)
		}
	}
	return nil
}

// tryQuit checks if MCP pool is running and shows confirmation dialog, or quits directly
func (h *Home) tryQuit() (tea.Model, tea.Cmd) {
	// Check if pool is enabled and has running MCPs
	userConfig, _ := session.LoadUserConfig()
	if userConfig != nil && userConfig.MCPPool.Enabled {
		runningCount := session.GetGlobalPoolRunningCount()
		if runningCount > 0 {
			// Show quit confirmation dialog
			h.confirmDialog.ShowQuitWithPool(runningCount)
			return h, nil
		}
	}
	// No pool running, quit directly (shutdown = true by default for clean exit)
	h.isQuitting = true
	return h, h.performQuit(true)
}

// performQuit triggers the quitting splash screen and schedules final shutdown
// shutdownPool: true = shutdown MCP pool, false = leave running in background
func (h *Home) performQuit(shutdownPool bool) tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(_ time.Time) tea.Msg {
		return quitMsg(shutdownPool)
	})
}

// performFinalShutdown performs the actual cleanup logic before exiting
// This is called via quitMsg after the splash screen has had time to render
func (h *Home) performFinalShutdown(shutdownPool bool) tea.Cmd {
	// Deletes still on the undo stack can no longer be undone once the TUI
	// exits: take them here, on the Update goroutine, and sweep them below.
	finalDeletes := undoEntryIDs(h.takeUndoStack())
	return func() tea.Msg {
		if len(finalDeletes) > 0 {
			runBounded(5*time.Second, func() { finalizeRemovedSessions(h.storage, "delete", finalDeletes) })
		}
		h.removeHeartbeat()
		// Stop system stats collector
		if h.sshCollector != nil {
			h.sshCollector.Stop()
		}
		if h.sysStatsCollector != nil {
			h.sysStatsCollector.Stop()
		}
		// Stop interval hooks
		if h.intervalHookRunner != nil {
			h.intervalHookRunner.Stop()
		}
		// Signal background worker to stop
		h.cancel()
		// Wait for background worker to finish (prevents race on shutdown)
		if h.statusWorkerDone != nil {
			select {
			case <-h.statusWorkerDone:
			case <-time.After(5 * time.Second):
				uiLog.Warn("status_worker_stop_timeout")
			}
		}
		// Wait for log workers to drain before closing the watcher they depend on
		logDone := make(chan struct{})
		go func() {
			h.logWorkerWg.Wait()
			close(logDone)
		}()
		select {
		case <-logDone:
		case <-time.After(5 * time.Second):
			uiLog.Warn("log_workers_stop_timeout")
		}

		// Close PipeManager (shuts down all control mode pipes)
		if pm := tmux.GetPipeManager(); pm != nil {
			pm.Close()
			tmux.SetPipeManager(nil)
		}
		// Close hook watcher (Claude Code lifecycle hooks)
		if h.hookWatcher != nil {
			h.hookWatcher.Stop()
		}
		// Close OpenCode SSE watcher (issue #1614)
		if h.sseWatcher != nil {
			h.sseWatcher.Stop()
		}
		// Close storage watcher
		if h.storageWatcher != nil {
			h.storageWatcher.Close()
		}
		// Close theme watcher
		h.stopThemeWatcher()
		// Close the recall index
		if h.recallSource != nil {
			h.recallSource.Close()
		}
		// Leave the watcher engine election (#2530). If this TUI owns the
		// engine, stopping it closes its channels, which ends the delivery
		// relay and the panel feed; the relay first dispatches whatever the
		// engine had buffered. Then the conductor delivery in flight finishes
		// and what is left is reported, instead of exiting mid-delivery, and
		// only then is the owner lock released for another process to take
		// over.
		h.StopWatcherEngineAndDeliveries(5 * time.Second)
		// Shutdown or disconnect from MCP pool based on user choice
		if err := session.ShutdownGlobalPool(shutdownPool); err != nil {
			mcpUILog.Warn("pool_shutdown_error", slog.String("error", err.Error()))
		}
		// Release primary claim and unregister from the heartbeat table
		if db := statedb.GetGlobal(); db != nil {
			_ = db.ResignPrimary()
			_ = db.ReleaseAllClaims()
			_ = db.UnregisterInstance()
		}
		// Clean up notification bar (clear tmux status bars and unbind keys)
		h.cleanupNotifications()
		// Save UI state (cursor, preview mode, filter) before saving instances
		h.saveUIState()
		// The remote snapshot is written on a 30 s debounce; quit writes
		// whatever is pending so the next start renders the fleet as last seen.
		h.flushRemoteSessionsCache(true)
		// End every remote agent now rather than leaving it to sshd's
		// keepalive: no ssh child outlives the TUI.
		session.CloseRemoteChannels()
		// Save both instances AND groups on quit (critical fix: was losing groups!)
		h.saveInstances()

		return tea.Quit()
	}
}

// sendToConductor types one queued health alert into its conductor's pane.
// The pane is looked up when the alert is sent, so a conductor restarted
// meanwhile still gets it. It takes the conductor's send lock first, like
// every `session send`, so it never types next to a routed event the send
// queue is delivering (#2537); nothing is typed if the lock stays busy.
func (h *Home) sendToConductor(d conductorDelivery) {
	id, ts := h.conductorPane(d.Conductor)
	if ts == nil {
		uiLog.Warn("watcher_delivery_conductor_not_loaded", slog.String("conductor", d.Conductor))
		return
	}
	budgetSessions := h.budgetSessions()
	if reason, refused := h.agentDeliveryOverBudget(sessionByID(budgetSessions, id), budgetSessions); refused {
		uiLog.Warn("dispatch_health_alert_budget_exceeded",
			slog.String("tmux_session", ts.Name), slog.String("reason", reason))
		return
	}
	lock, err := session.AcquireSendLock(id, conductorSendLockWait)
	if err != nil {
		uiLog.Warn("dispatch_health_alert_send_busy",
			slog.String("tmux_session", ts.Name),
			slog.String("error", err.Error()))
		return
	}
	defer lock.Release()
	if err := deliverToConductorPane(ts, d.Text); err != nil {
		uiLog.Warn("dispatch_health_alert_send_failed",
			slog.String("tmux_session", ts.Name),
			slog.String("error", err.Error()))
	}
}

// scheduleClearAndHeartbeat sends /clear and then a heartbeat to a conductor
// whose context is about to auto-compact, unless an enforced cost budget
// refuses the delivery. It reports whether the delivery was scheduled; a
// refused one is not marked sent, so it is tried again once the budget allows.
func (h *Home) scheduleClearAndHeartbeat(inst *session.Instance, instances []*session.Instance, tmuxSess *tmux.Session) bool {
	if reason, refused := h.agentDeliveryOverBudget(inst, instances); refused {
		uiLog.Warn("conductor_clear_heartbeat_budget_exceeded",
			slog.String("instance", inst.ID), slog.String("reason", reason))
		return false
	}
	h.clearOnCompactSent[inst.ID] = time.Now()
	conductorName := conductorDisplayName(inst)
	safego.Go(uiLog, "conductor_clear_and_heartbeat", func() {
		time.Sleep(500 * time.Millisecond)
		msg := fmt.Sprintf("Heartbeat: Check sessions in your group (%s). List any that are waiting, auto-respond where safe, and report what needs my attention.", conductorName)
		if err := clearConductorAndHeartbeat(func(body string) error {
			return conductorHeartbeatDeliver(tmuxSess, body)
		}, msg, 3*time.Second); err != nil {
			uiLog.Warn("conductor_clear_heartbeat_refused", slog.String("error", err.Error()))
		}
	})
	return true
}

// handleForkDialogKey handles keyboard input for the fork dialog
func (h *Home) handleForkDialogKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if h.forkDialog.IsBranchPickerOpen() {
		var cmd tea.Cmd
		h.forkDialog, cmd = h.forkDialog.Update(msg)
		return h, cmd
	}

	switch msg.String() {
	case "enter":
		// Validate before proceeding
		if validationErr := h.forkDialog.Validate(); validationErr != "" {
			h.forkDialog.SetError(validationErr)
			return h, nil
		}

		// Get fork parameters from dialog including worktree settings
		title, groupPath, branchName, worktreeEnabled := h.forkDialog.GetValuesWithWorktree()
		opts := h.forkDialog.GetOptions()
		h.clearError() // Clear any previous error

		// Find the currently selected session
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeSession && item.Session != nil {
				source := item.Session

				// Resolve worktree target if enabled; actual creation runs in async command.
				// Bare-repo project roots must pass — same contract as the
				// new-session path (#742). #1185: a worktree enabled by config
				// default (not an explicit toggle) on a non-repo dir falls back
				// to a normal fork instead of erroring.
				parentID := h.forkDialog.GetParentSessionID()
				parentPath := h.forkDialog.GetParentProjectPath()
				result := h.buildForkCmd(
					source, title, groupPath, branchName,
					forkToggles{
						Worktree:         worktreeEnabled,
						WithState:        h.forkDialog.IsWithStateEnabled(),
						WithIgnored:      h.forkDialog.IsWithStateAndGitignoredEnabled(),
						Sandbox:          h.forkDialog.IsSandboxEnabled(),
						ExplicitWorktree: h.forkDialog.IsWorktreeExplicit(),
						// Dialog title is explicit user intent: lock against #572 name sync.
						LockTitle: true,
					},
					opts,
					parentID, parentPath,
				)
				if result.errMsg != "" {
					h.forkDialog.SetError(result.errMsg)
					return h, nil
				}
				if result.cmd == nil {
					return h, nil
				}
				h.forkDialog.Hide()
				return h, result.cmd
			}
		}
		h.forkDialog.Hide()
		return h, nil

	case "esc":
		h.forkDialog.Hide()
		h.clearError() // Clear any error
		return h, nil
	}

	var cmd tea.Cmd
	h.forkDialog, cmd = h.forkDialog.Update(msg)
	return h, cmd
}

// uniqueForkBranch returns base, or base-2 / base-3 / … — the first name with no
// existing git branch and no linked worktree — so repeated quick forks of the same
// source session don't collide on the deterministic branch name (comprehensive
// fork forces a fresh worktree+branch). Non-git sources have no branch to collide
// with, so base is returned unchanged.
func uniqueForkBranch(projectPath, base string) string {
	backend, err := vcsbackend.Detect(projectPath)
	if err != nil {
		return base
	}
	candidate := base
	for n := 2; forkBranchTaken(backend, candidate); n++ {
		candidate = fmt.Sprintf("%s-%d", base, n)
		if n > 1000 {
			return candidate // pathological guard; never expected in practice
		}
	}
	return candidate
}

// forkBranchTaken reports whether a branch/bookmark name is already used by the
// detected VCS backend or already has a linked worktree/workspace.
func forkBranchTaken(backend vcs.Backend, branch string) bool {
	if backend.BranchExists(branch) {
		return true
	}
	wt, err := backend.GetWorktreeForBranch(branch)
	return err == nil && wt != ""
}

// buildForkCmd resolves the worktree target (when requested + git-capable),
// populates the worktree fields on opts, builds WorktreeStateOptions, and
// returns the async fork command plus any non-fatal success notice. Shared by
// the dialog (Shift+F) and quick fork (f). Fatal validation text is returned to
// the caller so Shift+F can keep using ForkDialog.SetError while quick fork uses
// Home.setError. explicitWorktree is forwarded to resolveWorktreeTarget's #1185
// fallback gate.
func (h *Home) buildForkCmd(
	source *session.Instance,
	title, groupPath, branchName string,
	toggles forkToggles,
	opts *session.ClaudeOptions,
	parentSessionID, parentProjectPath string,
) forkBuildResult {
	worktreeApplied := false
	notice := ""
	if toggles.Worktree && branchName != "" {
		worktreePath, repoRoot, fallback, errMsg := resolveWorktreeTarget(source.ProjectPath, branchName, toggles.ExplicitWorktree)
		if errMsg != "" {
			return forkBuildResult{errMsg: errMsg}
		}
		if !fallback {
			if opts == nil {
				opts = &session.ClaudeOptions{}
			}
			opts.WorkDir = worktreePath
			opts.WorktreePath = worktreePath
			opts.WorktreeRepoRoot = repoRoot
			opts.WorktreeBranch = branchName
			worktreeApplied = true
		} else {
			notice = "forked without worktree: not a git or jujutsu repo"
		}
	}
	forkState := git.WorktreeStateOptions{WithState: toggles.WithState, WithIgnored: toggles.WithIgnored}
	if !worktreeApplied {
		// State materialization requires a freshly created worktree.
		forkState = git.WorktreeStateOptions{}
	}
	return forkBuildResult{
		cmd:             h.forkSessionCmdWithOptions(source, title, groupPath, toggles, opts, forkState, parentSessionID, parentProjectPath, notice),
		worktreeApplied: worktreeApplied,
		notice:          notice,
	}
}

// forkSessionCmdWithOptions creates a forked session with the given title, group, shared fork options, and optional sandbox.
// Shows immediate UI feedback by tracking the source session in forkingSessions.
func (h *Home) forkSessionCmdWithOptions(
	source *session.Instance,
	title, groupPath string,
	toggles forkToggles,
	opts *session.ClaudeOptions,
	forkState git.WorktreeStateOptions,
	parentSessionID, parentProjectPath string,
	notice string,
) tea.Cmd {
	if source == nil {
		return nil
	}

	if forkState.WithIgnored {
		forkState.WithState = true
	}

	// Track source session as "forking" for immediate UI feedback
	h.forkingSessions[source.ID] = time.Now()

	sourceID := source.ID // Capture for closure

	return func() tea.Msg {
		var setupWarning string // non-fatal worktree setup-script failure, if any

		// Check tmux availability before forking
		if err := tmux.IsTmuxAvailable(); err != nil {
			return sessionForkedMsg{err: fmt.Errorf("cannot fork session: %w", err), sourceID: sourceID}
		}

		// toggles is a value copy, so degrading Sandbox here is local to this fork.
		forkNotice := notice
		if toggles.Sandbox {
			if err := docker.CheckAvailability(context.Background()); err != nil {
				toggles.Sandbox = false
				forkNotice = joinForkNotices(forkNotice, "forked without Docker: not available")
			}
		}

		// withStateWorktreeCreated tracks whether forkWithStateWorktree actually
		// created a new worktree+branch, so later failures can roll it back
		// without dereferencing opts when no worktree was created (e.g. the
		// #1185 config-default fallback leaves the worktree fields empty).
		withStateWorktreeCreated := false
		if opts != nil && opts.WorktreePath != "" && opts.WorktreeRepoRoot != "" && opts.WorktreeBranch != "" {
			// Worktree creation can be slow on large repos; keep it in async cmd path
			// so the TUI remains responsive.
			//
			// Detect the VCS so jj repos get `jj workspace add` instead of `git worktree add`.
			backend, err := vcsbackend.Detect(opts.WorktreeRepoRoot)
			if err != nil {
				return sessionForkedMsg{err: fmt.Errorf("failed to detect VCS: %w", err), sourceID: sourceID}
			}

			// With-state forks are routed by backend and never reuse an existing
			// worktree/workspace; otherwise check for an existing worktree for this
			// branch before creating a new one.
			if forkState.WithState {
				switch backend.Type() {
				case vcs.TypeGit:
					setupErr, err := forkWithStateWorktree(
						source.ProjectPath,
						opts.WorktreeRepoRoot,
						opts.WorktreePath,
						opts.WorktreeBranch,
						forkState,
						defaultForkWithStateWorktreeDeps(source.ProjectPath),
					)
					if err != nil {
						return sessionForkedMsg{err: err, sourceID: sourceID}
					}
					if setupErr != nil {
						setupWarning = formatSetupWarning(setupErr)
					}
				case vcs.TypeJujutsu:
					if err := forkWithStateWorkspaceJJ(
						source.ProjectPath,
						opts.WorktreeRepoRoot,
						opts.WorktreePath,
						opts.WorktreeBranch,
						forkState,
					); err != nil {
						return sessionForkedMsg{err: err, sourceID: sourceID}
					}
					// The jj path has no setup result to carry it, so report a
					// skipped untrusted dir-local config here.
					if _, notice, err := session.GetWorktreeSettingsForDirWithNotice(source.ProjectPath); err == nil && notice != "" {
						setupWarning = formatSetupWarning(&dirLocalNoticeError{notice: notice})
					}
					// gitignored files are copied via git's exclude machinery; a jj
					// repo with no git worktree root (pure jj, or a linked workspace)
					// can't enumerate them, so tell the user instead of dropping them
					// silently.
					if forkState.WithIgnored && !jujutsu.SupportsGitignoredCopy(source.ProjectPath) {
						forkNotice = joinForkNotices(forkNotice, "forked without gitignored files: this jj repo has no git metadata to copy them")
					}
				default:
					return sessionForkedMsg{err: fmt.Errorf("--with-state is not supported for this repository's VCS backend"), sourceID: sourceID}
				}
				withStateWorktreeCreated = true
			} else if existingPath, err := backend.GetWorktreeForBranch(opts.WorktreeBranch); err == nil && existingPath != "" {
				uiLog.Info("worktree_reuse", slog.String("branch", opts.WorktreeBranch), slog.String("path", existingPath))
				opts.WorktreePath = existingPath
			} else {
				if err := os.MkdirAll(filepath.Dir(opts.WorktreePath), 0o755); err != nil {
					return sessionForkedMsg{err: fmt.Errorf("failed to create directory: %w", err), sourceID: sourceID}
				}
				setupErr, err := createWorktreeWithSetupAndLog(backend, opts.WorktreePath, opts.WorktreeBranch, source.ProjectPath)
				if err != nil {
					return sessionForkedMsg{err: fmt.Errorf("worktree creation failed: %w", err), sourceID: sourceID}
				}
				if setupErr != nil {
					setupWarning = formatSetupWarning(setupErr)
				}
			}
		}

		inst, err := completeFork(source, title, groupPath, toggles, opts, parentSessionID, parentProjectPath, withStateWorktreeCreated, defaultForkInstanceDeps())
		if err != nil {
			return sessionForkedMsg{err: err, sourceID: sourceID}
		}

		return sessionForkedMsg{instance: inst, sourceID: sourceID, notice: forkNotice, setupWarning: setupWarning}
	}
}

// attachSession attaches to a session using custom PTY with Ctrl+Q detection
func (h *Home) attachSession(inst *session.Instance) tea.Cmd {
	tmuxSess := inst.GetTmuxSession()
	if tmuxSess == nil {
		return nil
	}
	var attachStart, detachedAt time.Time // tui.attach / tui.detach stamps, only while timings are on
	if optiming.Enabled() {
		attachStart = time.Now()
	}
	// Keys typed while the setup below runs belong to the pane (see holdInputForExec).
	defer holdInputForExec()()

	// PERFORMANCE: Ensure tmux session is configured on first attach
	// This runs deferred ConfigureStatusBar, EnableMouseMode
	// which were skipped during lazy loading for TUI startup performance
	tmuxSess.EnsureConfigured()

	// Sync session IDs to tmux environment for resume functionality
	// (Deferred from load time for performance)
	inst.SyncSessionIDsToTmux()

	// Mark session as accessed (for recency-sorted path suggestions).
	// Do not synchronously save here; saving on attach blocks transition and causes
	// visible blank-screen delay before tmux attach starts.
	// #2058: also records the visit for the alternate-session toggle / MRU walk.
	h.markSessionVisited(inst)
	h.telemetryAttachStart(inst)

	// #1114 follow-up: Claude's /rename fires no agent-deck hook, so an idle
	// session's title and iTerm2 badge can be stale at attach time (the
	// hook-driven sync only runs on the next turn boundary). Reconcile from the
	// agent's current Claude session name here — before tmuxSess.Attach() emits
	// the badge from DisplayName — so detach/reattach reliably refreshes it.
	if session.IsClaudeCompatible(inst.Tool) {
		sessionID := session.ReadHookSessionAnchor(inst.ID)
		if sessionID == "" {
			sessionID = inst.ClaudeSessionID
		}
		if newName, changed := inst.ReconcileTitleFromClaude(sessionID); changed {
			// A sync-sourced title stays unlocked (TitleLocked is false here,
			// since ReconcileTitleFromClaude only runs on unlocked sessions) so
			// it keeps tracking Claude's session name across reloads.
			h.pendingTitleChanges[inst.ID] = pendingTitle{title: newName, locked: inst.TitleLocked}
			h.invalidatePreviewCache(inst.ID)
			h.rebuildFlatItems()
			h.saveInstances()
			uiLog.Info("title_reconciled_on_attach",
				slog.String("session_id", inst.ID),
				slog.String("title", newName))
		}
	}

	// Acknowledge on ATTACH (not detach) - but ONLY if session is waiting (yellow)
	// This ensures:
	// - GREEN (running) sessions stay green when attached/detached
	// - YELLOW (waiting) sessions turn gray when user looks at them
	// - Detach just lets polling take over naturally
	if inst.GetStatusThreadSafe() == session.StatusWaiting {
		tmuxSess.Acknowledge()
		// Persist ack to SQLite so other instances see it
		if db := statedb.GetGlobal(); db != nil {
			_ = db.SetAcknowledged(inst.ID, true)
		}
		statusLog.Debug("acknowledged_on_attach", slog.String("title", inst.Title))
	}

	// Use tea.Exec with a custom command that runs our Attach method
	// On return, immediately update all session statuses (don't reload from storage
	// which would lose the tmux session state)
	h.isAttaching.Store(true) // Prevent View() output only during actual attach transition
	res := &attachResult{}
	opts := h.attachOptions(tmuxSess)
	if !attachStart.IsZero() {
		opts.TimingStart, opts.DetachedAt = attachStart, &detachedAt
	}
	// #2704: record the client this attach runs in, so `session focus
	// --remote --attach` can detach exactly it to bring the TUI back. Both
	// callbacks run inside attachCmd.Run, on one goroutine.
	clientPID := 0
	opts.OnClientStarted = func(pid int) {
		clientPID = pid
		if db := statedb.GetGlobal(); db != nil {
			_ = session.WriteTUIAttach(db, session.TUIAttach{Socket: tmuxSess.SocketName, ClientPID: pid})
		}
	}
	opts = withSSHPasteHint(opts, inst.Tool,
		func() string { pane, _ := tmuxSess.CapturePane(); return pane },
		func(msg string) { tmux.ShowAttachNotice(msg) })
	return execHoldingInput(attachCmd{
		session: tmuxSess,
		opts:    opts,
		result:  res,
		// #1753: clear the flag inside Run(), i.e. BEFORE Bubble Tea restores the
		// terminal and resumes the loop. The ExecCallback below runs on its own
		// goroutine, so clearing it only there raced the first View() after resume:
		// View() returns "" while isAttaching is set, so losing that race meant the
		// list stayed blank until the NEXT message arrived. Clearing it here makes
		// the first post-detach repaint deterministic. Nothing can call View() in
		// between — the event loop is parked inside Program.exec for the whole
		// attach.
		onExit: func() {
			if db := statedb.GetGlobal(); db != nil && clientPID != 0 {
				_ = session.ClearTUIAttach(db, clientPID)
			}
			h.isAttaching.Store(false)
		},
	}, func(err error) tea.Msg {
		// Belt for the path where Bubble Tea fails to release the terminal and
		// invokes this callback without ever running attachCmd.Run().
		h.isAttaching.Store(false) // Atomic store for thread safety

		// NOTE: No manual screen clear here. Bubble Tea's RestoreTerminal()
		// re-enters alt screen which handles clearing. Direct fmt.Print
		// of escape codes races with the Bubble Tea renderer.

		// Update last accessed time to detach time (more accurate than attach time)
		inst.MarkAccessed()

		// NOTE: We don't acknowledge on detach anymore.
		// Acknowledgment happens on ATTACH (only if session was waiting/yellow).
		// This lets running sessions stay green through attach/detach cycles.

		// Capture current pane CWD after attach returns for optional path follow.
		//
		// #1753: only when the feature is actually on. GetWorkDir costs two tmux
		// subprocess spawns (Exists + display-message), and on macOS every fork
		// briefly quiesces the whole process — measured at 5-13ms of dead time here,
		// the single largest item between the detach key and the first repaint, paid
		// on every detach for a feature that defaults to off. followAttachReturnCwd
		// consults the same setting, so an empty value changes nothing when it is off.
		instanceSettings := session.GetInstanceSettings()
		followCwd := instanceSettings.GetFollowCwdOnAttach()
		workDirIfFollowing := func(ts *tmux.Session) string {
			if !followCwd || ts == nil {
				return ""
			}
			return strings.TrimSpace(ts.GetWorkDir())
		}
		currentWorkDir := workDirIfFollowing(tmuxSess)

		// The user pressed the session-switch key while attached: surface the
		// in-attach switcher instead of just returning to the list.
		if res.intent != tmux.SwitchNone {
			// While attached the user may have switched the tmux client to
			// another session via the notification bar (Ctrl+b 1-6), recorded
			// in lastNotifSwitchID. Open the switcher on the session actually in
			// view — not the one we first attached to — so the pre-highlight,
			// Esc-reattach, and follow-CWD all target the right session.
			fromID, fromWorkDir := inst.ID, currentWorkDir
			// Consume the value: clear it so a switcher exit that bypasses the
			// statusUpdateMsg path (e.g. Ctrl+Q out of the switcher) can't leave
			// a stale ID to pre-highlight the wrong session on a later attach.
			// The commit/Esc-reattach paths re-set it via attachToSwitchTarget.
			h.lastNotifSwitchMu.Lock()
			switchedID := h.lastNotifSwitchID
			h.lastNotifSwitchID = ""
			h.lastNotifSwitchMu.Unlock()
			if switchedID != "" && switchedID != inst.ID {
				h.instancesMu.RLock()
				switched := h.instanceByID[switchedID]
				h.instancesMu.RUnlock()
				if switched != nil {
					fromID = switched.ID
					if wd := workDirIfFollowing(switched.GetTmuxSession()); wd != "" {
						fromWorkDir = wd
					}
				}
			}
			if res.intent == tmux.ScrollbackRequested {
				return openScrollbackMsg{
					fromSessionID:   fromID,
					attachedWorkDir: fromWorkDir,
				}
			}
			return openSwitcherMsg{
				fromSessionID:   fromID,
				attachedWorkDir: fromWorkDir,
			}
		}

		return statusUpdateMsg{attachedSessionID: inst.ID, attachedWorkDir: currentWorkDir, detachedAt: detachedAt}
	})
}

func (h *Home) followAttachReturnCwd(msg statusUpdateMsg) {
	if msg.attachedSessionID == "" {
		return
	}

	workDir := strings.TrimSpace(msg.attachedWorkDir)
	if workDir == "" {
		return
	}

	instanceSettings := session.GetInstanceSettings()
	if !instanceSettings.GetFollowCwdOnAttach() {
		return
	}

	workDir = filepath.Clean(workDir)
	if !filepath.IsAbs(workDir) {
		return
	}

	info, err := os.Stat(workDir)
	if err != nil || !info.IsDir() {
		return
	}

	h.instancesMu.RLock()
	inst := h.instanceByID[msg.attachedSessionID]
	h.instancesMu.RUnlock()
	if inst == nil {
		return
	}

	oldPath := strings.TrimSpace(inst.ProjectPath)
	if oldPath == workDir {
		return
	}

	inst.ProjectPath = workDir
	if tmuxSess := inst.GetTmuxSession(); tmuxSess != nil {
		tmuxSess.WorkDir = workDir
	}
	h.invalidatePreviewCache(inst.ID)
	h.saveInstances()

	uiLog.Info(
		"attach_follow_cwd_updated",
		slog.String("session_id", inst.ID),
		slog.String("old_path", oldPath),
		slog.String("new_path", workDir),
	)
}

// attachResult is a shared out-parameter for attachCmd.Run. attachCmd is passed
// to tea.Exec by value, so Run cannot return the SwitchIntent through its value
// receiver; it writes through this pointer instead, which the tea.Exec callback
// then reads to decide whether to open the session switcher.
type attachResult struct {
	intent tmux.SwitchIntent
}

// attachCmd implements tea.ExecCommand for custom PTY attach
type attachCmd struct {
	session *tmux.Session
	opts    tmux.AttachOptions
	result  *attachResult
	// onExit runs as Run returns, i.e. while Bubble Tea's event loop is still
	// parked inside Program.exec and before it restores the terminal. It exists so
	// the attach flag can be cleared without racing the first repaint (#1753); see
	// the call site in attachSession.
	onExit func()
}

func (a attachCmd) Run() error {
	// NOTE: Screen clearing is ONLY done in the tea.Exec callback (after Attach returns)
	// Removing clear screen here prevents double-clearing which corrupts terminal state
	if a.onExit != nil {
		defer a.onExit()
	}

	ctx := context.Background()
	intent, err := a.session.AttachWithOptions(ctx, a.opts)
	if a.result != nil {
		a.result.intent = intent
	}
	return err
}

func (a attachCmd) SetStdin(r io.Reader)  {}
func (a attachCmd) SetStdout(w io.Writer) {}
func (a attachCmd) SetStderr(w io.Writer) {}

// createRemoteSession creates a new session on a remote and auto-attaches to it.
// Used by quick-create (N): auto-generated name, remote defaults (shell).
func (h *Home) createRemoteSession(remoteName string) tea.Cmd {
	return h.createRemoteSessionWithOptions(remoteName, session.RemoteAddOptions{})
}

// remoteCreateAndAttachCmd creates a session on the remote, then attaches to it.
type remoteCreateAndAttachCmd struct {
	runner    *session.SSHRunner
	opts      session.RemoteAddOptions
	createCtx context.Context
	// created receives the remote's session id once `add` succeeded, so
	// the row can be drawn the moment the attach returns, before any
	// fetch confirms it.
	created *string
	// onExit: same contract as attachCmd.onExit (#1753) — clear the attach flag
	// while Bubble Tea's loop is still parked, so the first View() after resume
	// never races the ExecCallback goroutine and renders blank.
	onExit func()
}

type remoteAttachFailedError struct {
	err error
}

func (e remoteAttachFailedError) Error() string {
	return e.err.Error()
}

func (e remoteAttachFailedError) Unwrap() error {
	return e.err
}

// Run creates the session on the remote with the dialog's choices, then
// attaches to it. A create failure is returned as-is; an attach failure after
// a successful create is wrapped in remoteAttachFailedError so the caller can
// tell the two apart.
func (r remoteCreateAndAttachCmd) Run() error {
	if r.onExit != nil {
		defer r.onExit()
	}
	baseCtx := r.createCtx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	ctx, cancel := context.WithTimeout(baseCtx, 20*time.Second)
	defer cancel()
	sessionID, err := r.runner.CreateSessionWithOptions(ctx, r.opts)
	if err != nil {
		return err
	}
	if r.created != nil {
		*r.created = sessionID
	}
	if err := r.runner.Attach(sessionID); err != nil {
		return remoteAttachFailedError{err: err}
	}
	return nil
}

func (r remoteCreateAndAttachCmd) SetStdin(reader io.Reader)  {}
func (r remoteCreateAndAttachCmd) SetStdout(writer io.Writer) {}
func (r remoteCreateAndAttachCmd) SetStderr(writer io.Writer) {}

// createRemoteSessionWithOptions creates a new session on a remote with the
// new-session dialog's choices (#1353), then auto-attaches to it. Zero values
// fall back to remote defaults (shell, auto-generated name, remote CWD).
func (h *Home) createRemoteSessionWithOptions(remoteName string, opts session.RemoteAddOptions) tea.Cmd {
	defer holdInputForExec()() // keys typed meanwhile belong to the remote pane
	config, err := session.LoadUserConfig()
	if err != nil || config == nil || config.Remotes == nil {
		return func() tea.Msg {
			return sessionCreatedMsg{err: fmt.Errorf("failed to load remote config")}
		}
	}
	rc, ok := config.Remotes[remoteName]
	if !ok {
		return func() tea.Msg {
			return sessionCreatedMsg{err: fmt.Errorf("remote '%s' not found", remoteName)}
		}
	}
	runner := session.NewSSHRunner(remoteName, rc)
	h.isAttaching.Store(true)
	var createdID string
	return execHoldingInput(remoteCreateAndAttachCmd{
		runner: runner, opts: opts, createCtx: h.ctx, created: &createdID,
		// Clear the flag inside Run(), before Bubble Tea restores the terminal
		// and resumes the loop (#1753); the callback below is only the belt for
		// the path where Run() never executes.
		onExit: func() { h.isAttaching.Store(false) },
	}, func(err error) tea.Msg {
		h.isAttaching.Store(false)
		return remoteCreateOutcomeMsg(remoteName, opts, createdID, err)
	})
}

// remoteCreateOutcomeMsg turns the create+attach result into the message the
// update loop applies: the row to draw (if the remote confirmed one), the
// footer error or notice, and the preview to seed for it.
//
// A spawn failure the remote recorded (session.RemoteSessionSpawnFailedError)
// is a confirmed row too: the remote kept the session as an error with its
// explainer, so the row is drawn with the error light and that explainer in
// the preview, the same picture `remote <r> add` + `session start` leaves.
// Pre-fix the row was never drawn and the create path had deleted the
// session, so it flashed in the header counts and vanished (g14 parity walk).
func remoteCreateOutcomeMsg(remoteName string, opts session.RemoteAddOptions, createdID string, err error) tea.Msg {
	tool := opts.Tool
	if tool == "" {
		tool = "shell"
	}
	if err != nil {
		var attachErr remoteAttachFailedError
		if errors.As(err, &attachErr) {
			return remoteSessionCreatedMsg{err: fmt.Errorf("failed to attach to remote session after creating it: %w", attachErr)}
		}
		var spawnFailed *session.RemoteSessionSpawnFailedError
		if errors.As(err, &spawnFailed) && spawnFailed.ID != "" {
			title := spawnFailed.Title
			if title == "" {
				title = opts.Title
			}
			return remoteSessionCreatedMsg{
				err: fmt.Errorf("on %s: %w", remoteName, err),
				created: &session.RemoteSessionInfo{
					ID: spawnFailed.ID, Title: title, Group: opts.Group, Tool: tool,
					Path: opts.Path, Status: string(session.StatusError), RemoteName: remoteName,
				},
				preview: spawnFailed.Preview(),
			}
		}
		if !opts.CreateDir && strings.TrimSpace(opts.Path) != "" && session.IsRemotePathMissing(err) {
			return remoteCreateDirNeededMsg{remoteName: remoteName, opts: opts, err: err}
		}
		var queued *session.RemoteSessionQueuedError
		if errors.As(err, &queued) {
			// The session exists on the remote; it starts when the group
			// has a free slot. Not a failure, and the row must show now.
			return remoteSessionCreatedMsg{notice: fmt.Sprintf("created '%s' on %s, queued until group '%s' has a free slot", queued.Title, remoteName, opts.Group)}
		}
		// Routed as the remote message so the terminal cleanup after
		// tea.Exec (mouse, keyboard mode, resize) runs on failure too.
		return remoteSessionCreatedMsg{err: fmt.Errorf("on %s: %w", remoteName, err)}
	}
	if createdID == "" {
		return remoteSessionCreatedMsg{}
	}
	return remoteSessionCreatedMsg{created: &session.RemoteSessionInfo{
		ID: createdID, Title: opts.Title, Group: opts.Group, Tool: tool,
		Path: opts.Path, Status: string(session.StatusRunning), RemoteName: remoteName,
	}}
}

// attachWindowCmd implements tea.ExecCommand for attaching to a specific tmux window
type attachWindowCmd struct {
	session     *tmux.Session
	windowIndex int
	detachByte  byte
	// onExit: same contract as attachCmd.onExit (#1753).
	onExit func()
}

func (a attachWindowCmd) Run() error {
	if a.onExit != nil {
		defer a.onExit()
	}
	ctx := context.Background()
	return a.session.AttachWindow(ctx, a.windowIndex, a.detachByte)
}

func (a attachWindowCmd) SetStdin(r io.Reader)  {}
func (a attachWindowCmd) SetStdout(w io.Writer) {}
func (a attachWindowCmd) SetStderr(w io.Writer) {}

// attachRemoteSession attaches to a remote session via SSH, suspending the TUI.
func (h *Home) attachRemoteSession(remoteName, sessionID string) tea.Cmd {
	defer holdInputForExec()() // keys typed meanwhile belong to the remote pane
	runner, err := remoteRunnerFor(remoteName)
	if err != nil {
		// Say so instead of a silent no-op on Enter.
		return func() tea.Msg {
			return remoteSessionCreatedMsg{err: fmt.Errorf("cannot attach on %s: %w", remoteName, err)}
		}
	}
	h.isAttaching.Store(true)
	return execHoldingInput(remoteAttachCmd{
		runner:    runner,
		sessionID: sessionID,
		// Clear the flag inside Run() (#1753) — see attachCmd.onExit. The
		// callback clear below is only the belt for the never-ran path.
		onExit: func() { h.isAttaching.Store(false) },
	}, func(err error) tea.Msg {
		h.isAttaching.Store(false)
		if err != nil {
			// An unreachable host or a session the remote refuses to attach
			// used to flash the screen and say nothing.
			return remoteSessionCreatedMsg{err: fmt.Errorf("failed to attach to '%s' on %s: %w", sessionID, remoteName, err)}
		}
		return statusUpdateMsg{}
	})
}

// remoteAttachCmd implements tea.ExecCommand for remote SSH attach
type remoteAttachCmd struct {
	runner    *session.SSHRunner
	sessionID string
	// onExit: same contract as attachCmd.onExit (#1753).
	onExit func()
}

func (r remoteAttachCmd) Run() error {
	if r.onExit != nil {
		defer r.onExit()
	}
	return r.runner.Attach(r.sessionID)
}

func (r remoteAttachCmd) SetStdin(reader io.Reader)  {}
func (r remoteAttachCmd) SetStdout(writer io.Writer) {}
func (r remoteAttachCmd) SetStderr(writer io.Writer) {}

// sessionSwitcherOverlayRegion returns the active-session portion of the
// dashboard. In the ordinary dual embedded layout this is the preview pane to the
// right of the sidebar; in a stacked layout it is the preview below the list.
// A single-column terminal has no preview pane, so the main content area is the
// least surprising fallback.
func (h *Home) sessionSwitcherOverlayRegion() terminalCellRect {
	helpBarHeight := 2
	filterBarHeight := 1
	updateBannerHeight := 0
	if h.shouldRenderUpdateNudge() {
		updateBannerHeight = 1
	}
	maintenanceBannerHeight := 0
	if h.maintenanceMsg != "" {
		maintenanceBannerHeight = 1
	}
	debugBarHeight := 0
	if h.debugMode {
		debugBarHeight = 1
	}
	contentHeight := h.height - 1 - helpBarHeight - filterBarHeight -
		updateBannerHeight - maintenanceBannerHeight - debugBarHeight
	contentY := 1 + filterBarHeight + updateBannerHeight + maintenanceBannerHeight

	switch h.getLayoutMode() {
	case LayoutModeDual:
		leftWidth, rightWidth := h.splitPaneWidths()
		return terminalCellRect{
			X:      leftWidth + paneSeparatorWidth,
			Y:      contentY,
			Width:  rightWidth,
			Height: contentHeight,
		}
	case LayoutModeStacked:
		listHeight := h.stackedListHeight(contentHeight)
		return terminalCellRect{
			X:      0,
			Y:      contentY + listHeight + 1,
			Width:  h.width,
			Height: max(contentHeight-listHeight-1, 1),
		}
	default:
		return terminalCellRect{X: 0, Y: contentY, Width: h.width, Height: contentHeight}
	}
}

// renderSessionSwitcherOverlay composites the Ctrl+S card over the existing
// dashboard rather than replacing the frame. Its left edge is fixed to the
// active-session region while its vertical position is centered within that
// region, leaving the sidebar fully unobscured in the dual layout.
func (h *Home) renderSessionSwitcherOverlay(background string) string {
	region := h.sessionSwitcherOverlayRegion()
	card := h.sessionSwitcher.dialogView(region.Width)
	if card == "" {
		return background
	}
	// Pad short card lines to the border. Separate a preview rule only where
	// it actually touches the border; a fixed extra cell can erase "Output".
	cardWidth := lipgloss.Width(card)
	cardHeight := lipgloss.Height(card)
	y := region.Y + max((region.Height-cardHeight)/2, 0)
	cardLines := strings.Split(card, "\n")
	backgroundLines := strings.Split(background, "\n")
	for i, line := range cardLines {
		cardLines[i] = line + strings.Repeat(" ", max(0, cardWidth-cellWidth(line)))
		if row := y + i; cardWidth < region.Width && row >= 0 && row < len(backgroundLines) {
			next := ansi.Strip(ansi.Cut(backgroundLines[row], region.X+cardWidth, region.X+cardWidth+1))
			if next == "─" {
				cardLines[i] += " "
			}
		}
	}
	card = strings.Join(cardLines, "\n")
	// View runs the composite through the final clampViewToViewport.
	return overlayAtCells(background, card, y, region.X)
}

// overlayAtCells paints overlay over base at terminal-cell coordinates. The
// shared dialog dropdown helper predates wide-grapheme sidebar labels and
// counts visible runes, which can shift a card right when a session name
// contains emoji/CJK. Ctrl+S is explicitly anchored to the pane boundary, so
// use ANSI-aware cell cuts here to keep that edge exact on every row.
func overlayAtCells(base, overlay string, row, col int) string {
	baseLines := strings.Split(base, "\n")
	overLines := strings.Split(overlay, "\n")
	for i, overLine := range overLines {
		targetRow := row + i
		if targetRow < 0 || targetRow >= len(baseLines) {
			continue
		}
		baseLine := baseLines[targetRow]
		baseWidth := cellWidth(baseLine)
		prefix := ansi.Cut(baseLine, 0, max(col, 0))
		if prefixWidth := cellWidth(prefix); prefixWidth < col {
			prefix += strings.Repeat(" ", col-prefixWidth)
		}
		afterCol := col + cellWidth(overLine)
		suffix := ""
		if afterCol < baseWidth {
			suffix = ansi.Cut(baseLine, afterCol, baseWidth)
		}
		baseLines[targetRow] = prefix + overLine + suffix
	}
	return strings.Join(baseLines, "\n")
}

// renderSessionItem renders a single session row into b, including the tree
// connector, status badge, tool label, and the dim tmux pane-title suffix.
// The pane-title suffix appears on the selected row, or on every row when
// show_pane_titles is enabled, and is truncated to listWidth so it never
// overflows the SESSIONS panel.
func (h *Home) renderSessionItem(
	b *strings.Builder,
	item session.Item,
	selected bool,
	snapshot map[string]sessionRenderState,
	listWidth int,
) {
	inst := item.Session
	if h.embeddedLayout && listWidth >= embeddedCardMinWidth {
		instState, ok := snapshot[inst.ID]
		if !ok {
			instState = h.getSessionRenderState(inst)
		}
		h.renderEmbeddedSessionItem(b, item, selected, instState, listWidth)
		return
	}

	// Read status/tool from snapshot so render path stays lock-light during key-repeat.
	instState, ok := snapshot[inst.ID]
	if !ok {
		instState = h.getSessionRenderState(inst)
	}
	instStatus := instState.status
	instSubstate := instState.substate
	instTool := instState.tool

	// Tree style for connectors - Use ColorText for clear visibility of box-drawing characters
	treeStyle := TreeConnectorStyle

	// Calculate base indentation for parent levels
	// Level 1 means direct child of root group, Level 2 means child of nested group, etc.
	baseIndent := ""
	if item.Level > 1 {
		// For deeply nested items, add spacing for parent levels
		// Sub-sessions get extra indentation (they're at Level = groupLevel + 2)
		if item.IsSubSession {
			// Sub-session: indent for group level, then continuation line for parent
			// Add leading space so │ aligns with ├ in regular items (both at position 1)
			groupIndent := strings.Repeat(treeEmpty, item.Level-2)
			if item.ParentIsLastInGroup {
				baseIndent = groupIndent + "  " // 2 spaces - parent is last, no continuation needed
			} else {
				// Style the │ character - leading space aligns │ with ├ above
				baseIndent = groupIndent + " " + treeStyle.Render("│")
			}
		} else {
			baseIndent = strings.Repeat(treeEmpty, item.Level-1)
		}
	}

	// Tree connector: └─ for last item, ├─ for others
	treeConnector := treeBranch
	if item.IsSubSession {
		// Sub-session uses its own last-in-group logic
		if item.IsLastSubSession {
			treeConnector = subLast
		} else {
			treeConnector = subBranch
		}
	} else if item.IsLastInGroup {
		treeConnector = treeLast
	}

	// Status indicator with consistent sizing. rowStatusGlyph maps the coarse
	// status (plus the Honest-Status-v2 error substates) to a glyph, and forces
	// the stopped glyph for archived sessions whose snapshot still carries a
	// stale live status.
	statusIcon, statusStyle := rowStatusGlyph(instStatus, instSubstate, inst.IsArchived())

	status := statusStyle.Render(statusIcon)

	// Title styling - add bold/underline for accessibility (colorblind users)
	var titleStyle lipgloss.Style
	switch instStatus {
	case session.StatusRunning, session.StatusWaiting:
		// Bold for active states (distinguishable without color)
		titleStyle = SessionTitleActive
	case session.StatusError:
		// Underline for error (distinguishable without color)
		titleStyle = SessionTitleError
	default:
		titleStyle = SessionTitleDefault
	}

	// Issue #391: per-session color tint. When the user has set
	// Instance.Color (validated CLI-side in isValidSessionColor), override
	// the title foreground with that color. Bold/underline from the
	// status-based style above is preserved — only the hue changes, so
	// colorblind accessibility via weight still works. Empty Color is the
	// default and leaves titleStyle untouched (zero behavior change for
	// users who haven't opted in).
	if inst.Color != "" {
		titleStyle = titleStyle.Foreground(lipgloss.Color(inst.Color))
	}

	// Maestro (fleet supervisor): gold title by default. An explicit
	// Instance.Color stays the stronger signal (issue #391 opt-in wins);
	// the ⬢ glyph and [SUPERVISOR] badge below render unconditionally.
	isMaestro := inst.IsMaestro()
	if isMaestro && inst.Color == "" {
		titleStyle = titleStyle.Foreground(ColorYellow)
	}

	// Tool badge with brand-specific color
	// Claude=orange, Gemini=purple, Codex=cyan, Aider=red
	toolStyle := GetToolStyle(instTool)

	// Selection indicator
	selectionPrefix := " "
	if selected {
		selectionPrefix = SessionSelectionPrefix.Render("▶")
		titleStyle = SessionTitleSelStyle
		toolStyle = SessionStatusSelStyle
		statusStyle = SessionStatusSelStyle
		status = statusStyle.Render(statusIcon)
		// Tree connector also gets selection styling
		treeStyle = TreeConnectorSelStyle
		// Rebuild baseIndent with selection arrow for sub-sessions
		// Replace the │ (or empty space) with ▶ so the arrow doesn't squeeze
		// between tree connector characters (e.g. " │▶├─" → " ▶ ├─")
		if item.IsSubSession {
			groupIndent := strings.Repeat(treeEmpty, max(0, item.Level-2))
			baseIndent = groupIndent + SessionSelectionPrefix.Render(" ▶")
			selectionPrefix = " "
		}
	}

	tool := toolStyle.Render(" " + instTool)
	if (listWidth > 0 && listWidth < 40) || h.toolBadgeHidden(instTool) {
		tool = ""
	}

	// Supervisor badge for the maestro row.
	maestroBadge := ""
	if isMaestro {
		mStyle := lipgloss.NewStyle().Foreground(ColorYellow).Bold(true)
		if selected {
			mStyle = SessionStatusSelStyle
		}
		maestroBadge = mStyle.Render(" [SUPERVISOR]")
	}

	// YOLO badge for Gemini/Codex sessions with YOLO mode enabled
	yoloBadge := ""
	showYolo := false
	if instTool == "gemini" && inst.GeminiYoloMode != nil && *inst.GeminiYoloMode {
		showYolo = true
	} else if instTool == "codex" {
		if opts := inst.GetCodexOptions(); opts != nil && opts.YoloMode != nil && *opts.YoloMode {
			showYolo = true
		}
	} else if instTool == "hermes" {
		// Mirror the toggle path's resolution: per-session override wins; otherwise
		// fall back to the global [hermes].yolo_mode in user config. Without the
		// fallback the badge lies about state for sessions launched with YOLO via
		// global config but no per-session override.
		if opts := inst.GetHermesOptions(); opts != nil && opts.YoloMode != nil {
			showYolo = *opts.YoloMode
		} else if cfg, _ := session.LoadUserConfig(); cfg != nil && cfg.Hermes.YoloMode {
			showYolo = true
		}
	}
	if showYolo {
		yoloStyle := lipgloss.NewStyle().Foreground(ColorYellow).Bold(true)
		if selected {
			yoloStyle = SessionStatusSelStyle
		}
		yoloBadge = yoloStyle.Render(" [YOLO]")
	}

	// Worktree branch badge for sessions running in git worktrees.
	worktreeBadge := ""
	if inst.IsWorktree() && inst.WorktreeBranch != "" {
		branch := inst.WorktreeBranch
		branch = truncateDisplay(branch, 15, "...")
		wtStyle := lipgloss.NewStyle().Foreground(ColorCyan)
		if selected {
			wtStyle = SessionStatusSelStyle
		}
		worktreeBadge = wtStyle.Render(" [" + branch + "]")
	}

	// Sandbox badge for containerized sessions.
	sandboxBadge := ""
	if inst.IsSandboxed() {
		sbStyle := lipgloss.NewStyle().Foreground(ColorCyan)
		if selected {
			sbStyle = SessionStatusSelStyle
		}
		sandboxBadge = sbStyle.Render(" [sandbox]")
	}

	// Multi-repo badge for multi-repo sessions.
	multiRepoBadge := ""
	if inst.IsMultiRepo() {
		mrStyle := lipgloss.NewStyle().Foreground(ColorCyan)
		if selected {
			mrStyle = SessionStatusSelStyle
		}
		pathCount := len(inst.AllProjectPaths())
		multiRepoBadge = mrStyle.Render(fmt.Sprintf(" [multi-repo: %d]", pathCount))
	}

	// SSH badge for remote sessions.
	sshBadge := ""
	if inst.IsSSH() {
		host := inst.SSHHost
		host = truncateDisplay(host, 20, "...")
		sshStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("3")) // yellow
		if selected {
			sshStyle = SessionStatusSelStyle
		}
		sshBadge = sshStyle.Render(" [ssh:" + host + "]")
	}

	// Agent marker for a session owned by an adopted agent.
	//
	// Deliberately one glyph and nothing more. The role, its version, its
	// triggers and its connector health belong in the preview panel's agent
	// card, where there is room to be honest about them; crowding them onto
	// the row would cost the scannability the list depends on. A deck with
	// nothing adopted has an empty index, so no marker renders at all.
	agentBadge := ""
	if _, owned := h.agentRowForSession(inst.ID); owned {
		agStyle := lipgloss.NewStyle().Foreground(ColorCyan)
		if selected {
			agStyle = SessionStatusSelStyle
		}
		agentBadge = agStyle.Render(" ⚙")
	}

	// Last-update timestamp badge — see pickBadgeTime for the formula.
	// Selected rows reuse the selection-bar style instead of dim, so the
	// badge stays legible inside the highlight.
	timestampBadge := ""
	if h.showSessionTimestamps {
		tsStyle := DimStyle
		if selected {
			tsStyle = SessionStatusSelStyle
		}
		var hookStatus *session.HookStatus
		if h.hookWatcher != nil {
			hookStatus = h.hookWatcher.GetHookStatus(inst.ID)
		}
		confirmedTs, confirmedObserved := inst.LastObservedActivity()
		ts := sessionActivityTime(inst.CreatedAt, inst.LastStartedAt, inst.LastActivityAt(), inst.LastAccessedAt, confirmedTs, confirmedObserved, hookStatus)
		timestampBadge = tsStyle.Render(" " + formatRelativeTime(ts))
	}

	// Window expand/collapse chevron for sessions with 2+ windows
	windowChevron := " " // space placeholder to keep status icons aligned
	if h.sessionHasWindows(item) {
		chevronChar := "▾"
		if h.windowsCollapsed[inst.ID] {
			chevronChar = "▸"
		}
		chevronStyle := TreeConnectorStyle
		if selected {
			chevronStyle = TreeConnectorSelStyle
		}
		windowChevron = chevronStyle.Render(chevronChar)
	}

	// Parent-child fold badge (#2631): chevron + child count on a parent row,
	// so a folded subtree is discoverable the same way a collapsed group is.
	childFoldBadge := ""
	if h.sessionHasChildren(item) {
		foldChar := "▾"
		if h.parentChildrenCollapsed[inst.ID] {
			foldChar = "▸"
		}
		foldStyle := TreeConnectorStyle
		if selected {
			foldStyle = TreeConnectorSelStyle
		}
		childFoldBadge = foldStyle.Render(fmt.Sprintf(" %s%d", foldChar, h.sessionChildCount(item)))
	}

	// The session name stays first. A useful pane or saved task description is
	// the optional dim suffix, including for auto-named quick sessions.
	// Snapshot form only here: the per-row Instance.mu reads the inst-based
	// form does can block behind a mid-sweep UpdateStatus writer for seconds,
	// scaling with visible rows (#1753 black-screen).
	displayTitle, paneSubtitle := sessionDisplayLabelsFromState(instState)
	// Pin marker (pin-sessions): a 📌 prefix flags any pinned row. Position in
	// the list conveys top vs bottom; the emoji conveys "this is pinned".
	// Prepended before the AutoName truncation budget so width accounting below
	// stays correct.
	if inst.Pin != session.PinNone {
		displayTitle = "📌 " + displayTitle
	}
	// Maestro (fleet supervisor): ⬢ glyph leads the title.
	if isMaestro {
		displayTitle = "⬢ " + displayTitle
	}
	// Include the stored slot in the existing badge/title cell budget. Normal
	// titles need the same reservation as auto-names so the badge stays visible.
	reserved := leftGutterWidth + cellWidth(baseIndent) + cellWidth(selectionPrefix) +
		cellWidth(treeStyle.Render(treeConnector)) + cellWidth(windowChevron) + cellWidth(childFoldBadge) +
		cellWidth(status) + 1 + cellWidth(tool) +
		cellWidth(maestroBadge) + cellWidth(yoloBadge) + cellWidth(worktreeBadge) +
		cellWidth(sandboxBadge) + cellWidth(multiRepoBadge) + cellWidth(sshBadge) +
		cellWidth(agentBadge) + cellWidth(timestampBadge)
	// Keep a useful title before spending narrow-row space on a worktree badge.
	if listWidth > 0 && listWidth < 40 && worktreeBadge != "" {
		originalWidth := cellWidth(worktreeBadge)
		badgeWidth := max(0, listWidth-(reserved-originalWidth)-minSessionTitleWidth-1)
		if originalWidth > badgeWidth {
			badge := ""
			if badgeWidth >= 4 {
				branch := strings.TrimSuffix(strings.TrimPrefix(stripAnsi(worktreeBadge), " ["), "]")
				badge = " [" + cellTruncate(branch, badgeWidth-3, "…") + "]"
			}
			if selected {
				worktreeBadge = SessionStatusSelStyle.Render(badge)
			} else {
				worktreeBadge = lipgloss.NewStyle().Foreground(ColorCyan).Render(badge)
			}
			reserved = reserved - originalWidth + cellWidth(badge)
		}
	}
	// Reserve the title's floor before the viewers and account badges claim
	// any of the remaining width, so a narrow column shrinks and then drops
	// the badges instead of collapsing the title (#2201). The viewers badge
	// (how many terminals have this session open, from the per-socket cache
	// so the row never spawns tmux) is fitted first: it is the smaller of
	// the two and says the session is being looked at right now; the
	// account badge takes what is left.
	badgeBudget := -1
	if listWidth > 0 {
		available := max(0, listWidth-reserved-2)
		titleFloor := min(available, minSessionTitleWidth)
		badgeBudget = max(0, available-titleFloor)
	}
	cachedViewers, cachedViewersKnown := inst.ViewersCached()
	viewersBadgeText, viewersWidth := fitViewersBadge(cachedViewers, cachedViewersKnown, selected, badgeBudget)
	// ⚡N: how many triggers wake this session (triggers_live.go). Read from
	// the poll's cached map only; it shrinks and drops after the viewers
	// badge and before the account badge.
	triggersBudget := badgeBudget
	if badgeBudget >= 0 {
		triggersBudget = max(0, badgeBudget-viewersWidth)
	}
	triggersBadgeText, triggersWidth := fitTriggersBadge(h.triggerBadges[inst.ID], selected, triggersBudget)
	accountBudget := instState.accountDisplay.width
	if badgeBudget >= 0 {
		accountBudget = min(accountBudget, badgeBudget-viewersWidth-triggersWidth)
	}
	accountBadge, accountWidth := instState.accountDisplay.fit(accountBudget)
	if accountBadge != "" {
		accountStyle := DimStyle
		if selected {
			accountStyle = SessionStatusSelStyle
		}
		accountBadge = accountStyle.Render(accountBadge)
	}
	if listWidth > 0 {
		budget := max(0, listWidth-reserved-viewersWidth-triggersWidth-accountWidth-1)
		if cellWidth(displayTitle) > budget {
			displayTitle = cellTruncate(displayTitle, budget, "…")
		}
	}
	title := titleStyle.Render(displayTitle)

	// Build row: [gutter][baseIndent][selection][tree][chevron][status] [title] [tool] [badges]
	// The leading gutter (leftGutterWidth) keeps sessions aligned with group
	// rows, which reserve the same gutter for root hotkey numbers.
	row := fmt.Sprintf(
		"%s%s%s%s%s%s %s%s%s%s%s%s%s%s%s%s%s%s%s%s",
		strings.Repeat(" ", leftGutterWidth),
		baseIndent,
		selectionPrefix,
		treeStyle.Render(treeConnector),
		windowChevron,
		status,
		title,
		tool,
		maestroBadge,
		yoloBadge,
		worktreeBadge,
		sandboxBadge,
		multiRepoBadge,
		sshBadge,
		agentBadge,
		viewersBadgeText,
		triggersBadgeText,
		accountBadge,
		timestampBadge,
		childFoldBadge,
	)

	// Append pane title filling remaining row space (only for the selected item).
	// #937 v2: cellWidth/cellTruncate (not lipgloss.Width / ansi.Truncate)
	// for both the row budget and the pane-title fit check. pane titles
	// often surface tmux pane content which can contain keycap glyphs
	// (#️⃣ 0️⃣–9️⃣ *️⃣) — uniseg reports those at 1 cell, terminals render 2,
	// so the prior measurement let the trailing pane-title text overflow
	// the panel and shove subsequent rows down by one cell. See
	// internal/ui/cellwidth.go for the upstream disagreement.
	if (selected || h.showPaneTitles) && paneSubtitle != "" {
		// The snapshot label helper has already omitted generic pane titles.
		// Dual layout: sidebar is narrower than h.width (#937). Using full
		// terminal width here overflows the SESSIONS pane, then lipgloss
		// truncation disagrees from terminal cells — wrapped lines duplicate
		// rows visually and mouseY→item indexing breaks until scroll settles.
		remaining := listWidth - cellWidth(row) - 2 // -2 for trailing margin
		if remaining > 10 {
			pt := paneSubtitle
			if cellWidth(pt) > remaining {
				pt = cellTruncate(pt, remaining, "…")
			}
			row += DimStyle.Render(" " + pt)
		}
	}

	b.WriteString(row)
	b.WriteString("\n")
}

// openSessionSwitcher pops the switcher pre-highlighted on fromID (the session
// we came from), so an immediate Enter returns there. reattachOnCancel marks
// whether Esc should re-attach to fromID (true when opened from an attached
// session) or simply close back to the overview (false when opened from the
// overview). It deliberately does NOT arm the idle auto-commit: opening alone
// never commits, so a stray Ctrl+S just shows the list. Auto-commit is armed
// only once the user cycles (Ctrl+S/Ctrl+A) at least once inside the picker
// (see handleSessionSwitcherKey). When fewer than two switchable sessions exist
// the picker stays closed.
//
// Local-only by design: this feeds local h.instances and re-attaches via the
// local tmux attach loop. Remote (ItemTypeRemoteSession) rows are intentionally
// excluded for now — see SessionSwitcher.Show and
// TestSessionSwitcher_RemoteSessionsUnsupported.
func (h *Home) openSessionSwitcher(fromID string, reattachOnCancel bool) {
	h.instancesMu.RLock()
	instances := make([]*session.Instance, len(h.instances))
	copy(instances, h.instances)
	h.instancesMu.RUnlock()

	// Mirror the overview: surface each session's dim conversation/pane title
	// (e.g. the Claude conversation summary) from the same render snapshot.
	subtitles := make(map[string]string, len(instances))
	for _, inst := range instances {
		if inst == nil {
			continue
		}
		if pt := h.getSessionRenderState(inst).paneTitle; pt != "" {
			subtitles[inst.ID] = pt
		}
	}

	h.sessionSwitcher.SetSize(h.width, h.height)
	if !h.sessionSwitcher.Show(fromID, instances, subtitles) {
		return
	}
	// Snapshot the row labels at open time so the switcher's View renders
	// lock-free (#1753) — see SessionSwitcher.labels.
	labels := make(map[string]sessionRenderState, len(instances))
	for _, inst := range instances {
		if inst != nil {
			labels[inst.ID] = h.getSessionRenderState(inst)
		}
	}
	h.sessionSwitcher.labels = labels
	h.sessionSwitcher.reattachOnCancel = reattachOnCancel
	// Keep the underlying sidebar on the same session as the picker from
	// the first frame. This matters when Ctrl+S was opened from a full-screen
	// attach whose session was not the dashboard's last cursor position.
	h.syncSessionSwitcherSidebarSelection()
	// Treat the opening Ctrl+S as the first advance so key-repeat that arrives
	// right after the attach->TUI handoff is throttled instead of spinning.
	h.sessionSwitcher.lastCycleAt = time.Now()
	// Invalidate any idle-commit timer still in flight from a previous picker
	// session, and schedule none: auto-commit arms only once the user cycles
	// (Ctrl+S/Ctrl+A) inside this picker.
	h.sessionSwitcher.bumpCommitGen()
}

// openEmbeddedSessionSwitcher records that commit and Esc should return through
// the same embedded-pane path the user came from. Full-screen and overview
// switchers retain their existing attach behavior.
func (h *Home) openEmbeddedSessionSwitcher(fromID string) {
	h.openSessionSwitcher(fromID, true)
	if h.sessionSwitcher.IsVisible() {
		h.sessionSwitcher.embeddedOnAttach = true
		// Ctrl+S deliberately reveals the dashboard sidebar so its selection can
		// mirror the picker highlight. Keep that visible layout when the idle
		// commit re-enters the selected session; restoring the old collapsed
		// state here made the sidebar disappear as soon as the user released W.
		h.embeddedSidebarHidden = false
	}
}

// armSwitcherCommit (re)starts the idle-commit countdown and returns the timer
// command. Ctrl+S / Ctrl+A call this, so the timer only fires once the user
// stops tapping — the closest we can get to "commit on key release".
func (h *Home) armSwitcherCommit() tea.Cmd {
	gen := h.sessionSwitcher.bumpCommitGen()
	if h.sessionSwitcher.embeddedOnAttach {
		// A timer cannot acknowledge ownership of prefetched input. An
		// embedded handoff requires the fenced Enter/Esc event instead.
		return nil
	}
	return tea.Tick(switcherIdleCommit, func(time.Time) tea.Msg {
		return switcherCommitMsg{gen: gen}
	})
}

// syncSessionSwitcherSidebarSelection maps the floating picker's highlight to
// the ordinary sidebar cursor. The dashboard remains visible beneath Ctrl+S,
// so keeping both cursors on the same session makes the spatial relationship
// explicit instead of leaving the sidebar parked on the origin row.
func (h *Home) syncSessionSwitcherSidebarSelection() {
	sel := h.sessionSwitcher.GetSelected()
	if sel == nil {
		return
	}
	h.selectSidebarSessionForSwitcher(sel.ID)
}

func (h *Home) selectSidebarSessionForSwitcher(id string) {
	if idx := h.flatItemIndexByID(id); idx >= 0 {
		h.cursor = idx
		h.syncViewport()
		return
	}
	// Production Home always owns a GroupTree; the nil guard keeps lightweight
	// renderer/interaction fixtures from needing the entire dashboard graph.
	if h.groupTree != nil {
		h.SelectSessionByID(id)
	}
}

// handleSwitcherCommit commits the highlighted session when the idle timer that
// fired is the current one (no later keypress superseded it).
func (h *Home) handleSwitcherCommit(msg switcherCommitMsg) tea.Cmd {
	if !h.sessionSwitcher.IsVisible() || h.sessionSwitcher.embeddedOnAttach || msg.gen != h.sessionSwitcher.commitGen {
		return nil
	}
	return h.commitSessionSwitch()
}

// commitSessionSwitch hides the switcher and re-attaches to the highlighted
// session.
func (h *Home) commitSessionSwitch() tea.Cmd {
	target := ""
	if sel := h.sessionSwitcher.GetSelected(); sel != nil {
		target = sel.ID
	}
	embedded := h.sessionSwitcher.embeddedOnAttach
	h.sessionSwitcher.Hide()
	return h.attachToSwitchTargetInMode(target, embedded)
}

// scrollbackCaptureLines is how many trailing history lines the pager captures.
// Deep enough to reach the banner of a long session (tmux's history-limit is
// typically 50k-500k) while bounded so a pathological pane can't balloon memory
// or wedge the capture. The preview pane's 2000-line cap is deliberately left
// untouched — this is a one-off, on-demand capture.
const scrollbackCaptureLines = 50000

// openScrollbackPager opens the in-view scrollback pager (#1491) bound to the
// session with the given ID and returns the async command that captures its
// tmux history. The pager opens in a loading state; scrollbackContentMsg
// installs the content (stale-guarded by session ID). Returns nil when the
// session no longer exists.
func (h *Home) openScrollbackPager(sessionID string) tea.Cmd {
	if sessionID == "" || h.scrollbackPager == nil {
		return nil
	}
	h.instancesMu.RLock()
	inst := h.instanceByID[sessionID]
	h.instancesMu.RUnlock()
	if inst == nil {
		return nil
	}
	tmuxSess := inst.GetTmuxSession()
	if tmuxSess == nil {
		return nil
	}
	title := strings.TrimSpace(inst.Title)
	h.scrollbackPager.Show(title, sessionID, h.width, h.height)
	return func() tea.Msg {
		content, err := tmuxSess.CaptureHistoryLines(scrollbackCaptureLines)
		return scrollbackContentMsg{sessionID: sessionID, content: content, err: err}
	}
}

// handleScrollbackPagerKey handles key events while the scrollback pager is
// visible (#1491). Navigation scrolls the captured history; Esc re-attaches to
// the session (back to where the user was), and the detach key (Ctrl+Q) closes
// the pager to the session list without re-attaching.
func (h *Home) handleScrollbackPagerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := h.scrollbackPager
	switch msg.String() {
	case "up", "k":
		p.ScrollUp(1)
	case "down", "j":
		p.ScrollDown(1)
	case "pgup", "b":
		p.PageUp()
	case "pgdown", " ", "f":
		p.PageDown()
	case "home", "g":
		p.Top()
	case "end", "G":
		p.Bottom()
	case "esc", "q":
		// Return to the session the user was reading.
		target := p.SessionID()
		p.Hide()
		return h, h.attachToSwitchTarget(target)
	case "ctrl+q":
		// Close to the session list without re-attaching.
		p.Hide()
		return h, nil
	}
	return h, nil
}

// attachToSwitchTarget re-attaches to the session with the given ID and lands
// the list cursor there on the next detach. Returns nil if the session is gone.
func (h *Home) attachToSwitchTarget(id string) tea.Cmd {
	return h.attachToSwitchTargetInMode(id, false)
}

func (h *Home) attachToSwitchTargetInMode(id string, embedded bool) tea.Cmd {
	if id == "" {
		return nil
	}
	h.instancesMu.RLock()
	inst := h.instanceByID[id]
	h.instancesMu.RUnlock()
	if inst == nil {
		return nil
	}
	if embedded {
		if !h.SelectSessionByID(id) {
			return nil
		}
		if h.enterEmbeddedMode() {
			return h.startEmbeddedModeCmd()
		}
		return nil
	}
	h.lastNotifSwitchMu.Lock()
	h.lastNotifSwitchID = id
	h.lastNotifSwitchMu.Unlock()
	return h.attachSession(inst)
}

// handleSessionSwitcherKey handles key events when the in-attach switcher is
// visible. Two interaction modes share the overlay:
//
//   - Ctrl+S (forward) / Ctrl+A (backward): the quick "tap and let go" mode.
//     Each tap re-arms the idle-commit timer (so it fires ~1s after you stop),
//     and the advance is throttled so holding the key cannot spin the list.
//   - Up / Down: deliberate browsing. These cancel the pending auto-commit, so
//     you stay in the switcher until you press Enter (or Esc).
//
// Embedded switchers require Enter/Esc and never use idle commit.
// Enter attaches to the highlight. Esc, when the picker was opened from an
// attached session, re-attaches to where you came from (you meant to switch,
// not to leave); when opened from the overview it just closes. Ctrl+Q (the
// detach key) always drops to the overview.
func (h *Home) handleSessionSwitcherKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		return h, h.commitSessionSwitch()
	case "esc":
		reattach := h.sessionSwitcher.reattachOnCancel
		embedded := h.sessionSwitcher.embeddedOnAttach
		fromID := h.sessionSwitcher.fromID
		h.sessionSwitcher.Hide()
		// Cancel means the origin is selected again in both the sidebar and
		// attach target. Overview-opened switchers also restore their origin.
		h.selectSidebarSessionForSwitcher(fromID)
		if reattach {
			return h, h.attachToSwitchTargetInMode(fromID, embedded)
		}
		return h, nil
	case "ctrl+q":
		// Detach key: leave the switcher (and any session), landing in the overview.
		h.sessionSwitcher.Hide()
		return h, nil
	case "ctrl+s":
		if h.sessionSwitcher.cycle(true, time.Now()) {
			h.syncSessionSwitcherSidebarSelection()
		}
		return h, h.armSwitcherCommit()
	case "ctrl+a":
		if h.sessionSwitcher.cycle(false, time.Now()) {
			h.syncSessionSwitcherSidebarSelection()
		}
		return h, h.armSwitcherCommit()
	case "up":
		h.sessionSwitcher.prev()
		h.syncSessionSwitcherSidebarSelection()
		h.sessionSwitcher.bumpCommitGen() // cancel pending auto-commit: manual mode
		return h, nil
	case "down":
		h.sessionSwitcher.next()
		h.syncSessionSwitcherSidebarSelection()
		h.sessionSwitcher.bumpCommitGen()
		return h, nil
	default:
		// Ignore other keys (incl. Tab) without disturbing the commit timer.
		return h, nil
	}
}
