package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/asheshgoplani/agent-deck/internal/clipboard"
	"github.com/asheshgoplani/agent-deck/internal/git"
	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
	"github.com/asheshgoplani/agent-deck/internal/update"
)

// Version is set by main.go for update checking
var Version = "0.0.0"

// SetVersion sets the current version for update checking
func SetVersion(v string) {
	Version = v
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

	// logCheckInterval - how often to check for oversized logs (fast check, just file stats)
	// This catches runaway logs before they cause high CPU
	logCheckInterval = 10 * time.Second

	// logMaintenanceInterval - how often to do full log maintenance (orphan cleanup, etc)
	// Prevents runaway log growth that can crash the system
	logMaintenanceInterval = 5 * time.Minute

	// analyticsCacheTTL - how long analytics data remains valid before refresh
	// Analytics don't change frequently, so 5s is a good balance between freshness and performance
	analyticsCacheTTL = 5 * time.Second
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
	setupWizard          *SetupWizard          // For first-run setup
	settingsPanel        *SettingsPanel        // For editing settings
	analyticsPanel       *AnalyticsPanel       // For displaying session analytics
	geminiModelDialog    *GeminiModelDialog    // For selecting Gemini model
	sessionPickerDialog  *SessionPickerDialog  // For sending output to another session
	worktreeFinishDialog *WorktreeFinishDialog // For finishing worktree sessions (merge + cleanup)

	// Analytics cache (async fetching with TTL)
	currentAnalytics       *session.SessionAnalytics                  // Current analytics for selected session (Claude)
	currentGeminiAnalytics *session.GeminiSessionAnalytics            // Current analytics for selected session (Gemini)
	analyticsSessionID     string                                     // Session ID for current analytics
	analyticsFetchingID    string                                     // ID currently being fetched (prevents duplicates)
	analyticsCache         map[string]*session.SessionAnalytics       // TTL cache: sessionID -> analytics (Claude)
	geminiAnalyticsCache   map[string]*session.GeminiSessionAnalytics // TTL cache: sessionID -> analytics (Gemini)
	analyticsCacheTime     map[string]time.Time                       // TTL cache: sessionID -> cache timestamp

	// State
	cursor         int            // Selected item index in flatItems
	viewOffset     int            // First visible item index (for scrolling)
	isAttaching    atomic.Bool    // Prevents View() output during attach (fixes Bubble Tea Issue #431) - atomic for thread safety
	statusFilter   session.Status // Filter sessions by status ("" = all, or specific status)
	previewMode    PreviewMode    // What to show in preview pane (both, output-only, analytics-only)
	err            error
	errTime        time.Time  // When error occurred (for auto-dismiss)
	isReloading    bool       // Visual feedback during auto-reload
	initialLoading bool       // True until first loadSessionsMsg received (shows splash screen)
	isQuitting     bool       // True when user pressed q, shows quitting splash
	reloadVersion  uint64     // Incremented on each reload to prevent stale background saves
	reloadMu       sync.Mutex // Protects reloadVersion, isReloading, and lastLoadMtime for thread-safe access
	lastLoadMtime  time.Time  // File mtime when we last loaded (for external change detection)

	// Preview cache (async fetching - View() must be pure, no blocking I/O)
	previewCache      map[string]string    // sessionID -> cached preview content
	previewCacheTime  map[string]time.Time // sessionID -> when cached (for expiration)
	previewCacheMu    sync.RWMutex         // Protects previewCache for thread-safety
	previewFetchingID string               // ID currently being fetched (prevents duplicate fetches)

	// Preview debouncing (PERFORMANCE: prevents subprocess spawn on every keystroke)
	// During rapid navigation, we delay preview fetch by 150ms to let navigation settle
	pendingPreviewID  string     // Session ID waiting for debounced fetch
	previewDebounceMu sync.Mutex // Protects pendingPreviewID

	// Round-robin status updates (Priority 1A optimization)
	// Instead of updating ALL sessions every tick, we update batches of 5-10 sessions
	// This reduces CPU usage by 90%+ while maintaining responsiveness
	statusUpdateIndex atomic.Int32 // Current position in round-robin cycle (atomic for thread safety)

	// Background status worker (Priority 1C optimization)
	// Moves status updates to a separate goroutine, completely decoupling from UI
	statusTrigger    chan statusUpdateRequest // Triggers background status update
	statusWorkerDone chan struct{}            // Signals worker has stopped

	// PERFORMANCE: Worker pool for output-driven status updates (Priority 2)
	// Caps the number of goroutines spawned for %output events from control pipes
	logUpdateChan chan *session.Instance // Buffers status update requests from PipeManager
	logWorkerWg   sync.WaitGroup         // Tracks log worker goroutines for clean shutdown

	// PERFORMANCE: Debounce output activity status updates
	lastLogActivity map[string]time.Time // sessionID -> last update time
	logActivityMu   sync.Mutex           // Protects lastLogActivity map

	// Worktree dirty status cache (lazy, 10s TTL)
	worktreeDirtyCache   map[string]bool      // sessionID -> isDirty
	worktreeDirtyCacheTs map[string]time.Time // sessionID -> cache timestamp
	worktreeDirtyMu      sync.Mutex           // Protects dirty cache maps

	// Memory management: periodic cache pruning
	lastCachePrune time.Time

	// Hook-based status detection (Claude Code lifecycle hooks)
	hookWatcher        *session.StatusFileWatcher
	pendingHooksPrompt bool // True if user should be prompted to install hooks

	// File watcher for external changes (auto-reload)
	storageWatcher *StorageWatcher

	// System theme watcher (active when theme="system"; nil otherwise)
	themeWatcher *ThemeWatcher

	// Storage warning (shown if storage initialization failed)
	storageWarning string

	// Watcher warning (shown if fsnotify may not work, e.g., on 9p/NFS)
	watcherWarning string

	// Update notification (async check on startup)
	updateInfo *update.UpdateInfo

	// Launching animation state (for newly created sessions)
	launchingSessions  map[string]time.Time // sessionID -> creation time
	resumingSessions   map[string]time.Time // sessionID -> resume time (for restart/resume)
	mcpLoadingSessions map[string]time.Time // sessionID -> MCP reload time
	forkingSessions    map[string]time.Time // sessionID -> fork start time (fork in progress)
	animationFrame     int                  // Current frame for spinner animation

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

	// Navigation tracking (PERFORMANCE: suspend background updates during rapid navigation)
	lastNavigationTime time.Time // When user last navigated (up/down/j/k)
	isNavigating       bool      // True if user is rapidly navigating

	// Cached status counts (invalidated on instance changes)
	cachedStatusCounts struct {
		running, waiting, idle, errored int
		valid                           atomic.Bool // THREAD-SAFE: accessed from main and worker goroutines
		timestamp                       time.Time   // For time-based expiration
	}

	// Reusable string builder for View() to reduce allocations
	viewBuilder strings.Builder

	// Notification bar (tmux status-left for waiting sessions)
	notificationManager  *session.NotificationManager
	notificationsEnabled bool
	boundKeys            map[string]string // Track which key is bound (key -> "sessionID:tmuxName")
	boundKeysMu          sync.Mutex        // Protects boundKeys for background worker access
	lastBarText          string            // Cache to avoid updating all sessions every tick
	lastBarTextMu        sync.Mutex        // Protects lastBarText for background worker access

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
}

type sessionForkedMsg struct {
	instance *session.Instance
	sourceID string // ID of the source session that was forked (for cleanup)
	err      error
}

type refreshMsg struct{}

type statusUpdateMsg struct{} // Triggers immediate status update without reloading

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

type tickMsg time.Time
type quitMsg bool

// previewFetchedMsg is sent when async preview content is ready
type previewFetchedMsg struct {
	sessionID string
	content   string
	err       error
}

