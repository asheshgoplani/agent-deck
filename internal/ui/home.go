package ui

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/asheshgoplani/agent-deck/internal/git"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
	"github.com/asheshgoplani/agent-deck/internal/update"
)

// Version is set by main.go for update checking
var Version = "0.0.0"

// Hostname is cached at startup for display in the header
var Hostname = "unknown"

func init() {
	if h, err := os.Hostname(); err == nil {
		Hostname = h
	}
}

// SetVersion sets the current version for update checking
func SetVersion(v string) {
	Version = v
}

// Terminal escape sequences for smooth transitions
const (
	// Synchronized output (DEC mode 2026) - batches screen updates for atomic rendering
	// Supported by iTerm2, kitty, Alacritty, WezTerm, and other modern terminals
	syncOutputBegin = "\x1b[?2026h"
	syncOutputEnd   = "\x1b[?2026l"

	// Screen clear + cursor home
	clearScreen = "\033[2J\033[H"

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
	search            *Search
	globalSearch      *GlobalSearch              // Global session search across all Claude conversations
	globalSearchIndex *session.GlobalSearchIndex // Search index (nil if disabled)
	newDialog         *NewDialog
	groupDialog       *GroupDialog    // For creating/renaming groups
	forkDialog        *ForkDialog     // For forking sessions
	confirmDialog     *ConfirmDialog  // For confirming destructive actions
	helpOverlay       *HelpOverlay    // For showing keyboard shortcuts
	mcpDialog         *MCPDialog      // For managing MCPs
	setupWizard       *SetupWizard    // For first-run setup
	settingsPanel     *SettingsPanel  // For editing settings
	analyticsPanel    *AnalyticsPanel // For displaying session analytics

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
	statusFilter   session.Status     // Filter sessions by status ("" = all, or specific status)
	toolFilter     session.ToolFilter // Filter by tool type ("" = all, "agents" = AI only, "shells" = shell only)
	previewMode    PreviewMode        // What to show in preview pane (both, output-only, analytics-only)
	err            error
	errTime        time.Time  // When error occurred (for auto-dismiss)
	isReloading    bool       // Visual feedback during auto-reload
	initialLoading bool       // True until first loadSessionsMsg received (shows splash screen)
	reloadVersion  uint64     // Incremented on each reload to prevent stale background saves
	reloadMu       sync.Mutex // Protects reloadVersion and isReloading for thread-safe access

	// Remote session attach request (from Ctrl+b N shortcut)
	// Background worker sets this when it detects "attach:" signal, main loop processes it
	pendingRemoteAttach   string     // Session ID to attach to (set by background)
	pendingRemoteAttachMu sync.Mutex // Protects pendingRemoteAttach

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

	// Event-driven status detection (Priority 2)
	logWatcher *tmux.LogWatcher

	// PERFORMANCE: Debounce log activity status updates
	lastLogActivity map[string]time.Time // sessionID -> last update time
	logActivityMu   sync.Mutex           // Protects lastLogActivity map

	// File watcher for external changes (auto-reload)
	storageWatcher *StorageWatcher

	// Storage warning (shown if storage initialization failed)
	storageWarning string

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

	// Remote session discovery
	remoteDiscoveryTrigger      chan struct{}    // Triggers manual discovery (e.g., on 'r' refresh)
	lastRemoteDiscovery         time.Time        // When discovery last ran
	remoteDiscoveryRunning      atomic.Bool      // Prevents concurrent discoveries
	remoteDiscoveryStarted      atomic.Bool      // True once worker goroutine has been started
	remoteDiscoveryNeedsRebuild atomic.Bool      // Signal main loop to rebuild flatItems after discovery

	// Multi-instance support
	// When AllowMultiple is enabled, only the primary instance (first to start) manages
	// the notification bar and key bindings. Secondary instances are read-only for those.
	isPrimaryInstance bool
}

