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
	PreviewModeBoth     PreviewMode = iota // Show both analytics and output (default)
	PreviewModeOutput                      // Show output only (content preview)
	PreviewModeAnalytics                   // Show analytics only
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
	search        *Search
	globalSearch  *GlobalSearch              // Global session search across all Claude conversations
	globalSearchIndex *session.GlobalSearchIndex // Search index (nil if disabled)
	newDialog     *NewDialog
	groupDialog   *GroupDialog   // For creating/renaming groups
	forkDialog    *ForkDialog    // For forking sessions
	confirmDialog *ConfirmDialog // For confirming destructive actions
	helpOverlay   *HelpOverlay   // For showing keyboard shortcuts
	mcpDialog      *MCPDialog      // For managing MCPs
	geminiModelDialog *GeminiModelDialog // For changing Gemini models
	setupWizard    *SetupWizard    // For first-run setup
	
	settingsPanel  *SettingsPanel  // For editing settings
	analyticsPanel *AnalyticsPanel // For displaying session analytics

	// Analytics cache (async fetching with TTL)
	currentAnalytics       *session.SessionAnalytics             // Current analytics for selected session (Claude)
	currentGeminiAnalytics *session.GeminiSessionAnalytics       // Current analytics for selected session (Gemini)
	analyticsSessionID     string                                // Session ID for current analytics
	analyticsFetchingID    string                                // ID currently being fetched (prevents duplicates)
	analyticsCache         map[string]*session.SessionAnalytics  // TTL cache: sessionID -> analytics (Claude)
	geminiAnalyticsCache   map[string]*session.GeminiSessionAnalytics // TTL cache: sessionID -> analytics (Gemini)
	analyticsCacheTime     map[string]time.Time                  // TTL cache: sessionID -> cache timestamp

	// State
	cursor        int            // Selected item index in flatItems
	viewOffset    int            // First visible item index (for scrolling)
	isAttaching   atomic.Bool   // Prevents View() output during attach (fixes Bubble Tea Issue #431) - atomic for thread safety
	statusFilter  session.Status // Filter sessions by status ("" = all, or specific status)
	previewMode   PreviewMode    // What to show in preview pane (both, output-only, analytics-only)
	err           error
	errTime       time.Time // When error occurred (for auto-dismiss)
	isReloading    bool      // Visual feedback during auto-reload
	initialLoading bool      // True until first loadSessionsMsg received (shows splash screen)
	reloadVersion  uint64    // Incremented on each reload to prevent stale background saves
	reloadMu       sync.Mutex // Protects reloadVersion and isReloading for thread-safe access

	// Preview cache (async fetching - View() must be pure, no blocking I/O)
	previewCache       map[string]string    // sessionID -> cached preview content
	previewCacheTime   map[string]time.Time // sessionID -> when cached (for expiration)
	previewCacheMu     sync.RWMutex         // Protects previewCache for thread-safety
	previewFetchingID  string               // ID currently being fetched (prevents duplicate fetches)

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

type updateCheckMsg struct {
	info *update.UpdateInfo
}

type tickMsg time.Time

// modelsFetchedMsg is sent when available Gemini models are fetched
type modelsFetchedMsg struct {
        models []string
        err    error
}

// modelSelectedMsg is sent when a Gemini model is selected
type modelSelectedMsg struct {
        sessionID string
        model     string
}

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
	viewOffset    int   // Current scroll position
	visibleHeight int   // How many items fit on screen
	flatItemIDs   []string // IDs of sessions in current flatItems order (for visible detection)
}

// NewHome creates a new home model with the default profile
func NewHome() *Home {
	return NewHomeWithProfile("")
}