// previewDebounceMsg signals debounce period elapsed for preview fetch
// PERFORMANCE: Delays preview fetch during rapid navigation
type previewDebounceMsg struct {
	sessionID string
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

// statusUpdateRequest is sent to the background worker with current viewport info
type statusUpdateRequest struct {
	viewOffset    int      // Current scroll position
	visibleHeight int      // How many items fit on screen
	flatItemIDs   []string // IDs of sessions in current flatItems order (for visible detection)
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
		setupWizard:          NewSetupWizard(),
		settingsPanel:        NewSettingsPanel(),
		analyticsPanel:       NewAnalyticsPanel(),
		geminiModelDialog:    NewGeminiModelDialog(),
		sessionPickerDialog:  NewSessionPickerDialog(),
		worktreeFinishDialog: NewWorktreeFinishDialog(),
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
		launchingSessions:    make(map[string]time.Time),
		resumingSessions:     make(map[string]time.Time),
		mcpLoadingSessions:   make(map[string]time.Time),
		forkingSessions:      make(map[string]time.Time),
		lastLogActivity:      make(map[string]time.Time),
		worktreeDirtyCache:   make(map[string]bool),
		worktreeDirtyCacheTs: make(map[string]time.Time),
		statusTrigger:        make(chan statusUpdateRequest, 1), // Buffered to avoid blocking
		statusWorkerDone:     make(chan struct{}),
		logUpdateChan:        make(chan *session.Instance, 100), // Buffered to absorb bursts
		boundKeys:            make(map[string]string),
		undoStack:            make([]deletedSessionEntry, 0, 10),
		pendingTitleChanges:  make(map[string]string),
	}

	// Restore persisted UI state (preview mode, status filter, cursor position)
	h.loadUIState()

	// Initialize notification manager if enabled in config
	// All instances manage the notification bar (they share SQLite state, so produce identical output)
	notifSettings := session.GetNotificationsSettings()
	if notifSettings.Enabled {
		h.notificationsEnabled = true
		h.notificationManager = session.NewNotificationManager(notifSettings.MaxShown, notifSettings.ShowAll)

		// Initialize tmux status bar options for proper notification display
		// Fixes truncation (default status-left-length is only 10 chars)
		_ = tmux.InitializeStatusBarOptions()
	}

	// Initialize event-driven status detection
	// Output callback: invoked when PipeManager detects %output from a session
	outputCallback := func(sessionName string) {
		h.instancesMu.RLock()
		for _, inst := range h.instances {
			if inst.GetTmuxSession() != nil && inst.GetTmuxSession().Name == sessionName {
				h.logActivityMu.Lock()
				lastUpdate := h.lastLogActivity[inst.ID]
				if time.Since(lastUpdate) < 500*time.Millisecond {
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

// rebuildFlatItems rebuilds the flattened view from group tree
func (h *Home) rebuildFlatItems() {
	allItems := h.groupTree.Flatten()

	// Apply status filter if active
	if h.statusFilter != "" {
		// First pass: identify groups that have matching sessions
		groupsWithMatches := make(map[string]bool)
		for _, item := range allItems {
			if item.Type == session.ItemTypeSession && item.Session != nil {
				if item.Session.Status == h.statusFilter {
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
				if item.Session.Status == h.statusFilter {
					filtered = append(filtered, item)
				}
			}
		}
		h.flatItems = filtered
	} else {
		h.flatItems = allItems
	}

	// Pre-compute root group numbers for O(1) hotkey lookup (replaces O(n) loop in renderGroupItem)
	rootNum := 0
	for i := range h.flatItems {
		if h.flatItems[i].Type == session.ItemTypeGroup && h.flatItems[i].Level == 0 {
			rootNum++
			h.flatItems[i].RootGroupNum = rootNum
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
	if !h.notificationsEnabled || h.notificationManager == nil {
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

	cmds := []tea.Cmd{
		h.loadSessions,

		h.tick(),
		h.checkForUpdate(),
	}

	// Start listening for storage changes
	if h.storageWatcher != nil {
		cmds = append(cmds, listenForReloads(h.storageWatcher))
	}

	// Start listening for OS theme changes
	if h.themeWatcher != nil {
		cmds = append(cmds, listenForThemeChange(h.themeWatcher))
	}

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

	for id, t := range h.analyticsCacheTime {
		if now.Sub(t) > maxAge {
			delete(h.analyticsCache, id)
			delete(h.geminiAnalyticsCache, id)
			delete(h.analyticsCacheTime, id)
		}
	}

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
func (h *Home) cleanupExpiredAnimations(animMap map[string]time.Time, claudeTimeout, defaultTimeout time.Duration) []string {
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
		if inst.Tool == "claude" || inst.Tool == "gemini" {
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

	// Brief minimum (500ms) to prevent flicker during rapid status changes
	if timeSinceStart < 500*time.Millisecond {
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

	if animTool == "claude" || animTool == "gemini" {
		// Claude ready indicators
		agentReady := strings.Contains(previewContent, "ctrl+c to interrupt") ||
			strings.Contains(previewContent, "No, and tell Claude what to do differently") ||
			strings.Contains(previewContent, "\n> ") ||
			strings.Contains(previewContent, "> \n") ||
			strings.Contains(previewContent, "esc to interrupt") ||
			strings.Contains(previewContent, "⠋") || strings.Contains(previewContent, "⠙") ||
			strings.Contains(previewContent, "Thinking") ||
			strings.Contains(previewContent, "╭─") // Claude UI border

		// Gemini prompts
		if animTool == "gemini" {
			agentReady = agentReady ||
				strings.Contains(previewContent, "▸") ||
				strings.Contains(previewContent, "gemini>")
		}

		if agentReady {
			return false
		}
	} else {
		// Non-Claude/Gemini: ready if any substantial content (>50 chars)
		if len(strings.TrimSpace(previewContent)) > 50 {
			return false
		}
	}

	// Not ready yet - keep showing animation
	return true
}

// fetchPreview returns a command that asynchronously fetches preview content
// This keeps View() pure (no blocking I/O) as per Bubble Tea best practices
func (h *Home) fetchPreview(inst *session.Instance) tea.Cmd {
	if inst == nil {
		return nil
	}
	sessionID := inst.ID
	return func() tea.Msg {
		content, err := inst.PreviewFull()
		return previewFetchedMsg{
			sessionID: sessionID,
			content:   content,
			err:       err,
		}
	}
}

// fetchPreviewDebounced returns a command that triggers preview fetch after debounce delay
// PERFORMANCE: Prevents rapid subprocess spawning during keyboard navigation
// The 150ms delay allows navigation to settle before spawning tmux capture-pane
func (h *Home) fetchPreviewDebounced(sessionID string) tea.Cmd {
	const debounceDelay = 150 * time.Millisecond

	h.previewDebounceMu.Lock()
	h.pendingPreviewID = sessionID
	h.previewDebounceMu.Unlock()

	return func() tea.Msg {
		time.Sleep(debounceDelay)
		return previewDebounceMsg{sessionID: sessionID}
	}
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

	// Check cache
	if cached, ok := h.analyticsCache[inst.ID]; ok {
		if time.Since(h.analyticsCacheTime[inst.ID]) < analyticsCacheTTL {
			return cached
		}
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
	if fetchTool == "claude" {
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
				uiLog.Warn("analytics_parse_failed", slog.String("session_id", sessionID), slog.String("claude_session_id", claudeSessionID), slog.String("error", err.Error()))
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
	} else if fetchTool == "gemini" {
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
	return nil
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
	if group, exists := h.groupTree.Groups[groupPath]; exists {
		return group.DefaultPath
	}
	return ""
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
			if inst.Tool == "claude" || inst.Tool == "codex" {
				if hs := h.hookWatcher.GetHookStatus(inst.ID); hs != nil {
					inst.UpdateHookStatus(hs)
				}
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
				notifLog.Debug("status_changed", slog.String("title", inst.Title), slog.String("old", string(oldStatus)), slog.String("new", string(newStatus)))
			}
			return nil
		})
	}
	_ = g.Wait() // Errors are logged within each goroutine

	statusDur := time.Since(statusStart)
	if skipped > 0 {
		perfLog.Debug("idle_sessions_skipped", slog.Int("skipped", skipped), slog.Int("checked", len(instances)-skipped))
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
	}

	// SQLite sync: heartbeat, status writes, ack reads (enables multi-instance coordination)
	if db := statedb.GetGlobal(); db != nil {
		// Heartbeat: mark this process as alive
		_ = db.Heartbeat()

		// Clean dead instances every ~20s (not every tick)
		if time.Since(h.lastDeadInstanceCleanup) > 20*time.Second {
			_ = db.CleanDeadInstances(30 * time.Second)
			h.lastDeadInstanceCleanup = time.Now()
		}

		// Write current status for each instance so other TUI instances stay in sync
		for _, inst := range instances {
			_ = db.WriteStatus(inst.ID, string(inst.GetStatusThreadSafe()), inst.Tool)
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
}

// syncNotificationsBackground updates the tmux notification bar directly
// Called from background worker - does NOT depend on Bubble Tea
func (h *Home) syncNotificationsBackground() {
	defer func() {
		if r := recover(); r != nil {
			notifLog.Error("sync_notifications_panic", slog.Any("panic", r))
		}
	}()

	if !h.notificationsEnabled || h.notificationManager == nil {
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
				notifLog.Debug("session_acknowledged", slog.String("title", inst.Title), slog.String("status", string(inst.Status)))
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

	notifLog.Debug("sync_state", slog.String("current_session_id", currentSessionID), slog.Int("instances", len(instances)))

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
	}
}

// Update handles messages
func (h *Home) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
		h.geminiModelDialog.SetSize(msg.Width, msg.Height)
		return h, nil

	case loadSessionsMsg:
		// Clear loading indicators and store file mtime for external change detection
		h.reloadMu.Lock()
		h.isReloading = false
		if !msg.loadMtime.IsZero() {
			h.lastLoadMtime = msg.loadMtime
		}
		h.reloadMu.Unlock()
		h.initialLoading = false // First load complete, hide splash

		// Show hooks installation prompt (after splash screen is gone)
		if h.pendingHooksPrompt && !h.setupWizard.IsVisible() {
			h.confirmDialog.ShowInstallHooks()
			h.confirmDialog.SetSize(h.width, h.height)
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
				allCmds := append(detectionCmds, h.fetchPreview(selected))
				return h, tea.Batch(allCmds...)
			}
			// No selection, but still run detection commands if any
			if len(detectionCmds) > 0 {
				return h, tea.Batch(detectionCmds...)
			}
		}
		return h, nil

	case sessionCreatedMsg:
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
			return h, h.fetchPreview(msg.instance)
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
			return h, h.fetchPreview(msg.instance)
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
		}

		// Invalidate status counts cache
		h.cachedStatusCounts.valid.Store(false)
		// Invalidate preview cache for deleted session
		h.invalidatePreviewCache(msg.deletedID)
		// Clean up analytics caches for deleted session
		delete(h.analyticsCache, msg.deletedID)
		delete(h.geminiAnalyticsCache, msg.deletedID)
		delete(h.analyticsCacheTime, msg.deletedID)
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
			h.setError(fmt.Errorf("deleted '%s'. Ctrl+Z to undo", deletedInstance.Title))
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
		h.setError(fmt.Errorf("restored '%s'", msg.instance.Title))
		return h, h.fetchPreview(msg.instance)

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
			h.setError(fmt.Errorf("failed to restart session: %w", msg.err))
		} else {
			// Find the instance and refresh its MCP state (O(1) lookup)
			if inst := h.getInstanceByID(msg.sessionID); inst != nil {
				// Refresh the loaded MCPs to match the new config
				inst.CaptureLoadedMCPs()
			}
			h.invalidatePreviewCache(msg.sessionID)
			// Save the updated session state (new tmux session name)
			h.saveInstances()
		}
		// NOTE: Do NOT delete from mcpLoadingSessions here!
		// The animation should continue until Claude is ready (detected via preview content)
		// or until the timeout expires (handled by cleanup logic in tickMsg handler)
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
		h.updateInfo = msg.info
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
		if len(parts) > 0 {
			h.maintenanceMsg = "Maintenance: " + strings.Join(parts, ", ") + fmt.Sprintf(" (%s)", r.Duration.Round(time.Millisecond))
			h.maintenanceMsgTime = time.Now()
			// Auto-clear after 30 seconds
			return h, tea.Tick(30*time.Second, func(_ time.Time) tea.Msg {
				return clearMaintenanceMsg{}
			})
		}
		return h, nil

	case clearMaintenanceMsg:
		h.maintenanceMsg = ""
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

		// Trigger status update on attach return to reflect current state
		// Acknowledgment was already done on attach (if session was waiting),
		// so this just refreshes the display with current busy indicator state.
		h.triggerStatusUpdate()

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
			return h, nil
		}

		// PERFORMANCE FIX: Skip save on attach return for 10 seconds
		// Saving can also be blocking (JSON serialization + file write).
		// Combine with periodic save instead of saving on every attach/detach.
		// We'll let the next tickMsg handle background save if needed.

		return h, nil

	case previewDebounceMsg:
		// PERFORMANCE: Debounce period elapsed - check if this fetch is still relevant
		// If user continued navigating, pendingPreviewID will have changed
		h.previewDebounceMu.Lock()
		isPending := h.pendingPreviewID == msg.sessionID
		if isPending {
			h.pendingPreviewID = "" // Clear pending state
		}
		h.previewDebounceMu.Unlock()

		if !isPending {
			return h, nil // Superseded by newer navigation
		}

		// Find session and trigger actual fetch
		h.instancesMu.RLock()
		inst := h.instanceByID[msg.sessionID]
		h.instancesMu.RUnlock()

		if inst != nil {
			var cmds []tea.Cmd

			// Preview fetch
			h.previewCacheMu.Lock()
			needsPreviewFetch := h.previewFetchingID != inst.ID
			if needsPreviewFetch {
				h.previewFetchingID = inst.ID
			}
			h.previewCacheMu.Unlock()
			if needsPreviewFetch {
				cmds = append(cmds, h.fetchPreview(inst))
			}

			// Analytics fetch (for Claude/Gemini sessions with analytics enabled)
			// Use TTL cache - only fetch if cache miss/expired and not already fetching
			tickTool := inst.GetToolThreadSafe()
			if (tickTool == "claude" || tickTool == "gemini") && h.analyticsFetchingID != inst.ID {
				if tickTool == "claude" {
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
				} else if tickTool == "gemini" {
					// Check Gemini cache
					var cached *session.GeminiSessionAnalytics
					if c, ok := h.geminiAnalyticsCache[inst.ID]; ok {
						if time.Since(h.analyticsCacheTime[inst.ID]) < analyticsCacheTTL {
							cached = c
						}
					}

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
		// Async preview content received - update cache with timestamp
		// Protect both previewFetchingID and previewCache with the same mutex
		h.previewCacheMu.Lock()
		h.previewFetchingID = ""
		if msg.err == nil {
			h.previewCache[msg.sessionID] = msg.content
			h.previewCacheTime[msg.sessionID] = time.Now()
		}
		h.previewCacheMu.Unlock()
		return h, nil

	case analyticsFetchedMsg:
		// Async analytics parsing complete - update TTL cache
		h.analyticsFetchingID = ""
		if msg.err == nil && msg.sessionID != "" {
			// Update cache timestamp
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
		delete(h.analyticsCache, msg.sessionID)
		delete(h.geminiAnalyticsCache, msg.sessionID)
		delete(h.analyticsCacheTime, msg.sessionID)
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

	case tickMsg:
		// Auto-dismiss errors after 5 seconds
		if h.err != nil && !h.errTime.IsZero() && time.Since(h.errTime) > 5*time.Second {
			h.clearError()
		}

		// PERFORMANCE: Detect when navigation has settled (300ms since last up/down)
		// This allows background updates to resume after rapid navigation stops
		const navigationSettleTime = 300 * time.Millisecond
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

		// Periodic UI state save (every 5 ticks = ~10 seconds)
		h.uiStateSaveTicks++
		if h.uiStateSaveTicks >= 5 {
			h.uiStateSaveTicks = 0
			h.saveUIState()
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

		// Fetch preview for currently selected session (if stale/missing and not fetching)
		// Cache expires after 2 seconds to show live terminal updates without excessive fetching
		const previewCacheTTL = 2 * time.Second
		var previewCmd tea.Cmd
		h.instancesMu.RLock()
		selected := h.getSelectedSession()
		h.instancesMu.RUnlock()
		if selected != nil {
			h.previewCacheMu.Lock()
			cachedTime, hasCached := h.previewCacheTime[selected.ID]
			cacheExpired := !hasCached || time.Since(cachedTime) > previewCacheTTL
			// Only fetch if cache is stale/missing AND not currently fetching this session
			if cacheExpired && h.previewFetchingID != selected.ID {
				h.previewFetchingID = selected.ID
				previewCmd = h.fetchPreview(selected)
			}
			h.previewCacheMu.Unlock()
		}
		return h, tea.Batch(h.tick(), previewCmd)

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

		// Handle setup wizard first (modal, blocks everything)
		if h.setupWizard.IsVisible() {
			var cmd tea.Cmd
			h.setupWizard, cmd = h.setupWizard.Update(msg)
			// Check if user pressed Enter on final step
			if msg.String() == "enter" && h.setupWizard.IsComplete() {
				// Save config and close wizard
				config := h.setupWizard.GetConfig()
				if err := session.SaveUserConfig(config); err != nil {
					h.err = err
					h.errTime = time.Now()
				}
				h.setupWizard.Hide()
				// Reload config cache
				_, _ = session.ReloadUserConfig()
				// Apply default tool to new dialog
				if defaultTool := session.GetDefaultTool(); defaultTool != "" {
					h.newDialog.SetDefaultTool(defaultTool)
				}
			}
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

				// Apply theme changes live
				h.stopThemeWatcher()
				resolvedTheme := session.ResolveTheme()
				InitTheme(resolvedTheme)
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

		// Create instance
		inst := session.NewInstanceWithGroupAndTool(title, projectPath, h.getCurrentGroupPath(), "claude")
		inst.ClaudeSessionID = result.SessionID

		// Build resume command with config dir and permission flags
		userConfig, _ := session.LoadUserConfig()
		opts := session.NewClaudeOptions(userConfig)

		// Build command - only set CLAUDE_CONFIG_DIR if explicitly configured
		// If not explicit, let the tmux shell's environment handle it
		// This is critical for WSL and other environments where users have
		// CLAUDE_CONFIG_DIR set in their .bashrc/.zshrc
		var cmdBuilder strings.Builder
		if session.IsClaudeConfigDirExplicit() {
			configDir := session.GetClaudeConfigDir()
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

// handleNewDialogKey handles keys when new dialog is visible
func (h *Home) handleNewDialogKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		// Validate before creating session
		if validationErr := h.newDialog.Validate(); validationErr != "" {
			h.newDialog.SetError(validationErr)
			return h, nil
		}

		// Get values including worktree settings
		name, path, command, branchName, worktreeEnabled := h.newDialog.GetValuesWithWorktree()
		groupPath := h.newDialog.GetSelectedGroup()
		claudeOpts := h.newDialog.GetClaudeOptions() // Get Claude options if applicable

		// Handle worktree creation if enabled
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

			// Ensure parent directory exists (needed for subdirectory mode)
			if err := os.MkdirAll(filepath.Dir(worktreePath), 0755); err != nil {
				h.newDialog.SetError(fmt.Sprintf("Failed to create parent directory: %v", err))
				return h, nil
			}

			// Create worktree
			if err := git.CreateWorktree(repoRoot, worktreePath, branchName); err != nil {
				h.newDialog.SetError(fmt.Sprintf("Failed to create worktree: %v", err))
				return h, nil
			}

			// Store repo root for later use
			worktreeRepoRoot = repoRoot
			// Update path to worktree for session creation
			path = worktreePath
		}

		// Build generic toolOptionsJSON from tool-specific options
		var toolOptionsJSON json.RawMessage
		if command == "claude" && claudeOpts != nil {
			toolOptionsJSON, _ = session.MarshalToolOptions(claudeOpts)
		} else if command == "codex" {
			yolo := h.newDialog.GetCodexYoloMode()
			codexOpts := &session.CodexOptions{YoloMode: &yolo}
			toolOptionsJSON, _ = session.MarshalToolOptions(codexOpts)
		}

		if _, err := os.Stat(path); os.IsNotExist(err) {
			h.newDialog.Hide()
			h.confirmDialog.ShowCreateDirectory(path, name, command, groupPath, toolOptionsJSON)
			return h, nil
		}

		h.newDialog.Hide()
		h.clearError()

		geminiYoloMode := h.newDialog.IsGeminiYoloMode()

		return h, h.createSessionInGroupWithWorktreeAndOptions(name, path, command, groupPath, worktreePath, worktreeRepoRoot, branchName, geminiYoloMode, toolOptionsJSON)

	case "esc":
		h.newDialog.Hide()
		h.clearError() // Clear any validation error
		return h, nil
	}

	var cmd tea.Cmd
	h.newDialog, cmd = h.newDialog.Update(msg)
	return h, cmd
}

// handleMainKey handles keys in main view
func (h *Home) handleMainKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
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
			h.syncViewport()
			// Track navigation for adaptive background updates
			h.lastNavigationTime = time.Now()
			h.isNavigating = true
			// PERFORMANCE: Debounced preview fetch - waits 150ms for navigation to settle
			// This prevents spawning tmux subprocess on every keystroke
			if selected := h.getSelectedSession(); selected != nil {
				return h, h.fetchPreviewDebounced(selected.ID)
			}
		}
		return h, nil

	case "down", "j":
		if h.cursor < len(h.flatItems)-1 {
			h.cursor++
			h.syncViewport()
			// Track navigation for adaptive background updates
			h.lastNavigationTime = time.Now()
			h.isNavigating = true
			// PERFORMANCE: Debounced preview fetch - waits 150ms for navigation to settle
			// This prevents spawning tmux subprocess on every keystroke
			if selected := h.getSelectedSession(); selected != nil {
				return h, h.fetchPreviewDebounced(selected.ID)
			}
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
		h.syncViewport()
		h.lastNavigationTime = time.Now()
		h.isNavigating = true
		if selected := h.getSelectedSession(); selected != nil {
			return h, h.fetchPreviewDebounced(selected.ID)
		}
		return h, nil

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
		h.syncViewport()
		h.lastNavigationTime = time.Now()
		h.isNavigating = true
		if selected := h.getSelectedSession(); selected != nil {
			return h, h.fetchPreviewDebounced(selected.ID)
		}
		return h, nil

	case "ctrl+b": // Full page up (backward)
		pageSize := h.getVisibleHeight()
		if pageSize < 1 {
			pageSize = 1
		}
		h.cursor -= pageSize
		if h.cursor < 0 {
			h.cursor = 0
		}
		h.syncViewport()
		h.lastNavigationTime = time.Now()
		h.isNavigating = true
		if selected := h.getSelectedSession(); selected != nil {
			return h, h.fetchPreviewDebounced(selected.ID)
		}
		return h, nil

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
		h.syncViewport()
		h.lastNavigationTime = time.Now()
		h.isNavigating = true
		if selected := h.getSelectedSession(); selected != nil {
			return h, h.fetchPreviewDebounced(selected.ID)
		}
		return h, nil

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
				// Block attachment during animations (must match renderPreviewPane display logic)
				if h.hasActiveAnimation(item.Session.ID) {
					h.setError(fmt.Errorf("session is starting, please wait..."))
					return h, nil
				}
				if item.Session.Exists() {
					h.isAttaching.Store(true) // Prevent View() output during transition (atomic)
					return h, h.attachSession(item.Session)
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
			}
		}
		return h, nil

	case "tab", "l", "right":
		// Expand/collapse group or expand if on session
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
			}
		}
		return h, nil

	case "h", "left":
		// Collapse group
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
			} else if item.Type == session.ItemTypeSession {
				// Move cursor to parent group
				h.groupTree.CollapseGroup(item.Path)
				h.rebuildFlatItems()
				// Find the group in flatItems
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
			if item.Type == session.ItemTypeGroup {
				h.groupTree.MoveGroupUp(item.Path)
			} else if item.Type == session.ItemTypeSession {
				h.groupTree.MoveSessionUp(item.Session)
			}
			h.rebuildFlatItems()
			if h.cursor > 0 {
				h.cursor--
			}
			h.saveInstances()
		}
		return h, nil

	case "shift+down", "J":
		// Move item down
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeGroup {
				h.groupTree.MoveGroupDown(item.Path)
			} else if item.Type == session.ItemTypeSession {
				h.groupTree.MoveSessionDown(item.Session)
			}
			h.rebuildFlatItems()
			if h.cursor < len(h.flatItems)-1 {
				h.cursor++
			}
			h.saveInstances()
		}
		return h, nil

	case "m":
		// Move session to different group
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeSession {
				h.groupDialog.ShowMove(h.groupTree.GetGroupNames())
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

	case "M", "shift+m":
		// MCP Manager - for Claude and Gemini sessions
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeSession && item.Session != nil &&
				(item.Session.Tool == "claude" || item.Session.Tool == "gemini") {
				h.mcpDialog.SetSize(h.width, h.height)
				if err := h.mcpDialog.Show(item.Session.ProjectPath, item.Session.ID, item.Session.Tool); err != nil {
					h.setError(err)
				}
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
				h.lastNavigationTime = time.Now()
				h.isNavigating = true
				if selected := h.getSelectedSession(); selected != nil {
					return h, h.fetchPreviewDebounced(selected.ID)
				}
			}
			return h, nil
		}
		// Record time for potential gg detection
		h.lastGTime = time.Now()

		// Create new group with context-aware Tab toggle (Issue #111):
		// - Group header: defaults to subgroup, Tab toggles to root
		// - Grouped session: defaults to root, Tab toggles to subgroup
		// - Ungrouped item: root only, no toggle
		if h.cursor < len(h.flatItems) {
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
			existing, ok := pathMap[inst.ProjectPath]
			if !ok {
				// First time seeing this path
				accessTime := inst.LastAccessedAt
				if accessTime.IsZero() {
					accessTime = inst.CreatedAt // Fall back to creation time
				}
				pathMap[inst.ProjectPath] = &pathInfo{
					path:           inst.ProjectPath,
					lastAccessedAt: accessTime,
				}
			} else {
				// Update if this instance was accessed more recently
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

		// Apply user's preferred default tool from config
		h.newDialog.SetDefaultTool(session.GetDefaultTool())

		// Auto-select parent group from current cursor position
		groupPath := session.DefaultGroupPath
		groupName := session.DefaultGroupName
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeGroup {
				groupPath = item.Group.Path
				groupName = item.Group.Name
			} else if item.Type == session.ItemTypeSession {
				// Use the session's group
				groupPath = item.Path
				if group, exists := h.groupTree.Groups[groupPath]; exists {
					groupName = group.Name
				}
			}
		}
		defaultPath := h.getDefaultPathForGroup(groupPath)
		h.newDialog.ShowInGroup(groupPath, groupName, defaultPath)
		return h, nil

	case "N":
		// Quick create: auto-generated name, smart defaults from group context
		return h, h.quickCreateSession()

	case "d":
		// Show confirmation dialog before deletion (prevents accidental deletion)
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeSession && item.Session != nil {
				h.confirmDialog.ShowDeleteSession(item.Session.ID, item.Session.Title)
			} else if item.Type == session.ItemTypeGroup && item.Path != session.DefaultGroupPath {
				h.confirmDialog.ShowDeleteGroup(item.Path, item.Group.Name)
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

	case "v":
		// Toggle preview mode (cycle: both → output-only → analytics-only → both)
		h.previewMode = (h.previewMode + 1) % 3
		return h, nil

	case "y":
		// Toggle Gemini YOLO mode (requires restart)
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeSession && item.Session != nil && item.Session.Tool == "gemini" {
				inst := item.Session
				// Determine current YOLO state
				currentYolo := false
				if inst.GeminiYoloMode != nil {
					currentYolo = *inst.GeminiYoloMode
				} else {
					// Fall back to global config
					userConfig, _ := session.LoadUserConfig()
					if userConfig != nil {
						currentYolo = userConfig.Gemini.YoloMode
					}
				}
				// Toggle: set per-session override to opposite of current
				newYolo := !currentYolo
				inst.GeminiYoloMode = &newYolo
				h.saveInstances()
				// If session is running, it needs restart to apply
				if inst.GetStatusThreadSafe() == session.StatusRunning || inst.GetStatusThreadSafe() == session.StatusWaiting {
					h.resumingSessions[inst.ID] = time.Now()
					return h, h.restartSession(inst)
				}
			}
		}
		return h, nil

	case "R":
		// Restart session (Shift+R - recreate tmux session with resume)
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
			return sessionRestoredMsg{instance: inst, err: err}
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

	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		// Quick jump to Nth root group (1-indexed)
		targetNum := int(msg.String()[0] - '0') // Convert "1" -> 1, "2" -> 2, etc.
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
		// Filter to error sessions only
		if h.statusFilter == session.StatusError {
			h.statusFilter = "" // Toggle off
		} else {
			h.statusFilter = session.StatusError
		}
		h.rebuildFlatItems()
		return h, nil
	}

	return h, nil
}

// handleConfirmDialogKey handles keys when confirmation dialog is visible
func (h *Home) handleConfirmDialogKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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
		case "esc":
			h.confirmDialog.Hide()
			h.isQuitting = false
			return h, nil
		}
		return h, nil

	case ConfirmCreateDirectory:
		switch msg.String() {
		case "y", "Y":
			name, path, command, groupPath, pendingToolOpts := h.confirmDialog.GetPendingSession()
			h.confirmDialog.Hide()
			if err := os.MkdirAll(path, 0755); err != nil {
				h.setError(fmt.Errorf("failed to create directory: %w", err))
				return h, nil
			}
			return h, h.createSessionInGroupWithWorktreeAndOptions(name, path, command, groupPath, "", "", "", false, pendingToolOpts)
		case "n", "N", "esc":
			h.confirmDialog.Hide()
			return h, nil
		}
		return h, nil

	case ConfirmInstallHooks:
		switch msg.String() {
		case "y", "Y":
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
			return h, nil
		case "n", "N", "esc":
			h.confirmDialog.Hide()
			h.pendingHooksPrompt = false
			// Remember user declined
			if db := statedb.GetGlobal(); db != nil {
				_ = db.SetMeta("hooks_prompted", "declined")
			}
			return h, nil
		}
		return h, nil

	default:
		// Handle delete confirmations (session/group)
		switch msg.String() {
		case "y", "Y":
			// User confirmed - perform the deletion
			switch h.confirmDialog.GetConfirmType() {
			case ConfirmDeleteSession:
				sessionID := h.confirmDialog.GetTargetID()
				if inst := h.getInstanceByID(sessionID); inst != nil {
					h.confirmDialog.Hide()
					return h, h.deleteSession(inst)
				}
			case ConfirmDeleteGroup:
				groupPath := h.confirmDialog.GetTargetID()
				h.groupTree.DeleteGroup(groupPath)
				h.instancesMu.Lock()
				h.instances = h.groupTree.GetAllInstances()
				h.instancesMu.Unlock()
				h.rebuildFlatItems()
				h.saveInstances()
			}
			h.confirmDialog.Hide()
			return h, nil

		case "n", "N", "esc":
			// User cancelled
			h.confirmDialog.Hide()
			return h, nil
		}
	}

	return h, nil
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
				mcpUILog.Debug("dialog_session_found", slog.String("session_id", targetInst.ID), slog.String("title", targetInst.Title))
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
			groupName := h.groupDialog.GetSelectedGroup()
			if groupName != "" && h.cursor < len(h.flatItems) {
				item := h.flatItems[h.cursor]
				if item.Type == session.ItemTypeSession {
					// Find the group path from name
					for _, g := range h.groupTree.GroupList {
						if g.Name == groupName {
							h.groupTree.MoveSessionToGroup(item.Session, g.Path)
							h.instancesMu.Lock()
							h.instances = h.groupTree.GetAllInstances()
							h.instancesMu.Unlock()
							h.rebuildFlatItems()
							h.saveInstances()
							break
						}
					}
				}
			}
		case GroupDialogRenameSession:
			newName := h.groupDialog.GetValue()
			if newName != "" {
				sessionID := h.groupDialog.GetSessionID()
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

				// Handle worktree creation if enabled
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

					if err := os.MkdirAll(filepath.Dir(worktreePath), 0755); err != nil {
						h.forkDialog.SetError(fmt.Sprintf("Failed to create directory: %v", err))
						return h, nil
					}

					if err := git.CreateWorktree(repoRoot, worktreePath, branchName); err != nil {
						h.forkDialog.SetError(fmt.Sprintf("Worktree creation failed: %v", err))
						return h, nil
					}

					opts.WorkDir = worktreePath
					opts.WorktreePath = worktreePath
					opts.WorktreeRepoRoot = repoRoot
					opts.WorktreeBranch = branchName
				}

				h.forkDialog.Hide()
				return h, h.forkSessionCmdWithOptions(source, title, groupPath, opts)
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
			uiLog.Warn("save_expected_path_failed", slog.String("profile", h.profile), slog.String("error", err.Error()))
			return
		}
		if h.storage.Path() != expectedPath {
			uiLog.Error("save_path_mismatch", slog.String("profile", h.profile), slog.String("expected", expectedPath), slog.String("got", h.storage.Path()))
			h.setError(fmt.Errorf("storage path mismatch (profile=%s): expected %s, got %s", h.profile, expectedPath, h.storage.Path()))
			return
		}

		// Take snapshot under lock for defensive programming
		// This ensures consistency even if architecture changes in the future
		h.instancesMu.RLock()
		instancesCopy := make([]*session.Instance, len(h.instances))
		copy(instancesCopy, h.instances)
		instanceCount := len(h.instances)
		h.instancesMu.RUnlock()

		uiLog.Debug("save_instances", slog.Int("count", instanceCount), slog.String("profile", h.profile), slog.String("path", h.storage.Path()), slog.Bool("force", force))

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

// getUsedClaudeSessionIDs returns a map of all Claude session IDs currently in use
// This is used for deduplication when detecting new session IDs
func (h *Home) getUsedClaudeSessionIDs() map[string]bool {
	h.instancesMu.RLock()
	defer h.instancesMu.RUnlock()

	usedIDs := make(map[string]bool)
	for _, inst := range h.instances {
		if inst.ClaudeSessionID != "" {
			usedIDs[inst.ClaudeSessionID] = true
		}
	}
	return usedIDs
}

// createSessionInGroupWithWorktreeAndOptions creates a new session with full options including YOLO mode and tool options
func (h *Home) createSessionInGroupWithWorktreeAndOptions(name, path, command, groupPath, worktreePath, worktreeRepoRoot, worktreeBranch string, geminiYoloMode bool, toolOptionsJSON json.RawMessage) tea.Cmd {
	return func() tea.Msg {
		// Check tmux availability before creating session
		if err := tmux.IsTmuxAvailable(); err != nil {
			return sessionCreatedMsg{err: fmt.Errorf("cannot create session: %w", err)}
		}

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
		default:
			// Check custom tools: tool identity stays as the custom name (e.g. "glm")
			// so config lookup works, but command resolves to the actual binary (e.g. "claude")
			if toolDef := session.GetToolDef(command); toolDef != nil {
				tool = command
				command = toolDef.Command
			}
		}

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

		// Set Gemini YOLO mode if enabled (per-session override)
		if geminiYoloMode && tool == "gemini" {
			inst.GeminiYoloMode = &geminiYoloMode
		}

		// Apply generic tool options (claude, codex, etc.)
		if len(toolOptionsJSON) > 0 {
			inst.ToolOptionsJSON = toolOptionsJSON
		}

		if err := inst.Start(); err != nil {
			return sessionCreatedMsg{err: err}
		}
		return sessionCreatedMsg{instance: inst}
	}
}

// quickForkSession performs a quick fork with default title suffix " (fork)"
func (h *Home) quickForkSession(source *session.Instance) tea.Cmd {
	if source == nil {
		return nil
	}
	// Use source title with " (fork)" suffix
	title := source.Title + " (fork)"
	groupPath := source.GroupPath
	return h.forkSessionCmd(source, title, groupPath)
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
		groupPath = session.DefaultGroupPath
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
			toolOptionsJSON = sourceSession.ToolOptionsJSON
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
				toolOptionsJSON = mostRecent.ToolOptionsJSON
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
		geminiYoloMode, toolOptionsJSON,
	)
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
	h.forkDialog.Show(source.Title, source.ProjectPath, source.GroupPath)
	return nil
}

// forkSessionCmd creates a forked session with the given title and group
// Shows immediate UI feedback by tracking the source session in forkingSessions
func (h *Home) forkSessionCmd(source *session.Instance, title, groupPath string) tea.Cmd {
	return h.forkSessionCmdWithOptions(source, title, groupPath, nil)
}

// forkSessionCmdWithOptions creates a forked session with the given title, group, and Claude options
// Shows immediate UI feedback by tracking the source session in forkingSessions
func (h *Home) forkSessionCmdWithOptions(source *session.Instance, title, groupPath string, opts *session.ClaudeOptions) tea.Cmd {
	if source == nil {
		return nil
	}

	// Track source session as "forking" for immediate UI feedback
	h.forkingSessions[source.ID] = time.Now()

	// Capture current used session IDs before starting the async fork
	// This ensures we don't detect an already-used session ID
	usedIDs := h.getUsedClaudeSessionIDs()
	sourceID := source.ID // Capture for closure

	return func() tea.Msg {
		// Check tmux availability before forking
		if err := tmux.IsTmuxAvailable(); err != nil {
			return sessionForkedMsg{err: fmt.Errorf("cannot fork session: %w", err), sourceID: sourceID}
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

		if err := inst.Start(); err != nil {
			return sessionForkedMsg{err: err, sourceID: sourceID}
		}

		switch inst.Tool {
		case "claude":
			_ = inst.WaitForClaudeSessionWithExclude(5*time.Second, usedIDs)
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

// sessionRestoredMsg signals that an undo-delete restore completed
type sessionRestoredMsg struct {
	instance *session.Instance
	err      error
}

// deleteSession deletes a session
func (h *Home) deleteSession(inst *session.Instance) tea.Cmd {
	id := inst.ID
	isWorktree := inst.IsWorktree()
	worktreePath := inst.WorktreePath
	worktreeRepoRoot := inst.WorktreeRepoRoot
	return func() tea.Msg {
		killErr := inst.Kill()
		if isWorktree {
			_ = git.RemoveWorktree(worktreeRepoRoot, worktreePath, false)
			_ = git.PruneWorktrees(worktreeRepoRoot)
		}
		return sessionDeletedMsg{deletedID: id, killErr: killErr}
	}
}

// sessionRestartedMsg signals that a session was restarted
type sessionRestartedMsg struct {
	sessionID string
	err       error
}

// mcpRestartedMsg signals that an MCP-triggered restart completed and should auto-attach
type mcpRestartedMsg struct {
	session *session.Instance
	err     error
}

// restartSession restarts a dead/errored session by creating a new tmux session
func (h *Home) restartSession(inst *session.Instance) tea.Cmd {
	id := inst.ID
	mcpUILog.Debug("restart_session_called", slog.String("id", inst.ID), slog.String("title", inst.Title), slog.String("tool", inst.Tool))
	return func() tea.Msg {
		mcpUILog.Debug("restart_session_executing", slog.String("id", id))
		err := inst.Restart()
		mcpUILog.Debug("restart_session_result", slog.String("id", id), slog.Any("error", err))
		return sessionRestartedMsg{sessionID: id, err: err}
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

	// Mark session as accessed (for recency-sorted path suggestions)
	inst.MarkAccessed()

	// Skip saving during reload to avoid overwriting external changes
	// THREAD-SAFE: Read isReloading under mutex
	h.reloadMu.Lock()
	reloading := h.isReloading
	h.reloadMu.Unlock()
	if !reloading && h.storage != nil {
		// Take snapshot under lock for defensive programming
		h.instancesMu.RLock()
		instancesCopy := make([]*session.Instance, len(h.instances))
		copy(instancesCopy, h.instances)
		instanceCount := len(h.instances)
		h.instancesMu.RUnlock()

		// DEFENSIVE: Never save empty instances if storage has data
		if instanceCount == 0 {
			if info, err := os.Stat(h.storage.Path()); err == nil && info.Size() > 100 {
				uiLog.Warn("save_attach_refusing_empty_overwrite", slog.Int64("file_bytes", info.Size()))
				goto skipSave
			}
		}

		groupTreeCopy := h.groupTree.ShallowCopyForSave()

		// CRITICAL FIX: NotifySave MUST be called immediately before SaveWithGroups
		// Previously it was called 18 lines earlier, creating a race window
		if h.storageWatcher != nil {
			h.storageWatcher.NotifySave()
		}
		_ = h.storage.SaveWithGroups(instancesCopy, groupTreeCopy)
	}
skipSave:

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
	return tea.Exec(attachCmd{session: tmuxSess}, func(err error) tea.Msg {
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

		return statusUpdateMsg{}
	})
}

// attachCmd implements tea.ExecCommand for custom PTY attach
type attachCmd struct {
	session *tmux.Session
}

func (a attachCmd) Run() error {
	// NOTE: Screen clearing is ONLY done in the tea.Exec callback (after Attach returns)
	// Removing clear screen here prevents double-clearing which corrupts terminal state

	ctx := context.Background()
	return a.session.Attach(ctx)
}

func (a attachCmd) SetStdin(r io.Reader)  {}
func (a attachCmd) SetStdout(w io.Writer) {}
func (a attachCmd) SetStderr(w io.Writer) {}

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
	h.instancesMu.RLock()
	for _, inst := range h.instances {
		switch inst.GetStatusThreadSafe() {
		case session.StatusRunning:
			running++
		case session.StatusWaiting:
			waiting++
		case session.StatusIdle:
			idle++
		case session.StatusError:
			errored++
		}
	}
	h.instancesMu.RUnlock()

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

	// "All" pill
	allLabel := "All"
	if h.statusFilter == "" {
		pills = append(pills, activePillStyle.Render(allLabel))
	} else {
		pills = append(pills, inactivePillStyle.Render(allLabel))
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

	// Idle pill (gray when active)
	idleLabel := fmt.Sprintf("○ %d", idle)
	if h.statusFilter == session.StatusIdle {
		pills = append(pills, lipgloss.NewStyle().
			Foreground(ColorBg).
			Background(ColorTextDim).
			Bold(true).
			Padding(0, 1).Render(idleLabel))
	} else if idle > 0 {
		pills = append(pills, lipgloss.NewStyle().
			Foreground(ColorText).
			Background(ColorSurface).
			Padding(0, 1).Render(idleLabel))
	} else {
		pills = append(pills, dimPillStyle.Render(idleLabel))
	}

	// Error pill (red when active)
	if errored > 0 || h.statusFilter == session.StatusError {
		errorLabel := fmt.Sprintf("✕ %d", errored)
		if h.statusFilter == session.StatusError {
			pills = append(pills, lipgloss.NewStyle().
				Foreground(ColorBg).
				Background(ColorRed).
				Bold(true).
				Padding(0, 1).Render(errorLabel))
		} else if errored > 0 {
			pills = append(pills, lipgloss.NewStyle().
				Foreground(ColorRed).
				Background(ColorSurface).
				Padding(0, 1).Render(errorLabel))
		}
	}

	// Hint for keyboard shortcuts (shift+number to filter, 0 to clear)
	hintStyle := lipgloss.NewStyle().Foreground(ColorComment).Faint(true)
	hint := hintStyle.Render("  !@#$ filter • 0 all")

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
	if h.geminiModelDialog.IsVisible() {
		return h.geminiModelDialog.View()
	}
	if h.sessionPickerDialog.IsVisible() {
		return h.sessionPickerDialog.View()
	}
	if h.worktreeFinishDialog.IsVisible() {
		return h.worktreeFinishDialog.View()
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
	title := titleStyle.Render(titleText)

	// Status-based stats (more useful than group/session counts)
	// Format: ● 2 running • ◐ 1 waiting • ○ 3 idle (• ✕ 1 error)
	var statsParts []string
	statsSep := lipgloss.NewStyle().Foreground(ColorBorder).Render(" • ")

	if running > 0 {
		statsParts = append(statsParts, lipgloss.NewStyle().Foreground(ColorGreen).Render(fmt.Sprintf("● %d running", running)))
	}
	if waiting > 0 {
		statsParts = append(statsParts, lipgloss.NewStyle().Foreground(ColorYellow).Render(fmt.Sprintf("◐ %d waiting", waiting)))
	}
	if idle > 0 {
		statsParts = append(statsParts, lipgloss.NewStyle().Foreground(ColorText).Render(fmt.Sprintf("○ %d idle", idle)))
	}
	if errored > 0 {
		statsParts = append(statsParts, lipgloss.NewStyle().Foreground(ColorRed).Render(fmt.Sprintf("✕ %d error", errored)))
	}

	// Fallback if no sessions
	stats := ""
	if len(statsParts) > 0 {
		stats = strings.Join(statsParts, statsSep)
	} else {
		stats = lipgloss.NewStyle().Foreground(ColorText).Render("no sessions")
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
	// Height breakdown: -1 header, -filterBarHeight filter, -updateBannerHeight banner, -maintenanceBannerHeight maintenance, -helpBarHeight help
	contentHeight := h.height - 1 - helpBarHeight - updateBannerHeight - maintenanceBannerHeight - filterBarHeight

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

	// CRITICAL: Use ensureExactHeight for robust, consistent output across all platforms
	// This is the single source of truth for output height - guarantees exactly h.height lines
	// regardless of component content, ANSI codes, or terminal differences
	result := ensureExactHeight(b.String(), h.height)

	// Apply width+height constraint via lipgloss.
	// CRITICAL: Use MaxWidth (truncate) instead of Width (word-wrap).
	// Width wraps long lines, which INCREASES the total line count beyond h.height.
	// When View() returns more lines than the terminal height, Bubble Tea's renderer
	// loses cursor position tracking, causing all subsequent frames to stack on top of
	// each other — making the entire view appear duplicated 2-4x.
	// MaxWidth truncates without adding lines. MaxHeight is a safety net.
	return lipgloss.NewStyle().
		MaxWidth(h.width).
		MaxHeight(h.height).
		Render(result)
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
func renderForkHintLine(b *strings.Builder) {
	hintStyle := lipgloss.NewStyle().Foreground(ColorText).Italic(true)
	keyStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	b.WriteString(hintStyle.Render("Fork:    "))
	b.WriteString(keyStyle.Render("f"))
	b.WriteString(hintStyle.Render(" quick fork, "))
	b.WriteString(keyStyle.Render("F"))
	b.WriteString(hintStyle.Render(" fork with options"))
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
	hint := hintStyle.Render("? for help")

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

	// Context-specific keys (left side)
	var contextKeys string
	if len(h.flatItems) == 0 {
		contextKeys = keyStyle.Render("n") + " " + keyStyle.Render("N") + " " + keyStyle.Render("i") + " " + keyStyle.Render("g")
	} else if h.cursor < len(h.flatItems) {
		item := h.flatItems[h.cursor]
		if item.Type == session.ItemTypeGroup {
			contextKeys = keyStyle.Render("⏎") + " " + keyStyle.Render("n") + " " + keyStyle.Render("N") + " " + keyStyle.Render("g")
		} else {
			contextKeys = keyStyle.Render("⏎") + " " + keyStyle.Render("n") + " " + keyStyle.Render("N") + " " + keyStyle.Render("R")
			if item.Session != nil && item.Session.CanFork() {
				contextKeys += " " + keyStyle.Render("f")
			}
			if item.Session != nil && (item.Session.Tool == "claude" || item.Session.Tool == "gemini") {
				contextKeys += " " + keyStyle.Render("M")
			}
		}
	}

	// Global keys (right side)
	globalStyle := lipgloss.NewStyle().Foreground(ColorComment)
	globalKeys := globalStyle.Render("↑↓") + " " + globalStyle.Render("/") + " " +
		globalStyle.Render("?") + " " + globalStyle.Render("q")

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

	raw := lipgloss.JoinVertical(lipgloss.Left, border, content)
	return lipgloss.NewStyle().MaxWidth(h.width).Render(raw)
}

// renderHelpBarCompact renders abbreviated help for medium terminals (70-99 cols)
func (h *Home) renderHelpBarCompact() string {
	borderStyle := lipgloss.NewStyle().Foreground(ColorBorder)
	border := borderStyle.Render(strings.Repeat("─", max(0, h.width)))

	sepStyle := lipgloss.NewStyle().Foreground(ColorBorder)
	sep := sepStyle.Render(" │ ")

	// Abbreviated key+short desc
	var contextHints []string
	if len(h.flatItems) == 0 {
		contextHints = []string{
			h.helpKeyShort("n/N", "New"),
			h.helpKeyShort("i", "Import"),
		}
	} else if h.cursor < len(h.flatItems) {
		item := h.flatItems[h.cursor]
		if item.Type == session.ItemTypeGroup {
			contextHints = []string{
				h.helpKeyShort("⏎", "Toggle"),
				h.helpKeyShort("n/N", "New"),
			}
		} else {
			contextHints = []string{
				h.helpKeyShort("⏎", "Attach"),
				h.helpKeyShort("n/N", "New"),
				h.helpKeyShort("R", "Restart"),
			}
			if item.Session != nil && item.Session.CanFork() {
				contextHints = append(contextHints, h.helpKeyShort("f", "Fork"))
			}
			if item.Session != nil && (item.Session.Tool == "claude" || item.Session.Tool == "gemini") {
				contextHints = append(contextHints, h.helpKeyShort("M", "MCP"))
				contextHints = append(contextHints, h.helpKeyShort("v", h.previewModeShort()))
			}
			contextHints = append(contextHints, h.helpKeyShort("c", "Copy"))
			contextHints = append(contextHints, h.helpKeyShort("x", "Send"))
		}
	}

	// Show undo hint when undo stack is non-empty
	if len(h.undoStack) > 0 {
		contextHints = append(contextHints, h.helpKeyShort("^Z", "Undo"))
	}

	// Global hints (abbreviated)
	globalStyle := lipgloss.NewStyle().Foreground(ColorComment)
	globalHints := globalStyle.Render("↑↓ Nav") + " " +
		globalStyle.Render("/") + " " +
		globalStyle.Render("?") + " " +
		globalStyle.Render("q")

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

	// Determine context-specific hints grouped by action type
	var primaryHints []string   // Main actions (attach, toggle, etc.)
	var secondaryHints []string // Edit actions (rename, move, delete)
	var contextTitle string

	if len(h.flatItems) == 0 {
		contextTitle = "Empty"
		primaryHints = []string{
			h.helpKey("n/N", "New/Quick"),
			h.helpKey("i", "Import"),
			h.helpKey("g", "Group"),
		}
	} else if h.cursor < len(h.flatItems) {
		item := h.flatItems[h.cursor]
		if item.Type == session.ItemTypeGroup {
			contextTitle = "Group"
			primaryHints = []string{
				h.helpKey("Tab", "Toggle"),
				h.helpKey("n/N", "New/Quick"),
				h.helpKey("g", "Group"),
			}
			secondaryHints = []string{
				h.helpKey("r", "Rename"),
				h.helpKey("d", "Delete"),
			}
		} else {
			contextTitle = "Session"
			primaryHints = []string{
				h.helpKey("Enter", "Attach"),
				h.helpKey("n/N", "New/Quick"),
				h.helpKey("g", "Group"),
				h.helpKey("R", "Restart"),
			}
			// Only show fork hints if session has a valid Claude session ID
			if item.Session != nil && item.Session.CanFork() {
				primaryHints = append(primaryHints, h.helpKey("f/F", "Fork"))
			}
			// Show MCP Manager and preview mode toggle for Claude and Gemini sessions
			if item.Session != nil && (item.Session.Tool == "claude" || item.Session.Tool == "gemini") {
				primaryHints = append(primaryHints, h.helpKey("M", "MCP"))
				primaryHints = append(primaryHints, h.helpKey("v", h.previewModeShort()))
			}
			primaryHints = append(primaryHints, h.helpKey("c", "Copy"))
			primaryHints = append(primaryHints, h.helpKey("x", "Send"))
			secondaryHints = []string{
				h.helpKey("r", "Rename"),
				h.helpKey("m", "Move"),
				h.helpKey("d", "Delete"),
			}
		}
	}

	// Show undo hint when undo stack is non-empty
	if len(h.undoStack) > 0 {
		secondaryHints = append(secondaryHints, h.helpKey("^Z", "Undo"))
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
	globalHints := globalStyle.Render("↑↓ Nav") + sep +
		globalStyle.Render("/ Search  G Global") + sep +
		globalStyle.Render("? Help  q Quit")

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

		emptyContent := renderEmptyStateResponsive(EmptyStateConfig{
			Icon:     "⬡",
			Title:    "No Sessions Yet",
			Subtitle: "Get started by creating your first session",
			Hints: []string{
				"Press n to create a new session",
				"Press i to import existing tmux sessions",
				"Press g to create a group",
			},
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

	for i := h.viewOffset; i < len(h.flatItems) && visibleCount < maxVisible; i++ {
		item := h.flatItems[i]
		h.renderItem(&b, item, i == h.cursor, i)
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

// renderItem renders a single item (group or session) for the left panel
func (h *Home) renderItem(b *strings.Builder, item session.Item, selected bool, itemIndex int) {
	if item.Type == session.ItemTypeGroup {
		h.renderGroupItem(b, item, selected, itemIndex)
	} else {
		h.renderSessionItem(b, item, selected)
	}
}

// renderGroupItem renders a group header
// PERFORMANCE: Uses cached styles from styles.go to avoid allocations
func (h *Home) renderGroupItem(b *strings.Builder, item session.Item, selected bool, itemIndex int) {
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

	// Use recursive count to include sessions in subgroups (Issue #48)
	sessionCount := h.groupTree.SessionCountForGroup(group.Path)
	countStr := countStyle.Render(fmt.Sprintf(" (%d)", sessionCount))

	// Status indicators (compact, on same line) using cached styles
	// Also count recursively for subgroups
	running := 0
	waiting := 0
	for path, g := range h.groupTree.Groups {
		if path == group.Path || strings.HasPrefix(path, group.Path+"/") {
			for _, sess := range g.Sessions {
				switch sess.Status {
				case session.StatusRunning:
					running++
				case session.StatusWaiting:
					waiting++
				}
			}
		}
	}

	statusStr := ""
	if running > 0 {
		statusStr += " " + GroupStatusRunning.Render(fmt.Sprintf("● %d", running))
	}
	if waiting > 0 {
		statusStr += " " + GroupStatusWaiting.Render(fmt.Sprintf("◐ %d", waiting))
	}

	// Build the row: [indent][hotkey][expand] [name](count) [status]
	row := fmt.Sprintf("%s%s%s %s%s%s", indent, hotkeyStr, expandIcon, nameStyle.Render(group.Name), countStr, statusStr)
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
func (h *Home) renderSessionItem(b *strings.Builder, item session.Item, selected bool) {
	inst := item.Session

	// Snapshot status and tool under read lock to avoid races with background worker
	instStatus := inst.GetStatusThreadSafe()
	instTool := inst.GetToolThreadSafe()

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
		// Rebuild baseIndent with selection styling for sub-sessions
		if item.IsSubSession && !item.ParentIsLastInGroup {
			groupIndent := strings.Repeat(treeEmpty, max(0, item.Level-2))
			baseIndent = groupIndent + " " + treeStyle.Render("│")
		}
	}

	title := titleStyle.Render(inst.Title)
	tool := toolStyle.Render(" " + instTool)

	// YOLO badge for Gemini sessions with YOLO mode enabled
	yoloBadge := ""
	if instTool == "gemini" && inst.GeminiYoloMode != nil && *inst.GeminiYoloMode {
		yoloStyle := lipgloss.NewStyle().Foreground(ColorYellow).Bold(true)
		if selected {
			yoloStyle = SessionStatusSelStyle
		}
		yoloBadge = yoloStyle.Render(" [YOLO]")
	}

	// Worktree branch badge for sessions running in git worktrees
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

	// Build row: [baseIndent][selection][tree][status] [title] [tool] [yolo] [worktree]
	// Format: " ├─ ● session-name tool" or "▶└─ ● session-name tool"
	// Sub-sessions get extra indent: "   ├─◐ sub-session tool"
	row := fmt.Sprintf("%s%s%s %s %s%s%s%s", baseIndent, selectionPrefix, treeStyle.Render(treeConnector), status, title, tool, yoloBadge, worktreeBadge)
	b.WriteString(row)
	b.WriteString("\n")
}

// renderLaunchingState renders the animated launching/resuming indicator for sessions
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
	b.WriteString(fmt.Sprintf("%s %s\n", labelStyle.Render("Created:"), valueStyle.Render(inst.CreatedAt.Format("Jan 2 15:04"))))

	return b.String()
}

// renderPreviewPane renders the right panel with live preview
func (h *Home) renderPreviewPane(width, height int) string {
	var b strings.Builder

	if len(h.flatItems) == 0 || h.cursor >= len(h.flatItems) {
		// Show different message when there are no sessions vs just no selection
		if len(h.flatItems) == 0 {
			return renderEmptyStateResponsive(EmptyStateConfig{
				Icon:     "✦",
				Title:    "Ready to Go",
				Subtitle: "Your workspace is set up",
				Hints: []string{
					"Press n to create your first session",
					"Press i to import tmux sessions",
				},
			}, width, height)
		}
		return renderEmptyStateResponsive(EmptyStateConfig{
			Icon:     "◇",
			Title:    "No Selection",
			Subtitle: "Select a session to preview",
			Hints:    nil,
		}, width, height)
	}

	item := h.flatItems[h.cursor]

	// If group is selected, show group info
	if item.Type == session.ItemTypeGroup {
		return h.renderGroupPreview(item.Group, width, height)
	}

	// Session preview
	selected := item.Session

	// Session info header box
	statusIcon := "○"
	statusColor := ColorTextDim
	switch selected.Status {
	case session.StatusRunning:
		statusIcon = "●"
		statusColor = ColorGreen
	case session.StatusWaiting:
		statusIcon = "◐"
		statusColor = ColorYellow
	case session.StatusError:
		statusIcon = "✕"
		statusColor = ColorRed
	}

	// Header with session name and status
	statusBadge := lipgloss.NewStyle().Foreground(statusColor).Render(statusIcon + " " + string(selected.Status))
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
	if selected.Status == session.StatusRunning {
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
		b.WriteString(wtHintStyle.Render("Finish:  "))
		b.WriteString(wtKeyStyle.Render("W"))
		b.WriteString(wtHintStyle.Render(" merge + cleanup"))
		b.WriteString("\n")
	}

	// Claude-specific info (session ID and MCPs)
	if selected.Tool == "claude" {
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
			hintStyle := lipgloss.NewStyle().Foreground(ColorText).Italic(true)
			keyStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
			b.WriteString(hintStyle.Render("Fork:    "))
			b.WriteString(keyStyle.Render("f"))
			b.WriteString(hintStyle.Render(" quick fork, "))
			b.WriteString(keyStyle.Render("F"))
			b.WriteString(hintStyle.Render(" fork with options"))
			b.WriteString("\n")
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
		uiLog.Debug("opencode_rendering_preview", slog.String("title", selected.Title), slog.String("session_id", selected.OpenCodeSessionID))

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
				renderForkHintLine(&b)
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
	if selected.Tool != "claude" && selected.Tool != "gemini" && selected.Tool != "opencode" && selected.Tool != "codex" {
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
				hintStyle := lipgloss.NewStyle().Foreground(ColorText).Italic(true)
				keyStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
				b.WriteString(hintStyle.Render("Resume:  "))
				b.WriteString(keyStyle.Render("r"))
				b.WriteString(hintStyle.Render(" restart with session resume"))
				b.WriteString("\n")
			}
		}
	}

	b.WriteString("\n")

	// Special handling for error state - show guidance instead of output
	if selected.Status == session.StatusError {
		errorHeader := renderSectionDivider("Session Inactive", width-4)
		b.WriteString(errorHeader)
		b.WriteString("\n\n")

		// Warning icon and message
		warnStyle := lipgloss.NewStyle().Foreground(ColorYellow)
		dimStyle := lipgloss.NewStyle().Foreground(ColorText)
		keyStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)

		b.WriteString(warnStyle.Render("⚠ No tmux session running"))
		b.WriteString("\n\n")
		b.WriteString(dimStyle.Render("This can happen if:"))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("  • Session was added but not yet started"))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("  • tmux server was restarted"))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("  • Terminal was closed or system rebooted"))
		b.WriteString("\n\n")
		b.WriteString(dimStyle.Render("Actions:"))
		b.WriteString("\n")
		b.WriteString("  ")
		b.WriteString(keyStyle.Render("R"))
		b.WriteString(dimStyle.Render(" Start   - create and start tmux session"))
		b.WriteString("\n")
		b.WriteString("  ")
		b.WriteString(keyStyle.Render("d"))
		b.WriteString(dimStyle.Render(" Delete  - remove from list"))
		b.WriteString("\n")
		b.WriteString("  ")
		b.WriteString(keyStyle.Render("Enter"))
		b.WriteString(dimStyle.Render(" - attach (will auto-start)"))
		b.WriteString("\n")

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

	// Check preview settings for what to show
	config, _ := session.LoadUserConfig()
	showAnalytics := config != nil && config.GetShowAnalytics() && (selected.Tool == "claude" || selected.Tool == "gemini")
	showOutput := config == nil || config.GetShowOutput() // Default to true if config fails

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

	// If output is disabled AND not starting up, return early
	// (We want to show the launch animation even if output is normally disabled)
	if !showOutput && !isStartingUp {
		// If analytics was also not shown, display session info card as fallback
		if !showAnalytics {
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

		// Brief minimum (500ms) to prevent flicker
		if timeSinceStart < 500*time.Millisecond {
			if isMcpLoading {
				showMcpLoadingAnimation = true
			} else {
				showLaunchingAnimation = true
			}
		} else if timeSinceStart < 15*time.Second {
			// STATUS-BASED CHECK: Session ready when Running/Waiting/Idle
			sessionReady := selected.Status == session.StatusRunning ||
				selected.Status == session.StatusWaiting ||
				selected.Status == session.StatusIdle

			if !sessionReady {
				// Also check content for faster detection
				h.previewCacheMu.RLock()
				previewContent := h.previewCache[selected.ID]
				h.previewCacheMu.RUnlock()

				if selected.Tool == "claude" || selected.Tool == "gemini" {
					// Claude/Gemini ready indicators
					agentReady := strings.Contains(previewContent, "ctrl+c to interrupt") ||
						strings.Contains(previewContent, "No, and tell Claude what to do differently") ||
						strings.Contains(previewContent, "\n> ") ||
						strings.Contains(previewContent, "> \n") ||
						strings.Contains(previewContent, "esc to interrupt") ||
						strings.Contains(previewContent, "⠋") || strings.Contains(previewContent, "⠙") ||
						strings.Contains(previewContent, "Thinking") ||
						strings.Contains(previewContent, "╭─")

					if selected.Tool == "gemini" {
						agentReady = agentReady ||
							strings.Contains(previewContent, "▸") ||
							strings.Contains(previewContent, "gemini>")
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
					if len(strings.TrimSpace(previewContent)) <= 50 {
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
	preview, hasCached := h.previewCache[selected.ID]
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
		if truncatedFromTop {
			// Reserve one line for the truncation indicator
			maxLines--
			if maxLines < 1 {
				maxLines = 1
			}
			truncatedCount = len(lines) - maxLines
			lines = lines[len(lines)-maxLines:]
		}

		previewStyle := lipgloss.NewStyle().Foreground(ColorText)
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

		for _, line := range lines {
			// Strip ANSI codes for accurate width measurement
			cleanLine := tmux.StripANSI(line)

			// Strip control characters (\r, \b, etc.) that can corrupt terminal
			// rendering. tmux capture-pane output may contain carriage returns
			// which, inside JoinHorizontal, move the cursor to column 0 and
			// overwrite the left panel content on that line.
			cleanLine = stripControlChars(cleanLine)

			// Handle empty lines - preserve some for readability
			trimmed := strings.TrimSpace(cleanLine)
			if trimmed == "" {
				consecutiveEmpty++
				if consecutiveEmpty <= maxConsecutiveEmpty {
					b.WriteString("\n") // Preserve empty line
				}
				continue
			}
			consecutiveEmpty = 0 // Reset counter on non-empty line

			// Truncate based on display width (handles CJK, emoji correctly)
			displayWidth := runewidth.StringWidth(cleanLine)
			if displayWidth > maxWidth {
				cleanLine = runewidth.Truncate(cleanLine, maxWidth-3, "...")
			}

			b.WriteString(previewStyle.Render(cleanLine))
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
		// Strip ANSI codes for accurate measurement
		cleanLine := tmux.StripANSI(line)
		displayWidth := runewidth.StringWidth(cleanLine)
		if displayWidth > maxWidth {
			// Truncate the clean version, then re-apply basic styling
			// Note: This loses original styling but prevents layout corruption
			truncated := runewidth.Truncate(cleanLine, maxWidth-3, "...")
			truncatedLines = append(truncatedLines, truncated)
		} else {
			truncatedLines = append(truncatedLines, line)
		}
	}

	return strings.Join(truncatedLines, "\n")
}

// stripControlChars removes C0 control characters (except \n and \t) from a string.
// tmux capture-pane output may include \r, \b, and other control characters that
// corrupt terminal rendering when embedded inside styled TUI output (e.g. \r moves
// the cursor to column 0, overwriting the left panel in a JoinHorizontal layout).
func stripControlChars(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' && r != '\t' {
			return -1 // Drop the character
		}
		return r
	}, s)
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
		case session.StatusError:
			errored++
		}
	}

	// Compact status line (inline, not badges)
	var statuses []string
	if running > 0 {
		statuses = append(statuses, lipgloss.NewStyle().Foreground(ColorGreen).Render(fmt.Sprintf("● %d running", running)))
	}
	if waiting > 0 {
		statuses = append(statuses, lipgloss.NewStyle().Foreground(ColorYellow).Render(fmt.Sprintf("◐ %d waiting", waiting)))
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
	b.WriteString(hintStyle.Render("Tab toggle • R rename • d delete • g subgroup"))

	// CRITICAL: Enforce width constraint on ALL lines to prevent overflow into left panel
	maxWidth := width - 2
	if maxWidth < 20 {
		maxWidth = 20
	}

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

// getOtherActiveSessions returns sessions excluding the given ID and error-status sessions.
func (h *Home) getOtherActiveSessions(excludeID string) []*session.Instance {
	var result []*session.Instance
	for _, inst := range h.instances {
		if inst.ID == excludeID {
			continue
		}
		if inst.GetStatusThreadSafe() == session.StatusError {
			continue
		}
		result = append(result, inst)
	}
	return result
}

// getSessionContent retrieves displayable content from a session.
// Tries GetLastResponse first, falls back to CaptureFullHistory.
func getSessionContent(inst *session.Instance) (string, error) {
	// Try AI response first
	resp, err := inst.GetLastResponse()
	if err == nil && resp.Content != "" {
		return resp.Content, nil
	}

	// Fall back to tmux pane capture
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