// reloadState preserves UI state during storage reload
type reloadState struct {
	cursorSessionID string          // ID of session at cursor (if cursor on session)
	cursorGroupPath string          // Path of group at cursor (if cursor on group)
	expandedGroups  map[string]bool // Expanded group paths
	viewOffset      int             // Scroll position
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

// storageChangedMsg signals that sessions.json was modified externally
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

// statusUpdateRequest is sent to the background worker with current viewport info
type statusUpdateRequest struct {
	viewOffset    int      // Current scroll position
	visibleHeight int      // How many items fit on screen
	flatItemIDs   []string // IDs of sessions in current flatItems order (for visible detection)
}

// startRemoteDiscoveryMsg signals that the remote discovery worker should start
// This is sent after the first render so local sessions are available immediately
type startRemoteDiscoveryMsg struct{}

// NewHome creates a new home model with the default profile
func NewHome() *Home {
	return NewHomeWithProfile("")
}

// NewHomeWithProfile creates a new home model with the specified profile as the primary instance.
// This is the default constructor for backward compatibility.
func NewHomeWithProfile(profile string) *Home {
	return NewHomeWithProfileAndMode(profile, true)
}

// NewHomeWithProfileAndMode creates a new Home with the specified profile and instance mode.
// isPrimary controls whether this instance manages the notification bar:
//   - true: Primary instance - manages notification bar, key bindings, signal file
//   - false: Secondary instance - read-only for notifications, full functionality otherwise
func NewHomeWithProfileAndMode(profile string, isPrimary bool) *Home {
	ctx, cancel := context.WithCancel(context.Background())

	var storageWarning string
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		// Log the error and set warning - sessions won't persist but app will still function
		log.Printf("Warning: failed to initialize storage, sessions won't persist: %v", err)
		storageWarning = fmt.Sprintf("⚠ Storage unavailable: %v (sessions won't persist)", err)
		storage = nil
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
		launchingSessions:      make(map[string]time.Time),
		resumingSessions:       make(map[string]time.Time),
		mcpLoadingSessions:     make(map[string]time.Time),
		forkingSessions:        make(map[string]time.Time),
		lastLogActivity:        make(map[string]time.Time),
		statusTrigger:          make(chan statusUpdateRequest, 1), // Buffered to avoid blocking
		statusWorkerDone:       make(chan struct{}),
		boundKeys:              make(map[string]string),
		remoteDiscoveryTrigger: make(chan struct{}, 1), // Buffered to avoid blocking
		isPrimaryInstance:      isPrimary,
	}

	// Initialize notification manager if enabled in config
	// Only primary instance manages the notification bar (prevents conflicts in multi-instance mode)
	notifSettings := session.GetNotificationsSettings()
	if notifSettings.Enabled && isPrimary {
		h.notificationsEnabled = true
		h.notificationManager = session.NewNotificationManager(notifSettings.MaxShown)

		// Initialize tmux status bar options for proper notification display
		// Fixes truncation (default status-left-length is only 10 chars)
		_ = tmux.InitializeStatusBarOptions()
	}

	// Initialize event-driven log watcher
	logWatcher, err := tmux.NewLogWatcher(tmux.LogDir(), func(sessionName string) {
		// Find session by tmux name and signal file activity
		h.instancesMu.RLock()
		for _, inst := range h.instances {
			if inst.GetTmuxSession() != nil && inst.GetTmuxSession().Name == sessionName {
				// PERFORMANCE: Debounce status updates from log events
				// Only trigger update if it's been >500ms since last log-triggered update
				h.logActivityMu.Lock()
				lastUpdate := h.lastLogActivity[inst.ID]
				if time.Since(lastUpdate) < 500*time.Millisecond {
					h.logActivityMu.Unlock()
					break // Too soon, skip this event
				}
				h.lastLogActivity[inst.ID] = time.Now()
				h.logActivityMu.Unlock()

				// Log file changed - trigger status update (will check busy indicator)
				// NOTE: We do NOT call SignalFileActivity() here anymore because
				// it bypasses the busy indicator check and causes false GREENs
				go func(i *session.Instance) {
					_ = i.UpdateStatus()
				}(inst)
				break
			}
		}
		h.instancesMu.RUnlock()
	})
	if err != nil {
		log.Printf("Warning: failed to create log watcher: %v (falling back to polling)", err)
	} else {
		h.logWatcher = logWatcher
		go h.logWatcher.Start()
	}

	// Start background status worker (Priority 1C)
	go h.statusWorker()

	// Note: Remote session discovery worker is started AFTER first render
	// via startRemoteDiscoveryMsg so local sessions are available immediately

	// Initialize global search
	h.globalSearch = NewGlobalSearch()
	claudeDir := session.GetClaudeConfigDir()
	userConfig, _ := session.LoadUserConfig()
	if userConfig != nil && userConfig.GlobalSearch.Enabled {
		globalSearchIndex, err := session.NewGlobalSearchIndex(claudeDir, userConfig.GlobalSearch)
		if err != nil {
			log.Printf("Warning: failed to initialize global search: %v", err)
		} else {
			h.globalSearchIndex = globalSearchIndex
			h.globalSearch.SetIndex(globalSearchIndex)
		}
	}

	// Initialize MCP socket pool if enabled
	// Note: Pool initialization happens AFTER loading sessions so we can discover MCPs in use
	// Pool will be initialized in Init() after sessions are loaded

	// Initialize storage watcher for auto-reload
	// Watches sessions.json for external changes (CLI commands) and triggers reload
	// with state preservation to maintain cursor position and expanded groups
	if storage != nil {
		storagePath, err := session.GetStoragePathForProfile(actualProfile)
		if err != nil {
			log.Printf("Warning: failed to get storage path for watcher: %v", err)
		} else {
			watcher, err := NewStorageWatcher(storagePath)
			if err != nil {
				// Log warning but continue (fallback to manual refresh with Ctrl+R)
				log.Printf("Warning: failed to initialize storage watcher: %v", err)
			} else {
				h.storageWatcher = watcher
				watcher.Start()
			}
		}
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
	// Restore expanded groups
	if h.groupTree != nil {
		for _, group := range h.groupTree.GroupList {
			group.Expanded = state.expandedGroups[group.Path]
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

	// Check if any filter is active
	hasStatusFilter := h.statusFilter != ""
	hasToolFilter := h.toolFilter != ""

	// Apply filters if active
	if hasStatusFilter || hasToolFilter {
		// First pass: identify groups that have matching sessions
		groupsWithMatches := make(map[string]bool)
		for _, item := range allItems {
			if item.Type == session.ItemTypeSession && item.Session != nil {
				matchesStatus := !hasStatusFilter || item.Session.Status == h.statusFilter
				matchesTool := !hasToolFilter || h.sessionMatchesToolFilter(item.Session)
				if matchesStatus && matchesTool {
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
				// Keep session if it matches all active filters
				matchesStatus := !hasStatusFilter || item.Session.Status == h.statusFilter
				matchesTool := !hasToolFilter || h.sessionMatchesToolFilter(item.Session)
				if matchesStatus && matchesTool {
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

// sessionMatchesToolFilter checks if a session matches the current tool filter
func (h *Home) sessionMatchesToolFilter(sess *session.Instance) bool {
	switch h.toolFilter {
	case session.ToolFilterAgents:
		return sess.IsAgent()
	case session.ToolFilterShells:
		return !sess.IsAgent()
	default:
		return true
	}
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

	// contentHeight = total height for main content area
	// -1 for header line, -helpBarHeight for help bar, -updateBannerHeight, -filterBarHeight
	contentHeight := h.height - 1 - helpBarHeight - updateBannerHeight - filterBarHeight

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

// syncNotifications updates the notification bar based on current session states
func (h *Home) syncNotifications() {
	if !h.notificationsEnabled || h.notificationManager == nil {
		return
	}

	// Debug: Check if we're being called
	debugNotif := os.Getenv("AGENTDECK_DEBUG") != ""

	// Phase 1: Check for signal file from Ctrl+b 1-6 shortcuts
	// When user presses Ctrl+b N, the key binding writes the session ID to a signal file
	var sessionToAcknowledgeID string
	if signalSessionID := tmux.ReadAndClearAckSignal(); signalSessionID != "" {
		sessionToAcknowledgeID = signalSessionID
		if debugNotif {
			log.Printf("[NOTIF] Signal file found: %s", signalSessionID)
		}
	}

	// Phase 2: Detect currently attached session (handles manual switches like Ctrl+b s)
	// This ensures sessions are excluded from bar even when switched via tmux directly
	currentSessionID := h.getAttachedSessionID()
	if debugNotif && currentSessionID != "" {
		log.Printf("[NOTIF] Detected attached session: %s", currentSessionID)
	}

	// Signal file takes priority (explicit acknowledgment via Ctrl+b N)
	if sessionToAcknowledgeID != "" {
		currentSessionID = sessionToAcknowledgeID
	}

	// Phase 3: Acknowledge the session if signal was received
	// NOTE: We always acknowledge regardless of current status because:
	// - Status might not be updated yet (async statusWorker)
	// - If we skip acknowledgment, the session gets re-added on next tick
	if sessionToAcknowledgeID != "" {
		h.instancesMu.RLock()
		if inst, ok := h.instanceByID[sessionToAcknowledgeID]; ok {
			if ts := inst.GetTmuxSession(); ts != nil {
				ts.Acknowledge()
				_ = inst.UpdateStatus()
				if debugNotif {
					log.Printf("[NOTIF] Acknowledged %s, new status: %s", inst.Title, inst.Status)
				}
			}
		}
		h.instancesMu.RUnlock()
	}

	// Phase 4: Sync notifications (uses its own lock internally)
	// Pass the current session ID so it gets excluded from the bar
	h.instancesMu.RLock()
	added, removed := h.notificationManager.SyncFromInstances(h.instances, currentSessionID)
	h.instancesMu.RUnlock()

	if debugNotif && (len(added) > 0 || len(removed) > 0) {
		log.Printf("[NOTIF] Sync: added=%v removed=%v", added, removed)
	}

	// Always update tmux status bars and key bindings
	h.updateTmuxNotifications()
}

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

// updateTmuxNotifications updates status bars and key bindings
func (h *Home) updateTmuxNotifications() {
	barText := h.notificationManager.FormatBar()

	// Only update status bars if the content changed
	// PERFORMANCE: Use global option - ONE tmux call instead of 100+
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
	} else {
		h.lastBarTextMu.Unlock()
	}

	// Update key bindings (thread-safe, can be called from foreground or background)
	h.updateKeyBindings()
}

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

	contentHeight := h.height - 1 - helpBarHeight - updateBannerHeight - filterBarHeight

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

// loadSessions loads sessions from storage and initializes the pool
func (h *Home) loadSessions() tea.Msg {
	if h.storage == nil {
		return loadSessionsMsg{instances: []*session.Instance{}, err: fmt.Errorf("storage not initialized")}
	}

	instances, groups, err := h.storage.LoadWithGroups()
	msg := loadSessionsMsg{instances: instances, groups: groups, err: err}

	// Initialize pool AFTER sessions are loaded
	userConfig, configErr := session.LoadUserConfig()
	if configErr == nil && userConfig != nil && userConfig.MCPPool.Enabled {
		pool, poolErr := session.InitializeGlobalPool(h.ctx, userConfig, instances)
		if poolErr != nil {
			log.Printf("Warning: failed to initialize MCP pool: %v", poolErr)
			msg.poolError = poolErr
		} else if pool != nil {
			proxies := pool.ListServers()
			log.Printf("✓ MCP Socket Pool initialized (%d proxies)", len(proxies))
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
	if inst.Status == session.StatusRunning ||
		inst.Status == session.StatusWaiting ||
		inst.Status == session.StatusIdle {
		// Session is ready - stop animation immediately
		return false
	}

	// CONTENT-BASED CHECK: Also check preview content for faster detection
	// This catches cases where status hasn't updated yet but content is visible
	h.previewCacheMu.RLock()
	previewContent := h.previewCache[sessionID]
	h.previewCacheMu.RUnlock()

	if inst.Tool == "claude" || inst.Tool == "gemini" {
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
		if inst.Tool == "gemini" {
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

	if inst.Tool == "claude" {
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
				log.Printf("Failed to parse analytics for session %s (claude session %s): %v", sessionID, claudeSessionID, err)
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
	} else if inst.Tool == "gemini" {
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
						log.Printf("STATUS WORKER PANIC (recovered): %v", r)
					}
				}()
				h.processStatusUpdate(req)
			}()
		}
	}
}

// startRemoteDiscoveryCmd returns a command that triggers starting the remote discovery worker
// This is deferred until after local sessions are loaded so the UI is responsive immediately
func (h *Home) startRemoteDiscoveryCmd() tea.Cmd {
	return func() tea.Msg {
		return startRemoteDiscoveryMsg{}
	}
}

// Remote discovery interval constants
const (
	remoteDiscoveryIntervalBackground = 60 * time.Second // When viewing local sessions
	remoteDiscoveryIntervalForeground = 10 * time.Second // When viewing remote sessions
)

// remoteDiscoveryWorker runs periodic remote session discovery in the background
// This discovers agentdeck_* sessions on configured SSH hosts with auto_discover=true
// Uses adaptive intervals: faster polling when viewing remote sessions
func (h *Home) remoteDiscoveryWorker() {
	settings := session.GetRemoteDiscoverySettings()
	if !settings.Enabled {
		log.Printf("[REMOTE-DISCOVERY] Disabled (no hosts with auto_discover=true)")
		return
	}

	log.Printf("[REMOTE-DISCOVERY] Worker started (adaptive intervals: %s background, %s foreground)",
		remoteDiscoveryIntervalBackground, remoteDiscoveryIntervalForeground)

	// Use a timer instead of ticker for adaptive intervals
	timer := time.NewTimer(h.getDiscoveryInterval())
	defer timer.Stop()

	// Run initial discovery immediately (worker start is already deferred until after local sessions load)
	h.runRemoteDiscovery()

	for {
		select {
		case <-h.ctx.Done():
			log.Printf("[REMOTE-DISCOVERY] Worker stopped")
			return

		case <-timer.C:
			h.runRemoteDiscovery()
			// Reset timer with current adaptive interval
			timer.Reset(h.getDiscoveryInterval())

		case <-h.remoteDiscoveryTrigger:
			// Manual trigger (e.g., from 'r' key refresh)
			h.runRemoteDiscovery()
			// Reset timer to restart interval countdown
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(h.getDiscoveryInterval())
		}
	}
}

// getDiscoveryInterval returns the appropriate discovery interval based on current view
// Returns faster interval when user is viewing a remote session
func (h *Home) getDiscoveryInterval() time.Duration {
	if h.isViewingRemoteSession() {
		return remoteDiscoveryIntervalForeground
	}
	return remoteDiscoveryIntervalBackground
}

// isViewingRemoteSession returns true if the currently selected session is remote
func (h *Home) isViewingRemoteSession() bool {
	selected := h.getSelectedSession()
	return selected != nil && selected.IsRemote()
}

// runRemoteDiscovery performs remote session discovery and sends results to TUI
func (h *Home) runRemoteDiscovery() {
	// Prevent concurrent discoveries
	if !h.remoteDiscoveryRunning.CompareAndSwap(false, true) {
		return
	}
	defer h.remoteDiscoveryRunning.Store(false)

	h.lastRemoteDiscovery = time.Now()

	// Get current instances snapshot
	h.instancesMu.RLock()
	existing := make([]*session.Instance, len(h.instances))
	copy(existing, h.instances)
	h.instancesMu.RUnlock()

	// Discover remote sessions
	discovered, updated, staleIDs, remoteGroups, errors := session.DiscoverRemoteTmuxSessions(existing)

	// Note: We don't send a message to the TUI here because that would require
	// access to the tea.Program which we don't have in the background worker.
	// Instead, we directly merge the results and trigger a save.

	if len(discovered) == 0 && len(updated) == 0 && len(staleIDs) == 0 && len(remoteGroups) == 0 && len(errors) == 0 {
		return // Nothing to do
	}

	// Log errors
	for hostID, err := range errors {
		log.Printf("[REMOTE-DISCOVERY] Error on %s: %v", hostID, err)
	}

	needsSave := false

	// Merge remote groups into local group tree
	if len(remoteGroups) > 0 && h.groupTree != nil {
		for _, rg := range remoteGroups {
			if _, exists := h.groupTree.Groups[rg.Path]; !exists {
				h.groupTree.EnsureGroupExists(rg.Path, rg.Name, rg.Order, rg.Expanded)
				log.Printf("[REMOTE-DISCOVERY] Created group: %s", rg.Path)
				needsSave = true
			}
		}
		if needsSave {
			h.groupTree.RebuildGroupList()
		}
	}

	// Remove stale sessions (sessions that no longer exist on remote)
	if len(staleIDs) > 0 {
		staleSet := make(map[string]bool)
		for _, id := range staleIDs {
			staleSet[id] = true
		}

		h.instancesMu.Lock()
		var filtered []*session.Instance
		for _, inst := range h.instances {
			if !staleSet[inst.ID] {
				filtered = append(filtered, inst)
			}
		}
		if len(filtered) < len(h.instances) {
			h.instances = filtered
			// Rebuild instanceByID map
			h.instanceByID = make(map[string]*session.Instance, len(h.instances))
			for _, inst := range h.instances {
				h.instanceByID[inst.ID] = inst
			}
			log.Printf("[REMOTE-DISCOVERY] Removed %d stale sessions", len(staleIDs))
			needsSave = true
		}
		h.instancesMu.Unlock()
	}

	// Handle updated sessions (group paths changed)
	if len(updated) > 0 && h.groupTree != nil {
		for _, upd := range updated {
			// Remove from old group and add to new group in tree
			h.groupTree.RemoveSession(upd.Instance)
			h.groupTree.AddSession(upd.Instance)
			log.Printf("[REMOTE-DISCOVERY] Moved session %s: %s -> %s",
				upd.Instance.Title, upd.OldGroupPath, upd.NewGroupPath)
		}
		h.groupTree.RebuildGroupList()
		needsSave = true
	}

	// Merge discovered sessions
	if len(discovered) > 0 {
		h.instancesMu.Lock()
		merged, newCount := session.MergeDiscoveredSessions(h.instances, discovered)
		if newCount > 0 {
			h.instances = merged
			// Rebuild instanceByID map
			h.instanceByID = make(map[string]*session.Instance, len(h.instances))
			for _, inst := range h.instances {
				h.instanceByID[inst.ID] = inst
			}
			log.Printf("[REMOTE-DISCOVERY] Added %d new sessions", newCount)
			needsSave = true
		}
		h.instancesMu.Unlock()
	}

	// Update status for remote sessions (batch with discovery SSH calls while connection is warm)
	// This leverages the ControlMaster connection established during discovery
	h.instancesMu.RLock()
	var remoteInstances []*session.Instance
	for _, inst := range h.instances {
		if inst.IsRemote() {
			remoteInstances = append(remoteInstances, inst)
		}
	}
	h.instancesMu.RUnlock()

	if len(remoteInstances) > 0 {
		statusUpdated := 0
		for _, inst := range remoteInstances {
			oldStatus := inst.Status
			if err := inst.UpdateStatus(); err == nil && inst.Status != oldStatus {
				statusUpdated++
			}
		}
		if statusUpdated > 0 {
			log.Printf("[REMOTE-DISCOVERY] Updated status for %d remote sessions", statusUpdated)
			h.cachedStatusCounts.valid.Store(false) // Invalidate status cache
		}
	}

	// Save to storage if anything changed
	// IMPORTANT: Use saveInstances() instead of direct SaveWithGroups() to:
	// 1. Check isReloading flag (prevents overwriting external changes during reload)
	// 2. Call NotifySave() (prevents storage watcher from triggering on our own save)
	// 3. Apply other defensive checks (empty instance protection, profile mismatch)
	if needsSave {
		h.saveInstances()
	}

	// Invalidate status cache to force UI refresh
	if needsSave {
		h.cachedStatusCounts.valid.Store(false)
		// Signal main loop to rebuild flatItems (can't call directly from background goroutine)
		h.remoteDiscoveryNeedsRebuild.Store(true)
	}
}

// triggerRemoteDiscovery signals the remote discovery worker to run immediately
func (h *Home) triggerRemoteDiscovery() {
	select {
	case h.remoteDiscoveryTrigger <- struct{}{}:
	default:
		// Channel full, discovery already pending
	}
}

// backgroundStatusUpdate runs independently of the TUI
// Updates session statuses and syncs notification bar directly to tmux
// This is called by the internal ticker even when TUI is paused (tea.Exec)
func (h *Home) backgroundStatusUpdate() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("Warning: background update recovered from panic: %v", r)
		}
	}()

	// Refresh tmux session cache
	tmux.RefreshExistingSessions()

	// Get instances snapshot
	h.instancesMu.RLock()
	if len(h.instances) == 0 {
		h.instancesMu.RUnlock()
		return
	}
	instances := make([]*session.Instance, len(h.instances))
	copy(instances, h.instances)
	h.instancesMu.RUnlock()

	// Update status for LOCAL instances only (remote sessions use SSH which is expensive)
	// Remote session status is updated during remote discovery cycle instead
	statusChanged := false
	for _, inst := range instances {
		if inst.IsRemote() {
			continue // Skip remote sessions - updated via remote discovery
		}
		oldStatus := inst.Status
		_ = inst.UpdateStatus()
		if inst.Status != oldStatus {
			statusChanged = true
			log.Printf("[BACKGROUND] Status changed: %s %s -> %s", inst.Title, oldStatus, inst.Status)
		}
	}

	// Invalidate cache if status changed
	if statusChanged {
		h.cachedStatusCounts.valid.Store(false)
	}

	// Always sync notification bar - must check for signal file (Ctrl+b N acknowledgments)
	// even when no status changes occurred
	h.syncNotificationsBackground()
}

// syncNotificationsBackground updates the tmux notification bar directly
// Called from background worker - does NOT depend on Bubble Tea
func (h *Home) syncNotificationsBackground() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("Warning: syncNotificationsBackground recovered from panic: %v", r)
		}
	}()

	if !h.notificationsEnabled || h.notificationManager == nil {
		return
	}

	debug := os.Getenv("AGENTDECK_DEBUG") != ""

	// Phase 1: Check for signal file from Ctrl+b 1-6 shortcuts
	// CRITICAL: This must be done in background sync too, because the foreground
	// sync might not run when user is attached to a session (tea.Exec pauses TUI)
	var sessionToAcknowledgeID string
	var remoteAttachSessionID string // For remote sessions that need attach flow
	if signalSessionID := tmux.ReadAndClearAckSignal(); signalSessionID != "" {
		if debug {
			log.Printf("[NOTIF-BG] Signal file found: %s", signalSessionID)
		}

		// Check for attach: prefix (remote session attach request)
		if strings.HasPrefix(signalSessionID, "attach:") {
			remoteAttachSessionID = strings.TrimPrefix(signalSessionID, "attach:")
			if debug {
				log.Printf("[NOTIF-BG] Remote attach request for: %s", remoteAttachSessionID)
			}
		} else {
			sessionToAcknowledgeID = signalSessionID
		}
	}

	// Handle remote attach request - set pendingRemoteAttach for main loop to process
	if remoteAttachSessionID != "" {
		h.pendingRemoteAttachMu.Lock()
		h.pendingRemoteAttach = remoteAttachSessionID
		h.pendingRemoteAttachMu.Unlock()
		if debug {
			log.Printf("[NOTIF-BG] Set pendingRemoteAttach: %s", remoteAttachSessionID)
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
				_ = inst.UpdateStatus()
				if debug {
					log.Printf("[NOTIF-BG] Acknowledged %s, new status: %s", inst.Title, inst.Status)
				}
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

	if debug {
		log.Printf("[NOTIF-BG] currentSessionID=%s, instances=%d", currentSessionID, len(instances))
	}

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

		log.Printf("[BACKGROUND] Notification bar updated: %s", barText)
	} else {
		h.lastBarTextMu.Unlock()
	}

	// CRITICAL: Update key bindings in background too!
	// This fixes the bug where key bindings became stale when TUI was paused (tea.Exec).
	// The updateTmuxNotifications() function is now thread-safe via boundKeysMu.
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
		bindingKey string // "sessionID:tmuxName" or "remote:sessionID" for remote sessions
		isRemote   bool   // True for remote sessions (need attach: signal instead of switch-client)
	}
	bindings := make([]bindingInfo, 0, len(entries))
	currentKeys := make(map[string]string) // key -> sessionID

	h.instancesMu.RLock()
	for _, e := range entries {
		currentKeys[e.AssignedKey] = e.SessionID

		// Look up CURRENT TmuxName from instance (cached entry may be stale)
		currentTmuxName := e.TmuxName
		isRemote := false
		if inst, ok := h.instanceByID[e.SessionID]; ok {
			if ts := inst.GetTmuxSession(); ts != nil {
				currentTmuxName = ts.Name
			}
			isRemote = inst.IsRemote()
		}

		// For remote sessions, use "remote:" prefix in binding key to differentiate
		bindingKey := e.SessionID + ":" + currentTmuxName
		if isRemote {
			bindingKey = "remote:" + e.SessionID
		}

		bindings = append(bindings, bindingInfo{
			key:        e.AssignedKey,
			sessionID:  e.SessionID,
			tmuxName:   currentTmuxName,
			bindingKey: bindingKey,
			isRemote:   isRemote,
		})
	}
	h.instancesMu.RUnlock()

	// Phase 2: Update key bindings while holding boundKeysMu
	h.boundKeysMu.Lock()
	for _, b := range bindings {
		existingBinding, isBound := h.boundKeys[b.key]
		if !isBound || existingBinding != b.bindingKey {
			if b.isRemote {
				// Remote session: use attach signal (can't switch-client to non-local session)
				_ = tmux.BindAttachKeyWithAck(b.key, b.sessionID)
			} else {
				// Local session: use standard switch-client with ack
				_ = tmux.BindSwitchKeyWithAck(b.key, b.tmuxName, b.sessionID)
			}
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
			oldStatus := inst.Status
			_ = inst.UpdateStatus() // Ignore errors in background worker
			if inst.Status != oldStatus {
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
		if inst.Status == "idle" {
			continue
		}

		oldStatus := inst.Status
		_ = inst.UpdateStatus() // Ignore errors in background worker
		if inst.Status != oldStatus {
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
	case tea.WindowSizeMsg:
		h.width = msg.Width
		h.height = msg.Height
		h.updateSizes()
		h.syncViewport() // Recalculate viewport when window size changes
		h.setupWizard.SetSize(msg.Width, msg.Height)
		h.settingsPanel.SetSize(msg.Width, msg.Height)
		return h, nil

	case loadSessionsMsg:
		// Clear loading indicators
		h.reloadMu.Lock()
		h.isReloading = false
		h.reloadMu.Unlock()
		h.initialLoading = false // First load complete, hide splash

		if msg.err != nil {
			h.setError(msg.err)
		} else {
			h.instancesMu.Lock()
			oldCount := len(h.instances)
			h.instances = msg.instances
			newCount := len(msg.instances)
			log.Printf("[RELOAD-DEBUG] loadSessionsMsg: replacing %d instances with %d instances (profile=%s)", oldCount, newCount, h.profile)
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
			h.rebuildFlatItems()
			h.search.SetItems(h.instances)

			// Restore state if provided (from auto-reload)
			if msg.restoreState != nil {
				h.restoreState(*msg.restoreState)
				h.syncViewport()
			} else {
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
		// Start remote discovery after local sessions are loaded (deferred startup)
		return h, h.startRemoteDiscoveryCmd()

	case startRemoteDiscoveryMsg:
		// Start remote discovery worker (only once, after first render)
		if h.remoteDiscoveryStarted.CompareAndSwap(false, true) {
			go h.remoteDiscoveryWorker()
			log.Printf("[REMOTE-DISCOVERY] Worker started (deferred after local sessions loaded)")
		}
		return h, nil

	case sessionCreatedMsg:
		// CRITICAL FIX: Skip processing during reload to prevent state corruption
		// If we modify h.instances during reload, the loadSessionsMsg will overwrite
		// our changes, but by then we've already modified groupTree inconsistently
		if h.isReloading {
			// The reload will provide fresh data - don't modify state now
			log.Printf("[RELOAD-DEBUG] sessionCreatedMsg: skipping during reload")
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

			// CRITICAL: Save to storage BEFORE starting tmux session to prevent orphans
			// If save fails, the tmux session won't be created
			h.saveInstances()

			// Start the session after save completes
			// This happens asynchronously - if it fails, the session will show as "stopped"
			return h, tea.Batch(
				h.startSession(msg.instance),
				h.fetchPreview(msg.instance),
			)
		}
		return h, nil

	case sessionForkedMsg:
		// Clean up forking state for source session
		if msg.sourceID != "" {
			delete(h.forkingSessions, msg.sourceID)
		}

		// CRITICAL FIX: Skip processing during reload to prevent state corruption
		if h.isReloading {
			log.Printf("[RELOAD-DEBUG] sessionForkedMsg: skipping during reload")
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

			// CRITICAL: Save to storage BEFORE starting tmux session to prevent orphans
			// If save fails, the tmux session won't be created
			h.saveInstances()

			// Start the forked session after save completes
			// This happens asynchronously - if it fails, the session will show as "stopped"
			return h, tea.Batch(
				h.startForkedSession(msg.instance),
				h.fetchPreview(msg.instance),
			)
		}
		return h, nil

	case sessionDeletedMsg:
		// CRITICAL FIX: Skip processing during reload to prevent state corruption
		if h.isReloading {
			log.Printf("[RELOAD-DEBUG] sessionDeletedMsg: skipping during reload")
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
		// Invalidate status counts cache
		h.cachedStatusCounts.valid.Store(false)
		// Invalidate preview cache for deleted session
		h.invalidatePreviewCache(msg.deletedID)
		// Remove from group tree (preserves empty groups)
		if deletedInstance != nil {
			h.groupTree.RemoveSession(deletedInstance)
		}
		h.rebuildFlatItems()
		// Update search items
		h.search.SetItems(h.instances)
		// Save both instances AND groups (critical fix: was losing groups!)
		h.saveInstances()
		return h, nil

	case openCodeDetectionCompleteMsg:
		// OpenCode session detection completed
		// CRITICAL: Find the CURRENT instance by ID and update it
		// The original pointer may have been replaced by storage watcher reload
		if msg.sessionID != "" {
			log.Printf("[OPENCODE] Detection complete for instance %s: session=%s",
				msg.instanceID, msg.sessionID)
			// Update the CURRENT instance (not the original pointer which may be stale)
			if inst := h.getInstanceByID(msg.instanceID); inst != nil {
				inst.OpenCodeSessionID = msg.sessionID
				inst.OpenCodeDetectedAt = time.Now()
				log.Printf("[OPENCODE] Updated current instance %s with session ID %s",
					msg.instanceID, msg.sessionID)
			} else {
				log.Printf("[OPENCODE] Warning: instance %s not found in current instances",
					msg.instanceID)
			}
		} else {
			log.Printf("[OPENCODE] Detection complete for instance %s: no session found",
				msg.instanceID)
			// Mark detection as completed even when no session found
			// This allows UI to show "No session found" instead of "Detecting..."
			if inst := h.getInstanceByID(msg.instanceID); inst != nil {
				inst.OpenCodeDetectedAt = time.Now()
				log.Printf("[OPENCODE] Marked detection complete for %s (no session found)",
					msg.instanceID)
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
			h.saveInstances()
			// NOTE: Do NOT delete from mcpLoadingSessions here!
			// Animation continues until Claude is ready or timeout expires
			log.Printf("[MCP-DEBUG] mcpRestartedMsg: MCP reload initiated for %s (animation continues)", msg.session.ID)
		}
		return h, nil

	case updateCheckMsg:
		h.updateInfo = msg.info
		return h, nil

	case refreshMsg:
		// Also trigger remote discovery on manual refresh
		h.triggerRemoteDiscovery()
		return h, h.loadSessions

	case storageChangedMsg:
		log.Printf("[RELOAD-DEBUG] storageChangedMsg received (profile=%s, current instances=%d)", h.profile, len(h.instances))

		// Show reload indicator and increment version to invalidate in-flight background saves
		h.reloadMu.Lock()
		h.isReloading = true
		h.reloadVersion++
		h.reloadMu.Unlock()

		// Preserve UI state before reload
		state := h.preserveState()

		// Reload from disk
		cmd := func() tea.Msg {
			instances, groups, err := h.storage.LoadWithGroups()
			log.Printf("[RELOAD-DEBUG] LoadWithGroups returned %d instances, err=%v", len(instances), err)
			return loadSessionsMsg{
				instances:    instances,
				groups:       groups,
				err:          err,
				restoreState: &state, // Pass state to restore after load
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
			if (inst.Tool == "claude" || inst.Tool == "gemini") && h.analyticsFetchingID != inst.ID {
				if inst.Tool == "claude" {
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
				} else if inst.Tool == "gemini" {
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

	case tickMsg:
		// Auto-dismiss errors after 5 seconds
		if h.err != nil && !h.errTime.IsZero() && time.Since(h.errTime) > 5*time.Second {
			h.clearError()
		}

		// Check if remote discovery needs UI refresh (set by background worker)
		if h.remoteDiscoveryNeedsRebuild.CompareAndSwap(true, false) {
			h.rebuildFlatItems()
			h.search.SetItems(h.instances)
		}

		// Check for pending remote attach request (from Ctrl+b N shortcut on remote session)
		h.pendingRemoteAttachMu.Lock()
		attachID := h.pendingRemoteAttach
		h.pendingRemoteAttach = ""
		h.pendingRemoteAttachMu.Unlock()
		if attachID != "" {
			if inst, ok := h.instanceByID[attachID]; ok {
				return h, h.attachSession(inst)
			}
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

		// Sync notification bar with current session states
		h.syncNotifications()

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
				// Auto-save on every change
				config := h.settingsPanel.GetConfig()
				if err := session.SaveUserConfig(config); err != nil {
					h.err = err
					h.errTime = time.Now()
				}
				_, _ = session.ReloadUserConfig()
				// Apply default tool to new dialog
				if defaultTool := session.GetDefaultTool(); defaultTool != "" {
					h.newDialog.SetDefaultTool(defaultTool)
				}
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

		// Build resume command with config dir and dangerous mode
		userConfig, _ := session.LoadUserConfig()
		dangerousMode := false
		if userConfig != nil {
			dangerousMode = userConfig.Claude.DangerousMode
		}

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
		if dangerousMode {
			cmdBuilder.WriteString(" --dangerously-skip-permissions")
		}
		inst.Command = cmdBuilder.String()

		// CRITICAL: Do NOT start the session here - it will be started in the message handler
		// AFTER saving to storage to prevent orphaned tmux sessions if save fails
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
			h.setError(fmt.Errorf("validation error: %s", validationErr))
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
				h.setError(fmt.Errorf("path is not a git repository"))
				return h, nil
			}

			repoRoot, err := git.GetRepoRoot(path)
			if err != nil {
				h.setError(fmt.Errorf("failed to get repo root: %w", err))
				return h, nil
			}

			// Generate worktree path
			worktreePath = git.GenerateWorktreePath(repoRoot, branchName)

			// Create worktree
			if err := git.CreateWorktree(repoRoot, worktreePath, branchName); err != nil {
				h.setError(fmt.Errorf("failed to create worktree: %w", err))
				return h, nil
			}

			// Store repo root for later use
			worktreeRepoRoot = repoRoot
			// Update path to worktree for session creation
			path = worktreePath
		}

		h.newDialog.Hide()
		h.clearError() // Clear any previous validation error

		// Get Gemini YOLO mode from dialog
		geminiYoloMode := h.newDialog.IsGeminiYoloMode()

		// Create session with worktree info and options (claudeOpts already obtained above)
		return h, h.createSessionInGroupWithWorktreeAndOptions(name, path, command, groupPath, worktreePath, worktreeRepoRoot, branchName, geminiYoloMode, claudeOpts)

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

	case "G": // Jump to bottom
		if len(h.flatItems) > 0 {
			h.cursor = len(h.flatItems) - 1
			h.syncViewport()
			h.lastNavigationTime = time.Now()
			h.isNavigating = true
			if selected := h.getSelectedSession(); selected != nil {
				return h, h.fetchPreviewDebounced(selected.ID)
			}
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
				h.groupTree.ToggleGroup(item.Path)
				h.rebuildFlatItems()
			}
		}
		return h, nil

	case "tab", "l", "right":
		// Expand/collapse group or expand if on session
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeGroup {
				h.groupTree.ToggleGroup(item.Path)
				h.rebuildFlatItems()
			}
		}
		return h, nil

	case "h", "left":
		// Collapse group
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeGroup {
				h.groupTree.CollapseGroup(item.Path)
				h.rebuildFlatItems()
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
			if item.Type == session.ItemTypeSession && item.Session != nil && item.Session.CanFork() {
				return h, h.quickForkSession(item.Session)
			}
		}
		return h, nil

	case "F", "shift+f":
		// Fork with dialog (customize title and group)
		// Only available when session has a valid Claude session ID
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeSession && item.Session != nil && item.Session.CanFork() {
				return h, h.forkSessionWithDialog(item.Session)
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

		// Create new group based on context:
		// - Session in a group → create subgroup in session's group
		// - Group selected → create peer group (sibling at same level)
		// - Root level → create root-level group
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeGroup {
				// Group selected: create peer group (sibling)
				// Get parent path by removing last segment
				parentPath := ""
				parentName := ""
				if idx := strings.LastIndex(item.Group.Path, "/"); idx > 0 {
					parentPath = item.Group.Path[:idx]
					// Get parent name from parent path
					if lastIdx := strings.LastIndex(parentPath, "/"); lastIdx >= 0 {
						parentName = parentPath[lastIdx+1:]
					} else {
						parentName = parentPath
					}
					h.groupDialog.ShowCreateSubgroup(parentPath, parentName)
				} else {
					// Top-level group: create another root-level group
					h.groupDialog.Show()
				}
				return h, nil
			} else if item.Type == session.ItemTypeSession && item.Session != nil && item.Session.GroupPath != "" {
				// Session in a group: create subgroup in session's group
				groupPath := item.Session.GroupPath
				groupName := groupPath
				if idx := strings.LastIndex(groupPath, "/"); idx >= 0 {
					groupName = groupPath[idx+1:]
				}
				h.groupDialog.ShowCreateSubgroup(groupPath, groupName)
				return h, nil
			}
		}
		// Create root-level group (no selection or session at root)
		h.groupDialog.Show()
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

	case "T":
		// Toggle theme between dark and light
		currentTheme := GetCurrentTheme()
		var newTheme string
		if currentTheme == ThemeLight {
			newTheme = "dark"
		} else {
			newTheme = "light"
		}
		// Apply new theme immediately
		InitTheme(newTheme)
		// Save preference to config
		if config, err := session.LoadUserConfig(); err == nil {
			config.Theme = newTheme
			if err := session.SaveUserConfig(config); err != nil {
				h.err = err
				h.errTime = time.Now()
			}
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
		groupPath := session.DefaultGroupName
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
		// Mark session as unread (change idle → waiting)
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeSession && item.Session != nil {
				tmuxSess := item.Session.GetTmuxSession()
				if tmuxSess != nil {
					tmuxSess.ResetAcknowledged()
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
				if inst.Status == session.StatusRunning || inst.Status == session.StatusWaiting {
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
				if item.Session.CanRestart() {
					// Track as resuming for animation (before async call starts)
					h.resumingSessions[item.Session.ID] = time.Now()
					return h, h.restartSession(item.Session)
				}
			}
		}
		return h, nil

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

	case "D":
		// Manual remote discovery (Shift+D - discover sessions on SSH hosts)
		h.triggerRemoteDiscovery()
		return h, nil

	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		// Quick jump to Nth root group (1-indexed)
		targetNum := int(msg.String()[0] - '0') // Convert "1" -> 1, "2" -> 2, etc.
		h.jumpToRootGroup(targetNum)
		return h, nil

	case "0":
		// Clear all filters (status and tool)
		h.statusFilter = ""
		h.toolFilter = ""
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

	case "%", "shift+5":
		// Cycle tool filter: all -> agents -> shells -> all
		switch h.toolFilter {
		case session.ToolFilterAll:
			h.toolFilter = session.ToolFilterAgents
		case session.ToolFilterAgents:
			h.toolFilter = session.ToolFilterShells
		case session.ToolFilterShells:
			h.toolFilter = session.ToolFilterAll
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
		// Special handling for quit with pool dialog
		switch msg.String() {
		case "k", "K":
			// Keep pool running - quit without shutting down
			h.confirmDialog.Hide()
			return h, h.performQuit(false) // false = don't shutdown pool
		case "s", "S":
			// Shut down pool - quit and shutdown
			h.confirmDialog.Hide()
			return h, h.performQuit(true) // true = shutdown pool
		case "esc":
			// Cancel - don't quit
			h.confirmDialog.Hide()
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
	return h, h.performQuit(true)
}

// performQuit performs the actual quit logic
// shutdownPool: true = shutdown MCP pool, false = leave running in background
func (h *Home) performQuit(shutdownPool bool) tea.Cmd {
	return func() tea.Msg {
		// Signal background worker to stop
		h.cancel()

		// Wait for background worker to finish with timeout (prevents hang if SSH is slow)
		select {
		case <-h.statusWorkerDone:
			// Clean exit
		case <-time.After(2 * time.Second):
			log.Printf("Warning: status worker did not stop within timeout")
		}

		if h.logWatcher != nil {
			h.logWatcher.Close()
		}
		// Close storage watcher
		if h.storageWatcher != nil {
			h.storageWatcher.Close()
		}
		// Close global search index
		if h.globalSearchIndex != nil {
			h.globalSearchIndex.Close()
		}
		// Shutdown or disconnect from MCP pool based on user choice
		if err := session.ShutdownGlobalPool(shutdownPool); err != nil {
			log.Printf("Warning: error handling MCP pool: %v", err)
		}
		// Clean up notification bar (clear tmux status bars and unbind keys)
		h.cleanupNotifications()
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
		log.Printf("[MCP-DEBUG] Enter pressed in MCP dialog")

		// Apply changes and close dialog
		hasChanged := h.mcpDialog.HasChanged()
		log.Printf("[MCP-DEBUG] HasChanged() = %v", hasChanged)

		if hasChanged {
			// Apply changes (saves state + writes .mcp.json)
			if err := h.mcpDialog.Apply(); err != nil {
				log.Printf("[MCP-DEBUG] Apply() failed: %v", err)
				h.setError(err)
				h.mcpDialog.Hide() // Hide dialog even on error
				return h, nil
			}
			log.Printf("[MCP-DEBUG] Apply() succeeded")

			// Find the session by ID (stored when dialog opened - same as Shift+S uses)
			sessionID := h.mcpDialog.GetSessionID()
			log.Printf("[MCP-DEBUG] Looking for sessionID: %q", sessionID)

			// O(1) lookup - no lock needed as Update() runs on main goroutine
			targetInst := h.getInstanceByID(sessionID)
			if targetInst != nil {
				log.Printf("[MCP-DEBUG] Found session by ID: %s, Title=%s", targetInst.ID, targetInst.Title)
			}

			if targetInst != nil {
				log.Printf("[MCP-DEBUG] Calling restartSession for: %s (with MCP loading animation)", targetInst.ID)
				// Track as MCP loading for animation in preview pane
				h.mcpLoadingSessions[targetInst.ID] = time.Now()
				// Set flag to skip MCP regeneration (Apply just wrote the config)
				targetInst.SkipMCPRegenerate = true
				// Restart the session to apply MCP changes
				h.mcpDialog.Hide()
				return h, h.restartSession(targetInst)
			} else {
				log.Printf("[MCP-DEBUG] No session found with ID: %s", sessionID)
			}
		}
		log.Printf("[MCP-DEBUG] Hiding dialog without restart")
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
			h.setError(fmt.Errorf("validation error: %s", validationErr))
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
				}
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
		// Get fork parameters from dialog
		title, groupPath := h.forkDialog.GetValues()
		opts := h.forkDialog.GetOptions()
		if title == "" {
			h.setError(fmt.Errorf("session name cannot be empty"))
			return h, nil
		}
		h.clearError() // Clear any previous error

		// Find the currently selected session
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeSession && item.Session != nil {
				h.forkDialog.Hide()
				return h, h.forkSessionCmdWithOptions(item.Session, title, groupPath, opts)
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
	if h.isReloading && !force {
		log.Printf("[SAVE-DEBUG] Skipping save during reload (force=%v)", force)
		return
	}
	if force && h.isReloading {
		log.Printf("[SAVE-DEBUG] Force saving despite reload in progress")
	}

	if h.storage != nil {
		// DEFENSIVE CHECK: Verify we're saving to the correct profile's file
		// This prevents catastrophic cross-profile contamination
		expectedPath, err := session.GetStoragePathForProfile(h.profile)
		if err != nil {
			log.Printf("[SAVE-DEBUG] Failed to get expected path for profile %s: %v", h.profile, err)
			return
		}
		if h.storage.Path() != expectedPath {
			log.Printf("[SAVE-DEBUG] CRITICAL: Storage path mismatch! Profile=%s, Expected=%s, Got=%s - ABORTING SAVE TO PREVENT DATA LOSS", h.profile, expectedPath, h.storage.Path())
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

		log.Printf("[SAVE-DEBUG] Saving %d instances to profile %s (path=%s, force=%v)", instanceCount, h.profile, h.storage.Path(), force)

		// DEFENSIVE: Never save empty instances if storage file has data
		// This prevents catastrophic data loss from transient load failures
		if instanceCount == 0 {
			// Check if storage file exists and has data before overwriting with empty
			if info, err := os.Stat(h.storage.Path()); err == nil && info.Size() > 100 {
				log.Printf("[SAVE-DEBUG] WARNING: Refusing to save empty instances - storage file has %d bytes (potential data loss)", info.Size())
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
		}
	}
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

// createSessionInGroupWithWorktreeAndOptions creates a new session with full options including YOLO mode and Claude options
func (h *Home) createSessionInGroupWithWorktreeAndOptions(name, path, command, groupPath, worktreePath, worktreeRepoRoot, worktreeBranch string, geminiYoloMode bool, claudeOpts *session.ClaudeOptions) tea.Cmd {
	return func() tea.Msg {
		// Check tmux availability before creating session
		if err := tmux.IsTmuxAvailable(); err != nil {
			return sessionCreatedMsg{err: fmt.Errorf("cannot create session: %w", err)}
		}

		// Determine tool from command for proper session initialization
		// When tool is "claude", session ID will be detected from files after start
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
		}

		var inst *session.Instance
		var err error

		// Check if this is a remote group and create remote session if so
		if hostID, isRemote := session.GetSSHHostIDFromGroupPath(groupPath); isRemote {
			// Create remote session on the SSH host
			inst, err = session.NewRemoteInstanceWithGroup(name, path, groupPath, tool, hostID)
			if err != nil {
				return sessionCreatedMsg{err: fmt.Errorf("failed to create remote session: %w", err)}
			}
		} else if groupPath != "" {
			inst = session.NewInstanceWithGroupAndTool(name, path, groupPath, tool)
		} else {
			inst = session.NewInstanceWithTool(name, path, tool)
		}
		inst.Command = command

		// Set worktree fields if provided (only for local sessions)
		if worktreePath != "" && inst.RemoteHost == "" {
			inst.WorktreePath = worktreePath
			inst.WorktreeRepoRoot = worktreeRepoRoot
			inst.WorktreeBranch = worktreeBranch
		}

		// Set Gemini YOLO mode if enabled (per-session override)
		if geminiYoloMode && tool == "gemini" {
			inst.GeminiYoloMode = &geminiYoloMode
		}

		// Apply Claude options if provided
		if tool == "claude" && claudeOpts != nil {
			if err := inst.SetClaudeOptions(claudeOpts); err != nil {
				return sessionCreatedMsg{err: fmt.Errorf("failed to set Claude options: %w", err)}
			}
		}

		// CRITICAL: Do NOT start the session here - it will be started in the message handler
		// AFTER saving to storage to prevent orphaned tmux sessions if save fails
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

	sourceID := source.ID // Capture for closure

	return func() tea.Msg {
		// Check tmux availability before forking
		if err := tmux.IsTmuxAvailable(); err != nil {
			return sessionForkedMsg{err: fmt.Errorf("cannot fork session: %w", err), sourceID: sourceID}
		}

		// Use CreateForkedInstanceWithOptions to get the proper fork command with options
		inst, _, err := source.CreateForkedInstanceWithOptions(title, groupPath, opts)
		if err != nil {
			return sessionForkedMsg{err: fmt.Errorf("cannot create forked instance: %w", err), sourceID: sourceID}
		}

		// CRITICAL: Do NOT start the session here - it will be started in the message handler
		// AFTER saving to storage to prevent orphaned tmux sessions if save fails
		return sessionForkedMsg{instance: inst, sourceID: sourceID}
	}
}

// startSession starts a newly created session after it has been saved to storage
// This is called after saving to prevent orphaned tmux sessions
func (h *Home) startSession(inst *session.Instance) tea.Cmd {
	return func() tea.Msg {
		// Start the session - this creates the tmux session
		if err := inst.Start(); err != nil {
			log.Printf("[SESSION] Failed to start session %s: %v", inst.ID, err)
			// Don't return error msg - session is already in storage as "stopped"
			// User can manually start it later
			return nil
		}

		// Return nil - no message needed, status will be updated via normal polling
		return nil
	}
}

// startForkedSession starts a forked session after it has been saved to storage
// This is called after saving to prevent orphaned tmux sessions
func (h *Home) startForkedSession(inst *session.Instance) tea.Cmd {
	return func() tea.Msg {
		// Start the session - this creates the tmux session
		if err := inst.Start(); err != nil {
			log.Printf("[FORK] Failed to start forked session %s: %v", inst.ID, err)
			// Don't return error msg - session is already in storage as "stopped"
			// User can manually start it later
			return nil
		}

		// Wait for Claude to create the new session file (fork creates new UUID)
		// Give Claude up to 5 seconds to initialize and write the session file
		// Build exclusion list from all current Claude sessions
		usedIDs := make(map[string]bool)
		h.instancesMu.RLock()
		for _, i := range h.instances {
			if i.ClaudeSessionID != "" {
				usedIDs[i.ClaudeSessionID] = true
			}
		}
		h.instancesMu.RUnlock()

		if inst.Tool == "claude" {
			_ = inst.WaitForClaudeSessionWithExclude(5*time.Second, usedIDs)
		}

		// Return nil - no message needed, status will be updated via normal polling
		return nil
	}
}

// sessionDeletedMsg signals that a session was deleted
type sessionDeletedMsg struct {
	deletedID string
	killErr   error // Error from Kill() if any
}

// deleteSession deletes a session
func (h *Home) deleteSession(inst *session.Instance) tea.Cmd {
	id := inst.ID
	return func() tea.Msg {
		killErr := inst.Kill()
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
	log.Printf("[MCP-DEBUG] restartSession() called for ID=%s, Title=%s, Tool=%s", inst.ID, inst.Title, inst.Tool)
	return func() tea.Msg {
		log.Printf("[MCP-DEBUG] restartSession() cmd executing - calling inst.Restart()")
		err := inst.Restart()
		log.Printf("[MCP-DEBUG] restartSession() inst.Restart() returned err=%v", err)
		return sessionRestartedMsg{sessionID: id, err: err}
	}
}

// attachSession attaches to a session using custom PTY with Ctrl+Q detection
func (h *Home) attachSession(inst *session.Instance) tea.Cmd {
	tmuxSess := inst.GetTmuxSession()
	if tmuxSess == nil {
		return nil
	}

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
				log.Printf("[SAVE-DEBUG] attachSession: Refusing to save empty instances - storage has %d bytes", info.Size())
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
	if inst.Status == session.StatusWaiting {
		tmuxSess.Acknowledge()
		log.Printf("[STATUS] Acknowledged %s on attach (was waiting)", inst.Title)
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

		// Clear screen with synchronized output for atomic rendering
		fmt.Print(syncOutputBegin + clearScreen + syncOutputEnd)

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
	return loadSessionsMsg{instances: instancesCopy}
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
		switch inst.Status {
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

// countSessionTools counts sessions by tool type (agent vs shell)
func (h *Home) countSessionTools() (agents, shells int) {
	h.instancesMu.RLock()
	defer h.instancesMu.RUnlock()
	for _, inst := range h.instances {
		if inst.IsAgent() {
			agents++
		} else {
			shells++
		}
	}
	return
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

	// Tool filter pills (separator + AI + Shell)
	agents, shells := h.countSessionTools()
	if shells > 0 || agents > 0 {
		// Separator
		pills = append(pills, lipgloss.NewStyle().Foreground(ColorBorder).Render("|"))

		// AI agents pill (cyan)
		aiLabel := fmt.Sprintf("AI %d", agents)
		if h.toolFilter == session.ToolFilterAgents {
			pills = append(pills, lipgloss.NewStyle().
				Foreground(ColorBg).
				Background(ColorCyan).
				Bold(true).
				Padding(0, 1).Render(aiLabel))
		} else if agents > 0 {
			pills = append(pills, lipgloss.NewStyle().
				Foreground(ColorCyan).
				Background(ColorSurface).
				Padding(0, 1).Render(aiLabel))
		} else {
			pills = append(pills, dimPillStyle.Render(aiLabel))
		}

		// Shell pill (purple)
		shellLabel := fmt.Sprintf("$ %d", shells)
		if h.toolFilter == session.ToolFilterShells {
			pills = append(pills, lipgloss.NewStyle().
				Foreground(ColorBg).
				Background(ColorPurple).
				Bold(true).
				Padding(0, 1).Render(shellLabel))
		} else if shells > 0 {
			pills = append(pills, lipgloss.NewStyle().
				Foreground(ColorPurple).
				Background(ColorSurface).
				Padding(0, 1).Render(shellLabel))
		} else {
			pills = append(pills, dimPillStyle.Render(shellLabel))
		}
	}

	// Hint for keyboard shortcuts (shift+number to filter, 0 to clear)
	hintStyle := lipgloss.NewStyle().Foreground(ColorComment).Faint(true)
	hint := hintStyle.Render("  !@#$ status • % tool • 0 all")

	// Join pills with spaces
	filterRow := strings.Join(pills, " ") + hint

	return lipgloss.NewStyle().
		Width(h.width).
		Padding(0, 1).
		Render(filterRow)
}

// updateSizes updates component sizes
func (h *Home) updateSizes() {
	h.search.SetSize(h.width, h.height)
	h.newDialog.SetSize(h.width, h.height)
	h.groupDialog.SetSize(h.width, h.height)
	h.confirmDialog.SetSize(h.width, h.height)
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

	// Right side: hostname and version (subtle inline style - no border to keep single line)
	rightStyle := lipgloss.NewStyle().
		Foreground(ColorComment).
		Faint(true)
	headerRight := rightStyle.Render(Hostname + " · v" + Version)

	// Fill remaining header space
	headerLeft := lipgloss.JoinHorizontal(lipgloss.Left, logo, "  ", title, "  ", stats)
	headerPadding := h.width - lipgloss.Width(headerLeft) - lipgloss.Width(headerRight) - 2
	if headerPadding < 1 {
		headerPadding = 1
	}
	headerContent := headerLeft + strings.Repeat(" ", headerPadding) + headerRight

	headerBar := lipgloss.NewStyle().
		Background(ColorSurface).
		Width(h.width).
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
			Width(h.width).
			Align(lipgloss.Center)
		updateText := fmt.Sprintf(" ⬆ Update available: v%s → v%s (run: agent-deck update) ",
			h.updateInfo.CurrentVersion, h.updateInfo.LatestVersion)
		b.WriteString(updateStyle.Render(updateText))
		b.WriteString("\n")
	}

	// ═══════════════════════════════════════════════════════════════════
	// MAIN CONTENT AREA - Responsive layout based on terminal width
	// ═══════════════════════════════════════════════════════════════════
	helpBarHeight := 2 // Help bar takes 2 lines (border + content)
	// Height breakdown: -1 header, -filterBarHeight filter, -updateBannerHeight banner, -helpBarHeight help
	contentHeight := h.height - 1 - helpBarHeight - updateBannerHeight - filterBarHeight

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

	// CRITICAL: Use ensureExactHeight for robust, consistent output across all platforms
	// This is the single source of truth for output height - guarantees exactly h.height lines
	// regardless of component content, ANSI codes, or terminal differences
	result := ensureExactHeight(b.String(), h.height)

	// Apply width constraint via lipgloss (width handling is reliable)
	return lipgloss.NewStyle().
		Width(h.width).
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
// Behavior:
//   - Strips ANSI codes to measure true visual width
//   - Pads short lines with spaces to reach target width
//   - Truncates long lines with "..." suffix
//   - Preserves ANSI styling where possible
//
// This fixes the "bleeding" issue where right panel content appears in left panel
// due to inconsistent line widths causing JoinHorizontal misalignment.
func ensureExactWidth(content string, width int) string {
	if width <= 0 {
		return content
	}

	lines := strings.Split(content, "\n")
	result := make([]string, len(lines))

	for i, line := range lines {
		// Measure visual width (excluding ANSI codes)
		cleanLine := tmux.StripANSI(line)
		displayWidth := runewidth.StringWidth(cleanLine)

		if displayWidth == width {
			// Already correct width
			result[i] = line
		} else if displayWidth < width {
			// Pad with spaces to reach target width
			padding := width - displayWidth
			result[i] = line + strings.Repeat(" ", padding)
		} else {
			// Line too wide - truncate the clean version
			// Note: This loses ANSI styling but prevents layout corruption
			truncated := runewidth.Truncate(cleanLine, width-3, "...")
			// Pad if truncation made it shorter
			truncWidth := runewidth.StringWidth(truncated)
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

	return lipgloss.JoinVertical(lipgloss.Left, border, content)
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
		contextKeys = keyStyle.Render("n") + " " + keyStyle.Render("i") + " " + keyStyle.Render("g")
	} else if h.cursor < len(h.flatItems) {
		item := h.flatItems[h.cursor]
		if item.Type == session.ItemTypeGroup {
			contextKeys = keyStyle.Render("⏎") + " " + keyStyle.Render("n") + " " + keyStyle.Render("g")
		} else {
			contextKeys = keyStyle.Render("⏎") + " " + keyStyle.Render("n") + " " + keyStyle.Render("R")
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
		padding = 2
	}

	content := leftPart + sep + strings.Repeat(" ", padding) + rightPart

	return lipgloss.JoinVertical(lipgloss.Left, border, content)
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
			h.helpKeyShort("n", "New"),
			h.helpKeyShort("i", "Import"),
		}
	} else if h.cursor < len(h.flatItems) {
		item := h.flatItems[h.cursor]
		if item.Type == session.ItemTypeGroup {
			contextHints = []string{
				h.helpKeyShort("⏎", "Toggle"),
				h.helpKeyShort("n", "New"),
			}
		} else {
			contextHints = []string{
				h.helpKeyShort("⏎", "Attach"),
				h.helpKeyShort("n", "New"),
				h.helpKeyShort("R", "Restart"),
			}
			if item.Session != nil && item.Session.CanFork() {
				contextHints = append(contextHints, h.helpKeyShort("f", "Fork"))
			}
			if item.Session != nil && (item.Session.Tool == "claude" || item.Session.Tool == "gemini") {
				contextHints = append(contextHints, h.helpKeyShort("M", "MCP"))
				contextHints = append(contextHints, h.helpKeyShort("v", h.previewModeShort()))
			}
		}
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
		padding = 2
	}

	content := leftPart + sep + strings.Repeat(" ", padding) + rightPart

	return lipgloss.JoinVertical(lipgloss.Left, border, content)
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
			h.helpKey("n", "New"),
			h.helpKey("i", "Import"),
			h.helpKey("g", "Group"),
		}
	} else if h.cursor < len(h.flatItems) {
		item := h.flatItems[h.cursor]
		if item.Type == session.ItemTypeGroup {
			contextTitle = "Group"
			primaryHints = []string{
				h.helpKey("Tab", "Toggle"),
				h.helpKey("n", "New"),
				h.helpKey("g", "Subgroup"),
			}
			secondaryHints = []string{
				h.helpKey("r", "Rename"),
				h.helpKey("d", "Delete"),
			}
		} else {
			contextTitle = "Session"
			primaryHints = []string{
				h.helpKey("Enter", "Attach"),
				h.helpKey("n", "New"),
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
			secondaryHints = []string{
				h.helpKey("r", "Rename"),
				h.helpKey("m", "Move"),
				h.helpKey("d", "Delete"),
			}
		}
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

	// Status indicators (reload, remote discovery)
	var statusIndicators []string
	indicatorStyle := lipgloss.NewStyle().
		Foreground(ColorYellow).
		Bold(true)
	if h.isReloading {
		statusIndicators = append(statusIndicators, indicatorStyle.Render("⟳ Reloading..."))
	}
	if h.remoteDiscoveryRunning.Load() {
		statusIndicators = append(statusIndicators, indicatorStyle.Render("⟳ Scanning remote..."))
	}

	// Global shortcuts (right side) - more compact with separators
	globalStyle := lipgloss.NewStyle().Foreground(ColorComment)
	globalHints := globalStyle.Render("↑↓ Nav") + sep +
		globalStyle.Render("/ Search  G Global") + sep +
		globalStyle.Render("? Help  q Quit")

	// Calculate spacing between left (context) and right (global) portions
	leftPart := contextLabel + " " + shortcutsLine
	if len(statusIndicators) > 0 {
		leftPart = strings.Join(statusIndicators, sep) + sep + leftPart
	}
	rightPart := globalHints
	padding := h.width - lipgloss.Width(leftPart) - lipgloss.Width(rightPart) - spacingNormal
	if padding < spacingNormal {
		padding = spacingNormal
	}

	helpContent := leftPart + strings.Repeat(" ", padding) + rightPart

	return lipgloss.JoinVertical(lipgloss.Left, border, helpContent)
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
	switch inst.Status {
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
	switch inst.Status {
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
	toolStyle := GetToolStyle(inst.Tool)

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
	tool := toolStyle.Render(" " + inst.Tool)

	// YOLO badge for Gemini sessions with YOLO mode enabled
	yoloBadge := ""
	if inst.Tool == "gemini" && inst.GeminiYoloMode != nil && *inst.GeminiYoloMode {
		yoloStyle := lipgloss.NewStyle().Foreground(ColorYellow).Bold(true)
		if selected {
			yoloStyle = SessionStatusSelStyle
		}
		yoloBadge = yoloStyle.Render(" [YOLO]")
	}

	// Build row: [baseIndent][selection][tree][status] [title] [tool] [yolo]
	// Format: " ├─ ● session-name tool" or "▶└─ ● session-name tool"
	// Sub-sessions get extra indent: "   ├─◐ sub-session tool"
	row := fmt.Sprintf("%s%s%s %s %s%s%s", baseIndent, selectionPrefix, treeStyle.Render(treeConnector), status, title, tool, yoloBadge)
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

	// Header with tool icon
	icon := ToolIcon(inst.Tool)
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

	// Host (for remote sessions)
	if inst.IsRemote() {
		b.WriteString(fmt.Sprintf("%s %s\n", labelStyle.Render("Host:"), valueStyle.Render(inst.GetHostID())))
	}

	// Path
	b.WriteString(fmt.Sprintf("%s %s\n", labelStyle.Render("Path:"), valueStyle.Render(inst.ProjectPath)))

	// Status with color
	var statusColor lipgloss.Color
	switch inst.Status {
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
	b.WriteString(fmt.Sprintf("%s %s\n", labelStyle.Render("Status:"), statusStyle.Render(string(inst.Status))))

	// Tool
	b.WriteString(fmt.Sprintf("%s %s\n", labelStyle.Render("Tool:"), valueStyle.Render(inst.Tool)))

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
		log.Printf("[OPENCODE-UI] Rendering preview for %s: OpenCodeSessionID=%q", selected.Title, selected.OpenCodeSessionID)

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