// NewHomeWithProfile creates a new home model with the specified profile
func NewHomeWithProfile(profile string) *Home {
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
		profile:           actualProfile,
		storage:           storage,
		storageWarning:    storageWarning,
		search:            NewSearch(),
		newDialog:         NewNewDialog(),
		groupDialog:       NewGroupDialog(),
		forkDialog:        NewForkDialog(),
		confirmDialog:     NewConfirmDialog(),
		helpOverlay:       NewHelpOverlay(),
		mcpDialog:         NewMCPDialog(),
		geminiModelDialog: NewGeminiModelDialog(),
		setupWizard:       NewSetupWizard(),
		
		settingsPanel:     NewSettingsPanel(),
		analyticsPanel:    NewAnalyticsPanel(),
		cursor:            0,
		initialLoading:    true, // Show splash until sessions load
		ctx:               ctx,
		cancel:            cancel,
		instances:         []*session.Instance{},
		instanceByID:      make(map[string]*session.Instance),
		groupTree:         session.NewGroupTree([]*session.Instance{}),
		flatItems:         []session.Item{},
		previewCache:         make(map[string]string),
		previewCacheTime:     make(map[string]time.Time),
		analyticsCache:       make(map[string]*session.SessionAnalytics),
		geminiAnalyticsCache: make(map[string]*session.GeminiSessionAnalytics),
		analyticsCacheTime:   make(map[string]time.Time),
		launchingSessions:    make(map[string]time.Time),
		resumingSessions:   make(map[string]time.Time),
		mcpLoadingSessions: make(map[string]time.Time),
		forkingSessions:    make(map[string]time.Time),
		lastLogActivity:    make(map[string]time.Time),
		statusTrigger:     make(chan statusUpdateRequest, 1), // Buffered to avoid blocking
		statusWorkerDone:  make(chan struct{}),
		boundKeys:         make(map[string]string),
	}

	// Initialize notification manager if enabled in config
	notifSettings := session.GetNotificationsSettings()
	if notifSettings.Enabled {
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

	// Update status for all instances (background can be more thorough)
	statusChanged := false
	for _, inst := range instances {
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
	if signalSessionID := tmux.ReadAndClearAckSignal(); signalSessionID != "" {
		sessionToAcknowledgeID = signalSessionID
		if debug {
			log.Printf("[NOTIF-BG] Signal file found: %s", signalSessionID)
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
			// Trigger OpenCode session detection for restored sessions without IDs
			for _, inst := range h.instances {
				if inst.Tool == "opencode" && inst.OpenCodeSessionID == "" {
					go inst.DetectOpenCodeSession()
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
				return h, h.fetchPreview(selected)
			}
		}
		return h, nil

	case modelsFetchedMsg:
		if h.geminiModelDialog != nil {
			var cmd tea.Cmd
			h.geminiModelDialog, cmd = h.geminiModelDialog.Update(msg)
			return h, cmd
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

			// Save both instances AND groups (critical fix: was losing groups!)
			h.saveInstances()

			// Start fetching preview for the new session
			return h, h.fetchPreview(msg.instance)
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

			// Save both instances AND groups
			h.saveInstances()

			// Start fetching preview for the forked session
			return h, h.fetchPreview(msg.instance)
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

	case modelSelectedMsg:
		log.Printf("[MODEL-DEBUG] modelSelectedMsg received: sessionID=%s, model=%s", msg.sessionID, msg.model)
		if inst := h.getInstanceByID(msg.sessionID); inst != nil {
			// Immediate UI update
			inst.GeminiModel = msg.model
			log.Printf("[MODEL-DEBUG] Updated instance %s with model %s", inst.ID, inst.GeminiModel)
			
			h.resumingSessions[inst.ID] = time.Now()
			return h, func() tea.Msg {
				err := inst.SetGeminiModel(msg.model)
				return sessionRestartedMsg{sessionID: msg.sessionID, err: err}
			}
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
		if h.geminiModelDialog.IsVisible() {
			var cmd tea.Cmd
			h.geminiModelDialog, cmd = h.geminiModelDialog.Update(msg)
			return h, cmd
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

	case "ctrl+g":
		// Gemini Model Manager
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeSession && item.Session != nil && item.Session.Tool == "gemini" {
				h.geminiModelDialog.SetSize(h.width, h.height)
				return h, h.geminiModelDialog.Show(item.Session.ID)
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
		// Wait for background worker to finish (prevents race on shutdown)
		<-h.statusWorkerDone

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
	// Skip saving during reload to avoid overwriting external changes (CLI)
	if h.isReloading {
		return
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

		log.Printf("[SAVE-DEBUG] Saving %d instances to profile %s (path=%s)", instanceCount, h.profile, h.storage.Path())

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

		// Apply Claude options if provided
		if tool == "claude" && claudeOpts != nil {
			if err := inst.SetClaudeOptions(claudeOpts); err != nil {
				return sessionCreatedMsg{err: fmt.Errorf("failed to set Claude options: %w", err)}
			}
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

		// Use CreateForkedInstanceWithOptions to get the proper fork command with options
		inst, _, err := source.CreateForkedInstanceWithOptions(title, groupPath, opts)
		if err != nil {
			return sessionForkedMsg{err: fmt.Errorf("cannot create forked instance: %w", err), sourceID: sourceID}
		}

		// Start the forked session
		if err := inst.Start(); err != nil {
			return sessionForkedMsg{err: err, sourceID: sourceID}
		}

		// Wait for Claude to create the new session file (fork creates new UUID)
		// Give Claude up to 5 seconds to initialize and write the session file
		// Pass usedIDs to prevent detecting an already-claimed session
		if inst.Tool == "claude" {
			_ = inst.WaitForClaudeSessionWithExclude(5*time.Second, usedIDs)
		}

		return sessionForkedMsg{instance: inst, sourceID: sourceID}
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

	return loadSessionsMsg{instances: instancesCopy, err: nil}
}

// updateSizes calculates layout dimensions based on current terminal size
// PERFORMANCE: Called only on terminal resize, not on every View() call
func (h *Home) updateSizes() {
	// Update dialog sizes
	h.newDialog.SetSize(h.width, h.height)
	h.groupDialog.SetSize(h.width, h.height)
	h.forkDialog.SetSize(h.width, h.height)
	h.confirmDialog.SetSize(h.width, h.height)
	h.helpOverlay.SetSize(h.width, h.height)
	h.mcpDialog.SetSize(h.width, h.height)
	h.geminiModelDialog.SetSize(h.width, h.height)
	h.settingsPanel.SetSize(h.width, h.height)
	h.analyticsPanel.SetSize(h.width, h.height)
	h.globalSearch.SetSize(h.width, h.height)
}

// renderHeader renders the top title bar
// Shows real-time status indicators in the logo reflecting the whole session list
func (h *Home) renderHeader() string {
	// Get counts from cache (non-blocking)
	counts := h.getStatusCounts()

	logo := RenderLogoCompact(counts.running, counts.waiting, counts.idle)
	title := TitleStyle.Render("Agent Deck")
	version := DimStyle.Render(" v" + Version)

	// Filter indicator (shown when shift+1..4 filters are active)
	filterStr := ""
	if h.statusFilter != "" {
		filterLabel := strings.ToUpper(string(h.statusFilter))
		filterStr = lipgloss.NewStyle().
			Foreground(ColorBg).
			Background(ColorYellow).
			Bold(true).
			Padding(0, 1).
			MarginLeft(2).
			Render("FILTER: " + filterLabel)
	}

	// Profile indicator (shown when not using default profile)
	profileStr := ""
	if h.profile != session.DefaultProfile && h.profile != "" {
		profileStr = lipgloss.NewStyle().
			Foreground(ColorBg).
			Background(ColorCyan).
			Bold(true).
			Padding(0, 1).
			MarginLeft(1).
			Render("PROFILE: " + strings.ToUpper(h.profile))
	}

	// Double-ESC hint (shown briefly when ESC pressed once)
	escHint := ""
	if !h.lastEscTime.IsZero() && time.Since(h.lastEscTime) < 500*time.Millisecond {
		escHint = lipgloss.NewStyle().
			Foreground(ColorOrange).
			Italic(true).
			MarginLeft(2).
			Render("Press ESC again to quit")
	}

	// Visual reload indicator (top right)
	reloadIndicator := ""
	if h.isReloading {
		reloadIndicator = lipgloss.NewStyle().
			Foreground(ColorGreen).
			Bold(true).
			Render(" ↻ RELOADING")
	}

	// Summary counts (right side)
	summary := ""
	if counts.running > 0 {
		summary += RunningStyle.Render(fmt.Sprintf(" %d running", counts.running))
	}
	if counts.waiting > 0 {
		summary += " " + WaitingStyle.Render(fmt.Sprintf(" %d waiting", counts.waiting))
	}

	header := logo + " " + title + version + filterStr + profileStr + escHint
	rightSide := summary + reloadIndicator

	// Calculate space between left and right side
	headerWidth := lipgloss.Width(header)
	rightWidth := lipgloss.Width(rightSide)
	padding := h.width - headerWidth - rightWidth
	if padding < 1 {
		padding = 1
	}

	return header + strings.Repeat(" ", padding) + rightSide
}

// renderFilterBar renders the persistent status filter bar
// Provides visual feedback for Shift+1..4 filtering and status meanings
func (h *Home) renderFilterBar(width int) string {
	// Status color definitions (Tokyo Night based)
	const (
		ColorRunning = "#9ece6a" // Tokyo Night Green
		ColorWaiting = "#e0af68" // Tokyo Night Yellow
		ColorIdle    = "#787fa0" // Tokyo Night Dim Text
		ColorError   = "#f7768e" // Tokyo Night Red
	)

	// Base style for the whole bar
	barStyle := lipgloss.NewStyle().
		Width(width).
		Height(1).
		Padding(0, 1)

	// Style for each filter option
	itemStyle := lipgloss.NewStyle().MarginRight(2)
	activeStyle := itemStyle.Copy().Bold(true).Underline(true)

	renderOption := func(key, label string, color string, status session.Status) string {
		style := itemStyle
		if h.statusFilter == status {
			style = activeStyle
		}
		// Create colored circle and gray text
		circle := lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render("●")
		keyStr := lipgloss.NewStyle().Foreground(ColorComment).Render(key)
		labelStr := lipgloss.NewStyle().Foreground(ColorComment).Render(label)

		return style.Render(fmt.Sprintf("%s %s %s", keyStr, circle, labelStr))
	}

	options := []string{
		renderOption("! Running", "Running", ColorRunning, session.StatusRunning),
		renderOption("@ Waiting", "Needs Input", ColorWaiting, session.StatusWaiting),
		renderOption("# Idle", "Inactive", ColorIdle, session.StatusIdle),
		renderOption("$ Error", "Error", ColorError, session.StatusError),
		renderOption("0 All", "Clear Filter", "", ""),
	}

	content := strings.Join(options, "")
	return barStyle.Render(content)
}

// renderHelpBar renders the bottom legend with keyboard shortcuts
func (h *Home) renderHelpBar() string {
	var sections []string

	// Section 1: Navigation
	sections = append(sections, MenuKey("↑↓", "Nav"))

	// Section 2: Actions (Adaptive based on selection)
	selected := h.getSelectedSession()
	if selected != nil {
		sections = append(sections, MenuKey("Enter", "Attach"))
		if selected.Tool == "claude" || selected.Tool == "gemini" {
			sections = append(sections, MenuKey("Shift+M", "MCPs"))
		}
		if selected.Tool == "gemini" {
			sections = append(sections, MenuKey("Ctrl+G", "Model"))
			sections = append(sections, MenuKey("y", "YOLO"))
		}
		sections = append(sections, MenuKey("v", "Mode"))
		sections = append(sections, MenuKey("R", "Restart"))
	} else {
		// Group selected
		sections = append(sections, MenuKey("Enter", "Toggle"))
	}

	// Section 3: Management
	sections = append(sections, MenuKey("n", "New"))
	sections = append(sections, MenuKey("g", "Group"))
	sections = append(sections, MenuKey("R", "Rename"))
	sections = append(sections, MenuKey("m", "Move"))
	sections = append(sections, MenuKey("d", "Delete"))

	// Section 4: Global
	sections = append(sections, MenuKey("/", "Search"))
	sections = append(sections, MenuKey("S", "Settings"))
	sections = append(sections, MenuKey("?", "Help"))
	sections = append(sections, MenuKey("q", "Quit"))

	// Join with subtle separator
	content := strings.Join(sections, "  ")

	// Apply border and padding
	return lipgloss.NewStyle().
		Width(h.width).
		Border(lipgloss.NormalBorder(), true, false, false, false).
		BorderTop(true).
		BorderForeground(ColorBorder).
		Padding(0, 1).
		Render(content)
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

	toolLabel := inst.Tool
	if inst.Tool == "gemini" {
		model := inst.GeminiModel
		if model == "" {
			model = "auto"
		}

		// Strip redundant gemini- prefix (e.g., gemini-2.0-flash -> 2.0-flash)
		displayModel := strings.TrimPrefix(model, "gemini-")

		// Handle "auto" with detected model (e.g. auto(2.0-flash))
		if model == "auto" && inst.GeminiAnalytics != nil && inst.GeminiAnalytics.Model != "" {
			shortenedDetected := strings.TrimPrefix(inst.GeminiAnalytics.Model, "gemini-")
			toolLabel = fmt.Sprintf("auto(%s)", shortenedDetected)
		} else {
			// Just the model name (without gemini- prefix)
			toolLabel = displayModel
		}
	}

	tool := toolStyle.Render(" " + toolLabel)

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

// renderPreviewPane renders the right panel with terminal preview or analytics
// Context-aware: shows different content based on previewMode and tool type
func (h *Home) renderPreviewPane(width, height int) string {
	if width < 10 || height < 5 {
		return ""
	}

	selected := h.getSelectedSession()
	if selected == nil {
		// Empty state logo
		counts := h.getStatusCounts()
		logo := RenderLogoLarge(counts.running, counts.waiting, counts.idle)
		centerStyle := lipgloss.NewStyle().
			Width(width).
			Height(height).
			Align(lipgloss.Center, lipgloss.Center)
		return centerStyle.Render(logo)
	}

	// Check if this session has an active launch animation
	if h.hasActiveAnimation(selected.ID) {
		var startTime time.Time
		if t, ok := h.launchingSessions[selected.ID]; ok {
			startTime = t
		} else if t, ok := h.resumingSessions[selected.ID]; ok {
			startTime = t
		} else if t, ok := h.mcpLoadingSessions[selected.ID]; ok {
			startTime = t
		} else if t, ok := h.forkingSessions[selected.ID]; ok {
			// Special handling for Forking animation (uses separate component)
			return h.renderForkingState(selected, width, t)
		}
		return h.renderLaunchingState(selected, width, startTime)
	}

	// Content layout depends on previewMode
	switch h.previewMode {
	case PreviewModeAnalytics:
		return h.renderAnalyticsView(selected, width, height)
	case PreviewModeOutput:
		return h.renderOutputView(selected, width, height)
	default: // PreviewModeBoth
		// Split pane: analytics on top (40%), output on bottom (60%)
		analyticsHeight := (height * 40) / 100
		if analyticsHeight < 8 {
			analyticsHeight = 8
		}
		outputHeight := height - analyticsHeight - 1 // -1 for separator

		analytics := h.renderAnalyticsView(selected, width, analyticsHeight)
		separator := lipgloss.NewStyle().
			Foreground(ColorBorder).
			Width(width).
			Render(strings.Repeat("─", width))
		output := h.renderOutputView(selected, width, outputHeight)

		return analytics + "\n" + separator + "\n" + output
	}
}

// renderAnalyticsView renders the analytics section for a session
func (h *Home) renderAnalyticsView(inst *session.Instance, width, height int) string {
	if inst == nil || (inst.Tool != "claude" && inst.Tool != "gemini") {
		// Not a supported tool for analytics
		centerStyle := lipgloss.NewStyle().
			Width(width).
			Height(height).
			Align(lipgloss.Center, lipgloss.Center).
			Foreground(ColorComment).
			Italic(true)
		return centerStyle.Render("(analytics not available for this tool)")
	}

	// Check user configuration: analytics can be disabled globally
	config, _ := session.LoadUserConfig()
	if config != nil && !config.GetShowAnalytics() {
		centerStyle := lipgloss.NewStyle().
			Width(width).
			Height(height).
			Align(lipgloss.Center, lipgloss.Center).
			Foreground(ColorComment).
			Italic(true)
		return centerStyle.Render("(analytics disabled in settings)")
	}

	// Use AnalyticsPanel component
	h.analyticsPanel.SetSize(width, height)
	return h.analyticsPanel.View()
}

// renderOutputView renders the terminal output section
func (h *Home) renderOutputView(inst *session.Instance, width, height int) string {
	h.previewCacheMu.RLock()
	content, ok := h.previewCache[inst.ID]
	h.previewCacheMu.RUnlock()

	if !ok || content == "" {
		centerStyle := lipgloss.NewStyle().
			Width(width).
			Height(height).
			Align(lipgloss.Center, lipgloss.Center).
			Foreground(ColorComment).
			Italic(true)
		return centerStyle.Render("(terminal is empty)")
	}

	// Process content: strip ANSI, wrap lines, crop to height
	lines := strings.Split(content, "\n")

	// Filter out empty lines at the end
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}

	// Only show last 'height' lines
	if len(lines) > height {
		lines = lines[len(lines)-height:]
	}

	// Style lines
	style := lipgloss.NewStyle().Foreground(ColorText)
	var sb strings.Builder
	for i, line := range lines {
		// Truncate to width if needed (responsive fix)
		if len(line) > width {
			line = line[:width-1] + "…"
		}
		sb.WriteString(style.Render(line))
		if i < len(lines)-1 {
			sb.WriteString("\n")
		}
	}

	return sb.String()
}

// renderForkingState renders the animated forking indicator
func (h *Home) renderForkingState(inst *session.Instance, width int, startTime time.Time) string {
	var b strings.Builder

	// Animation frame
	spinnerFrames := []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"}
	spinner := spinnerFrames[h.animationFrame]

	// Centered layout
	centerStyle := lipgloss.NewStyle().
		Width(width - 4).
		Align(lipgloss.Center)

	// Spinner with fork color (orange)
	spinnerStyle := lipgloss.NewStyle().
		Foreground(ColorOrange).
		Bold(true)
	b.WriteString(centerStyle.Render(spinnerStyle.Render(spinner + "  FORKING SESSION  " + spinner)))
	b.WriteString("\n\n")

	// Title with emoji
	titleStyle := lipgloss.NewStyle().
		Foreground(ColorPurple).
		Bold(true)
	b.WriteString(centerStyle.Render(titleStyle.Render("🍴 Forking " + inst.Title)))
	b.WriteString("\n\n")

	// Description
	descStyle := lipgloss.NewStyle().
		Foreground(ColorText).
		Italic(true)
	b.WriteString(centerStyle.Render(descStyle.Render("Capturing conversation context...")))
	b.WriteString("\n\n")

	// Elapsed time
	elapsed := time.Since(startTime).Round(time.Second)
	timeStyle := lipgloss.NewStyle().
		Foreground(ColorYellow).
		Italic(true)
	b.WriteString(centerStyle.Render(timeStyle.Render(fmt.Sprintf("%s", elapsed))))

	return b.String()
}

// ensureExactHeight ensures the string has exactly the specified number of lines
// by either truncating or padding with blank lines.
func ensureExactHeight(s string, height int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > height {
		return strings.Join(lines[:height], "\n")
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// renderDualColumnLayout renders the classic side-by-side view (80+ columns)
func (h *Home) renderDualColumnLayout(contentWidth, contentHeight int) string {
	// 35/65 split for side-by-side view
	leftWidth := (contentWidth * 35) / 100
	if leftWidth < 30 {
		leftWidth = 30
	}
	rightWidth := contentWidth - leftWidth - 1 // -1 for separator

	// Left panel (Session List)
	listTitle := lipgloss.NewStyle().
		Foreground(ColorCyan).
		Bold(true).
		Width(leftWidth).
		Render(" SESSIONS")
	listUnderline := lipgloss.NewStyle().
		Foreground(ColorBorder).
		Render(strings.Repeat("─", leftWidth))

	// renderSessionList handles scrolling internally using viewOffset and height
	panelContentHeight := contentHeight - 2 // -2 for title + underline
	listContent := h.renderSessionList(leftWidth, panelContentHeight)
	listContent = ensureExactHeight(listContent, panelContentHeight)

	leftPanel := listTitle + "\n" + listUnderline + "\n" + listContent

	// Right panel (Preview/Analytics)
	previewTitle := lipgloss.NewStyle().
		Foreground(ColorCyan).
		Bold(true).
		Width(rightWidth).
		Render(" PREVIEW")
	previewUnderline := lipgloss.NewStyle().
		Foreground(ColorBorder).
		Render(strings.Repeat("─", rightWidth))

	previewContent := h.renderPreviewPane(rightWidth, contentHeight-2)
	previewContent = ensureExactHeight(previewContent, contentHeight-2)

	rightPanel := previewTitle + "\n" + previewUnderline + "\n" + previewContent

	// Vertical separator
	separator := lipgloss.NewStyle().
		Foreground(ColorBorder).
		Height(contentHeight).
		Render("│")

	return lipgloss.JoinHorizontal(lipgloss.Top, leftPanel, separator, rightPanel)
}

// renderStackedLayout renders list on top, preview on bottom (50-79 columns)
func (h *Home) renderStackedLayout(contentWidth, contentHeight int) string {
	// 60/40 split vertically
	listHeight := (contentHeight * 60) / 100
	if listHeight < 5 {
		listHeight = 5
	}
	previewHeight := contentHeight - listHeight - 1 // -1 for separator

	// Session List (Top)
	listTitle := lipgloss.NewStyle().
		Foreground(ColorCyan).
		Bold(true).
		Width(contentWidth).
		Render(" SESSIONS")
	listContent := h.renderSessionList(contentWidth, listHeight-2) // -2 for title
	listContent = ensureExactHeight(listContent, listHeight-2)
	topPanel := listTitle + "\n" + lipgloss.NewStyle().Foreground(ColorBorder).Render(strings.Repeat("─", contentWidth)) + "\n" + listContent

	// Horizontal Separator
	separator := lipgloss.NewStyle().
		Foreground(ColorBorder).
		Width(contentWidth).
		Render(strings.Repeat("━", contentWidth))

	// Preview (Bottom)
	previewContent := h.renderPreviewPane(contentWidth, previewHeight)
	previewContent = ensureExactHeight(previewContent, previewHeight)
	bottomPanel := previewContent

	return topPanel + "\n" + separator + "\n" + bottomPanel
}

// renderSingleColumnLayout renders list only (<50 columns)
func (h *Home) renderSingleColumnLayout(contentWidth, contentHeight int) string {
	listTitle := lipgloss.NewStyle().
		Foreground(ColorCyan).
		Bold(true).
		Width(contentWidth).
		Render(" SESSIONS")
	listUnderline := lipgloss.NewStyle().
		Foreground(ColorBorder).
		Render(strings.Repeat("─", contentWidth))

	listContent := h.renderSessionList(contentWidth, contentHeight-2)
	listContent = ensureExactHeight(listContent, contentHeight-2)

	return listTitle + "\n" + listUnderline + "\n" + listContent
}

// View renders the application UI
func (h *Home) View() string {
	// Bubble Tea Issue #431: View() is called once before Update() after tea.Exec returns.
	// This causes terminal output from the attached session to be overwritten by a blank/stale
	// view for one frame, resulting in a visible flash.
	// FIX: Use an atomic flag to block View() output until Update() has cleared the flag.
	if h.isAttaching.Load() {
		return ""
	}

	if h.initialLoading {
		return h.renderSplashScreen()
	}

	// Header line (Title + Logo + Counts)
	header := h.renderHeader()

	// Persistent filter indicator bar
	filterBar := h.renderFilterBar(h.width)

	// Update notification banner (only if update available)
	updateBanner := ""
	if h.updateInfo != nil && h.updateInfo.Available {
		updateBanner = lipgloss.NewStyle().
			Width(h.width).
			Background(ColorYellow).
			Foreground(ColorBg).
			Bold(true).
			Padding(0, 1).
			Render(fmt.Sprintf("💡 Update available: v%s → v%s  (Press S for Settings to update)", h.updateInfo.CurrentVersion, h.updateInfo.LatestVersion))
	}

	// Help legend at the bottom
	helpBar := h.renderHelpBar()
	helpBarHeight := lipgloss.Height(helpBar)

	// Calculate content area height
	// Header (1) + FilterBar (1) + UpdateBanner (0 or 1) + HelpBar (2)
	contentHeight := h.height - 1 - lipgloss.Height(filterBar) - lipgloss.Height(updateBanner) - helpBarHeight

	// Error banner (if active)
	errorBanner := ""
	if h.err != nil {
		errorBanner = ErrorStyle.Render(fmt.Sprintf("✕ %v", h.err))
		// Optional: reduce contentHeight further if banner is multiline
	}

	// Main content area - responsive layout
	var mainContent string
	layoutMode := h.getLayoutMode()
	switch layoutMode {
	case LayoutModeStacked:
		mainContent = h.renderStackedLayout(h.width, contentHeight)
	case LayoutModeSingle:
		mainContent = h.renderSingleColumnLayout(h.width, contentHeight)
	default: // LayoutModeDual
		mainContent = h.renderDualColumnLayout(h.width, contentHeight)
	}

	// Compose full view
	view := header + "\n" + filterBar + "\n"
	if updateBanner != "" {
		view += updateBanner + "\n"
	}
	view += mainContent + "\n"
	if errorBanner != "" {
		view += errorBanner + "\n"
	}
	view += helpBar

	// Overlay visibility (modals)
	if h.newDialog.IsVisible() {
		view = h.newDialog.View()
	} else if h.groupDialog.IsVisible() {
		view = h.groupDialog.View()
	} else if h.forkDialog.IsVisible() {
		view = h.forkDialog.View()
	} else if h.confirmDialog.IsVisible() {
		view = h.confirmDialog.View()
	} else if h.helpOverlay.IsVisible() {
		view = h.helpOverlay.View()
	} else if h.mcpDialog.IsVisible() {
		view = h.mcpDialog.View()
	} else if h.geminiModelDialog.IsVisible() {
		view = h.geminiModelDialog.View()
	} else if h.settingsPanel.IsVisible() {
		view = h.settingsPanel.View()
	} else if h.setupWizard.IsVisible() {
		view = h.setupWizard.View()
	}

	// Performance optimization: return pre-composed string if no changes
	// (not implemented yet, but possible by caching View() result)

	return view
}

// getStatusCounts returns cached status counts or calculates them
// PERFORMANCE: Avoids iterating all sessions on every View() call
func (h *Home) getStatusCounts() struct{ running, waiting, idle, errored int } {
	// Check if cache is still valid (not invalidated and less than 1s old)
	if h.cachedStatusCounts.valid.Load() && time.Since(h.cachedStatusCounts.timestamp) < 1*time.Second {
		return struct{ running, waiting, idle, errored int }{
			running: h.cachedStatusCounts.running,
			waiting: h.cachedStatusCounts.waiting,
			idle:    h.cachedStatusCounts.idle,
			errored: h.cachedStatusCounts.errored,
		}
	}

	// Cache miss/invalid - calculate fresh counts
	var running, waiting, idle, errored int
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

	// Update cache
	h.cachedStatusCounts.running = running
	h.cachedStatusCounts.waiting = waiting
	h.cachedStatusCounts.idle = idle
	h.cachedStatusCounts.errored = errored
	h.cachedStatusCounts.valid.Store(true)
	h.cachedStatusCounts.timestamp = time.Now()

	return struct{ running, waiting, idle, errored int }{running, waiting, idle, errored}
}

// renderSplashScreen renders the initial loading screen
func (h *Home) renderSplashScreen() string {
	// Centered logo and message
	counts := h.getStatusCounts()
	logo := RenderLogoLarge(counts.running, counts.waiting, counts.idle)

	message := lipgloss.NewStyle().
		Foreground(ColorPurple).
		Bold(true).
		MarginTop(1).
		Render("AGENT DECK")

	subtitle := lipgloss.NewStyle().
		Foreground(ColorComment).
		Italic(true).
		Render("Loading sessions...")

	splash := logo + "\n" + message + "\n" + subtitle

	return lipgloss.Place(h.width, h.height, lipgloss.Center, lipgloss.Center, splash)
}