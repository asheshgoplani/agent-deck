package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
	"golang.org/x/sync/errgroup"

	"github.com/asheshgoplani/agent-deck/internal/clipboard"
	"github.com/asheshgoplani/agent-deck/internal/costs"
	"github.com/asheshgoplani/agent-deck/internal/feedback"
	"github.com/asheshgoplani/agent-deck/internal/git"
	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/sysinfo"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
	"github.com/asheshgoplani/agent-deck/internal/update"
	"github.com/asheshgoplani/agent-deck/internal/watcher"
	"github.com/asheshgoplani/agent-deck/internal/web"
)

// Version is set by main.go for update checking
var Version = "0.0.0"

// SetVersion sets the current version for update checking
func SetVersion(v string) {
	Version = v
}

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
	pipeUILog = logging.ForComponent("pipe")
)

const (
	// tickInterval for UI refresh and status updates
	// Background worker polls at 2s intervals for status detection
	// At 2s: 2-5 CapturePane() calls/sec = minimal CPU overhead
	tickInterval = 2 * time.Second

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

// Minimum terminal size requirements (reduced for mobile support)
const (
	minTerminalWidth  = 40 // Reduced from 80 - supports mobile terminals
	minTerminalHeight = 12 // Reduced from 20 - supports smaller screens
)

// FilterKeyActive is the keyboard shortcut for the "open" status filter
// (shows all sessions except error/stopped). Change this constant to rebind.
const FilterKeyActive = "%"

// FilterModeActive is the filter value for "open" sessions: excludes error/stopped.
// This is NOT a session status (never assigned to a session), just a filter mode.
const FilterModeActive session.Status = "active"

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

// Home is the main application model
type Home struct {
	// Dimensions
	width  int
	height int

	// Profile
	profile string // The profile this Home is displaying

	// Data (protected by instancesMu for background worker access)
	instances    []*session.Instance
	instanceByID map[string]*session.Instance // O(1) instance lookup by ID
	instancesMu  sync.RWMutex                 // Protects instances slice for thread-safe background access
	storage      *session.Storage
	groupTree    *session.GroupTree
	flatItems    []session.Item // Flattened view for cursor navigation

	// Components
	search               *Search
	globalSearch         *GlobalSearch              // Global session search across all Claude conversations
	globalSearchIndex    *session.GlobalSearchIndex // Search index (nil if disabled)
	newDialog            *NewDialog
	groupDialog          *GroupDialog          // For creating/renaming groups
	forkDialog           *ForkDialog           // For forking sessions
	confirmDialog        *ConfirmDialog        // For confirming destructive actions
	helpOverlay          *HelpOverlay          // For showing keyboard shortcuts
	mcpDialog            *MCPDialog            // For managing MCPs
	editPathsDialog      *EditPathsDialog      // For editing multi-repo paths
	skillDialog          *SkillDialog          // For managing project skills
	setupWizard          *SetupWizard          // For first-run setup
	settingsPanel        *SettingsPanel        // For editing settings
	analyticsPanel       *AnalyticsPanel       // For displaying session analytics
	geminiModelDialog    *GeminiModelDialog    // For selecting Gemini model
	sessionPickerDialog  *SessionPickerDialog  // For sending output to another session
	worktreeFinishDialog *WorktreeFinishDialog // For finishing worktree sessions (merge + cleanup)
	feedbackDialog       *FeedbackDialog       // For in-app feedback popup (Phase 2)
	feedbackState        *feedback.State       // Loaded at first show, avoids repeated disk I/O
	feedbackSender       *feedback.Sender      // Sender constructed once in NewHome (Phase 3, per D-05)
	watcherPanel         *WatcherPanel         // For showing watcher status and events
	watcherEngine        *watcher.Engine       // nil until Init (D-07: lifecycle tied to TUI startup)

	// Configurable hotkeys
	hotkeys        map[string]string // action -> configured key
	hotkeyLookup   map[string]string // pressed key -> canonical key used by switch cases
	blockedHotkeys map[string]bool   // canonical keys disabled via remap/unbind

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

	// State
	cursor              int            // Selected item index in flatItems
	viewOffset          int            // First visible item index (for scrolling)
	previewScrollOffset int            // Lines scrolled up from tail in the preview pane (#574). 0 = tail (default). Reset on cursor move.
	isAttaching         atomic.Bool    // Prevents View() output during attach (fixes Bubble Tea Issue #431) - atomic for thread safety
	statusFilter        session.Status // Filter sessions by status ("" = all, or specific status)
	groupScope          string         // Limit TUI to a specific group path ("" = all groups)
	previewMode         PreviewMode    // What to show in preview pane (both, output-only, analytics-only)
	err                 error
	errTime             time.Time  // When error occurred (for auto-dismiss)
	isReloading         bool       // Visual feedback during auto-reload
	initialLoading      bool       // True until first loadSessionsMsg received (shows splash screen)
	isQuitting          bool       // True when user pressed q, shows quitting splash
	reloadVersion       uint64     // Incremented on each reload to prevent stale background saves
	reloadMu            sync.Mutex // Protects reloadVersion, isReloading, and lastLoadMtime for thread-safe access
	lastLoadMtime       time.Time  // File mtime when we last loaded (for external change detection)

	// Preview cache (async fetching - View() must be pure, no blocking I/O)
	previewCache      map[string]string    // previewKey -> cached preview content
	previewCacheTime  map[string]time.Time // previewKey -> when cached (for expiration)
	previewCacheMu    sync.RWMutex         // Protects previewCache for thread-safety
	previewFetchingID string               // ID currently being fetched (prevents duplicate fetches)

	// Preview debouncing (PERFORMANCE: prevents subprocess spawn on every keystroke)
	// During rapid navigation, we delay preview fetch by 150ms to let navigation settle
	pendingPreviewKey string     // Preview key waiting for debounced fetch
	previewDebounceMu sync.Mutex // Protects pendingPreviewKey

	// Round-robin status updates (Priority 1A optimization)
	// Instead of updating ALL sessions every tick, we update batches of 5-10 sessions
	// This reduces CPU usage by 90%+ while maintaining responsiveness
	statusUpdateIndex atomic.Int32 // Current position in round-robin cycle (atomic for thread safety)

	// Background status worker (Priority 1C optimization)
	// Moves status updates to a separate goroutine, completely decoupling from UI
	statusTrigger       chan statusUpdateRequest // Triggers background status update
	statusWorkerDone    chan struct{}            // Signals worker has stopped
	lastFullStatusSweep atomic.Int64             // UnixNano timestamp of last full background status sweep
	lastPersistedStatus map[string]string        // instanceID -> last status written to SQLite

	// PERFORMANCE: Worker pool for output-driven status updates (Priority 2)
	// Caps the number of goroutines spawned for %output events from control pipes
	logUpdateChan chan *session.Instance // Buffers status update requests from PipeManager
	logWorkerWg   sync.WaitGroup         // Tracks log worker goroutines for clean shutdown

	// PERFORMANCE: Debounce output activity status updates
	lastLogActivity map[string]time.Time // sessionID -> last update time
	logActivityMu   sync.Mutex           // Protects lastLogActivity map

	// Window toggle state — sessions with collapsed window sub-items
	windowsCollapsed map[string]bool // sessionID -> true if windows hidden

	// Worktree dirty status cache (lazy, 10s TTL)
	worktreeDirtyCache   map[string]bool      // sessionID -> isDirty
	worktreeDirtyCacheTs map[string]time.Time // sessionID -> cache timestamp
	worktreeDirtyMu      sync.Mutex           // Protects dirty cache maps

	// Memory management: periodic cache pruning
	lastCachePrune time.Time

	// Hook-based status detection (Claude Code lifecycle hooks)
	hookWatcher        *session.StatusFileWatcher
	pendingHooksPrompt bool // True if user should be prompted to install hooks

	// Context-% based /clear for conductor sessions with clear_on_compact
	clearOnCompactSent map[string]time.Time // instanceID -> last /clear send time (debounce)

	// File watcher for external changes (auto-reload)
	storageWatcher *StorageWatcher

	// Optional in-memory web menu data sink for web mode.
	webMenuData   *web.MemoryMenuData
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

	// Launching animation state (for newly created sessions)
	launchingSessions  map[string]time.Time        // sessionID -> creation time
	resumingSessions   map[string]time.Time        // sessionID -> resume time (for restart/resume)
	mcpLoadingSessions map[string]time.Time        // sessionID -> MCP reload time
	forkingSessions    map[string]time.Time        // sessionID -> fork start time (fork in progress)
	creatingSessions   map[string]*CreatingSession // tempID -> placeholder for worktree creation in progress
	animationFrame     int                         // Current frame for spinner animation

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

	// Vi-style gg to jump to top (#38)
	lastGTime time.Time // When 'g' was last pressed (double-tap within 500ms jumps to top)

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

	// Jump mode (vimium-style hint navigation)
	jumpMode   bool   // True when jump mode is active
	jumpBuffer string // Characters typed so far in jump mode

	// Cached status counts (invalidated on instance changes)
	cachedStatusCounts struct {
		running, waiting, idle, errored int
		valid                           atomic.Bool // THREAD-SAFE: accessed from main and worker goroutines
		timestamp                       time.Time   // For time-based expiration
	}

	// Full repaint mode: issue tea.ClearScreen every tick to avoid
	// incremental redraw drift in terminals with unicode grapheme widths
	fullRepaint       bool
	defaultFilter     string // from config.toml [display] default_filter
	activeFilterLabel string // from config.toml [display] active_filter_label

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

	// Cursor sync: track last notification bar switch during attach
	// When user switches sessions via Ctrl+b N while attached (tea.Exec),
	// we record the target session ID so cursor can follow after detach
	lastNotifSwitchID string
	lastNotifSwitchMu sync.Mutex

	// Undo delete stack (Chrome-style: Ctrl+Z restores in reverse order)
	undoStack []deletedSessionEntry

	// Pending title changes: survives reload races.
	// When a rename save is skipped (isReloading=true), the title change is
	// stored here and re-applied after the reload completes.
	pendingTitleChanges map[string]string

	// UI state persistence across restarts
	pendingCursorRestore *uiState // Consumed on first loadSessionsMsg to restore cursor
	uiStateSaveTicks     int      // Counter for periodic UI state saves in tick handler

	// Remote sessions (Phase 2: Agent-Deck Remotes)
	remoteSessions     map[string][]session.RemoteSessionInfo // remoteName -> sessions
	remoteSessionsMu   sync.RWMutex
	lastRemoteFetch    time.Time // When remote sessions were last fetched
	remotesFetchActive bool      // Prevents overlapping fetches

	// Cost tracking
	costStore         *costs.Store
	costPricer        *costs.Pricer
	costBudget        *costs.BudgetChecker
	costToday         atomic.Int64 // microdollars
	costWeek          atomic.Int64 // microdollars
	costRefreshTime   time.Time
	showCostDashboard bool
	costDashboard     costDashboard

	// System stats collector (CPU, RAM, disk, etc.)
	sysStatsCollector *sysinfo.Collector
	sysStatsConfig    session.SystemStatsSettings
}

// reloadState preserves UI state during storage reload
type reloadState struct {
	cursorSessionID string          // ID of session at cursor (if cursor on session)
	cursorGroupPath string          // Path of group at cursor (if cursor on group)
	expandedGroups  map[string]bool // Expanded group paths
	viewOffset      int             // Scroll position
}

// uiState persists cursor, preview mode, and status filter across restarts
type uiState struct {
	CursorSessionID string `json:"cursor_session_id,omitempty"`
	CursorGroupPath string `json:"cursor_group_path,omitempty"`
	PreviewMode     int    `json:"preview_mode"`
	StatusFilter    string `json:"status_filter,omitempty"`
}

type selectedItemIdentity struct {
	groupPath       string
	sessionID       string
	windowSessionID string
	windowIndex     int
}

func (h *Home) reloadHotkeysFromConfig() {
	h.setHotkeys(resolveHotkeys(session.GetHotkeyOverrides()))
}

func (h *Home) detachByte() byte {
	return ResolvedDetachByte(session.GetHotkeyOverrides())
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
}

func (h *Home) normalizeMainKey(pressed string) string {
	if canonical, ok := h.hotkeyLookup[pressed]; ok {
		return canonical
	}
	if h.blockedHotkeys[pressed] {
		return ""
	}
	return pressed
}

func (h *Home) actionKey(action string) string {
	return actionHotkey(h.hotkeys, action)
}

// deletedSessionEntry holds a deleted session for undo restore
type deletedSessionEntry struct {
	instance  *session.Instance
	deletedAt time.Time
}

// getLayoutMode returns the current layout mode based on terminal width
func (h *Home) getLayoutMode() string {
	switch {
	case h.width < layoutBreakpointSingle:
		return LayoutModeSingle
	case h.width < layoutBreakpointStacked:
		return LayoutModeStacked
	default:
		return LayoutModeDual
	}
}

// Messages
type loadSessionsMsg struct {
	instances    []*session.Instance
	groups       []*session.GroupData
	err          error
	restoreState *reloadState // Optional state to restore after reload
	poolProxies  int          // Number of socket proxies started
	poolError    error        // Pool initialization error
	loadMtime    time.Time    // File mtime at load time (for external change detection)
}

type sessionCreatedMsg struct {
	instance *session.Instance
	err      error
	tempID   string // matches creatingSessions key for placeholder removal
}

type sessionForkedMsg struct {
	instance *session.Instance
	sourceID string // ID of the source session that was forked (for cleanup)
	err      error
}

type refreshMsg struct{}

type statusUpdateMsg struct {
	attachedSessionID string // Session that just returned from attach (if local attach)
	attachedWorkDir   string // pane_current_path captured after attach returns
} // Triggers immediate status update without reloading

type attachReturnRefreshMsg struct{}

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

// remoteSessionsFetchedMsg is sent when async remote sessions fetch completes.
type remoteSessionsFetchedMsg struct {
	sessions map[string][]session.RemoteSessionInfo
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

func newNotesEditor() textarea.Model {
	editor := textarea.New()
	editor.ShowLineNumbers = false
	editor.Placeholder = "Write notes for this session..."
	editor.Prompt = ""
	editor.Blur()
	return editor
}

// NewHome creates a new home model with the default profile
func NewHome() *Home {
	return NewHomeWithProfile("")
}

// NewHomeWithProfile creates a new home model with the specified profile.
func NewHomeWithProfile(profile string) *Home {
	return NewHomeWithProfileAndMode(profile)
}

// NewHomeWithProfileAndMode creates a new Home with the specified profile.
// All instances manage the notification bar equally via shared SQLite state.
func NewHomeWithProfileAndMode(profile string) *Home {
	ctx, cancel := context.WithCancel(context.Background())

	var storageWarning string
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		// Log the error and set warning - sessions won't persist but app will still function
		uiLog.Warn("storage_init_failed", slog.String("error", err.Error()))
		storageWarning = fmt.Sprintf("⚠ Storage unavailable: %v (sessions won't persist)", err)
		storage = nil
	}

	// Ensure StateDB global is set for cross-package status writes.
	// Registration and election happen in main.go before NewHome is called.
	// This fallback handles CLI paths (e.g., NewHomeWithProfile) that skip main.go setup.
	if storage != nil && statedb.GetGlobal() == nil {
		if db := storage.GetDB(); db != nil {
			statedb.SetGlobal(db)
			_ = db.RegisterInstance(false)
		}
	}

	// Get the actual profile name (could be resolved from env var or config)
	actualProfile := session.DefaultProfile
	if storage != nil {
		actualProfile = storage.Profile()
	}

	h := &Home{
		profile:              actualProfile,
		storage:              storage,
		storageWarning:       storageWarning,
		search:               NewSearch(),
		newDialog:            NewNewDialog(),
		groupDialog:          NewGroupDialog(),
		forkDialog:           NewForkDialog(),
		confirmDialog:        NewConfirmDialog(),
		helpOverlay:          NewHelpOverlay(),
		mcpDialog:            NewMCPDialog(),
		editPathsDialog:      NewEditPathsDialog(),
		skillDialog:          NewSkillDialog(),
		setupWizard:          NewSetupWizard(),
		settingsPanel:        NewSettingsPanel(),
		analyticsPanel:       NewAnalyticsPanel(),
		geminiModelDialog:    NewGeminiModelDialog(),
		sessionPickerDialog:  NewSessionPickerDialog(),
		worktreeFinishDialog: NewWorktreeFinishDialog(),
		feedbackDialog:       NewFeedbackDialog(),
		feedbackSender:       feedback.NewSender(),
		watcherPanel:         NewWatcherPanel(),
		cursor:               0,
		initialLoading:       true, // Show splash until sessions load
		ctx:                  ctx,
		cancel:               cancel,
		instances:            []*session.Instance{},
		instanceByID:         make(map[string]*session.Instance),
		groupTree:            session.NewGroupTree([]*session.Instance{}),
		flatItems:            []session.Item{},
		previewCache:         make(map[string]string),
		previewCacheTime:     make(map[string]time.Time),
		analyticsCache:       make(map[string]*session.SessionAnalytics),
		geminiAnalyticsCache: make(map[string]*session.GeminiSessionAnalytics),
		analyticsCacheTime:   make(map[string]time.Time),
		clearOnCompactSent:   make(map[string]time.Time),
		launchingSessions:    make(map[string]time.Time),
		resumingSessions:     make(map[string]time.Time),
		mcpLoadingSessions:   make(map[string]time.Time),
		forkingSessions:      make(map[string]time.Time),
		creatingSessions:     make(map[string]*CreatingSession),
		lastLogActivity:      make(map[string]time.Time),
		windowsCollapsed:     make(map[string]bool),
		worktreeDirtyCache:   make(map[string]bool),
		worktreeDirtyCacheTs: make(map[string]time.Time),
		statusTrigger:        make(chan statusUpdateRequest, 1), // Buffered to avoid blocking
		statusWorkerDone:     make(chan struct{}),
		lastPersistedStatus:  make(map[string]string),
		logUpdateChan:        make(chan *session.Instance, 100), // Buffered to absorb bursts
		hotkeys:              make(map[string]string),
		hotkeyLookup:         make(map[string]string),
		blockedHotkeys:       make(map[string]bool),
		notesEditor:          newNotesEditor(),
		boundKeys:            make(map[string]string),
		undoStack:            make([]deletedSessionEntry, 0, 10),
		pendingTitleChanges:  make(map[string]string),
		debugMode:            logging.IsDebugEnabled(),
		lastClickIndex:       -1,
	}
	h.sessionRenderSnapshot.Store(make(map[string]sessionRenderState))

	h.reloadHotkeysFromConfig()

	// Cache display settings (config.toml [display])
	if cfg, _ := session.LoadUserConfig(); cfg != nil {
		h.fullRepaint = cfg.Display.GetFullRepaint()
		h.defaultFilter = cfg.Display.GetDefaultFilter()
		h.activeFilterLabel = cfg.Display.ActiveFilterLabel
		h.sysStatsConfig = cfg.SystemStats
	} else {
		h.fullRepaint = (session.DisplaySettings{}).GetFullRepaint()
	}

	// Initialize system stats collector if enabled
	if h.sysStatsConfig.GetEnabled() {
		h.sysStatsCollector = sysinfo.NewCollector(h.sysStatsConfig.GetRefreshSeconds(), nil)
	}

	// Keep settings panel profile-aware so profile overrides (e.g., Claude config dir)
	// are displayed and edited in the correct scope.
	h.settingsPanel.SetProfile(actualProfile)

	// Restore persisted UI state (preview mode, status filter, cursor position)
	h.loadUIState()

	// Apply default_filter from config if no filter was restored from persisted state.
	// Auto-clears if no sessions match (handled in rebuildFlatItems).
	if h.statusFilter == "" && h.defaultFilter != "" {
		h.statusFilter = session.Status(h.defaultFilter)
	}

	tmuxSettings := session.GetTmuxSettings()
	h.manageTmuxNotifications = tmuxSettings.GetInjectStatusLine()

	// Initialize notification manager if enabled in config and tmux status injection is allowed.
	// All instances manage the notification bar (they share SQLite state, so produce identical output)
	notifSettings := session.GetNotificationsSettings()
	if notifSettings.Enabled && h.manageTmuxNotifications {
		h.notificationsEnabled = true
		h.notificationManager = session.NewNotificationManager(notifSettings.MaxShown, notifSettings.ShowAll, notifSettings.Minimal)

		// Initialize tmux status bar options for proper notification display
		// Fixes truncation (default status-left-length is only 10 chars)
		_ = tmux.InitializeStatusBarOptions()

	}

	// Bind mouse click on status-right to detach (click the "ctrl+q/click detach" hint)
	// This is unconditional — the status-right always shows the detach hint
	_ = tmux.BindMouseStatusRightDetach()

	// Initialize event-driven status detection
	// Output callback: invoked when PipeManager detects %output from a session
	outputCallback := func(sessionName string) {
		h.instancesMu.RLock()
		for _, inst := range h.instances {
			if inst.GetTmuxSession() != nil && inst.GetTmuxSession().Name == sessionName {
				h.logActivityMu.Lock()
				lastUpdate := h.lastLogActivity[inst.ID]
				if time.Since(lastUpdate) < logOutputDebounce {
					h.logActivityMu.Unlock()
					break
				}
				h.lastLogActivity[inst.ID] = time.Now()
				h.logActivityMu.Unlock()

				select {
				case h.logUpdateChan <- inst:
				default:
				}
				break
			}
		}
		h.instancesMu.RUnlock()
	}

	// Control mode pipes: event-driven, zero-subprocess status detection
	pm := tmux.NewPipeManager(h.ctx, outputCallback)

	// Window change callback: refresh window cache immediately when tabs are added/closed
	pm.SetWindowChangeCallback(func() {
		tmux.RefreshSessionCache()
	})

	tmux.SetPipeManager(pm)

	// Connect pipes for all existing running sessions in background
	go func() {
		time.Sleep(500 * time.Millisecond) // Let TUI render first
		h.instancesMu.RLock()
		instances := make([]*session.Instance, len(h.instances))
		copy(instances, h.instances)
		h.instancesMu.RUnlock()

		for _, inst := range instances {
			if ts := inst.GetTmuxSession(); ts != nil && ts.Exists() {
				if err := pm.Connect(ts.Name); err != nil {
					pipeUILog.Debug("startup_pipe_connect_failed",
						slog.String("session", ts.Name),
						slog.String("error", err.Error()))
				}
			}
		}
		pipeUILog.Debug("startup_pipes_connected", slog.Int("count", pm.ConnectedCount()))
	}()

	// Start background status worker (Priority 1C)
	go h.statusWorker()

	// Start log worker pool (Priority 2)
	h.startLogWorkers()

	// Initialize global search
	// DISABLED: Global search opens 884+ directory watchers and loads 4.4 GB of JSONL
	// content into memory, causing agent-deck to balloon to 6+ GB and get OOM-killed.
	// TODO: Fix by limiting watched dirs and enforcing balanced tier for large datasets.
	h.globalSearch = NewGlobalSearch()
	// claudeDir := session.GetClaudeConfigDir()
	// userConfig, _ := session.LoadUserConfig()
	// if userConfig != nil && userConfig.GlobalSearch.Enabled {
	// 	globalSearchIndex, err := session.NewGlobalSearchIndex(claudeDir, userConfig.GlobalSearch)
	// 	if err != nil {
	// 		uiLog.Warn("global_search_init_failed", slog.String("error", err.Error()))
	// 	} else {
	// 		h.globalSearchIndex = globalSearchIndex
	// 		h.globalSearch.SetIndex(globalSearchIndex)
	// 	}
	// }

	// Initialize MCP socket pool if enabled
	// Note: Pool initialization happens AFTER loading sessions so we can discover MCPs in use
	// Pool will be initialized in Init() after sessions are loaded

	// Initialize storage watcher for auto-reload
	// Polls SQLite metadata for external changes (CLI commands, other instances)
	// and triggers reload with state preservation
	if storage != nil {
		watcher, err := NewStorageWatcher(storage.GetDB())
		if err != nil {
			uiLog.Warn("storage_watcher_init_failed", slog.String("error", err.Error()))
		} else if watcher != nil {
			h.storageWatcher = watcher
			watcher.Start()
		}
	}

	// Hook-based status detection (Claude Code lifecycle hooks)
	userConfig, _ := session.LoadUserConfig()
	hooksEnabled := userConfig == nil || userConfig.Claude.GetHooksEnabled()
	if hooksEnabled {
		configDir := session.GetClaudeConfigDir()
		alreadyInstalled := session.CheckClaudeHooksInstalled(configDir)

		if alreadyInstalled {
			// Hooks already present: start watcher, no prompt needed
			hookWatcher, err := session.NewStatusFileWatcher(nil)
			if err != nil {
				uiLog.Warn("hook_watcher_init_failed", slog.String("error", err.Error()))
			} else {
				h.hookWatcher = hookWatcher
				go hookWatcher.Start()
			}
		} else {
			// Hooks not installed: check if user was already prompted
			prompted := false
			if db := statedb.GetGlobal(); db != nil {
				if val, err := db.GetMeta("hooks_prompted"); err == nil && val != "" {
					prompted = true
					if val == "accepted" {
						// User previously accepted but hooks got removed: re-install silently
						if _, err := session.InjectClaudeHooks(configDir); err != nil {
							uiLog.Warn("hook_reinstall_failed", slog.String("error", err.Error()))
						} else {
							uiLog.Info("claude_hooks_reinstalled", slog.String("config_dir", configDir))
						}
						hookWatcher, err := session.NewStatusFileWatcher(nil)
						if err != nil {
							uiLog.Warn("hook_watcher_init_failed", slog.String("error", err.Error()))
						} else {
							h.hookWatcher = hookWatcher
							go hookWatcher.Start()
						}
					}
					// val == "declined": user doesn't want hooks, skip
				}
			}
			if !prompted {
				h.pendingHooksPrompt = true
			}
		}
	}

	// Start system theme watcher if configured
	if session.GetTheme() == "system" {
		h.themeWatcher = NewThemeWatcher(ctx)
	}

	// Run log maintenance at startup (non-blocking)
	// This truncates large log files and removes orphaned logs based on user config
	// Also initializes lastLogMaintenance and lastLogCheck so periodic checks start from now
	h.lastLogMaintenance = time.Now()
	h.lastLogCheck = time.Now()
	go func() {
		logSettings := session.GetLogSettings()
		tmux.RunLogMaintenance(logSettings.MaxSizeMB, logSettings.MaxLines, logSettings.RemoveOrphans)
	}()

	return h
}

// SetWebMenuData configures an optional in-memory menu sink for web mode.
func (h *Home) SetWebMenuData(menuData *web.MemoryMenuData) {
	h.webMenuDataMu.Lock()
	h.webMenuData = menuData
	h.webMenuDataMu.Unlock()
	if !h.initialLoading {
		h.publishWebMenuSnapshot()
	}
}

func (h *Home) getWebMenuData() *web.MemoryMenuData {
	h.webMenuDataMu.RLock()
	defer h.webMenuDataMu.RUnlock()
	return h.webMenuData
}

// SetCostStore sets the cost store for cost tracking display.
func (h *Home) SetCostStore(store *costs.Store) {
	h.costStore = store
}

// SetCostPricer sets the pricer for cost calculations.
func (h *Home) SetCostPricer(pricer *costs.Pricer) {
	h.costPricer = pricer
}

// SetCostBudget sets the budget checker for cost limits.
func (h *Home) SetCostBudget(budget *costs.BudgetChecker) {
	h.costBudget = budget
}

// SetGroupScope limits the TUI to sessions within the given group path.
// The path is normalized: lowercased and spaces replaced with hyphens.
func (h *Home) SetGroupScope(path string) {
	h.groupScope = strings.ToLower(strings.ReplaceAll(path, " ", "-"))
}

// isInGroupScope returns true if the given path is within the active group scope.
// Returns true for all paths when no scope is set.
func (h *Home) isInGroupScope(path string) bool {
	if h.groupScope == "" {
		return true
	}
	return path == h.groupScope || strings.HasPrefix(path, h.groupScope+"/")
}

// scopedGroupPaths returns group paths filtered to the active scope.
// Returns all paths when no scope is set.
func (h *Home) scopedGroupPaths() []string {
	allPaths := h.groupTree.GetGroupPaths()
	if h.groupScope == "" {
		return allPaths
	}
	var scoped []string
	for _, p := range allPaths {
		if h.isInGroupScope(p) {
			scoped = append(scoped, p)
		}
	}
	return scoped
}

// groupScopeDisplayName returns the human-readable name for the active group scope.
// Falls back to the raw scope path if the group is not found in the tree.
func (h *Home) groupScopeDisplayName() string {
	if h.groupScope == "" {
		return ""
	}
	if group, exists := h.groupTree.Groups[h.groupScope]; exists {
		return group.Name
	}
	return h.groupScope
}

// refreshCostTotals updates cached cost totals from the store.
// Throttled to run at most every 10 seconds.
func (h *Home) refreshCostTotals() {
	if h.costStore == nil {
		return
	}
	if time.Since(h.costRefreshTime) < 10*time.Second {
		return
	}
	h.costRefreshTime = time.Now()
	today, _ := h.costStore.TotalToday()
	week, _ := h.costStore.TotalThisWeek()
	h.costToday.Store(today.TotalCostMicrodollars)
	h.costWeek.Store(week.TotalCostMicrodollars)
}

func (h *Home) publishWebMenuSnapshot() {
	menuData := h.getWebMenuData()
	if menuData == nil || h.groupTree == nil {
		return
	}

	h.instancesMu.RLock()
	instancesCopy := make([]*session.Instance, len(h.instances))
	copy(instancesCopy, h.instances)
	h.instancesMu.RUnlock()

	groupTreeCopy := h.groupTree.ShallowCopyForSave()
	groupsData := make([]*session.GroupData, 0, len(groupTreeCopy.GroupList))
	for _, g := range groupTreeCopy.GroupList {
		if g == nil {
			continue
		}
		groupsData = append(groupsData, &session.GroupData{
			Name:        g.Name,
			Path:        g.Path,
			Expanded:    g.Expanded,
			Order:       g.Order,
			DefaultPath: g.DefaultPath,
		})
	}

	menuData.SetSnapshot(web.BuildMenuSnapshot(h.profile, instancesCopy, groupsData, time.Now()))
}

func (h *Home) publishWebSessionStates(instances []*session.Instance) {
	menuData := h.getWebMenuData()
	if menuData == nil || len(instances) == 0 {
		return
	}

	states := make(map[string]web.MenuSessionState, len(instances))
	for _, inst := range instances {
		if inst == nil {
			continue
		}
		states[inst.ID] = web.MenuSessionState{
			Status: inst.GetStatusThreadSafe(),
			Tool:   inst.GetToolThreadSafe(),
		}
	}
	menuData.UpdateSessionStates(states, time.Now())
}

// preserveState captures current UI state before reload
func (h *Home) preserveState() reloadState {
	state := reloadState{
		expandedGroups: make(map[string]bool),
		viewOffset:     h.viewOffset,
	}

	// Capture cursor position (session ID or group path)
	if h.cursor < len(h.flatItems) {
		item := h.flatItems[h.cursor]
		switch item.Type {
		case session.ItemTypeSession:
			if item.Session != nil {
				state.cursorSessionID = item.Session.ID
			}
		case session.ItemTypeGroup:
			state.cursorGroupPath = item.Path
		}
	}

	// Capture expanded groups
	if h.groupTree != nil {
		for _, group := range h.groupTree.GroupList {
			if group.Expanded {
				state.expandedGroups[group.Path] = true
			}
		}
	}

	return state
}

// restoreState applies preserved UI state after reload
func (h *Home) restoreState(state reloadState) {
	// Restore expanded groups (only for groups present in the map;
	// new groups keep their default expanded state from storage)
	if h.groupTree != nil {
		for _, group := range h.groupTree.GroupList {
			if expanded, exists := state.expandedGroups[group.Path]; exists {
				group.Expanded = expanded
			}
		}
	}

	// Rebuild flat items with restored group states
	h.rebuildFlatItems()

	// Restore cursor position
	found := false

	// First, try to restore cursor to session if we had one selected
	if state.cursorSessionID != "" {
		for i, item := range h.flatItems {
			if item.Type == session.ItemTypeSession &&
				item.Session != nil &&
				item.Session.ID == state.cursorSessionID {
				h.cursor = i
				found = true
				break
			}
		}
	}

	// If session not found, try to restore cursor to group if we had one selected
	if !found && state.cursorGroupPath != "" {
		for i, item := range h.flatItems {
			if item.Type == session.ItemTypeGroup && item.Path == state.cursorGroupPath {
				h.cursor = i
				found = true
				break
			}
		}
	}

	// Fallback: clamp cursor to valid range if target not found or cursor out of bounds
	if !found || h.cursor >= len(h.flatItems) {
		if len(h.flatItems) > 0 {
			h.cursor = min(h.cursor, len(h.flatItems)-1)
			h.cursor = max(h.cursor, 0)
		} else {
			h.cursor = 0
		}
	}

	// Restore scroll position (clamped to valid range)
	if len(h.flatItems) > 0 {
		h.viewOffset = min(state.viewOffset, len(h.flatItems)-1)
		h.viewOffset = max(h.viewOffset, 0)
	} else {
		h.viewOffset = 0
	}
}

// sessionHasWindows returns true if the session item has 2+ cached tmux windows.
func (h *Home) sessionHasWindows(item session.Item) bool {
	if item.Session == nil {
		return false
	}
	tmuxSess := item.Session.GetTmuxSession()
	if tmuxSess == nil {
		return false
	}
	return len(tmux.GetCachedWindows(tmuxSess.Name)) >= 2
}

// moveCursorToSession moves the cursor to the flat item matching the given session ID.
func (h *Home) moveCursorToSession(sessionID string) {
	for i, fi := range h.flatItems {
		if fi.Type == session.ItemTypeSession && fi.Session != nil && fi.Session.ID == sessionID {
			h.cursor = i
			return
		}
	}
}

// moveCursorToGroup moves the cursor to the flat item matching the given group path.
func (h *Home) moveCursorToGroup(path string) {
	for i, fi := range h.flatItems {
		if fi.Type == session.ItemTypeGroup && fi.Path == path {
			h.cursor = i
			return
		}
	}
}

func (h *Home) captureSelectedItemIdentity() selectedItemIdentity {
	if h.cursor < 0 || h.cursor >= len(h.flatItems) {
		return selectedItemIdentity{windowIndex: -1}
	}

	item := h.flatItems[h.cursor]
	identity := selectedItemIdentity{windowIndex: -1}
	switch item.Type {
	case session.ItemTypeGroup:
		identity.groupPath = item.Path
	case session.ItemTypeSession:
		if item.Session != nil {
			identity.sessionID = item.Session.ID
		}
	case session.ItemTypeWindow:
		identity.windowSessionID = item.WindowSessionID
		identity.windowIndex = item.WindowIndex
	}
	return identity
}

func (h *Home) restoreSelectedItemIdentity(identity selectedItemIdentity) bool {
	for i, item := range h.flatItems {
		switch {
		case identity.windowSessionID != "" && item.Type == session.ItemTypeWindow && item.WindowSessionID == identity.windowSessionID && item.WindowIndex == identity.windowIndex:
			h.cursor = i
			return true
		case identity.sessionID != "" && item.Type == session.ItemTypeSession && item.Session != nil && item.Session.ID == identity.sessionID:
			h.cursor = i
			return true
		case identity.groupPath != "" && item.Type == session.ItemTypeGroup && item.Path == identity.groupPath:
			h.cursor = i
			return true
		}
	}

	if identity.windowSessionID != "" {
		for i, item := range h.flatItems {
			if item.Type == session.ItemTypeSession && item.Session != nil && item.Session.ID == identity.windowSessionID {
				h.cursor = i
				return true
			}
		}
	}

	return false
}

func (h *Home) rebuildFlatItemsPreservingSelection(identity selectedItemIdentity) {
	h.rebuildFlatItems()
	if !h.restoreSelectedItemIdentity(identity) && len(h.flatItems) > 0 {
		h.cursor = min(h.cursor, len(h.flatItems)-1)
		h.cursor = max(h.cursor, 0)
	}
	h.syncViewport()
}

// rebuildFlatItems rebuilds the flattened view from group tree
func (h *Home) rebuildFlatItems() {
	h.jumpMode = false
	h.jumpBuffer = ""

	allItems := h.groupTree.Flatten()

	// Apply status filter if active
	if h.statusFilter != "" {
		// First pass: identify groups that have matching sessions
		groupsWithMatches := make(map[string]bool)
		for _, item := range allItems {
			if item.Type == session.ItemTypeSession && item.Session != nil {
				if matchesStatusFilter(h.statusFilter, item.Session.Status) {
					// Mark this session's group and all parent groups as having matches
					groupsWithMatches[item.Path] = true
					// Also mark parent paths
					parts := strings.Split(item.Path, "/")
					for i := range parts {
						parentPath := strings.Join(parts[:i+1], "/")
						groupsWithMatches[parentPath] = true
					}
				}
			}
		}

		// Second pass: filter items
		filtered := make([]session.Item, 0, len(allItems))
		for _, item := range allItems {
			if item.Type == session.ItemTypeGroup {
				// Keep group if it has matching sessions
				if groupsWithMatches[item.Path] {
					filtered = append(filtered, item)
				}
			} else if item.Type == session.ItemTypeSession && item.Session != nil {
				// Keep session if it matches the filter
				if matchesStatusFilter(h.statusFilter, item.Session.Status) {
					filtered = append(filtered, item)
				}
			}
		}
		// Auto-clear filter if it matches nothing but sessions exist
		if len(filtered) == 0 && len(allItems) > 0 {
			h.statusFilter = ""
			h.flatItems = allItems
		} else {
			h.flatItems = filtered
		}
	} else {
		h.flatItems = allItems
	}

	// Apply group scope filter (composes with status filter above)
	if h.groupScope != "" {
		scoped := make([]session.Item, 0, len(h.flatItems))
		for _, item := range h.flatItems {
			if h.isInGroupScope(item.Path) {
				scoped = append(scoped, item)
			}
		}
		h.flatItems = scoped
	}

	// Inject window items after sessions that have 2+ windows
	if len(h.flatItems) > 0 {
		expanded := make([]session.Item, 0, len(h.flatItems)+8)
		for _, item := range h.flatItems {
			expanded = append(expanded, item)

			if item.Type != session.ItemTypeSession || item.Session == nil {
				continue
			}
			tmuxSess := item.Session.GetTmuxSession()
			if tmuxSess == nil {
				continue
			}
			wins := tmux.GetCachedWindows(tmuxSess.Name)
			if len(wins) < 2 || h.windowsCollapsed[item.Session.ID] {
				continue
			}

			for winIdx, win := range wins {
				expanded = append(expanded, session.Item{
					Type:                session.ItemTypeWindow,
					WindowIndex:         win.Index,
					WindowName:          win.Name,
					WindowTool:          win.Tool,
					WindowSessionID:     item.Session.ID,
					Level:               item.Level + 1,
					Path:                item.Path,
					IsWindow:            true,
					IsLastWindow:        winIdx == len(wins)-1,
					IsLastInGroup:       item.IsLastInGroup && winIdx == len(wins)-1,
					ParentIsLastInGroup: item.IsLastInGroup,
				})
			}
		}
		h.flatItems = expanded
	}

	// Append remote sessions as selectable items
	h.remoteSessionsMu.RLock()
	remoteNames := make([]string, 0, len(h.remoteSessions))
	remotes := make(map[string][]session.RemoteSessionInfo, len(h.remoteSessions))
	for name, sessions := range h.remoteSessions {
		remoteNames = append(remoteNames, name)
		remotes[name] = append([]session.RemoteSessionInfo(nil), sessions...)
	}
	h.remoteSessionsMu.RUnlock()
	sort.Strings(remoteNames)
	if len(remotes) > 0 {
		for _, remoteName := range remoteNames {
			sessions := remotes[remoteName]
			// Add remote group header
			h.flatItems = append(h.flatItems, session.Item{
				Type:       session.ItemTypeRemoteGroup,
				RemoteName: remoteName,
				Path:       "remotes/" + remoteName,
				Level:      0,
			})
			// Add remote sessions
			for i := range sessions {
				h.flatItems = append(h.flatItems, session.Item{
					Type:          session.ItemTypeRemoteSession,
					RemoteSession: &sessions[i],
					RemoteName:    remoteName,
					Path:          "remotes/" + remoteName,
					Level:         1,
					IsLastInGroup: i == len(sessions)-1,
				})
			}
		}
	}

	// Pre-compute root group numbers for O(1) hotkey lookup (replaces O(n) loop in renderGroupItem)
	rootNum := 0
	for i := range h.flatItems {
		if h.flatItems[i].Type == session.ItemTypeGroup && h.flatItems[i].Level == 0 {
			rootNum++
			h.flatItems[i].RootGroupNum = rootNum
		}
	}

	// Invalidate mouse double-click tracking (item indices may have shifted)
	h.lastClickIndex = -1

	// Inject creating session placeholders (worktree creation in progress)
	for _, creating := range h.creatingSessions {
		item := session.Item{
			Type:          session.ItemTypeSession,
			Level:         1,
			Path:          creating.GroupPath,
			CreatingID:    creating.ID,
			CreatingTitle: creating.Title,
			CreatingTool:  creating.Tool,
		}
		// Insert at the appropriate group position
		inserted := false
		if creating.GroupPath != "" {
			for i := len(h.flatItems) - 1; i >= 0; i-- {
				fi := h.flatItems[i]
				if fi.Type == session.ItemTypeGroup && fi.Path == creating.GroupPath {
					// Insert after the group header
					h.flatItems = append(h.flatItems[:i+1], append([]session.Item{item}, h.flatItems[i+1:]...)...)
					inserted = true
					break
				}
				if fi.Path == creating.GroupPath && (fi.Type == session.ItemTypeSession || fi.CreatingID != "") {
					// Insert after the last session in this group
					h.flatItems = append(h.flatItems[:i+1], append([]session.Item{item}, h.flatItems[i+1:]...)...)
					inserted = true
					break
				}
			}
		}
		if !inserted {
			// No group found or no group — append at end (before remotes)
			h.flatItems = append(h.flatItems, item)
		}
	}

	// Ensure cursor is valid
	if h.cursor >= len(h.flatItems) {
		h.cursor = len(h.flatItems) - 1
	}
	if h.cursor < 0 {
		h.cursor = 0
	}
	// Adjust viewport if cursor is out of view
	h.syncViewport()

	// Publish an updated web snapshot when menu structure/session list changes.
	h.publishWebMenuSnapshot()
}

// syncViewport ensures the cursor is visible within the viewport
// Call this after any cursor movement
func (h *Home) syncViewport() {
	if len(h.flatItems) == 0 {
		h.viewOffset = 0
		return
	}

	// Calculate visible height for session list
	// MUST match the calculation in View() exactly!
	//
	// Layout breakdown:
	// - Header: 1 line
	// - Filter bar: 1 line (always shown)
	// - Update banner: 0 or 1 line (when update available)
	// - Maintenance banner: 0 or 1 line (when maintenance completed)
	// - Main content: contentHeight lines
	// - Help bar: 2 lines (border + content)
	// Panel title within content: 2 lines (title + underline)
	// Panel content: contentHeight - 2 lines
	helpBarHeight := 2
	panelTitleLines := 2 // SESSIONS title + underline (matches View())

	// Filter bar is always shown for consistent layout (matches View())
	filterBarHeight := 1
	updateBannerHeight := 0
	if h.updateInfo != nil && h.updateInfo.Available {
		updateBannerHeight = 1
	}
	maintenanceBannerHeight := 0
	if h.maintenanceMsg != "" {
		maintenanceBannerHeight = 1
	}

	// contentHeight = total height for main content area
	// -1 for header line, -helpBarHeight for help bar, -updateBannerHeight, -maintenanceBannerHeight, -filterBarHeight
	contentHeight := h.height - 1 - helpBarHeight - updateBannerHeight - maintenanceBannerHeight - filterBarHeight

	// CRITICAL: Calculate panelContentHeight based on current layout mode
	// This MUST match the calculations in renderStackedLayout/renderDualColumnLayout/renderSingleColumnLayout
	var panelContentHeight int
	layoutMode := h.getLayoutMode()
	switch layoutMode {
	case LayoutModeStacked:
		// Stacked layout: list gets 60% of height, minus title (2 lines)
		// Must match: listHeight := (totalHeight * 60) / 100; listContent height = listHeight - 2
		listHeight := (contentHeight * 60) / 100
		if listHeight < 5 {
			listHeight = 5
		}
		panelContentHeight = listHeight - panelTitleLines
	case LayoutModeSingle:
		// Single column: list gets full height minus title
		// Must match: listHeight := totalHeight - 2
		panelContentHeight = contentHeight - panelTitleLines
	default: // LayoutModeDual
		// Dual layout: list panel gets full contentHeight minus title
		panelContentHeight = contentHeight - panelTitleLines
	}

	// maxVisible = how many items can be shown (reserving 1 for "more below" indicator)
	maxVisible := panelContentHeight - 1
	if maxVisible < 1 {
		maxVisible = 1
	}

	// Account for "more above" indicator (takes 1 line when scrolled down)
	// This is the key fix: when we're scrolled down, we have 1 less visible line
	effectiveMaxVisible := maxVisible
	if h.viewOffset > 0 {
		effectiveMaxVisible-- // "more above" indicator takes 1 line
	}
	if effectiveMaxVisible < 1 {
		effectiveMaxVisible = 1
	}

	// If cursor is above viewport, scroll up
	if h.cursor < h.viewOffset {
		h.viewOffset = h.cursor
	}

	// If cursor is below viewport, scroll down
	if h.cursor >= h.viewOffset+effectiveMaxVisible {
		// When scrolling down, we need to account for the "more above" indicator
		// that will appear once viewOffset > 0
		if h.viewOffset == 0 {
			// First scroll down: "more above" will appear, reducing visible by 1
			h.viewOffset = h.cursor - (maxVisible - 1) + 1
		} else {
			// Already scrolled: "more above" already showing
			h.viewOffset = h.cursor - effectiveMaxVisible + 1
		}
	}

	// Clamp viewOffset to valid range
	// When scrolled down, "more above" takes 1 line, so we can show fewer items
	finalMaxVisible := maxVisible
	if h.viewOffset > 0 {
		finalMaxVisible--
	}
	maxOffset := len(h.flatItems) - finalMaxVisible
	if maxOffset < 0 {
		maxOffset = 0
	}
	if h.viewOffset > maxOffset {
		h.viewOffset = maxOffset
	}
	if h.viewOffset < 0 {
		h.viewOffset = 0
	}
}

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
			if ts := inst.GetTmuxSession(); ts != nil && ts.Name == sessName {
				return inst.ID
			}
		}
	}
	return ""
}

// NOTE: updateTmuxNotifications (foreground) was removed in v0.9.2 as a CPU optimization.
// Status bar updates and key binding updates are handled by syncNotificationsBackground().

// cleanupNotifications removes all notification bar state on exit
func (h *Home) cleanupNotifications() {
	// Always unbind status-right mouse click (bound unconditionally at init)
	tmux.UnbindMouseStatusClicks()

	if !h.manageTmuxNotifications || !h.notificationsEnabled || h.notificationManager == nil {
		return
	}

	// Clear global status bar (ONE call instead of per-session)
	_ = tmux.ClearStatusLeftGlobal()

	// Unbind all keys (with mutex protection)
	h.boundKeysMu.Lock()
	for key := range h.boundKeys {
		_ = tmux.UnbindKey(key)
	}
	h.boundKeys = make(map[string]string)
	h.boundKeysMu.Unlock()
}

// getVisibleHeight returns the number of visible items in the session list
// Used for vi-style pagination (Ctrl+u/d/f/b)
func (h *Home) getVisibleHeight() int {
	helpBarHeight := 2
	panelTitleLines := 2
	filterBarHeight := 1
	updateBannerHeight := 0
	if h.updateInfo != nil && h.updateInfo.Available {
		updateBannerHeight = 1
	}
	maintenanceBannerHeight := 0
	if h.maintenanceMsg != "" {
		maintenanceBannerHeight = 1
	}

	contentHeight := h.height - 1 - helpBarHeight - updateBannerHeight - maintenanceBannerHeight - filterBarHeight

	var panelContentHeight int
	layoutMode := h.getLayoutMode()
	switch layoutMode {
	case LayoutModeStacked:
		listHeight := (contentHeight * 60) / 100
		if listHeight < 5 {
			listHeight = 5
		}
		panelContentHeight = listHeight - panelTitleLines
	case LayoutModeSingle:
		panelContentHeight = contentHeight - panelTitleLines
	default: // LayoutModeDual
		panelContentHeight = contentHeight - panelTitleLines
	}

	maxVisible := panelContentHeight - 1
	if maxVisible < 1 {
		maxVisible = 1
	}
	return maxVisible
}

// jumpToRootGroup jumps the cursor to the Nth root-level group (1-indexed)
// Root groups are those at Level 0 (no "/" in path)
func (h *Home) jumpToRootGroup(n int) {
	if n < 1 || n > 9 {
		return
	}

	// Find the Nth root group in flatItems
	rootGroupCount := 0
	for i, item := range h.flatItems {
		if item.Type == session.ItemTypeGroup && item.Level == 0 {
			rootGroupCount++
			if rootGroupCount == n {
				h.cursor = i
				h.syncViewport()
				return
			}
		}
	}
	// If n exceeds available root groups, do nothing (no-op)
}

// Init initializes the model
func (h *Home) Init() tea.Cmd {
	// Check for first run (no config.toml exists)
	configPath, _ := session.GetUserConfigPath()
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		h.setupWizard.Show()
		h.setupWizard.SetSize(h.width, h.height)
	}

	// Start system stats collection
	if h.sysStatsCollector != nil {
		h.sysStatsCollector.Start()
	}

	cmds := []tea.Cmd{
		h.loadSessions,

		h.tick(),
		h.reviverTick(),
		h.checkForUpdate(),
		h.fetchRemoteSessions,
	}

	// Start listening for storage changes
	if h.storageWatcher != nil {
		cmds = append(cmds, listenForReloads(h.storageWatcher))
	}

	// Start listening for OS theme changes
	if h.themeWatcher != nil {
		cmds = append(cmds, listenForThemeChange(h.themeWatcher))
	}

	// Start watcher engine (D-07: lifecycle tied to TUI startup)
	cmds = append(cmds, h.startWatcherEngine())

	return tea.Batch(cmds...)
}

// checkForUpdate checks for updates asynchronously
func (h *Home) checkForUpdate() tea.Cmd {
	return func() tea.Msg {
		info, _ := update.CheckForUpdate(Version, false)
		return updateCheckMsg{info: info}
	}
}

// listenForReloads waits for storage change notification
func listenForReloads(sw *StorageWatcher) tea.Cmd {
	return func() tea.Msg {
		if sw == nil {
			return nil
		}
		<-sw.ReloadChannel()
		return storageChangedMsg{}
	}
}

// listenForThemeChange waits for the next OS theme change.
// MUST be re-issued in the Update handler for systemThemeMsg to keep listening.
func listenForThemeChange(tw *ThemeWatcher) tea.Cmd {
	if tw == nil {
		return nil
	}
	return func() tea.Msg {
		isDark, ok := <-tw.ChangeChannel()
		if !ok {
			return nil
		}
		return systemThemeMsg{dark: isDark}
	}
}

// listenForWatcherEvent waits for the next event from the engine's EventCh.
// Must be re-issued after each event to keep listening (Bubble Tea cmd pattern).
func listenForWatcherEvent(ch <-chan watcher.Event) tea.Cmd {
	return func() tea.Msg {
		evt, ok := <-ch
		if !ok {
			return nil
		}
		return watcherEventMsg{event: evt}
	}
}

// listenForWatcherHealth waits for the next health state from the engine's HealthCh.
// Must be re-issued after each state to keep listening.
func listenForWatcherHealth(ch <-chan watcher.HealthState) tea.Cmd {
	return func() tea.Msg {
		state, ok := <-ch
		if !ok {
			return nil
		}
		return watcherHealthMsg{state: state}
	}
}

func (h *Home) stopThemeWatcher() {
	if h.themeWatcher != nil {
		h.themeWatcher.Close()
		h.themeWatcher = nil
	}
}

func (h *Home) startThemeWatcher() tea.Cmd {
	h.stopThemeWatcher()
	h.themeWatcher = NewThemeWatcher(h.ctx)
	if h.themeWatcher == nil {
		return nil
	}
	return listenForThemeChange(h.themeWatcher)
}

// startWatcherEngine initialises and starts the watcher engine from statedb state.
// Watchers marked status="running" are registered as adapters before Start() is called.
// Returns a tea.Batch of channel listener commands, or nil if no watchers exist.
func (h *Home) startWatcherEngine() tea.Cmd {
	db := statedb.GetGlobal()
	if db == nil {
		return nil
	}

	rows, err := db.LoadWatchers()
	if err != nil || len(rows) == 0 {
		return nil
	}

	// Load user config for watcher settings.
	cfg, _ := session.LoadUserConfig()
	var watcherCfg session.WatcherSettings
	if cfg != nil {
		watcherCfg = cfg.Watcher
	}

	// Build engine config.
	router, _ := watcher.LoadFromWatcherDir() // nil on error (no clients.json yet)
	healthInterval := time.Duration(watcherCfg.GetHealthCheckIntervalSeconds()) * time.Second

	engineCfg := watcher.EngineConfig{
		DB:                  db,
		Router:              router,
		MaxEventsPerWatcher: watcherCfg.GetMaxEventsPerWatcher(),
		HealthCheckInterval: healthInterval,
	}
	eng := watcher.NewEngine(engineCfg)

	maxSilenceMinutes := watcherCfg.GetMaxSilenceMinutes()

	// Register all running watchers as adapters.
	for _, row := range rows {
		if row.Status != "running" {
			continue
		}
		var adapter watcher.WatcherAdapter
		switch row.Type {
		case "webhook":
			adapter = &watcher.WebhookAdapter{}
		case "ntfy":
			adapter = &watcher.NtfyAdapter{}
		case "slack":
			adapter = &watcher.SlackAdapter{}
		case "github":
			adapter = &watcher.GitHubAdapter{}
		default:
			continue
		}

		adapterCfg := watcher.AdapterConfig{
			Type:     row.Type,
			Name:     row.Name,
			Settings: map[string]string{},
		}
		eng.RegisterAdapter(row.ID, adapter, adapterCfg, maxSilenceMinutes)
	}

	if err := eng.Start(); err != nil {
		uiLog.Warn("watcher_engine_start_failed", "error", err.Error())
		return nil
	}

	h.watcherEngine = eng

	return tea.Batch(
		listenForWatcherEvent(eng.EventCh()),
		listenForWatcherHealth(eng.HealthCh()),
	)
}

// propagateThemeToSessions updates COLORFGBG in all running tmux sessions
// so that terminal-aware tools pick up the new light/dark setting.
func (h *Home) propagateThemeToSessions() {
	colorfgbg := session.ThemeColorFGBG()
	if colorfgbg == "" {
		return
	}
	h.instancesMu.RLock()
	instances := make([]*session.Instance, len(h.instances))
	copy(instances, h.instances)
	h.instancesMu.RUnlock()

	go func() {
		for _, inst := range instances {
			if tmuxSess := inst.GetTmuxSession(); tmuxSess != nil && tmuxSess.Exists() {
				_ = tmuxSess.SetEnvironment("COLORFGBG", colorfgbg)
				_ = tmuxSess.ApplyThemeOptions()
			}
		}
	}()
}

// fetchRemoteSessions fetches sessions from all configured remotes.
func (h *Home) fetchRemoteSessions() tea.Msg {
	config, err := session.LoadUserConfig()
	if err != nil || config == nil || len(config.Remotes) == 0 {
		return remoteSessionsFetchedMsg{sessions: nil}
	}

	results := make(map[string][]session.RemoteSessionInfo)
	ctx, cancel := context.WithTimeout(h.ctx, 15*time.Second)
	defer cancel()

	for name, rc := range config.Remotes {
		runner := session.NewSSHRunner(name, rc)
		sessions, err := runner.FetchSessions(ctx)
		if err != nil {
			continue
		}
		for i := range sessions {
			sessions[i].RemoteName = name
		}
		results[name] = sessions
	}

	return remoteSessionsFetchedMsg{sessions: results}
}

// loadSessions loads sessions from storage and initializes the pool
func (h *Home) loadSessions() tea.Msg {
	if h.storage == nil {
		return loadSessionsMsg{instances: []*session.Instance{}, err: fmt.Errorf("storage not initialized")}
	}

	// Capture file mtime BEFORE loading to detect external changes later
	loadMtime, _ := h.storage.GetFileMtime()

	instances, groups, err := h.storage.LoadWithGroups()
	msg := loadSessionsMsg{instances: instances, groups: groups, err: err, loadMtime: loadMtime}

	// Initialize pool AFTER sessions are loaded
	userConfig, configErr := session.LoadUserConfig()
	if configErr == nil && userConfig != nil && userConfig.MCPPool.Enabled {
		pool, poolErr := session.InitializeGlobalPool(h.ctx, userConfig, instances)
		if poolErr != nil {
			mcpUILog.Warn("pool_init_failed", slog.String("error", poolErr.Error()))
			msg.poolError = poolErr
		} else if pool != nil {
			proxies := pool.ListServers()
			mcpUILog.Info("pool_initialized", slog.Int("proxies", len(proxies)))
			msg.poolProxies = len(proxies)
		}
	}

	return msg
}

// tick returns a command that sends a tick message at regular intervals
// Status updates use time-based cooldown to prevent flickering
func (h *Home) tick() tea.Cmd {
	return tea.Tick(tickInterval, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// reviverTick fires every 60s to sweep the session list for instances whose
// tmux server survived an SSH scope cleanup but whose control pipe got
// reaped. See .planning/v178-ssh-reviver/PLAN.md (REPORT-D).
func (h *Home) reviverTick() tea.Cmd {
	return tea.Tick(60*time.Second, func(_ time.Time) tea.Msg {
		return reviverTickMsg{}
	})
}

// invalidatePreviewCache removes a session's preview from the cache
// Called when session is deleted, renamed, or moved to ensure stale data is not displayed
func (h *Home) invalidatePreviewCache(sessionID string) {
	h.previewCacheMu.Lock()
	delete(h.previewCache, sessionID)
	delete(h.previewCacheTime, sessionID)
	h.previewCacheMu.Unlock()
}

// pruneAnalyticsCache removes stale entries from analytics and log activity caches.
// Called periodically from the tick handler to prevent unbounded map growth.
func (h *Home) pruneAnalyticsCache() {
	const maxAge = 10 * time.Minute
	now := time.Now()

	h.analyticsCacheMu.Lock()
	for id, t := range h.analyticsCacheTime {
		if now.Sub(t) > maxAge {
			delete(h.analyticsCache, id)
			delete(h.geminiAnalyticsCache, id)
			delete(h.analyticsCacheTime, id)
		}
	}
	h.analyticsCacheMu.Unlock()

	h.logActivityMu.Lock()
	for id, t := range h.lastLogActivity {
		if now.Sub(t) > maxAge {
			delete(h.lastLogActivity, id)
		}
	}
	h.logActivityMu.Unlock()

	// Prune MCP info cache (entries older than 10 minutes)
	session.PruneMCPCache(maxAge)
}

// setError sets an error with timestamp for auto-dismiss
func (h *Home) setError(err error) {
	h.err = err
	if err != nil {
		h.errTime = time.Now()
	}
}

// clearError clears the current error
func (h *Home) clearError() {
	h.err = nil
	h.errTime = time.Time{}
}

// cleanupExpiredAnimations removes expired entries from an animation map
// Returns list of IDs that were removed (for logging/debugging if needed)
func (h *Home) cleanupExpiredAnimations(
	animMap map[string]time.Time,
	claudeTimeout, defaultTimeout time.Duration,
) []string {
	var toDelete []string
	for sessionID, startTime := range animMap {
		inst := h.instanceByID[sessionID]
		if inst == nil {
			// Session was deleted, clean up
			toDelete = append(toDelete, sessionID)
			continue
		}
		// Use appropriate timeout based on tool
		// Claude and Gemini use longer timeout (MCP loading can be slow)
		timeout := defaultTimeout
		if session.IsClaudeCompatible(inst.Tool) || inst.Tool == "gemini" {
			timeout = claudeTimeout
		}
		if time.Since(startTime) > timeout {
			toDelete = append(toDelete, sessionID)
		}
	}
	for _, id := range toDelete {
		delete(animMap, id)
	}
	return toDelete
}

func launchAnimationMinDuration(tool string) time.Duration {
	if session.IsClaudeCompatible(tool) || tool == "gemini" {
		return minLaunchAnimationDurationClaude
	}
	return minLaunchAnimationDurationDefault
}

// hasActiveAnimation checks if a session has an animation currently being displayed
// Returns true only if the animation is actually showing (not just tracked in the map)
// This MUST match the display logic in renderPreviewPane exactly
func (h *Home) hasActiveAnimation(sessionID string) bool {
	inst := h.instanceByID[sessionID]
	if inst == nil {
		return false
	}

	// Check forking first (always shows while tracked)
	if _, ok := h.forkingSessions[sessionID]; ok {
		return true
	}

	// Determine animation start time and type
	var startTime time.Time
	var hasAnimation bool

	if t, ok := h.launchingSessions[sessionID]; ok {
		startTime = t
		hasAnimation = true
	} else if t, ok := h.resumingSessions[sessionID]; ok {
		startTime = t
		hasAnimation = true
	} else if t, ok := h.mcpLoadingSessions[sessionID]; ok {
		startTime = t
		hasAnimation = true
	}

	if !hasAnimation {
		return false
	}

	// STATUS-BASED ANIMATION: Show animation until session is ready
	// Instead of hardcoded 6-second minimum, use actual session status
	// Status is updated via background polling (2s interval)
	timeSinceStart := time.Since(startTime)

	// Brief minimum to prevent flicker during rapid status changes
	if timeSinceStart < launchAnimationMinDuration(inst.GetToolThreadSafe()) {
		return true
	}

	// Maximum animation time (15s) as safety fallback
	if timeSinceStart >= 15*time.Second {
		return false
	}

	// STATUS-BASED CHECK: Session is ready when status is Running or Waiting
	// - StatusRunning (GREEN): Claude is actively processing
	// - StatusWaiting (YELLOW): Claude is at prompt, waiting for input
	// - StatusIdle (GRAY): Claude has stopped and user acknowledged
	animStatus := inst.GetStatusThreadSafe()
	animTool := inst.GetToolThreadSafe()
	if animStatus == session.StatusRunning ||
		animStatus == session.StatusWaiting ||
		animStatus == session.StatusIdle {
		// Session is ready - stop animation immediately
		return false
	}

	// CONTENT-BASED CHECK: Also check preview content for faster detection
	// This catches cases where status hasn't updated yet but content is visible
	h.previewCacheMu.RLock()
	previewContent := h.previewCache[sessionID]
	h.previewCacheMu.RUnlock()

	// Strip ANSI for reliable pattern matching (preview cache now contains ANSI-rich content)
	plainPreview := ansi.Strip(previewContent)

	if animTool == "claude" || animTool == "gemini" {
		// Claude ready indicators
		agentReady := strings.Contains(plainPreview, "ctrl+c to interrupt") ||
			strings.Contains(plainPreview, "No, and tell Claude what to do differently") ||
			strings.Contains(plainPreview, "\n> ") ||
			strings.Contains(plainPreview, "> \n") ||
			strings.Contains(plainPreview, "esc to interrupt") ||
			strings.Contains(plainPreview, "⠋") || strings.Contains(plainPreview, "⠙") ||
			strings.Contains(plainPreview, "Thinking") ||
			strings.Contains(plainPreview, "╭─") // Claude UI border

		// Gemini prompts
		if animTool == "gemini" {
			agentReady = agentReady ||
				strings.Contains(plainPreview, "▸") ||
				strings.Contains(plainPreview, "gemini>")
		}

		if agentReady {
			return false
		}
	} else {
		// Non-Claude/Gemini: ready if any substantial content (>50 chars)
		if len(strings.TrimSpace(plainPreview)) > 50 {
			return false
		}
	}

	// Not ready yet - keep showing animation
	return true
}

// previewCacheKey returns the cache key for a preview: sessionID or sessionID:windowIndex.
func previewCacheKey(sessionID string, windowIndex int) string {
	if windowIndex < 0 {
		return sessionID
	}
	return fmt.Sprintf("%s:%d", sessionID, windowIndex)
}

func remotePreviewCacheKey(remoteName, sessionID string) string {
	return fmt.Sprintf("remote:%s:%s", remoteName, sessionID)
}

const (
	remotePreviewMaxLines = 200
	remotePreviewMaxBytes = 16 * 1024
)

func truncateRemotePreviewContent(content string) string {
	if content == "" {
		return ""
	}

	lines := strings.Split(content, "\n")
	if len(lines) > remotePreviewMaxLines {
		lines = lines[len(lines)-remotePreviewMaxLines:]
		content = strings.Join(lines, "\n")
	}

	if len(content) <= remotePreviewMaxBytes {
		return content
	}

	trimmed := []byte(content)
	trimmed = trimmed[len(trimmed)-remotePreviewMaxBytes:]
	for len(trimmed) > 0 && !utf8.Valid(trimmed) {
		trimmed = trimmed[1:]
	}
	if idx := bytes.IndexByte(trimmed, '\n'); idx >= 0 && idx < len(trimmed)-1 {
		trimmed = trimmed[idx+1:]
	}

	return string(trimmed)
}

// fetchPreview returns a command that asynchronously fetches preview content.
// windowIndex < 0 captures the session's primary pane; >= 0 captures a specific window.
func (h *Home) fetchPreview(inst *session.Instance, key string, windowIndex int) tea.Cmd {
	if inst == nil {
		return nil
	}
	return func() tea.Msg {
		var content string
		var err error
		if windowIndex >= 0 {
			content, err = inst.PreviewWindowFull(windowIndex)
		} else {
			content, err = inst.PreviewFull()
		}
		return previewFetchedMsg{
			previewKey: key,
			content:    content,
			err:        err,
		}
	}
}

// fetchPreviewDebounced returns a command that triggers preview fetch after debounce delay.
// PERFORMANCE: Prevents rapid subprocess spawning during keyboard navigation.
func (h *Home) fetchPreviewDebounced(sessionID string, windowIndex int) tea.Cmd {
	const debounceDelay = 150 * time.Millisecond

	key := previewCacheKey(sessionID, windowIndex)
	h.previewDebounceMu.Lock()
	h.pendingPreviewKey = key
	h.previewDebounceMu.Unlock()

	return func() tea.Msg {
		time.Sleep(debounceDelay)
		return previewDebounceMsg{previewKey: key, sessionID: sessionID, windowIndex: windowIndex}
	}
}

func (h *Home) fetchRemotePreviewDebounced(remoteName, sessionID string) tea.Cmd {
	const debounceDelay = 150 * time.Millisecond

	key := remotePreviewCacheKey(remoteName, sessionID)
	h.previewDebounceMu.Lock()
	h.pendingPreviewKey = key
	h.previewDebounceMu.Unlock()

	return func() tea.Msg {
		time.Sleep(debounceDelay)
		return previewDebounceMsg{previewKey: key, sessionID: sessionID, windowIndex: -1, remoteName: remoteName}
	}
}

func (h *Home) fetchRemotePreview(remoteName, sessionID, key string) tea.Cmd {
	return func() tea.Msg {
		config, err := session.LoadUserConfig()
		if err != nil || config == nil || config.Remotes == nil {
			return previewFetchedMsg{previewKey: key, err: fmt.Errorf("failed to load remote config")}
		}

		rc, ok := config.Remotes[remoteName]
		if !ok {
			return previewFetchedMsg{previewKey: key, err: fmt.Errorf("remote '%s' not found", remoteName)}
		}

		runner := session.NewSSHRunner(remoteName, rc)
		ctx, cancel := context.WithTimeout(h.ctx, 15*time.Second)
		defer cancel()

		content, fetchErr := runner.FetchSessionOutput(ctx, sessionID)
		content = truncateRemotePreviewContent(content)
		return previewFetchedMsg{previewKey: key, content: content, err: fetchErr}
	}
}

// selectedPreviewTarget returns the instance, cache key, and window index for the currently
// selected flat item. windowIndex is -1 for session items.
func (h *Home) selectedPreviewTarget() (*session.Instance, string, int) {
	if h.cursor >= len(h.flatItems) {
		return nil, "", -1
	}
	item := h.flatItems[h.cursor]
	switch item.Type {
	case session.ItemTypeSession:
		if item.Session == nil {
			return nil, "", -1
		}
		return item.Session, item.Session.ID, -1
	case session.ItemTypeWindow:
		inst := h.getInstanceByID(item.WindowSessionID)
		if inst == nil {
			return nil, "", -1
		}
		return inst, previewCacheKey(inst.ID, item.WindowIndex), item.WindowIndex
	}
	return nil, "", -1
}

func (h *Home) selectedRemotePreviewTarget() (string, string, string, bool) {
	if h.cursor >= len(h.flatItems) {
		return "", "", "", false
	}
	item := h.flatItems[h.cursor]
	if item.Type != session.ItemTypeRemoteSession || item.RemoteSession == nil {
		return "", "", "", false
	}
	if item.RemoteName == "" || item.RemoteSession.ID == "" {
		return "", "", "", false
	}

	key := remotePreviewCacheKey(item.RemoteName, item.RemoteSession.ID)
	return item.RemoteName, item.RemoteSession.ID, key, true
}

// fetchSelectedPreview debounces a preview fetch for the currently selected item.
// Handles both session and window items transparently.
func (h *Home) fetchSelectedPreview() tea.Cmd {
	inst, _, winIdx := h.selectedPreviewTarget()
	if inst == nil {
		remoteName, remoteSessionID, _, ok := h.selectedRemotePreviewTarget()
		if !ok {
			return nil
		}
		return h.fetchRemotePreviewDebounced(remoteName, remoteSessionID)
	}
	return h.fetchPreviewDebounced(inst.ID, winIdx)
}

// detectOpenCodeSessionCmd returns a command that asynchronously detects
// the OpenCode session ID for a restored session and signals completion.
// This follows the Bubble Tea pattern of returning a tea.Cmd for async work.
func (h *Home) detectOpenCodeSessionCmd(inst *session.Instance) tea.Cmd {
	if inst == nil {
		return nil
	}

	instanceID := inst.ID

	return func() tea.Msg {
		// Run detection (this blocks until complete or timeout)
		inst.DetectOpenCodeSession()

		// Return message to trigger save
		return openCodeDetectionCompleteMsg{
			instanceID: instanceID,
			sessionID:  inst.OpenCodeSessionID,
		}
	}
}

// getAnalyticsForSession returns cached analytics if still valid (within TTL)
// Returns nil if cache miss or expired, triggering async fetch
func (h *Home) getAnalyticsForSession(inst *session.Instance) *session.SessionAnalytics {
	if inst == nil {
		return nil
	}

	// Check cache under lock (background status worker also reads this path).
	h.analyticsCacheMu.RLock()
	cached, ok := h.analyticsCache[inst.ID]
	cacheTime, hasCacheTime := h.analyticsCacheTime[inst.ID]
	h.analyticsCacheMu.RUnlock()
	if ok && hasCacheTime && time.Since(cacheTime) < analyticsCacheTTL {
		return cached
	}

	return nil // Will trigger async fetch
}

// fetchAnalytics returns a command that asynchronously parses session analytics
// This keeps View() pure (no blocking I/O) as per Bubble Tea best practices
func (h *Home) fetchAnalytics(inst *session.Instance) tea.Cmd {
	if inst == nil {
		return nil
	}
	sessionID := inst.ID

	fetchTool := inst.GetToolThreadSafe()
	switch fetchTool {
	case "claude":
		claudeSessionID := inst.ClaudeSessionID
		return func() tea.Msg {
			// Get JSONL path for this session
			jsonlPath := inst.GetJSONLPath()
			if jsonlPath == "" {
				// No JSONL path available - return empty analytics
				return analyticsFetchedMsg{
					sessionID: sessionID,
					analytics: nil,
					err:       nil,
				}
			}

			// Parse the JSONL file
			analytics, err := session.ParseSessionJSONL(jsonlPath)
			if err != nil {
				uiLog.Warn(
					"analytics_parse_failed",
					slog.String("session_id", sessionID),
					slog.String("claude_session_id", claudeSessionID),
					slog.String("error", err.Error()),
				)
				return analyticsFetchedMsg{
					sessionID: sessionID,
					analytics: nil,
					err:       err,
				}
			}

			return analyticsFetchedMsg{
				sessionID: sessionID,
				analytics: analytics,
				err:       nil,
			}
		}
	case "gemini":
		return func() tea.Msg {
			// Gemini analytics are updated via UpdateGeminiSession which is called in background
			// during UpdateStatus(). We just return the current snapshot.
			return analyticsFetchedMsg{
				sessionID:       sessionID,
				geminiAnalytics: inst.GeminiAnalytics,
				err:             nil,
			}
		}
	}

	return nil
}

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

type sessionRenderState struct {
	status    session.Status
	tool      string
	paneTitle string // Current task description from tmux pane title (stripped of spinner/done markers)
}

// cleanPaneTitle strips spinner/done marker characters from a tmux pane title
// and returns the task description. Returns "" for default/generic titles.
func cleanPaneTitle(title string) string {
	if title == "" {
		return ""
	}
	// Strip known spinner/done markers, plus any Braille chars (U+2800-28FF)
	// that Claude Code may use as spinner frames beyond the canonical set.
	cleaned := tmux.StripSpinnerRunes(title)
	cleaned = strings.TrimLeftFunc(cleaned, func(r rune) bool {
		return r >= 0x2800 && r <= 0x28FF
	})
	cleaned = strings.TrimSpace(cleaned)
	switch cleaned {
	case "", "Claude Code", "Gemini CLI", "Codex CLI":
		return ""
	}
	return cleaned
}

func (h *Home) getSessionRenderSnapshot() map[string]sessionRenderState {
	if snap := h.sessionRenderSnapshot.Load(); snap != nil {
		if typed, ok := snap.(map[string]sessionRenderState); ok {
			return typed
		}
	}
	return nil
}

func (h *Home) refreshSessionRenderSnapshot(instances []*session.Instance) {
	if instances == nil {
		h.instancesMu.RLock()
		instances = make([]*session.Instance, len(h.instances))
		copy(instances, h.instances)
		h.instancesMu.RUnlock()
	}

	snap := make(map[string]sessionRenderState, len(instances))
	for _, inst := range instances {
		if inst == nil {
			continue
		}
		state := sessionRenderState{
			status: inst.GetStatusThreadSafe(),
			tool:   inst.GetToolThreadSafe(),
		}
		// Look up pane title from the already-refreshed tmux cache.
		if tmuxSess := inst.GetTmuxSession(); tmuxSess != nil {
			if paneInfo, ok := tmux.GetCachedPaneInfo(tmuxSess.Name); ok {
				state.paneTitle = cleanPaneTitle(paneInfo.Title)
			}
		}
		snap[inst.ID] = state
	}
	h.sessionRenderSnapshot.Store(snap)
}

func (h *Home) getSessionRenderState(inst *session.Instance) sessionRenderState {
	if inst == nil {
		return sessionRenderState{}
	}
	if snap := h.getSessionRenderSnapshot(); snap != nil {
		if state, ok := snap[inst.ID]; ok {
			return state
		}
	}
	// Fallback for newly-added sessions before snapshot refresh.
	return sessionRenderState{
		status: inst.GetStatusThreadSafe(),
		tool:   inst.GetToolThreadSafe(),
	}
}

// markNavigationActivity records a short "hot" window where background workers
// should avoid heavy refresh work to keep key navigation responsive.
func (h *Home) markNavigationActivity() {
	now := time.Now()
	h.lastNavigationTime = now
	h.isNavigating = true
	h.navigationHotUntil.Store(now.Add(900 * time.Millisecond).UnixNano())
}

func (h *Home) beginAttachReturnGrace(now time.Time) {
	h.lastAttachReturn = now
	h.lastNavigationTime = now
	h.isNavigating = true
	h.navigationHotUntil.Store(now.Add(attachReturnHotDuration).UnixNano())
}

func (h *Home) shouldSuppressPreviewRefresh(now time.Time) bool {
	return !h.lastAttachReturn.IsZero() && now.Sub(h.lastAttachReturn) < attachReturnPreviewGrace
}

// getInstanceByID returns the instance with the given ID using O(1) map lookup
// Returns nil if not found. Caller must hold instancesMu if accessing from background goroutine.
func (h *Home) getInstanceByID(id string) *session.Instance {
	return h.instanceByID[id]
}

// pushUndoStack adds a deleted session to the undo stack (LIFO, capped at 10)
func (h *Home) pushUndoStack(inst *session.Instance) {
	entry := deletedSessionEntry{
		instance:  inst,
		deletedAt: time.Now(),
	}
	h.undoStack = append(h.undoStack, entry)
	if len(h.undoStack) > 10 {
		h.undoStack = h.undoStack[len(h.undoStack)-10:]
	}
}

// getDefaultPathForGroup returns the default path for a group
// Returns empty string if group not found or no default path set
func (h *Home) getDefaultPathForGroup(groupPath string) string {
	if h.groupTree == nil {
		return ""
	}
	// Only use stored path if it still exists on disk.
	// Stale paths (e.g. deleted worktrees) would cause the
	// new-session dialog to prompt for directory creation.
	p := h.groupTree.DefaultPathForGroup(groupPath)
	if p != "" {
		if _, err := os.Stat(p); err != nil {
			return ""
		}
	}
	return p
}

// statusWorker runs in a background goroutine with its own ticker
// This ensures status updates continue even when TUI is paused (tea.Exec)
func (h *Home) statusWorker() {
	defer close(h.statusWorkerDone)

	// Internal ticker - independent of Bubble Tea event loop
	// This is the key insight: when tea.Exec suspends the TUI (user attaches to session),
	// the Bubble Tea tick messages stop firing, but this goroutine keeps running
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-h.ctx.Done():
			return

		case <-ticker.C:
			// Self-triggered update - runs even when TUI is paused
			h.backgroundStatusUpdate()
			// Coalesce a queued immediate request after full sweep.
			select {
			case <-h.statusTrigger:
			default:
			}

		case req := <-h.statusTrigger:
			// Explicit trigger from TUI (for immediate updates)
			// Panic recovery to prevent worker death from killing status updates
			func() {
				defer func() {
					if r := recover(); r != nil {
						statusLog.Error("worker_panic", slog.Any("panic", r))
					}
				}()
				h.processStatusUpdate(req)
			}()
		}
	}
}

// startLogWorkers initializes the log worker pool
func (h *Home) startLogWorkers() {
	// Start 2 workers to handle log-triggered status updates concurrently
	// This is enough to handle bursts without overwhelming the system
	for i := 0; i < 2; i++ {
		h.logWorkerWg.Add(1)
		go h.logWorker()
	}
}

// logWorker processes per-session status updates triggered by PipeManager %output events
func (h *Home) logWorker() {
	defer h.logWorkerWg.Done()
	for {
		select {
		case <-h.ctx.Done():
			return
		case inst := <-h.logUpdateChan:
			if inst == nil {
				continue
			}
			// Panic recovery for worker stability
			func() {
				defer func() {
					if r := recover(); r != nil {
						uiLog.Error("log_worker_panic", slog.Any("panic", r))
					}
				}()
				_ = inst.UpdateStatus()
			}()
		}
	}
}

// backgroundStatusUpdate runs independently of the TUI
// Updates session statuses and syncs notification bar directly to tmux
// This is called by the internal ticker even when TUI is paused (tea.Exec)
func (h *Home) backgroundStatusUpdate() {
	defer func() {
		if r := recover(); r != nil {
			notifLog.Error("background_update_panic", slog.Any("panic", r))
		}
	}()

	totalStart := time.Now()
	if hotUntil := h.navigationHotUntil.Load(); hotUntil > 0 && time.Now().UnixNano() < hotUntil {
		return
	}

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

	// Get instances snapshot
	h.instancesMu.RLock()
	if len(h.instances) == 0 {
		h.instancesMu.RUnlock()
		return
	}
	instances := make([]*session.Instance, len(h.instances))
	copy(instances, h.instances)
	h.instancesMu.RUnlock()

	// PERFORMANCE: Gradually configure unconfigured sessions in background
	// Configure one session per tick to avoid blocking the status update
	// This ensures all sessions get configured within ~1 minute even without user interaction
	for _, inst := range instances {
		if tmuxSess := inst.GetTmuxSession(); tmuxSess != nil {
			if !tmuxSess.IsConfigured() && tmuxSess.Exists() {
				tmuxSess.EnsureConfigured()
				inst.SyncSessionIDsToTmux()
				break // Only one per tick to avoid blocking
			}
		}
	}

	// Feed hook statuses from watcher to instances (enables hook fast path in UpdateStatus)
	if h.hookWatcher != nil {
		for _, inst := range instances {
			if session.IsClaudeCompatible(inst.Tool) || inst.Tool == "codex" || inst.Tool == "gemini" {
				if hs := h.hookWatcher.GetHookStatus(inst.ID); hs != nil {
					inst.UpdateHookStatus(hs)
				}
			}
		}
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
		// Check cached analytics for context usage
		cached := h.getAnalyticsForSession(inst)
		if cached == nil {
			continue
		}
		if cached.ContextPercent(0) >= clearOnCompactThreshold {
			if tmuxSess := inst.GetTmuxSession(); tmuxSess != nil {
				h.clearOnCompactSent[inst.ID] = time.Now()
				conductorName := strings.TrimPrefix(inst.Title, "conductor-")
				go func() {
					time.Sleep(500 * time.Millisecond)
					_ = tmuxSess.SendKeysAndEnter("/clear")
					// After /clear wipes context, immediately send heartbeat to restore orientation
					time.Sleep(3 * time.Second)
					_ = session.DefaultProfile
					if meta, err := session.LoadConductorMeta(conductorName); err == nil {
						_ = meta.Profile
					}
					msg := fmt.Sprintf("Heartbeat: Check sessions in your group (%s). List any that are waiting, auto-respond where safe, and report what needs my attention.", conductorName)
					_ = tmuxSess.SendKeysAndEnter(msg)
				}()
			}
		}
	}

	// Update status for all instances in parallel (I/O bound: tmux subprocess calls)
	// With PipeManager, skip sessions idle for >5s (no %output events = no status change)
	statusStart := time.Now()
	var statusChanged atomic.Bool
	var slowMu sync.Mutex
	var slowSessions []string
	pm := tmux.GetPipeManager()
	var skipped int

	g := new(errgroup.Group)
	g.SetLimit(10) // Pool of 10 workers (tmux server serializes, more doesn't help)

	for _, inst := range instances {
		inst := inst // capture loop variable

		// Skip idle sessions when PipeManager knows they haven't produced output.
		// Only skip if pipe is alive (otherwise we need UpdateStatus for Error detection).
		if pm != nil {
			if ts := inst.GetTmuxSession(); ts != nil && pm.IsConnected(ts.Name) {
				lastOut := pm.LastOutputTime(ts.Name)
				if !lastOut.IsZero() && time.Since(lastOut) > 5*time.Second {
					skipped++
					continue
				}
			}
		}

		g.Go(func() error {
			oldStatus := inst.GetStatusThreadSafe()
			instStart := time.Now()
			_ = inst.UpdateStatus()
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
			}
			return nil
		})
	}
	_ = g.Wait() // Errors are logged within each goroutine

	statusDur := time.Since(statusStart)
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

	// Invalidate cache if status changed
	if statusChanged.Load() {
		h.cachedStatusCounts.valid.Store(false)
		h.publishWebSessionStates(instances)
	}
	h.refreshSessionRenderSnapshot(instances)

	// SQLite sync: heartbeat, status writes, ack reads (enables multi-instance coordination)
	if db := statedb.GetGlobal(); db != nil {
		// Heartbeat: mark this process as alive
		_ = db.Heartbeat()

		// Clean dead instances every ~20s (not every tick)
		if time.Since(h.lastDeadInstanceCleanup) > 20*time.Second {
			_ = db.CleanDeadInstances(30 * time.Second)
			h.lastDeadInstanceCleanup = time.Now()
		}

		// Write statuses only when changed to reduce SQLite write pressure.
		currentIDs := make(map[string]struct{}, len(instances))
		for _, inst := range instances {
			currentIDs[inst.ID] = struct{}{}
			status := string(inst.GetStatusThreadSafe())
			if prev, ok := h.lastPersistedStatus[inst.ID]; ok && prev == status {
				continue
			}
			_ = db.WriteStatus(inst.ID, status, inst.Tool)
			h.lastPersistedStatus[inst.ID] = status
		}
		for id := range h.lastPersistedStatus {
			if _, ok := currentIDs[id]; !ok {
				delete(h.lastPersistedStatus, id)
			}
		}

		// Read acknowledgments from SQLite (picks up acks from other instances)
		if ackStatuses, err := db.ReadAllStatuses(); err == nil {
			for _, inst := range instances {
				if s, ok := ackStatuses[inst.ID]; ok && s.Acknowledged {
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

// syncNotificationsBackground updates the tmux notification bar directly
// Called from background worker - does NOT depend on Bubble Tea
func (h *Home) syncNotificationsBackground() {
	defer func() {
		if r := recover(); r != nil {
			notifLog.Error("sync_notifications_panic", slog.Any("panic", r))
		}
	}()

	if !h.manageTmuxNotifications || !h.notificationsEnabled || h.notificationManager == nil {
		return
	}

	// Phase 1: Check for signal file from Ctrl+b 1-6 shortcuts
	// CRITICAL: This must be done in background sync too, because the foreground
	// sync might not run when user is attached to a session (tea.Exec pauses TUI)
	var sessionToAcknowledgeID string
	if signalSessionID := tmux.ReadAndClearAckSignal(); signalSessionID != "" {
		sessionToAcknowledgeID = signalSessionID
		notifLog.Debug("signal_found", slog.String("session_id", signalSessionID))

		// Track notification switch during attach for cursor sync on detach
		if h.isAttaching.Load() {
			h.lastNotifSwitchMu.Lock()
			h.lastNotifSwitchID = signalSessionID
			h.lastNotifSwitchMu.Unlock()
			notifLog.Debug("attach_switch_recorded", slog.String("session_id", signalSessionID))
		}
	}

	// Get current instances (copy to avoid race with main goroutine)
	h.instancesMu.RLock()
	instances := make([]*session.Instance, len(h.instances))
	copy(instances, h.instances)

	// Phase 2: Acknowledge the session if signal was received
	if sessionToAcknowledgeID != "" {
		if inst, ok := h.instanceByID[sessionToAcknowledgeID]; ok {
			if ts := inst.GetTmuxSession(); ts != nil {
				ts.Acknowledge()
				// Persist ack to SQLite so other instances see it
				if db := statedb.GetGlobal(); db != nil {
					_ = db.SetAcknowledged(inst.ID, true)
				}
				_ = inst.UpdateStatus()
				notifLog.Debug(
					"session_acknowledged",
					slog.String("title", inst.Title),
					slog.String("status", string(inst.Status)),
				)
			}
		}
	}
	h.instancesMu.RUnlock()

	// Detect currently attached session (may be the user's session during tea.Exec)
	currentSessionID := h.getAttachedSessionID()

	// Signal file takes priority for determining "current" session
	if sessionToAcknowledgeID != "" {
		currentSessionID = sessionToAcknowledgeID
	}

	notifLog.Debug(
		"sync_state",
		slog.String("current_session_id", currentSessionID),
		slog.Int("instances", len(instances)),
	)

	// Sync notification manager with current states
	h.notificationManager.SyncFromInstances(instances, currentSessionID)

	// Update tmux status bar directly
	barText := h.notificationManager.FormatBar()

	// Only update if changed (avoid unnecessary tmux calls)
	h.lastBarTextMu.Lock()
	if barText != h.lastBarText {
		h.lastBarText = barText
		h.lastBarTextMu.Unlock()

		if barText == "" {
			_ = tmux.ClearStatusLeftGlobal()
		} else {
			_ = tmux.SetStatusLeftGlobal(barText)
		}

		// Force immediate visual update (bypasses 15-second status-interval)
		_ = tmux.RefreshStatusBarImmediate()

		notifLog.Info("bar_updated", slog.String("text", barText))
	} else {
		h.lastBarTextMu.Unlock()
	}

	// CRITICAL: Update key bindings in background too!
	// This fixes the bug where key bindings became stale when TUI was paused (tea.Exec).
	// updateKeyBindings() is thread-safe via boundKeysMu.
	h.updateKeyBindings()
}

// updateKeyBindings updates tmux key bindings based on current notification entries.
// Thread-safe via boundKeysMu. Can be called from both foreground and background.
func (h *Home) updateKeyBindings() {
	if !h.manageTmuxNotifications {
		return
	}

	// Minimal mode shows counts only — no named slots, no key bindings to manage
	if h.notificationManager.IsMinimal() {
		return
	}

	entries := h.notificationManager.GetEntries()

	// Phase 1: Collect binding info while holding instancesMu (read-only)
	type bindingInfo struct {
		key        string
		sessionID  string
		tmuxName   string
		bindingKey string // "sessionID:tmuxName"
	}
	bindings := make([]bindingInfo, 0, len(entries))
	currentKeys := make(map[string]string) // key -> sessionID

	h.instancesMu.RLock()
	for _, e := range entries {
		currentKeys[e.AssignedKey] = e.SessionID

		// Look up CURRENT TmuxName from instance (cached entry may be stale)
		currentTmuxName := e.TmuxName
		if inst, ok := h.instanceByID[e.SessionID]; ok {
			if ts := inst.GetTmuxSession(); ts != nil {
				currentTmuxName = ts.Name
			}
		}

		bindings = append(bindings, bindingInfo{
			key:        e.AssignedKey,
			sessionID:  e.SessionID,
			tmuxName:   currentTmuxName,
			bindingKey: e.SessionID + ":" + currentTmuxName,
		})
	}
	h.instancesMu.RUnlock()

	// Phase 2: Update key bindings while holding boundKeysMu
	h.boundKeysMu.Lock()
	for _, b := range bindings {
		existingBinding, isBound := h.boundKeys[b.key]
		if !isBound || existingBinding != b.bindingKey {
			_ = tmux.BindSwitchKeyWithAck(b.key, b.tmuxName, b.sessionID)
			h.boundKeys[b.key] = b.bindingKey
		}
	}

	// Unbind keys no longer needed
	for key := range h.boundKeys {
		if _, stillNeeded := currentKeys[key]; !stillNeeded {
			_ = tmux.UnbindKey(key)
			delete(h.boundKeys, key)
		}
	}
	h.boundKeysMu.Unlock()
}

// triggerStatusUpdate sends a non-blocking request to the background worker
// If the worker is busy, the request is dropped (next tick will retry)
func (h *Home) triggerStatusUpdate() {
	// Build list of session IDs from flatItems for visible detection
	flatItemIDs := make([]string, 0, len(h.flatItems))
	for _, item := range h.flatItems {
		if item.Type == session.ItemTypeSession && item.Session != nil {
			flatItemIDs = append(flatItemIDs, item.Session.ID)
		}
	}

	visibleHeight := h.height - 8
	if visibleHeight < 5 {
		visibleHeight = 5
	}

	req := statusUpdateRequest{
		viewOffset:    h.viewOffset,
		visibleHeight: visibleHeight,
		flatItemIDs:   flatItemIDs,
	}

	// Non-blocking send - if worker is busy, skip this tick
	select {
	case h.statusTrigger <- req:
		// Request sent successfully
	default:
		// Worker busy, will retry next tick
	}
}

// processStatusUpdate implements round-robin status updates (Priority 1A + 1B)
// Called by the background worker goroutine
// Instead of updating ALL sessions every tick (which causes lag with 100+ sessions),
// we update in batches:
//   - Always update visible sessions first (ensures UI responsiveness)
//   - Round-robin through remaining sessions (spreads CPU load over time)
//
// Performance: With 10 sessions, updating all takes ~1-2s of cumulative time per tick.
// With batching (3 visible + 2 non-visible per tick), we keep each tick under 100ms.
func (h *Home) processStatusUpdate(req statusUpdateRequest) {
	const batchSize = 2 // Reduced from 5 to 2 - fewer CapturePane() calls per tick
	if hotUntil := h.navigationHotUntil.Load(); hotUntil > 0 && time.Now().UnixNano() < hotUntil {
		return
	}
	if last := h.lastFullStatusSweep.Load(); last > 0 {
		if time.Since(time.Unix(0, last)) < 1500*time.Millisecond {
			// A full all-session sweep just ran; skip redundant incremental update.
			return
		}
	}

	// CRITICAL FIX: Refresh session cache in background worker, NOT main goroutine
	// This prevents UI freezing when subprocess spawning is slow (high system load)
	// The cache refresh spawns `tmux list-sessions` which can block for 50-200ms
	tmux.RefreshExistingSessions()

	// Take a snapshot of instances under read lock (thread-safe)
	h.instancesMu.RLock()
	if len(h.instances) == 0 {
		h.instancesMu.RUnlock()
		return
	}
	instancesCopy := make([]*session.Instance, len(h.instances))
	copy(instancesCopy, h.instances)
	h.instancesMu.RUnlock()

	// Build set of visible session IDs for quick lookup
	visibleIDs := make(map[string]bool)

	// Find visible sessions based on viewOffset and flatItemIDs
	for i := req.viewOffset; i < len(req.flatItemIDs) && i < req.viewOffset+req.visibleHeight; i++ {
		visibleIDs[req.flatItemIDs[i]] = true
	}

	// Track which sessions we've updated this tick
	updated := make(map[string]bool)
	// Track if any status actually changed (for cache invalidation)
	statusChanged := false

	// Step 1: Always update visible sessions (Priority 1B - visible first)
	for _, inst := range instancesCopy {
		if visibleIDs[inst.ID] {
			oldStatus := inst.GetStatusThreadSafe()
			_ = inst.UpdateStatus() // Ignore errors in background worker
			if inst.GetStatusThreadSafe() != oldStatus {
				statusChanged = true
			}
			updated[inst.ID] = true
		}
	}

	// Step 2: Round-robin through non-visible sessions (Priority 1A - batching)
	// OPTIMIZATION: Skip idle sessions - they need user interaction to become active.
	// This significantly reduces CapturePane() calls for large session lists.
	remaining := batchSize
	startIdx := int(h.statusUpdateIndex.Load())
	instanceCount := len(instancesCopy)

	for i := 0; i < instanceCount && remaining > 0; i++ {
		idx := (startIdx + i) % instanceCount
		inst := instancesCopy[idx]

		// Skip if already updated (visible)
		if updated[inst.ID] {
			continue
		}

		// Skip idle sessions - they require user interaction to change state
		// Background polling will catch any activity when user interacts
		if inst.GetStatusThreadSafe() == session.StatusIdle {
			continue
		}

		oldStatus := inst.GetStatusThreadSafe()
		_ = inst.UpdateStatus() // Ignore errors in background worker
		if inst.GetStatusThreadSafe() != oldStatus {
			statusChanged = true
		}
		remaining--
		h.statusUpdateIndex.Store(int32((idx + 1) % instanceCount))
	}

	// Only invalidate status counts cache if status actually changed
	// This reduces View() overhead by keeping cache valid when no changes occurred
	if statusChanged {
		h.cachedStatusCounts.valid.Store(false)
		h.publishWebSessionStates(instancesCopy)
	}
	h.refreshSessionRenderSnapshot(instancesCopy)
}

// Update implements tea.Model. It delegates to updateInner and, when
// fullRepaint is enabled, appends tea.ClearScreen on KeyMsg and mouse-wheel
// MouseMsg events to prevent incremental-redraw drift between the tick-based
// clears (issue #607). Under the default (full_repaint = false) this wrapper
// is a pass-through — no regression for users who never opt in.
func (h *Home) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	model, cmd := h.updateInner(msg)
	if !h.fullRepaint {
		return model, cmd
	}
	switch m := msg.(type) {
	case tea.KeyMsg:
		_ = m
		return model, appendClearScreen(cmd)
	case tea.MouseMsg:
		if m.Button == tea.MouseButtonWheelUp || m.Button == tea.MouseButtonWheelDown {
			return model, appendClearScreen(cmd)
		}
	}
	return model, cmd
}

// appendClearScreen batches tea.ClearScreen onto cmd, preserving nil-safety.
func appendClearScreen(cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return tea.ClearScreen
	}
	return tea.Batch(cmd, tea.ClearScreen)
}

func (h *Home) updateInner(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case quitMsg:
		// Execute final shutdown logic after splash delay
		return h, h.performFinalShutdown(bool(msg))

	case tea.WindowSizeMsg:
		h.width = msg.Width
		h.height = msg.Height
		h.updateSizes()
		h.syncViewport() // Recalculate viewport when window size changes
		h.setupWizard.SetSize(msg.Width, msg.Height)
		h.settingsPanel.SetSize(msg.Width, msg.Height)
		h.watcherPanel.SetSize(msg.Width, msg.Height)
		h.geminiModelDialog.SetSize(msg.Width, msg.Height)
		return h, nil

	case tea.MouseMsg:
		// Route mouse wheel events to the active scrollable area.
		// Priority: setup wizard > settings > help > global search > MCP dialog > new/fork dialogs > main list.
		// Non-wheel events are silently ignored (O(1), no blocking I/O).
		switch msg.Button {
		case tea.MouseButtonWheelUp, tea.MouseButtonWheelDown:
			if h.setupWizard.IsVisible() {
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
			// preview region of the dual layout, scroll preview content
			// instead of moving the list cursor. Other layouts keep the
			// legacy list-scroll behaviour because they have no dedicated
			// preview click-target (single = no preview; stacked = same-
			// width column where Y-based routing is ambiguous enough to
			// leave as list-scroll).
			if h.getLayoutMode() == LayoutModeDual {
				leftWidth := int(float64(h.width) * 0.35)
				if msg.X >= leftWidth {
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
					h.previewScrollOffset = 0
					h.syncViewport()
					h.markNavigationActivity()
					return h, h.fetchSelectedPreview()
				}
			} else {
				if h.cursor < len(h.flatItems)-1 {
					h.cursor++
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

	case loadSessionsMsg:
		// Clear loading indicators and store file mtime for external change detection
		h.reloadMu.Lock()
		h.isReloading = false
		if !msg.loadMtime.IsZero() {
			h.lastLoadMtime = msg.loadMtime
		}
		h.reloadMu.Unlock()
		h.initialLoading = false // First load complete, hide splash
		h.reloadHotkeysFromConfig()

		// Show hooks installation prompt (after splash screen is gone)
		if h.pendingHooksPrompt && !h.setupWizard.IsVisible() {
			h.confirmDialog.ShowInstallHooks()
			h.confirmDialog.SetSize(h.width, h.height)
		}

		// Show feedback popup if user has a new version and hasn't rated yet (D-11/D-12)
		if h.feedbackDialog != nil && !h.feedbackDialog.IsVisible() {
			fbState, _ := feedback.LoadState()
			if feedback.ShouldShow(fbState, Version) {
				feedback.RecordShown(fbState)
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
						}
					case session.ItemTypeGroup:
						msg.restoreState.cursorGroupPath = currentItem.Path
						msg.restoreState.cursorSessionID = ""
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
				if inst.Tool == "opencode" && inst.OpenCodeSessionID == "" {
					detectionCmds = append(detectionCmds, h.detectOpenCodeSessionCmd(inst))
				}
			}
			h.instancesMu.Unlock()
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
				for id, title := range h.pendingTitleChanges {
					if inst := h.getInstanceByID(id); inst != nil && inst.Title != title {
						inst.Title = title
						inst.SyncTmuxDisplayName()
						applied = true
						uiLog.Info("pending_rename_reapplied",
							slog.String("session_id", id),
							slog.String("title", title))
					}
				}
				// Clear pending changes and persist if any were re-applied
				h.pendingTitleChanges = make(map[string]string)
				if applied {
					h.forceSaveInstances()
				}
			}

			// Restore state if provided (from auto-reload)
			if msg.restoreState != nil {
				h.restoreState(*msg.restoreState)
				h.syncViewport()
			} else {
				h.rebuildFlatItems()
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
					h.pendingCursorRestore = nil
					h.syncViewport()
				}
				// Save after dedup to persist any ID changes (initial load only)
				h.saveInstances()
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
			// Use forceSave to bypass mtime check - new session creation MUST persist
			h.forceSaveInstances()

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
			// Use forceSave to bypass mtime check - forked session MUST persist
			h.forceSaveInstances()

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
		if deletedInstance != nil {
			h.pushUndoStack(deletedInstance)
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
		// Explicitly delete from database to prevent resurrection on reload
		if err := h.storage.DeleteInstance(msg.deletedID); err != nil {
			uiLog.Warn("delete_instance_db_err", slog.String("id", msg.deletedID), slog.String("err", err.Error()))
		}
		// Save both instances AND groups (critical fix: was losing groups!)
		// Use forceSave to bypass mtime check - delete MUST persist
		h.forceSaveInstances()

		// Show undo hint (using setError as a transient message)
		if deletedInstance != nil {
			if undoKey := h.actionKey(hotkeyUndoDelete); undoKey != "" {
				h.setError(fmt.Errorf("deleted '%s'. %s to undo", deletedInstance.Title, undoKey))
			} else {
				h.setError(fmt.Errorf("deleted '%s'", deletedInstance.Title))
			}
		}
		return h, nil

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

	case sessionRestoredMsg:
		h.reloadMu.Lock()
		reloading := h.isReloading
		h.reloadMu.Unlock()
		if reloading {
			uiLog.Debug("reload_skip_session_restored")
			return h, nil
		}
		if msg.err != nil {
			h.setError(fmt.Errorf("failed to restore session: %w", msg.err))
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

		// Use forceSave to bypass mtime check - restore MUST persist
		h.forceSaveInstances()
		if msg.warning != "" {
			h.setError(fmt.Errorf("restored '%s' (%s)", msg.instance.Title, msg.warning))
		} else {
			h.setError(fmt.Errorf("restored '%s'", msg.instance.Title))
		}
		return h, h.fetchPreview(msg.instance, msg.instance.ID, -1)

	case openCodeDetectionCompleteMsg:
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
			// Save the updated session state (new tmux session name)
			h.saveInstances()
			if msg.warning != "" {
				h.setError(fmt.Errorf("%s", msg.warning))
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

	case updateCheckMsg:
		h.lastUpdateCheck = time.Now()
		if msg.info != nil && !msg.info.Available {
			// Update is no longer available (e.g., user updated via terminal) — dismiss banner
			h.updateInfo = nil
		} else {
			h.updateInfo = msg.info
		}
		return h, nil

	case remoteSessionsFetchedMsg:
		h.remoteSessionsMu.Lock()
		h.remoteSessions = msg.sessions
		h.lastRemoteFetch = time.Now()
		h.remotesFetchActive = false
		h.remoteSessionsMu.Unlock()
		h.rebuildFlatItems()
		return h, nil

	case remoteSessionDeletedMsg:
		if msg.err != nil {
			h.setError(fmt.Errorf("failed to delete remote session: %w", msg.err))
			return h, nil
		}
		h.setError(fmt.Errorf("deleted '%s' on %s", msg.title, msg.remoteName))
		return h, h.fetchRemoteSessions

	case remoteSessionClosedMsg:
		if msg.err != nil {
			h.setError(fmt.Errorf("failed to close remote session: %w", msg.err))
			return h, nil
		}
		h.setError(fmt.Errorf("closed '%s' on %s", msg.title, msg.remoteName))
		return h, h.fetchRemoteSessions

	case remoteSessionRestartedMsg:
		if msg.err != nil {
			h.setError(fmt.Errorf("failed to restart remote session: %w", msg.err))
			return h, nil
		}
		h.setError(fmt.Errorf("restarted '%s' on %s", msg.title, msg.remoteName))
		return h, h.fetchRemoteSessions

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
			// Auto-clear after 30 seconds
			return h, tea.Tick(30*time.Second, func(_ time.Time) tea.Msg {
				return clearMaintenanceMsg{}
			})
		}
		return h, nil

	case reviverTickMsg:
		// Fire-and-forget reviver sweep for instances whose tmux server
		// survived an SSH scope cleanup but whose pipe was reaped. Runs in
		// a goroutine so it never blocks the Bubble Tea update loop.
		go func(instances []*session.Instance) {
			rev := session.NewReviver()
			_ = rev.ReviveAll(instances)
		}(append([]*session.Instance(nil), h.instances...))
		return h, h.reviverTick()

	case clearMaintenanceMsg:
		h.maintenanceMsg = ""
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

	case refreshMsg:
		return h, h.loadSessions

	case systemThemeMsg:
		theme := "light"
		if msg.dark {
			theme = "dark"
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

		// Reload from disk
		cmd := func() tea.Msg {
			// Capture file mtime BEFORE loading to detect external changes later
			loadMtime, _ := h.storage.GetFileMtime()
			instances, groups, err := h.storage.LoadWithGroups()
			uiLog.Debug("reload_load_with_groups", slog.Int("instances", len(instances)), slog.Any("error", err))
			return loadSessionsMsg{
				instances:    instances,
				groups:       groups,
				err:          err,
				restoreState: &state, // Pass state to restore after load
				loadMtime:    loadMtime,
			}
		}

		// Continue listening for next change
		return h, tea.Batch(cmd, listenForReloads(h.storageWatcher))

	case statusUpdateMsg:
		// Clear attach flag - we've returned from the attached session
		h.isAttaching.Store(false) // Atomic store for thread safety
		now := time.Now()
		h.beginAttachReturnGrace(now)

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
			return h, tea.EnableMouseCellMotion
		}

		h.followAttachReturnCwd(msg)

		// PERFORMANCE FIX: Skip save on attach return for 10 seconds
		// Saving can also be blocking (JSON serialization + file write).
		// Combine with periodic save instead of saving on every attach/detach.
		// We'll let the next tickMsg handle background save if needed.

		// Re-enable mouse mode after returning from tea.Exec (tmux detach-client
		// resets mouse reporting), restore legacy keyboard reporting (tmux's
		// extended-keys setting leaves Kitty/modifyOtherKeys on the outer terminal;
		// see RestoreLegacyKeyboardCmd for the full rationale), and schedule a
		// delayed refresh so the main menu reflects attach-return state changes.
		return h, tea.Batch(
			tea.EnableMouseCellMotion,
			RestoreLegacyKeyboardCmd(os.Stdout),
			tea.Tick(attachReturnRefreshDelay, func(time.Time) tea.Msg { return attachReturnRefreshMsg{} }),
		)

	case attachReturnRefreshMsg:
		selectedBefore := h.captureSelectedItemIdentity()
		tmux.RefreshSessionCache()
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
		// Async preview content received - always advance the TTL so failures
		// and empty responses don't trigger a fetch on every tick.
		// Protect both previewFetchingID and previewCache with the same mutex
		h.previewCacheMu.Lock()
		h.previewFetchingID = ""
		h.previewCacheTime[msg.previewKey] = time.Now()
		if msg.err == nil {
			h.previewCache[msg.previewKey] = msg.content
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
		if err := h.storage.DeleteInstance(msg.sessionID); err != nil {
			uiLog.Warn("worktree_finish_delete_err", slog.String("id", msg.sessionID), slog.String("err", err.Error()))
		}
		h.forceSaveInstances()

		// Show success message
		successMsg := fmt.Sprintf("Finished worktree '%s'", msg.sessionTitle)
		if msg.merged {
			successMsg += fmt.Sprintf(", merged into %s", msg.targetBranch)
		}
		h.setError(fmt.Errorf("%s", successMsg))
		return h, nil

	case copyResultMsg:
		if msg.err != nil {
			h.setError(msg.err)
		} else {
			h.setError(fmt.Errorf("Copied %d lines to clipboard (%s)", msg.lineCount, msg.sessionTitle))
		}
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
		h.refreshWatcherPanel()
		if h.watcherEngine != nil {
			return h, listenForWatcherEvent(h.watcherEngine.EventCh())
		}
		return h, nil

	case watcherHealthMsg:
		// Update health display and re-register listener.
		h.refreshWatcherPanel()
		// Dispatch health alert to conductor session on warning/error transitions (D-22, D-23).
		if msg.state.Status == watcher.HealthStatusWarning || msg.state.Status == watcher.HealthStatusError {
			h.dispatchHealthAlert(msg.state)
		}
		if h.watcherEngine != nil {
			return h, listenForWatcherHealth(h.watcherEngine.HealthCh())
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
		var remoteFetchCmd tea.Cmd

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

		// Periodic remote session fetch (every 30 seconds)
		h.remoteSessionsMu.RLock()
		shouldFetch := !h.remotesFetchActive && time.Since(h.lastRemoteFetch) >= 30*time.Second
		h.remoteSessionsMu.RUnlock()
		if shouldFetch {
			h.remoteSessionsMu.Lock()
			h.remotesFetchActive = true
			h.remoteSessionsMu.Unlock()
			remoteFetchCmd = h.fetchRemoteSessions
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

			// Prune dead pipes and connect new sessions
			if pm := tmux.GetPipeManager(); pm != nil {
				h.instancesMu.RLock()
				for _, inst := range h.instances {
					if ts := inst.GetTmuxSession(); ts != nil && ts.Exists() {
						if !pm.IsConnected(ts.Name) {
							go func(name string) {
								_ = pm.Connect(name)
							}(ts.Name)
						}
					}
				}
				h.instancesMu.RUnlock()
			}
		}

		// Full log maintenance (orphan cleanup, etc) every 5 minutes
		if time.Since(h.lastLogMaintenance) >= logMaintenanceInterval {
			h.lastLogMaintenance = time.Now()
			go func() {
				logSettings := session.GetLogSettings()
				tmux.RunLogMaintenance(logSettings.MaxSizeMB, logSettings.MaxLines, logSettings.RemoveOrphans)
			}()
		}

		// Periodic update re-check every 5 minutes to dismiss stale banner
		// after the user updates agent-deck via terminal while TUI is running
		const updateRecheckInterval = 5 * time.Minute
		if h.updateInfo != nil && h.updateInfo.Available && time.Since(h.lastUpdateCheck) >= updateRecheckInterval {
			h.lastUpdateCheck = time.Now()
			return h, tea.Batch(h.tick(), h.checkForUpdate())
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
			if ok {
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
		cmds := []tea.Cmd{h.tick(), previewCmd, remoteFetchCmd}
		if h.fullRepaint {
			cmds = append(cmds, tea.ClearScreen)
		}
		return h, tea.Batch(cmds...)

	case globalSearchDebounceMsg, globalSearchResultsMsg:
		// Route async global search messages to the global search component
		if h.globalSearch.IsVisible() {
			var cmd tea.Cmd
			h.globalSearch, cmd = h.globalSearch.Update(msg)
			return h, cmd
		}
		return h, nil

	case tea.KeyMsg:
		// Track user activity for adaptive status updates
		h.lastUserInputTime = time.Now()

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
				// Save config and close wizard
				config := h.setupWizard.GetConfig()
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

		// Handle watcher panel (before settings panel)
		if h.watcherPanel.IsVisible() {
			var cmd tea.Cmd
			h.watcherPanel, cmd = h.watcherPanel.Update(msg)
			return h, cmd
		}

		// Handle settings panel
		if h.settingsPanel.IsVisible() {
			var cmd tea.Cmd
			var shouldSave bool
			h.settingsPanel, cmd, shouldSave = h.settingsPanel.Update(msg)
			if shouldSave {
				config := h.settingsPanel.GetConfig()
				if err := session.SaveUserConfig(config); err != nil {
					h.err = err
					h.errTime = time.Now()
				}
				_, _ = session.ReloadUserConfig()
				h.reloadHotkeysFromConfig()

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
		if h.editPathsDialog.IsVisible() {
			return h.handleEditPathsDialogKey(msg)
		}
		if h.skillDialog.IsVisible() {
			return h.handleSkillDialogKey(msg)
		}
		if h.geminiModelDialog.IsVisible() {
			d, cmd := h.geminiModelDialog.Update(msg)
			h.geminiModelDialog = d
			return h, cmd
		}
		if h.sessionPickerDialog.IsVisible() {
			return h.handleSessionPickerDialogKey(msg)
		}
		if h.worktreeFinishDialog.IsVisible() {
			return h.handleWorktreeFinishDialogKey(msg)
		}
		if h.feedbackDialog.IsVisible() {
			d, cmd := h.feedbackDialog.Update(msg)
			h.feedbackDialog = d
			return h, cmd
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

// handleSearchKey handles keys when search is visible
func (h *Home) handleSearchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		selected := h.search.Selected()
		if selected != nil {
			// Ensure the session's group AND all parent groups are expanded so it's visible
			if selected.GroupPath != "" {
				h.groupTree.ExpandGroupWithParents(selected.GroupPath)
			}
			h.rebuildFlatItems()

			// Find the session in flatItems (not instances) and set cursor
			for i, item := range h.flatItems {
				if item.Type == session.ItemTypeSession && item.Session != nil && item.Session.ID == selected.ID {
					h.cursor = i
					h.syncViewport() // Ensure the cursor is visible in the viewport
					break
				}
			}
		}
		h.search.Hide()
		return h, nil
	case "esc":
		h.search.Hide()
		return h, nil
	}

	var cmd tea.Cmd
	h.search, cmd = h.search.Update(msg)

	// Check if user wants to switch to global search
	if h.search.WantsSwitchToGlobal() && h.globalSearchIndex != nil {
		h.globalSearch.SetSize(h.width, h.height)
		h.globalSearch.Show()
	}

	return h, cmd
}

// handleGlobalSearchKey handles keys when global search is visible
func (h *Home) handleGlobalSearchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		selected := h.globalSearch.Selected()
		if selected != nil {
			h.globalSearch.Hide()
			return h, h.handleGlobalSearchSelection(selected)
		}
		h.globalSearch.Hide()
		return h, nil
	case "esc":
		h.globalSearch.Hide()
		return h, nil
	}

	var cmd tea.Cmd
	h.globalSearch, cmd = h.globalSearch.Update(msg)

	// Check if user wants to switch to local search
	if h.globalSearch.WantsSwitchToLocal() {
		h.search.SetItems(h.instances)
		h.search.Show()
	}

	return h, cmd
}

// handleGlobalSearchSelection handles selection from global search
func (h *Home) handleGlobalSearchSelection(result *GlobalSearchResult) tea.Cmd {
	// Check if session already exists in Agent Deck
	h.instancesMu.RLock()
	for _, inst := range h.instances {
		if inst.ClaudeSessionID == result.SessionID {
			h.instancesMu.RUnlock()
			// Jump to existing session
			h.jumpToSession(inst)
			return nil
		}
	}
	h.instancesMu.RUnlock()

	// Create new session with this Claude session ID
	return h.createSessionFromGlobalSearch(result)
}

// jumpToSession jumps the cursor to the specified session
func (h *Home) jumpToSession(inst *session.Instance) {
	// Ensure the session's group is expanded
	if inst.GroupPath != "" {
		h.groupTree.ExpandGroupWithParents(inst.GroupPath)
	}
	h.rebuildFlatItems()

	// Find and select the session
	for i, item := range h.flatItems {
		if item.Type == session.ItemTypeSession && item.Session != nil && item.Session.ID == inst.ID {
			h.cursor = i
			h.syncViewport()
			break
		}
	}
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

		// Create instance. Issue #666: resolveNewSessionGroup rescues empty
		// cursor-group (Window / RemoteGroup / placeholder flatItems) so
		// the empty string never reaches NewInstanceWithGroupAndTool, which
		// would otherwise override the extractGroupPath default with "".
		inst := session.NewInstanceWithGroupAndTool(title, projectPath, h.resolveNewSessionGroup(), "claude")
		inst.ClaudeSessionID = result.SessionID

		// Build resume command with config dir and permission flags
		userConfig, _ := session.LoadUserConfig()
		opts := session.NewClaudeOptions(userConfig)

		// Build command - only set CLAUDE_CONFIG_DIR if explicitly configured
		// If not explicit, let the tmux shell's environment handle it
		// This is critical for WSL and other environments where users have
		// CLAUDE_CONFIG_DIR set in their .bashrc/.zshrc
		var cmdBuilder strings.Builder
		if session.IsClaudeConfigDirExplicitForGroup(inst.GroupPath) {
			configDir := session.GetClaudeConfigDirForGroup(inst.GroupPath)
			cmdBuilder.WriteString(fmt.Sprintf("CLAUDE_CONFIG_DIR=%s ", configDir))
		}
		cmdBuilder.WriteString("claude --resume ")
		cmdBuilder.WriteString(result.SessionID)
		if opts.SkipPermissions {
			cmdBuilder.WriteString(" --dangerously-skip-permissions")
		} else if opts.AllowSkipPermissions {
			cmdBuilder.WriteString(" --allow-dangerously-skip-permissions")
		}
		inst.Command = cmdBuilder.String()

		// Persist options so restarts use per-session settings
		_ = inst.SetClaudeOptions(opts)

		// Start the session
		if err := inst.Start(); err != nil {
			return sessionCreatedMsg{err: fmt.Errorf("failed to start session: %w", err)}
		}

		return sessionCreatedMsg{instance: inst}
	}
}

// getCurrentGroupPath returns the group path of the currently selected item
func (h *Home) getCurrentGroupPath() string {
	if h.cursor >= 0 && h.cursor < len(h.flatItems) {
		item := h.flatItems[h.cursor]
		if item.Type == session.ItemTypeGroup && item.Group != nil {
			return item.Group.Path
		}
		if item.Type == session.ItemTypeSession && item.Session != nil {
			return item.Session.GroupPath
		}
	}
	return ""
}

// resolveNewSessionGroup returns the group path a freshly-created session
// should land in when no explicit group is chosen via a dialog.
//
// Issue #666: the createSessionFromGlobalSearch path (a Claude
// conversation imported from global search) previously called
// getCurrentGroupPath() directly and passed its return value into
// NewInstanceWithGroupAndTool. When the cursor was on a Window,
// RemoteGroup, RemoteSession, or creating-placeholder item,
// getCurrentGroupPath returned "" (home.go:4810). That empty string
// OVERRODE the extractGroupPath default set at construction time inside
// NewInstanceWithGroupAndTool — producing an Instance with
// GroupPath="". At the next reload the empty row silently re-derived
// via extractGroupPath(ProjectPath), making the imported session appear
// under a path-derived group instead of the one the user was browsing.
// Exact user-visible symptom: "TUI-created session ends up in a
// different group, sometimes with a path-derived name."
//
// The rescue contract: always return a non-empty, user-recoverable
// group. Prefer the cursor's group, then the scoped-view root, then
// DefaultGroupPath as the universal safe default.
func (h *Home) resolveNewSessionGroup() string {
	if gp := h.getCurrentGroupPath(); gp != "" {
		return gp
	}
	if h.groupScope != "" {
		return h.groupScope
	}
	return session.DefaultGroupPath
}

// handleNewDialogKey handles keys when new dialog is visible
func (h *Home) handleNewDialogKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// When the recent sessions picker is open, let the dialog handle all keys first.
	if h.newDialog.IsRecentPickerOpen() {
		var cmd tea.Cmd
		h.newDialog, cmd = h.newDialog.Update(msg)
		return h, cmd
	}
	// When branch search results are visible, let the dialog consume Enter/Esc/navigation
	// before the outer dialog-level handlers create/cancel the session.
	if h.newDialog.IsBranchPickerOpen() {
		var cmd tea.Cmd
		h.newDialog, cmd = h.newDialog.Update(msg)
		return h, cmd
	}

	switch msg.String() {
	case "enter":
		// When multi-repo path list is focused, let the dialog handle enter (edit/save path).
		if h.newDialog.IsMultiRepoEditing() {
			var cmd tea.Cmd
			h.newDialog, cmd = h.newDialog.Update(msg)
			return h, cmd
		}

		// Validate before creating session
		if validationErr := h.newDialog.Validate(); validationErr != "" {
			h.newDialog.SetError(validationErr)
			return h, nil
		}

		// Get values including worktree settings.
		name, path, command, branchName, worktreeEnabled := h.newDialog.GetValuesWithWorktree()
		groupPath := h.newDialog.GetSelectedGroup()
		claudeOpts := h.newDialog.GetClaudeOptions() // Get Claude options if applicable.

		// Resolve worktree target if enabled; actual worktree creation runs in async command.
		var worktreePath, worktreeRepoRoot string
		if worktreeEnabled && branchName != "" {
			// Validate path is a git repo
			if !git.IsGitRepo(path) {
				h.newDialog.SetError("Path is not a git repository")
				return h, nil
			}

			repoRoot, err := git.GetWorktreeBaseRoot(path)
			if err != nil {
				h.newDialog.SetError(fmt.Sprintf("Failed to get repo root: %v", err))
				return h, nil
			}

			// Generate worktree path using configured location/template
			wtSettings := session.GetWorktreeSettings()
			worktreePath = git.WorktreePath(git.WorktreePathOptions{
				Branch:    branchName,
				Location:  wtSettings.DefaultLocation,
				RepoDir:   repoRoot,
				SessionID: git.GeneratePathID(),
				Template:  wtSettings.Template(),
			})

			// Store repo root for later use
			worktreeRepoRoot = repoRoot
		}

		// Build generic toolOptionsJSON from tool-specific options
		var toolOptionsJSON json.RawMessage
		var claudeExtraArgs []string
		if command == "claude" && claudeOpts != nil {
			toolOptionsJSON, _ = session.MarshalToolOptions(claudeOpts)
			claudeExtraArgs = h.newDialog.GetClaudeExtraArgs()
		} else if command == "codex" {
			yolo := h.newDialog.GetCodexYoloMode()
			codexOpts := &session.CodexOptions{YoloMode: &yolo}
			toolOptionsJSON, _ = session.MarshalToolOptions(codexOpts)
		}

		parentSessionID := h.newDialog.GetParentSessionID()
		parentProjectPath := h.newDialog.GetParentProjectPath()

		// Only non-worktree sessions may need interactive "create directory" confirmation.
		if !worktreeEnabled {
			if _, err := os.Stat(path); os.IsNotExist(err) {
				h.newDialog.Hide()
				h.confirmDialog.ShowCreateDirectory(path, name, command, groupPath, toolOptionsJSON, claudeExtraArgs, parentSessionID, parentProjectPath)
				return h, nil
			}
		}

		h.newDialog.Hide()
		h.clearError()

		geminiYoloMode := h.newDialog.IsGeminiYoloMode()
		sandboxMode := h.newDialog.IsSandboxEnabled()
		multiRepoPaths, multiRepoEnabled := h.newDialog.GetMultiRepoPaths()
		var additionalPaths []string
		if multiRepoEnabled && len(multiRepoPaths) > 1 {
			// First path stays as ProjectPath, rest are additional
			path = multiRepoPaths[0]
			additionalPaths = multiRepoPaths[1:]
		}

		// Show immediate placeholder in UI while worktree + session is created async
		var tempID string
		if worktreeEnabled && branchName != "" {
			tempID = session.GenerateID()
			h.creatingSessions[tempID] = &CreatingSession{
				ID:        tempID,
				Title:     name,
				Tool:      command,
				GroupPath: groupPath,
				StartTime: time.Now(),
			}
			h.rebuildFlatItems()
			// Auto-select the placeholder
			for i, item := range h.flatItems {
				if item.CreatingID == tempID {
					h.cursor = i
					h.syncViewport()
					break
				}
			}
		}

		return h, h.createSessionInGroupWithWorktreeAndOptions(
			name,
			path,
			command,
			groupPath,
			worktreePath,
			worktreeRepoRoot,
			branchName,
			geminiYoloMode,
			sandboxMode,
			toolOptionsJSON,
			claudeExtraArgs,
			multiRepoEnabled,
			additionalPaths,
			parentSessionID,
			parentProjectPath,
			tempID,
		)

	case "esc":
		h.newDialog.Hide()
		h.clearError() // Clear any validation error
		return h, nil
	}

	var cmd tea.Cmd
	h.newDialog, cmd = h.newDialog.Update(msg)
	return h, cmd
}

func (h *Home) beginNotesEditing(inst *session.Instance) {
	if inst == nil {
		return
	}
	h.notesEditing = true
	h.notesEditingSessionID = inst.ID
	h.notesEditor.SetValue(inst.Notes)
	h.notesEditor.Focus()
}

func (h *Home) stopNotesEditing() {
	h.notesEditing = false
	h.notesEditingSessionID = ""
	h.notesEditor.Blur()
}

func (h *Home) handleNotesEditorKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	selected := h.getSelectedSession()
	if selected == nil || selected.ID != h.notesEditingSessionID {
		h.stopNotesEditing()
		return h, nil
	}

	switch msg.String() {
	case "esc":
		h.stopNotesEditing()
		return h, nil
	case "ctrl+s":
		notes := strings.TrimRight(h.notesEditor.Value(), "\n")
		h.instancesMu.RLock()
		inst := h.instanceByID[h.notesEditingSessionID]
		h.instancesMu.RUnlock()
		if inst != nil {
			inst.Notes = notes
			h.forceSaveInstances()
		}
		h.stopNotesEditing()
		return h, nil
	}

	var cmd tea.Cmd
	h.notesEditor, cmd = h.notesEditor.Update(msg)
	return h, cmd
}

// overlayJumpHint places a badge-style hint label at the item name position.
func (h *Home) overlayJumpHint(line string, hint string, buffer string, itemName string) string {
	if hint == "" {
		return line
	}

	offset := findNameOffset(line, itemName)
	visibleLen := lipgloss.Width(line)
	if visibleLen < offset+len(hint) {
		return line
	}

	var hintRendered string
	for i, ch := range hint {
		s := string(ch)
		if i < len(buffer) {
			// Already typed: dimmed badge
			hintRendered += lipgloss.NewStyle().Foreground(ColorBg).Background(ColorComment).Bold(true).Render(s)
		} else {
			// Remaining: bright yellow badge
			hintRendered += lipgloss.NewStyle().Foreground(ColorBg).Background(ColorYellow).Bold(true).Render(s)
		}
	}

	return replaceVisibleRange(line, offset, len(hint), hintRendered)
}

// jumpItemName returns the display name for an item, used to locate hint badge position.
func jumpItemName(item session.Item) string {
	switch item.Type {
	case session.ItemTypeGroup:
		if item.Group != nil {
			return item.Group.Name
		}
	case session.ItemTypeSession:
		if item.Session != nil {
			return item.Session.Title
		}
	case session.ItemTypeWindow:
		return item.WindowName
	case session.ItemTypeRemoteGroup:
		return "remotes/" + item.RemoteName
	case session.ItemTypeRemoteSession:
		if item.RemoteSession != nil {
			return item.RemoteSession.Title
		}
	}
	return ""
}

// handleJumpKey processes key input during jump mode.
func (h *Home) handleJumpKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	switch {
	case key == "esc":
		h.jumpMode = false
		h.jumpBuffer = ""
		return h, nil

	case len(key) == 1 && key[0] >= 'a' && key[0] <= 'z':
		h.jumpBuffer += key
		hints := generateJumpHints(len(h.flatItems))
		result := matchJumpHint(hints, h.jumpBuffer)

		if result.matched {
			h.cursor = result.index
			h.syncViewport()
			h.jumpMode = false
			h.jumpBuffer = ""
			// For sessions/windows/remotes: attach directly.
			// For groups: just move cursor (user can press Enter to toggle).
			item := h.flatItems[result.index]
			if item.Type != session.ItemTypeGroup && item.Type != session.ItemTypeRemoteGroup {
				return h.handleMainKey(tea.KeyMsg{Type: tea.KeyEnter})
			}
			return h, nil
		}
		if !result.isPrefix {
			h.jumpMode = false
			h.jumpBuffer = ""
		}
		return h, nil

	default:
		// Non-letter key — exit jump mode, pass key through
		h.jumpMode = false
		h.jumpBuffer = ""
		return h.handleMainKey(msg)
	}
}

// hasModalVisible returns true if any modal dialog or overlay is currently visible
func (h *Home) hasModalVisible() bool {
	return h.initialLoading || h.isQuitting || h.notesEditing || h.jumpMode ||
		h.setupWizard.IsVisible() || h.settingsPanel.IsVisible() ||
		h.watcherPanel.IsVisible() || // hotkeyWatcherPanel overlay
		h.helpOverlay.IsVisible() || h.search.IsVisible() || h.globalSearch.IsVisible() ||
		h.newDialog.IsVisible() || h.groupDialog.IsVisible() || h.forkDialog.IsVisible() ||
		h.confirmDialog.IsVisible() || h.mcpDialog.IsVisible() || h.skillDialog.IsVisible() ||
		h.geminiModelDialog.IsVisible() || h.sessionPickerDialog.IsVisible() ||
		h.worktreeFinishDialog.IsVisible() || h.editPathsDialog.IsVisible()
}

// markNavigationAndFetchPreview sets navigation tracking state and returns a debounced preview command
func (h *Home) markNavigationAndFetchPreview() tea.Cmd {
	h.lastNavigationTime = time.Now()
	h.isNavigating = true
	return h.fetchSelectedPreview()
}

// clickedItemID returns a stable identifier for the item at the given flatItems index
func (h *Home) clickedItemID(index int) string {
	if index < 0 || index >= len(h.flatItems) {
		return ""
	}
	item := h.flatItems[index]
	if item.Type == session.ItemTypeSession && item.Session != nil {
		return "session:" + item.Session.ID
	}
	if item.Type == session.ItemTypeGroup {
		return "group:" + item.Path
	}
	return ""
}

// handleMouse handles mouse events (click to select, double-click to activate)
func (h *Home) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if h.hasModalVisible() {
		return h, nil
	}

	switch msg.Button {
	case tea.MouseButtonLeft:
		if msg.Action != tea.MouseActionPress {
			return h, nil
		}

		// Check if click is in the session list panel
		if h.getLayoutMode() == LayoutModeDual {
			leftWidth := int(float64(h.width) * 0.35)
			if msg.X >= leftWidth {
				return h, nil
			}
		}

		itemIndex := h.mouseYToItemIndex(msg.Y)
		if itemIndex < 0 || itemIndex >= len(h.flatItems) {
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
			if item.Type == session.ItemTypeSession && item.Session != nil {
				if h.hasActiveAnimation(item.Session.ID) {
					h.setError(fmt.Errorf("session is starting, please wait..."))
					return h, nil
				}
				if item.Session.Exists() {
					h.isAttaching.Store(true) // Prevent View() output during transition (atomic)
					return h, h.attachSession(item.Session)
				}
			} else if item.Type == session.ItemTypeGroup {
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

// getListContentStartY returns the Y coordinate where list items begin rendering
func (h *Home) getListContentStartY() int {
	// Header: 1 line, Filter bar: 1 line
	startY := 2
	if h.updateInfo != nil && h.updateInfo.Available {
		startY++ // Update banner
	}
	if h.maintenanceMsg != "" {
		startY++ // Maintenance banner
	}
	// Panel title: 2 lines (title + underline)
	startY += 2
	return startY
}

// mouseYToItemIndex maps a mouse Y coordinate to a flatItems index, or -1 if not on an item
func (h *Home) mouseYToItemIndex(y int) int {
	if len(h.flatItems) == 0 {
		return -1
	}

	lineInList := y - h.getListContentStartY()
	if lineInList < 0 {
		return -1
	}

	// Account for "more above" indicator
	if h.viewOffset > 0 {
		if lineInList == 0 {
			return -1 // Clicked on the "more above" indicator itself
		}
		lineInList-- // Shift down past the indicator
	}

	// Reject clicks beyond the visible list area (e.g. in the preview section of stacked layout)
	// When scrolled, the "more above" indicator takes 1 render line, reducing visible items by 1.
	maxVisible := h.getVisibleHeight()
	if h.viewOffset > 0 {
		maxVisible--
	}
	if lineInList >= maxVisible {
		return -1
	}

	itemIndex := h.viewOffset + lineInList
	if itemIndex >= len(h.flatItems) {
		return -1
	}
	return itemIndex
}

// handleMainKey handles keys in main view
func (h *Home) handleMainKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	raw := msg.String()
	key := h.normalizeMainKey(raw)
	uiLog.Info("keypress", "raw", raw, "normalized", key, "type", msg.Type, "runes", string(msg.Runes))
	if key == "" {
		return h, nil
	}

	switch key {
	case "q", "ctrl+c":
		return h.tryQuit()

	case "esc":
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

	case "up", "k":
		if h.cursor > 0 {
			h.cursor--
			h.previewScrollOffset = 0
			h.syncViewport()
			h.markNavigationActivity()
			// PERFORMANCE: Debounced preview fetch - waits 150ms for navigation to settle
			// This prevents spawning tmux subprocess on every keystroke
			return h, h.fetchSelectedPreview()
		}
		return h, nil

	case "down", "j":
		if h.cursor < len(h.flatItems)-1 {
			h.cursor++
			h.previewScrollOffset = 0
			h.syncViewport()
			h.markNavigationActivity()
			// PERFORMANCE: Debounced preview fetch - waits 150ms for navigation to settle
			// This prevents spawning tmux subprocess on every keystroke
			return h, h.fetchSelectedPreview()
		}
		return h, nil

		// Vi-style pagination (#38) - half/full page scrolling
	case "ctrl+u": // Half page up
		pageSize := h.getVisibleHeight() / 2
		if pageSize < 1 {
			pageSize = 1
		}
		h.cursor -= pageSize
		if h.cursor < 0 {
			h.cursor = 0
		}
		h.previewScrollOffset = 0
		h.syncViewport()
		h.markNavigationActivity()
		return h, h.fetchSelectedPreview()

	case "ctrl+d": // Half page down
		pageSize := h.getVisibleHeight() / 2
		if pageSize < 1 {
			pageSize = 1
		}
		h.cursor += pageSize
		if h.cursor >= len(h.flatItems) {
			h.cursor = len(h.flatItems) - 1
		}
		if h.cursor < 0 {
			h.cursor = 0
		}
		h.previewScrollOffset = 0
		h.syncViewport()
		h.markNavigationActivity()
		return h, h.fetchSelectedPreview()

	case "ctrl+b": // Full page up (backward)
		pageSize := h.getVisibleHeight()
		if pageSize < 1 {
			pageSize = 1
		}
		h.cursor -= pageSize
		if h.cursor < 0 {
			h.cursor = 0
		}
		h.previewScrollOffset = 0
		h.syncViewport()
		h.markNavigationActivity()
		return h, h.fetchSelectedPreview()

	case "ctrl+f": // Full page down (forward)
		pageSize := h.getVisibleHeight()
		if pageSize < 1 {
			pageSize = 1
		}
		h.cursor += pageSize
		if h.cursor >= len(h.flatItems) {
			h.cursor = len(h.flatItems) - 1
		}
		if h.cursor < 0 {
			h.cursor = 0
		}
		h.previewScrollOffset = 0
		h.syncViewport()
		h.markNavigationActivity()
		return h, h.fetchSelectedPreview()

	case "G": // Open global search (fall back to local search if index not available)
		if h.globalSearchIndex != nil {
			h.globalSearch.SetSize(h.width, h.height)
			h.globalSearch.Show()
		} else {
			h.search.Show()
		}
		return h, nil

	case "enter":
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeSession && item.Session != nil {
				if item.Session.Exists() {
					// Pane dead (process exited) — restart instead of attaching to dead pane.
					tmuxSess := item.Session.GetTmuxSession()
					if tmuxSess != nil && tmuxSess.IsPaneDead() {
						if !h.hasActiveAnimation(item.Session.ID) {
							h.resumingSessions[item.Session.ID] = time.Now()
							return h, h.restartSession(item.Session)
						}
						return h, nil
					}
					return h, h.attachSession(item.Session)
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
			} else if item.Type == session.ItemTypeWindow {
				// Find parent session by WindowSessionID
				var parentInst *session.Instance
				h.instancesMu.RLock()
				for _, inst := range h.instances {
					if inst.ID == item.WindowSessionID {
						parentInst = inst
						break
					}
				}
				h.instancesMu.RUnlock()

				if parentInst != nil && parentInst.Exists() {
					tmuxSess := parentInst.GetTmuxSession()
					if tmuxSess != nil {
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
						return h, tea.Exec(attachWindowCmd{session: tmuxSess, windowIndex: item.WindowIndex, detachByte: h.detachByte()}, func(err error) tea.Msg {
							h.isAttaching.Store(false)
							parentInst.MarkAccessed()
							return statusUpdateMsg{}
						})
					}
				}
			} else if item.Type == session.ItemTypeRemoteSession && item.RemoteSession != nil {
				// Attach to remote session via SSH
				return h, h.attachRemoteSession(item.RemoteName, item.RemoteSession.ID)
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
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			collapsed := false
			if item.Type == session.ItemTypeGroup {
				groupPath := item.Path
				h.groupTree.CollapseGroup(groupPath)
				h.rebuildFlatItems()
				for i, fi := range h.flatItems {
					if fi.Type == session.ItemTypeGroup && fi.Path == groupPath {
						h.cursor = i
						break
					}
				}
				collapsed = true
			} else if item.Type == session.ItemTypeWindow {
				// Collapse parent session's windows and jump to it
				sid := item.WindowSessionID
				h.windowsCollapsed[sid] = true
				h.rebuildFlatItems()
				h.moveCursorToSession(sid)
				collapsed = false // no group state to save
			} else if item.Type == session.ItemTypeSession && h.sessionHasWindows(item) && !h.windowsCollapsed[item.Session.ID] {
				// Collapse this session's windows
				h.windowsCollapsed[item.Session.ID] = true
				h.rebuildFlatItems()
				collapsed = false
			} else if item.Type == session.ItemTypeSession {
				// Move cursor to parent group
				h.groupTree.CollapseGroup(item.Path)
				h.rebuildFlatItems()
				for i, fi := range h.flatItems {
					if fi.Type == session.ItemTypeGroup && fi.Path == item.Path {
						h.cursor = i
						break
					}
				}
				collapsed = true
			}
			if collapsed {
				h.saveGroupState()
			}
		}
		return h, nil

	case "shift+up", "K":
		// Move item up
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			switch item.Type {
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
			h.saveInstances()
		}
		return h, nil

	case "shift+down", "J":
		// Move item down
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			switch item.Type {
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
			h.saveInstances()
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

	case "m":
		// MCP Manager - for Claude and Gemini sessions
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeSession && item.Session != nil &&
				(session.IsClaudeCompatible(item.Session.Tool) || item.Session.Tool == "gemini") {
				h.mcpDialog.SetSize(h.width, h.height)
				if err := h.mcpDialog.Show(item.Session.ProjectPath, item.Session.ID, item.Session.Tool); err != nil {
					h.setError(err)
				}
			}
		}
		return h, nil

	case "f":
		// Quick fork session (same title with " (fork)" suffix)
		// Only available when session has a valid Claude session ID
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeSession && item.Session != nil {
				// Block fork during animations to prevent concurrent operations
				if h.hasActiveAnimation(item.Session.ID) {
					h.setError(fmt.Errorf("session is starting, please wait..."))
					return h, nil
				}
				if item.Session.CanFork() {
					return h, h.quickForkSession(item.Session)
				}
			}
		}
		return h, nil

	case "F", "shift+f":
		// Fork with dialog (customize title and group)
		// Only available when session has a valid Claude session ID
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeSession && item.Session != nil {
				// Block fork during animations to prevent concurrent operations
				if h.hasActiveAnimation(item.Session.ID) {
					h.setError(fmt.Errorf("session is starting, please wait..."))
					return h, nil
				}
				if item.Session.CanFork() {
					return h, h.forkSessionWithDialog(item.Session)
				}
			}
		}
		return h, nil

	case "s":
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
			if item.Type == session.ItemTypeSession {
				h.groupDialog.ShowMove(h.scopedGroupPaths())
			}
		}
		return h, nil

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
		// Vi-style gg to jump to top (#38) - check for double-tap first
		if time.Since(h.lastGTime) < 500*time.Millisecond {
			// Double g - jump to top
			if len(h.flatItems) > 0 {
				h.cursor = 0
				h.syncViewport()
				h.markNavigationActivity()
				return h, h.fetchSelectedPreview()
			}
			return h, nil
		}
		// Record time for potential gg detection
		h.lastGTime = time.Now()

		// Create new group with context-aware Tab toggle (Issue #111):
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
			if item.Type == session.ItemTypeGroup {
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
		// Open global search first if available, otherwise local search
		if h.globalSearchIndex != nil {
			h.globalSearch.SetSize(h.width, h.height)
			h.globalSearch.Show()
		} else {
			h.search.Show()
		}
		return h, nil

	case "?":
		h.helpOverlay.SetSize(h.width, h.height)
		h.helpOverlay.Show()
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

	case "E":
		// Exec an interactive shell inside the sandbox container.
		if selected := h.getSelectedSession(); selected != nil && selected.IsSandboxed() &&
			selected.SandboxContainer != "" {
			tmuxName, shellErr := selected.OpenContainerShell()
			if shellErr != nil {
				h.setError(shellErr)
				return h, nil
			}
			termSession := &tmux.Session{Name: tmuxName}
			h.isAttaching.Store(true)
			return h, tea.Exec(attachCmd{session: termSession, detachByte: h.detachByte()}, func(err error) tea.Msg {
				h.isAttaching.Store(false)
				return statusUpdateMsg{}
			})
		}
		return h, nil

	case "n":
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

		// Apply user's preferred default tool from config
		h.newDialog.SetDefaultTool(session.GetDefaultTool())

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

	case "d":
		// Show confirmation dialog before deletion (prevents accidental deletion)
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeSession && item.Session != nil {
				h.confirmDialog.ShowDeleteSession(item.Session.ID, item.Session.Title, item.Session.IsSandboxed(), item.Session.IsWorktree())
			} else if item.Type == session.ItemTypeRemoteSession && item.RemoteSession != nil {
				h.confirmDialog.ShowDeleteRemoteSession(item.RemoteName, item.RemoteSession.ID, item.RemoteSession.Title)
			} else if item.Type == session.ItemTypeGroup && item.Path != session.DefaultGroupPath && item.Path != h.groupScope {
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

	case "i":
		return h, h.importSessions

	case "u":
		// Mark session as unread (idle → waiting)
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeSession && item.Session != nil {
				tmuxSess := item.Session.GetTmuxSession()
				if tmuxSess != nil {
					tmuxSess.ResetAcknowledged()
					// Persist to SQLite so background sync doesn't overwrite
					if db := statedb.GetGlobal(); db != nil {
						_ = db.SetAcknowledged(item.Session.ID, false)
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
		// Quick approve: send "1" + Enter to the highlighted Claude session
		// without attaching. Gated to Claude-compatible tools so a stray press
		// on a vim/shell session cannot dump a "1" into the buffer. No status
		// guard - Bash-tool permission prompts in Claude Code don't transition
		// the session to StatusWaiting, so guarding on it would defeat the
		// most common use case. Matches the existing "agent-deck session send"
		// CLI, which has no status guard either.
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeSession && item.Session != nil &&
				session.IsClaudeCompatible(item.Session.Tool) {
				if tmuxSess := item.Session.GetTmuxSession(); tmuxSess != nil {
					_ = tmuxSess.SendKeysAndEnter("1")
				}
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
		entry := h.undoStack[len(h.undoStack)-1]
		h.undoStack = h.undoStack[:len(h.undoStack)-1]
		inst := entry.instance
		return h, func() tea.Msg {
			err := inst.Restart()
			return sessionRestoredMsg{
				instance: inst,
				err:      err,
				warning:  inst.ConsumeCodexRestartWarning(),
			}
		}

	case "ctrl+r":
		// Manual refresh (useful if watcher fails or for user preference)
		state := h.preserveState()

		cmd := func() tea.Msg {
			instances, groups, err := h.storage.LoadWithGroups()
			return loadSessionsMsg{
				instances:    instances,
				groups:       groups,
				err:          err,
				restoreState: &state,
			}
		}

		return h, cmd

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
		h.statusFilter = ""
		h.rebuildFlatItems()
		return h, nil

	case "!", "shift+1":
		// Filter to running sessions only
		if h.statusFilter == session.StatusRunning {
			h.statusFilter = "" // Toggle off
		} else {
			h.statusFilter = session.StatusRunning
		}
		h.rebuildFlatItems()
		return h, nil

	case "@", "shift+2":
		// Filter to waiting sessions only
		if h.statusFilter == session.StatusWaiting {
			h.statusFilter = "" // Toggle off
		} else {
			h.statusFilter = session.StatusWaiting
		}
		h.rebuildFlatItems()
		return h, nil

	case "#", "shift+3":
		// Filter to idle sessions only
		if h.statusFilter == session.StatusIdle {
			h.statusFilter = "" // Toggle off
		} else {
			h.statusFilter = session.StatusIdle
		}
		h.rebuildFlatItems()
		return h, nil

	case "$", "shift+4":
		// Cost dashboard (when cost tracking is active), otherwise filter to error sessions
		if h.costStore != nil {
			h.showCostDashboard = true
			h.costDashboard = newCostDashboard(h.costStore, h.width, h.height)
			return h, nil
		}
		// Fallback: filter to error sessions only
		if h.statusFilter == session.StatusError {
			h.statusFilter = "" // Toggle off
		} else {
			h.statusFilter = session.StatusError
		}
		h.rebuildFlatItems()
		return h, nil

	case FilterKeyActive, "shift+5":
		// Filter to open sessions (excludes error/stopped)
		if h.statusFilter == FilterModeActive {
			h.statusFilter = "" // Toggle off
		} else {
			h.statusFilter = FilterModeActive
		}
		h.rebuildFlatItems()
		return h, nil
	}

	return h, nil
}

// handleConfirmDialogKey handles keys when confirmation dialog is visible
func (h *Home) handleConfirmDialogKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Let the dialog handle arrow/tab navigation first.
	h.confirmDialog.Update(msg)

	switch h.confirmDialog.GetConfirmType() {
	case ConfirmQuitWithPool:
		switch msg.String() {
		case "k", "K":
			h.confirmDialog.Hide()
			h.isQuitting = true
			return h, h.performQuit(false)
		case "s", "S":
			h.confirmDialog.Hide()
			h.isQuitting = true
			return h, h.performQuit(true)
		case "enter":
			// Activate focused button: 0=keep, 1=shutdown
			shutdown := h.confirmDialog.GetFocusedButton() == 1
			h.confirmDialog.Hide()
			h.isQuitting = true
			return h, h.performQuit(shutdown)
		case "esc":
			h.confirmDialog.Hide()
			h.isQuitting = false
			return h, nil
		}
		return h, nil

	case ConfirmCreateDirectory:
		switch msg.String() {
		case "y", "Y":
			return h, h.confirmCreateDirectory()
		case "enter":
			if h.confirmDialog.GetFocusedButton() == 0 {
				return h, h.confirmCreateDirectory()
			}
			h.confirmDialog.Hide()
			return h, nil
		case "n", "N", "esc":
			h.confirmDialog.Hide()
			return h, nil
		}
		return h, nil

	case ConfirmInstallHooks:
		switch msg.String() {
		case "y", "Y":
			return h, h.confirmInstallHooks()
		case "enter":
			if h.confirmDialog.GetFocusedButton() == 0 {
				return h, h.confirmInstallHooks()
			}
			return h, h.declineInstallHooks()
		case "n", "N", "esc":
			return h, h.declineInstallHooks()
		}
		return h, nil

	default:
		// Handle delete/close confirmations (session/group/remote)
		switch msg.String() {
		case "y", "Y":
			return h, h.confirmAction()
		case "enter":
			if h.confirmDialog.GetFocusedButton() == 0 {
				return h, h.confirmAction()
			}
			h.confirmDialog.Hide()
			return h, nil
		case "n", "N", "esc":
			h.confirmDialog.Hide()
			return h, nil
		}
	}

	return h, nil
}

// confirmAction executes the confirmed destructive action.
func (h *Home) confirmAction() tea.Cmd {
	switch h.confirmDialog.GetConfirmType() {
	case ConfirmDeleteSession:
		sessionID := h.confirmDialog.GetTargetID()
		if inst := h.getInstanceByID(sessionID); inst != nil {
			h.confirmDialog.Hide()
			return h.deleteSession(inst)
		}
	case ConfirmCloseSession:
		sessionID := h.confirmDialog.GetTargetID()
		if inst := h.getInstanceByID(sessionID); inst != nil {
			h.confirmDialog.Hide()
			return h.closeSession(inst)
		}
	case ConfirmDeleteGroup:
		groupPath := h.confirmDialog.GetTargetID()
		h.groupTree.DeleteGroup(groupPath)
		h.instancesMu.Lock()
		h.instances = h.groupTree.GetAllInstances()
		h.instancesMu.Unlock()
		h.rebuildFlatItems()
		h.saveInstances()
	case ConfirmDeleteRemoteSession:
		sessionID := h.confirmDialog.GetTargetID()
		remoteName := h.confirmDialog.GetRemoteName()
		title := h.confirmDialog.targetName
		h.confirmDialog.Hide()
		return h.deleteRemoteSession(remoteName, sessionID, title)
	case ConfirmCloseRemoteSession:
		sessionID := h.confirmDialog.GetTargetID()
		remoteName := h.confirmDialog.GetRemoteName()
		title := h.confirmDialog.targetName
		h.confirmDialog.Hide()
		return h.closeRemoteSession(remoteName, sessionID, title)
	}
	h.confirmDialog.Hide()
	return nil
}

// confirmCreateDirectory handles the "yes" action for ConfirmCreateDirectory.
func (h *Home) confirmCreateDirectory() tea.Cmd {
	name, path, command, groupPath, pendingToolOpts, pendingExtraArgs, parentSessionID, parentProjectPath := h.confirmDialog.GetPendingSession()
	h.confirmDialog.Hide()
	if err := os.MkdirAll(path, 0o755); err != nil {
		h.setError(fmt.Errorf("failed to create directory: %w", err))
		return nil
	}
	return h.createSessionInGroupWithWorktreeAndOptions(
		name,
		path,
		command,
		groupPath,
		"",
		"",
		"",
		false,
		false,
		pendingToolOpts,
		pendingExtraArgs,
		false,
		nil,
		parentSessionID,
		parentProjectPath,
		"", // no placeholder — non-worktree sessions are fast
	)
}

// confirmInstallHooks handles the "yes" action for ConfirmInstallHooks.
func (h *Home) confirmInstallHooks() tea.Cmd {
	h.confirmDialog.Hide()
	h.pendingHooksPrompt = false
	configDir := session.GetClaudeConfigDir()
	if _, err := session.InjectClaudeHooks(configDir); err != nil {
		uiLog.Warn("hook_install_failed", slog.String("error", err.Error()))
	} else {
		uiLog.Info("claude_hooks_installed", slog.String("config_dir", configDir))
	}
	// Start the status file watcher
	hookWatcher, err := session.NewStatusFileWatcher(nil)
	if err != nil {
		uiLog.Warn("hook_watcher_init_failed", slog.String("error", err.Error()))
	} else {
		h.hookWatcher = hookWatcher
		go hookWatcher.Start()
	}
	// Remember user's choice
	if db := statedb.GetGlobal(); db != nil {
		_ = db.SetMeta("hooks_prompted", "accepted")
	}
	return nil
}

// declineInstallHooks handles the "no" action for ConfirmInstallHooks.
func (h *Home) declineInstallHooks() tea.Cmd {
	h.confirmDialog.Hide()
	h.pendingHooksPrompt = false
	// Remember user declined
	if db := statedb.GetGlobal(); db != nil {
		_ = db.SetMeta("hooks_prompted", "declined")
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
	return func() tea.Msg {
		// Stop system stats collector
		if h.sysStatsCollector != nil {
			h.sysStatsCollector.Stop()
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
		// Close storage watcher
		if h.storageWatcher != nil {
			h.storageWatcher.Close()
		}
		// Close theme watcher
		h.stopThemeWatcher()
		// Close global search index
		if h.globalSearchIndex != nil {
			h.globalSearchIndex.Close()
		}
		// Stop watcher engine (D-07: lifecycle tied to TUI)
		if h.watcherEngine != nil {
			h.watcherEngine.Stop()
			h.watcherEngine = nil
		}
		// Shutdown or disconnect from MCP pool based on user choice
		if err := session.ShutdownGlobalPool(shutdownPool); err != nil {
			mcpUILog.Warn("pool_shutdown_error", slog.String("error", err.Error()))
		}
		// Release primary claim and unregister from the heartbeat table
		if db := statedb.GetGlobal(); db != nil {
			_ = db.ResignPrimary()
			_ = db.UnregisterInstance()
		}
		// Clean up notification bar (clear tmux status bars and unbind keys)
		h.cleanupNotifications()
		// Save UI state (cursor, preview mode, filter) before saving instances
		h.saveUIState()
		// Save both instances AND groups on quit (critical fix: was losing groups!)
		h.saveInstances()

		return tea.Quit()
	}
}

// refreshWatcherPanel loads watcher and event data from statedb and updates the panel.
// Safe to call when watcherPanel is hidden; data is preloaded for when the panel opens.
func (h *Home) refreshWatcherPanel() {
	db := statedb.GetGlobal()
	if db == nil {
		return
	}

	watchers, err := db.LoadWatchers()
	if err != nil {
		return
	}

	items := make([]WatcherDisplayItem, len(watchers))
	for i, w := range watchers {
		healthStatus := w.Status // default: same as status
		if w.Status == "running" {
			healthStatus = "healthy"
		}
		items[i] = WatcherDisplayItem{
			ID:           w.ID,
			Name:         w.Name,
			Type:         w.Type,
			Status:       w.Status,
			HealthStatus: healthStatus,
			Conductor:    w.Conductor,
		}

		// Count events in the last hour to compute events-per-hour rate.
		events, evtErr := db.LoadWatcherEvents(w.ID, 100)
		if evtErr == nil {
			cutoff := time.Now().Add(-time.Hour)
			count := 0
			for _, e := range events {
				if e.CreatedAt.After(cutoff) {
					count++
				}
			}
			items[i].EventsPerHour = float64(count)
		}
	}
	h.watcherPanel.SetWatchers(items)

	// If a watcher is selected in detail mode, load its recent events.
	if sel := h.watcherPanel.SelectedWatcher(); sel != nil {
		events, evtErr := db.LoadWatcherEvents(sel.ID, 10)
		if evtErr == nil {
			displayEvents := make([]WatcherEventDisplay, len(events))
			for i, e := range events {
				displayEvents[i] = WatcherEventDisplay{
					Timestamp: e.CreatedAt,
					Sender:    e.Sender,
					Subject:   e.Subject,
					RoutedTo:  e.RoutedTo,
					SessionID: e.SessionID,
				}
			}
			h.watcherPanel.SetEvents(displayEvents)
		}
	}
}

// dispatchHealthAlert sends a health alert message to the conductor session associated
// with the watcher that entered warning or error state (D-22, D-23, TUI-03).
func (h *Home) dispatchHealthAlert(state watcher.HealthState) {
	db := statedb.GetGlobal()
	if db == nil {
		return
	}

	watchers, err := db.LoadWatchers()
	if err != nil {
		return
	}

	var conductorName string
	for _, w := range watchers {
		if w.Name == state.WatcherName && w.Conductor != "" {
			conductorName = w.Conductor
			break
		}
	}
	if conductorName == "" {
		return // No conductor configured, skip alert.
	}

	// Build alert message (D-23): include name, status, reason, and suggested action.
	alertMsg := fmt.Sprintf("[WATCHER HEALTH ALERT] Watcher %q transitioned to %s: %s. Suggested action: check watcher configuration and source connectivity.",
		state.WatcherName, state.Status, state.Message)

	// Find the conductor session by title and deliver via tmux send-keys (T-16-08).
	sessionTitle := session.ConductorSessionTitle(conductorName)
	h.instancesMu.RLock()
	instances := h.instances
	h.instancesMu.RUnlock()
	for _, inst := range instances {
		if inst.Title == sessionTitle {
			ts := inst.GetTmuxSession()
			if ts != nil && ts.Name != "" {
				tmuxName := ts.Name
				go func() {
					_ = exec.Command("tmux", "send-keys", "-t", tmuxName, alertMsg, "Enter").Run()
				}()
			}
			break
		}
	}
}

// handleMCPDialogKey handles keys when MCP dialog is visible
func (h *Home) handleMCPDialogKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		// DEBUG: Log entry point
		mcpUILog.Debug("dialog_enter_pressed")

		// Apply changes and close dialog
		hasChanged := h.mcpDialog.HasChanged()
		mcpUILog.Debug("dialog_has_changed", slog.Bool("changed", hasChanged))

		if hasChanged {
			// Apply changes (saves state + writes .mcp.json)
			if err := h.mcpDialog.Apply(); err != nil {
				mcpUILog.Debug("dialog_apply_failed", slog.String("error", err.Error()))
				h.setError(err)
				h.mcpDialog.Hide() // Hide dialog even on error
				return h, nil
			}
			mcpUILog.Debug("dialog_apply_succeeded")

			// Find the session by ID (stored when dialog opened - same as Shift+S uses)
			sessionID := h.mcpDialog.GetSessionID()
			mcpUILog.Debug("dialog_looking_for_session", slog.String("session_id", sessionID))

			// O(1) lookup - no lock needed as Update() runs on main goroutine
			targetInst := h.getInstanceByID(sessionID)
			if targetInst != nil {
				mcpUILog.Debug(
					"dialog_session_found",
					slog.String("session_id", targetInst.ID),
					slog.String("title", targetInst.Title),
				)
			}

			if targetInst != nil {
				mcpUILog.Debug("dialog_restarting_session", slog.String("session_id", targetInst.ID))
				// Track as MCP loading for animation in preview pane
				h.mcpLoadingSessions[targetInst.ID] = time.Now()
				// Set flag to skip MCP regeneration (Apply just wrote the config)
				targetInst.SkipMCPRegenerate = true
				// Restart the session to apply MCP changes
				h.mcpDialog.Hide()
				return h, h.restartSession(targetInst)
			} else {
				mcpUILog.Debug("dialog_session_not_found", slog.String("session_id", sessionID))
			}
		}
		mcpUILog.Debug("dialog_hiding_without_restart")
		h.mcpDialog.Hide()
		return h, nil

	case "esc":
		h.mcpDialog.Hide()
		return h, nil

	default:
		h.mcpDialog.Update(msg)
		return h, nil
	}
}

// handleEditPathsDialogKey handles key events for the edit paths dialog.
func (h *Home) handleEditPathsDialogKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		if h.editPathsDialog.IsEditing() {
			h.editPathsDialog.Update(msg)
			return h, nil
		}
		// Confirm changes
		if h.editPathsDialog.HasChanged() {
			if errMsg := h.editPathsDialog.Validate(); errMsg != "" {
				h.editPathsDialog.validationErr = errMsg
				return h, nil
			}
			newPaths := h.editPathsDialog.GetPaths()
			sessionID := h.editPathsDialog.GetSessionID()
			h.editPathsDialog.Hide()
			inst := h.getInstanceByID(sessionID)
			if inst != nil {
				return h, h.applyMultiRepoPathChanges(inst, newPaths)
			}
		}
		h.editPathsDialog.Hide()
		return h, nil
	case "esc":
		if h.editPathsDialog.IsEditing() {
			h.editPathsDialog.Update(msg)
			return h, nil
		}
		h.editPathsDialog.Hide()
		return h, nil
	default:
		h.editPathsDialog.Update(msg)
		return h, nil
	}
}

// applyMultiRepoPathChanges updates the symlink directory and restarts the session.
func (h *Home) applyMultiRepoPathChanges(inst *session.Instance, newPaths []string) tea.Cmd {
	id := inst.ID
	return func() tea.Msg {
		h.instancesMu.RLock()
		current := h.instanceByID[id]
		h.instancesMu.RUnlock()
		if current == nil {
			return sessionRestartedMsg{sessionID: id, err: fmt.Errorf("session no longer exists")}
		}

		tempDir := current.MultiRepoTempDir
		if tempDir == "" {
			return sessionRestartedMsg{sessionID: id, err: fmt.Errorf("no multi-repo temp dir")}
		}

		// Remove all existing symlinks/entries in tempDir
		entries, _ := os.ReadDir(tempDir)
		for _, entry := range entries {
			_ = os.RemoveAll(filepath.Join(tempDir, entry.Name()))
		}

		// Create new symlinks
		dirnames := session.DeduplicateDirnames(newPaths)
		var newProjectPath string
		var newAdditionalPaths []string
		for i, p := range newPaths {
			linkPath := filepath.Join(tempDir, dirnames[i])
			_ = os.Symlink(p, linkPath)
			if i == 0 {
				newProjectPath = linkPath
			} else {
				newAdditionalPaths = append(newAdditionalPaths, linkPath)
			}
		}

		// Update instance fields under write lock to avoid races with
		// the background status worker that reads via instanceByID.
		h.instancesMu.Lock()
		current.ProjectPath = newProjectPath
		current.AdditionalPaths = newAdditionalPaths
		if current.GetTmuxSession() != nil {
			current.GetTmuxSession().WorkDir = tempDir
		}
		h.instancesMu.Unlock()

		h.saveInstances()

		err := current.Restart()
		return sessionRestartedMsg{sessionID: id, err: err}
	}
}

// handleSkillDialogKey handles keys when Skills dialog is visible
func (h *Home) handleSkillDialogKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		needsApply := h.skillDialog.NeedsApply()
		if needsApply {
			if err := h.skillDialog.Apply(); err != nil {
				h.setError(err)
				h.skillDialog.Hide()
				return h, nil
			}

			sessionID := h.skillDialog.GetSessionID()
			targetInst := h.getInstanceByID(sessionID)
			if targetInst != nil && session.ShouldRestartProjectSkills(targetInst.Tool) {
				h.skillDialog.Hide()
				return h, h.restartSession(targetInst)
			}
		}
		h.skillDialog.Hide()
		return h, nil

	case "esc":
		h.skillDialog.Hide()
		return h, nil

	default:
		h.skillDialog.Update(msg)
		return h, nil
	}
}

// handleGroupDialogKey handles keys when group dialog is visible
func (h *Home) handleGroupDialogKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		// Validate before proceeding
		if validationErr := h.groupDialog.Validate(); validationErr != "" {
			h.groupDialog.SetError(validationErr)
			return h, nil
		}
		h.clearError() // Clear any previous validation error

		switch h.groupDialog.Mode() {
		case GroupDialogCreate:
			name := h.groupDialog.GetValue()
			if name != "" {
				if h.groupDialog.HasParent() {
					// Create subgroup under parent
					parentPath := h.groupDialog.GetParentPath()
					h.groupTree.CreateSubgroup(parentPath, name)
				} else {
					// Create root-level group
					h.groupTree.CreateGroup(name)
				}
				h.rebuildFlatItems()
				h.saveInstances() // Persist the new group
			}
		case GroupDialogRename:
			name := h.groupDialog.GetValue()
			if name != "" {
				h.groupTree.RenameGroup(h.groupDialog.GetGroupPath(), name)
				h.instancesMu.Lock()
				h.instances = h.groupTree.GetAllInstances()
				h.instancesMu.Unlock()
				h.rebuildFlatItems()
				h.saveInstances()
			}
		case GroupDialogMove:
			targetGroupPath := h.groupDialog.GetSelectedGroup()
			if targetGroupPath != "" && h.cursor < len(h.flatItems) {
				item := h.flatItems[h.cursor]
				if item.Type == session.ItemTypeSession {
					h.groupTree.MoveSessionToGroup(item.Session, targetGroupPath)
					h.instancesMu.Lock()
					h.instances = h.groupTree.GetAllInstances()
					h.instancesMu.Unlock()
					h.rebuildFlatItems()
					h.saveInstances()
				}
			}
		case GroupDialogRenameSession:
			newName := h.groupDialog.GetValue()
			if newName != "" {
				sessionID := h.groupDialog.GetSessionID()

				// Handle remote session rename
				if strings.HasPrefix(sessionID, "remote:") {
					parts := strings.SplitN(sessionID, ":", 3) // "remote", remoteName, actualID
					if len(parts) == 3 {
						remoteName, remoteID := parts[1], parts[2]
						go func() {
							config, err := session.LoadUserConfig()
							if err != nil || config == nil || config.Remotes == nil {
								return
							}
							rc, ok := config.Remotes[remoteName]
							if !ok {
								return
							}
							runner := session.NewSSHRunner(remoteName, rc)
							ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
							defer cancel()
							_, _ = runner.RunCommand(ctx, "rename", remoteID, newName)
						}()
						// Update local cache immediately for responsiveness
						h.remoteSessionsMu.Lock()
						if sessions, ok := h.remoteSessions[remoteName]; ok {
							for i := range sessions {
								if sessions[i].ID == parts[2] {
									sessions[i].Title = newName
									break
								}
							}
						}
						h.remoteSessionsMu.Unlock()
						h.rebuildFlatItems()
					}
				} else {
					// Local session rename
					// Find and rename the session (O(1) lookup)
					if inst := h.getInstanceByID(sessionID); inst != nil {
						inst.Title = newName
						inst.SyncTmuxDisplayName()
					}
					// Store pending title change so it survives reload races.
					// If saveInstances() is skipped (isReloading=true), the reload
					// replaces h.instances from disk, losing the in-memory rename.
					// loadSessionsMsg re-applies pending changes after reload.
					h.pendingTitleChanges[sessionID] = newName
					// Invalidate preview cache since title changed
					h.invalidatePreviewCache(sessionID)
					h.rebuildFlatItems()
					h.saveInstances()
				}
			}
		}
		h.groupDialog.Hide()
		return h, nil
	case "esc":
		h.groupDialog.Hide()
		h.clearError() // Clear any validation error
		return h, nil
	}

	var cmd tea.Cmd
	h.groupDialog, cmd = h.groupDialog.Update(msg)
	return h, cmd
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
				if worktreeEnabled && branchName != "" {
					if !git.IsGitRepo(source.ProjectPath) {
						h.forkDialog.SetError("Path is not a git repository")
						return h, nil
					}
					repoRoot, err := git.GetWorktreeBaseRoot(source.ProjectPath)
					if err != nil {
						h.forkDialog.SetError(fmt.Sprintf("Failed to get repo root: %v", err))
						return h, nil
					}

					wtSettings := session.GetWorktreeSettings()
					worktreePath := git.WorktreePath(git.WorktreePathOptions{
						Branch:    branchName,
						Location:  wtSettings.DefaultLocation,
						RepoDir:   repoRoot,
						SessionID: git.GeneratePathID(),
						Template:  wtSettings.Template(),
					})
					if opts == nil {
						opts = &session.ClaudeOptions{}
					}

					opts.WorkDir = worktreePath
					opts.WorktreePath = worktreePath
					opts.WorktreeRepoRoot = repoRoot
					opts.WorktreeBranch = branchName
				}

				parentID := h.forkDialog.GetParentSessionID()
				parentPath := h.forkDialog.GetParentProjectPath()
				h.forkDialog.Hide()
				return h, h.forkSessionCmdWithOptions(source, title, groupPath, opts, h.forkDialog.IsSandboxEnabled(), parentID, parentPath)
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

// saveInstances saves instances to storage
func (h *Home) saveInstances() {
	h.saveInstancesWithForce(false)
}

// forceSaveInstances saves instances regardless of isReloading flag.
// Use this for critical updates that MUST persist (e.g., OpenCode detection results)
// that would otherwise be lost due to race conditions with storage watcher reloads.
func (h *Home) forceSaveInstances() {
	h.saveInstancesWithForce(true)
}

// saveInstancesWithForce is the internal save implementation.
// force=true bypasses the isReloading check for critical updates.
func (h *Home) saveInstancesWithForce(force bool) {
	// Skip saving during reload to avoid overwriting external changes (CLI)
	// Unless force=true for critical updates like detection results
	h.reloadMu.Lock()
	reloading := h.isReloading
	h.reloadMu.Unlock()

	if reloading && !force {
		uiLog.Debug("save_skip_during_reload", slog.Bool("force", force))
		return
	}
	if force && reloading {
		uiLog.Debug("save_force_during_reload")
	}

	// EXTERNAL CHANGE DETECTION: Check if file was modified since we last loaded.
	// This catches external changes (e.g., from CLI) even when fsnotify fails
	// (common on 9p/NFS filesystems in WSL2).
	// NOTE: Skip this check when force=true because critical saves MUST happen
	// (e.g., new session creation, fork, delete - these would lose data if skipped)
	if !force {
		h.reloadMu.Lock()
		ourLoadMtime := h.lastLoadMtime
		h.reloadMu.Unlock()

		if h.storage != nil && !ourLoadMtime.IsZero() {
			currentMtime, err := h.storage.GetFileMtime()
			if err == nil && !currentMtime.IsZero() && currentMtime.After(ourLoadMtime) {
				uiLog.Warn("save_abort_external_change",
					slog.Time("our_load", ourLoadMtime),
					slog.Time("current_mtime", currentMtime))
				// File was modified externally - trigger reload instead of overwriting
				if h.storageWatcher != nil {
					h.storageWatcher.TriggerReload()
				}
				return
			}
		}
	}

	if h.storage != nil {
		// DEFENSIVE CHECK: Verify we're saving to the correct profile's database
		// This prevents catastrophic cross-profile contamination
		expectedPath, err := session.GetDBPathForProfile(h.profile)
		if err != nil {
			uiLog.Warn(
				"save_expected_path_failed",
				slog.String("profile", h.profile),
				slog.String("error", err.Error()),
			)
			return
		}
		if h.storage.Path() != expectedPath {
			uiLog.Error(
				"save_path_mismatch",
				slog.String("profile", h.profile),
				slog.String("expected", expectedPath),
				slog.String("got", h.storage.Path()),
			)
			h.setError(
				fmt.Errorf(
					"storage path mismatch (profile=%s): expected %s, got %s",
					h.profile,
					expectedPath,
					h.storage.Path(),
				),
			)
			return
		}

		// Take snapshot under lock for defensive programming
		// This ensures consistency even if architecture changes in the future
		h.instancesMu.RLock()
		instancesCopy := make([]*session.Instance, len(h.instances))
		copy(instancesCopy, h.instances)
		instanceCount := len(h.instances)
		h.instancesMu.RUnlock()

		uiLog.Debug(
			"save_instances",
			slog.Int("count", instanceCount),
			slog.String("profile", h.profile),
			slog.String("path", h.storage.Path()),
			slog.Bool("force", force),
		)

		// DEFENSIVE: Never save empty instances if storage file has data
		// This prevents catastrophic data loss from transient load failures
		if instanceCount == 0 {
			// Check if storage file exists and has data before overwriting with empty
			if info, err := os.Stat(h.storage.Path()); err == nil && info.Size() > 100 {
				uiLog.Warn("save_refusing_empty_overwrite", slog.Int64("file_bytes", info.Size()))
				return
			}
		}

		groupTreeCopy := h.groupTree.ShallowCopyForSave()

		// CRITICAL FIX: NotifySave MUST be called immediately before SaveWithGroups
		// Previously it was called 25 lines earlier, creating a race window where the
		// 500ms ignore window could expire before the save completed under load
		if h.storageWatcher != nil {
			h.storageWatcher.NotifySave()
		}

		// Save both instances and groups (including empty ones)
		if err := h.storage.SaveWithGroups(instancesCopy, groupTreeCopy); err != nil {
			h.setError(fmt.Errorf("failed to save: %w", err))
		} else {
			// CRITICAL FIX: Update lastLoadMtime after successful save.
			// Without this, subsequent saves incorrectly detect the TUI's own previous
			// save as an "external change" (currentMtime > stale lastLoadMtime) and abort.
			// This caused session renames and other non-force saves to silently fail.
			// See: https://github.com/asheshgoplani/agent-deck/issues/141
			if newMtime, err := h.storage.GetFileMtime(); err == nil && !newMtime.IsZero() {
				h.reloadMu.Lock()
				h.lastLoadMtime = newMtime
				h.reloadMu.Unlock()
			}
			// Clear pending title changes on successful save (rename was persisted)
			if len(h.pendingTitleChanges) > 0 {
				h.pendingTitleChanges = make(map[string]string)
			}
		}
	}
}

// saveGroupState saves only group expanded/collapsed state to SQLite.
// This is lightweight (no Touch, no StorageWatcher trigger) and safe to call after every toggle.
func (h *Home) saveGroupState() {
	if h.storage == nil || h.groupTree == nil {
		return
	}
	groupTreeCopy := h.groupTree.ShallowCopyForSave()
	if err := h.storage.SaveGroupsOnly(groupTreeCopy); err != nil {
		uiLog.Warn("save_group_state_failed", slog.String("error", err.Error()))
	}
}

// saveUIState persists cursor position, preview mode, and status filter to SQLite metadata.
func (h *Home) saveUIState() {
	if h.storage == nil {
		return
	}
	db := h.storage.GetDB()
	if db == nil {
		return
	}

	state := uiState{
		PreviewMode:  int(h.previewMode),
		StatusFilter: string(h.statusFilter),
	}

	// Capture cursor position
	if h.cursor >= 0 && h.cursor < len(h.flatItems) {
		item := h.flatItems[h.cursor]
		switch item.Type {
		case session.ItemTypeSession:
			if item.Session != nil {
				state.CursorSessionID = item.Session.ID
			}
		case session.ItemTypeWindow:
			state.CursorSessionID = item.WindowSessionID
		case session.ItemTypeGroup:
			state.CursorGroupPath = item.Path
		}
	}

	data, err := json.Marshal(state)
	if err != nil {
		uiLog.Warn("save_ui_state_marshal_failed", slog.String("error", err.Error()))
		return
	}
	if err := db.SetMeta("ui_state", string(data)); err != nil {
		uiLog.Warn("save_ui_state_failed", slog.String("error", err.Error()))
	}
}

// loadUIState reads persisted UI state from SQLite metadata.
// Preview mode and status filter are applied immediately.
// Cursor position is stored in pendingCursorRestore for deferred application after initial load.
func (h *Home) loadUIState() {
	if h.storage == nil {
		return
	}
	db := h.storage.GetDB()
	if db == nil {
		return
	}

	val, err := db.GetMeta("ui_state")
	if err != nil || val == "" {
		return
	}

	var state uiState
	if err := json.Unmarshal([]byte(val), &state); err != nil {
		uiLog.Warn("load_ui_state_unmarshal_failed", slog.String("error", err.Error()))
		return
	}

	// Apply preview mode and status filter immediately
	h.previewMode = PreviewMode(state.PreviewMode)
	h.statusFilter = session.Status(state.StatusFilter)

	// Defer cursor restoration until flatItems are populated
	h.pendingCursorRestore = &state
}

// createSessionInGroupWithWorktreeAndOptions creates a new session with full options including YOLO mode, sandbox, and tool options.
func (h *Home) createSessionInGroupWithWorktreeAndOptions(
	name, path, command, groupPath, worktreePath, worktreeRepoRoot, worktreeBranch string,
	geminiYoloMode bool,
	sandboxEnabled bool,
	toolOptionsJSON json.RawMessage,
	claudeExtraArgs []string,
	multiRepoEnabled bool,
	additionalPaths []string,
	parentSessionID, parentProjectPath string,
	tempID string,
) tea.Cmd {
	return func() tea.Msg {
		// Check tmux availability before creating session
		if err := tmux.IsTmuxAvailable(); err != nil {
			return sessionCreatedMsg{err: fmt.Errorf("cannot create session: %w", err), tempID: tempID}
		}

		if worktreePath != "" && worktreeRepoRoot != "" && worktreeBranch != "" && !multiRepoEnabled {
			// Single-repo worktree: create here. Multi-repo worktrees are handled below.
			//
			// Check for an existing worktree for this branch before creating a new one.
			if existingPath, err := git.GetWorktreeForBranch(worktreeRepoRoot, worktreeBranch); err == nil && existingPath != "" {
				uiLog.Info("worktree_reuse", slog.String("branch", worktreeBranch), slog.String("path", existingPath))
				worktreePath = existingPath
			} else {
				if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
					return sessionCreatedMsg{err: fmt.Errorf("failed to create parent directory: %w", err), tempID: tempID}
				}
				var setupBuf bytes.Buffer
				setupErr, err := git.CreateWorktreeWithSetup(worktreeRepoRoot, worktreePath, worktreeBranch, &setupBuf, &setupBuf)
				if err != nil {
					return sessionCreatedMsg{err: fmt.Errorf("failed to create worktree: %w", err), tempID: tempID}
				}
				if setupErr != nil {
					uiLog.Warn("worktree_setup_script_failed", slog.String("error", setupErr.Error()), slog.String("output", setupBuf.String()))
				}
			}
			path = worktreePath
		}

		tool, command := createSessionTool(command)

		var inst *session.Instance
		if groupPath != "" {
			inst = session.NewInstanceWithGroupAndTool(name, path, groupPath, tool)
		} else {
			inst = session.NewInstanceWithTool(name, path, tool)
		}
		inst.Command = command

		// Set worktree fields if provided
		if worktreePath != "" {
			inst.WorktreePath = worktreePath
			inst.WorktreeRepoRoot = worktreeRepoRoot
			inst.WorktreeBranch = worktreeBranch
		}

		applyCreateSessionToolOverrides(inst, tool, geminiYoloMode)

		// Apply generic tool options (claude, codex, etc.)
		if len(toolOptionsJSON) > 0 {
			inst.ToolOptionsJSON = toolOptionsJSON
		}

		// Apply claude extra CLI tokens (claude-only, ignored for other tools).
		if tool == "claude" && len(claudeExtraArgs) > 0 {
			inst.ExtraArgs = claudeExtraArgs
		}

		// Apply sandbox config.
		if sandboxEnabled {
			inst.Sandbox = session.NewSandboxConfig("")
		}

		// Apply multi-repo config.
		if multiRepoEnabled && len(additionalPaths) > 0 {
			inst.MultiRepoEnabled = true
			inst.AdditionalPaths = additionalPaths
			allPaths := inst.AllProjectPaths()

			if worktreeBranch != "" {
				// Multi-repo + worktree: create a persistent parent dir with all worktrees inside.
				// Layout: ~/.agent-deck/multi-repo-worktrees/<branch>-<id>/<repo-name>/
				home, _ := os.UserHomeDir()
				sanitizedBranch := strings.ReplaceAll(worktreeBranch, "/", "-")
				sanitizedBranch = strings.ReplaceAll(sanitizedBranch, " ", "-")
				parentDir := filepath.Join(home, ".agent-deck", "multi-repo-worktrees",
					fmt.Sprintf("%s-%s", sanitizedBranch, inst.ID[:8]))
				if mkErr := os.MkdirAll(parentDir, 0o755); mkErr != nil {
					return sessionCreatedMsg{err: fmt.Errorf("failed to create multi-repo worktree dir: %w", mkErr), tempID: tempID}
				}
				if resolved, evalErr := filepath.EvalSymlinks(parentDir); evalErr == nil {
					parentDir = resolved
				}
				inst.MultiRepoTempDir = parentDir

				// Create worktrees inside parentDir, named after each repo
				dirnames := session.DeduplicateDirnames(allPaths)
				var newProjectPath string
				var newAdditionalPaths []string
				for i, p := range allPaths {
					wtPath := filepath.Join(parentDir, dirnames[i])
					if git.IsGitRepo(p) {
						repoRoot, rootErr := git.GetWorktreeBaseRoot(p)
						if rootErr != nil {
							uiLog.Warn("multi_repo_worktree_skip", slog.String("path", p), slog.String("error", rootErr.Error()))
							// Copy path as-is into the parent dir via symlink
							_ = os.Symlink(p, wtPath)
							if i == 0 {
								newProjectPath = wtPath
							} else {
								newAdditionalPaths = append(newAdditionalPaths, wtPath)
							}
							continue
						}
						if err := git.CreateWorktree(repoRoot, wtPath, worktreeBranch); err != nil {
							uiLog.Warn("multi_repo_worktree_create_fail", slog.String("path", p), slog.String("error", err.Error()))
							_ = os.Symlink(p, wtPath)
							if i == 0 {
								newProjectPath = wtPath
							} else {
								newAdditionalPaths = append(newAdditionalPaths, wtPath)
							}
							continue
						}
						inst.MultiRepoWorktrees = append(inst.MultiRepoWorktrees, session.MultiRepoWorktree{
							OriginalPath: p,
							WorktreePath: wtPath,
							RepoRoot:     repoRoot,
							Branch:       worktreeBranch,
						})
						if i == 0 {
							newProjectPath = wtPath
						} else {
							newAdditionalPaths = append(newAdditionalPaths, wtPath)
						}
					} else {
						// Non-git paths: symlink into parent dir
						_ = os.Symlink(p, wtPath)
						if i == 0 {
							newProjectPath = wtPath
						} else {
							newAdditionalPaths = append(newAdditionalPaths, wtPath)
						}
					}
				}
				inst.ProjectPath = newProjectPath
				inst.AdditionalPaths = newAdditionalPaths
			} else {
				// Multi-repo without worktree: create a persistent parent dir with symlinks.
				home, _ := os.UserHomeDir()
				parentDir := filepath.Join(home, ".agent-deck", "multi-repo-worktrees", inst.ID[:8])
				if mkErr := os.MkdirAll(parentDir, 0o755); mkErr != nil {
					return sessionCreatedMsg{err: fmt.Errorf("failed to create multi-repo dir: %w", mkErr), tempID: tempID}
				}
				if resolved, evalErr := filepath.EvalSymlinks(parentDir); evalErr == nil {
					parentDir = resolved
				}
				inst.MultiRepoTempDir = parentDir

				// Create symlinks for all paths
				dirnames := session.DeduplicateDirnames(allPaths)
				var newProjectPath string
				var newAdditionalPaths []string
				for i, p := range allPaths {
					linkPath := filepath.Join(parentDir, dirnames[i])
					_ = os.Symlink(p, linkPath)
					if i == 0 {
						newProjectPath = linkPath
					} else {
						newAdditionalPaths = append(newAdditionalPaths, linkPath)
					}
				}
				inst.ProjectPath = newProjectPath
				inst.AdditionalPaths = newAdditionalPaths
			}

			// Update tmux session working directory to the parent dir
			if inst.GetTmuxSession() != nil {
				inst.GetTmuxSession().WorkDir = inst.MultiRepoTempDir
			}
		}

		if parentSessionID != "" {
			inst.SetParentWithPath(parentSessionID, parentProjectPath)
		}

		uiLog.Info("session_create_starting",
			slog.String("tool", inst.Tool),
			slog.String("path", inst.ProjectPath),
			slog.Bool("sandbox", inst.IsSandboxed()),
		)
		if err := inst.Start(); err != nil {
			uiLog.Error("session_create_failed", slog.String("error", err.Error()))
			return sessionCreatedMsg{err: err, tempID: tempID}
		}
		uiLog.Info("session_create_succeeded", slog.String("id", inst.ID))
		return sessionCreatedMsg{instance: inst, tempID: tempID}
	}
}

// createSessionTool maps a free-form command to (tool, command). Built-in
// tools resolve to themselves; custom tool names from user config resolve
// to the configured binary while keeping the custom name as the tool
// identity (so [tools.<name>] config lookup works). Anything unrecognised
// falls back to a "shell" session running `command` verbatim.
func createSessionTool(command string) (string, string) {
	tool := "shell"
	switch command {
	case "claude":
		tool = "claude"
	case "gemini":
		tool = "gemini"
	case "aider":
		tool = "aider"
	case "codex":
		tool = "codex"
	case "opencode":
		tool = "opencode"
	case "pi":
		tool = "pi"
	default:
		if toolDef := session.GetToolDef(command); toolDef != nil {
			tool = command
			command = toolDef.Command
		}
	}
	return tool, command
}

func applyCreateSessionToolOverrides(inst *session.Instance, tool string, geminiYoloMode bool) {
	if inst == nil {
		return
	}
	if tool == "gemini" {
		inst.SetGeminiYoloMode(geminiYoloMode)
	}
}

// quickForkSession performs a quick fork with default title suffix " (fork)"
func (h *Home) quickForkSession(source *session.Instance) tea.Cmd {
	if source == nil {
		return nil
	}
	title := source.Title + " (fork)"
	groupPath := source.GroupPath
	return h.forkSessionCmd(source, title, groupPath, source.ParentSessionID, source.ParentProjectPath)
}

// quickCreateSession creates a session instantly with auto-generated name and smart defaults.
// When the cursor is on a session, it inherits that session's path and tool settings
// (duplicate-like behavior per community feedback). When on a group header, it uses
// the group's default path and most recently created session's settings.
func (h *Home) quickCreateSession() tea.Cmd {
	groupPath := ""
	var sourceSession *session.Instance

	// Determine context from cursor position
	if h.cursor >= 0 && h.cursor < len(h.flatItems) {
		item := h.flatItems[h.cursor]
		if item.Type == session.ItemTypeSession && item.Session != nil {
			sourceSession = item.Session
			groupPath = item.Session.GroupPath
		} else if item.Type == session.ItemTypeGroup && item.Group != nil {
			groupPath = item.Group.Path
		}
	}
	if groupPath == "" {
		if h.groupScope != "" {
			groupPath = h.groupScope
		} else {
			groupPath = session.DefaultGroupPath
		}
	}

	projectPath := ""
	tool := ""
	command := ""
	var toolOptionsJSON json.RawMessage
	geminiYoloMode := false

	if sourceSession != nil {
		// Cursor on a session: inherit from THAT session (duplicate-like)
		projectPath = sourceSession.ProjectPath
		tool = sourceSession.Tool
		command = sourceSession.Command
		if len(sourceSession.ToolOptionsJSON) > 0 {
			toolOptionsJSON = session.StripResumeFields(sourceSession.ToolOptionsJSON)
		}
		if sourceSession.GeminiYoloMode != nil && *sourceSession.GeminiYoloMode {
			geminiYoloMode = true
		}
	} else {
		// Cursor on a group header: use group defaults + most recent session
		projectPath = h.getDefaultPathForGroup(groupPath)
		if projectPath == "" {
			projectPath = h.mostRecentPathInGroup(groupPath)
		}

		h.instancesMu.RLock()
		var mostRecent *session.Instance
		for _, inst := range h.instances {
			if inst.GroupPath == groupPath {
				if mostRecent == nil || inst.CreatedAt.After(mostRecent.CreatedAt) {
					mostRecent = inst
				}
			}
		}
		if mostRecent != nil {
			tool = mostRecent.Tool
			command = mostRecent.Command
			if len(mostRecent.ToolOptionsJSON) > 0 {
				toolOptionsJSON = session.StripResumeFields(mostRecent.ToolOptionsJSON)
			}
			if mostRecent.GeminiYoloMode != nil && *mostRecent.GeminiYoloMode {
				geminiYoloMode = true
			}
		}
		h.instancesMu.RUnlock()
	}

	// Fallback for path
	if projectPath == "" {
		var err error
		projectPath, err = os.Getwd()
		if err != nil {
			return func() tea.Msg {
				return sessionCreatedMsg{err: fmt.Errorf("cannot determine project path: %w", err)}
			}
		}
	}

	// Fallback for tool
	if tool == "" {
		tool = session.GetDefaultTool()
	}
	if tool == "" {
		tool = "claude"
	}
	if command == "" {
		command = tool
	}

	// Generate unique name
	h.instancesMu.RLock()
	name := session.GenerateUniqueSessionName(h.instances, groupPath)
	h.instancesMu.RUnlock()

	return h.createSessionInGroupWithWorktreeAndOptions(
		name, projectPath, command, groupPath,
		"", "", "", // no worktree
		geminiYoloMode, false, toolOptionsJSON,
		nil,        // no extra claude args (recent-session path)
		false, nil, // no multi-repo
		"", "", // no parent
		"", // no placeholder
	)
}

// suggestConductorParent returns the ID of the most contextually relevant conductor
// based on the current cursor position: the cursor session itself if it's a conductor,
// or the conductor pointed to by its ParentSessionID.
func (h *Home) suggestConductorParent() string {
	if h.cursor < 0 || h.cursor >= len(h.flatItems) {
		return ""
	}
	item := h.flatItems[h.cursor]
	if item.Type != session.ItemTypeSession || item.Session == nil {
		return ""
	}
	inst := item.Session
	// Cursor is directly on a conductor.
	if inst.IsConductor {
		return inst.ID
	}
	// Cursor is on a session that has a conductor parent.
	if inst.ParentSessionID != "" {
		h.instancesMu.RLock()
		parent, ok := h.instanceByID[inst.ParentSessionID]
		h.instancesMu.RUnlock()
		if ok && parent.IsConductor {
			return parent.ID
		}
	}
	return ""
}

// activeConductorSessions returns all non-stopped, non-error conductor sessions
// in the current profile (h.instances is already scoped to the active profile).
func (h *Home) activeConductorSessions() []*session.Instance {
	h.instancesMu.RLock()
	defer h.instancesMu.RUnlock()

	var out []*session.Instance
	for _, inst := range h.instances {
		if inst.IsConductor &&
			inst.Status != session.StatusError &&
			inst.Status != session.StatusStopped {
			out = append(out, inst)
		}
	}
	return out
}

// mostRecentPathInGroup returns the project path of the most recently created
// session in the given group, or empty string if no sessions exist.
func (h *Home) mostRecentPathInGroup(groupPath string) string {
	h.instancesMu.RLock()
	defer h.instancesMu.RUnlock()

	var mostRecent *session.Instance
	for _, inst := range h.instances {
		if inst.GroupPath == groupPath && inst.ProjectPath != "" {
			if mostRecent == nil || inst.CreatedAt.After(mostRecent.CreatedAt) {
				mostRecent = inst
			}
		}
	}
	if mostRecent != nil {
		return mostRecent.ProjectPath
	}
	return ""
}

// forkSessionWithDialog opens the fork dialog to customize title and group
func (h *Home) forkSessionWithDialog(source *session.Instance) tea.Cmd {
	if source == nil {
		return nil
	}
	// Pre-populate dialog with source session info
	conductors := h.activeConductorSessions()
	suggestedParentID := h.suggestConductorParent()
	h.forkDialog.Show(source.Title, source.ProjectPath, source.GroupPath, conductors, suggestedParentID)
	return nil
}

// forkSessionCmd creates a forked session with the given title and group
// Shows immediate UI feedback by tracking the source session in forkingSessions
func (h *Home) forkSessionCmd(source *session.Instance, title, groupPath, parentSessionID, parentProjectPath string) tea.Cmd {
	return h.forkSessionCmdWithOptions(source, title, groupPath, nil, false, parentSessionID, parentProjectPath)
}

// forkSessionCmdWithOptions creates a forked session with the given title, group, Claude options, and optional sandbox.
// Shows immediate UI feedback by tracking the source session in forkingSessions.
func (h *Home) forkSessionCmdWithOptions(
	source *session.Instance,
	title, groupPath string,
	opts *session.ClaudeOptions,
	sandboxEnabled bool,
	parentSessionID, parentProjectPath string,
) tea.Cmd {
	if source == nil {
		return nil
	}

	// Track source session as "forking" for immediate UI feedback
	h.forkingSessions[source.ID] = time.Now()

	sourceID := source.ID // Capture for closure

	return func() tea.Msg {
		// Check tmux availability before forking
		if err := tmux.IsTmuxAvailable(); err != nil {
			return sessionForkedMsg{err: fmt.Errorf("cannot fork session: %w", err), sourceID: sourceID}
		}

		if opts != nil && opts.WorktreePath != "" && opts.WorktreeRepoRoot != "" && opts.WorktreeBranch != "" {
			// Worktree creation can be slow on large repos; keep it in async cmd path
			// so the TUI remains responsive.
			//
			// Check for an existing worktree for this branch before creating a new one.
			if existingPath, err := git.GetWorktreeForBranch(opts.WorktreeRepoRoot, opts.WorktreeBranch); err == nil && existingPath != "" {
				uiLog.Info("worktree_reuse", slog.String("branch", opts.WorktreeBranch), slog.String("path", existingPath))
				opts.WorktreePath = existingPath
			} else {
				if err := os.MkdirAll(filepath.Dir(opts.WorktreePath), 0o755); err != nil {
					return sessionForkedMsg{err: fmt.Errorf("failed to create directory: %w", err), sourceID: sourceID}
				}
				var setupBuf bytes.Buffer
				setupErr, err := git.CreateWorktreeWithSetup(opts.WorktreeRepoRoot, opts.WorktreePath, opts.WorktreeBranch, &setupBuf, &setupBuf)
				if err != nil {
					return sessionForkedMsg{err: fmt.Errorf("worktree creation failed: %w", err), sourceID: sourceID}
				}
				if setupErr != nil {
					uiLog.Warn("worktree_setup_script_failed", slog.String("error", setupErr.Error()), slog.String("output", setupBuf.String()))
				}
			}
		}

		var inst *session.Instance
		var err error

		switch source.Tool {
		case "opencode":
			inst, _, err = source.CreateForkedOpenCodeInstance(title, groupPath)
		default:
			inst, _, err = source.CreateForkedInstanceWithOptions(title, groupPath, opts)
		}
		if err != nil {
			return sessionForkedMsg{err: fmt.Errorf("cannot create forked instance: %w", err), sourceID: sourceID}
		}

		// Apply sandbox config to forked instance.
		if sandboxEnabled {
			inst.Sandbox = session.NewSandboxConfig("")
		}

		// Propagate multi-repo config from source.
		if source.IsMultiRepo() {
			inst.MultiRepoEnabled = true
			inst.AdditionalPaths = append([]string{}, source.AdditionalPaths...)
			// Copy worktree tracking from source (shared worktrees)
			if len(source.MultiRepoWorktrees) > 0 {
				inst.MultiRepoWorktrees = append([]session.MultiRepoWorktree{}, source.MultiRepoWorktrees...)
			}
			// Create a new persistent dir for the fork with symlinks to shared worktrees
			home, _ := os.UserHomeDir()
			parentDir := filepath.Join(home, ".agent-deck", "multi-repo-worktrees", inst.ID[:8])
			if mkErr := os.MkdirAll(parentDir, 0o755); mkErr != nil {
				return sessionForkedMsg{err: fmt.Errorf("failed to create multi-repo dir: %w", mkErr), sourceID: sourceID}
			}
			if resolved, evalErr := filepath.EvalSymlinks(parentDir); evalErr == nil {
				parentDir = resolved
			}
			inst.MultiRepoTempDir = parentDir
			if inst.GetTmuxSession() != nil {
				inst.GetTmuxSession().WorkDir = parentDir
			}
			// Recreate symlinks/entries in new parent dir pointing to source worktree paths
			allPaths := inst.AllProjectPaths()
			dirnames := session.DeduplicateDirnames(allPaths)
			var newProjectPath string
			var newAdditionalPaths []string
			for i, p := range allPaths {
				linkPath := filepath.Join(parentDir, dirnames[i])
				_ = os.Symlink(p, linkPath)
				if i == 0 {
					newProjectPath = linkPath
				} else {
					newAdditionalPaths = append(newAdditionalPaths, linkPath)
				}
			}
			inst.ProjectPath = newProjectPath
			inst.AdditionalPaths = newAdditionalPaths
		}

		if parentSessionID != "" {
			inst.SetParentWithPath(parentSessionID, parentProjectPath)
		}

		if err := inst.Start(); err != nil {
			return sessionForkedMsg{err: err, sourceID: sourceID}
		}

		switch inst.Tool {
		case "opencode":
			go inst.DetectOpenCodeSession()
		}

		return sessionForkedMsg{instance: inst, sourceID: sourceID}
	}
}

// sessionDeletedMsg signals that a session was deleted
type sessionDeletedMsg struct {
	deletedID string
	killErr   error // Error from Kill() if any
}

// sessionClosedMsg signals that a session process was closed without deleting metadata.
type sessionClosedMsg struct {
	sessionID string
	killErr   error
}

// sessionRestoredMsg signals that an undo-delete restore completed
type sessionRestoredMsg struct {
	instance *session.Instance
	err      error
	warning  string
}

// deleteSession deletes a session
func (h *Home) deleteSession(inst *session.Instance) tea.Cmd {
	id := inst.ID
	isWorktree := inst.IsWorktree()
	worktreePath := inst.WorktreePath
	worktreeRepoRoot := inst.WorktreeRepoRoot
	isMultiRepo := inst.IsMultiRepo()
	multiRepoTempDir := inst.MultiRepoTempDir
	multiRepoWorktrees := inst.MultiRepoWorktrees
	return func() tea.Msg {
		killErr := inst.Kill()
		if isWorktree {
			if err := git.RemoveWorktree(worktreeRepoRoot, worktreePath, true); err != nil {
				uiLog.Warn("worktree_remove_err", slog.String("path", worktreePath), slog.String("err", err.Error()))
			}
			if err := git.PruneWorktrees(worktreeRepoRoot); err != nil {
				uiLog.Warn("worktree_prune_err", slog.String("repo", worktreeRepoRoot), slog.String("err", err.Error()))
			}
		}
		if isMultiRepo {
			// Clean up multi-repo temp directory
			if multiRepoTempDir != "" {
				_ = os.RemoveAll(multiRepoTempDir)
			}
			// Clean up per-repo worktrees
			for _, wt := range multiRepoWorktrees {
				if err := git.RemoveWorktree(wt.RepoRoot, wt.WorktreePath, true); err != nil {
					uiLog.Warn("worktree_remove_err", slog.String("path", wt.WorktreePath), slog.String("err", err.Error()))
				}
				if err := git.PruneWorktrees(wt.RepoRoot); err != nil {
					uiLog.Warn("worktree_prune_err", slog.String("repo", wt.RepoRoot), slog.String("err", err.Error()))
				}
			}
		}
		return sessionDeletedMsg{deletedID: id, killErr: killErr}
	}
}

// closeSession stops a session process but keeps metadata in list/storage.
func (h *Home) closeSession(inst *session.Instance) tea.Cmd {
	id := inst.ID
	return func() tea.Msg {
		killErr := inst.Kill()
		return sessionClosedMsg{sessionID: id, killErr: killErr}
	}
}

// sessionRestartedMsg signals that a session was restarted.
type sessionRestartedMsg struct {
	sessionID string
	err       error
	warning   string
	fresh     bool
}

// mcpRestartedMsg signals that an MCP-triggered restart completed and should auto-attach
type mcpRestartedMsg struct {
	session *session.Instance
	err     error
}

// restartSession restarts a dead/errored session by creating a new tmux session.
func (h *Home) restartSession(inst *session.Instance) tea.Cmd {
	id := inst.ID
	mcpUILog.Debug(
		"restart_session_called",
		slog.String("id", inst.ID),
		slog.String("title", inst.Title),
		slog.String("tool", inst.Tool),
	)
	return func() tea.Msg {
		mcpUILog.Debug("restart_session_executing", slog.String("id", id))

		// Resolve current instance by ID at execution time. During storage reloads,
		// the pointer captured from the key event can be replaced before this cmd runs.
		h.instancesMu.RLock()
		current := h.instanceByID[id]
		h.instancesMu.RUnlock()
		if current == nil {
			err := fmt.Errorf("session no longer exists")
			mcpUILog.Debug("restart_session_result", slog.String("id", id), slog.Any("error", err))
			return sessionRestartedMsg{sessionID: id, err: err}
		}

		err := current.Restart()
		mcpUILog.Debug("restart_session_result", slog.String("id", id), slog.Any("error", err))
		return sessionRestartedMsg{
			sessionID: id,
			err:       err,
			warning:   current.ConsumeCodexRestartWarning(),
		}
	}
}

// restartSessionFresh restarts a session without resuming the previous tool session.
func (h *Home) restartSessionFresh(inst *session.Instance) tea.Cmd {
	id := inst.ID
	mcpUILog.Debug(
		"restart_session_fresh_called",
		slog.String("id", inst.ID),
		slog.String("title", inst.Title),
		slog.String("tool", inst.Tool),
	)
	return func() tea.Msg {
		mcpUILog.Debug("restart_session_fresh_executing", slog.String("id", id))

		h.instancesMu.RLock()
		current := h.instanceByID[id]
		h.instancesMu.RUnlock()
		if current == nil {
			err := fmt.Errorf("session no longer exists")
			mcpUILog.Debug("restart_session_fresh_result", slog.String("id", id), slog.Any("error", err))
			return sessionRestartedMsg{sessionID: id, err: err, fresh: true}
		}

		err := current.RestartFresh()
		mcpUILog.Debug("restart_session_fresh_result", slog.String("id", id), slog.Any("error", err))
		return sessionRestartedMsg{
			sessionID: id,
			err:       err,
			warning:   current.ConsumeCodexRestartWarning(),
			fresh:     true,
		}
	}
}

type remoteSessionDeletedMsg struct {
	remoteName string
	sessionID  string
	title      string
	err        error
}

type remoteSessionClosedMsg struct {
	remoteName string
	sessionID  string
	title      string
	err        error
}

type remoteSessionRestartedMsg struct {
	remoteName string
	sessionID  string
	title      string
	err        error
}

// deleteRemoteSession deletes a remote session and refreshes the remote list.
func (h *Home) deleteRemoteSession(remoteName, sessionID, title string) tea.Cmd {
	return func() tea.Msg {
		config, err := session.LoadUserConfig()
		if err != nil || config == nil || config.Remotes == nil {
			return remoteSessionDeletedMsg{
				remoteName: remoteName,
				sessionID:  sessionID,
				title:      title,
				err:        fmt.Errorf("failed to load remote config"),
			}
		}
		rc, ok := config.Remotes[remoteName]
		if !ok {
			return remoteSessionDeletedMsg{
				remoteName: remoteName,
				sessionID:  sessionID,
				title:      title,
				err:        fmt.Errorf("remote '%s' not found", remoteName),
			}
		}
		runner := session.NewSSHRunner(remoteName, rc)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		err = runner.DeleteSession(ctx, sessionID)
		return remoteSessionDeletedMsg{remoteName: remoteName, sessionID: sessionID, title: title, err: err}
	}
}

// closeRemoteSession stops a remote session process without deleting metadata.
func (h *Home) closeRemoteSession(remoteName, sessionID, title string) tea.Cmd {
	return func() tea.Msg {
		config, err := session.LoadUserConfig()
		if err != nil || config == nil || config.Remotes == nil {
			return remoteSessionClosedMsg{
				remoteName: remoteName,
				sessionID:  sessionID,
				title:      title,
				err:        fmt.Errorf("failed to load remote config"),
			}
		}
		rc, ok := config.Remotes[remoteName]
		if !ok {
			return remoteSessionClosedMsg{
				remoteName: remoteName,
				sessionID:  sessionID,
				title:      title,
				err:        fmt.Errorf("remote '%s' not found", remoteName),
			}
		}
		runner := session.NewSSHRunner(remoteName, rc)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		err = runner.StopSession(ctx, sessionID)
		return remoteSessionClosedMsg{remoteName: remoteName, sessionID: sessionID, title: title, err: err}
	}
}

// restartRemoteSession restarts a remote session.
func (h *Home) restartRemoteSession(remoteName, sessionID, title string) tea.Cmd {
	return func() tea.Msg {
		config, err := session.LoadUserConfig()
		if err != nil || config == nil || config.Remotes == nil {
			return remoteSessionRestartedMsg{
				remoteName: remoteName,
				sessionID:  sessionID,
				title:      title,
				err:        fmt.Errorf("failed to load remote config"),
			}
		}
		rc, ok := config.Remotes[remoteName]
		if !ok {
			return remoteSessionRestartedMsg{
				remoteName: remoteName,
				sessionID:  sessionID,
				title:      title,
				err:        fmt.Errorf("remote '%s' not found", remoteName),
			}
		}
		runner := session.NewSSHRunner(remoteName, rc)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		err = runner.RestartSession(ctx, sessionID)
		return remoteSessionRestartedMsg{remoteName: remoteName, sessionID: sessionID, title: title, err: err}
	}
}

// attachSession attaches to a session using custom PTY with Ctrl+Q detection
func (h *Home) attachSession(inst *session.Instance) tea.Cmd {
	tmuxSess := inst.GetTmuxSession()
	if tmuxSess == nil {
		return nil
	}

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
	inst.MarkAccessed()

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
	return tea.Exec(attachCmd{session: tmuxSess, detachByte: h.detachByte()}, func(err error) tea.Msg {
		// CRITICAL: Set isAttaching to false BEFORE returning the message
		// This prevents a race condition where View() could be called with
		// isAttaching=true before Update() processes statusUpdateMsg,
		// causing a blank screen on return from attached session
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
		currentWorkDir := strings.TrimSpace(tmuxSess.GetWorkDir())

		return statusUpdateMsg{attachedSessionID: inst.ID, attachedWorkDir: currentWorkDir}
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

// attachCmd implements tea.ExecCommand for custom PTY attach
type attachCmd struct {
	session    *tmux.Session
	detachByte byte
}

func (a attachCmd) Run() error {
	// NOTE: Screen clearing is ONLY done in the tea.Exec callback (after Attach returns)
	// Removing clear screen here prevents double-clearing which corrupts terminal state

	ctx := context.Background()
	return a.session.Attach(ctx, a.detachByte)
}

func (a attachCmd) SetStdin(r io.Reader)  {}
func (a attachCmd) SetStdout(w io.Writer) {}
func (a attachCmd) SetStderr(w io.Writer) {}

// createRemoteSession creates a new session on a remote and auto-attaches to it.
func (h *Home) createRemoteSession(remoteName string) tea.Cmd {
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
	return tea.Exec(remoteCreateAndAttachCmd{runner: runner}, func(err error) tea.Msg {
		h.isAttaching.Store(false)
		if err != nil {
			return sessionCreatedMsg{err: fmt.Errorf("failed to create remote session: %w", err)}
		}
		return statusUpdateMsg{}
	})
}

// remoteCreateAndAttachCmd creates a session on the remote, then attaches to it.
type remoteCreateAndAttachCmd struct {
	runner *session.SSHRunner
}

func (r remoteCreateAndAttachCmd) Run() error {
	ctx := context.Background()
	sessionID, err := r.runner.CreateSession(ctx)
	if err != nil {
		return err
	}
	return r.runner.Attach(sessionID)
}

func (r remoteCreateAndAttachCmd) SetStdin(reader io.Reader)  {}
func (r remoteCreateAndAttachCmd) SetStdout(writer io.Writer) {}
func (r remoteCreateAndAttachCmd) SetStderr(writer io.Writer) {}

// attachWindowCmd implements tea.ExecCommand for attaching to a specific tmux window
type attachWindowCmd struct {
	session     *tmux.Session
	windowIndex int
	detachByte  byte
}

func (a attachWindowCmd) Run() error {
	ctx := context.Background()
	return a.session.AttachWindow(ctx, a.windowIndex, a.detachByte)
}

func (a attachWindowCmd) SetStdin(r io.Reader)  {}
func (a attachWindowCmd) SetStdout(w io.Writer) {}
func (a attachWindowCmd) SetStderr(w io.Writer) {}

// attachRemoteSession attaches to a remote session via SSH, suspending the TUI.
func (h *Home) attachRemoteSession(remoteName, sessionID string) tea.Cmd {
	config, err := session.LoadUserConfig()
	if err != nil || config == nil || config.Remotes == nil {
		return nil
	}
	rc, ok := config.Remotes[remoteName]
	if !ok {
		return nil
	}
	runner := session.NewSSHRunner(remoteName, rc)
	h.isAttaching.Store(true)
	return tea.Exec(remoteAttachCmd{runner: runner, sessionID: sessionID}, func(err error) tea.Msg {
		h.isAttaching.Store(false)
		return statusUpdateMsg{}
	})
}

// remoteAttachCmd implements tea.ExecCommand for remote SSH attach
type remoteAttachCmd struct {
	runner    *session.SSHRunner
	sessionID string
}

func (r remoteAttachCmd) Run() error {
	return r.runner.Attach(r.sessionID)
}

func (r remoteAttachCmd) SetStdin(reader io.Reader)  {}
func (r remoteAttachCmd) SetStdout(writer io.Writer) {}
func (r remoteAttachCmd) SetStderr(writer io.Writer) {}

// importSessions imports existing tmux sessions
func (h *Home) importSessions() tea.Msg {
	discovered, err := session.DiscoverExistingTmuxSessions(h.instances)
	if err != nil {
		return loadSessionsMsg{err: err}
	}

	h.instancesMu.Lock()
	h.instances = append(h.instances, discovered...)
	instancesCopy := make([]*session.Instance, len(h.instances))
	copy(instancesCopy, h.instances)
	h.instancesMu.Unlock()

	// Add discovered sessions to group tree before saving
	for _, inst := range discovered {
		h.groupTree.AddSession(inst)
	}
	// Save both instances AND groups (critical fix: was losing groups!)
	h.saveInstances()
	state := h.preserveState()
	return loadSessionsMsg{instances: instancesCopy, restoreState: &state}
}

// countSessionStatuses counts sessions by status for the logo display
// Uses cache to avoid O(n) iteration on every View() call
// Cache expires after 500ms to balance freshness with performance
// PERFORMANCE: Increased from 100ms to 500ms - status changes are rare
// during UI interaction, and longer cache reduces View() overhead
func (h *Home) countSessionStatuses() (running, waiting, idle, errored int) {
	// Return cached values if valid and not expired
	const cacheDuration = 500 * time.Millisecond
	if h.cachedStatusCounts.valid.Load() &&
		time.Since(h.cachedStatusCounts.timestamp) < cacheDuration {
		return h.cachedStatusCounts.running, h.cachedStatusCounts.waiting,
			h.cachedStatusCounts.idle, h.cachedStatusCounts.errored
	}

	// Compute counts
	snapshot := h.getSessionRenderSnapshot()
	if snapshot == nil {
		h.refreshSessionRenderSnapshot(nil)
		snapshot = h.getSessionRenderSnapshot()
	}
	for _, state := range snapshot {
		switch state.status {
		case session.StatusRunning:
			running++
		case session.StatusWaiting:
			waiting++
		case session.StatusIdle:
			idle++
		case session.StatusError, session.StatusStopped:
			errored++
		}
	}

	// Cache results with timestamp
	h.cachedStatusCounts.running = running
	h.cachedStatusCounts.waiting = waiting
	h.cachedStatusCounts.idle = idle
	h.cachedStatusCounts.errored = errored
	h.cachedStatusCounts.valid.Store(true)
	h.cachedStatusCounts.timestamp = time.Now()
	return running, waiting, idle, errored
}

// renderFilterBar renders the quick filter pills
// Format: [All] [● Running 2] [◐ Waiting 1] [○ Idle 5] [✕ Error 1]
func (h *Home) renderFilterBar() string {
	running, waiting, idle, errored := h.countSessionStatuses()

	// Pill styling
	activePillStyle := lipgloss.NewStyle().
		Foreground(ColorBg).
		Background(ColorAccent).
		Bold(true).
		Padding(0, 1)

	inactivePillStyle := lipgloss.NewStyle().
		Foreground(ColorText).
		Background(ColorSurface).
		Padding(0, 1)

	dimPillStyle := lipgloss.NewStyle().
		Foreground(ColorText).
		Faint(true).
		Padding(0, 1)

	// Build pills
	var pills []string

	// "All" / "Open" pill
	isActive := h.statusFilter == FilterModeActive
	activeLabel := h.activeFilterLabel
	if activeLabel == "" {
		activeLabel = "Open"
	}
	// "All" is shorter than "Open" — pad with a trailing space outside the pill
	// so toggling doesn't shift the bar, without extending the highlight.
	allPad := ""
	if len(activeLabel) > len("All") {
		allPad = " "
	}
	if isActive {
		pills = append(pills, activePillStyle.Render(activeLabel))
	} else if h.statusFilter == "" {
		pills = append(pills, activePillStyle.Render("All")+allPad)
	} else {
		pills = append(pills, inactivePillStyle.Render("All")+allPad)
	}

	// Running pill (green when active, dim if 0)
	runningLabel := fmt.Sprintf("● %d", running)
	if h.statusFilter == session.StatusRunning {
		pills = append(pills, lipgloss.NewStyle().
			Foreground(ColorBg).
			Background(ColorGreen).
			Bold(true).
			Padding(0, 1).Render(runningLabel))
	} else if running > 0 {
		pills = append(pills, lipgloss.NewStyle().
			Foreground(ColorGreen).
			Background(ColorSurface).
			Padding(0, 1).Render(runningLabel))
	} else {
		pills = append(pills, dimPillStyle.Render(runningLabel))
	}

	// Waiting pill (yellow when active)
	waitingLabel := fmt.Sprintf("◐ %d", waiting)
	if h.statusFilter == session.StatusWaiting {
		pills = append(pills, lipgloss.NewStyle().
			Foreground(ColorBg).
			Background(ColorYellow).
			Bold(true).
			Padding(0, 1).Render(waitingLabel))
	} else if waiting > 0 {
		pills = append(pills, lipgloss.NewStyle().
			Foreground(ColorYellow).
			Background(ColorSurface).
			Padding(0, 1).Render(waitingLabel))
	} else {
		pills = append(pills, dimPillStyle.Render(waitingLabel))
	}

	// Idle pill (gray when selected, dimmed when active filter hides it)
	idleLabel := fmt.Sprintf("○ %d", idle)
	if h.statusFilter == session.StatusIdle {
		pills = append(pills, lipgloss.NewStyle().
			Foreground(ColorBg).
			Background(ColorTextDim).
			Bold(true).
			Padding(0, 1).Render(idleLabel))
	} else if idle == 0 {
		pills = append(pills, dimPillStyle.Render(idleLabel))
	} else {
		pills = append(pills, lipgloss.NewStyle().
			Foreground(ColorText).
			Background(ColorSurface).
			Padding(0, 1).Render(idleLabel))
	}

	// Error pill (red when selected, dimmed when active filter hides it)
	if errored > 0 || h.statusFilter == session.StatusError {
		errorLabel := fmt.Sprintf("✕ %d", errored)
		if h.statusFilter == session.StatusError {
			pills = append(pills, lipgloss.NewStyle().
				Foreground(ColorBg).
				Background(ColorRed).
				Bold(true).
				Padding(0, 1).Render(errorLabel))
		} else if isActive {
			pills = append(pills, dimPillStyle.Render(errorLabel))
		} else if errored > 0 {
			pills = append(pills, lipgloss.NewStyle().
				Foreground(ColorRed).
				Background(ColorSurface).
				Padding(0, 1).Render(errorLabel))
		}
	}

	// Hint for keyboard shortcuts (cached — content is static)
	hint := cachedFilterBarHint()

	// Join pills with spaces (leading space replaces Padding)
	filterRow := " " + strings.Join(pills, " ") + hint

	return lipgloss.NewStyle().
		MaxWidth(h.width).
		Render(filterRow)
}

// updateSizes updates component sizes
func (h *Home) updateSizes() {
	h.search.SetSize(h.width, h.height)
	h.newDialog.SetSize(h.width, h.height)
	h.groupDialog.SetSize(h.width, h.height)
	h.confirmDialog.SetSize(h.width, h.height)
	h.geminiModelDialog.SetSize(h.width, h.height)
	h.worktreeFinishDialog.SetSize(h.width, h.height)
	if h.feedbackDialog != nil {
		h.feedbackDialog.SetSize(h.width, h.height)
	}
}

// View renders the UI
func (h *Home) View() string {
	// CRITICAL: Return empty during attach to prevent View() output leakage
	// (Bubble Tea Issue #431 - View gets printed to stdout during tea.Exec)
	if h.isAttaching.Load() { // Atomic read for thread safety
		return ""
	}

	if h.width == 0 {
		return "Loading..."
	}

	var renderStart time.Time
	if logging.IsDebugEnabled() {
		renderStart = time.Now()
	}

	// Check minimum terminal size for usability
	if h.width < minTerminalWidth || h.height < minTerminalHeight {
		return lipgloss.Place(
			h.width, h.height,
			lipgloss.Center, lipgloss.Center,
			lipgloss.NewStyle().
				Foreground(ColorYellow).
				Render(fmt.Sprintf(
					"Terminal too small (%dx%d)\nMinimum: %dx%d",
					h.width, h.height,
					minTerminalWidth, minTerminalHeight,
				)),
		)
	}

	// Show loading splash during initial session load
	if h.initialLoading {
		return renderLoadingSplash(h.width, h.height, h.animationFrame)
	}

	// Show quitting splash during shutdown
	if h.isQuitting {
		return renderQuittingSplash(h.width, h.height, h.animationFrame)
	}

	// Setup wizard takes over entire screen
	if h.setupWizard.IsVisible() {
		return h.setupWizard.View()
	}

	// Watcher panel is modal (before settings panel)
	if h.watcherPanel.IsVisible() {
		return h.watcherPanel.View()
	}

	// Settings panel is modal
	if h.settingsPanel.IsVisible() {
		return h.settingsPanel.View()
	}

	// Overlays take full screen
	if h.helpOverlay.IsVisible() {
		return h.helpOverlay.View()
	}
	if h.search.IsVisible() {
		return h.search.View()
	}
	if h.globalSearch.IsVisible() {
		return h.globalSearch.View()
	}
	if h.newDialog.IsVisible() {
		return h.newDialog.View()
	}
	if h.groupDialog.IsVisible() {
		return h.groupDialog.View()
	}
	if h.forkDialog.IsVisible() {
		return h.forkDialog.View()
	}
	if h.confirmDialog.IsVisible() {
		return h.confirmDialog.View()
	}
	if h.mcpDialog.IsVisible() {
		return h.mcpDialog.View()
	}
	if h.editPathsDialog.IsVisible() {
		return h.editPathsDialog.View()
	}
	if h.skillDialog.IsVisible() {
		return h.skillDialog.View()
	}
	if h.geminiModelDialog.IsVisible() {
		return h.geminiModelDialog.View()
	}
	if h.sessionPickerDialog.IsVisible() {
		return h.sessionPickerDialog.View()
	}
	if h.worktreeFinishDialog.IsVisible() {
		return h.worktreeFinishDialog.View()
	}
	if h.feedbackDialog.IsVisible() {
		return h.feedbackDialog.View()
	}
	if h.showCostDashboard {
		return h.costDashboard.View()
	}

	// Reuse viewBuilder to reduce allocations (reset and pre-allocate)
	h.viewBuilder.Reset()
	h.viewBuilder.Grow(32768) // Pre-allocate 32KB for typical view size
	b := &h.viewBuilder

	// ═══════════════════════════════════════════════════════════════════
	// HEADER BAR
	// ═══════════════════════════════════════════════════════════════════
	// Calculate real session status counts for logo and stats
	running, waiting, idle, errored := h.countSessionStatuses()
	logo := RenderLogoCompact(running, waiting, idle)

	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorAccent)

	// Show profile in title if not default
	titleText := "Agent Deck"
	if h.profile != "" && h.profile != session.DefaultProfile {
		profileStyle := lipgloss.NewStyle().
			Foreground(ColorCyan).
			Bold(true)
		titleText = "Agent Deck " + profileStyle.Render("["+h.profile+"]")
	}
	if h.groupScope != "" {
		scopeStyle := lipgloss.NewStyle().
			Foreground(ColorPurple).
			Bold(true)
		titleText += " " + scopeStyle.Render("["+h.groupScopeDisplayName()+"]")
	}
	title := titleStyle.Render(titleText)

	// Status-based stats (more useful than group/session counts)
	// Format: ● 2 running • ◐ 1 waiting • ○ 3 idle (• ✕ 1 error)
	var statsParts []string
	statsSep := lipgloss.NewStyle().Foreground(ColorBorder).Render(" • ")

	if running > 0 {
		statsParts = append(
			statsParts,
			lipgloss.NewStyle().Foreground(ColorGreen).Render(fmt.Sprintf("● %d running", running)),
		)
	}
	if waiting > 0 {
		statsParts = append(
			statsParts,
			lipgloss.NewStyle().Foreground(ColorYellow).Render(fmt.Sprintf("◐ %d waiting", waiting)),
		)
	}
	if idle > 0 {
		statsParts = append(
			statsParts,
			lipgloss.NewStyle().Foreground(ColorText).Render(fmt.Sprintf("○ %d idle", idle)),
		)
	}
	if errored > 0 {
		statsParts = append(
			statsParts,
			lipgloss.NewStyle().Foreground(ColorRed).Render(fmt.Sprintf("✕ %d error", errored)),
		)
	}

	// Fallback if no sessions
	stats := ""
	if len(statsParts) > 0 {
		stats = strings.Join(statsParts, statsSep)
	} else {
		stats = lipgloss.NewStyle().Foreground(ColorText).Render("no sessions")
	}

	// Cost tracking segment
	todayMicro := h.costToday.Load()
	weekMicro := h.costWeek.Load()
	if todayMicro > 0 || weekMicro > 0 {
		costStyle := lipgloss.NewStyle().Foreground(ColorCyan)
		costText := costStyle.Render(costs.FormatUSD(todayMicro) + " today")
		stats += statsSep + costText
	}
	_ = weekMicro // reserved for future weekly display

	// System stats segment (CPU, RAM, etc.)
	if h.sysStatsCollector != nil {
		sysStats := h.sysStatsCollector.Get()
		formatted := sysinfo.Format(sysStats, h.sysStatsConfig.GetFormat(), h.sysStatsConfig.GetShow())
		if formatted != "" {
			sysStyle := lipgloss.NewStyle().Foreground(ColorComment)
			stats += statsSep + sysStyle.Render(formatted)
		}
	}

	// Version badge (right-aligned, subtle inline style - no border to keep single line)
	versionStyle := lipgloss.NewStyle().
		Foreground(ColorComment).
		Faint(true)
	versionBadge := versionStyle.Render("v" + Version)

	// Fill remaining header space
	headerLeft := lipgloss.JoinHorizontal(lipgloss.Left, logo, "  ", title, "  ", stats)
	headerPadding := h.width - lipgloss.Width(headerLeft) - lipgloss.Width(versionBadge) - 2
	if headerPadding < 1 {
		headerPadding = 1
	}
	headerContent := headerLeft + strings.Repeat(" ", headerPadding) + versionBadge

	headerBar := lipgloss.NewStyle().
		Background(ColorSurface).
		MaxWidth(h.width).
		Padding(0, 1).
		Render(headerContent)

	b.WriteString(headerBar)
	b.WriteString("\n")

	// ═══════════════════════════════════════════════════════════════════
	// FILTER BAR (quick status filters)
	// ═══════════════════════════════════════════════════════════════════
	// Always show filter bar for consistent layout (prevents viewport jumping)
	filterBarHeight := 1
	b.WriteString(h.renderFilterBar())
	b.WriteString("\n")

	// ═══════════════════════════════════════════════════════════════════
	// UPDATE BANNER (if update available)
	// ═══════════════════════════════════════════════════════════════════
	updateBannerHeight := 0
	if h.updateInfo != nil && h.updateInfo.Available {
		updateBannerHeight = 1
		updateStyle := lipgloss.NewStyle().
			Foreground(ColorBg).
			Background(ColorYellow).
			Bold(true).
			MaxWidth(h.width).
			Align(lipgloss.Center)
		updateText := fmt.Sprintf(" ⬆ Update available: v%s → v%s (run: agent-deck update) ",
			h.updateInfo.CurrentVersion, h.updateInfo.LatestVersion)
		b.WriteString(updateStyle.Render(updateText))
		b.WriteString("\n")
	}

	// ═══════════════════════════════════════════════════════════════════
	// MAINTENANCE BANNER (if maintenance completed recently)
	// ═══════════════════════════════════════════════════════════════════
	maintenanceBannerHeight := 0
	if h.maintenanceMsg != "" {
		maintenanceBannerHeight = 1
		maintStyle := lipgloss.NewStyle().
			Foreground(ColorBg).
			Background(ColorCyan).
			Bold(true).
			MaxWidth(h.width).
			Align(lipgloss.Center)
		b.WriteString(maintStyle.Render(" " + h.maintenanceMsg + " "))
		b.WriteString("\n")
	}

	// ═══════════════════════════════════════════════════════════════════
	// MAIN CONTENT AREA - Responsive layout based on terminal width
	// ═══════════════════════════════════════════════════════════════════
	helpBarHeight := 2 // Help bar takes 2 lines (border + content)
	debugBarHeight := 0
	if h.debugMode {
		debugBarHeight = 1
	}
	// Height breakdown: -1 header, -filterBarHeight filter, -updateBannerHeight banner, -maintenanceBannerHeight maintenance, -helpBarHeight help, -debugBarHeight debug
	contentHeight := h.height - 1 - helpBarHeight - updateBannerHeight - maintenanceBannerHeight - filterBarHeight - debugBarHeight

	// Route to appropriate layout based on terminal width
	layoutMode := h.getLayoutMode()

	var mainContent string
	switch layoutMode {
	case LayoutModeSingle:
		mainContent = h.renderSingleColumnLayout(contentHeight)
	case LayoutModeStacked:
		mainContent = h.renderStackedLayout(contentHeight)
	default: // LayoutModeDual
		mainContent = h.renderDualColumnLayout(contentHeight)
	}

	// Ensure mainContent has exact height
	mainContent = ensureExactHeight(mainContent, contentHeight)
	b.WriteString(mainContent)
	b.WriteString("\n")

	// ═══════════════════════════════════════════════════════════════════
	// HELP BAR (context-aware shortcuts)
	// ═══════════════════════════════════════════════════════════════════
	helpBar := h.renderHelpBar()
	b.WriteString(helpBar)

	// Debug performance overlay (AGENTDECK_DEBUG=1 only)
	if h.debugMode {
		b.WriteString("\n")
		b.WriteString(h.renderDebugBar())
	}

	// Error and warning messages are displayed but may be truncated by final height constraint
	if h.err != nil {
		remaining := 5*time.Second - time.Since(h.errTime)
		if remaining < 0 {
			remaining = 0
		}
		dismissHint := lipgloss.NewStyle().Foreground(ColorText).Render(
			fmt.Sprintf(" (auto-dismiss in %ds)", int(remaining.Seconds())+1))
		errMsg := ErrorStyle.Render("⚠ "+h.err.Error()) + dismissHint
		b.WriteString("\n")
		b.WriteString(errMsg)
	}

	if h.storageWarning != "" {
		warnStyle := lipgloss.NewStyle().Foreground(ColorYellow)
		b.WriteString("\n")
		b.WriteString(warnStyle.Render(h.storageWarning))
	}

	if h.watcherWarning != "" {
		warnStyle := lipgloss.NewStyle().Foreground(ColorYellow)
		b.WriteString("\n")
		b.WriteString(warnStyle.Render("⚠ " + h.watcherWarning))
	}

	// Performance: log render duration when debug mode is active
	if logging.IsDebugEnabled() {
		elapsed := time.Since(renderStart)
		if elapsed > 50*time.Millisecond {
			perfLog.Warn("slow_view_render", slog.Duration("elapsed", elapsed),
				slog.Int("width", h.width), slog.Int("height", h.height),
				slog.Int("sessions", len(h.flatItems)))
		} else {
			perfLog.Debug("view_render", slog.Duration("elapsed", elapsed))
		}
		h.lastRenderDuration.Store(elapsed.Microseconds())
	}

	// CRITICAL: Use ensureExactHeight for robust, consistent output across all platforms
	// This is the single source of truth for output height - guarantees exactly h.height lines
	// regardless of component content, ANSI codes, or terminal differences
	return clampViewToViewport(b.String(), h.width, h.height)
}

// renderPanelTitle creates a styled section title with underline
func (h *Home) renderPanelTitle(title string, width int) string {
	// Truncate title if it exceeds width
	if len(title) > width {
		if width > 3 {
			title = title[:width-3] + "..."
		} else {
			title = title[:width]
		}
	}

	titleStyle := lipgloss.NewStyle().
		Foreground(ColorCyan).
		Bold(true).
		Width(width)

	underlineStyle := lipgloss.NewStyle().
		Foreground(ColorBorder).
		Width(width)

	// Create underline that extends to panel width
	underlineLen := max(0, width)
	underline := underlineStyle.Render(strings.Repeat("─", underlineLen))

	return titleStyle.Render(title) + "\n" + underline
}

// renderLoadingSplash creates a simple centered loading splash screen
// Shows the three status indicators (running/waiting/idle) cycling
func renderLoadingSplash(width, height int, frame int) string {
	// Status indicator cycle: each status lights up in sequence
	// Frame 0-1: Running (green ●)
	// Frame 2-3: Waiting (yellow ◐)
	// Frame 4-5: Idle (gray ○)
	// Frame 6-7: All lit together

	phase := (frame / 2) % 4

	// Active status colors (match the actual TUI colors)
	greenStyle := lipgloss.NewStyle().Foreground(ColorGreen).Bold(true)
	yellowStyle := lipgloss.NewStyle().Foreground(ColorYellow).Bold(true)
	grayStyle := lipgloss.NewStyle().Foreground(ColorTextDim)

	// Dim style for inactive indicators
	dimStyle := lipgloss.NewStyle().Foreground(ColorComment)

	// Text styles
	titleStyle := lipgloss.NewStyle().Foreground(ColorText).Bold(true)
	subtitleStyle := lipgloss.NewStyle().Foreground(ColorTextDim)

	var content strings.Builder

	if width >= 40 && height >= 10 {
		// Full version - big status indicators in a row
		var running, waiting, idle string

		switch phase {
		case 0: // Running highlighted
			running = greenStyle.Render("●")
			waiting = dimStyle.Render("◐")
			idle = dimStyle.Render("○")
		case 1: // Waiting highlighted
			running = dimStyle.Render("●")
			waiting = yellowStyle.Render("◐")
			idle = dimStyle.Render("○")
		case 2: // Idle highlighted
			running = dimStyle.Render("●")
			waiting = dimStyle.Render("◐")
			idle = grayStyle.Render("○")
		case 3: // All lit
			running = greenStyle.Render("●")
			waiting = yellowStyle.Render("◐")
			idle = grayStyle.Render("○")
		}

		content.WriteString("\n")
		content.WriteString("      " + running + "   " + waiting + "   " + idle + "      \n")
		content.WriteString("\n")
		content.WriteString(titleStyle.Render("Agent Deck") + "\n")
		content.WriteString("\n")
		content.WriteString(subtitleStyle.Render("Loading sessions..."))
	} else if width >= 25 && height >= 6 {
		// Compact version
		var indicators string
		switch phase {
		case 0:
			indicators = greenStyle.Render("●") + " " + dimStyle.Render("◐") + " " + dimStyle.Render("○")
		case 1:
			indicators = dimStyle.Render("●") + " " + yellowStyle.Render("◐") + " " + dimStyle.Render("○")
		case 2:
			indicators = dimStyle.Render("●") + " " + dimStyle.Render("◐") + " " + grayStyle.Render("○")
		case 3:
			indicators = greenStyle.Render("●") + " " + yellowStyle.Render("◐") + " " + grayStyle.Render("○")
		}
		content.WriteString(indicators + "\n")
		content.WriteString("\n")
		content.WriteString(titleStyle.Render("Agent Deck") + "\n")
		content.WriteString(subtitleStyle.Render("Loading..."))
	} else {
		// Minimal
		content.WriteString(greenStyle.Render("●") + " " + titleStyle.Render("Agent Deck") + "\n")
		content.WriteString(subtitleStyle.Render("Loading..."))
	}

	// Center the content
	contentStyle := lipgloss.NewStyle().
		Align(lipgloss.Center).
		Width(width)

	rendered := lipgloss.Place(
		width, height,
		lipgloss.Center, lipgloss.Center,
		contentStyle.Render(content.String()),
	)

	return rendered
}

// renderQuittingSplash renders a splash screen during application shutdown
func renderQuittingSplash(width, height int, frame int) string {
	// Status indicator cycle (matches loading splash for consistency)
	phase := (frame / 2) % 4

	// Active status colors
	greenStyle := lipgloss.NewStyle().Foreground(ColorGreen).Bold(true)
	yellowStyle := lipgloss.NewStyle().Foreground(ColorYellow).Bold(true)
	grayStyle := lipgloss.NewStyle().Foreground(ColorTextDim)
	dimStyle := lipgloss.NewStyle().Foreground(ColorComment)

	// Text styles
	titleStyle := lipgloss.NewStyle().Foreground(ColorText).Bold(true)
	subtitleStyle := lipgloss.NewStyle().Foreground(ColorYellow)

	var content strings.Builder

	if width >= 40 && height >= 10 {
		var running, waiting, idle string
		switch phase {
		case 0:
			running = greenStyle.Render("●")
			waiting = dimStyle.Render("◐")
			idle = dimStyle.Render("○")
		case 1:
			running = dimStyle.Render("●")
			waiting = yellowStyle.Render("◐")
			idle = dimStyle.Render("○")
		case 2:
			running = dimStyle.Render("●")
			waiting = dimStyle.Render("◐")
			idle = grayStyle.Render("○")
		case 3:
			running = greenStyle.Render("●")
			waiting = yellowStyle.Render("◐")
			idle = grayStyle.Render("○")
		}

		content.WriteString("\n")
		content.WriteString("      " + running + "   " + waiting + "   " + idle + "      \n")
		content.WriteString("\n")
		content.WriteString(titleStyle.Render("Agent Deck") + "\n")
		content.WriteString("\n")
		content.WriteString(subtitleStyle.Render("Shutting down..."))
	} else {
		// Compact/Minimal
		content.WriteString(titleStyle.Render("Agent Deck") + "\n")
		content.WriteString(subtitleStyle.Render("Shutting down..."))
	}

	contentStyle := lipgloss.NewStyle().
		Align(lipgloss.Center).
		Width(width)

	return lipgloss.Place(
		width, height,
		lipgloss.Center, lipgloss.Center,
		contentStyle.Render(content.String()),
	)
}

// EmptyStateConfig holds content for responsive empty state rendering
type EmptyStateConfig struct {
	Icon     string
	Title    string
	Subtitle string
	Hints    []string // Full list of hints (will be reduced based on space)
}

// renderEmptyStateResponsive creates a centered empty state that adapts to available space
// Uses progressive disclosure: full → compact → minimal based on width/height
func renderEmptyStateResponsive(config EmptyStateConfig, width, height int) string {
	// Determine content tier based on available space
	// Use the more restrictive of width or height constraints
	tier := "full"
	if width < emptyStateWidthCompact || height < emptyStateHeightCompact {
		tier = "minimal"
	} else if width < emptyStateWidthFull || height < emptyStateHeightFull {
		tier = "compact"
	}

	// Adaptive padding based on tier
	var vPad, hPad int
	switch tier {
	case "full":
		vPad, hPad = spacingNormal, spacingLarge
	case "compact":
		vPad, hPad = spacingTight, spacingNormal
	case "minimal":
		vPad, hPad = 0, spacingTight
	}

	// Styles
	iconStyle := lipgloss.NewStyle().
		Foreground(ColorAccent).
		Bold(true)
	titleStyle := lipgloss.NewStyle().
		Foreground(ColorText).
		Bold(true)
	subtitleStyle := lipgloss.NewStyle().
		Foreground(ColorText).
		Italic(true)
	hintStyle := lipgloss.NewStyle().
		Foreground(ColorComment)

	var content strings.Builder

	// Icon - always shown but with adaptive spacing
	content.WriteString(iconStyle.Render(config.Icon))
	if tier == "full" {
		content.WriteString("\n\n")
	} else {
		content.WriteString("\n")
	}

	// Title - always shown
	content.WriteString(titleStyle.Render(config.Title))

	// Subtitle - shown in full and compact modes
	if config.Subtitle != "" && tier != "minimal" {
		content.WriteString("\n")
		// Truncate subtitle if width is tight
		subtitle := config.Subtitle
		maxSubtitleWidth := width - hPad*2 - 4 // Account for padding and margins
		if maxSubtitleWidth > 0 && len(subtitle) > maxSubtitleWidth {
			subtitle = subtitle[:maxSubtitleWidth-3] + "..."
		}
		content.WriteString(subtitleStyle.Render(subtitle))
	}

	// Hints - progressive disclosure based on tier
	if len(config.Hints) > 0 {
		var hintsToShow []string
		switch tier {
		case "full":
			hintsToShow = config.Hints // Show all
		case "compact":
			// Show first 2 hints max
			if len(config.Hints) > 2 {
				hintsToShow = config.Hints[:2]
			} else {
				hintsToShow = config.Hints
			}
		case "minimal":
			// Show only the first (most important) hint
			hintsToShow = config.Hints[:1]
		}

		if tier == "full" {
			content.WriteString("\n\n")
		} else {
			content.WriteString("\n")
		}

		for i, hint := range hintsToShow {
			// Truncate hint if width is tight
			displayHint := hint
			maxHintWidth := width - hPad*2 - 6 // Account for "• " prefix and margins
			if maxHintWidth > 0 && len(displayHint) > maxHintWidth {
				displayHint = displayHint[:maxHintWidth-3] + "..."
			}
			content.WriteString(hintStyle.Render("• " + displayHint))
			if i < len(hintsToShow)-1 {
				content.WriteString("\n")
			}
		}
	}

	contentStyle := lipgloss.NewStyle().
		Foreground(ColorText).
		Align(lipgloss.Center).
		Padding(vPad, hPad).
		MaxWidth(width)

	rendered := contentStyle.Render(content.String())

	// Ensure exact height
	return ensureExactHeight(rendered, height)
}

// ensureExactHeight is a critical helper that ensures any content has EXACTLY n lines.
// This is essential for consistent TUI layout across all platforms and terminal sizes.
//
// Behavior:
//   - If content has fewer lines than n: pads with blank lines at the end
//   - If content has more lines than n: truncates from the end (keeps header/start)
//   - Returns content with exactly n lines (n-1 internal newlines, no trailing newline)
//
// This function handles ANSI-styled content correctly by counting \n characters
// rather than visual lines, which works reliably across all terminal emulators.
func ensureExactHeight(content string, n int) string {
	if n <= 0 {
		return ""
	}

	// Split into lines
	lines := strings.Split(content, "\n")

	// Truncate or pad to exactly n lines
	if len(lines) > n {
		// Keep first n lines (preserves header info)
		lines = lines[:n]
	} else if len(lines) < n {
		// Pad with blank lines
		for len(lines) < n {
			lines = append(lines, "")
		}
	}

	// Join back - this creates n-1 newlines for n lines
	return strings.Join(lines, "\n")
}

// clampViewToViewport hard-clamps the rendered frame to the terminal viewport.
// This is the final safety net against any component returning an unexpected
// extra line or a line that still exceeds the viewport width.
func clampViewToViewport(content string, width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}

	lines := strings.Split(content, "\n")
	if len(lines) > height {
		lines = lines[:height]
	} else if len(lines) < height {
		for len(lines) < height {
			lines = append(lines, "")
		}
	}

	for i, line := range lines {
		if ansi.StringWidth(line) > width {
			lines[i] = ansi.Truncate(line, width, "")
		}
	}

	return strings.Join(lines, "\n")
}

// ensureExactWidth ensures each line in content has exactly the specified visual width.
// This is essential for proper horizontal panel alignment in lipgloss.JoinHorizontal.
//
// CRITICAL: Uses lipgloss.Width() for measurement to stay consistent with
// lipgloss.JoinHorizontal's internal width calculation. Using a different
// measurement (e.g. runewidth.StringWidth after custom ANSI stripping) can
// disagree by even 1 character, causing JoinHorizontal to pad all lines to
// the wider measurement. This makes the joined output exceed terminal width,
// lines wrap, and Bubble Tea's renderer loses cursor tracking — producing
// duplicated/stacked content (the "scrolling artifact" bug).
//
// Behavior:
//   - Measures width using lipgloss.Width (same as JoinHorizontal)
//   - Pads short lines with spaces to reach target width
//   - Truncates long lines using lipgloss.MaxWidth (preserves ANSI where possible)
//   - Guarantees every line is exactly `width` visual characters
func ensureExactWidth(content string, width int) string {
	if width <= 0 {
		return content
	}

	lines := strings.Split(content, "\n")
	result := make([]string, len(lines))

	for i, line := range lines {
		// Measure visual width using lipgloss (same measurement as JoinHorizontal)
		displayWidth := lipgloss.Width(line)

		if displayWidth == width {
			result[i] = line
		} else if displayWidth < width {
			// Pad with spaces to reach target width
			result[i] = line + strings.Repeat(" ", width-displayWidth)
		} else {
			// Line too wide — truncate using lipgloss for consistent ANSI handling
			truncated := lipgloss.NewStyle().MaxWidth(width).Render(line)
			// Verify and pad if truncation left it short
			truncWidth := lipgloss.Width(truncated)
			if truncWidth < width {
				truncated += strings.Repeat(" ", width-truncWidth)
			}
			result[i] = truncated
		}
	}

	return strings.Join(result, "\n")
}

// renderDualColumnLayout renders side-by-side panels for wide terminals (80+ cols)
func (h *Home) renderDualColumnLayout(contentHeight int) string {
	var b strings.Builder

	// Calculate panel widths (35% left, 65% right for more preview space)
	leftWidth := int(float64(h.width) * 0.35)
	rightWidth := h.width - leftWidth - 3 // -3 for separator

	// Panel title is exactly 2 lines (title + underline)
	// Panel content gets the remaining space: contentHeight - 2
	panelTitleLines := 2
	panelContentHeight := contentHeight - panelTitleLines

	// Build left panel (session list) with styled title
	leftTitle := h.renderPanelTitle("SESSIONS", leftWidth)
	leftContent := h.renderSessionList(leftWidth, panelContentHeight)
	// CRITICAL: Ensure left content has exactly panelContentHeight lines
	leftContent = ensureExactHeight(leftContent, panelContentHeight)
	leftPanel := leftTitle + "\n" + leftContent

	// Build right panel (preview) with styled title
	rightTitle := h.renderPanelTitle("PREVIEW", rightWidth)
	rightContent := h.renderPreviewPane(rightWidth, panelContentHeight)
	// CRITICAL: Ensure right content has exactly panelContentHeight lines
	rightContent = ensureExactHeight(rightContent, panelContentHeight)
	rightPanel := rightTitle + "\n" + rightContent

	// Build separator - must be exactly contentHeight lines
	separatorStyle := lipgloss.NewStyle().Foreground(ColorBorder)
	separatorLines := make([]string, contentHeight)
	for i := range separatorLines {
		separatorLines[i] = separatorStyle.Render(" │ ")
	}
	separator := strings.Join(separatorLines, "\n")

	// CRITICAL: Ensure both panels have exactly contentHeight lines before joining
	leftPanel = ensureExactHeight(leftPanel, contentHeight)
	rightPanel = ensureExactHeight(rightPanel, contentHeight)

	// CRITICAL: Ensure both panels have exactly the correct width for proper alignment
	// Without this, variable-width lines cause JoinHorizontal to misalign content
	leftPanel = ensureExactWidth(leftPanel, leftWidth)
	rightPanel = ensureExactWidth(rightPanel, rightWidth)

	// Join panels horizontally - all components have exact heights AND widths now
	mainContent := lipgloss.JoinHorizontal(lipgloss.Top, leftPanel, separator, rightPanel)

	// Safety net: enforce per-line MaxWidth on the joined output.
	// Even with ensureExactWidth, JoinHorizontal can produce lines wider than
	// h.width due to separator ANSI codes or rounding. Any line that wraps in the
	// terminal adds a visual line, which shifts Bubble Tea's cursor tracking and
	// causes duplicated/stacked content on scroll.
	mainContent = lipgloss.NewStyle().MaxWidth(h.width).Render(mainContent)

	b.WriteString(mainContent)

	return b.String()
}

// renderStackedLayout renders list above preview for medium terminals (50-79 cols)
func (h *Home) renderStackedLayout(totalHeight int) string {
	var b strings.Builder

	// Split height: 60% list, 40% preview
	listHeight := (totalHeight * 60) / 100
	previewHeight := totalHeight - listHeight - 1 // -1 for separator

	if listHeight < 5 {
		listHeight = 5
	}
	if previewHeight < 3 {
		previewHeight = 3
	}

	// Session list (full width)
	listTitle := h.renderPanelTitle("SESSIONS", h.width)
	listContent := h.renderSessionList(h.width, listHeight-2) // -2 for title
	listContent = ensureExactHeight(listContent, listHeight-2)
	b.WriteString(listTitle)
	b.WriteString("\n")
	b.WriteString(listContent)
	b.WriteString("\n")

	// Separator
	sepStyle := lipgloss.NewStyle().Foreground(ColorBorder)
	b.WriteString(sepStyle.Render(strings.Repeat("─", max(0, h.width))))
	b.WriteString("\n")

	// Preview (full width)
	previewTitle := h.renderPanelTitle("PREVIEW", h.width)
	previewContent := h.renderPreviewPane(h.width, previewHeight-2) // -2 for title
	previewContent = ensureExactHeight(previewContent, previewHeight-2)
	b.WriteString(previewTitle)
	b.WriteString("\n")
	b.WriteString(previewContent)

	return b.String()
}

// renderSingleColumnLayout renders list only for narrow terminals (<50 cols)
func (h *Home) renderSingleColumnLayout(totalHeight int) string {
	var b strings.Builder

	// Full height for list
	listHeight := totalHeight - 2 // -2 for title

	listTitle := h.renderPanelTitle("SESSIONS", h.width)
	listContent := h.renderSessionList(h.width, listHeight)
	listContent = ensureExactHeight(listContent, listHeight)

	b.WriteString(listTitle)
	b.WriteString("\n")
	b.WriteString(listContent)

	return b.String()
}

// renderSectionDivider creates a modern section divider with optional centered label
// Format: ─────────── Label ─────────── (lines extend to fill width)
func renderSectionDivider(label string, width int) string {
	lineStyle := lipgloss.NewStyle().Foreground(ColorBorder)

	if label == "" {
		return lineStyle.Render(strings.Repeat("─", max(0, width)))
	}

	// Label with subtle background for better visibility
	labelStyle := lipgloss.NewStyle().
		Foreground(ColorText).
		Bold(true)

	// Calculate side widths
	labelWidth := len(label) + 2 // +2 for spacing on each side of label
	sideWidth := (width - labelWidth) / 2
	if sideWidth < 3 {
		sideWidth = 3
	}

	return lineStyle.Render(strings.Repeat("─", sideWidth)) +
		" " + labelStyle.Render(label) + " " +
		lineStyle.Render(strings.Repeat("─", sideWidth))
}

// renderToolStatusLine renders a Status + Session line for a tool section.
// sessionID is the detected session ID (empty = not connected).
// detectedAt is when detection ran (zero = still detecting, used only when threeState is true).
// threeState enables the "Detecting..." intermediate state (for tools like OpenCode/Codex).
func renderToolStatusLine(b *strings.Builder, sessionID string, detectedAt time.Time, threeState bool) {
	labelStyle := lipgloss.NewStyle().Foreground(ColorText)
	valueStyle := lipgloss.NewStyle().Foreground(ColorText)

	if sessionID != "" {
		statusStyle := lipgloss.NewStyle().Foreground(ColorGreen).Bold(true)
		b.WriteString(labelStyle.Render("Status:  "))
		b.WriteString(statusStyle.Render("● Connected"))
		b.WriteString("\n")

		b.WriteString(labelStyle.Render("Session: "))
		b.WriteString(valueStyle.Render(sessionID))
		b.WriteString("\n")
	} else if threeState && detectedAt.IsZero() {
		statusStyle := lipgloss.NewStyle().Foreground(ColorYellow)
		b.WriteString(labelStyle.Render("Status:  "))
		b.WriteString(statusStyle.Render("◐ Detecting session..."))
		b.WriteString("\n")
	} else {
		statusStyle := lipgloss.NewStyle().Foreground(ColorText)
		b.WriteString(labelStyle.Render("Status:  "))
		if threeState {
			b.WriteString(statusStyle.Render("○ No session found"))
		} else {
			b.WriteString(statusStyle.Render("○ Not connected"))
		}
		b.WriteString("\n")
	}
}

// renderDetectedAtLine renders a "Detected: X ago" line.
func renderDetectedAtLine(b *strings.Builder, detectedAt time.Time) {
	if detectedAt.IsZero() {
		return
	}
	labelStyle := lipgloss.NewStyle().Foreground(ColorText)
	dimStyle := lipgloss.NewStyle().Foreground(ColorText).Italic(true)
	b.WriteString(labelStyle.Render("Detected:"))
	b.WriteString(dimStyle.Render(" " + formatRelativeTime(detectedAt)))
	b.WriteString("\n")
}

// renderForkHintLine renders the fork keyboard hint line.
func (h *Home) renderForkHintLine(b *strings.Builder) {
	quickForkKey := h.actionKey(hotkeyQuickFork)
	forkWithOptionsKey := h.actionKey(hotkeyForkWithOptions)
	if quickForkKey == "" && forkWithOptionsKey == "" {
		return
	}

	hintStyle := lipgloss.NewStyle().Foreground(ColorText).Italic(true)
	keyStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	b.WriteString(hintStyle.Render("Fork:    "))
	if quickForkKey != "" {
		b.WriteString(keyStyle.Render(quickForkKey))
		b.WriteString(hintStyle.Render(" quick fork"))
	}
	if quickForkKey != "" && forkWithOptionsKey != "" {
		b.WriteString(hintStyle.Render(", "))
	}
	if forkWithOptionsKey != "" {
		b.WriteString(keyStyle.Render(forkWithOptionsKey))
		b.WriteString(hintStyle.Render(" fork with options"))
	}
	b.WriteString("\n")
}

// renderSimpleMCPLine renders MCPs without sync status (for Gemini and other tools).
// Width-aware truncation shows "(+N more)" when MCPs don't fit.
func renderSimpleMCPLine(b *strings.Builder, mcpInfo *session.MCPInfo, width int) {
	if mcpInfo == nil || !mcpInfo.HasAny() {
		return
	}

	labelStyle := lipgloss.NewStyle().Foreground(ColorText)
	valueStyle := lipgloss.NewStyle().Foreground(ColorText)

	var mcpParts []string
	for _, name := range mcpInfo.Global {
		mcpParts = append(mcpParts, valueStyle.Render(name+" (g)"))
	}
	for _, name := range mcpInfo.Project {
		mcpParts = append(mcpParts, valueStyle.Render(name+" (p)"))
	}
	for _, mcp := range mcpInfo.LocalMCPs {
		mcpParts = append(mcpParts, valueStyle.Render(mcp.Name+" (l)"))
	}

	if len(mcpParts) == 0 {
		return
	}

	b.WriteString(labelStyle.Render("MCPs:    "))

	mcpMaxWidth := width - 4 - 9
	if mcpMaxWidth < 20 {
		mcpMaxWidth = 20
	}

	var mcpResult strings.Builder
	mcpCount := 0
	currentWidth := 0

	for i, part := range mcpParts {
		plainPart := tmux.StripANSI(part)
		partWidth := runewidth.StringWidth(plainPart)

		addedWidth := partWidth
		if mcpCount > 0 {
			addedWidth += 2
		}

		remaining := len(mcpParts) - i
		isLast := remaining == 1

		var wouldExceed bool
		if isLast {
			wouldExceed = currentWidth+addedWidth > mcpMaxWidth
		} else {
			moreIndicator := fmt.Sprintf(" (+%d more)", remaining)
			moreWidth := runewidth.StringWidth(moreIndicator)
			wouldExceed = currentWidth+addedWidth+moreWidth > mcpMaxWidth
		}

		if wouldExceed {
			moreStyle := lipgloss.NewStyle().Foreground(ColorText).Italic(true)
			if mcpCount > 0 {
				mcpResult.WriteString(moreStyle.Render(fmt.Sprintf(" (+%d more)", remaining)))
			} else {
				mcpResult.WriteString(moreStyle.Render(fmt.Sprintf("(%d MCPs)", len(mcpParts))))
			}
			break
		}

		if mcpCount > 0 {
			mcpResult.WriteString(", ")
		}
		mcpResult.WriteString(part)
		currentWidth += addedWidth
		mcpCount++
	}

	b.WriteString(mcpResult.String())
	b.WriteString("\n")
}

// renderHelpBar renders context-aware keyboard shortcuts, adapting to terminal width
func (h *Home) renderHelpBar() string {
	// Route to appropriate tier based on width
	switch {
	case h.width < layoutBreakpointSingle:
		return h.renderHelpBarTiny()
	case h.width < 70:
		return h.renderHelpBarMinimal()
	case h.width < 100:
		return h.renderHelpBarCompact()
	default:
		return h.renderHelpBarFull()
	}
}

// renderHelpBarTiny renders minimal help for very narrow terminals (<50 cols)
func (h *Home) renderHelpBarTiny() string {
	borderStyle := lipgloss.NewStyle().Foreground(ColorBorder)
	border := borderStyle.Render(strings.Repeat("─", max(0, h.width)))

	hintStyle := lipgloss.NewStyle().Foreground(ColorComment)
	helpKey := h.actionKey(hotkeyHelp)
	var hint string
	if h.jumpMode {
		hint = lipgloss.NewStyle().Foreground(ColorYellow).Bold(true).Render("Jump: a-z/esc")
	} else {
		hintText := "Help key unbound"
		if helpKey != "" {
			hintText = helpKey + " for help"
		}
		hint = hintStyle.Render(hintText)
	}

	// Center the hint
	padding := (h.width - lipgloss.Width(hint)) / 2
	if padding < 0 {
		padding = 0
	}
	content := strings.Repeat(" ", padding) + hint

	raw := lipgloss.JoinVertical(lipgloss.Left, border, content)
	return lipgloss.NewStyle().MaxWidth(h.width).Render(raw)
}

// renderHelpBarMinimal renders keys-only help for narrow terminals (50-69 cols)
func (h *Home) renderHelpBarMinimal() string {
	borderStyle := lipgloss.NewStyle().Foreground(ColorBorder)
	border := borderStyle.Render(strings.Repeat("─", max(0, h.width)))

	keyStyle := lipgloss.NewStyle().
		Foreground(ColorBg).
		Background(ColorAccent).
		Bold(true)
	sepStyle := lipgloss.NewStyle().Foreground(ColorBorder)
	sep := sepStyle.Render(" │ ")
	renderKeys := func(keys ...string) string {
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			if strings.TrimSpace(key) == "" {
				continue
			}
			parts = append(parts, keyStyle.Render(key))
		}
		return strings.Join(parts, " ")
	}

	// Context-specific keys (left side)
	var contextKeys string
	newKey := h.actionKey(hotkeyNewSession)
	quickKey := h.actionKey(hotkeyQuickCreate)
	importKey := h.actionKey(hotkeyImport)
	groupKey := h.actionKey(hotkeyCreateGroup)
	restartKey := h.actionKey(hotkeyRestart)
	restartFreshKey := h.actionKey(hotkeyRestartFresh)
	forkKey := h.actionKey(hotkeyQuickFork)
	mcpKey := h.actionKey(hotkeyMCPManager)
	skillsKey := h.actionKey(hotkeySkillsManager)
	notesKey := h.actionKey(hotkeyEditNotes)
	if cfg, _ := session.LoadUserConfig(); cfg != nil && !cfg.GetShowNotes() {
		notesKey = ""
	}
	if h.jumpMode {
		contextKeys = keyStyle.Render("a-z") + " " + keyStyle.Render("esc")
		if h.jumpBuffer != "" {
			bufStyle := lipgloss.NewStyle().Foreground(ColorYellow).Bold(true)
			contextKeys = bufStyle.Render(h.jumpBuffer+"…") + " " + contextKeys
		}
	} else if len(h.flatItems) == 0 {
		contextKeys = renderKeys(newKey, quickKey, importKey, groupKey)
	} else if h.cursor < len(h.flatItems) {
		item := h.flatItems[h.cursor]
		if item.Type == session.ItemTypeGroup {
			contextKeys = renderKeys("⏎", newKey, quickKey, groupKey)
		} else {
			contextKeys = renderKeys("⏎", newKey, quickKey, restartKey)
			if item.Session != nil && item.Session.CanRestartFresh() {
				freshRendered := renderKeys(restartFreshKey)
				if freshRendered != "" {
					contextKeys += " " + freshRendered
				}
			}
			if item.Session != nil && item.Session.CanFork() {
				forkRendered := renderKeys(forkKey)
				if forkRendered != "" {
					contextKeys += " " + forkRendered
				}
			}
			if item.Session != nil && (session.IsClaudeCompatible(item.Session.Tool) || item.Session.Tool == "gemini") {
				mcpRendered := renderKeys(mcpKey)
				if mcpRendered != "" {
					contextKeys += " " + mcpRendered
				}
			}
			if item.Session != nil && session.SupportsProjectSkills(item.Session.Tool) {
				skillsRendered := renderKeys(skillsKey)
				if skillsRendered != "" {
					contextKeys += " " + skillsRendered
				}
			}
			notesRendered := renderKeys(notesKey)
			if notesRendered != "" {
				contextKeys += " " + notesRendered
			}
		}
	}

	// Global keys (right side)
	globalStyle := lipgloss.NewStyle().Foreground(ColorComment)
	globalParts := []string{globalStyle.Render("↑↓")}
	if key := h.actionKey(hotkeySearch); key != "" {
		globalParts = append(globalParts, globalStyle.Render(key))
	}
	if key := h.actionKey(hotkeySettings); key != "" {
		globalParts = append(globalParts, globalStyle.Render(key))
	}
	if key := h.actionKey(hotkeyHelp); key != "" {
		globalParts = append(globalParts, globalStyle.Render(key))
	}
	if key := h.actionKey(hotkeyQuit); key != "" {
		globalParts = append(globalParts, globalStyle.Render(key))
	}
	globalKeys := strings.Join(globalParts, " ")
	if contextKeys == "" {
		contextKeys = globalStyle.Render("No actions bound")
	}

	// Calculate padding
	leftPart := contextKeys
	rightPart := globalKeys
	padding := h.width - lipgloss.Width(leftPart) - lipgloss.Width(rightPart) - 4
	if padding < 2 {
		// Content too wide for one line — drop right part to avoid overflow
		padding = 2
		rightPart = ""
	}

	content := leftPart + sep + strings.Repeat(" ", padding) + rightPart
	if rightPart == "" {
		content = leftPart
	}

	raw := lipgloss.JoinVertical(lipgloss.Left, border, content)
	return lipgloss.NewStyle().MaxWidth(h.width).Render(raw)
}

// renderHelpBarCompact renders abbreviated help for medium terminals (70-99 cols)
func (h *Home) renderHelpBarCompact() string {
	borderStyle := lipgloss.NewStyle().Foreground(ColorBorder)
	border := borderStyle.Render(strings.Repeat("─", max(0, h.width)))

	sepStyle := lipgloss.NewStyle().Foreground(ColorBorder)
	sep := sepStyle.Render(" │ ")
	newQuickKey := joinHotkeyLabels(h.actionKey(hotkeyNewSession), h.actionKey(hotkeyQuickCreate))
	restartFreshKey := h.actionKey(hotkeyRestartFresh)

	// Abbreviated key+short desc
	var contextHints []string
	if len(h.flatItems) == 0 {
		if newQuickKey != "" {
			contextHints = append(contextHints, h.helpKeyShort(newQuickKey, "New"))
		}
		if key := h.actionKey(hotkeyImport); key != "" {
			contextHints = append(contextHints, h.helpKeyShort(key, "Import"))
		}
	} else if h.cursor < len(h.flatItems) {
		item := h.flatItems[h.cursor]
		if item.Type == session.ItemTypeGroup {
			contextHints = append(contextHints, h.helpKeyShort("⏎", "Toggle"))
			if newQuickKey != "" {
				contextHints = append(contextHints, h.helpKeyShort(newQuickKey, "New"))
			}
		} else {
			contextHints = append(contextHints, h.helpKeyShort("⏎", "Attach"))
			if newQuickKey != "" {
				contextHints = append(contextHints, h.helpKeyShort(newQuickKey, "New"))
			}
			if key := h.actionKey(hotkeyRestart); key != "" {
				contextHints = append(contextHints, h.helpKeyShort(key, "Restart"))
			}
			if item.Session != nil && item.Session.CanRestartFresh() && restartFreshKey != "" {
				contextHints = append(contextHints, h.helpKeyShort(restartFreshKey, "Fresh"))
			}
			if item.Session != nil && item.Session.CanFork() {
				if key := h.actionKey(hotkeyQuickFork); key != "" {
					contextHints = append(contextHints, h.helpKeyShort(key, "Fork"))
				}
			}
			if item.Session != nil && (session.IsClaudeCompatible(item.Session.Tool) || item.Session.Tool == "gemini") {
				if key := h.actionKey(hotkeyMCPManager); key != "" {
					contextHints = append(contextHints, h.helpKeyShort(key, "MCP"))
				}
				if key := h.actionKey(hotkeyTogglePreview); key != "" {
					contextHints = append(contextHints, h.helpKeyShort(key, h.previewModeShort()))
				}
			}
			if item.Session != nil && session.SupportsProjectSkills(item.Session.Tool) {
				if key := h.actionKey(hotkeySkillsManager); key != "" {
					contextHints = append(contextHints, h.helpKeyShort(key, "Skills"))
				}
			}
			if key := h.actionKey(hotkeyCopyOutput); key != "" {
				contextHints = append(contextHints, h.helpKeyShort(key, "Copy"))
			}
			if key := h.actionKey(hotkeySendOutput); key != "" {
				contextHints = append(contextHints, h.helpKeyShort(key, "Send"))
			}
			if key := h.actionKey(hotkeyEditNotes); key != "" {
				contextHints = append(contextHints, h.helpKeyShort(key, "Notes"))
			}
		}
	}

	// Show undo hint when undo stack is non-empty
	if len(h.undoStack) > 0 {
		if key := h.actionKey(hotkeyUndoDelete); key != "" {
			contextHints = append(contextHints, h.helpKeyShort(key, "Undo"))
		}
	}

	// Jump mode overrides all context hints
	if h.jumpMode {
		contextHints = []string{
			h.helpKeyShort("a-z", "Hint"),
			h.helpKeyShort("esc", "Cancel"),
		}
		if h.jumpBuffer != "" {
			bufStyle := lipgloss.NewStyle().Foreground(ColorYellow).Bold(true)
			contextHints = append([]string{bufStyle.Render(h.jumpBuffer + "…")}, contextHints...)
		}
	}

	// Global hints (abbreviated)
	globalStyle := lipgloss.NewStyle().Foreground(ColorComment)
	globalParts := []string{globalStyle.Render("↑↓ Nav")}
	if key := h.actionKey(hotkeySearch); key != "" {
		globalParts = append(globalParts, globalStyle.Render(key))
	}
	if key := h.actionKey(hotkeySettings); key != "" {
		globalParts = append(globalParts, globalStyle.Render(key))
	}
	if key := h.actionKey(hotkeyHelp); key != "" {
		globalParts = append(globalParts, globalStyle.Render(key))
	}
	if key := h.actionKey(hotkeyQuit); key != "" {
		globalParts = append(globalParts, globalStyle.Render(key))
	}
	globalHints := strings.Join(globalParts, " ")

	leftPart := strings.Join(contextHints, " ")
	rightPart := globalHints
	padding := h.width - lipgloss.Width(leftPart) - lipgloss.Width(rightPart) - 4
	if padding < 2 {
		// Content too wide for one line — drop right part to avoid overflow
		padding = 2
		rightPart = ""
	}

	content := leftPart + sep + strings.Repeat(" ", padding) + rightPart

	raw := lipgloss.JoinVertical(lipgloss.Left, border, content)
	return lipgloss.NewStyle().MaxWidth(h.width).Render(raw)
}

// helpKeyShort formats a compact keyboard shortcut (no padding)
func (h *Home) helpKeyShort(key, desc string) string {
	keyStyle := lipgloss.NewStyle().
		Foreground(ColorBg).
		Background(ColorAccent).
		Bold(true)
	descStyle := lipgloss.NewStyle().Foreground(ColorText)
	return keyStyle.Render(key) + descStyle.Render(desc)
}

// previewModeShort returns a short description of current preview mode for help bar
func (h *Home) previewModeShort() string {
	switch h.previewMode {
	case PreviewModeOutput:
		return "Out"
	case PreviewModeAnalytics:
		return "Stats"
	default:
		return "Both"
	}
}

// renderHelpBarFull renders context-aware keyboard shortcuts with visual grouping (100+ cols)
func (h *Home) renderHelpBarFull() string {
	// Separator style for grouping related actions
	sepStyle := lipgloss.NewStyle().Foreground(ColorBorder)
	sep := sepStyle.Render(" │ ")
	newQuickKey := joinHotkeyLabels(h.actionKey(hotkeyNewSession), h.actionKey(hotkeyQuickCreate))
	renameKey := h.actionKey(hotkeyRename)
	restartKey := h.actionKey(hotkeyRestart)
	restartFreshKey := h.actionKey(hotkeyRestartFresh)
	deleteKey := h.actionKey(hotkeyDelete)
	closeKey := h.actionKey(hotkeyCloseSession)
	groupKey := h.actionKey(hotkeyCreateGroup)
	moveKey := h.actionKey(hotkeyMoveToGroup)
	mcpKey := h.actionKey(hotkeyMCPManager)
	skillsKey := h.actionKey(hotkeySkillsManager)
	previewKey := h.actionKey(hotkeyTogglePreview)
	forkKeys := joinHotkeyLabels(h.actionKey(hotkeyQuickFork), h.actionKey(hotkeyForkWithOptions))
	copyKey := h.actionKey(hotkeyCopyOutput)
	sendKey := h.actionKey(hotkeySendOutput)
	execShellKey := h.actionKey(hotkeyExecShell)
	notesKey := h.actionKey(hotkeyEditNotes)
	if cfg, _ := session.LoadUserConfig(); cfg != nil && !cfg.GetShowNotes() {
		notesKey = ""
	}
	undoKey := h.actionKey(hotkeyUndoDelete)

	// Determine context-specific hints grouped by action type
	var primaryHints []string   // Main actions (attach, toggle, etc.)
	var secondaryHints []string // Edit actions (rename, move, delete)
	var contextTitle string

	if len(h.flatItems) == 0 {
		contextTitle = "Empty"
		if newQuickKey != "" {
			primaryHints = append(primaryHints, h.helpKey(newQuickKey, "New/Quick"))
		}
		if key := h.actionKey(hotkeyImport); key != "" {
			primaryHints = append(primaryHints, h.helpKey(key, "Import"))
		}
		if groupKey != "" {
			primaryHints = append(primaryHints, h.helpKey(groupKey, "Group"))
		}
	} else if h.cursor < len(h.flatItems) {
		item := h.flatItems[h.cursor]
		if item.Type == session.ItemTypeGroup {
			contextTitle = "Group"
			primaryHints = append(primaryHints, h.helpKey("Tab", "Toggle"))
			if newQuickKey != "" {
				primaryHints = append(primaryHints, h.helpKey(newQuickKey, "New/Quick"))
			}
			if groupKey != "" {
				primaryHints = append(primaryHints, h.helpKey(groupKey, "Group"))
			}
			if renameKey != "" {
				secondaryHints = append(secondaryHints, h.helpKey(renameKey, "Rename"))
			}
			if deleteKey != "" {
				secondaryHints = append(secondaryHints, h.helpKey(deleteKey, "Delete"))
			}
		} else {
			contextTitle = "Session"
			primaryHints = append(primaryHints, h.helpKey("Enter", "Attach"))
			if newQuickKey != "" {
				primaryHints = append(primaryHints, h.helpKey(newQuickKey, "New/Quick"))
			}
			if groupKey != "" {
				primaryHints = append(primaryHints, h.helpKey(groupKey, "Group"))
			}
			if restartKey != "" {
				primaryHints = append(primaryHints, h.helpKey(restartKey, "Restart"))
			}
			if item.Session != nil && item.Session.CanRestartFresh() && restartFreshKey != "" {
				primaryHints = append(primaryHints, h.helpKey(restartFreshKey, "Restart Fresh"))
			}
			// Only show fork hints if session has a valid Claude session ID
			if item.Session != nil && item.Session.CanFork() {
				if forkKeys != "" {
					primaryHints = append(primaryHints, h.helpKey(forkKeys, "Fork"))
				}
			}
			// Show MCP Manager and preview mode toggle for Claude and Gemini sessions
			if item.Session != nil && (session.IsClaudeCompatible(item.Session.Tool) || item.Session.Tool == "gemini") {
				if mcpKey != "" {
					primaryHints = append(primaryHints, h.helpKey(mcpKey, "MCP"))
				}
				if previewKey != "" {
					primaryHints = append(primaryHints, h.helpKey(previewKey, h.previewModeShort()))
				}
			}
			if item.Session != nil && session.SupportsProjectSkills(item.Session.Tool) {
				if skillsKey != "" {
					primaryHints = append(primaryHints, h.helpKey(skillsKey, "Skills"))
				}
			}
			if item.Session != nil && item.Session.IsSandboxed() {
				if execShellKey != "" {
					primaryHints = append(primaryHints, h.helpKey(execShellKey, "Exec"))
				}
			}
			if item.Session != nil && item.Session.IsMultiRepo() {
				if editPathsKey := h.actionKey(hotkeyEditPaths); editPathsKey != "" {
					primaryHints = append(primaryHints, h.helpKey(editPathsKey, "Paths"))
				}
			}
			if copyKey != "" {
				primaryHints = append(primaryHints, h.helpKey(copyKey, "Copy"))
			}
			if sendKey != "" {
				primaryHints = append(primaryHints, h.helpKey(sendKey, "Send"))
			}
			if notesKey != "" {
				primaryHints = append(primaryHints, h.helpKey(notesKey, "Notes"))
			}
			if renameKey != "" {
				secondaryHints = append(secondaryHints, h.helpKey(renameKey, "Rename"))
			}
			if moveKey != "" {
				secondaryHints = append(secondaryHints, h.helpKey(moveKey, "Move"))
			}
			if deleteKey != "" {
				secondaryHints = append(secondaryHints, h.helpKey(deleteKey, "Delete"))
			}
			if closeKey != "" {
				secondaryHints = append(secondaryHints, h.helpKey(closeKey, "Close"))
			}
		}
	}

	// Show undo hint when undo stack is non-empty
	if len(h.undoStack) > 0 {
		if undoKey != "" {
			secondaryHints = append(secondaryHints, h.helpKey(undoKey, "Undo"))
		}
	}

	// Jump mode overrides all context hints
	if h.jumpMode {
		contextTitle = "Jump"
		primaryHints = []string{
			h.helpKey("a-z", "Type hint"),
			h.helpKey("esc", "Cancel"),
		}
		if h.jumpBuffer != "" {
			bufStyle := lipgloss.NewStyle().Foreground(ColorYellow).Bold(true)
			primaryHints = append([]string{bufStyle.Render(h.jumpBuffer + "…")}, primaryHints...)
		}
		secondaryHints = nil
	}

	// Top border
	borderStyle := lipgloss.NewStyle().Foreground(ColorBorder)
	border := borderStyle.Render(strings.Repeat("─", max(0, h.width)))

	// Context indicator with subtle styling
	ctxStyle := lipgloss.NewStyle().
		Foreground(ColorPurple).
		Bold(true)
	contextLabel := ctxStyle.Render(contextTitle + ":")

	// Build shortcuts line with visual grouping
	var shortcutsLine string
	shortcutsLine = strings.Join(primaryHints, " ")
	if len(secondaryHints) > 0 {
		shortcutsLine += sep + strings.Join(secondaryHints, " ")
	}

	// Reload indicator
	var reloadIndicator string
	h.reloadMu.Lock()
	reloading := h.isReloading
	h.reloadMu.Unlock()
	if reloading {
		reloadStyle := lipgloss.NewStyle().
			Foreground(ColorYellow).
			Bold(true)
		reloadIndicator = reloadStyle.Render("⟳ Reloading...")
	}

	// Global shortcuts (right side) - more compact with separators
	globalStyle := lipgloss.NewStyle().Foreground(ColorComment)
	globalParts := []string{globalStyle.Render("↑↓ Nav")}
	if key := h.actionKey(hotkeySearch); key != "" {
		globalParts = append(globalParts, globalStyle.Render(key+" Search"))
	}
	globalParts = append(globalParts, globalStyle.Render("G Global"))
	if key := h.actionKey(hotkeySettings); key != "" {
		globalParts = append(globalParts, globalStyle.Render(key+" Settings"))
	}
	if key := h.actionKey(hotkeyHelp); key != "" {
		globalParts = append(globalParts, globalStyle.Render(key+" Help"))
	}
	if key := h.actionKey(hotkeyQuit); key != "" {
		globalParts = append(globalParts, globalStyle.Render(key+" Quit"))
	}
	globalHints := strings.Join(globalParts, sep)

	// Calculate spacing between left (context) and right (global) portions
	leftPart := contextLabel + " " + shortcutsLine
	if reloadIndicator != "" {
		leftPart = reloadIndicator + sep + leftPart
	}
	rightPart := globalHints
	padding := h.width - lipgloss.Width(leftPart) - lipgloss.Width(rightPart) - spacingNormal
	if padding < spacingNormal {
		// Content too wide for one line — drop right part to avoid overflow
		padding = spacingNormal
		rightPart = ""
	}

	helpContent := leftPart + strings.Repeat(" ", padding) + rightPart

	raw := lipgloss.JoinVertical(lipgloss.Left, border, helpContent)
	return lipgloss.NewStyle().MaxWidth(h.width).Render(raw)
}

// helpKey formats a keyboard shortcut for the help bar
func (h *Home) helpKey(key, desc string) string {
	keyStyle := lipgloss.NewStyle().
		Foreground(ColorBg).
		Background(ColorAccent).
		Bold(true).
		Padding(0, 1)
	descStyle := lipgloss.NewStyle().Foreground(ColorText)
	return keyStyle.Render(key) + " " + descStyle.Render(desc)
}

// renderDebugBar renders a compact performance overlay for debug mode.
// Shows: render time, goroutine count, heap usage, session count.
func (h *Home) renderDebugBar() string {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	renderUs := h.lastRenderDuration.Load()
	goroutines := runtime.NumGoroutine()
	heapMB := float64(memStats.HeapAlloc) / (1024 * 1024)
	sessionCount := len(h.flatItems)

	debugText := fmt.Sprintf(
		" DEBUG | render: %dus | goroutines: %d | heap: %.1fMB | items: %d ",
		renderUs, goroutines, heapMB, sessionCount,
	)

	debugStyle := lipgloss.NewStyle().
		Foreground(ColorBg).
		Background(lipgloss.Color("#FF6600")).
		Bold(true).
		MaxWidth(h.width)

	return debugStyle.Render(debugText)
}

// renderSessionList renders the left panel with hierarchical session list
func (h *Home) renderSessionList(width, height int) string {
	var b strings.Builder

	if len(h.flatItems) == 0 {
		// Responsive empty state - adapts to available space
		// Account for border (2 chars each side) when calculating content area
		contentWidth := width - 4
		contentHeight := height - 2
		if contentWidth < 10 {
			contentWidth = 10
		}
		if contentHeight < 5 {
			contentHeight = 5
		}

		// Group-scoped empty state
		if h.groupScope != "" {
			hints := []string{}
			if key := h.actionKey(hotkeyNewSession); key != "" {
				hints = append(hints, fmt.Sprintf("Press %s to create a session", key))
			}
			emptyContent := renderEmptyStateResponsive(EmptyStateConfig{
				Icon:     "⬡",
				Title:    "No sessions in " + h.groupScopeDisplayName(),
				Subtitle: "This group is empty",
				Hints:    hints,
			}, contentWidth, contentHeight)

			return lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(ColorBorder).
				Render(emptyContent)
		}

		hints := make([]string, 0, 3)
		if key := h.actionKey(hotkeyNewSession); key != "" {
			hints = append(hints, fmt.Sprintf("Press %s to create a new session", key))
		}
		if key := h.actionKey(hotkeyImport); key != "" {
			hints = append(hints, fmt.Sprintf("Press %s to import existing tmux sessions", key))
		}
		if key := h.actionKey(hotkeyCreateGroup); key != "" {
			hints = append(hints, fmt.Sprintf("Press %s to create a group", key))
		}
		if len(hints) == 0 {
			hints = append(hints, "Create or import sessions to get started")
		}

		emptyContent := renderEmptyStateResponsive(EmptyStateConfig{
			Icon:     "⬡",
			Title:    "No Sessions Yet",
			Subtitle: "Get started by creating your first session",
			Hints:    hints,
		}, contentWidth, contentHeight)

		return lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorBorder).
			Render(emptyContent)
	}

	// Render items starting from viewOffset
	visibleCount := 0
	maxVisible := height - 1 // Leave room for scrolling indicator
	if maxVisible < 1 {
		maxVisible = 1
	}

	// Show "more above" indicator if scrolled down
	if h.viewOffset > 0 {
		b.WriteString(DimStyle.Render(fmt.Sprintf("  ⋮ +%d above", h.viewOffset)))
		b.WriteString("\n")
		maxVisible-- // Account for the indicator line
	}

	snapshot := h.getSessionRenderSnapshot()
	groupStats := h.buildGroupRenderStats(snapshot)
	var jumpHints []string
	if h.jumpMode {
		jumpHints = generateJumpHints(len(h.flatItems))
	}

	for i := h.viewOffset; i < len(h.flatItems) && visibleCount < maxVisible; i++ {
		item := h.flatItems[i]
		if h.jumpMode && i < len(jumpHints) {
			// Render item to temp buffer, then overlay hint badge at name position
			var itemBuf strings.Builder
			h.renderItem(&itemBuf, item, i == h.cursor, i, groupStats, snapshot)
			raw := itemBuf.String()
			hint := jumpHints[i]
			isMatch := h.jumpBuffer == "" || strings.HasPrefix(hint, h.jumpBuffer)

			if isMatch {
				// Get the display name for this item type
				itemName := jumpItemName(item)
				// Overlay hint on the first line, preserve rest exactly
				if idx := strings.Index(raw, "\n"); idx >= 0 {
					b.WriteString(h.overlayJumpHint(raw[:idx], hint, h.jumpBuffer, itemName))
					b.WriteString(raw[idx:]) // includes \n and any subsequent lines
				} else {
					b.WriteString(h.overlayJumpHint(raw, hint, h.jumpBuffer, itemName))
				}
			} else {
				// Non-matching: render normally (no dimming to preserve layout)
				b.WriteString(raw)
			}
		} else {
			h.renderItem(&b, item, i == h.cursor, i, groupStats, snapshot)
		}
		visibleCount++
	}

	// Show "more below" indicator if there are more items
	remaining := len(h.flatItems) - (h.viewOffset + visibleCount)
	if remaining > 0 {
		b.WriteString(DimStyle.Render(fmt.Sprintf("  ⋮ +%d below", remaining)))
	}

	// Height padding is handled by ensureExactHeight() in View() for consistency
	return b.String()
}

type groupRenderStats struct {
	sessionCount int
	running      int
	waiting      int
}

func (h *Home) buildGroupRenderStats(snapshot map[string]sessionRenderState) map[string]groupRenderStats {
	stats := make(map[string]groupRenderStats)
	if h.groupTree == nil {
		return stats
	}

	for path, g := range h.groupTree.Groups {
		if g == nil {
			continue
		}

		directSessions := len(g.Sessions)
		directRunning := 0
		directWaiting := 0
		for _, sess := range g.Sessions {
			state, ok := snapshot[sess.ID]
			status := sess.Status
			if ok {
				status = state.status
			}
			switch status {
			case session.StatusRunning:
				directRunning++
			case session.StatusWaiting:
				directWaiting++
			}
		}

		// Add direct totals to this group and all ancestors once,
		// avoiding repeated recursive scans per rendered row.
		ancestor := path
		for ancestor != "" {
			entry := stats[ancestor]
			entry.sessionCount += directSessions
			entry.running += directRunning
			entry.waiting += directWaiting
			stats[ancestor] = entry

			idx := strings.LastIndex(ancestor, "/")
			if idx == -1 {
				break
			}
			ancestor = ancestor[:idx]
		}
	}

	return stats
}

// renderItem renders a single item (group or session) for the left panel
func (h *Home) renderItem(
	b *strings.Builder,
	item session.Item,
	selected bool,
	itemIndex int,
	groupStats map[string]groupRenderStats,
	snapshot map[string]sessionRenderState,
) {
	switch item.Type {
	case session.ItemTypeGroup:
		h.renderGroupItem(b, item, selected, itemIndex, groupStats)
	case session.ItemTypeSession:
		if item.CreatingID != "" {
			h.renderCreatingSessionItem(b, item, selected)
		} else {
			h.renderSessionItem(b, item, selected, snapshot)
		}
	case session.ItemTypeWindow:
		h.renderWindowItem(b, item, selected)
	case session.ItemTypeRemoteGroup:
		h.renderRemoteGroupItem(b, item, selected)
	case session.ItemTypeRemoteSession:
		h.renderRemoteSessionItem(b, item, selected)
	}
}

// renderGroupItem renders a group header
// PERFORMANCE: Uses cached styles from styles.go to avoid allocations
func (h *Home) renderGroupItem(
	b *strings.Builder,
	item session.Item,
	selected bool,
	itemIndex int,
	groupStats map[string]groupRenderStats,
) {
	group := item.Group

	// Calculate indentation based on nesting level (no tree lines, just spaces)
	// Uses spacingNormal (2 chars) per level for consistent hierarchy visualization
	indent := strings.Repeat(strings.Repeat(" ", spacingNormal), max(0, item.Level))

	// Expand/collapse indicator with filled triangles (using cached styles)
	var expandIcon string
	if selected {
		if group.Expanded {
			expandIcon = GroupExpandSelStyle.Render("▾")
		} else {
			expandIcon = GroupExpandSelStyle.Render("▸")
		}
	} else {
		if group.Expanded {
			expandIcon = GroupExpandStyle.Render("▾") // Filled triangle for expanded
		} else {
			expandIcon = GroupExpandStyle.Render("▸") // Filled triangle for collapsed
		}
	}

	// Hotkey indicator (subtle, only for root groups, hidden when selected)
	// Uses pre-computed RootGroupNum from rebuildFlatItems() - O(1) lookup instead of O(n) loop
	hotkeyStr := ""
	if item.Level == 0 && !selected {
		if item.RootGroupNum >= 1 && item.RootGroupNum <= 9 {
			hotkeyStr = GroupHotkeyStyle.Render(fmt.Sprintf("%d·", item.RootGroupNum))
		}
	}

	// Select appropriate cached styles based on selection state
	nameStyle := GroupNameStyle
	countStyle := GroupCountStyle
	if selected {
		nameStyle = GroupNameSelStyle
		countStyle = GroupCountSelStyle
	}

	// Use precomputed recursive stats (group + descendants) for this render pass.
	stats := groupStats[group.Path]
	countStr := countStyle.Render(fmt.Sprintf(" (%d)", stats.sessionCount))

	statusStr := ""
	if stats.running > 0 {
		statusStr += " " + GroupStatusRunning.Render(fmt.Sprintf("● %d", stats.running))
	}
	if stats.waiting > 0 {
		statusStr += " " + GroupStatusWaiting.Render(fmt.Sprintf("◐ %d", stats.waiting))
	}

	// Build the row: [indent][hotkey][expand] [name](count) [status]
	row := fmt.Sprintf(
		"%s%s%s %s%s%s",
		indent,
		hotkeyStr,
		expandIcon,
		nameStyle.Render(group.Name),
		countStr,
		statusStr,
	)
	b.WriteString(row)
	b.WriteString("\n")
}

// Tree drawing characters for visual hierarchy
const (
	treeBranch = "├─" // Mid-level item (has siblings below)
	treeLast   = "└─" // Last item in group (no siblings below)
	treeLine   = "│ " // Continuation line
	treeEmpty  = "  " // Empty space (for alignment)
	// Sub-session connectors (nested under parent)
	subBranch = "├─" // Sub-session with siblings below
	subLast   = "└─" // Last sub-session
)

// renderSessionItem renders a single session item for the left panel
// PERFORMANCE: Uses cached styles from styles.go to avoid allocations
func (h *Home) renderCreatingPreview(creating *CreatingSession, width, height int) string {
	var b strings.Builder
	spinnerFrames := []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"}
	spinner := spinnerFrames[h.animationFrame]

	centerStyle := lipgloss.NewStyle().
		Width(width - 4).
		Align(lipgloss.Center)

	// Spinner line
	spinnerStyle := lipgloss.NewStyle().
		Foreground(ColorPurple).
		Bold(true)
	spinnerLine := spinnerStyle.Render(spinner + "  " + spinner + "  " + spinner)
	b.WriteString("\n\n")
	b.WriteString(centerStyle.Render(spinnerLine))
	b.WriteString("\n\n")

	// Title
	titleStyle := lipgloss.NewStyle().
		Foreground(ColorPurple).
		Bold(true)
	b.WriteString(centerStyle.Render(titleStyle.Render("🔨 Creating Worktree")))
	b.WriteString("\n\n")

	// Description
	descStyle := lipgloss.NewStyle().
		Foreground(ColorText)
	b.WriteString(centerStyle.Render(descStyle.Render("Setting up " + creating.Title + "...")))
	b.WriteString("\n\n")

	// Elapsed time
	elapsed := time.Since(creating.StartTime).Truncate(time.Second)
	timeStyle := lipgloss.NewStyle().
		Foreground(ColorTextDim).
		Italic(true)
	b.WriteString(centerStyle.Render(timeStyle.Render(fmt.Sprintf("Elapsed: %s", elapsed))))
	b.WriteString("\n\n")

	// Progress dots animation
	dots := strings.Repeat("·", (h.animationFrame%4)+1) + strings.Repeat(" ", 3-h.animationFrame%4)
	dotStyle := lipgloss.NewStyle().Foreground(ColorPurple)
	b.WriteString(centerStyle.Render(dotStyle.Render(dots)))

	return b.String()
}

func (h *Home) renderCreatingSessionItem(
	b *strings.Builder,
	item session.Item,
	selected bool,
) {
	spinnerFrames := []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"}
	spinner := spinnerFrames[h.animationFrame]

	// Selection styling
	if selected {
		b.WriteString(lipgloss.NewStyle().
			Foreground(ColorAccent).
			Bold(true).
			Render("▸ "))
	} else {
		b.WriteString("  ")
	}

	// Tree connector
	if item.Level > 0 {
		b.WriteString(TreeConnectorStyle.Render("├── "))
	}

	// Spinner + title
	spinnerStyle := lipgloss.NewStyle().Foreground(ColorPurple)
	titleStyle := lipgloss.NewStyle().Foreground(ColorText).Italic(true)
	b.WriteString(spinnerStyle.Render(spinner))
	b.WriteString(" ")
	b.WriteString(titleStyle.Render(item.CreatingTitle))
	b.WriteString(lipgloss.NewStyle().Foreground(ColorTextDim).Italic(true).Render(" (creating worktree...)"))
	b.WriteString("\n")
}

func (h *Home) renderSessionItem(
	b *strings.Builder,
	item session.Item,
	selected bool,
	snapshot map[string]sessionRenderState,
) {
	inst := item.Session

	// Read status/tool from snapshot so render path stays lock-light during key-repeat.
	instState, ok := snapshot[inst.ID]
	if !ok {
		instState = h.getSessionRenderState(inst)
	}
	instStatus := instState.status
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

	// Status indicator with consistent sizing
	var statusIcon string
	var statusStyle lipgloss.Style
	switch instStatus {
	case session.StatusRunning:
		statusIcon = "●"
		statusStyle = SessionStatusRunning
	case session.StatusWaiting:
		statusIcon = "◐"
		statusStyle = SessionStatusWaiting
	case session.StatusIdle:
		statusIcon = "○"
		statusStyle = SessionStatusIdle
	case session.StatusError:
		statusIcon = "✕"
		statusStyle = SessionStatusError
	case session.StatusStopped:
		statusIcon = "■"
		statusStyle = SessionStatusStopped
	default:
		statusIcon = "○"
		statusStyle = SessionStatusIdle
	}

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

	title := titleStyle.Render(inst.Title)
	tool := toolStyle.Render(" " + instTool)

	// YOLO badge for Gemini/Codex sessions with YOLO mode enabled
	yoloBadge := ""
	showYolo := false
	if instTool == "gemini" && inst.GeminiYoloMode != nil && *inst.GeminiYoloMode {
		showYolo = true
	} else if instTool == "codex" {
		if opts := inst.GetCodexOptions(); opts != nil && opts.YoloMode != nil && *opts.YoloMode {
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
		if len(branch) > 15 {
			branch = branch[:12] + "..."
		}
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
		if len(host) > 20 {
			host = host[:17] + "..."
		}
		sshStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("3")) // yellow
		if selected {
			sshStyle = SessionStatusSelStyle
		}
		sshBadge = sshStyle.Render(" [ssh:" + host + "]")
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

	// Build row: [baseIndent][selection][tree][chevron][status] [title] [tool] [badges]
	row := fmt.Sprintf(
		"%s%s%s%s%s %s%s%s%s%s%s%s",
		baseIndent,
		selectionPrefix,
		treeStyle.Render(treeConnector),
		windowChevron,
		status,
		title,
		tool,
		yoloBadge,
		worktreeBadge,
		sandboxBadge,
		multiRepoBadge,
		sshBadge,
	)

	// Append pane title filling remaining row space (only for the selected item).
	// lipgloss.Width(row) accounts for indentation, tree connectors, and all badges,
	// so deeply-nested sessions with many badges naturally get less pane title space.
	if selected && instState.paneTitle != "" {
		remaining := h.width - lipgloss.Width(row) - 2 // -2 for trailing margin
		if remaining > 10 {
			pt := instState.paneTitle
			if lipgloss.Width(pt) > remaining {
				pt = ansi.Truncate(pt, remaining, "…")
			}
			row += DimStyle.Render(" " + pt)
		}
	}

	b.WriteString(row)
	b.WriteString("\n")
}

// renderWindowItem renders a single window item (child of a session) for the left panel
func (h *Home) renderWindowItem(b *strings.Builder, item session.Item, selected bool) {
	treeStyle := TreeConnectorStyle

	// Base indent — windows are children of sessions.
	// Show │ continuation line at the parent session's level when it's not the
	// last item in the group. Extra space after │ so window ├─ aligns under
	// the parent session's ○ status bullet (position 4).
	baseIndent := ""
	if item.Level > 1 {
		groupIndent := strings.Repeat(treeEmpty, item.Level-2)
		if item.ParentIsLastInGroup {
			baseIndent = groupIndent + "   " // No continuation needed
		} else {
			baseIndent = groupIndent + " " + treeStyle.Render("│") + " "
		}
	}

	// Tree connector
	treeConnector := subBranch
	if item.IsLastWindow {
		treeConnector = subLast
	}

	// Selection
	selectionPrefix := " "
	nameStyle := lipgloss.NewStyle().Foreground(ColorTextDim)
	indexStyle := lipgloss.NewStyle().Foreground(ColorTextDim)
	if selected {
		selectionPrefix = SessionSelectionPrefix.Render("▶")
		nameStyle = SessionTitleSelStyle
		indexStyle = SessionStatusSelStyle
		treeStyle = TreeConnectorSelStyle
		// Rebuild baseIndent with selection styling
		if item.Level > 1 && !item.ParentIsLastInGroup {
			groupIndent := strings.Repeat(treeEmpty, max(0, item.Level-2))
			baseIndent = groupIndent + " " + treeStyle.Render("│") + " "
		}
	}

	winLabel := indexStyle.Render(fmt.Sprintf("[%d]", item.WindowIndex))
	winName := nameStyle.Render(" " + item.WindowName)

	// Tool badge (if detected)
	toolBadge := ""
	if item.WindowTool != "" {
		toolStyle := GetToolStyle(item.WindowTool)
		if selected {
			toolStyle = SessionStatusSelStyle
		}
		toolBadge = toolStyle.Render(" " + item.WindowTool)
	}

	row := fmt.Sprintf(
		"%s%s%s %s%s%s",
		baseIndent,
		selectionPrefix,
		treeStyle.Render(treeConnector),
		winLabel,
		winName,
		toolBadge,
	)
	b.WriteString(row)
	b.WriteString("\n")
}

// renderLaunchingState renders the animated launching/resuming indicator for sessions
// renderRemotePreview renders the preview pane for a remote group or session
func (h *Home) renderRemotePreview(item session.Item, width, height int) string {
	if item.Type == session.ItemTypeRemoteGroup {
		h.remoteSessionsMu.RLock()
		count := 0
		if sessions, ok := h.remoteSessions[item.RemoteName]; ok {
			count = len(sessions)
		}
		h.remoteSessionsMu.RUnlock()

		config, _ := session.LoadUserConfig()
		host := item.RemoteName
		if config != nil && config.Remotes != nil {
			if rc, ok := config.Remotes[item.RemoteName]; ok {
				host = rc.Host
			}
		}

		return renderEmptyStateResponsive(EmptyStateConfig{
			Icon:     "⬡",
			Title:    "Remote: " + item.RemoteName,
			Subtitle: fmt.Sprintf("Host: %s — %d sessions", host, count),
			Hints:    []string{"Press Enter on a session to attach via SSH"},
		}, width, height)
	}

	// Remote session preview
	rs := item.RemoteSession
	if rs == nil {
		return ""
	}

	var b strings.Builder
	nameStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)
	b.WriteString(nameStyle.Render(rs.Title))
	b.WriteString("  ")

	statusColor := ColorTextDim
	statusIcon := "○"
	switch rs.Status {
	case "running":
		statusIcon = "●"
		statusColor = ColorGreen
	case "waiting":
		statusIcon = "◐"
		statusColor = ColorYellow
	case "error":
		statusIcon = "✗"
		statusColor = ColorRed
	}
	b.WriteString(lipgloss.NewStyle().Foreground(statusColor).Render(statusIcon + " " + rs.Status))
	b.WriteString("\n\n")

	dimStyle := lipgloss.NewStyle().Foreground(ColorTextDim)
	b.WriteString(dimStyle.Render("Remote:  ") + item.RemoteName + "\n")
	b.WriteString(dimStyle.Render("Path:    ") + rs.Path + "\n")
	if rs.Tool != "" {
		b.WriteString(dimStyle.Render("Tool:    ") + rs.Tool + "\n")
	}
	if rs.Group != "" {
		b.WriteString(dimStyle.Render("Group:   ") + rs.Group + "\n")
	}
	b.WriteString("\n")

	pvKey := remotePreviewCacheKey(item.RemoteName, rs.ID)
	h.previewCacheMu.RLock()
	previewContent, hasPreview := h.previewCache[pvKey]
	_, hasFetched := h.previewCacheTime[pvKey]
	h.previewCacheMu.RUnlock()

	b.WriteString(dimStyle.Render("Last response") + "\n")
	b.WriteString(strings.Repeat("-", max(1, min(width-4, 40))))
	b.WriteString("\n")
	previewContent = truncateRemotePreviewContent(previewContent)
	if hasPreview && strings.TrimSpace(previewContent) != "" {
		b.WriteString(previewContent)
		b.WriteString("\n\n")
	} else if hasFetched {
		b.WriteString(dimStyle.Render("No response available yet."))
		b.WriteString("\n\n")
	} else if rs.Status == "running" || rs.Status == "waiting" {
		b.WriteString(dimStyle.Render("Fetching remote preview..."))
		b.WriteString("\n\n")
	} else {
		b.WriteString(dimStyle.Render("No response available yet."))
		b.WriteString("\n\n")
	}

	b.WriteString(dimStyle.Render("Press Enter to attach via SSH"))

	return b.String()
}

// renderRemoteGroupItem renders a remote group header (e.g., "remotes/dev")
func (h *Home) renderRemoteGroupItem(b *strings.Builder, item session.Item, selected bool) {
	// Count sessions for this remote
	h.remoteSessionsMu.RLock()
	count := 0
	if sessions, ok := h.remoteSessions[item.RemoteName]; ok {
		count = len(sessions)
	}
	h.remoteSessionsMu.RUnlock()

	nameStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Bold(true) // yellow
	countStyle := DimStyle
	expandIcon := "▾"
	selPrefix := "  "
	if selected {
		nameStyle = GroupNameSelStyle
		countStyle = GroupCountSelStyle
		selPrefix = "▶ "
	}

	b.WriteString(fmt.Sprintf("%s%s %s%s\n",
		selPrefix,
		expandIcon,
		nameStyle.Render("remotes/"+item.RemoteName),
		countStyle.Render(fmt.Sprintf(" (%d)", count)),
	))
}

// renderRemoteSessionItem renders a single remote session row
func (h *Home) renderRemoteSessionItem(b *strings.Builder, item session.Item, selected bool) {
	rs := item.RemoteSession
	if rs == nil {
		return
	}

	statusIcon := "○"
	statusColor := lipgloss.Color("8") // gray
	switch rs.Status {
	case "running":
		statusIcon = "●"
		statusColor = lipgloss.Color("2") // green
	case "waiting":
		statusIcon = "◉"
		statusColor = lipgloss.Color("3") // yellow
	case "idle":
		statusIcon = "○"
		statusColor = lipgloss.Color("8")
	case "error":
		statusIcon = "✗"
		statusColor = lipgloss.Color("1") // red
	}

	sStyle := lipgloss.NewStyle().Foreground(statusColor)
	titleStyle := lipgloss.NewStyle().Foreground(ColorText)
	if selected {
		sStyle = SessionStatusSelStyle
		titleStyle = SessionStatusSelStyle
	}

	titleStr := rs.Title
	if len(titleStr) > 25 {
		titleStr = titleStr[:22] + "..."
	}

	toolStr := ""
	if rs.Tool != "" {
		tStyle := DimStyle
		if selected {
			tStyle = SessionStatusSelStyle
		}
		toolStr = tStyle.Render(" " + rs.Tool)
	}

	treeConnector := "├─"
	if item.IsLastInGroup {
		treeConnector = "└─"
	}

	selPrefix := "  "
	if selected {
		selPrefix = "▶ "
	}

	b.WriteString(fmt.Sprintf("%s  %s %s %s%s\n",
		selPrefix,
		DimStyle.Render(treeConnector),
		sStyle.Render(statusIcon),
		titleStyle.Render(titleStr),
		toolStr,
	))
}

func (h *Home) renderLaunchingState(inst *session.Instance, width int, startTime time.Time) string {
	var b strings.Builder

	// Check if this is a resume operation (vs new launch)
	_, isResuming := h.resumingSessions[inst.ID]

	// Braille spinner frames - creates smooth rotation effect
	spinnerFrames := []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"}
	spinner := spinnerFrames[h.animationFrame]

	// Tool-specific messaging with emoji
	var toolName, toolDesc, emoji string
	if isResuming {
		emoji = "🔄"
	} else {
		emoji = "🚀"
	}

	switch inst.Tool {
	case "claude":
		toolName = "Claude Code"
		if isResuming {
			toolDesc = "Resuming Claude session..."
		} else {
			toolDesc = "Starting Claude session..."
		}
	case "gemini":
		toolName = "Gemini"
		if isResuming {
			toolDesc = "Resuming Gemini session..."
		} else {
			toolDesc = "Connecting to Gemini..."
		}
	case "aider":
		toolName = "Aider"
		if isResuming {
			toolDesc = "Resuming Aider session..."
		} else {
			toolDesc = "Starting Aider..."
		}
	case "codex":
		toolName = "Codex"
		if isResuming {
			toolDesc = "Resuming Codex session..."
		} else {
			toolDesc = "Starting Codex..."
		}
	case "opencode":
		toolName = "OpenCode"
		if isResuming {
			toolDesc = "Resuming OpenCode session..."
		} else {
			toolDesc = "Starting OpenCode..."
		}
	default:
		toolName = "Shell"
		if isResuming {
			toolDesc = "Resuming shell session..."
		} else {
			toolDesc = "Launching shell session..."
		}
	}

	// Centered layout
	centerStyle := lipgloss.NewStyle().
		Width(width - 4).
		Align(lipgloss.Center)

	// Spinner with tool color
	spinnerStyle := lipgloss.NewStyle().
		Foreground(ColorAccent).
		Bold(true)
	spinnerLine := spinnerStyle.Render(spinner + "  " + spinner + "  " + spinner)
	b.WriteString(centerStyle.Render(spinnerLine))
	b.WriteString("\n\n")

	// Title with emoji
	titleStyle := lipgloss.NewStyle().
		Foreground(ColorPurple).
		Bold(true)
	var actionVerb string
	if isResuming {
		actionVerb = "Resuming"
	} else {
		actionVerb = "Launching"
	}
	b.WriteString(centerStyle.Render(titleStyle.Render(emoji + " " + actionVerb + " " + toolName)))
	b.WriteString("\n\n")

	// Description
	descStyle := lipgloss.NewStyle().
		Foreground(ColorText).
		Italic(true)
	b.WriteString(centerStyle.Render(descStyle.Render(toolDesc)))
	b.WriteString("\n\n")

	// Progress dots animation
	dotsCount := (h.animationFrame % 4) + 1
	dots := strings.Repeat("●", dotsCount) + strings.Repeat("○", 4-dotsCount)
	dotsStyle := lipgloss.NewStyle().
		Foreground(ColorAccent)
	b.WriteString(centerStyle.Render(dotsStyle.Render(dots)))
	b.WriteString("\n\n")

	// Elapsed time (consistent with MCP and Fork animations)
	elapsed := time.Since(startTime).Round(time.Second)
	timeStyle := lipgloss.NewStyle().
		Foreground(ColorYellow).
		Italic(true)
	b.WriteString(centerStyle.Render(timeStyle.Render(fmt.Sprintf("Loading... %s", elapsed))))

	return b.String()
}

// renderMcpLoadingState renders the MCP loading animation in the preview pane
func (h *Home) renderMcpLoadingState(inst *session.Instance, width int, startTime time.Time) string {
	var b strings.Builder

	// Braille spinner frames - creates smooth rotation effect
	spinnerFrames := []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"}
	spinner := spinnerFrames[h.animationFrame]

	// Centered layout
	centerStyle := lipgloss.NewStyle().
		Width(width - 4).
		Align(lipgloss.Center)

	// Spinner with cyan color (MCP-themed)
	spinnerStyle := lipgloss.NewStyle().
		Foreground(ColorCyan).
		Bold(true)
	spinnerLine := spinnerStyle.Render(spinner + "  " + spinner + "  " + spinner)
	b.WriteString(centerStyle.Render(spinnerLine))
	b.WriteString("\n\n")

	// MCP loading title
	titleStyle := lipgloss.NewStyle().
		Foreground(ColorCyan).
		Bold(true)
	b.WriteString(centerStyle.Render(titleStyle.Render("🔌 Reloading MCPs")))
	b.WriteString("\n\n")

	// Description
	descStyle := lipgloss.NewStyle().
		Foreground(ColorText).
		Italic(true)
	b.WriteString(centerStyle.Render(descStyle.Render("Restarting session with updated MCP configuration...")))
	b.WriteString("\n\n")

	// Progress dots animation
	dotsCount := (h.animationFrame % 4) + 1
	dots := strings.Repeat("●", dotsCount) + strings.Repeat("○", 4-dotsCount)
	dotsStyle := lipgloss.NewStyle().
		Foreground(ColorCyan)
	b.WriteString(centerStyle.Render(dotsStyle.Render(dots)))
	b.WriteString("\n\n")

	// Elapsed time
	elapsed := time.Since(startTime).Round(time.Second)
	timeStyle := lipgloss.NewStyle().
		Foreground(ColorYellow).
		Italic(true)
	b.WriteString(centerStyle.Render(timeStyle.Render(fmt.Sprintf("Loading... %s", elapsed))))

	return b.String()
}

// renderForkingState renders the forking animation when session is being forked
func (h *Home) renderForkingState(inst *session.Instance, width int, startTime time.Time) string {
	var b strings.Builder

	// Centered layout
	centerStyle := lipgloss.NewStyle().
		Width(width - 4).
		Align(lipgloss.Center)

	// Braille spinner frames
	spinnerFrames := []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"}
	spinner := spinnerFrames[h.animationFrame]

	// Spinner with purple color (fork-themed)
	spinnerStyle := lipgloss.NewStyle().
		Foreground(ColorPurple).
		Bold(true)
	spinnerLine := spinnerStyle.Render(spinner + "  " + spinner + "  " + spinner)
	b.WriteString(centerStyle.Render(spinnerLine))
	b.WriteString("\n\n")

	// Forking title
	titleStyle := lipgloss.NewStyle().
		Foreground(ColorPurple).
		Bold(true)
	b.WriteString(centerStyle.Render(titleStyle.Render("🔀 Forking Session")))
	b.WriteString("\n\n")

	// Description
	descStyle := lipgloss.NewStyle().
		Foreground(ColorText).
		Italic(true)
	b.WriteString(centerStyle.Render(descStyle.Render("Creating a new Claude session from this conversation...")))
	b.WriteString("\n\n")

	// Progress dots animation
	dotsCount := (h.animationFrame % 4) + 1
	dots := strings.Repeat("●", dotsCount) + strings.Repeat("○", 4-dotsCount)
	dotsStyle := lipgloss.NewStyle().
		Foreground(ColorPurple)
	b.WriteString(centerStyle.Render(dotsStyle.Render(dots)))
	b.WriteString("\n\n")

	// Elapsed time (consistent with other animations)
	elapsed := time.Since(startTime).Round(time.Second)
	timeStyle := lipgloss.NewStyle().
		Foreground(ColorYellow).
		Italic(true)
	b.WriteString(centerStyle.Render(timeStyle.Render(fmt.Sprintf("Loading... %s", elapsed))))

	return b.String()
}

// renderSessionInfoCard renders a simple session info card as fallback view
// Used when both show_output and show_analytics are disabled
func (h *Home) renderSessionInfoCard(inst *session.Instance, width, height int) string {
	if inst == nil {
		dimStyle := lipgloss.NewStyle().Foreground(ColorTextDim).Italic(true)
		return dimStyle.Render("No session selected")
	}

	var b strings.Builder

	// Snapshot status/tool under read lock for thread safety
	cardStatus := inst.GetStatusThreadSafe()
	cardTool := inst.GetToolThreadSafe()

	// Header with tool icon
	icon := ToolIcon(cardTool)
	header := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorAccent).
		Render(fmt.Sprintf("%s %s", icon, inst.Title))
	b.WriteString(header)
	b.WriteString("\n")
	b.WriteString(strings.Repeat("─", max(0, min(width-4, 40))))
	b.WriteString("\n\n")

	labelStyle := lipgloss.NewStyle().Foreground(ColorTextDim)
	valueStyle := lipgloss.NewStyle().Foreground(ColorText)

	// Path
	b.WriteString(fmt.Sprintf("%s %s\n", labelStyle.Render("Path:"), valueStyle.Render(inst.ProjectPath)))

	// Status with color
	var statusColor lipgloss.Color
	switch cardStatus {
	case session.StatusRunning:
		statusColor = ColorGreen
	case session.StatusWaiting:
		statusColor = ColorYellow
	case session.StatusError:
		statusColor = ColorRed
	default:
		statusColor = ColorTextDim
	}
	statusStyle := lipgloss.NewStyle().Foreground(statusColor)
	b.WriteString(fmt.Sprintf("%s %s\n", labelStyle.Render("Status:"), statusStyle.Render(string(cardStatus))))

	// Tool
	b.WriteString(fmt.Sprintf("%s %s\n", labelStyle.Render("Tool:"), valueStyle.Render(cardTool)))

	// Session ID (if available) - Claude, Gemini, or OpenCode
	sessionID := inst.ClaudeSessionID
	if sessionID == "" {
		sessionID = inst.GeminiSessionID
	}
	if sessionID == "" {
		sessionID = inst.OpenCodeSessionID
	}
	if sessionID != "" {
		shortID := sessionID
		if len(shortID) > 12 {
			shortID = shortID[:12] + "..."
		}
		b.WriteString(fmt.Sprintf("%s %s\n", labelStyle.Render("Session:"), valueStyle.Render(shortID)))
	}

	// Created date
	b.WriteString(
		fmt.Sprintf("%s %s\n", labelStyle.Render("Created:"), valueStyle.Render(inst.CreatedAt.Format("Jan 2 15:04"))),
	)

	return b.String()
}

// renderPreviewPane renders the right panel with live preview
func (h *Home) renderPreviewPane(width, height int) string {
	var b strings.Builder

	if len(h.flatItems) == 0 || h.cursor >= len(h.flatItems) {
		// Show different message when there are no sessions vs just no selection
		if len(h.flatItems) == 0 {
			// Group-scoped empty preview
			if h.groupScope != "" {
				return renderEmptyStateResponsive(EmptyStateConfig{
					Icon:     "✦",
					Title:    h.groupScopeDisplayName(),
					Subtitle: "Group scope active",
					Hints:    []string{"Only sessions in this group are shown"},
				}, width, height)
			}
			hints := make([]string, 0, 2)
			if key := h.actionKey(hotkeyNewSession); key != "" {
				hints = append(hints, fmt.Sprintf("Press %s to create your first session", key))
			}
			if key := h.actionKey(hotkeyImport); key != "" {
				hints = append(hints, fmt.Sprintf("Press %s to import tmux sessions", key))
			}
			if len(hints) == 0 {
				hints = append(hints, "Create or import sessions to get started")
			}
			content := renderEmptyStateResponsive(EmptyStateConfig{
				Icon:     "✦",
				Title:    "Ready to Go",
				Subtitle: "Your workspace is set up",
				Hints:    hints,
			}, width, height)
			if statsBlock := h.renderSystemStatsBlock(width); statsBlock != "" {
				content += "\n" + statsBlock
			}
			return content
		}
		content := renderEmptyStateResponsive(EmptyStateConfig{
			Icon:     "◇",
			Title:    "No Selection",
			Subtitle: "Select a session to preview",
			Hints:    nil,
		}, width, height)
		if statsBlock := h.renderSystemStatsBlock(width); statsBlock != "" {
			content += "\n" + statsBlock
		}
		return content
	}

	item := h.flatItems[h.cursor]

	// If group is selected, show group info
	if item.Type == session.ItemTypeGroup {
		return h.renderGroupPreview(item.Group, width, height)
	}

	// Remote items: show simple preview
	if item.Type == session.ItemTypeRemoteGroup || item.Type == session.ItemTypeRemoteSession {
		return h.renderRemotePreview(item, width, height)
	}

	// Window items: resolve parent session for preview
	if item.Type == session.ItemTypeWindow {
		parentInst := h.getInstanceByID(item.WindowSessionID)
		if parentInst == nil {
			return renderEmptyStateResponsive(EmptyStateConfig{
				Icon:     "◇",
				Title:    fmt.Sprintf("Window %d: %s", item.WindowIndex, item.WindowName),
				Subtitle: "Parent session not found",
			}, width, height)
		}
		item.Session = parentInst
	}

	// Creating session placeholder: show dedicated animation
	if item.CreatingID != "" {
		if creating, ok := h.creatingSessions[item.CreatingID]; ok {
			return h.renderCreatingPreview(creating, width, height)
		}
		return ""
	}

	// Session preview
	selected := item.Session

	// Compute preview cache key (window-aware)
	pvKey := selected.ID
	if item.Type == session.ItemTypeWindow {
		pvKey = previewCacheKey(selected.ID, item.WindowIndex)
	}

	// Session info header box
	// Cache status once to avoid races with background status updates
	selectedStatus := selected.GetStatusThreadSafe()
	statusIcon := "○"
	statusColor := ColorTextDim
	switch selectedStatus {
	case session.StatusRunning:
		statusIcon = "●"
		statusColor = ColorGreen
	case session.StatusWaiting:
		statusIcon = "◐"
		statusColor = ColorYellow
	case session.StatusError:
		statusIcon = "✕"
		statusColor = ColorRed
	case session.StatusStopped:
		statusIcon = "■"
		statusColor = ColorTextDim
	}

	// Header with session name and status
	statusBadge := lipgloss.NewStyle().Foreground(statusColor).Render(statusIcon + " " + string(selectedStatus))
	nameStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)
	b.WriteString(nameStyle.Render(selected.Title))
	b.WriteString("  ")
	b.WriteString(statusBadge)
	b.WriteString("\n")

	// Info lines: path and activity time
	infoStyle := lipgloss.NewStyle().Foreground(ColorText)
	pathStr := truncatePath(selected.ProjectPath, width-4)
	b.WriteString(infoStyle.Render("📁 " + pathStr))
	b.WriteString("\n")

	// Activity time - shows when session was last active
	activityTime := selected.GetLastActivityTime()
	activityStr := formatRelativeTime(activityTime)
	if selectedStatus == session.StatusRunning {
		activityStr = "active now"
	}
	b.WriteString(infoStyle.Render("⏱ " + activityStr))
	b.WriteString("\n")

	toolBadge := lipgloss.NewStyle().
		Foreground(ColorBg).
		Background(ColorPurple).
		Padding(0, 1).
		Render(selected.Tool)
	groupBadge := lipgloss.NewStyle().
		Foreground(ColorBg).
		Background(ColorCyan).
		Padding(0, 1).
		Render(selected.GroupPath)
	b.WriteString(toolBadge)
	b.WriteString(" ")
	b.WriteString(groupBadge)
	b.WriteString("\n")

	// Worktree info section (for sessions running in git worktrees)
	if selected.IsWorktree() {
		wtHeader := renderSectionDivider("Worktree", width-4)
		b.WriteString(wtHeader)
		b.WriteString("\n")

		wtLabelStyle := lipgloss.NewStyle().Foreground(ColorText)
		wtBranchStyle := lipgloss.NewStyle().Foreground(ColorCyan).Bold(true)
		wtValueStyle := lipgloss.NewStyle().Foreground(ColorText)
		wtHintStyle := lipgloss.NewStyle().Foreground(ColorText).Italic(true)
		wtKeyStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)

		// Branch
		if selected.WorktreeBranch != "" {
			b.WriteString(wtLabelStyle.Render("Branch:  "))
			b.WriteString(wtBranchStyle.Render(selected.WorktreeBranch))
			b.WriteString("\n")
		}

		// Repo root (truncated)
		if selected.WorktreeRepoRoot != "" {
			repoPath := truncatePath(selected.WorktreeRepoRoot, width-4-9)
			b.WriteString(wtLabelStyle.Render("Repo:    "))
			b.WriteString(wtValueStyle.Render(repoPath))
			b.WriteString("\n")
		}

		// Worktree path (truncated)
		if selected.WorktreePath != "" {
			wtPath := truncatePath(selected.WorktreePath, width-4-9)
			b.WriteString(wtLabelStyle.Render("Path:    "))
			b.WriteString(wtValueStyle.Render(wtPath))
			b.WriteString("\n")
		}

		// Dirty status (lazy-cached, fetched via previewDebounce handler with 10s TTL)
		h.worktreeDirtyMu.Lock()
		isDirty, hasCached := h.worktreeDirtyCache[selected.ID]
		h.worktreeDirtyMu.Unlock()

		dirtyLabel := "checking..."
		dirtyStyle := wtValueStyle
		if hasCached {
			if isDirty {
				dirtyLabel = "dirty (uncommitted changes)"
				dirtyStyle = lipgloss.NewStyle().Foreground(ColorYellow)
			} else {
				dirtyLabel = "clean"
				dirtyStyle = lipgloss.NewStyle().Foreground(ColorGreen)
			}
		}
		b.WriteString(wtLabelStyle.Render("Status:  "))
		b.WriteString(dirtyStyle.Render(dirtyLabel))
		b.WriteString("\n")

		// Finish hint
		if finishKey := h.actionKey(hotkeyWorktreeFinish); finishKey != "" {
			b.WriteString(wtHintStyle.Render("Finish:  "))
			b.WriteString(wtKeyStyle.Render(finishKey))
			b.WriteString(wtHintStyle.Render(" merge + cleanup"))
			b.WriteString("\n")
		}
	}

	// Multi-repo info section
	if selected.IsMultiRepo() {
		mrHeader := renderSectionDivider("Multi-Repo", width-4)
		b.WriteString(mrHeader)
		b.WriteString("\n")

		mrLabelStyle := lipgloss.NewStyle().Foreground(ColorText)
		mrValueStyle := lipgloss.NewStyle().Foreground(ColorText)

		for i, p := range selected.AllProjectPaths() {
			label := fmt.Sprintf("  %d. ", i+1)
			b.WriteString(mrLabelStyle.Render(label))
			b.WriteString(mrValueStyle.Render(truncatePath(p, width-4-len(label))))
			b.WriteString("\n")
		}

		editPathsKey := h.actionKey(hotkeyEditPaths)
		if editPathsKey != "" {
			hintStyle := lipgloss.NewStyle().Foreground(ColorComment)
			b.WriteString(hintStyle.Render(fmt.Sprintf("  %s: edit paths", editPathsKey)))
			b.WriteString("\n")
		}
	}

	// Claude-specific info (session ID and MCPs)
	if session.IsClaudeCompatible(selected.Tool) {
		// Section divider for Claude info
		claudeHeader := renderSectionDivider("Claude", width-4)
		b.WriteString(claudeHeader)
		b.WriteString("\n")

		labelStyle := lipgloss.NewStyle().Foreground(ColorText)
		valueStyle := lipgloss.NewStyle().Foreground(ColorText)

		// Status line
		if selected.ClaudeSessionID != "" {
			statusStyle := lipgloss.NewStyle().Foreground(ColorGreen).Bold(true)
			b.WriteString(labelStyle.Render("Status:  "))
			b.WriteString(statusStyle.Render("● Connected"))
			b.WriteString("\n")

			// Full session ID on its own line
			b.WriteString(labelStyle.Render("Session: "))
			b.WriteString(valueStyle.Render(selected.ClaudeSessionID))
			b.WriteString("\n")
		} else {
			statusStyle := lipgloss.NewStyle().Foreground(ColorText)
			b.WriteString(labelStyle.Render("Status:  "))
			b.WriteString(statusStyle.Render("○ Not connected"))
			b.WriteString("\n")
		}

		// MCP servers - compact format with source indicators and sync status
		mcpInfo := selected.GetMCPInfo()
		hasLoadedMCPs := len(selected.LoadedMCPNames) > 0
		hasMCPs := mcpInfo != nil && mcpInfo.HasAny()

		if hasMCPs || hasLoadedMCPs {
			b.WriteString(labelStyle.Render("MCPs:    "))

			// Build set of loaded MCPs for comparison
			loadedSet := make(map[string]bool)
			for _, name := range selected.LoadedMCPNames {
				loadedSet[name] = true
			}

			// Build set of current MCPs (from config)
			currentSet := make(map[string]bool)
			if mcpInfo != nil {
				for _, name := range mcpInfo.Global {
					currentSet[name] = true
				}
				for _, name := range mcpInfo.Project {
					currentSet[name] = true
				}
				for _, mcp := range mcpInfo.LocalMCPs {
					currentSet[mcp.Name] = true
				}
			}

			// Styles for different MCP states
			pendingStyle := lipgloss.NewStyle().Foreground(ColorYellow)
			staleStyle := lipgloss.NewStyle().Foreground(ColorText)

			var mcpParts []string

			// Helper to add MCP with appropriate styling
			addMCP := func(name, source string) {
				label := name + " (" + source + ")"
				if !hasLoadedMCPs {
					// Old session without LoadedMCPNames - show all as normal (no sync info)
					mcpParts = append(mcpParts, valueStyle.Render(label))
				} else if loadedSet[name] {
					// In both loaded and current - active (normal style)
					mcpParts = append(mcpParts, valueStyle.Render(label))
				} else {
					// In current but not loaded - pending (needs restart)
					mcpParts = append(mcpParts, pendingStyle.Render(label+" ⟳"))
				}
			}

			// Add MCPs from current config with source indicators
			if mcpInfo != nil {
				for _, name := range mcpInfo.Global {
					addMCP(name, "g")
				}
				for _, name := range mcpInfo.Project {
					addMCP(name, "p")
				}
				for _, mcp := range mcpInfo.LocalMCPs {
					// Show source path if different from project path
					sourceIndicator := "l"
					if mcp.SourcePath != selected.ProjectPath {
						// Show abbreviated path (just directory name)
						sourceIndicator = "l:" + filepath.Base(mcp.SourcePath)
					}
					addMCP(mcp.Name, sourceIndicator)
				}
			}

			// Add stale MCPs (loaded but no longer in config)
			if hasLoadedMCPs {
				for _, name := range selected.LoadedMCPNames {
					if !currentSet[name] {
						// Still running but removed from config
						mcpParts = append(mcpParts, staleStyle.Render(name+" ✕"))
					}
				}
			}

			// Calculate available width for MCPs (width - 4 for panel padding - 9 for "MCPs:    " label)
			mcpMaxWidth := width - 4 - 9
			if mcpMaxWidth < 20 {
				mcpMaxWidth = 20 // Minimum sensible width
			}

			// Build MCPs progressively to fit within available width
			var mcpResult strings.Builder
			mcpCount := 0
			currentWidth := 0

			for i, part := range mcpParts {
				// Strip ANSI codes to measure actual display width
				plainPart := tmux.StripANSI(part)
				partWidth := runewidth.StringWidth(plainPart)

				// Calculate width including separator if not first
				addedWidth := partWidth
				if mcpCount > 0 {
					addedWidth += 2 // ", " separator
				}

				remaining := len(mcpParts) - i
				isLast := remaining == 1

				// For non-last MCPs: reserve space for "+N more" indicator
				// For last MCP: just check if it fits without indicator
				var wouldExceed bool
				if isLast {
					// Last MCP - just check if it fits
					wouldExceed = currentWidth+addedWidth > mcpMaxWidth
				} else {
					// Not last - check with indicator space reserved
					moreIndicator := fmt.Sprintf(" (+%d more)", remaining)
					moreWidth := runewidth.StringWidth(moreIndicator)
					wouldExceed = currentWidth+addedWidth+moreWidth > mcpMaxWidth
				}

				if wouldExceed {
					// Would exceed - show indicator for remaining
					moreStyle := lipgloss.NewStyle().Foreground(ColorText).Italic(true)
					if mcpCount > 0 {
						mcpResult.WriteString(moreStyle.Render(fmt.Sprintf(" (+%d more)", remaining)))
					} else {
						// No MCPs fit - just show count
						mcpResult.WriteString(moreStyle.Render(fmt.Sprintf("(%d MCPs)", len(mcpParts))))
					}
					break
				}

				// Add separator if not first
				if mcpCount > 0 {
					mcpResult.WriteString(", ")
				}
				mcpResult.WriteString(part)
				currentWidth += addedWidth
				mcpCount++
			}

			b.WriteString(mcpResult.String())
			b.WriteString("\n")
		}

		// Fork hint when session can be forked
		if selected.CanFork() {
			quickForkKey := h.actionKey(hotkeyQuickFork)
			forkWithOptionsKey := h.actionKey(hotkeyForkWithOptions)
			if quickForkKey != "" || forkWithOptionsKey != "" {
				hintStyle := lipgloss.NewStyle().Foreground(ColorText).Italic(true)
				keyStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
				b.WriteString(hintStyle.Render("Fork:    "))
				if quickForkKey != "" {
					b.WriteString(keyStyle.Render(quickForkKey))
					b.WriteString(hintStyle.Render(" quick fork"))
				}
				if quickForkKey != "" && forkWithOptionsKey != "" {
					b.WriteString(hintStyle.Render(", "))
				}
				if forkWithOptionsKey != "" {
					b.WriteString(keyStyle.Render(forkWithOptionsKey))
					b.WriteString(hintStyle.Render(" fork with options"))
				}
				b.WriteString("\n")
			}
		}
	}

	// Gemini-specific info (session ID)
	if selected.Tool == "gemini" {
		geminiHeader := renderSectionDivider("Gemini", width-4)
		b.WriteString(geminiHeader)
		b.WriteString("\n")

		labelStyle := lipgloss.NewStyle().Foreground(ColorText)
		valueStyle := lipgloss.NewStyle().Foreground(ColorText)

		if selected.GeminiSessionID != "" {
			statusStyle := lipgloss.NewStyle().Foreground(ColorGreen).Bold(true)
			b.WriteString(labelStyle.Render("Status:  "))
			b.WriteString(statusStyle.Render("● Connected"))
			b.WriteString("\n")

			b.WriteString(labelStyle.Render("Session: "))
			b.WriteString(valueStyle.Render(selected.GeminiSessionID))
			b.WriteString("\n")

			// Display active model
			modelDisplay := "auto"
			if selected.GeminiModel != "" {
				modelDisplay = selected.GeminiModel
			}
			accentStyle := lipgloss.NewStyle().Foreground(ColorAccent)
			b.WriteString(labelStyle.Render("Model:   "))
			b.WriteString(accentStyle.Render(modelDisplay))
			b.WriteString("\n")

			// MCPs for Gemini (global only)
			mcpInfo := selected.GetMCPInfo()
			renderSimpleMCPLine(&b, mcpInfo, width)
		} else {
			statusStyle := lipgloss.NewStyle().Foreground(ColorText)
			b.WriteString(labelStyle.Render("Status:  "))
			b.WriteString(statusStyle.Render("○ Not connected"))
			b.WriteString("\n")
		}
	}

	// OpenCode-specific info (session ID)
	if selected.Tool == "opencode" {
		opencodeHeader := renderSectionDivider("OpenCode", width-4)
		b.WriteString(opencodeHeader)
		b.WriteString("\n")

		labelStyle := lipgloss.NewStyle().Foreground(ColorText)
		valueStyle := lipgloss.NewStyle().Foreground(ColorText)

		// Debug: log what value we're seeing
		uiLog.Debug(
			"opencode_rendering_preview",
			slog.String("title", selected.Title),
			slog.String("session_id", selected.OpenCodeSessionID),
		)

		if selected.OpenCodeSessionID != "" {
			statusStyle := lipgloss.NewStyle().Foreground(ColorGreen).Bold(true)
			b.WriteString(labelStyle.Render("Status:  "))
			b.WriteString(statusStyle.Render("● Connected"))
			b.WriteString("\n")

			b.WriteString(labelStyle.Render("Session: "))
			b.WriteString(valueStyle.Render(selected.OpenCodeSessionID))
			b.WriteString("\n")

			// Show when session was detected
			if !selected.OpenCodeDetectedAt.IsZero() {
				detectedAgo := formatRelativeTime(selected.OpenCodeDetectedAt)
				dimStyle := lipgloss.NewStyle().Foreground(ColorText).Italic(true)
				b.WriteString(labelStyle.Render("Detected:"))
				b.WriteString(dimStyle.Render(" " + detectedAgo))
				b.WriteString("\n")
			}

			// Fork hint for OpenCode
			if selected.CanFork() {
				h.renderForkHintLine(&b)
			}
		} else {
			// Check if detection has completed (OpenCodeDetectedAt is set even when no session found)
			if selected.OpenCodeDetectedAt.IsZero() {
				// Detection not yet completed - show detecting state
				statusStyle := lipgloss.NewStyle().Foreground(ColorYellow)
				b.WriteString(labelStyle.Render("Status:  "))
				b.WriteString(statusStyle.Render("◐ Detecting session..."))
				b.WriteString("\n")
			} else {
				// Detection completed but no session found
				statusStyle := lipgloss.NewStyle().Foreground(ColorText)
				b.WriteString(labelStyle.Render("Status:  "))
				b.WriteString(statusStyle.Render("○ No session found"))
				b.WriteString("\n")
			}
		}
	}

	// Codex-specific info (session ID, detection)
	if selected.Tool == "codex" {
		codexHeader := renderSectionDivider("Codex", width-4)
		b.WriteString(codexHeader)
		b.WriteString("\n")

		renderToolStatusLine(&b, selected.CodexSessionID, selected.CodexDetectedAt, true)
		if selected.CodexSessionID != "" {
			renderDetectedAtLine(&b, selected.CodexDetectedAt)
		}
	}

	// Custom tool info (tools defined in config.toml that aren't built-in)
	if !session.IsClaudeCompatible(selected.Tool) && selected.Tool != "gemini" && selected.Tool != "opencode" &&
		selected.Tool != "codex" {
		if toolDef := session.GetToolDef(selected.Tool); toolDef != nil {
			toolName := selected.Tool
			if toolDef.Icon != "" {
				toolName = toolDef.Icon + " " + toolName
			}
			customHeader := renderSectionDivider(toolName, width-4)
			b.WriteString(customHeader)
			b.WriteString("\n")

			labelStyle := lipgloss.NewStyle().Foreground(ColorText)

			genericID := selected.GetGenericSessionID()
			if genericID != "" {
				statusStyle := lipgloss.NewStyle().Foreground(ColorGreen).Bold(true)
				valueStyle := lipgloss.NewStyle().Foreground(ColorText)
				b.WriteString(labelStyle.Render("Status:  "))
				b.WriteString(statusStyle.Render("● Connected"))
				b.WriteString("\n")

				b.WriteString(labelStyle.Render("Session: "))
				b.WriteString(valueStyle.Render(genericID))
				b.WriteString("\n")
			} else {
				statusStyle := lipgloss.NewStyle().Foreground(ColorText)
				b.WriteString(labelStyle.Render("Status:  "))
				b.WriteString(statusStyle.Render("○ Not connected"))
				b.WriteString("\n")
			}

			// Resume hint when tool supports restart with session resume
			if selected.CanRestartGeneric() {
				if restartKey := h.actionKey(hotkeyRestart); restartKey != "" {
					hintStyle := lipgloss.NewStyle().Foreground(ColorText).Italic(true)
					keyStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
					b.WriteString(hintStyle.Render("Resume:  "))
					b.WriteString(keyStyle.Render(restartKey))
					b.WriteString(hintStyle.Render(" restart with session resume"))
					b.WriteString("\n")
				}
			}
			if selected.CanRestartFresh() {
				if restartFreshKey := h.actionKey(hotkeyRestartFresh); restartFreshKey != "" {
					hintStyle := lipgloss.NewStyle().Foreground(ColorText).Italic(true)
					keyStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
					b.WriteString(hintStyle.Render("Fresh:   "))
					b.WriteString(keyStyle.Render(restartFreshKey))
					b.WriteString(hintStyle.Render(" restart with a new session ID"))
					b.WriteString("\n")
				}
			}
		}
	}

	b.WriteString("\n")

	// Check preview settings for what to show
	config, _ := session.LoadUserConfig()
	showAnalytics := config != nil && config.GetShowAnalytics() &&
		(session.IsClaudeCompatible(selected.Tool) || selected.Tool == "gemini")
	showOutput := config == nil || config.GetShowOutput() // Default to true if config fails
	showNotes := config != nil && config.GetShowNotes()   // Default to false if config fails
	notesOutputSplit := 0.33
	if config != nil {
		notesOutputSplit = config.Preview.GetNotesOutputSplit()
	}

	// Apply preview mode override (v key cycles through modes)
	switch h.previewMode {
	case PreviewModeOutput:
		showAnalytics = false
		showOutput = true
	case PreviewModeAnalytics:
		// showAnalytics keeps its default value (only available for Claude/Gemini)
		showOutput = false
		// PreviewModeBoth: use config settings (default)
	}

	// Special handling for stopped state - user-intentional stop with resume guidance
	if selectedStatus == session.StatusStopped {
		stoppedHeader := renderSectionDivider("Session Stopped", width-4)
		b.WriteString(stoppedHeader)
		b.WriteString("\n\n")

		warnStyle := lipgloss.NewStyle().Foreground(ColorYellow)
		dimStyle := lipgloss.NewStyle().Foreground(ColorText)
		keyStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)

		b.WriteString(warnStyle.Render("■ Session stopped by user"))
		b.WriteString("\n\n")
		b.WriteString(dimStyle.Render("You stopped this session intentionally."))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("The session record is preserved for resuming."))
		b.WriteString("\n\n")
		b.WriteString(dimStyle.Render("Actions:"))
		b.WriteString("\n")
		if restartKey := h.actionKey(hotkeyRestart); restartKey != "" {
			b.WriteString("  ")
			b.WriteString(keyStyle.Render(restartKey))
			b.WriteString(dimStyle.Render(" Resume  - restart with session resume"))
			b.WriteString("\n")
		}
		if selected.CanRestartFresh() {
			if restartFreshKey := h.actionKey(hotkeyRestartFresh); restartFreshKey != "" {
				b.WriteString("  ")
				b.WriteString(keyStyle.Render(restartFreshKey))
				b.WriteString(dimStyle.Render(" Fresh   - restart with a new session ID"))
				b.WriteString("\n")
			}
		}
		if deleteKey := h.actionKey(hotkeyDelete); deleteKey != "" {
			b.WriteString("  ")
			b.WriteString(keyStyle.Render(deleteKey))
			b.WriteString(dimStyle.Render(" Delete  - remove from list"))
			b.WriteString("\n")
		}
		b.WriteString("  ")
		b.WriteString(keyStyle.Render("Enter"))
		b.WriteString(dimStyle.Render(" - attach (will auto-start)"))
		b.WriteString("\n")
		if selected.IsMultiRepo() {
			if editPathsKey := h.actionKey(hotkeyEditPaths); editPathsKey != "" {
				b.WriteString("  ")
				b.WriteString(keyStyle.Render(editPathsKey))
				b.WriteString(dimStyle.Render(" Paths   - edit multi-repo paths"))
				b.WriteString("\n")
			}
		}

		// Pad output to exact height to prevent layout shifts
		content := b.String()
		lines := strings.Split(content, "\n")
		lineCount := len(lines)

		if lineCount < height {
			for i := lineCount; i < height; i++ {
				content += "\n"
			}
		}

		if len(content) > 0 && content[len(content)-1] == '\n' {
			content = content[:len(content)-1]
		}

		return content
	}

	// Special handling for error state - crash/unexpected failure with diagnostic guidance
	if selectedStatus == session.StatusError {
		errorHeader := renderSectionDivider("Session Error", width-4)
		b.WriteString(errorHeader)
		b.WriteString("\n\n")

		warnStyle := lipgloss.NewStyle().Foreground(ColorYellow)
		dimStyle := lipgloss.NewStyle().Foreground(ColorText)
		keyStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)

		b.WriteString(warnStyle.Render("✕ No tmux session running"))
		b.WriteString("\n\n")
		b.WriteString(dimStyle.Render("This can happen if:"))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("  - Session was added but not yet started"))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("  - tmux server was restarted"))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("  - Terminal was closed or system rebooted"))
		b.WriteString("\n\n")
		b.WriteString(dimStyle.Render("Actions:"))
		b.WriteString("\n")
		if restartKey := h.actionKey(hotkeyRestart); restartKey != "" {
			b.WriteString("  ")
			b.WriteString(keyStyle.Render(restartKey))
			b.WriteString(dimStyle.Render(" Start   - create and start tmux session"))
			b.WriteString("\n")
		}
		if selected.CanRestartFresh() {
			if restartFreshKey := h.actionKey(hotkeyRestartFresh); restartFreshKey != "" {
				b.WriteString("  ")
				b.WriteString(keyStyle.Render(restartFreshKey))
				b.WriteString(dimStyle.Render(" Fresh   - start without resuming the prior session"))
				b.WriteString("\n")
			}
		}
		if deleteKey := h.actionKey(hotkeyDelete); deleteKey != "" {
			b.WriteString("  ")
			b.WriteString(keyStyle.Render(deleteKey))
			b.WriteString(dimStyle.Render(" Delete  - remove from list"))
			b.WriteString("\n")
		}
		b.WriteString("  ")
		b.WriteString(keyStyle.Render("Enter"))
		b.WriteString(dimStyle.Render(" - attach (will auto-start)"))
		b.WriteString("\n")
		if selected.IsMultiRepo() {
			if editPathsKey := h.actionKey(hotkeyEditPaths); editPathsKey != "" {
				b.WriteString("  ")
				b.WriteString(keyStyle.Render(editPathsKey))
				b.WriteString(dimStyle.Render(" Paths   - edit multi-repo paths"))
				b.WriteString("\n")
			}
		}

		// Pad output to exact height to prevent layout shifts
		content := b.String()
		lines := strings.Split(content, "\n")
		lineCount := len(lines)

		if lineCount < height {
			for i := lineCount; i < height; i++ {
				content += "\n"
			}
		}

		if len(content) > 0 && content[len(content)-1] == '\n' {
			content = content[:len(content)-1]
		}

		return content
	}

	// Check if session is launching/resuming (for animation priority)
	_, isSessionLaunching := h.launchingSessions[selected.ID]
	_, isSessionResuming := h.resumingSessions[selected.ID]
	_, isSessionForking := h.forkingSessions[selected.ID]
	isStartingUp := isSessionLaunching || isSessionResuming || isSessionForking

	// Analytics panel (for Claude/Gemini sessions with analytics enabled)
	// Skip showing "Loading analytics..." during startup - let the launch animation take focus
	if showAnalytics && !isStartingUp {
		analyticsHeader := renderSectionDivider("Analytics", width-4)
		b.WriteString(analyticsHeader)
		b.WriteString("\n")

		// Check if we have analytics for this session
		if h.analyticsSessionID == selected.ID && (h.currentAnalytics != nil || h.currentGeminiAnalytics != nil) {
			// Pass display settings from config
			if config != nil {
				h.analyticsPanel.SetDisplaySettings(config.Preview.GetAnalyticsSettings())
			}
			h.analyticsPanel.SetSize(width-4, height/2)
			b.WriteString(h.analyticsPanel.View())
			b.WriteString("\n")
		} else {
			// Analytics not yet loaded
			loadingStyle := lipgloss.NewStyle().
				Foreground(ColorText).
				Italic(true)
			b.WriteString(loadingStyle.Render("Loading analytics..."))
			b.WriteString("\n\n")
		}
	}

	remainingLines := height - (strings.Count(b.String(), "\n") + 1)
	if showNotes {
		notesLines := notesSectionLineBudget(remainingLines, showOutput || isStartingUp, notesOutputSplit)
		if notesLines > 0 {
			b.WriteString(h.renderNotesSection(selected, width, notesLines))
			b.WriteString("\n")
		}
	}

	// If output is disabled AND not starting up, return early
	// (We want to show the launch animation even if output is normally disabled)
	if !showOutput && !isStartingUp {
		// If analytics was also not shown, display session info card as fallback
		hasNotesContent := strings.TrimSpace(selected.Notes) != "" ||
			(h.notesEditing && h.notesEditingSessionID == selected.ID)
		if !showAnalytics && !hasNotesContent {
			infoCard := h.renderSessionInfoCard(selected, width, height)
			b.WriteString("\n")
			b.WriteString(infoCard)
		}

		// Pad output to exact height to prevent layout shifts
		content := b.String()
		lines := strings.Split(content, "\n")
		lineCount := len(lines)
		if lineCount < height {
			for i := lineCount; i < height; i++ {
				content += "\n"
			}
		}
		if len(content) > 0 && content[len(content)-1] == '\n' {
			content = content[:len(content)-1]
		}
		return content
	}

	// Terminal output header
	termHeader := renderSectionDivider("Output", width-4)
	b.WriteString(termHeader)
	b.WriteString("\n")

	// Check if this session is launching (newly created), resuming (restarted), or forking
	launchTime, isLaunching := h.launchingSessions[selected.ID]
	resumeTime, isResuming := h.resumingSessions[selected.ID]
	mcpLoadTime, isMcpLoading := h.mcpLoadingSessions[selected.ID]
	forkTime, isForking := h.forkingSessions[selected.ID]

	// Determine if we should show animation (launch, resume, MCP loading, or forking)
	// For Claude: show for minimum 6 seconds, then check for ready indicators
	// For others: show for first 3 seconds after creation
	showLaunchingAnimation := false
	showMcpLoadingAnimation := false
	showForkingAnimation := isForking // Show forking animation immediately
	var animationStartTime time.Time
	if isLaunching {
		animationStartTime = launchTime
	} else if isResuming {
		animationStartTime = resumeTime
	} else if isMcpLoading {
		animationStartTime = mcpLoadTime
	}

	// Apply STATUS-BASED animation logic (matches hasActiveAnimation exactly)
	// Animation shows until session is ready, detected via status or content
	if isLaunching || isResuming || isMcpLoading {
		timeSinceStart := time.Since(animationStartTime)

		// Brief minimum to prevent flicker
		if timeSinceStart < launchAnimationMinDuration(selected.Tool) {
			if isMcpLoading {
				showMcpLoadingAnimation = true
			} else {
				showLaunchingAnimation = true
			}
		} else if timeSinceStart < 15*time.Second {
			// STATUS-BASED CHECK: Session ready when Running/Waiting/Idle
			sessionReady := selectedStatus == session.StatusRunning ||
				selectedStatus == session.StatusWaiting ||
				selectedStatus == session.StatusIdle

			if !sessionReady {
				// Also check content for faster detection
				h.previewCacheMu.RLock()
				previewContent := h.previewCache[pvKey]
				h.previewCacheMu.RUnlock()

				// Strip ANSI for reliable pattern matching
				plainPreview := ansi.Strip(previewContent)

				if session.IsClaudeCompatible(selected.Tool) || selected.Tool == "gemini" {
					// Claude/Gemini ready indicators
					agentReady := strings.Contains(plainPreview, "ctrl+c to interrupt") ||
						strings.Contains(plainPreview, "No, and tell Claude what to do differently") ||
						strings.Contains(plainPreview, "\n> ") ||
						strings.Contains(plainPreview, "> \n") ||
						strings.Contains(plainPreview, "esc to interrupt") ||
						strings.Contains(plainPreview, "⠋") || strings.Contains(plainPreview, "⠙") ||
						strings.Contains(plainPreview, "Thinking") ||
						strings.Contains(plainPreview, "╭─")

					if selected.Tool == "gemini" {
						agentReady = agentReady ||
							strings.Contains(plainPreview, "▸") ||
							strings.Contains(plainPreview, "gemini>")
					}

					if !agentReady {
						if isMcpLoading {
							showMcpLoadingAnimation = true
						} else {
							showLaunchingAnimation = true
						}
					}
				} else {
					// Non-Claude/Gemini: ready if substantial content
					if len(strings.TrimSpace(plainPreview)) <= 50 {
						if isMcpLoading {
							showMcpLoadingAnimation = true
						} else {
							showLaunchingAnimation = true
						}
					}
				}
			}
		}
		// After 15 seconds, animation stops regardless
	}

	// Terminal preview - use cached content (async fetching keeps View() pure)
	h.previewCacheMu.RLock()
	preview, hasCached := h.previewCache[pvKey]
	h.previewCacheMu.RUnlock()

	// Show forking animation when fork is in progress (highest priority)
	if showForkingAnimation {
		b.WriteString("\n")
		b.WriteString(h.renderForkingState(selected, width, forkTime))
	} else if showMcpLoadingAnimation {
		// Show MCP loading animation when reloading MCPs
		b.WriteString("\n")
		b.WriteString(h.renderMcpLoadingState(selected, width, mcpLoadTime))
	} else if showLaunchingAnimation {
		// Show launching animation for new sessions
		b.WriteString("\n")
		b.WriteString(h.renderLaunchingState(selected, width, animationStartTime))
	} else if !hasCached {
		// Show loading indicator while waiting for async fetch
		loadingStyle := lipgloss.NewStyle().
			Foreground(ColorText).
			Italic(true)
		b.WriteString(loadingStyle.Render("Loading preview..."))
	} else if preview == "" {
		emptyTerm := lipgloss.NewStyle().
			Foreground(ColorText).
			Italic(true).
			Render("(terminal is empty)")
		b.WriteString(emptyTerm)
	} else {
		// Calculate maxLines dynamically based on how many header lines we've already written
		// This accounts for Claude sessions having more header lines than other sessions
		currentContent := b.String()
		headerLines := strings.Count(currentContent, "\n") + 1 // +1 for the current line
		lines := strings.Split(preview, "\n")

		// Strip trailing empty lines BEFORE truncation
		// This ensures we show actual content, not empty trailing lines when space is limited
		// (Terminal output often ends with empty lines at cursor position)
		for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
			lines = lines[:len(lines)-1]
		}

		// If all lines were empty, show empty indicator
		if len(lines) == 0 {
			emptyTerm := lipgloss.NewStyle().
				Foreground(ColorText).
				Italic(true).
				Render("(terminal is empty)")
			b.WriteString(emptyTerm)
			return b.String()
		}

		maxLines := height - headerLines - 1 // -1 for potential truncation indicator
		if maxLines < 1 {
			maxLines = 1
		}

		// Track if we're truncating from the top (for indicator)
		truncatedFromTop := len(lines) > maxLines
		truncatedCount := 0
		scrolledBelow := 0
		if truncatedFromTop {
			// Reserve one line for the "⋮ N more above" indicator
			maxLines--
			if maxLines < 1 {
				maxLines = 1
			}
			// #574: slide the visible window up by previewScrollOffset lines
			// so the user can see older output instead of the tail. Clamp
			// the offset to the valid range so arbitrary values don't go
			// out of bounds or below zero.
			maxOffset := len(lines) - maxLines
			if maxOffset < 0 {
				maxOffset = 0
			}
			if h.previewScrollOffset > maxOffset {
				h.previewScrollOffset = maxOffset
			}
			if h.previewScrollOffset < 0 {
				h.previewScrollOffset = 0
			}
			endIdx := len(lines) - h.previewScrollOffset
			startIdx := endIdx - maxLines
			if startIdx < 0 {
				startIdx = 0
			}
			truncatedCount = startIdx
			scrolledBelow = len(lines) - endIdx
			lines = lines[startIdx:endIdx]
		} else {
			// Content fits without truncation — offset has no effect, keep state consistent.
			h.previewScrollOffset = 0
		}
		_ = scrolledBelow // reserved for a future "⋮ N below" indicator; offset clamp already prevents stale state

		maxWidth := width - 4
		if maxWidth < 10 {
			maxWidth = 10
		}

		// Show truncation indicator if content was cut from top
		if truncatedFromTop {
			truncIndicator := lipgloss.NewStyle().
				Foreground(ColorText).
				Italic(true).
				Render(fmt.Sprintf("⋮ %d more lines above", truncatedCount))
			b.WriteString(truncIndicator)
			b.WriteString("\n")
		}

		// Track consecutive empty lines to preserve some spacing
		consecutiveEmpty := 0
		const maxConsecutiveEmpty = 2 // Allow up to 2 consecutive empty lines

		isLightTheme := GetCurrentTheme() == ThemeLight
		for _, line := range lines {
			// Strip dangerous control characters (\r, \b, etc.) but preserve
			// ANSI escape sequences (ESC = 0x1b) so colors and formatting
			// from the captured terminal output pass through to display.
			safeLine := stripControlCharsPreserveANSI(line)

			// Strip CSI K (Erase in Line) and CSI J (Erase in Display).
			// Without this, captured content (e.g. Neovim mini.statusline)
			// instructs the outer terminal to paint the active SGR
			// background beyond the pane's truncation point. See #579.
			safeLine = stripDisplayErasingEscapes(safeLine)

			// In light theme, remap captured ANSI background colors to the
			// current preview surface instead of stripping them completely.
			// This preserves the soft highlighted blocks used by tools like
			// Codex without letting dark background bands bleed through.
			if isLightTheme {
				safeLine = remapANSIBackground(safeLine, previewSurfaceANSI())
			}

			// Check if visually empty (strip ANSI for this check)
			stripped := ansi.Strip(safeLine)
			trimmed := strings.TrimSpace(stripped)
			if trimmed == "" {
				consecutiveEmpty++
				if consecutiveEmpty <= maxConsecutiveEmpty {
					b.WriteString("\n") // Preserve empty line
				}
				continue
			}
			consecutiveEmpty = 0 // Reset counter on non-empty line

			// Truncate based on display width using ANSI-aware measurement
			// ansi.StringWidth ignores escape sequences for accurate width
			displayWidth := ansi.StringWidth(safeLine)
			if displayWidth > maxWidth {
				// ansi.Truncate preserves ANSI codes while truncating visible content
				safeLine = ansi.Truncate(safeLine, maxWidth-3, "...")
			}

			b.WriteString(safeLine)
			b.WriteString("\n")
		}
	}

	// CRITICAL: Enforce width constraint on ALL lines to prevent overflow into left panel
	// When lipgloss.JoinHorizontal combines panels, any line exceeding rightWidth
	// will wrap and corrupt the layout
	maxWidth := width - 2 // Small margin for safety
	if maxWidth < 20 {
		maxWidth = 20
	}

	result := b.String()
	lines := strings.Split(result, "\n")
	var truncatedLines []string
	for _, line := range lines {
		// Use ANSI-aware width measurement to handle lines with escape codes
		displayWidth := ansi.StringWidth(line)
		if displayWidth > maxWidth {
			// ANSI-aware truncation preserves escape codes while trimming visible content
			truncated := ansi.Truncate(line, maxWidth-3, "...")
			truncatedLines = append(truncatedLines, truncated)
		} else {
			truncatedLines = append(truncatedLines, line)
		}
	}

	return strings.Join(truncatedLines, "\n")
}

func notesSectionLineBudget(remaining int, reserveOutput bool, split float64) int {
	if remaining <= 0 {
		return 0
	}

	if !reserveOutput {
		return remaining
	}

	if split <= 0 {
		split = 0.33
	}

	notesLines := int(float64(remaining) * split)
	if notesLines < 3 {
		notesLines = 3
	}

	maxNotes := remaining - 3
	if maxNotes < 1 {
		maxNotes = 1
	}
	if notesLines > maxNotes {
		notesLines = maxNotes
	}

	return notesLines
}

func (h *Home) renderNotesSection(inst *session.Instance, width, maxLines int) string {
	if inst == nil || maxLines <= 0 {
		return ""
	}
	if maxLines < 2 {
		maxLines = 2
	}

	contentWidth := width - 4
	if contentWidth < 10 {
		contentWidth = 10
	}

	lines := make([]string, 0, maxLines)
	lines = append(lines, renderSectionDivider("Notes", width-4))
	bodyLines := maxLines - 1
	if bodyLines < 1 {
		bodyLines = 1
	}

	hintStyle := lipgloss.NewStyle().Foreground(ColorComment).Italic(true)
	notesStyle := lipgloss.NewStyle().Foreground(ColorText)
	emptyStyle := lipgloss.NewStyle().Foreground(ColorTextDim).Italic(true)

	if h.notesEditing && h.notesEditingSessionID == inst.ID {
		editorLines := bodyLines - 1
		if editorLines < 1 {
			editorLines = 1
		}

		h.notesEditor.SetWidth(contentWidth)
		h.notesEditor.SetHeight(editorLines)
		h.notesEditor.Focus()

		editorView := h.notesEditor.View()
		viewLines := strings.Split(editorView, "\n")
		if len(viewLines) > editorLines {
			viewLines = viewLines[:editorLines]
		}
		for len(viewLines) < editorLines {
			viewLines = append(viewLines, "")
		}

		for _, line := range viewLines {
			lines = append(lines, ansi.Truncate(line, contentWidth, "..."))
		}

		lines = append(lines, hintStyle.Render("Ctrl+S save • Esc cancel"))
	} else {
		notesBodyLines := bodyLines - 1
		if notesBodyLines < 1 {
			notesBodyLines = 1
		}

		rawNotes := strings.TrimRight(inst.Notes, "\n")
		rawLines := strings.Split(rawNotes, "\n")
		if len(rawLines) == 1 && strings.TrimSpace(rawLines[0]) == "" {
			rawLines = nil
		}

		if len(rawLines) == 0 {
			lines = append(lines, emptyStyle.Render("No notes"))
		} else {
			overflow := len(rawLines) > notesBodyLines
			displayLines := rawLines
			if overflow {
				displayLines = rawLines[:notesBodyLines]
			}
			for _, line := range displayLines {
				safe := stripControlCharsPreserveANSI(line)
				safe = runewidth.Truncate(safe, contentWidth, "...")
				lines = append(lines, notesStyle.Render(safe))
			}
			if overflow && len(lines) > 0 {
				more := len(rawLines) - notesBodyLines
				lines[len(lines)-1] = hintStyle.Render(fmt.Sprintf("... +%d more lines", more))
			}
		}

		hintText := "Notes hotkey unbound"
		if editKey := h.actionKey(hotkeyEditNotes); editKey != "" {
			hintText = fmt.Sprintf("%s edit notes", editKey)
		}
		lines = append(lines, hintStyle.Render(hintText))
	}

	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	for len(lines) < maxLines {
		lines = append(lines, "")
	}

	return strings.Join(lines, "\n")
}

// stripControlCharsPreserveANSI removes dangerous C0 control characters while
// preserving ANSI escape sequences (ESC = 0x1b). This allows terminal colors
// and formatting from capture-pane -e output to pass through to display, while
// still stripping \r, \b, and other control chars that corrupt TUI layout.
func stripControlCharsPreserveANSI(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' && r != '\t' && r != '\x1b' {
			return -1 // Drop the character
		}
		return r
	}, s)
}

// ansiBackgroundRE matches ANSI background color escape sequences:
//   - ESC[40m..ESC[47m  — standard 8-color backgrounds
//   - ESC[100m..ESC[107m — bright/high-intensity backgrounds
//   - ESC[48;...m        — 256-color and true-color backgrounds (ESC[48;5;Nm / ESC[48;2;R;G;Bm)
var ansiBackgroundRE = regexp.MustCompile(`\x1b\[(?:4[0-7]|10[0-7]|48;[0-9;]+)m`)

// eraseEscapesRE matches CSI "Erase in Line" (ESC [ ... K) and CSI
// "Erase in Display" (ESC [ ... J). These tell the outer terminal to
// paint the currently-active SGR background across the remainder of
// the physical row (K) or screen (J). When captured content is
// rendered inside the preview pane, that paint extends past the pane
// boundary — the regression reported as #579, where Neovim's
// mini.statusline leaks its green bar past the right edge. Stripping
// K/J makes every cell the preview emits explicit, so the outer
// terminal paints only what fits the truncation budget.
var eraseEscapesRE = regexp.MustCompile(`\x1b\[[0-9;?]*[KJ]`)

// stripDisplayErasingEscapes removes CSI K / CSI J sequences from
// captured terminal content while preserving SGR (color/attribute)
// sequences.
func stripDisplayErasingEscapes(s string) string {
	return eraseEscapesRE.ReplaceAllString(s, "")
}

// previewSurfaceANSI returns a truecolor ANSI background sequence matching
// the current preview surface. Falls back to empty string if the color is not
// a hex RGB value.
func previewSurfaceANSI() string {
	hex := strings.TrimPrefix(string(ColorSurface), "#")
	if len(hex) != 6 {
		return ""
	}
	r, err := strconv.ParseUint(hex[0:2], 16, 8)
	if err != nil {
		return ""
	}
	g, err := strconv.ParseUint(hex[2:4], 16, 8)
	if err != nil {
		return ""
	}
	b, err := strconv.ParseUint(hex[4:6], 16, 8)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("\x1b[48;2;%d;%d;%dm", r, g, b)
}

// remapANSIBackground replaces ANSI background color sequences with the
// provided replacement while preserving all other ANSI sequences (foreground
// colors, bold, italic, underline). Used in light theme so captured terminal
// output keeps soft highlighted regions instead of dropping them entirely.
func remapANSIBackground(s, replacement string) string {
	if replacement == "" {
		return ansiBackgroundRE.ReplaceAllString(s, "")
	}
	return ansiBackgroundRE.ReplaceAllString(s, replacement)
}

// truncatePath shortens a path to fit within maxLen display width
func truncatePath(path string, maxLen int) string {
	pathWidth := runewidth.StringWidth(path)
	if pathWidth <= maxLen {
		return path
	}
	if maxLen < 10 {
		maxLen = 10
	}
	// Show beginning and end: /Users/.../project
	// Use rune-based slicing for proper Unicode handling
	runes := []rune(path)
	startLen := maxLen / 3
	endLen := maxLen*2/3 - 3
	if startLen+endLen+3 > len(runes) {
		// Path is short in runes but wide in display - use simple truncation
		return runewidth.Truncate(path, maxLen-3, "...")
	}
	return string(runes[:startLen]) + "..." + string(runes[len(runes)-endLen:])
}

// formatRelativeTime formats a time as a human-readable relative string
// Examples: "just now", "2m ago", "1h ago", "3h ago", "1d ago"
func formatRelativeTime(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}

	d := time.Since(t)

	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		mins := int(d.Minutes())
		if mins == 1 {
			return "1m ago"
		}
		return fmt.Sprintf("%dm ago", mins)
	case d < 24*time.Hour:
		hours := int(d.Hours())
		if hours == 1 {
			return "1h ago"
		}
		return fmt.Sprintf("%dh ago", hours)
	default:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1d ago"
		}
		return fmt.Sprintf("%dd ago", days)
	}
}

// renderGroupPreview renders the preview pane for a group
func (h *Home) renderGroupPreview(group *session.Group, width, height int) string {
	var b strings.Builder

	// Group header with folder icon
	headerStyle := lipgloss.NewStyle().
		Foreground(ColorCyan).
		Bold(true)
	b.WriteString(headerStyle.Render("📁 " + group.Name))
	b.WriteString("\n\n")

	// Session count
	countStyle := lipgloss.NewStyle().
		Foreground(ColorText).
		Bold(true)
	b.WriteString(countStyle.Render(fmt.Sprintf("%d sessions", len(group.Sessions))))
	b.WriteString("\n\n")

	// Status breakdown with inline badges
	running, waiting, idle, errored := 0, 0, 0, 0
	for _, sess := range group.Sessions {
		switch sess.Status {
		case session.StatusRunning:
			running++
		case session.StatusWaiting:
			waiting++
		case session.StatusIdle:
			idle++
		case session.StatusError, session.StatusStopped:
			errored++
		}
	}

	// Compact status line (inline, not badges)
	var statuses []string
	if running > 0 {
		statuses = append(
			statuses,
			lipgloss.NewStyle().Foreground(ColorGreen).Render(fmt.Sprintf("● %d running", running)),
		)
	}
	if waiting > 0 {
		statuses = append(
			statuses,
			lipgloss.NewStyle().Foreground(ColorYellow).Render(fmt.Sprintf("◐ %d waiting", waiting)),
		)
	}
	if idle > 0 {
		statuses = append(statuses, lipgloss.NewStyle().Foreground(ColorText).Render(fmt.Sprintf("○ %d idle", idle)))
	}
	if errored > 0 {
		statuses = append(statuses, lipgloss.NewStyle().Foreground(ColorRed).Render(fmt.Sprintf("✕ %d error", errored)))
	}

	if len(statuses) > 0 {
		b.WriteString(strings.Join(statuses, "  "))
		b.WriteString("\n\n")
	}

	// Repository worktree summary (when all sessions share the same repo root)
	if repoInfo := h.getGroupWorktreeInfo(group); repoInfo != nil {
		b.WriteString(renderSectionDivider("Repository", width-4))
		b.WriteString("\n")

		repoLabelStyle := lipgloss.NewStyle().Foreground(ColorText)
		repoValueStyle := lipgloss.NewStyle().Foreground(ColorText)
		repoBranchStyle := lipgloss.NewStyle().Foreground(ColorCyan).Bold(true)

		b.WriteString(repoLabelStyle.Render("Repo:       "))
		b.WriteString(repoValueStyle.Render(truncatePath(repoInfo.repoRoot, width-4-12)))
		b.WriteString("\n")

		b.WriteString(repoLabelStyle.Render("Worktrees:  "))
		b.WriteString(repoValueStyle.Render(fmt.Sprintf("%d active", len(repoInfo.branches))))
		b.WriteString("\n")

		for _, br := range repoInfo.branches {
			dirtyMark := ""
			if br.dirtyChecked {
				if br.isDirty {
					dirtyMark = lipgloss.NewStyle().Foreground(ColorYellow).Render(" (dirty)")
				} else {
					dirtyMark = lipgloss.NewStyle().Foreground(ColorGreen).Render(" (clean)")
				}
			}
			b.WriteString("  ")
			b.WriteString(repoBranchStyle.Render("• " + br.branch))
			b.WriteString(dirtyMark)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	// Sessions divider
	b.WriteString(renderSectionDivider("Sessions", width-4))
	b.WriteString("\n")

	// Session list (compact)
	if len(group.Sessions) == 0 {
		emptyStyle := lipgloss.NewStyle().Foreground(ColorText).Italic(true)
		b.WriteString(emptyStyle.Render("  No sessions in this group"))
		b.WriteString("\n")
	} else {
		maxShow := height - 12
		if maxShow < 3 {
			maxShow = 3
		}
		for i, sess := range group.Sessions {
			if i >= maxShow {
				remaining := len(group.Sessions) - i
				b.WriteString(DimStyle.Render(fmt.Sprintf("  ... +%d more", remaining)))
				break
			}

			// Status icon
			statusIcon := "○"
			statusColor := ColorTextDim
			switch sess.Status {
			case session.StatusRunning:
				statusIcon, statusColor = "●", ColorGreen
			case session.StatusWaiting:
				statusIcon, statusColor = "◐", ColorYellow
			case session.StatusError:
				statusIcon, statusColor = "✕", ColorRed
			case session.StatusStopped:
				statusIcon, statusColor = "■", ColorTextDim
			}
			status := lipgloss.NewStyle().Foreground(statusColor).Render(statusIcon)
			name := lipgloss.NewStyle().Foreground(ColorText).Render(sess.Title)
			tool := lipgloss.NewStyle().Foreground(ColorPurple).Faint(true).Render(sess.Tool)

			b.WriteString(fmt.Sprintf("  %s %s %s\n", status, name, tool))
		}
	}

	// Keyboard hints at bottom
	b.WriteString("\n")
	hintStyle := lipgloss.NewStyle().Foreground(ColorComment).Italic(true)
	hints := []string{"Tab toggle"}
	if key := h.actionKey(hotkeyRename); key != "" {
		hints = append(hints, key+" rename")
	}
	if key := h.actionKey(hotkeyDelete); key != "" {
		hints = append(hints, key+" delete")
	}
	if key := h.actionKey(hotkeyCreateGroup); key != "" {
		hints = append(hints, key+" subgroup")
	}
	b.WriteString(hintStyle.Render(strings.Join(hints, " • ")))

	// CRITICAL: Enforce width constraint on ALL lines to prevent overflow into left panel
	maxWidth := max(width-2, 20)

	result := b.String()
	lines := strings.Split(result, "\n")
	var truncatedLines []string
	for _, line := range lines {
		cleanLine := tmux.StripANSI(line)
		displayWidth := runewidth.StringWidth(cleanLine)
		if displayWidth > maxWidth {
			truncated := runewidth.Truncate(cleanLine, maxWidth-3, "...")
			truncatedLines = append(truncatedLines, truncated)
		} else {
			truncatedLines = append(truncatedLines, line)
		}
	}

	return strings.Join(truncatedLines, "\n")
}

// groupWorktreeBranch holds info about a single worktree branch in a group
type groupWorktreeBranch struct {
	branch       string
	isDirty      bool
	dirtyChecked bool
}

// groupWorktreeInfo holds aggregated worktree info for a group sharing a common repo
type groupWorktreeInfo struct {
	repoRoot string
	branches []groupWorktreeBranch
}

// getGroupWorktreeInfo returns worktree summary if all sessions in the group
// share the same repo root and at least one is a worktree. Returns nil otherwise.
func (h *Home) getGroupWorktreeInfo(group *session.Group) *groupWorktreeInfo {
	if len(group.Sessions) < 2 {
		return nil
	}

	// Check if all sessions share a common repo root and count worktrees
	var commonRepo string
	var branches []groupWorktreeBranch
	for _, sess := range group.Sessions {
		if !sess.IsWorktree() {
			continue
		}
		if commonRepo == "" {
			commonRepo = sess.WorktreeRepoRoot
		} else if sess.WorktreeRepoRoot != commonRepo {
			return nil // Different repos, skip
		}

		// Get dirty status from cache
		h.worktreeDirtyMu.Lock()
		isDirty, hasCached := h.worktreeDirtyCache[sess.ID]
		h.worktreeDirtyMu.Unlock()

		branches = append(branches, groupWorktreeBranch{
			branch:       sess.WorktreeBranch,
			isDirty:      isDirty,
			dirtyChecked: hasCached,
		})
	}

	if len(branches) == 0 {
		return nil
	}

	return &groupWorktreeInfo{
		repoRoot: commonRepo,
		branches: branches,
	}
}

// --- Copy & Send Output helpers ---

const maxTransferSize = 500 * 1024 // 500KB max for inter-session transfer

// copySessionOutput returns a tea.Cmd that copies the session's last response to clipboard.
func (h *Home) copySessionOutput(inst *session.Instance) tea.Cmd {
	return func() tea.Msg {
		content, err := getSessionContent(inst)
		if err != nil {
			return copyResultMsg{err: err}
		}

		termInfo := tmux.GetTerminalInfo()
		result, err := clipboard.Copy(content, termInfo.SupportsOSC52)
		if err != nil {
			return copyResultMsg{err: fmt.Errorf("clipboard: %w", err)}
		}
		return copyResultMsg{
			sessionTitle: inst.Title,
			lineCount:    result.LineCount,
		}
	}
}

// sendOutputToSession returns a tea.Cmd that sends the source session's output to the target.
func (h *Home) sendOutputToSession(source, target *session.Instance) tea.Cmd {
	return func() tea.Msg {
		content, err := getSessionContent(source)
		if err != nil {
			return sendOutputResultMsg{
				targetTitle: target.Title,
				err:         err,
			}
		}

		// Truncate if too large
		if len(content) > maxTransferSize {
			content = content[:maxTransferSize] + "\n[Truncated at 500KB]"
		}

		// Wrap with header/footer
		wrapped := fmt.Sprintf("--- Output from [%s] ---\n%s\n--- End output from [%s] ---\n",
			source.Title, content, source.Title)

		tmuxSession := target.GetTmuxSession()
		if tmuxSession == nil {
			return sendOutputResultMsg{
				targetTitle: target.Title,
				err:         fmt.Errorf("target session has no tmux pane"),
			}
		}

		if err := tmuxSession.SendKeysChunked(wrapped); err != nil {
			return sendOutputResultMsg{
				targetTitle: target.Title,
				err:         fmt.Errorf("send failed: %w", err),
			}
		}

		lineCount := strings.Count(content, "\n")
		return sendOutputResultMsg{
			sourceTitle: source.Title,
			targetTitle: target.Title,
			lineCount:   lineCount,
		}
	}
}

// handleSessionPickerDialogKey handles key events when the session picker is visible.
func (h *Home) handleSessionPickerDialogKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		selected := h.sessionPickerDialog.GetSelected()
		source := h.sessionPickerDialog.GetSource()
		h.sessionPickerDialog.Hide()
		if selected != nil && source != nil {
			return h, h.sendOutputToSession(source, selected)
		}
		return h, nil
	case "esc":
		h.sessionPickerDialog.Hide()
		return h, nil
	default:
		h.sessionPickerDialog.Update(msg)
		return h, nil
	}
}

// handleWorktreeFinishDialogKey processes key events for the worktree finish dialog
func (h *Home) handleWorktreeFinishDialogKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	action := h.worktreeFinishDialog.HandleKey(msg.String())

	switch action {
	case "close":
		return h, nil

	case "confirm":
		// Execute the finish operation
		mergeEnabled, targetBranch, keepBranch := h.worktreeFinishDialog.GetOptions()
		h.worktreeFinishDialog.SetExecuting(true)

		sid := h.worktreeFinishDialog.sessionID
		sTitle := h.worktreeFinishDialog.sessionTitle
		branch := h.worktreeFinishDialog.branchName
		repoRoot := h.worktreeFinishDialog.repoRoot
		wtPath := h.worktreeFinishDialog.worktreePath

		// Find the instance for kill/remove
		h.instancesMu.RLock()
		inst := h.instanceByID[sid]
		h.instancesMu.RUnlock()

		return h, h.finishWorktree(inst, sid, sTitle, branch, repoRoot, wtPath, mergeEnabled, targetBranch, keepBranch)

	case "input":
		// Pass through to text input
		h.worktreeFinishDialog.UpdateTargetInput(msg)
		return h, nil
	}

	return h, nil
}

// finishWorktree performs the worktree finish operation asynchronously:
// merge branch, remove worktree, delete branch, kill session, remove from storage
func (h *Home) finishWorktree(inst *session.Instance, sessionID, sessionTitle, branchName, repoRoot, worktreePath string, mergeEnabled bool, targetBranch string, keepBranch bool) tea.Cmd {
	return func() tea.Msg {
		merged := false

		// Step 1: Merge (if requested)
		if mergeEnabled {
			// Checkout target branch in main repo
			cmd := exec.Command("git", "-C", repoRoot, "checkout", targetBranch)
			checkoutOutput, err := cmd.CombinedOutput()
			if err != nil {
				return worktreeFinishResultMsg{
					sessionID: sessionID, sessionTitle: sessionTitle,
					err: fmt.Errorf("failed to checkout %s: %s", targetBranch, strings.TrimSpace(string(checkoutOutput))),
				}
			}

			// Merge the worktree branch
			if err := git.MergeBranch(repoRoot, branchName); err != nil {
				// Abort the merge to leave things clean
				abortCmd := exec.Command("git", "-C", repoRoot, "merge", "--abort")
				_ = abortCmd.Run()
				return worktreeFinishResultMsg{
					sessionID: sessionID, sessionTitle: sessionTitle,
					err: fmt.Errorf("merge failed (aborted): %v", err),
				}
			}
			merged = true
		}

		// Step 2: Remove worktree
		if _, statErr := os.Stat(worktreePath); !os.IsNotExist(statErr) {
			_ = git.RemoveWorktree(repoRoot, worktreePath, false)
		}
		_ = git.PruneWorktrees(repoRoot)

		// Step 3: Delete branch (if not keeping)
		if !keepBranch {
			// Use force delete if we merged (branch is fully merged), regular delete otherwise
			_ = git.DeleteBranch(repoRoot, branchName, merged)
		}

		// Step 4: Kill tmux session
		if inst != nil && inst.Exists() {
			_ = inst.Kill()
		}

		return worktreeFinishResultMsg{
			sessionID:    sessionID,
			sessionTitle: sessionTitle,
			targetBranch: targetBranch,
			merged:       merged,
		}
	}
}

// getOtherActiveSessions returns sessions excluding the given ID and error/stopped-status sessions.
func (h *Home) getOtherActiveSessions(excludeID string) []*session.Instance {
	var result []*session.Instance
	for _, inst := range h.instances {
		if inst.ID == excludeID {
			continue
		}
		s := inst.GetStatusThreadSafe()
		if s == session.StatusError || s == session.StatusStopped {
			continue
		}
		result = append(result, inst)
	}
	return result
}

// getSessionContent retrieves displayable content from a session.
//
// Refreshes tool session IDs from the live tmux env BEFORE reading, so that
// a stale ClaudeSessionID (e.g. from a prior resumed conversation) cannot
// surface old JSONL content as "the last response". Fix for issue #598
// (cross-session `x` transferred unpredictable content).
//
// Falls back to tmux scrollback capture when structured response lookup
// returns no content.
func getSessionContent(inst *session.Instance) (string, error) {
	var live string
	if session.IsClaudeCompatible(inst.Tool) {
		live = inst.GetSessionIDFromTmux()
	}
	return getSessionContentWithLive(inst, live)
}

// getSessionContentWithLive is the testable core: given a live Claude session
// ID (may be empty), prefer it over any stored ID before reading the last
// response, then fall back to tmux scrollback.
func getSessionContentWithLive(inst *session.Instance, liveClaudeID string) (string, error) {
	if session.IsClaudeCompatible(inst.Tool) && liveClaudeID != "" && liveClaudeID != inst.ClaudeSessionID {
		inst.ClaudeSessionID = liveClaudeID
	}

	// Use best-effort: richer recovery than GetLastResponse if the refreshed
	// ID still doesn't resolve to a readable JSONL.
	if resp, err := inst.GetLastResponseBestEffort(); err == nil && resp != nil && resp.Content != "" {
		return resp.Content, nil
	}

	tmuxSession := inst.GetTmuxSession()
	if tmuxSession == nil {
		return "", fmt.Errorf("no output available for this session")
	}

	content, err := tmuxSession.CaptureFullHistory()
	if err != nil {
		return "", fmt.Errorf("failed to capture output: %w", err)
	}

	content = strings.TrimSpace(content)
	if content == "" {
		return "", fmt.Errorf("no output available for this session")
	}

	return content, nil
}

// renderSystemStatsBlock renders a detailed system stats block for the empty state preview pane.
func (h *Home) renderSystemStatsBlock(width int) string {
	if h.sysStatsCollector == nil {
		return ""
	}

	stats := h.sysStatsCollector.Get()
	show := h.sysStatsConfig.GetShow()
	showSet := make(map[string]bool, len(show))
	for _, s := range show {
		showSet[s] = true
	}

	labelStyle := lipgloss.NewStyle().Foreground(ColorComment).Width(8)
	valStyle := lipgloss.NewStyle().Foreground(ColorText)
	barWidth := width - 20
	if barWidth < 10 {
		barWidth = 10
	}
	if barWidth > 40 {
		barWidth = 40
	}

	var lines []string

	titleStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	lines = append(lines, titleStyle.Render("  System"))
	lines = append(lines, "")

	if showSet["cpu"] && stats.CPU.Available {
		bar := renderBar(stats.CPU.UsagePercent, barWidth)
		lines = append(lines, fmt.Sprintf("  %s %s %s", labelStyle.Render("CPU"), bar, valStyle.Render(fmt.Sprintf("%.0f%%", stats.CPU.UsagePercent))))
	}
	if showSet["ram"] && stats.Memory.Available {
		bar := renderBar(stats.Memory.UsagePercent, barWidth)
		used := sysinfo.FormatBytes(stats.Memory.UsedBytes)
		total := sysinfo.FormatBytes(stats.Memory.TotalBytes)
		lines = append(lines, fmt.Sprintf("  %s %s %s", labelStyle.Render("RAM"), bar, valStyle.Render(fmt.Sprintf("%s/%s", used, total))))
	}
	if showSet["disk"] && stats.Disk.Available {
		bar := renderBar(stats.Disk.UsagePercent, barWidth)
		used := sysinfo.FormatBytes(stats.Disk.UsedBytes)
		total := sysinfo.FormatBytes(stats.Disk.TotalBytes)
		lines = append(lines, fmt.Sprintf("  %s %s %s", labelStyle.Render("Disk"), bar, valStyle.Render(fmt.Sprintf("%s/%s", used, total))))
	}
	if showSet["load"] && stats.Load.Available {
		lines = append(lines, fmt.Sprintf("  %s %s", labelStyle.Render("Load"), valStyle.Render(fmt.Sprintf("%.2f  %.2f  %.2f", stats.Load.Load1, stats.Load.Load5, stats.Load.Load15))))
	}
	if showSet["gpu"] && stats.GPU.Available {
		bar := renderBar(stats.GPU.UsagePercent, barWidth)
		label := "GPU"
		if stats.GPU.Name != "" {
			label = "GPU"
		}
		lines = append(lines, fmt.Sprintf("  %s %s %s", labelStyle.Render(label), bar, valStyle.Render(fmt.Sprintf("%.0f%%", stats.GPU.UsagePercent))))
	}
	if showSet["network"] && stats.Network.Available {
		rx := sysinfo.FormatBytesPerSec(stats.Network.RxBytesPerSec)
		tx := sysinfo.FormatBytesPerSec(stats.Network.TxBytesPerSec)
		lines = append(lines, fmt.Sprintf("  %s %s", labelStyle.Render("Net"), valStyle.Render(fmt.Sprintf("↓ %s  ↑ %s", rx, tx))))
	}

	if len(lines) <= 2 {
		return ""
	}

	return strings.Join(lines, "\n")
}

// renderBar creates an ASCII progress bar: [████░░░░░░]
func renderBar(percent float64, width int) string {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}

	filled := int(percent / 100 * float64(width))
	empty := width - filled

	var color lipgloss.Color
	switch {
	case percent >= 90:
		color = ColorRed
	case percent >= 70:
		color = ColorYellow
	default:
		color = ColorGreen
	}

	filledStyle := lipgloss.NewStyle().Foreground(color)
	emptyStyle := lipgloss.NewStyle().Foreground(ColorBorder)

	return filledStyle.Render(strings.Repeat("█", filled)) + emptyStyle.Render(strings.Repeat("░", empty))
}

// matchesStatusFilter returns true if the given session status matches the
// current filter. For FilterModeActive, everything except error/stopped matches
// (including StatusStarting — sessions being launched count as active).
func matchesStatusFilter(filter, status session.Status) bool {
	if filter == FilterModeActive {
		return status != session.StatusError && status != session.StatusStopped
	}
	return status == filter
}

// cachedFilterBarHint returns the static filter bar hint string.
// Cached after first call since the content never changes after theme init.
var _cachedFilterBarHint string

func cachedFilterBarHint() string {
	if _cachedFilterBarHint == "" {
		_cachedFilterBarHint = lipgloss.NewStyle().
			Foreground(ColorComment).
			Faint(true).
			Render("  !@#$ filter • 0 all • " + FilterKeyActive + " open")
	}
	return _cachedFilterBarHint
}
