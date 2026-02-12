package tmux

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
	"golang.org/x/sync/singleflight"
)

var statusLog = logging.ForComponent(logging.CompStatus)
var respawnLog = logging.ForComponent(logging.CompSession)
var mcpLog = logging.ForComponent(logging.CompMCP)

// ErrCaptureTimeout is returned when CapturePane exceeds its timeout.
// Callers should preserve previous state rather than transitioning to error/inactive.
var ErrCaptureTimeout = errors.New("capture-pane timed out")

const SessionPrefix = "agentdeck_"

// Session cache - reduces subprocess spawns from O(n) to O(1) per tick
// Instead of calling `tmux has-session` and `tmux display-message` for each session,
// we call `tmux list-sessions` ONCE and cache both existence and activity timestamps
var (
	sessionCacheMu   sync.RWMutex
	sessionCacheData map[string]int64 // session_name -> activity_timestamp (0 if not in cache)
	sessionCacheTime time.Time
)

// RefreshSessionCache updates the cache of existing tmux sessions and their activity
// Call this ONCE per tick, then use Session.Exists() and Session.GetWindowActivity()
// which read from cache. This reduces 30+ subprocess spawns to just 1 per tick cycle.
//
// Tries PipeManager first (zero subprocess), falls back to subprocess.
//
// NOTE: We use window_activity (not session_activity) because window_activity updates
// when there's actual terminal output, while session_activity only updates on
// session-level events. This is critical for detecting when Claude is actively working.
func RefreshSessionCache() {
	// Try control mode pipe first (zero subprocess)
	if pm := GetPipeManager(); pm != nil {
		if activities, err := pm.RefreshAllActivities(); err == nil && len(activities) > 0 {
			sessionCacheMu.Lock()
			sessionCacheData = activities
			sessionCacheTime = time.Now()
			sessionCacheMu.Unlock()
			return
		}
		// Pipe failed: log it so we can verify zero subprocess usage
		statusLog.Debug("refresh_cache_subprocess_fallback")
	}

	// Subprocess fallback: list-windows -a
	cmd := exec.Command("tmux", "list-windows", "-a", "-F", "#{session_name}\t#{window_activity}")
	output, err := cmd.Output()
	if err != nil {
		sessionCacheMu.Lock()
		sessionCacheData = nil
		sessionCacheTime = time.Time{}
		sessionCacheMu.Unlock()
		return
	}

	newCache := make(map[string]int64)
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		name := parts[0]
		var activity int64
		_, _ = fmt.Sscanf(parts[1], "%d", &activity) // ignore error, 0 is valid default
		// Keep maximum activity (most recent) if session has multiple windows
		if existing, ok := newCache[name]; !ok || activity > existing {
			newCache[name] = activity
		}
	}

	sessionCacheMu.Lock()
	sessionCacheData = newCache
	sessionCacheTime = time.Now()
	sessionCacheMu.Unlock()
}

// RefreshExistingSessions is an alias for RefreshSessionCache for backwards compatibility
func RefreshExistingSessions() {
	RefreshSessionCache()
}

// sessionExistsFromCache checks if a session exists using the cached data
// Returns (exists, cacheValid) - if cache is stale/empty, cacheValid is false
func sessionExistsFromCache(name string) (bool, bool) {
	sessionCacheMu.RLock()
	defer sessionCacheMu.RUnlock()

	// Cache is valid for 2 seconds (4 ticks at 500ms)
	if sessionCacheData == nil || time.Since(sessionCacheTime) > 2*time.Second {
		return false, false // Cache invalid
	}

	_, exists := sessionCacheData[name]
	return exists, true
}

// registerSessionInCache adds a newly created session to the cache
// This prevents the race condition where a new session isn't found
// because the cache was refreshed before the session was created
func registerSessionInCache(name string) {
	sessionCacheMu.Lock()
	defer sessionCacheMu.Unlock()

	// Initialize cache if nil
	if sessionCacheData == nil {
		sessionCacheData = make(map[string]int64)
	}

	// Add session with current time as activity
	sessionCacheData[name] = time.Now().Unix()
}

// sessionActivityFromCache gets session activity timestamp from cache
// Returns (activity, cacheValid) - if cache is stale/empty, cacheValid is false
func sessionActivityFromCache(name string) (int64, bool) {
	sessionCacheMu.RLock()
	defer sessionCacheMu.RUnlock()

	// Cache is valid for 2 seconds (4 ticks at 500ms)
	if sessionCacheData == nil || time.Since(sessionCacheTime) > 2*time.Second {
		return 0, false // Cache invalid
	}

	activity, exists := sessionCacheData[name]
	if !exists {
		return 0, false // Session not in cache (doesn't exist)
	}
	return activity, true
}

// IsTmuxAvailable checks if tmux is installed and accessible
// Returns nil if tmux is available, otherwise returns an error with details
func IsTmuxAvailable() error {
	cmd := exec.Command("tmux", "-V")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("tmux not found or not working: %w (output: %s)", err, string(output))
	}
	return nil
}

// TerminalInfo contains detected terminal information
type TerminalInfo struct {
	Name              string // Terminal name (warp, iterm2, kitty, alacritty, etc.)
	SupportsOSC8      bool   // Supports OSC 8 hyperlinks
	SupportsOSC52     bool   // Supports OSC 52 clipboard
	SupportsTrueColor bool   // Supports 24-bit color
}

// DetectTerminal identifies the current terminal emulator from environment variables
// Returns terminal name: "warp", "iterm2", "kitty", "alacritty", "vscode", "windows-terminal", or "unknown"
func DetectTerminal() string {
	// Check terminal-specific environment variables (most reliable)

	// Warp Terminal
	if os.Getenv("TERM_PROGRAM") == "WarpTerminal" || os.Getenv("WARP_IS_LOCAL_SHELL_SESSION") != "" {
		return "warp"
	}

	// iTerm2
	if os.Getenv("TERM_PROGRAM") == "iTerm.app" || os.Getenv("ITERM_SESSION_ID") != "" {
		return "iterm2"
	}

	// kitty
	if os.Getenv("TERM") == "xterm-kitty" || os.Getenv("KITTY_WINDOW_ID") != "" {
		return "kitty"
	}

	// Alacritty
	if os.Getenv("ALACRITTY_SOCKET") != "" || os.Getenv("ALACRITTY_LOG") != "" {
		return "alacritty"
	}

	// VS Code integrated terminal
	if os.Getenv("TERM_PROGRAM") == "vscode" || os.Getenv("VSCODE_INJECTION") != "" {
		return "vscode"
	}

	// Windows Terminal
	if os.Getenv("WT_SESSION") != "" {
		return "windows-terminal"
	}

	// WezTerm
	if os.Getenv("TERM_PROGRAM") == "WezTerm" || os.Getenv("WEZTERM_PANE") != "" {
		return "wezterm"
	}

	// Apple Terminal.app
	if os.Getenv("TERM_PROGRAM") == "Apple_Terminal" {
		return "apple-terminal"
	}

	// Hyper
	if os.Getenv("TERM_PROGRAM") == "Hyper" {
		return "hyper"
	}

	// Check TERM_PROGRAM as fallback
	if termProgram := os.Getenv("TERM_PROGRAM"); termProgram != "" {
		return strings.ToLower(termProgram)
	}

	return "unknown"
}

// GetTerminalInfo returns detailed terminal capabilities
func GetTerminalInfo() TerminalInfo {
	terminal := DetectTerminal()

	info := TerminalInfo{
		Name:              terminal,
		SupportsOSC8:      false,
		SupportsOSC52:     false,
		SupportsTrueColor: false,
	}

	// Check COLORTERM for true color support
	colorterm := os.Getenv("COLORTERM")
	if colorterm == "truecolor" || colorterm == "24bit" {
		info.SupportsTrueColor = true
	}

	// Set capabilities based on terminal
	// Reference: https://github.com/Alhadis/OSC8-Adoption
	switch terminal {
	case "warp":
		// Warp: Full modern terminal support
		info.SupportsOSC8 = true  // Native clickable paths
		info.SupportsOSC52 = true // Clipboard integration
		info.SupportsTrueColor = true

	case "iterm2":
		// iTerm2: Excellent escape sequence support
		info.SupportsOSC8 = true
		info.SupportsOSC52 = true
		info.SupportsTrueColor = true

	case "kitty":
		// kitty: Full modern terminal support
		info.SupportsOSC8 = true
		info.SupportsOSC52 = true
		info.SupportsTrueColor = true

	case "alacritty":
		// Alacritty: OSC 8 since v0.11, OSC 52 supported
		info.SupportsOSC8 = true
		info.SupportsOSC52 = true
		info.SupportsTrueColor = true

	case "wezterm":
		// WezTerm: Full support
		info.SupportsOSC8 = true
		info.SupportsOSC52 = true
		info.SupportsTrueColor = true

	case "windows-terminal":
		// Windows Terminal: OSC 8 since v1.4
		info.SupportsOSC8 = true
		info.SupportsOSC52 = true
		info.SupportsTrueColor = true

	case "vscode":
		// VS Code: OSC 8 supported in integrated terminal
		info.SupportsOSC8 = true
		info.SupportsOSC52 = true
		info.SupportsTrueColor = true

	case "hyper":
		// Hyper: Limited OSC support
		info.SupportsOSC8 = false
		info.SupportsOSC52 = true
		info.SupportsTrueColor = true

	case "apple-terminal":
		// Apple Terminal.app: No OSC 8 support
		info.SupportsOSC8 = false
		info.SupportsOSC52 = false
		info.SupportsTrueColor = false

	default:
		// Unknown terminal - assume basic support
		// Most modern terminals support these features
		info.SupportsOSC8 = true // Optimistic default
		info.SupportsOSC52 = true
	}

	return info
}

// SupportsHyperlinks returns true if the current terminal supports OSC 8 hyperlinks
func SupportsHyperlinks() bool {
	return GetTerminalInfo().SupportsOSC8
}

// Tool detection patterns (used by DetectTool for initial tool identification)
var toolDetectionPatterns = map[string][]*regexp.Regexp{
	"claude": {
		regexp.MustCompile(`(?i)claude`),
		regexp.MustCompile(`(?i)anthropic`),
	},
	"gemini": {
		regexp.MustCompile(`(?i)gemini`),
		regexp.MustCompile(`(?i)google ai`),
	},
	"opencode": {
		regexp.MustCompile(`(?i)opencode`),
		regexp.MustCompile(`(?i)open code`),
	},
	"codex": {
		regexp.MustCompile(`(?i)codex`),
		regexp.MustCompile(`(?i)openai`),
	},
}

// StateTracker tracks content changes for notification-style status detection
//
// StateTracker implements a simple 3-state model:
//
//	GREEN (active)   = Content changed within 2 seconds
//	YELLOW (waiting) = Content stable, user hasn't seen it
//	GRAY (idle)      = Content stable, user has seen it
type StateTracker struct {
	lastHash              string    // SHA256 of normalized content (for fallback)
	lastChangeTime        time.Time // When sustained activity was last confirmed
	acknowledged          bool      // User has seen this state (yellow vs gray)
	acknowledgedAt        time.Time // When acknowledged was set (for grace period)
	lastActivityTimestamp int64     // tmux window_activity timestamp for spike detection
	waitingSince          time.Time // When session transitioned to waiting status
	promptNoBusyCount     int       // consecutive prompt-visible polls with no busy signal while active

	// Non-blocking spike detection: track changes across tick cycles
	activityCheckStart  time.Time // When we started tracking for sustained activity
	activityChangeCount int       // How many timestamp changes seen in current window

	// Spinner activity tracking: grace period between tool calls
	spinnerTracker *SpinnerActivityTracker
}

// SpinnerActivityTracker tracks when the spinner was last detected on screen.
// Used for the grace period between tool calls where the spinner briefly disappears.
//
// This is intentionally simple: spinner PRESENCE from the curated char set
// (which excludes ✻ done marker and · non-spinner) is the reliable signal.
// No movement tracking needed because the char set itself distinguishes active vs done.
type SpinnerActivityTracker struct {
	lastBusyTime time.Time     // when spinner was last detected on screen
	gracePeriod  time.Duration // how long to stay busy after spinner disappears (default: 6s)
}

// NewSpinnerActivityTracker creates a tracker with default grace period.
func NewSpinnerActivityTracker() *SpinnerActivityTracker {
	return &SpinnerActivityTracker{
		gracePeriod: 6 * time.Second, // cover 3 polls (2s each) of spinner absence
	}
}

// MarkBusy records that an active spinner char is currently visible on screen.
func (sat *SpinnerActivityTracker) MarkBusy() {
	sat.lastBusyTime = time.Now()
}

// InGracePeriod returns true if an active spinner was visible recently.
// This covers the brief gap between tool calls where the spinner disappears
// before the next tool starts.
func (sat *SpinnerActivityTracker) InGracePeriod() bool {
	return !sat.lastBusyTime.IsZero() && time.Since(sat.lastBusyTime) < sat.gracePeriod
}

// findSpinnerInContent extracts the first spinner character found in the last
// N lines of terminal content. Returns the char and the full line it was found on.
// Skips box-drawing lines (UI borders) and empty lines.
func findSpinnerInContent(content string, spinnerChars []string) (char string, line string, found bool) {
	lines := strings.Split(content, "\n")
	// Check last 10 lines (status line is always near bottom)
	start := len(lines) - 10
	if start < 0 {
		start = 0
	}
	for i := len(lines) - 1; i >= start; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" {
			continue
		}
		// Skip box-drawing lines (UI borders)
		if startsWithBoxDrawing(lines[i]) {
			continue
		}
		for _, ch := range spinnerChars {
			if strings.Contains(lines[i], ch) {
				return ch, lines[i], true
			}
		}
	}
	return "", "", false
}

// isBrailleSpinnerChar returns true for the classic 10-frame braille spinner.
func isBrailleSpinnerChar(ch string) bool {
	switch ch {
	case "⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏":
		return true
	default:
		return false
	}
}

// Session represents a tmux session
// NOTE: All mutable fields are protected by mu. The Bubble Tea event loop is single-threaded,
// but we use mutex protection for defensive programming and future-proofing.
type Session struct {
	Name        string
	DisplayName string
	WorkDir     string
	Command     string
	Created     time.Time
	InstanceID  string // Agent-deck instance ID for hook callbacks
	startupAt   time.Time

	// mu protects all mutable fields below from concurrent access
	mu sync.Mutex

	// PERFORMANCE: Lazy initialization flag
	// When true, ConfigureStatusBar/EnableMouseMode have been run
	// Allows deferring non-essential tmux configuration until first attach
	configured bool

	// PERFORMANCE: Cache CapturePane content for short duration (500ms)
	// Reduces subprocess spawns during rapid status checks/log events
	cacheMu      sync.RWMutex
	cacheContent string
	cacheTime    time.Time
	captureSf    singleflight.Group // Deduplicates concurrent CapturePane subprocess calls

	// Content tracking for HasUpdated (separate from StateTracker)
	lastHash    string
	lastContent string

	// Cached tool detection (avoids re-detecting every status check)
	detectedTool     string
	toolDetectedAt   time.Time
	toolDetectExpiry time.Duration // How long before re-detecting (default 30s)

	// Simple state tracking (hash-based)
	stateTracker *StateTracker

	// Last status returned (for debugging)
	lastStableStatus string

	// OptionOverrides are user-specified tmux set-option overrides from config.
	// Applied AFTER all defaults in Start(), so they take precedence.
	// Keys are tmux option names, values are their settings.
	// Example: {"allow-passthrough": "all", "history-limit": "50000"}
	OptionOverrides map[string]string

	// Custom patterns for generic tool support
	customToolName       string
	customBusyPatterns   []string
	customPromptPatterns []string
	customDetectPatterns []string

	// Configurable patterns (replaces hardcoded detection logic)
	// When non-nil, hasBusyIndicator and normalizeContent use these instead of hardcoded values
	resolvedPatterns *ResolvedPatterns

	// Cached PromptDetector (avoids allocating a new one on every hasPromptIndicator call)
	cachedPromptDetector     *PromptDetector
	cachedPromptDetectorTool string

	// Environment variable cache (reduces tmux show-environment subprocess spawns)
	envCache   map[string]envCacheEntry
	envCacheMu sync.RWMutex
}

type envCacheEntry struct {
	value string
	time  time.Time
}

const (
	envCacheTTL        = 30 * time.Second
	startupStateWindow = 2 * time.Minute
)

// invalidateCache clears the CapturePane cache.
// MUST be called after any action that might change terminal content.
func (s *Session) invalidateCache() {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	s.cacheContent = ""
	s.cacheTime = time.Time{}
}

// ensureStateTrackerLocked lazily allocates the tracker so callers can safely
// acknowledge even before the first GetStatus call.
// MUST be called with mu held.
func (s *Session) ensureStateTrackerLocked() {
	if s.stateTracker == nil {
		s.stateTracker = &StateTracker{
			lastHash:       "",
			lastChangeTime: time.Now(),
			acknowledged:   false,
			spinnerTracker: NewSpinnerActivityTracker(),
		}
	}
	// Ensure spinnerTracker exists even for older StateTrackers
	if s.stateTracker.spinnerTracker == nil {
		s.stateTracker.spinnerTracker = NewSpinnerActivityTracker()
	}
}

// shouldHoldActiveOnPromptLocked applies a small hysteresis when a session was
// recently active but current capture shows prompt with no busy signal.
// This avoids active <-> waiting flicker from transient capture misses.
// MUST be called with s.mu held.
func (s *Session) shouldHoldActiveOnPromptLocked() bool {
	if s.stateTracker == nil || s.lastStableStatus != "active" {
		return false
	}
	const promptNoBusyHoldPolls = 2
	if s.stateTracker.promptNoBusyCount < promptNoBusyHoldPolls {
		s.stateTracker.promptNoBusyCount++
		return true
	}
	return false
}

// resetPromptNoBusyHoldLocked clears prompt-no-busy hysteresis counters.
// MUST be called with s.mu held.
func (s *Session) resetPromptNoBusyHoldLocked() {
	if s.stateTracker != nil {
		s.stateTracker.promptNoBusyCount = 0
	}
}

// inStartupWindowLocked returns true when the session is still in its startup phase.
// MUST be called with s.mu held.
func (s *Session) inStartupWindowLocked() bool {
	return !s.startupAt.IsZero() && time.Since(s.startupAt) < startupStateWindow
}

// SetCustomPatterns sets custom patterns for generic tool support
// These patterns enable custom tools defined in config.toml to have proper status detection
func (s *Session) SetCustomPatterns(toolName string, busyPatterns, promptPatterns, detectPatterns []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.customToolName = toolName
	s.customBusyPatterns = busyPatterns
	s.customPromptPatterns = promptPatterns
	s.customDetectPatterns = detectPatterns
}

// SetPatterns sets the compiled ResolvedPatterns for configurable status detection.
// When set, hasBusyIndicator and normalizeContent use these instead of hardcoded values.
func (s *Session) SetPatterns(p *ResolvedPatterns) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resolvedPatterns = p
}

// SetDetectPatterns sets tool auto-detection patterns (separate from busy/prompt patterns).
func (s *Session) SetDetectPatterns(toolName string, detectPatterns []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.customToolName = toolName
	s.customDetectPatterns = detectPatterns
}

// LogFile returns the path to this session's log file
// Logs are stored in ~/.agent-deck/logs/<session-name>.log
func (s *Session) LogFile() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = "/tmp"
	}
	logDir := filepath.Join(homeDir, ".agent-deck", "logs")
	return filepath.Join(logDir, s.Name+".log")
}

// LogDir returns the directory containing all session logs
func LogDir() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = "/tmp"
	}
	return filepath.Join(homeDir, ".agent-deck", "logs")
}

// NewSession creates a new Session instance with a unique name
func NewSession(name, workDir string) *Session {
	sanitized := sanitizeName(name)
	// Add unique suffix to prevent name collisions
	uniqueSuffix := generateShortID()
	return &Session{
		Name:             SessionPrefix + sanitized + "_" + uniqueSuffix,
		DisplayName:      name,
		WorkDir:          workDir,
		Created:          time.Now(),
		startupAt:        time.Now(),
		lastStableStatus: "waiting",
		toolDetectExpiry: 30 * time.Second, // Re-detect tool every 30 seconds
		// stateTracker and promptDetector will be created lazily on first status check
	}
}

// ReconnectSession creates a Session object for an existing tmux session
// This is used when loading sessions from storage - it properly initializes
// all fields needed for status detection to work correctly
//
// Note: This runs immediate configuration (ConfigureStatusBar).
// For lazy loading during TUI startup, use ReconnectSessionLazy instead.
func ReconnectSession(tmuxName, displayName, workDir, command string) *Session {
	sess := &Session{
		Name:             tmuxName,
		DisplayName:      displayName,
		WorkDir:          workDir,
		Command:          command,
		Created:          time.Now(), // Approximate - we don't persist this
		startupAt:        time.Time{},
		lastStableStatus: "waiting",
		toolDetectExpiry: 30 * time.Second,
		configured:       false, // Will be set to true after configuration
		// stateTracker and promptDetector will be created lazily on first status check
	}

	// Configure existing sessions
	if sess.Exists() {
		sess.ConfigureStatusBar()
		sess.configured = true
	}

	return sess
}

// ReconnectSessionWithStatus creates a Session with pre-initialized state based on previous status
// This restores the exact status state across app restarts:
//   - "idle" (gray): acknowledged=true, cooldown expired
//   - "waiting" (yellow): acknowledged=false, cooldown expired
//   - "active" (green): will be recalculated based on actual content changes
func ReconnectSessionWithStatus(tmuxName, displayName, workDir, command string, previousStatus string) *Session {
	sess := ReconnectSession(tmuxName, displayName, workDir, command)

	switch previousStatus {
	case "idle":
		// Session was acknowledged (user saw it) - restore as GRAY
		sess.stateTracker = &StateTracker{
			lastHash:       "",                                // Will be set on first GetStatus
			lastChangeTime: time.Now().Add(-10 * time.Second), // Cooldown expired
			acknowledged:   true,
		}
		sess.lastStableStatus = "idle"

	case "waiting", "active":
		// Session needs attention - restore as YELLOW
		// Active sessions will show green when content changes
		sess.stateTracker = &StateTracker{
			lastHash:       "",                                // Will be set on first GetStatus
			lastChangeTime: time.Now().Add(-10 * time.Second), // Cooldown expired
			acknowledged:   false,
		}
		sess.lastStableStatus = "waiting"

	default:
		// Unknown status - default to waiting
		sess.lastStableStatus = "waiting"
	}

	return sess
}

// ReconnectSessionLazy creates a Session object without running any tmux configuration.
// PERFORMANCE: This is used during TUI startup to avoid subprocess overhead.
// Non-essential configuration (EnableMouseMode, ConfigureStatusBar)
// is deferred until first user interaction via EnsureConfigured().
//
// Use this for bulk session loading where immediate configuration is not needed.
// For sessions that need immediate configuration, use ReconnectSession or ReconnectSessionWithStatus.
func ReconnectSessionLazy(tmuxName, displayName, workDir, command string, previousStatus string) *Session {
	sess := &Session{
		Name:             tmuxName,
		DisplayName:      displayName,
		WorkDir:          workDir,
		Command:          command,
		Created:          time.Now(), // Approximate - we don't persist this
		startupAt:        time.Time{},
		lastStableStatus: "waiting",
		toolDetectExpiry: 30 * time.Second,
		configured:       false, // Explicitly mark as not configured
	}

	// Restore state tracker based on previous status (without running tmux commands)
	switch previousStatus {
	case "idle":
		sess.stateTracker = &StateTracker{
			lastHash:       "",
			lastChangeTime: time.Now().Add(-10 * time.Second),
			acknowledged:   true,
		}
		sess.lastStableStatus = "idle"

	case "waiting", "active":
		sess.stateTracker = &StateTracker{
			lastHash:       "",
			lastChangeTime: time.Now().Add(-10 * time.Second),
			acknowledged:   false,
		}
		sess.lastStableStatus = "waiting"

	default:
		sess.lastStableStatus = "waiting"
	}

	return sess
}

// EnsureConfigured runs deferred tmux configuration if not already done.
// PERFORMANCE: This should be called before attaching to a session or when
// the session needs full functionality (e.g., status bar, mouse mode).
//
// Safe to call multiple times - does nothing if already configured or session doesn't exist.
// Thread-safe via mutex protection.
func (s *Session) EnsureConfigured() {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Already configured or session doesn't exist - nothing to do
	if s.configured || !s.Exists() {
		return
	}

	// Run deferred configuration
	s.ConfigureStatusBar()
	_ = s.EnableMouseMode()

	s.configured = true
	statusLog.Debug("lazy_config_completed", slog.String("session", s.DisplayName))
}

// IsConfigured returns whether the session has been fully configured.
// Used for debugging and testing.
func (s *Session) IsConfigured() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.configured
}

// generateShortID generates a short random ID for uniqueness
func generateShortID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		// Fallback to timestamp
		return fmt.Sprintf("%d", time.Now().UnixNano()%100000)
	}
	return hex.EncodeToString(b)
}

// SetEnvironment sets an environment variable for this tmux session
func (s *Session) SetEnvironment(key, value string) error {
	cmd := exec.Command("tmux", "set-environment", "-t", s.Name, key, value)
	err := cmd.Run()
	if err == nil {
		// Invalidate cache entry so next GetEnvironment sees the new value
		s.envCacheMu.Lock()
		if s.envCache != nil {
			delete(s.envCache, key)
		}
		s.envCacheMu.Unlock()
	}
	return err
}

// GetEnvironment gets an environment variable from this tmux session.
// Uses a 30-second cache to avoid spawning tmux show-environment subprocesses
// on every poll cycle. Call InvalidateEnvCache() after SetEnvironment to clear.
func (s *Session) GetEnvironment(key string) (string, error) {
	// Check cache first
	s.envCacheMu.RLock()
	if s.envCache != nil {
		if entry, ok := s.envCache[key]; ok && time.Since(entry.time) < envCacheTTL {
			s.envCacheMu.RUnlock()
			return entry.value, nil
		}
	}
	s.envCacheMu.RUnlock()

	cmd := exec.Command("tmux", "show-environment", "-t", s.Name, key)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("variable not found or session doesn't exist: %s", key)
	}
	// Output format: "KEY=value\n"
	line := strings.TrimSpace(string(output))
	prefix := key + "="
	if strings.HasPrefix(line, prefix) {
		value := strings.TrimPrefix(line, prefix)
		// Store in cache
		s.envCacheMu.Lock()
		if s.envCache == nil {
			s.envCache = make(map[string]envCacheEntry)
		}
		s.envCache[key] = envCacheEntry{value: value, time: time.Now()}
		s.envCacheMu.Unlock()
		return value, nil
	}
	return "", fmt.Errorf("variable not found: %s", key)
}

// InvalidateEnvCache clears the environment variable cache for this session.
// Should be called after SetEnvironment to ensure fresh reads.
func (s *Session) InvalidateEnvCache() {
	s.envCacheMu.Lock()
	s.envCache = nil
	s.envCacheMu.Unlock()
}

// sanitizeName converts a display name to a valid tmux session name
func sanitizeName(name string) string {
	// Replace spaces and special characters with hyphens
	re := regexp.MustCompile(`[^a-zA-Z0-9-]+`)
	return re.ReplaceAllString(name, "-")
}

// Start creates and starts a tmux session
func (s *Session) Start(command string) error {
	s.Command = command
	s.invalidateCache()
	s.Created = time.Now()
	s.startupAt = s.Created
	s.mu.Lock()
	s.lastStableStatus = "waiting"
	s.stateTracker = nil
	s.cachedPromptDetector = nil
	s.cachedPromptDetectorTool = ""
	s.mu.Unlock()

	// Check if session already exists (shouldn't happen with unique IDs, but handle gracefully)
	if s.Exists() {
		// Session with this exact name exists - regenerate with new unique suffix
		sanitized := sanitizeName(s.DisplayName)
		s.Name = SessionPrefix + sanitized + "_" + generateShortID()
	}

	// Ensure working directory exists
	workDir := s.WorkDir
	if workDir == "" {
		workDir = os.Getenv("HOME")
	}

	// Create new tmux session in detached mode
	cmd := exec.Command("tmux", "new-session", "-d", "-s", s.Name, "-c", workDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create tmux session: %w (output: %s)", err, string(output))
	}

	// Register session in cache immediately to prevent race condition
	// where Exists() returns false because cache was refreshed before session creation
	registerSessionInCache(s.Name)

	// Set default window/pane styles to prevent color issues in some terminals (Warp, etc.)
	// This ensures no unexpected background colors are applied
	_ = exec.Command("tmux", "set-option", "-t", s.Name, "window-style", "default").Run()
	_ = exec.Command("tmux", "set-option", "-t", s.Name, "window-active-style", "default").Run()

	// Enable mouse mode for proper scrolling (per-session, doesn't affect user's other sessions)
	// This allows:
	// - Mouse wheel scrolling through terminal history
	// - Text selection with mouse
	// - Pane resizing with mouse
	// Non-fatal: session still works, just without mouse support
	// This can fail on very old tmux versions
	_ = exec.Command("tmux", "set-option", "-t", s.Name, "mouse", "on").Run()

	// Enable escape sequence passthrough for modern terminal features (tmux 3.2+)
	// This allows:
	// - OSC 8: Clickable hyperlinks/file paths (Warp, iTerm2, kitty, Alacritty, etc.)
	// - OSC 52: Clipboard integration (copy/paste from remote sessions)
	// - Image protocols: Inline images in terminals that support it
	// Uses -q flag to silently ignore on older tmux versions (< 3.2)
	_ = exec.Command("tmux", "set-option", "-t", s.Name, "-q", "allow-passthrough", "on").Run()

	// Enable hyperlink support in terminal features (tmux 3.4+, server-wide option)
	// This tells tmux to track hyperlinks like it tracks colors/attributes
	// Required for OSC 8 hyperlinks to work - passthrough alone isn't enough
	// Uses -as to append to existing terminal-features, -q to ignore if unsupported
	_ = exec.Command("tmux", "set", "-asq", "terminal-features", ",*:hyperlinks").Run()

	// Enable OSC 52 clipboard integration for seamless copy/paste
	// Works with: Warp, iTerm2, kitty, Alacritty, WezTerm, Windows Terminal, VS Code
	// The 'on' value (tmux 2.6+) allows apps inside tmux to set the clipboard
	_ = exec.Command("tmux", "set-option", "-t", s.Name, "set-clipboard", "on").Run()

	// Set large history buffer for AI agent sessions (default is 2000)
	// AI agents produce extensive output, 10000 lines is a good balance
	_ = exec.Command("tmux", "set-option", "-t", s.Name, "history-limit", "10000").Run()

	// Reduce escape-time for responsive Vim/editor usage (default 500ms is too slow)
	// 10ms is a good balance between responsiveness and SSH reliability
	_ = exec.Command("tmux", "set-option", "-t", s.Name, "escape-time", "10").Run()

	// Apply user-specified tmux option overrides from config (after defaults)
	// This allows users to override any default, e.g. allow-passthrough = "all"
	if len(s.OptionOverrides) > 0 {
		for key, value := range s.OptionOverrides {
			_ = exec.Command("tmux", "set-option", "-t", s.Name, "-q", key, value).Run()
		}
	}

	// Configure status bar with session info for easy identification
	// Shows: session title on left, project folder on right
	s.ConfigureStatusBar()

	// Send the command to the session
	if command != "" {
		cmdToSend := command
		// IMPORTANT: Commands containing bash-specific syntax (like `session_id=$(...)`)
		// must be wrapped in `bash -c` for fish shell compatibility (#47).
		// Fish uses different syntax: `set var (...)` instead of `var=$(...)`.
		if strings.Contains(command, "$(") || strings.Contains(command, "session_id=") {
			// Escape single quotes in the command for bash -c wrapper
			escapedCmd := strings.ReplaceAll(command, "'", "'\"'\"'")
			cmdToSend = fmt.Sprintf("bash -c '%s'", escapedCmd)
		}
		if err := s.SendKeysAndEnter(cmdToSend); err != nil {
			return fmt.Errorf("failed to send command: %w", err)
		}
	}

	// Connect control mode pipe for event-driven status detection
	if pm := GetPipeManager(); pm != nil {
		if err := pm.Connect(s.Name); err != nil {
			statusLog.Debug("control_pipe_connect_failed", slog.String("session", s.Name), slog.String("error", err.Error()))
		}
	}

	// Note: We tried using tmux hooks for instant GREEN status detection:
	// - alert-activity: Only fires for background windows (not current window)
	// - after-send-keys: Fires for ALL send-keys calls (too noisy, catches agent-deck operations)
	// Neither works reliably for detecting user input. We use polling for GREEN instead.
	// The Stop hook (via Claude settings) handles instant YELLOW detection.

	return nil
}

// Exists checks if the tmux session exists
// Uses cached session list when available (refreshed by RefreshExistingSessions)
// Falls back to direct tmux call if cache is stale
func (s *Session) Exists() bool {
	// Try cache first (O(1) map lookup, no subprocess)
	if exists, cacheValid := sessionExistsFromCache(s.Name); cacheValid {
		return exists
	}

	// If PipeManager has a live control connection, the session definitely exists.
	if pm := GetPipeManager(); pm != nil {
		if pm.IsConnected(s.Name) {
			return true
		}
	}

	// Cache is stale and no live pipe: fall back to direct tmux check.
	cmd := exec.Command("tmux", "has-session", "-t", s.Name)
	return cmd.Run() == nil
}

// ConfigureStatusBar sets up the tmux status bar with session info
// Shows: notification bar on left (managed by NotificationManager), session info on right
// NOTE: status-left is reserved for the notification bar showing waiting sessions
// This function only configures status-right to avoid overwriting notification bar
func (s *Session) ConfigureStatusBar() {
	// Get short folder name from WorkDir
	folderName := filepath.Base(s.WorkDir)
	if folderName == "" || folderName == "." {
		folderName = "~"
	}

	// Right side: detach hint + session title with folder path
	// The hint uses subtle gray (#565f89) so it doesn't compete with session info
	rightStatus := fmt.Sprintf("#[fg=#565f89]ctrl+q detach#[default] │ 📁 %s | %s ", s.DisplayName, folderName)

	// PERFORMANCE: Batch all 5 status bar options into single subprocess call
	// Uses tmux command chaining with \; separator (73% reduction in subprocess calls)
	// Before: 5 separate exec.Command calls = 5 subprocess spawns
	// After: 1 exec.Command call = 1 subprocess spawn
	cmd := exec.Command("tmux",
		"set-option", "-t", s.Name, "status", "on", ";",
		"set-option", "-t", s.Name, "status-style", "bg=#1a1b26,fg=#a9b1d6", ";",
		"set-option", "-t", s.Name, "status-left-length", "120", ";",
		"set-option", "-t", s.Name, "status-right", rightStatus, ";",
		"set-option", "-t", s.Name, "status-right-length", "80")
	_ = cmd.Run()
}

// EnableMouseMode enables mouse scrolling, clipboard integration, and optimal settings
// Safe to call multiple times - just sets the options again
//
// Enables:
// - mouse on: Mouse wheel scrolling, text selection, pane resizing
// - set-clipboard on: OSC 52 clipboard integration (works with modern terminals)
// - allow-passthrough on: OSC 8 hyperlinks, advanced escape sequences (tmux 3.2+)
// - history-limit 10000: Large scrollback buffer for AI agent output
// - escape-time 10: Fast Vim/editor responsiveness (default 500ms is too slow)
//
// Terminal compatibility:
// - Warp, iTerm2, kitty, Alacritty, WezTerm: Full support (hyperlinks, clipboard, true color)
// - Windows Terminal, VS Code: Full support
// - Apple Terminal.app: Limited (no hyperlinks or clipboard)
//
// Note: With mouse mode on, hold Shift while selecting to use native terminal selection
// instead of tmux's selection (useful for copying to system clipboard in some terminals)
func (s *Session) EnableMouseMode() error {
	// CRITICAL: Mouse mode must succeed - keep as separate call for error handling
	// This is the only essential feature; all others are enhancements
	mouseCmd := exec.Command("tmux", "set-option", "-t", s.Name, "mouse", "on")
	if err := mouseCmd.Run(); err != nil {
		return err
	}

	// PERFORMANCE: Batch all non-fatal enhancements into single subprocess call
	// Uses tmux command chaining with \; separator (67% reduction in subprocess calls)
	// Before: 5 separate exec.Command calls = 5 subprocess spawns
	// After: 1 exec.Command call = 1 subprocess spawn
	//
	// Enhancements included:
	// - set-clipboard on: OSC 52 clipboard integration (Warp, iTerm2, kitty, etc.)
	// - allow-passthrough on: OSC 8 hyperlinks, advanced escape sequences (tmux 3.2+)
	// - terminal-features hyperlinks: Track hyperlinks like colors (tmux 3.4+)
	// - history-limit 10000: Large scrollback for AI agent output
	// - escape-time 10: Fast Vim/editor responsiveness (default 500ms is too slow)
	//
	// Uses -q flag where supported to silently ignore on older tmux versions
	enhanceCmd := exec.Command("tmux",
		"set-option", "-t", s.Name, "set-clipboard", "on", ";",
		"set-option", "-t", s.Name, "-q", "allow-passthrough", "on", ";",
		"set-option", "-t", s.Name, "history-limit", "10000", ";",
		"set-option", "-t", s.Name, "escape-time", "10", ";",
		"set", "-asq", "terminal-features", ",*:hyperlinks")
	// Ignore errors - all these are non-fatal enhancements
	// Older tmux versions may not support some options
	_ = enhanceCmd.Run()

	return nil
}

// Kill terminates the tmux session.
// Like RespawnPane, this captures the process tree first and ensures all
// processes actually die. tmux kill-session sends SIGHUP which some CLI
// tools (e.g. Claude Code 2.1.27+) ignore, leaving orphan processes.
func (s *Session) Kill() error {
	// Disconnect control mode pipe
	if pm := GetPipeManager(); pm != nil {
		pm.Disconnect(s.Name)
	}

	// Remove old log file if it exists (from pre-control-pipe era)
	logFile := s.LogFile()
	os.Remove(logFile) // Ignore errors

	// Capture process tree BEFORE killing so we can verify they die
	_, oldPIDs := s.getPaneProcessTree()
	if len(oldPIDs) > 0 {
		respawnLog.Info("pre_kill_process_tree", slog.String("session", s.Name), slog.Any("pids", oldPIDs))
	}

	// Kill the tmux session
	cmd := exec.Command("tmux", "kill-session", "-t", s.Name)
	err := cmd.Run()

	// Verify old processes are dead; escalate to SIGKILL if needed
	if len(oldPIDs) > 0 {
		go s.ensureProcessesDead(oldPIDs, 0)
	}

	return err
}

// getPaneProcessTree returns the pane's direct PID and all descendant PIDs.
// Used before respawn to track processes that must die.
func (s *Session) getPaneProcessTree() (panePID int, allPIDs []int) {
	target := s.Name + ":"
	out, err := exec.Command("tmux", "list-panes", "-t", target, "-F", "#{pane_pid}").Output()
	if err != nil {
		return 0, nil
	}
	// Take only the first line (handles multi-pane sessions safely)
	pidStr := strings.TrimSpace(string(out))
	if idx := strings.IndexByte(pidStr, '\n'); idx >= 0 {
		pidStr = pidStr[:idx]
	}
	panePID, err = strconv.Atoi(pidStr)
	if err != nil {
		return 0, nil
	}

	// Collect the pane PID plus all descendants via pgrep -P (recursive)
	allPIDs = []int{panePID}
	queue := []int{panePID}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		pgrepOut, err := exec.Command("pgrep", "-P", strconv.Itoa(parent)).Output()
		if err != nil {
			continue
		}
		for _, line := range strings.Split(strings.TrimSpace(string(pgrepOut)), "\n") {
			if pid, err := strconv.Atoi(strings.TrimSpace(line)); err == nil && pid > 0 {
				allPIDs = append(allPIDs, pid)
				queue = append(queue, pid)
			}
		}
	}
	return panePID, allPIDs
}

// isOurProcess checks if a PID still belongs to a process we spawned
// (claude, node, zsh, bash, sh) rather than an unrelated process that
// reused the PID. This prevents accidentally killing random processes.
func isOurProcess(pid int) bool {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil {
		return false // Process doesn't exist
	}
	name := strings.ToLower(strings.TrimSpace(string(out)))
	for _, known := range []string{"claude", "node", "zsh", "bash", "sh", "cat", "npm"} {
		if strings.Contains(name, known) {
			return true
		}
	}
	return false
}

// ensureProcessesDead checks if any of the given PIDs are still alive and
// escalates from SIGTERM to SIGKILL. This prevents zombie/orphan process
// accumulation when CLI tools (e.g. Claude Code) ignore SIGHUP from tmux.
func (s *Session) ensureProcessesDead(oldPIDs []int, newPanePID int) {
	if len(oldPIDs) == 0 {
		return
	}

	// Wait briefly for respawn-pane's SIGHUP to take effect
	time.Sleep(500 * time.Millisecond)

	var survivors []int
	for _, pid := range oldPIDs {
		// Skip the new pane process (respawn reuses the pane PID slot sometimes)
		if pid == newPanePID {
			continue
		}
		// Check if process is still alive (signal 0 = existence check)
		proc, err := os.FindProcess(pid)
		if err != nil {
			continue
		}
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			continue // Already dead
		}
		// Guard against PID reuse: verify it's still one of our processes
		if !isOurProcess(pid) {
			respawnLog.Info("pid_not_ours_skipping", slog.Int("pid", pid))
			continue
		}
		survivors = append(survivors, pid)
	}

	if len(survivors) == 0 {
		return
	}

	// First try SIGTERM
	respawnLog.Info("survivors_sending_sigterm", slog.Int("count", len(survivors)), slog.Any("pids", survivors))
	for _, pid := range survivors {
		if proc, err := os.FindProcess(pid); err == nil {
			_ = proc.Signal(syscall.SIGTERM)
		}
	}

	// Wait for SIGTERM
	time.Sleep(1 * time.Second)

	// Check again and SIGKILL any remaining
	var stubborn []int
	for _, pid := range survivors {
		proc, err := os.FindProcess(pid)
		if err != nil {
			continue
		}
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			continue // Dead now
		}
		stubborn = append(stubborn, pid)
	}

	if len(stubborn) == 0 {
		respawnLog.Info("all_survivors_terminated_after_sigterm")
		return
	}

	respawnLog.Info("stubborn_sending_sigkill", slog.Int("count", len(stubborn)), slog.Any("pids", stubborn))
	for _, pid := range stubborn {
		if proc, err := os.FindProcess(pid); err == nil {
			_ = proc.Signal(syscall.SIGKILL)
		}
	}
	respawnLog.Info("sigkill_cleanup_complete", slog.Int("count", len(stubborn)))
}

// RespawnPane kills the current process in the pane and starts a new command.
// This is more reliable than sending Ctrl+C and waiting for shell prompt.
// The -k flag kills the current process before respawning.
//
// IMPORTANT: After respawn, this function verifies that old processes actually
// died. Some CLI tools (notably Claude Code 2.1.27+) ignore SIGHUP sent by
// tmux respawn-pane, leaving orphan processes that consume CPU indefinitely.
// If old processes survive, we escalate through SIGTERM → SIGKILL.
func (s *Session) RespawnPane(command string) error {
	if !s.Exists() {
		return fmt.Errorf("session does not exist: %s", s.Name)
	}
	s.invalidateCache()

	// Capture the current process tree BEFORE respawn so we can verify they die
	_, oldPIDs := s.getPaneProcessTree()
	if len(oldPIDs) > 0 {
		respawnLog.Info("pre_respawn_process_tree", slog.Any("pids", oldPIDs))
	}

	// Clear scrollback buffer BEFORE respawn to prevent stale content
	// from previous conversation appearing when user attaches (#138).
	clearTarget := s.Name + ":"
	clearCmd := exec.Command("tmux", "clear-history", "-t", clearTarget)
	if clearOut, clearErr := clearCmd.CombinedOutput(); clearErr != nil {
		respawnLog.Debug("clear_history_failed", slog.String("error", clearErr.Error()), slog.String("output", string(clearOut)))
	} else {
		respawnLog.Info("cleared_scrollback", slog.String("session", s.Name))
	}

	// Build respawn-pane command
	// -k: Kill current process
	// -t: Target pane (session:window.pane format, use session: for active pane)
	// command: New command to run
	target := s.Name + ":" // Append colon to target the active pane
	args := []string{"respawn-pane", "-k", "-t", target}
	if command != "" {
		// Wrap command in interactive shell to ensure aliases and shell configs are available
		// tmux respawn-pane runs commands directly without loading ~/.bashrc or ~/.zshrc,
		// so shell aliases (like 'cdw' for claude) won't work without this wrapper
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/bash"
		}

		// IMPORTANT: Commands containing bash-specific syntax (like `session_id=$(...)`)
		// must use bash, regardless of user's shell. This fixes fish shell compatibility (#47).
		// Fish uses different syntax: `set var (...)` instead of `var=$(...)`.
		// We detect bash-specific constructs and force bash for those commands.
		if strings.Contains(command, "$(") || strings.Contains(command, "session_id=") {
			shell = "/bin/bash"
		}

		// Use -i for interactive (loads aliases) and -c for command
		wrappedCmd := fmt.Sprintf("%s -ic %q", shell, command)
		args = append(args, wrappedCmd)
	}

	mcpLog.Debug("respawn_pane_executing", slog.Any("args", args))
	cmd := exec.Command("tmux", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		mcpLog.Debug("respawn_pane_error", slog.String("error", err.Error()), slog.String("output", string(output)))
		return fmt.Errorf("failed to respawn pane: %w (output: %s)", err, string(output))
	}
	mcpLog.Debug("respawn_pane_output", slog.String("output", string(output)))

	// Get the NEW pane PID so we don't accidentally kill the fresh process
	newPanePID, _ := s.getPaneProcessTree()

	// Verify old processes are dead; escalate to SIGKILL if needed
	// Run in background so RespawnPane returns quickly
	if len(oldPIDs) > 0 {
		go s.ensureProcessesDead(oldPIDs, newPanePID)
	}

	// Reconnect control mode pipe (respawn changes the pane process)
	if pm := GetPipeManager(); pm != nil {
		pm.Disconnect(s.Name)
		if err := pm.Connect(s.Name); err != nil {
			statusLog.Debug("control_pipe_reconnect_failed", slog.String("session", s.Name), slog.String("error", err.Error()))
		}
	}

	// Reset startup/status trackers so GetStatus can classify the fresh process correctly.
	s.mu.Lock()
	s.startupAt = time.Now()
	s.lastStableStatus = "waiting"
	s.stateTracker = nil
	s.cachedPromptDetector = nil
	s.cachedPromptDetectorTool = ""
	s.mu.Unlock()

	return nil
}

// GetWindowActivity returns Unix timestamp of last tmux window activity
// Uses cached data when available (refreshed by RefreshSessionCache)
// Falls back to direct tmux call if cache is stale
func (s *Session) GetWindowActivity() (int64, error) {
	// Try cache first (O(1) map lookup, no subprocess)
	if activity, cacheValid := sessionActivityFromCache(s.Name); cacheValid {
		return activity, nil
	}

	// When PipeManager is active, route through pipe (zero subprocess)
	if pm := GetPipeManager(); pm != nil {
		return pm.GetWindowActivity(s.Name)
	}

	// No PipeManager: fall back to direct check (spawns subprocess)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "tmux", "display-message", "-t", s.Name, "-p", "#{window_activity}")
	output, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("failed to get window activity: %w", err)
	}
	var ts int64
	_, err = fmt.Sscanf(strings.TrimSpace(string(output)), "%d", &ts)
	if err != nil {
		return 0, fmt.Errorf("failed to parse timestamp: %w", err)
	}
	return ts, nil
}

// GetCachedWindowActivity returns the cached window_activity timestamp without
// spawning a subprocess. Returns 0 if the cache is stale or session not found.
// This is used for cheap idle-session activity gating in tiered polling.
func (s *Session) GetCachedWindowActivity() int64 {
	activity, valid := sessionActivityFromCache(s.Name)
	if valid {
		return activity
	}
	return 0
}

// CapturePane captures the visible pane content.
// Tries control mode pipe first (zero subprocess), falls back to subprocess.
// Uses singleflight to deduplicate concurrent calls.
func (s *Session) CapturePane() (string, error) {
	// Fast path: return cached content if fresh
	s.cacheMu.RLock()
	if s.cacheContent != "" && time.Since(s.cacheTime) < 500*time.Millisecond {
		content := s.cacheContent
		s.cacheMu.RUnlock()
		return content, nil
	}
	s.cacheMu.RUnlock()

	// Slow path: deduplicate concurrent calls via singleflight.
	v, err, _ := s.captureSf.Do("capture", func() (interface{}, error) {
		// Double-check cache inside singleflight
		s.cacheMu.RLock()
		if s.cacheContent != "" && time.Since(s.cacheTime) < 500*time.Millisecond {
			content := s.cacheContent
			s.cacheMu.RUnlock()
			return content, nil
		}
		s.cacheMu.RUnlock()

		// Try control mode pipe first (zero subprocess)
		if pm := GetPipeManager(); pm != nil {
			if content, pipeErr := pm.CapturePane(s.Name); pipeErr == nil {
				s.cacheMu.Lock()
				s.cacheContent = content
				s.cacheTime = time.Now()
				s.cacheMu.Unlock()
				return content, nil
			}
			// Pipe failed: log it so we can verify zero subprocess usage
			statusLog.Debug("capture_pane_subprocess_fallback", slog.String("session", s.Name))
		}

		// Subprocess fallback: -J joins wrapped lines, 3s timeout
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "tmux", "capture-pane", "-t", s.Name, "-p", "-J")
		output, err := cmd.Output()
		if err != nil {
			if ctx.Err() == context.DeadlineExceeded {
				return "", ErrCaptureTimeout
			}
			return "", fmt.Errorf("failed to capture pane: %w", err)
		}

		content := string(output)

		s.cacheMu.Lock()
		s.cacheContent = content
		s.cacheTime = time.Now()
		s.cacheMu.Unlock()

		return content, nil
	})
	if err != nil {
		return "", err
	}
	return v.(string), nil
}

// CaptureFullHistory captures the scrollback history (limited to last 2000 lines for performance)
func (s *Session) CaptureFullHistory() (string, error) {
	// Limit to last 2000 lines to balance content availability with memory usage
	// AI agent conversations can be long - 2000 lines captures ~40-80 screens of content
	// -J joins wrapped lines and trims trailing spaces so hashes don't change on resize
	cmd := exec.Command("tmux", "capture-pane", "-t", s.Name, "-p", "-J", "-S", "-2000")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to capture history: %w", err)
	}
	return string(output), nil
}

// HasUpdated checks if the pane content has changed since last check
func (s *Session) HasUpdated() (bool, error) {
	content, err := s.CapturePane()
	if err != nil {
		return false, err
	}

	// Calculate SHA256 hash of content
	hash := sha256.Sum256([]byte(content))
	hashStr := hex.EncodeToString(hash[:])

	// Protect access to lastHash and lastContent
	s.mu.Lock()
	defer s.mu.Unlock()

	// First time check
	if s.lastHash == "" {
		s.lastHash = hashStr
		s.lastContent = content
		return true, nil
	}

	// Compare with previous hash
	if hashStr != s.lastHash {
		s.lastHash = hashStr
		s.lastContent = content
		return true, nil
	}

	return false, nil
}

// DetectTool detects which AI coding tool is running in the session
// Uses caching to avoid re-detection on every call
func (s *Session) DetectTool() string {
	// Check cache first (read lock pattern for better concurrency)
	s.mu.Lock()
	if s.detectedTool != "" && time.Since(s.toolDetectedAt) < s.toolDetectExpiry {
		result := s.detectedTool
		s.mu.Unlock()
		return result
	}
	s.mu.Unlock()

	// If a custom tool name is set, return it directly.
	// Custom tools have their underlying command detected at creation time;
	// runtime detection should preserve the custom name.
	s.mu.Lock()
	if s.customToolName != "" {
		s.detectedTool = s.customToolName
		s.toolDetectedAt = time.Now()
		s.mu.Unlock()
		return s.customToolName
	}
	s.mu.Unlock()

	// Detect tool from command first (most reliable)
	if s.Command != "" {
		cmdLower := strings.ToLower(s.Command)
		var tool string
		if strings.Contains(cmdLower, "claude") {
			tool = "claude"
		} else if strings.Contains(cmdLower, "gemini") {
			tool = "gemini"
		} else if strings.Contains(cmdLower, "opencode") || strings.Contains(cmdLower, "open code") {
			tool = "opencode"
		} else if strings.Contains(cmdLower, "codex") {
			tool = "codex"
		}
		if tool != "" {
			s.mu.Lock()
			s.detectedTool = tool
			s.toolDetectedAt = time.Now()
			s.mu.Unlock()
			return tool
		}
	}

	// Fallback to content detection
	content, err := s.CapturePane()
	if err != nil {
		s.mu.Lock()
		s.detectedTool = "shell"
		s.toolDetectedAt = time.Now()
		s.mu.Unlock()
		return "shell"
	}

	// Strip ANSI codes for accurate matching
	cleanContent := StripANSI(content)

	// Check using pre-compiled patterns
	detectedTool := "shell"
	for tool, patterns := range toolDetectionPatterns {
		for _, pattern := range patterns {
			if pattern.MatchString(cleanContent) {
				detectedTool = tool
				break
			}
		}
		if detectedTool != "shell" {
			break
		}
	}

	s.mu.Lock()
	s.detectedTool = detectedTool
	s.toolDetectedAt = time.Now()
	s.mu.Unlock()
	return detectedTool
}

// ForceDetectTool forces a re-detection of the tool, ignoring cache
func (s *Session) ForceDetectTool() string {
	s.mu.Lock()
	s.detectedTool = ""
	s.toolDetectedAt = time.Time{}
	s.mu.Unlock()
	return s.DetectTool()
}

// AcknowledgeWithSnapshot marks the session as seen and baselines the current
// content hash. Called when user detaches from session.
func (s *Session) AcknowledgeWithSnapshot() {
	shortName := s.DisplayName
	if len(shortName) > 12 {
		shortName = shortName[:12]
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.ensureStateTrackerLocked()

	// PERFORMANCE FIX: Skip CapturePane() - it's BLOCKING (200-500ms per call)
	// When user detaches with Ctrl+Q, we don't need to capture fresh content.
	// Instead, we use the last known content from the state tracker.
	// This eliminates 10+ second delays when returning from attached sessions.
	// The next UpdateStatus() poll will capture fresh content anyway.

	// Set acknowledged state immediately without capturing
	s.stateTracker.acknowledged = true
	s.stateTracker.acknowledgedAt = time.Now() // Set grace period start
	s.lastStableStatus = "idle"

	// Clear cooldown to show GRAY status immediately
	// This ensures explicit user acknowledge (Ctrl+Q detach) takes effect immediately
	s.stateTracker.lastChangeTime = time.Now()
	statusLog.Debug("ack_snapshot", slog.String("session", shortName))
}

// GetStatus returns the current status of the session
//
// Activity-based 3-state model with spike filtering:
//
//	GREEN (active)   = Sustained activity (2+ changes in 1s) within cooldown
//	YELLOW (waiting) = Cooldown expired, NOT acknowledged (needs attention)
//	GRAY (idle)      = Cooldown expired, acknowledged (user has seen it)
//
// Key insight: Status bar updates cause single timestamp changes (spikes).
// Real AI work causes multiple timestamp changes over 1 second (sustained).
// This filters spikes to prevent false GREEN flashes.
//
// Logic:
// 1. Check busy indicator (immediate GREEN if present)
// 2. Get activity timestamp (fast ~4ms)
// 3. If timestamp changed → check if sustained or spike
//   - Sustained (1+ more changes in 1s) → GREEN
//   - Spike (no more changes) → filtered (no state change)
//
// 4. Check cooldown → GREEN if within
// 5. Cooldown expired → YELLOW or GRAY based on acknowledged

func (s *Session) GetStatus() (string, error) {
	shortName := s.DisplayName
	if len(shortName) > 12 {
		shortName = shortName[:12]
	}

	if !s.Exists() {
		s.mu.Lock()
		s.lastStableStatus = "inactive"
		s.mu.Unlock()
		statusLog.Debug("session_inactive", slog.String("session", shortName))
		return "inactive", nil
	}

	// FAST PATH: Title-based state detection for Claude Code sessions.
	// Claude Code sets pane titles via OSC sequences: Braille spinner while working,
	// ✳ markers when done. One character check replaces full CapturePane + content scan.
	if paneInfo, ok := GetCachedPaneInfo(s.Name); ok {
		titleState := AnalyzePaneTitle(paneInfo.Title, paneInfo.CurrentCommand)
		switch titleState {
		case TitleStateWorking:
			// Braille spinner in title = actively working. Short-circuit completely.
			s.mu.Lock()
			s.ensureStateTrackerLocked()
			s.stateTracker.lastChangeTime = time.Now()
			s.stateTracker.acknowledged = false
			s.resetPromptNoBusyHoldLocked()
			s.stateTracker.spinnerTracker.MarkBusy()
			s.lastStableStatus = "active"
			s.startupAt = time.Time{}
			s.mu.Unlock()
			statusLog.Debug("title_working", slog.String("session", shortName), slog.String("title", paneInfo.Title))
			return "active", nil

		case TitleStateDone:
			// Done marker, Claude still alive. Fall through to existing detection
			// for waiting vs idle (prompt detection + acknowledgment logic).
			statusLog.Debug("title_done_fallthrough", slog.String("session", shortName))

		default:
			// Unknown title (non-Claude tools). Fall through to full detection.
		}
	}

	// Get current activity timestamp (fast: ~4ms)
	currentTS, err := s.GetWindowActivity()
	if err != nil {
		// Fallback to content-hash based detection
		return s.getStatusFallback()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Skip expensive busy indicator check if no activity change and not in active state
	// This is the key optimization: only call CapturePane() when activity detected
	needsBusyCheck := false
	if s.stateTracker != nil {
		// Check busy indicator if:
		// Check busy indicator if:
		// 1. timestamp changed (new activity)
		// 2. in spike detection window (activity recently detected, waiting to confirm)
		inSpikeWindow := !s.stateTracker.activityCheckStart.IsZero() &&
			time.Since(s.stateTracker.activityCheckStart) < 1*time.Second
		if s.stateTracker.lastActivityTimestamp != currentTS || inSpikeWindow {
			needsBusyCheck = true
		}
	} else {
		// First call - check for busy indicator
		needsBusyCheck = true
	}

	if needsBusyCheck {
		// Release lock for slow CapturePane operation
		s.mu.Unlock()
		content, err := s.CapturePane()
		s.mu.Lock()

		if errors.Is(err, ErrCaptureTimeout) {
			// Timeout: preserve previous state to avoid false RED flashing
			if s.lastStableStatus != "" {
				statusLog.Debug("capture_timeout_preserve", slog.String("session", shortName), slog.String("status", s.lastStableStatus))
				return s.lastStableStatus, nil
			}
			// No previous state, fall through to default logic
			statusLog.Debug("capture_timeout_no_previous", slog.String("session", shortName))
		} else if err == nil {
			s.ensureStateTrackerLocked()

			// Check for explicit busy indicator (spinner, "ctrl+c to interrupt")
			isExplicitlyBusy := s.hasBusyIndicator(content)
			// Debug: show last line of content for this session
			lines := strings.Split(content, "\n")
			lastLine := ""
			for i := len(lines) - 1; i >= 0; i-- {
				if strings.TrimSpace(lines[i]) != "" {
					lastLine = lines[i]
					if len(lastLine) > 60 {
						lastLine = lastLine[:60] + "..."
					}
					break
				}
			}
			statusLog.Debug("needs_busy_check", slog.String("session", shortName), slog.Bool("busy", isExplicitlyBusy), slog.String("last_line", lastLine))

			// Check for prompt indicators (AskUserQuestion, permission dialogs, etc.)
			// BUSY indicator is AUTHORITATIVE: if spinner is active (or in grace period),
			// return GREEN immediately. Prompt detection must NOT override this because
			// the ❯ prompt from the user's previous input is always visible and causes
			// false "waiting" detection during tool transitions.
			if isExplicitlyBusy {
				s.stateTracker.lastChangeTime = time.Now()
				s.stateTracker.acknowledged = false
				s.resetPromptNoBusyHoldLocked()
				s.stateTracker.lastActivityTimestamp = currentTS
				s.lastStableStatus = "active"
				s.startupAt = time.Time{}
				statusLog.Debug("busy_indicator_active", slog.String("session", shortName))
				return "active", nil
			}

			// Update content hash for spike detection (deferred until after early return above).
			// The 500ms CapturePane cache means the spike path gets the same content,
			// so we store the normalized result once and reuse it via cachedNormContent.
			cleanContent := s.normalizeContent(content)
			currentHash := s.hashContent(cleanContent)
			if currentHash != "" {
				// Keep the content hash for diagnostics/fallback logic only.
				// Do NOT clear acknowledgment on hash changes: dynamic footer text
				// (timers, context counters, redraws) can mutate content without any
				// real new work and causes idle -> waiting flapping.
				s.stateTracker.lastHash = currentHash
			}

			// Not busy. Check for prompt indicators to distinguish YELLOW vs fall-through.
			hasPrompt := s.hasPromptIndicator(content)
			if hasPrompt {
				// Respect acknowledgment: if user already acknowledged (e.g. by attaching),
				// keep idle status. The prompt is still visible but the user is looking at it.
				if s.stateTracker.acknowledged {
					s.resetPromptNoBusyHoldLocked()
					s.lastStableStatus = "idle"
					s.startupAt = time.Time{}
					statusLog.Debug("prompt_detected_idle", slog.String("session", shortName))
					return "idle", nil
				}
				if s.shouldHoldActiveOnPromptLocked() {
					s.startupAt = time.Time{}
					statusLog.Debug("prompt_no_busy_hold_active",
						slog.String("session", shortName),
						slog.Int("count", s.stateTracker.promptNoBusyCount))
					return "active", nil
				}
				s.resetPromptNoBusyHoldLocked()
				if s.lastStableStatus != "waiting" {
					s.stateTracker.waitingSince = time.Now()
				}
				s.lastStableStatus = "waiting"
				s.startupAt = time.Time{}
				statusLog.Debug("prompt_detected_waiting", slog.String("session", shortName))
				return "waiting", nil
			}

			// During startup there may be a long period with neither spinner nor prompt.
			// Keep this as STARTING to avoid premature waiting/idle transitions.
			if s.inStartupWindowLocked() {
				s.resetPromptNoBusyHoldLocked()
				s.lastStableStatus = "starting"
				statusLog.Debug("startup_no_prompt_or_busy", slog.String("session", shortName))
				return "starting", nil
			}
			s.resetPromptNoBusyHoldLocked()
		}
	}

	// Initialize on first call
	if s.stateTracker == nil {
		now := time.Now()
		s.stateTracker = &StateTracker{
			lastChangeTime:        now,
			acknowledged:          false, // Start unacknowledged so stopped sessions show YELLOW
			lastActivityTimestamp: currentTS,
			waitingSince:          now, // Track when session became waiting
			spinnerTracker:        NewSpinnerActivityTracker(),
		}
		if s.inStartupWindowLocked() {
			s.lastStableStatus = "starting"
			statusLog.Debug("init_starting", slog.String("session", shortName))
			return "starting", nil
		}
		s.lastStableStatus = "waiting"
		statusLog.Debug("init_waiting", slog.String("session", shortName))
		return "waiting", nil
	}

	// Restored session (lastActivityTimestamp == 0)
	if s.stateTracker.lastActivityTimestamp == 0 {
		s.stateTracker.lastActivityTimestamp = currentTS
		if s.inStartupWindowLocked() {
			s.lastStableStatus = "starting"
			statusLog.Debug("restored_starting", slog.String("session", shortName))
			return "starting", nil
		}
		if s.stateTracker.acknowledged {
			s.lastStableStatus = "idle"
			statusLog.Debug("restored_idle", slog.String("session", shortName))
			return "idle", nil
		}
		if s.lastStableStatus != "waiting" {
			s.stateTracker.waitingSince = time.Now()
		}
		s.lastStableStatus = "waiting"
		statusLog.Debug("restored_waiting", slog.String("session", shortName))
		return "waiting", nil
	}

	// Activity timestamp changed → non-blocking spike detection across tick cycles
	if s.stateTracker.lastActivityTimestamp != currentTS {
		oldTS := s.stateTracker.lastActivityTimestamp
		s.stateTracker.lastActivityTimestamp = currentTS

		// Check if we're in a detection window
		const spikeWindow = 1 * time.Second
		now := time.Now()

		if s.stateTracker.activityCheckStart.IsZero() || now.Sub(s.stateTracker.activityCheckStart) > spikeWindow {
			// Start new detection window
			s.stateTracker.activityCheckStart = now
			s.stateTracker.activityChangeCount = 1
			statusLog.Debug("activity_start", slog.String("session", shortName), slog.Int64("old_ts", oldTS), slog.Int64("new_ts", currentTS), slog.Int("count", 1))
		} else {
			// Within detection window - count this change
			s.stateTracker.activityChangeCount++
			statusLog.Debug("activity_count", slog.String("session", shortName), slog.Int64("old_ts", oldTS), slog.Int64("new_ts", currentTS), slog.Int("count", s.stateTracker.activityChangeCount))

			// 2+ changes within 1 second = potential sustained activity
			// BUT we must confirm with content check (fixes cursor blink false positives)
			if s.stateTracker.activityChangeCount >= 2 {
				// Gate the spike: confirm with content check before setting GREEN
				s.mu.Unlock()
				content, captureErr := s.CapturePane()
				s.mu.Lock()

				if captureErr == nil {
					// Check for explicit busy indicator (spinner, "ctrl+c to interrupt")
					isExplicitlyBusy := s.hasBusyIndicator(content)

					// Only GREEN if explicit busy indicator found
					// Content hash changes alone are NOT reliable - cursor blinks,
					// terminal redraws, and status bar updates can cause hash changes
					if isExplicitlyBusy {
						s.stateTracker.lastChangeTime = now
						s.stateTracker.acknowledged = false
						s.resetPromptNoBusyHoldLocked()
						s.stateTracker.activityCheckStart = time.Time{} // Reset window
						s.stateTracker.activityChangeCount = 0
						s.lastStableStatus = "active"
						s.startupAt = time.Time{}
						statusLog.Debug("sustained_confirmed", slog.String("session", shortName))
						return "active", nil
					}

					// Not busy - update hash for tracking (deferred past the early return above)
					cleanContent := s.normalizeContent(content)
					currentHash := s.hashContent(cleanContent)
					if currentHash != "" {
						// Hash changes alone are not enough to clear acknowledgment.
						s.stateTracker.lastHash = currentHash
					}

					if s.hasPromptIndicator(content) {
						if s.stateTracker.acknowledged {
							s.resetPromptNoBusyHoldLocked()
							s.lastStableStatus = "idle"
							s.startupAt = time.Time{}
							statusLog.Debug("sustained_prompt_idle", slog.String("session", shortName))
							s.stateTracker.activityCheckStart = time.Time{}
							s.stateTracker.activityChangeCount = 0
							return "idle", nil
						}
						if s.shouldHoldActiveOnPromptLocked() {
							s.startupAt = time.Time{}
							statusLog.Debug("sustained_prompt_hold_active",
								slog.String("session", shortName),
								slog.Int("count", s.stateTracker.promptNoBusyCount))
							s.stateTracker.activityCheckStart = time.Time{}
							s.stateTracker.activityChangeCount = 0
							return "active", nil
						}
						s.resetPromptNoBusyHoldLocked()
						if s.lastStableStatus != "waiting" {
							s.stateTracker.waitingSince = time.Now()
						}
						s.lastStableStatus = "waiting"
						s.startupAt = time.Time{}
						statusLog.Debug("sustained_prompt_waiting", slog.String("session", shortName))
						s.stateTracker.activityCheckStart = time.Time{}
						s.stateTracker.activityChangeCount = 0
						return "waiting", nil
					}

					// No busy indicator - spike was false positive (cursor blink, status bar, etc.)
					statusLog.Debug("sustained_rejected", slog.String("session", shortName))
				}

				// Reset spike tracking - the activity was not real
				s.stateTracker.activityCheckStart = time.Time{}
				s.stateTracker.activityChangeCount = 0
			}
		}
		// Not enough changes yet - continue with current status (don't block)
	} else {
		// No timestamp change - check if spike window expired with only 1 change
		if s.stateTracker.activityChangeCount == 1 && !s.stateTracker.activityCheckStart.IsZero() {
			if time.Since(s.stateTracker.activityCheckStart) > 1*time.Second {
				// Only 1 change in 1 second = spike, reset tracking
				statusLog.Debug("spike_expired", slog.String("session", shortName), slog.Int("count", 1))
				s.stateTracker.activityCheckStart = time.Time{}
				s.stateTracker.activityChangeCount = 0
			}
		}
	}

	// During spike detection window (waiting to confirm sustained activity),
	// keep the PREVIOUS stable status instead of flashing GREEN
	// Only confirmed sustained activity (2+ changes in 1s) triggers GREEN
	if !s.stateTracker.activityCheckStart.IsZero() &&
		time.Since(s.stateTracker.activityCheckStart) < 1*time.Second {
		// Return previous status - don't flash GREEN on unconfirmed single spike
		statusLog.Debug("spike_window_pending", slog.String("session", shortName), slog.String("status", s.lastStableStatus))
		if s.lastStableStatus != "" {
			return s.lastStableStatus, nil
		}
		// Fallback if no previous status
		statusLog.Debug("spike_window_fallback_waiting", slog.String("session", shortName))
		return "waiting", nil
	}

	// If we were previously active but skipped the busy check (no timestamp change),
	// verify before transitioning away from GREEN - the session might still be busy
	if s.lastStableStatus == "active" && !needsBusyCheck {
		// Re-check busy indicator before dropping out of GREEN
		s.mu.Unlock()
		content, captureErr := s.CapturePane()
		s.mu.Lock()
		if captureErr == nil && s.hasBusyIndicator(content) {
			// Busy indicator is authoritative (includes spinner grace period).
			s.resetPromptNoBusyHoldLocked()
			s.startupAt = time.Time{}
			statusLog.Debug("still_busy", slog.String("session", shortName))
			return "active", nil
		}
		if captureErr == nil && s.hasPromptIndicator(content) {
			// Not busy, but prompt visible. Transition to waiting/idle.
			if !s.stateTracker.acknowledged {
				if s.shouldHoldActiveOnPromptLocked() {
					s.startupAt = time.Time{}
					statusLog.Debug("prompt_recheck_hold_active",
						slog.String("session", shortName),
						slog.Int("count", s.stateTracker.promptNoBusyCount))
					return "active", nil
				}
				s.resetPromptNoBusyHoldLocked()
				if s.lastStableStatus != "waiting" {
					s.stateTracker.waitingSince = time.Now()
				}
				s.lastStableStatus = "waiting"
				s.startupAt = time.Time{}
				statusLog.Debug("prompt_recheck_waiting", slog.String("session", shortName))
				return "waiting", nil
			}
			s.resetPromptNoBusyHoldLocked()
			s.lastStableStatus = "idle"
			s.startupAt = time.Time{}
			statusLog.Debug("prompt_recheck_idle", slog.String("session", shortName))
			return "idle", nil
		}
		statusLog.Debug("no_longer_busy", slog.String("session", shortName))
	}

	// No busy indicator found - check acknowledged state
	if s.stateTracker.acknowledged {
		s.resetPromptNoBusyHoldLocked()
		s.lastStableStatus = "idle"
		s.startupAt = time.Time{}
		statusLog.Debug("idle_acknowledged", slog.String("session", shortName))
		return "idle", nil
	}
	if s.inStartupWindowLocked() {
		s.resetPromptNoBusyHoldLocked()
		s.lastStableStatus = "starting"
		statusLog.Debug("startup_pending", slog.String("session", shortName))
		return "starting", nil
	}
	s.resetPromptNoBusyHoldLocked()
	// Track when we transition to waiting (not already waiting)
	if s.lastStableStatus != "waiting" {
		s.stateTracker.waitingSince = time.Now()
	}
	s.lastStableStatus = "waiting"
	s.startupAt = time.Time{}
	statusLog.Debug("waiting_not_acknowledged", slog.String("session", shortName))
	return "waiting", nil
}

// getStatusFallback uses content-hash based detection as fallback
// when activity timestamp detection fails
func (s *Session) getStatusFallback() (string, error) {
	shortName := s.DisplayName
	if len(shortName) > 12 {
		shortName = shortName[:12]
	}

	content, err := s.CapturePane()
	if err != nil {
		if errors.Is(err, ErrCaptureTimeout) {
			// Timeout: preserve previous state instead of going inactive
			s.mu.Lock()
			prev := s.lastStableStatus
			s.mu.Unlock()
			if prev != "" {
				statusLog.Debug("fallback_timeout_preserve", slog.String("session", shortName), slog.String("status", prev))
				return prev, nil
			}
		}
		s.mu.Lock()
		s.lastStableStatus = "inactive"
		s.mu.Unlock()
		statusLog.Debug("fallback_inactive", slog.String("session", shortName), slog.String("error", err.Error()))
		return "inactive", nil
	}

	// Keep precedence aligned with the main path:
	// 1) busy (authoritative), 2) prompt, 3) waiting/idle.
	if s.hasBusyIndicator(content) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.ensureStateTrackerLocked()
		s.stateTracker.lastChangeTime = time.Now()
		s.stateTracker.acknowledged = false
		s.resetPromptNoBusyHoldLocked()
		s.lastStableStatus = "active"
		s.startupAt = time.Time{}
		statusLog.Debug("fallback_active", slog.String("session", shortName))
		return "active", nil
	}

	if s.hasPromptIndicator(content) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.ensureStateTrackerLocked()
		if s.stateTracker.acknowledged {
			s.resetPromptNoBusyHoldLocked()
			s.lastStableStatus = "idle"
			s.startupAt = time.Time{}
			statusLog.Debug("fallback_idle_prompt_ack", slog.String("session", shortName))
			return "idle", nil
		}
		if s.shouldHoldActiveOnPromptLocked() {
			s.startupAt = time.Time{}
			statusLog.Debug("fallback_prompt_hold_active",
				slog.String("session", shortName),
				slog.Int("count", s.stateTracker.promptNoBusyCount))
			return "active", nil
		}
		s.resetPromptNoBusyHoldLocked()
		s.stateTracker.acknowledged = false
		if s.lastStableStatus != "waiting" {
			s.stateTracker.waitingSince = time.Now()
		}
		s.lastStableStatus = "waiting"
		s.startupAt = time.Time{}
		statusLog.Debug("fallback_waiting_prompt", slog.String("session", shortName))
		return "waiting", nil
	}

	cleanContent := s.normalizeContent(content)
	currentHash := s.hashContent(cleanContent)
	if currentHash == "" {
		currentHash = "__empty__"
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stateTracker == nil {
		now := time.Now()
		s.stateTracker = &StateTracker{
			lastHash:       currentHash,
			lastChangeTime: now,
			acknowledged:   false, // Start unacknowledged so stopped sessions show YELLOW
			waitingSince:   now,   // Track when session became waiting
		}
		if s.inStartupWindowLocked() {
			s.lastStableStatus = "starting"
			statusLog.Debug("fallback_init_starting", slog.String("session", shortName))
			return "starting", nil
		}
		s.lastStableStatus = "waiting"
		statusLog.Debug("fallback_init_waiting", slog.String("session", shortName))
		return "waiting", nil
	}

	if s.stateTracker.lastHash == "" {
		s.stateTracker.lastHash = currentHash
		if s.inStartupWindowLocked() {
			s.lastStableStatus = "starting"
			statusLog.Debug("fallback_restored_starting", slog.String("session", shortName))
			return "starting", nil
		}
		if s.stateTracker.acknowledged {
			s.lastStableStatus = "idle"
			s.startupAt = time.Time{}
			statusLog.Debug("fallback_restored_idle", slog.String("session", shortName))
			return "idle", nil
		}
		if s.lastStableStatus != "waiting" {
			s.stateTracker.waitingSince = time.Now()
		}
		s.lastStableStatus = "waiting"
		s.startupAt = time.Time{}
		statusLog.Debug("fallback_restored_waiting", slog.String("session", shortName))
		return "waiting", nil
	}

	// Update hash for tracking, but do NOT trigger GREEN based on hash change alone
	// The busy indicator check above (hasBusyIndicator) already handles the GREEN case
	// Hash changes can occur from cursor blinks, terminal redraws, status bar updates, etc.
	if s.stateTracker.lastHash != currentHash {
		s.stateTracker.lastHash = currentHash
		statusLog.Debug("fallback_hash_updated", slog.String("session", shortName))
	}

	// No busy indicator found - check acknowledged state
	if s.stateTracker.acknowledged {
		s.lastStableStatus = "idle"
		s.startupAt = time.Time{}
		statusLog.Debug("fallback_idle_acknowledged", slog.String("session", shortName))
		return "idle", nil
	}
	if s.inStartupWindowLocked() {
		s.lastStableStatus = "starting"
		statusLog.Debug("fallback_starting_pending", slog.String("session", shortName))
		return "starting", nil
	}
	// Track when we transition to waiting (not already waiting)
	if s.lastStableStatus != "waiting" {
		s.stateTracker.waitingSince = time.Now()
	}
	s.lastStableStatus = "waiting"
	s.startupAt = time.Time{}
	statusLog.Debug("fallback_waiting_not_acknowledged", slog.String("session", shortName))
	return "waiting", nil
}

// Acknowledge marks the session as "seen" by the user
// Call this when user attaches to the session
func (s *Session) Acknowledge() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.ensureStateTrackerLocked()
	s.stateTracker.acknowledged = true
	s.resetPromptNoBusyHoldLocked()
	s.lastStableStatus = "idle"
}

// ResetAcknowledged marks the session as needing attention
// Call this when a hook event indicates the agent finished (Stop, AfterAgent)
// This ensures the session shows yellow (waiting) instead of gray (idle)
func (s *Session) ResetAcknowledged() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.ensureStateTrackerLocked()
	s.stateTracker.acknowledged = false
	s.resetPromptNoBusyHoldLocked()
	s.stateTracker.waitingSince = time.Now() // Track when session became waiting for ordering
	s.lastStableStatus = "waiting"
}

// ApplySharedAcknowledged applies acknowledgment state replicated from SQLite.
// Unlike Acknowledge/ResetAcknowledged, this only synchronizes the ack flag and
// does not force an immediate status transition. GetStatus() will naturally map
// to waiting/idle on the next poll based on busy/prompt conditions.
func (s *Session) ApplySharedAcknowledged(ack bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stateTracker == nil {
		// No local state yet; ack=false is already the default behavior.
		if !ack {
			return
		}
		s.ensureStateTrackerLocked()
	} else if s.stateTracker.spinnerTracker == nil {
		s.stateTracker.spinnerTracker = NewSpinnerActivityTracker()
	}

	s.stateTracker.acknowledged = ack
	s.resetPromptNoBusyHoldLocked()
	if ack {
		s.stateTracker.acknowledgedAt = time.Now()
	}
}

// GetLastActivityTime returns when the session content last changed
// Returns zero time if no activity has been tracked
func (s *Session) GetLastActivityTime() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stateTracker == nil {
		return time.Time{}
	}
	return s.stateTracker.lastChangeTime
}

// GetWaitingSince returns when the session transitioned to waiting status
// Returns zero time if session has never been waiting
func (s *Session) GetWaitingSince() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stateTracker == nil {
		return time.Time{}
	}
	return s.stateTracker.waitingSince
}

// hasBusyIndicator checks if the terminal shows explicit busy indicators.
// Now uses spinner movement detection for all paths (experiment).
func (s *Session) hasBusyIndicator(content string) bool {
	// Always use spinner movement detection regardless of resolvedPatterns
	return s.hasBusyIndicatorResolved(content)
}

var defaultResolvedPatternsCache sync.Map // map[string]*ResolvedPatterns

func inferToolFromSessionFields(detected, custom, command string) string {
	if detected != "" {
		return strings.ToLower(detected)
	}
	if custom != "" {
		return strings.ToLower(custom)
	}
	cmd := strings.ToLower(command)
	switch {
	case strings.Contains(cmd, "claude"):
		return "claude"
	case strings.Contains(cmd, "gemini"):
		return "gemini"
	case strings.Contains(cmd, "opencode"), strings.Contains(cmd, "open code"):
		return "opencode"
	case strings.Contains(cmd, "codex"):
		return "codex"
	default:
		return ""
	}
}

func defaultResolvedPatternsForTool(tool string) *ResolvedPatterns {
	tool = strings.ToLower(strings.TrimSpace(tool))
	if tool == "" {
		return nil
	}
	if cached, ok := defaultResolvedPatternsCache.Load(tool); ok {
		if rp, ok := cached.(*ResolvedPatterns); ok {
			return rp
		}
	}

	raw := DefaultRawPatterns(tool)
	if raw == nil {
		return nil
	}
	resolved, err := CompilePatterns(raw)
	if err != nil {
		return nil
	}
	if actual, loaded := defaultResolvedPatternsCache.LoadOrStore(tool, resolved); loaded {
		if rp, ok := actual.(*ResolvedPatterns); ok {
			return rp
		}
	}
	return resolved
}

func hasInterruptBusyContext(lines []string, phrase string, spinnerChars []string) bool {
	phrase = strings.ToLower(strings.TrimSpace(phrase))
	if phrase == "" {
		return false
	}

	for _, line := range lines {
		clean := strings.ToLower(strings.TrimSpace(StripANSI(line)))
		if !strings.Contains(clean, phrase) {
			continue
		}

		// Exact interrupt prompt line (older tool variants).
		if clean == phrase {
			return true
		}

		// Common busy-line context for Claude/Gemini style status lines.
		if strings.Contains(clean, "(") ||
			strings.Contains(clean, "tokens") ||
			strings.Contains(clean, "thinking") ||
			strings.Contains(clean, "…") ||
			strings.Contains(clean, "·") {
			return true
		}

		// Spinner char present on same line is also sufficient.
		for _, ch := range spinnerChars {
			if strings.Contains(clean, strings.ToLower(ch)) {
				return true
			}
		}
	}

	return false
}

// hasBusyIndicatorResolved detects active work with a pattern-first strategy:
//  1. Busy regex/string patterns (tool-specific, e.g. Claude ellipsis/interrupt lines)
//  2. Spinner fallback (strict for Claude; permissive for other tools)
//  3. Grace period between tool-call transitions
//
// This avoids false GREEN from decorative symbols or status/footer redraws.
func (s *Session) hasBusyIndicatorResolved(content string) bool {
	shortName := s.DisplayName
	if len(shortName) > 12 {
		shortName = shortName[:12]
	}

	tool := inferToolFromSessionFields(s.detectedTool, s.customToolName, s.Command)
	patterns := s.resolvedPatterns
	if patterns == nil {
		patterns = defaultResolvedPatternsForTool(tool)
	}

	// Get spinner chars from resolved/default patterns, fallback to defaults
	spinnerChars := defaultSpinnerChars()
	if patterns != nil && len(patterns.SpinnerChars) > 0 {
		spinnerChars = patterns.SpinnerChars
	}

	// Find spinner in terminal content
	char, spinnerLine, found := findSpinnerInContent(content, spinnerChars)

	// Get or create spinner tracker
	s.ensureStateTrackerLocked()
	tracker := s.stateTracker.spinnerTracker

	// BusyPatterns (regex + string) are authoritative because they capture
	// real active-line semantics for each tool.
	if patterns != nil {
		recentLines := lastNLines(content, 25)
		recentContent := strings.Join(recentLines, "\n")
		for _, re := range patterns.BusyRegexps {
			if re.MatchString(recentContent) {
				tracker.MarkBusy()
				statusLog.Debug("busy_pattern_match", slog.String("session", shortName), slog.String("pattern", re.String()))
				return true
			}
		}
		lowerContent := strings.ToLower(recentContent)
		for _, str := range patterns.BusyStrings {
			lowerStr := strings.ToLower(str)
			if !strings.Contains(lowerContent, lowerStr) {
				continue
			}
			if strings.Contains(lowerStr, "interrupt") &&
				!hasInterruptBusyContext(recentLines, lowerStr, spinnerChars) {
				statusLog.Debug("busy_string_ignored_no_context",
					slog.String("session", shortName),
					slog.String("pattern", str))
				continue
			}
			tracker.MarkBusy()
			statusLog.Debug("busy_string_match", slog.String("session", shortName), slog.String("pattern", str))
			return true
		}
	}
	isClaude := strings.EqualFold(tool, "claude")

	if found {
		// For Claude, braille spinner frames are authoritative.
		// Asterisk-style frames can appear in non-active contexts, so require context.
		lineClean := StripANSI(spinnerLine)
		lineLower := strings.ToLower(lineClean)
		hasActiveContext := strings.Contains(lineClean, "…") || strings.Contains(lineLower, "interrupt")
		if !isClaude || isBrailleSpinnerChar(char) || hasActiveContext {
			tracker.MarkBusy()
			statusLog.Debug("busy_spinner_found", slog.String("session", shortName), slog.String("char", char))
			return true
		}
		statusLog.Debug("busy_spinner_ignored_no_active_context",
			slog.String("session", shortName),
			slog.String("char", char))
	}

	// No busy signal. Check grace period: between tool calls the spinner
	// briefly disappears. If it was visible recently, stay busy.
	if tracker.InGracePeriod() {
		statusLog.Debug("busy_spinner_grace", slog.String("session", shortName),
			slog.Duration("since_busy", time.Since(tracker.lastBusyTime)))
		return true
	}

	statusLog.Debug("busy_no_spinner", slog.String("session", shortName))
	return false
}

// hasPromptIndicator checks if the terminal shows a prompt waiting for user input.
// Uses the PromptDetector which understands tool-specific prompt patterns (permission
// dialogs, AskUserQuestion UI, input prompts, etc.). Prompt detection takes priority
// over busy indicators because tools can show status text alongside interactive prompts.
//
// NOTE: This method reads s.detectedTool and s.customToolName without locking.
// Callers in GetStatus() already hold s.mu, so we must not re-lock.
func (s *Session) hasPromptIndicator(content string) bool {
	tool := inferToolFromSessionFields(s.detectedTool, s.customToolName, s.Command)
	patterns := s.resolvedPatterns
	if patterns == nil {
		patterns = defaultResolvedPatternsForTool(tool)
	}

	// Configured prompt patterns are checked first so custom tool definitions and
	// per-tool overrides can participate in waiting-state detection.
	if patterns != nil {
		recentLines := lastNLines(content, 25)
		recentContent := strings.Join(recentLines, "\n")
		for _, re := range patterns.PromptRegexps {
			if re.MatchString(recentContent) {
				return true
			}
		}
		lowerContent := strings.ToLower(recentContent)
		for _, str := range patterns.PromptStrings {
			if strings.Contains(lowerContent, strings.ToLower(str)) {
				return true
			}
		}
	}
	if tool == "" {
		return false
	}
	// Reuse cached detector if tool hasn't changed (avoids allocation per call)
	if s.cachedPromptDetector == nil || s.cachedPromptDetectorTool != tool {
		s.cachedPromptDetector = NewPromptDetector(tool)
		s.cachedPromptDetectorTool = tool
	}
	return s.cachedPromptDetector.HasPrompt(content)
}

// lastNLines splits content into lines, trims trailing blank lines, and returns
// the last n lines. Used by busy/prompt detection to focus on recent terminal output.
func lastNLines(content string, n int) []string {
	lines := strings.Split(content, "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	start := len(lines) - n
	if start < 0 {
		start = 0
	}
	return lines[start:]
}

// startsWithBoxDrawing checks if a line starts with box-drawing characters (UI borders).
func startsWithBoxDrawing(line string) bool {
	trimmedLine := strings.TrimSpace(line)
	if len(trimmedLine) == 0 {
		return false
	}
	r := []rune(trimmedLine)[0]
	return r == '│' || r == '├' || r == '└' || r == '─' || r == '┌' || r == '┐' || r == '┘' || r == '┤' || r == '┬' || r == '┴' || r == '┼' || r == '╭' || r == '╰' || r == '╮' || r == '╯'
}

// isSustainedActivity checks if activity is sustained (real work) or a spike.
// Checks 5 times over 1 second, counts timestamp changes.
// Returns true if 1+ changes detected AFTER initial check (sustained activity).
// Returns false if no additional changes (spike - status bar update, etc).
//
// This filters out false positives from:
// - Status bar time updates (e.g., Claude Code's auto-compact %)
// - Single cursor movements
// - Terminal refresh events
func (s *Session) isSustainedActivity() bool {
	const (
		checkCount    = 5
		checkInterval = 200 * time.Millisecond
	)

	prevTS, err := s.GetWindowActivity()
	if err != nil {
		return false
	}

	changes := 0
	for i := 0; i < checkCount; i++ {
		time.Sleep(checkInterval)
		currentTS, err := s.GetWindowActivity()
		if err != nil {
			continue
		}
		if currentTS != prevTS {
			changes++
			prevTS = currentTS
		}
	}

	isSustained := changes >= 1 // At least 1 MORE change after initial detection
	statusLog.Debug("is_sustained_activity", slog.String("session", s.DisplayName), slog.Int("changes", changes), slog.Bool("sustained", isSustained))
	return isSustained
}

// Precompiled regex patterns for dynamic content stripping
// These are compiled once at package init for performance
var (
	// Matches Claude Code status line: "(45s · 1234 tokens · ctrl+c to interrupt)" and "(35s · ↑ 673 tokens)"
	dynamicStatusPattern = regexp.MustCompile(`\([^)]*\d+s\s*·[^)]*(?:tokens|↑|↓)[^)]*\)`)

	// Claude Code 2.1.25+ active spinner: symbol + unicode ellipsis (U+2026)
	// Matches: "✳ Gusting…", "✻ Adding mcp-proxy subcommand…" (single or multi-word)
	// Does NOT match done state: "✻ Worked for 54s" (no ellipsis)
	claudeSpinnerActivePattern = regexp.MustCompile(`[·✳✽✶✻✢]\s*.+…`)

	// Matches whimsical thinking words with timing info (e.g., "⠋ Flibbertigibbeting... (25s · 340 tokens)")
	// Requires spinner prefix to avoid matching normal English words like "processing" or "computing"
	// Updated to include all 90 Claude Code whimsical words
	thinkingPattern = regexp.MustCompile(`[⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏·✳✽✶✻✢]\s*(?i)(` + whimsicalWordsPattern + `)[^(]*\([^)]*\)`)

	// Claude 2.1.25+ uses unicode ellipsis: "✳ Gusting… (35s · ↑ 673 tokens)"
	// Word-list independent - any spinner + text + ellipsis + parenthesized status
	thinkingPatternEllipsis = regexp.MustCompile(`[⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏·✳✽✶✻✢]\s*.+…\s*\([^)]*\)`)

	// Progress bar patterns for normalization (Fix 2.1)
	// These cause hash changes when progress updates
	progressBarPattern = regexp.MustCompile(`\[=*>?\s*\]\s*\d+%`)                  // [====>   ] 45%
	downloadPattern    = regexp.MustCompile(`\d+\.?\d*[KMGT]?B/\d+\.?\d*[KMGT]?B`) // 1.2MB/5.6MB
	percentagePattern  = regexp.MustCompile(`\b\d{1,3}%`)                          // 45% (word boundary to avoid false matches)

	// Time patterns like "12:34" or "12:34:56" that change every second
	// Gemini and other tools show timestamps that cause hash changes
	timePattern = regexp.MustCompile(`\b\d{1,2}:\d{2}(?::\d{2})?\b`)

	// Collapses runs of 3+ newlines to 2 newlines (one blank line)
	blankLinesPattern = regexp.MustCompile(`\n{3,}`)
)

// claudeWhimsicalWords contains all 90 whimsical "thinking" words used by Claude Code
// Source: https://github.com/levindixon/tengu_spinner_words
// These words appear as status messages like "Flibbertigibbeting... (25s · 340 tokens)"
var claudeWhimsicalWords = []string{
	"accomplishing", "actioning", "actualizing", "baking", "booping",
	"brewing", "calculating", "cerebrating", "channelling", "churning",
	"clauding", "coalescing", "cogitating", "combobulating", "computing",
	"concocting", "conjuring", "considering", "contemplating", "cooking",
	"crafting", "creating", "crunching", "deciphering", "deliberating",
	"determining", "discombobulating", "divining", "doing", "effecting",
	"elucidating", "enchanting", "envisioning", "finagling", "flibbertigibbeting",
	"forging", "forming", "frolicking", "generating", "germinating",
	"hatching", "herding", "honking", "hustling", "ideating",
	"imagining", "incubating", "inferring", "jiving", "manifesting",
	"marinating", "meandering", "moseying", "mulling", "mustering",
	"musing", "noodling", "percolating", "perusing", "philosophising",
	"pondering", "pontificating", "processing", "puttering", "puzzling",
	"reticulating", "ruminating", "scheming", "schlepping", "shimmying",
	"shucking", "simmering", "smooshing", "spelunking", "spinning",
	"stewing", "sussing", "synthesizing", "thinking", "tinkering",
	"transmuting", "unfurling", "unravelling", "vibing", "wandering",
	"whirring", "wibbling", "wizarding", "working", "wrangling",
	// Claude Code 2.1.25+ additions
	"billowing", "gusting", "metamorphosing", "sublimating", "recombobulating", "sautéing",
}

// whimsicalWordsPattern is the regex alternation of all whimsical words
// Built at init time for performance
var whimsicalWordsPattern = strings.Join(claudeWhimsicalWords, "|")

// normalizeContent strips ANSI codes, spinner characters, and normalizes whitespace
// This is critical for stable hashing - prevents flickering from:
// 1. Color/style changes in terminal output
// 2. Animated spinner characters (⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏)
// 3. Other non-printing control characters
// 4. Terminal resize (which can add trailing spaces with tmux -J flag)
// 5. Multiple consecutive blank lines
// 6. Dynamic time/token counters (e.g., "45s · 1234 tokens")
func (s *Session) normalizeContent(content string) string {
	// Strip ANSI escape codes first (handles CSI, OSC, and C1 codes)
	result := StripANSI(content)

	// Strip other non-printing control characters
	result = stripControlChars(result)

	// Strip spinner characters that animate and cause hash changes
	// Single-pass O(n) removal using map lookup instead of 16 sequential ReplaceAll calls
	result = StripSpinnerRunes(result)

	// Strip dynamic time/token counters that change every second
	result = dynamicStatusPattern.ReplaceAllString(result, "(STATUS)")

	// Use resolved combo patterns when available, otherwise fall back to package-level patterns
	if s.resolvedPatterns != nil && s.resolvedPatterns.ThinkingPattern != nil {
		result = s.resolvedPatterns.ThinkingPattern.ReplaceAllString(result, "$1...")
	} else {
		result = thinkingPattern.ReplaceAllString(result, "$1...")
	}
	if s.resolvedPatterns != nil && s.resolvedPatterns.ThinkingPatternEllipsis != nil {
		result = s.resolvedPatterns.ThinkingPatternEllipsis.ReplaceAllString(result, "THINKING…")
	} else {
		result = thinkingPatternEllipsis.ReplaceAllString(result, "THINKING…")
	}

	// Strip progress indicators that change frequently (Fix 2.1)
	// These cause hash changes during downloads, builds, etc.
	result = progressBarPattern.ReplaceAllString(result, "[PROGRESS]") // [====>   ] 45%
	result = downloadPattern.ReplaceAllString(result, "X.XMB/Y.YMB")   // 1.2MB/5.6MB
	result = percentagePattern.ReplaceAllString(result, "N%")          // 45%

	// Normalize time patterns (12:34 or 12:34:56) that change every second
	result = timePattern.ReplaceAllString(result, "HH:MM:SS")

	// Normalize trailing whitespace per line (fixes resize false positives)
	// tmux capture-pane -J can add trailing spaces when terminal is resized
	lines := strings.Split(result, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	result = strings.Join(lines, "\n")

	// Normalize multiple consecutive blank lines to a single blank line
	// This prevents hash changes from cursor position variations
	result = normalizeBlankLines(result)

	return result
}

// normalizeBlankLines collapses runs of 3+ newlines to 2 newlines (one blank line)
func normalizeBlankLines(content string) string {
	return blankLinesPattern.ReplaceAllString(content, "\n\n")
}

// stripControlChars removes all ASCII control characters except for tab, newline,
// and carriage return. This helps stabilize content for hashing.
func stripControlChars(content string) string {
	var result strings.Builder
	result.Grow(len(content))
	for _, r := range content {
		// Keep printable characters (space and above), and essential whitespace.
		// DEL (127) is excluded.
		if (r >= 32 && r != 127) || r == '\t' || r == '\n' || r == '\r' {
			result.WriteRune(r)
		}
	}
	return result.String()
}

// hashContent generates SHA256 hash of content (same as Claude Squad)
func (s *Session) hashContent(content string) string {
	h := sha256.Sum256([]byte(content))
	return hex.EncodeToString(h[:])
}

// SendKeys sends keys to the tmux session
// Uses -l flag to treat keys as literal text, preventing tmux special key interpretation
func (s *Session) SendKeys(keys string) error {
	s.invalidateCache()
	// The -l flag makes tmux treat the string as literal text, not key names
	// This prevents issues like "Enter" being interpreted as the Enter key
	// and provides a layer of safety against tmux special sequences
	cmd := exec.Command("tmux", "send-keys", "-l", "-t", s.Name, keys)
	return cmd.Run()
}

// SendEnter sends an Enter key to the tmux session
func (s *Session) SendEnter() error {
	s.invalidateCache()
	cmd := exec.Command("tmux", "send-keys", "-t", s.Name, "Enter")
	return cmd.Run()
}

// SendKeysAndEnter sends literal text followed by Enter atomically in a single
// tmux subprocess. Uses tmux command chaining (;) to eliminate the race condition
// where two separate send-keys calls can be separated by OS scheduling delays.
// See: tmux#1185, tmux#1517, tmux#1778
func (s *Session) SendKeysAndEnter(keys string) error {
	s.invalidateCache()
	cmd := exec.Command("tmux",
		"send-keys", "-l", "-t", s.Name, "--", keys, ";",
		"send-keys", "-t", s.Name, "Enter")
	return cmd.Run()
}

// SendKeysChunked sends large content to the tmux session in chunks to avoid
// tmux/OS buffer limits. Content ≤4KB is sent directly via SendKeys.
// Larger content is split at newline boundaries with a short delay between chunks.
func (s *Session) SendKeysChunked(content string) error {
	const chunkSize = 4096
	const chunkDelay = 50 * time.Millisecond

	if len(content) <= chunkSize {
		return s.SendKeys(content)
	}

	chunks := splitIntoChunks(content, chunkSize)
	for i, chunk := range chunks {
		if err := s.SendKeys(chunk); err != nil {
			return fmt.Errorf("failed to send chunk %d/%d: %w", i+1, len(chunks), err)
		}
		if i < len(chunks)-1 {
			time.Sleep(chunkDelay)
		}
	}
	return nil
}

// splitIntoChunks splits content into chunks of at most maxSize bytes,
// preferring to split at newline boundaries. If a single line exceeds maxSize,
// it is split at the byte boundary as a fallback.
func splitIntoChunks(content string, maxSize int) []string {
	if content == "" {
		return nil
	}
	if len(content) <= maxSize {
		return []string{content}
	}

	var chunks []string
	remaining := content

	for len(remaining) > 0 {
		if len(remaining) <= maxSize {
			chunks = append(chunks, remaining)
			break
		}

		// Find the last newline within the chunk boundary
		cutPoint := strings.LastIndex(remaining[:maxSize], "\n")
		if cutPoint > 0 {
			// Include the newline in this chunk
			chunks = append(chunks, remaining[:cutPoint+1])
			remaining = remaining[cutPoint+1:]
		} else {
			// No newline found: hard split at maxSize
			chunks = append(chunks, remaining[:maxSize])
			remaining = remaining[maxSize:]
		}
	}

	return chunks
}

// SendCtrlC sends Ctrl+C (interrupt signal) to the tmux session
func (s *Session) SendCtrlC() error {
	s.invalidateCache()
	cmd := exec.Command("tmux", "send-keys", "-t", s.Name, "C-c")
	return cmd.Run()
}

// SendCtrlU sends Ctrl+U (clear line) to the tmux session
func (s *Session) SendCtrlU() error {
	s.invalidateCache()
	cmd := exec.Command("tmux", "send-keys", "-t", s.Name, "C-u")
	return cmd.Run()
}

// WaitForShellPrompt polls the terminal until a shell prompt is detected
// Returns true if shell prompt found, false if timeout
// Shell prompts: $, #, %, ❯, ➜, or bare > at end of line
func (s *Session) WaitForShellPrompt(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	pollInterval := 100 * time.Millisecond

	shellPrompts := []string{"$ ", "# ", "% ", "❯ ", "➜ "}

	for time.Now().Before(deadline) {
		content, err := s.CapturePane()
		if err != nil {
			time.Sleep(pollInterval)
			continue
		}

		// Get the last non-empty line
		lines := strings.Split(strings.TrimSpace(content), "\n")
		if len(lines) == 0 {
			time.Sleep(pollInterval)
			continue
		}
		lastLine := strings.TrimSpace(lines[len(lines)-1])

		// Check for shell prompts
		for _, prompt := range shellPrompts {
			if strings.HasSuffix(lastLine, strings.TrimSpace(prompt)) ||
				strings.Contains(lastLine, prompt) {
				return true
			}
		}

		// Also check for bare ">" but make sure it's not Claude's input prompt
		// Claude's prompt is just ">" or "> " without path prefix
		// Shell prompts typically have a path or user prefix before >
		if strings.HasSuffix(lastLine, ">") && len(lastLine) > 2 {
			return true
		}

		time.Sleep(pollInterval)
	}

	return false
}

// WaitForReady polls the terminal until the agent is ready for input
// Ready state = NO busy indicator AND prompt visible
// This works for Claude ("> "), Gemini, and other agents
func (s *Session) WaitForReady(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	pollInterval := 100 * time.Millisecond
	attempts := 0

	for time.Now().Before(deadline) {
		attempts++
		content, err := s.CapturePane()
		if err != nil {
			statusLog.Debug("wait_for_ready_capture_error", slog.Int("attempt", attempts), slog.String("error", err.Error()))
			time.Sleep(pollInterval)
			continue
		}

		busy := s.hasBusyIndicator(content)
		prompt := hasPrompt(content)

		if attempts%10 == 0 { // Log every 10th attempt (every second)
			statusLog.Debug("wait_for_ready_status", slog.Int("attempt", attempts), slog.Bool("busy", busy), slog.Bool("prompt", prompt))
		}

		// Check: NOT busy AND has prompt
		if !busy && prompt {
			statusLog.Debug("wait_for_ready_detected", slog.Int("attempts", attempts), slog.Float64("seconds", float64(attempts)*0.1))
			return true // Ready for input!
		}

		time.Sleep(pollInterval)
	}

	statusLog.Debug("wait_for_ready_timeout", slog.Int("attempts", attempts))
	return false // Timeout
}

// hasPrompt checks for input prompts (Claude, shell, other agents)
func hasPrompt(content string) bool {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 {
		return false
	}

	// Check last 5 lines (Claude's "> " might be above permissions dialog)
	start := len(lines) - 5
	if start < 0 {
		start = 0
	}

	for _, line := range lines[start:] {
		trimmed := strings.TrimSpace(line)

		// Claude prompt: "> " or just ">"
		if strings.Contains(line, "> ") || trimmed == ">" {
			return true
		}

		// Shell prompts: $, #, %, ❯, ➜
		if strings.HasSuffix(trimmed, "$") ||
			strings.HasSuffix(trimmed, "#") ||
			strings.HasSuffix(trimmed, "%") ||
			strings.Contains(line, "❯") ||
			strings.Contains(line, "➜") {
			return true
		}
	}

	return false
}

// IsClaudeRunning checks if Claude appears to be running in the session
// Returns true if Claude indicators are found
func (s *Session) IsClaudeRunning() bool {
	content, err := s.CapturePane()
	if err != nil {
		return false
	}

	// Check for Claude-specific indicators
	claudeIndicators := []string{
		"ctrl+c to interrupt",
		"Thinking...",
		"Connecting...",
		"Press Ctrl-C again to exit",
	}

	// Also check for spinner characters (Claude's busy indicator)
	spinnerChars := "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"

	for _, indicator := range claudeIndicators {
		if strings.Contains(content, indicator) {
			return true
		}
	}

	// Check last few lines for spinner
	lines := strings.Split(content, "\n")
	for i := len(lines) - 1; i >= 0 && i >= len(lines)-5; i-- {
		line := lines[i]
		for _, c := range spinnerChars {
			if strings.ContainsRune(line, c) {
				return true
			}
		}
	}

	return false
}

// SendCommand sends a command to the tmux session and presses Enter
func (s *Session) SendCommand(command string) error {
	return s.SendKeysAndEnter(command)
}

// GetWorkDir returns the current working directory of the tmux pane
// This is the live directory from the pane, not the initial WorkDir
func (s *Session) GetWorkDir() string {
	if !s.Exists() {
		return ""
	}

	cmd := exec.Command("tmux", "display-message", "-t", s.Name, "-p", "#{pane_current_path}")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// ListAllSessions returns all Agent Deck tmux sessions
func ListAllSessions() ([]*Session, error) {
	cmd := exec.Command("tmux", "list-sessions", "-F", "#{session_name}")
	output, err := cmd.Output()
	if err != nil {
		// No sessions exist
		if strings.Contains(err.Error(), "no server running") ||
			strings.Contains(err.Error(), "no sessions") {
			return []*Session{}, nil
		}
		return nil, fmt.Errorf("failed to list sessions: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	var sessions []*Session

	for _, line := range lines {
		if strings.HasPrefix(line, SessionPrefix) {
			displayName := strings.TrimPrefix(line, SessionPrefix)
			// Get session info
			sess := &Session{
				Name:        line,
				DisplayName: displayName,
			}
			// Try to get working directory
			workDirCmd := exec.Command("tmux", "display-message", "-t", line, "-p", "#{pane_current_path}")
			if workDirOutput, err := workDirCmd.Output(); err == nil {
				sess.WorkDir = strings.TrimSpace(string(workDirOutput))
			}
			sessions = append(sessions, sess)
		}
	}

	return sessions, nil
}

// ═══════════════════════════════════════════════════════════════════════════
// Log Management Functions
// ═══════════════════════════════════════════════════════════════════════════

// TruncateLogFile truncates a log file to keep only the last maxLines lines
// This is called when a log file exceeds maxSizeBytes
func TruncateLogFile(logPath string, maxLines int) error {
	// Read the file
	data, err := os.ReadFile(logPath)
	if err != nil {
		return fmt.Errorf("failed to read log file: %w", err)
	}

	// Split into lines
	lines := strings.Split(string(data), "\n")

	// If already under limit, nothing to do
	if len(lines) <= maxLines {
		return nil
	}

	// Keep only the last maxLines
	start := len(lines) - maxLines
	truncatedLines := lines[start:]

	// Write back
	truncatedData := strings.Join(truncatedLines, "\n")
	if err := os.WriteFile(logPath, []byte(truncatedData), 0644); err != nil {
		return fmt.Errorf("failed to write truncated log: %w", err)
	}

	statusLog.Debug("log_truncated", slog.String("file", filepath.Base(logPath)), slog.Int("from_lines", len(lines)), slog.Int("to_lines", len(truncatedLines)))
	return nil
}

// TruncateLargeLogFiles checks all log files and truncates any that exceed maxSizeMB
func TruncateLargeLogFiles(maxSizeMB int, maxLines int) (truncated int, err error) {
	logDir := LogDir()

	entries, err := os.ReadDir(logDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil // No logs directory yet
		}
		return 0, fmt.Errorf("failed to read log directory: %w", err)
	}

	maxSizeBytes := int64(maxSizeMB * 1024 * 1024)

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".log") {
			continue
		}

		logPath := filepath.Join(logDir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			continue
		}

		if info.Size() > maxSizeBytes {
			if err := TruncateLogFile(logPath, maxLines); err != nil {
				statusLog.Debug("truncate_failed", slog.String("file", entry.Name()), slog.String("error", err.Error()))
				continue
			}
			truncated++
		}
	}

	return truncated, nil
}

// CleanupOrphanedLogs removes log files for sessions that no longer exist
// A log is considered orphaned if:
// 1. No tmux session with matching name exists
// 2. The log file is older than 1 hour (to avoid race conditions during session creation)
func CleanupOrphanedLogs() (removed int, freedBytes int64, err error) {
	logDir := LogDir()

	entries, err := os.ReadDir(logDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil // No logs directory yet
		}
		return 0, 0, fmt.Errorf("failed to read log directory: %w", err)
	}

	// Get list of existing tmux sessions
	sessions, err := ListAllSessions()
	if err != nil {
		// If tmux server isn't running, we can't determine orphans safely
		return 0, 0, nil
	}

	// Build a set of active session names
	activeNames := make(map[string]bool)
	for _, sess := range sessions {
		activeNames[sess.Name] = true
	}

	now := time.Now()
	minAge := 1 * time.Hour // Only cleanup logs older than 1 hour

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".log") {
			continue
		}

		sessionName := strings.TrimSuffix(entry.Name(), ".log")
		logPath := filepath.Join(logDir, entry.Name())

		// Check if session exists
		if activeNames[sessionName] {
			continue // Session still exists
		}

		// Check age
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) < minAge {
			continue // Too recent, might be in process of creation
		}

		// Remove orphaned log
		size := info.Size()
		if err := os.Remove(logPath); err != nil {
			statusLog.Debug("orphan_remove_failed", slog.String("file", entry.Name()), slog.String("error", err.Error()))
			continue
		}

		removed++
		freedBytes += size
		statusLog.Debug("orphan_removed", slog.String("file", entry.Name()), slog.Float64("size_kb", float64(size)/1024))
	}

	return removed, freedBytes, nil
}

// RunLogMaintenance performs all log maintenance tasks based on settings
// This should be called once at startup and optionally periodically
func RunLogMaintenance(maxSizeMB int, maxLines int, removeOrphans bool) {
	// Truncate large files
	truncated, err := TruncateLargeLogFiles(maxSizeMB, maxLines)
	if err != nil {
		statusLog.Debug("log_truncation_error", slog.String("error", err.Error()))
	} else if truncated > 0 {
		statusLog.Debug("log_truncation_complete", slog.Int("count", truncated))
	}

	// Remove orphaned logs
	if removeOrphans {
		removed, freed, err := CleanupOrphanedLogs()
		if err != nil {
			statusLog.Debug("orphan_cleanup_error", slog.String("error", err.Error()))
		} else if removed > 0 {
			statusLog.Debug("orphan_cleanup_complete", slog.Int("count", removed), slog.Float64("freed_mb", float64(freed)/(1024*1024)))
		}
	}
}

// ═══════════════════════════════════════════════════════════════════════════

// ═══════════════════════════════════════════════════════════════════════════
// Notification Bar Helper Functions
// ═══════════════════════════════════════════════════════════════════════════

// ListAgentDeckSessions returns the names of all agentdeck tmux sessions.
// This is used to update notification bars across ALL sessions, not just
// those in the current profile. This ensures consistent notification bars
// when users switch between sessions.
func ListAgentDeckSessions() ([]string, error) {
	cmd := exec.Command("tmux", "list-sessions", "-F", "#{session_name}")
	output, err := cmd.Output()
	if err != nil {
		// No sessions exist
		if strings.Contains(err.Error(), "no server running") ||
			strings.Contains(err.Error(), "no sessions") {
			return []string{}, nil
		}
		return nil, fmt.Errorf("failed to list sessions: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	var sessions []string

	for _, line := range lines {
		if strings.HasPrefix(line, SessionPrefix) {
			sessions = append(sessions, line)
		}
	}

	return sessions, nil
}

// SetStatusLeft sets the left side of tmux status bar for a session.
// Used by NotificationManager to display waiting session notifications.
func SetStatusLeft(sessionName, text string) error {
	// Escape single quotes for tmux by replacing ' with '\''
	escaped := strings.ReplaceAll(text, "'", "'\\''")
	cmd := exec.Command("tmux", "set-option", "-t", sessionName, "status-left", escaped)
	return cmd.Run()
}

// ClearStatusLeft resets status-left to default for a session.
// Called when notifications are cleared or acknowledged.
func ClearStatusLeft(sessionName string) error {
	// -u flag unsets the option, reverting to tmux default
	cmd := exec.Command("tmux", "set-option", "-t", sessionName, "-u", "status-left")
	return cmd.Run()
}

// SetStatusLeftGlobal sets the left side of tmux status bar globally.
// This is a MAJOR performance optimization: ONE tmux call instead of 100+.
// All agentdeck sessions inherit this global setting.
func SetStatusLeftGlobal(text string) error {
	escaped := strings.ReplaceAll(text, "'", "'\\''")
	cmd := exec.Command("tmux", "set-option", "-g", "status-left", escaped)
	return cmd.Run()
}

// ClearStatusLeftGlobal resets status-left to default globally.
func ClearStatusLeftGlobal() error {
	cmd := exec.Command("tmux", "set-option", "-gu", "status-left")
	return cmd.Run()
}

// InitializeStatusBarOptions sets optimal status bar options for agent-deck.
// Fixes truncation by setting adequate status-left-length globally.
// Should be called once during startup.
func InitializeStatusBarOptions() error {
	// Set adequate status-left-length globally (default is only 10 chars!)
	// This ensures the notification bar content is not truncated
	return exec.Command("tmux", "set-option", "-g", "status-left-length", "120").Run()
}

// RefreshStatusBarImmediate forces an immediate status bar redraw for ALL connected clients.
// This bypasses the status-interval timer (default 15s) for instant visual feedback.
// Uses -S flag which only refreshes the status line (lightweight operation ~1-2ms per client).
// Filters out control mode clients (from PipeManager) which don't have a visible status bar.
func RefreshStatusBarImmediate() error {
	// Get all connected clients, filtering out control mode clients
	cmd := exec.Command("tmux", "list-clients", "-F", "#{client_name}\t#{client_control_mode}")
	output, err := cmd.Output()
	if err != nil {
		return nil
	}

	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 || parts[0] == "" {
			continue
		}
		// Skip control mode clients (PipeManager pipes)
		if parts[1] == "1" {
			continue
		}
		_ = exec.Command("tmux", "refresh-client", "-S", "-t", parts[0]).Run()
	}
	return nil
}

// GetAttachedSessions returns the names of tmux sessions that have real clients attached.
// Used to detect which session the user is currently viewing.
// Filters out control mode clients (from PipeManager) which are not real user sessions.
func GetAttachedSessions() ([]string, error) {
	cmd := exec.Command("tmux", "list-clients", "-F", "#{session_name}\t#{client_control_mode}")
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var sessions []string
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 || parts[0] == "" {
			continue
		}
		// Skip control mode clients
		if parts[1] == "1" {
			continue
		}
		sessions = append(sessions, parts[0])
	}
	return sessions, nil
}

// BindSwitchKey binds a number key to switch to target session.
// Uses prefix table (default) so Ctrl+b N works.
// The key should be a single character like "1", "2", etc.
// Deprecated: Use BindSwitchKeyWithAck for notification bar integration.
func BindSwitchKey(key, targetSession string) error {
	cmd := exec.Command("tmux", "bind-key", key, "switch-client", "-t", targetSession)
	return cmd.Run()
}

// BindSwitchKeyWithAck binds a number key to switch to target session AND
// writes a signal file so agent-deck can acknowledge the session was selected.
// This enables proper acknowledgment when user presses Ctrl+b 1-6 shortcuts.
func BindSwitchKeyWithAck(key, targetSession, sessionID string) error {
	// Get signal file path
	signalFile, err := GetAckSignalPath()
	if err != nil {
		// Fall back to simple binding if we can't get the path
		return BindSwitchKey(key, targetSession)
	}

	// Create a compound command that:
	// 1. Writes the session ID to a signal file (for agent-deck to acknowledge)
	// 2. Switches to the target session
	script := fmt.Sprintf("echo '%s' > '%s' && tmux switch-client -t '%s'",
		sessionID, signalFile, targetSession)
	cmd := exec.Command("tmux", "bind-key", key, "run-shell", script)
	return cmd.Run()
}

// GetAckSignalPath returns the path to the acknowledgment signal file
func GetAckSignalPath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(homeDir, ".agent-deck", "ack-signal"), nil
}

// ReadAndClearAckSignal reads the session ID from the signal file and deletes it.
// Returns empty string if no signal file exists or on error.
func ReadAndClearAckSignal() string {
	signalFile, err := GetAckSignalPath()
	if err != nil {
		return ""
	}

	data, err := os.ReadFile(signalFile)
	if err != nil {
		return "" // File doesn't exist or can't be read
	}

	// Delete the file immediately after reading
	_ = os.Remove(signalFile)

	return strings.TrimSpace(string(data))
}

// UnbindKey removes a key binding and restores default behavior.
// After unbinding, attempts to restore the default behavior where number keys
// select windows. The restore is best-effort since it may fail in environments
// without windows (e.g., CI) and agent-deck rebinds keys every 2s anyway.
func UnbindKey(key string) error {
	// First unbind our custom binding
	_ = exec.Command("tmux", "unbind-key", key).Run()

	// Best-effort restore default: number keys select windows
	// bind-key 1 select-window -t :1
	_ = exec.Command("tmux", "bind-key", key, "select-window", "-t", ":"+key).Run()
	return nil
}

// GetActiveSession returns the session name the user is currently attached to.
// Returns empty string and error if not attached to any session.
func GetActiveSession() (string, error) {
	cmd := exec.Command("tmux", "display-message", "-p", "#{client_session}")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ═══════════════════════════════════════════════════════════════════════════

// DiscoverAllTmuxSessions returns all tmux sessions (including non-Agent Deck ones)
func DiscoverAllTmuxSessions() ([]*Session, error) {
	cmd := exec.Command("tmux", "list-sessions", "-F", "#{session_name}:#{pane_current_path}")
	output, err := cmd.Output()
	if err != nil {
		// No sessions exist
		if strings.Contains(err.Error(), "no server running") ||
			strings.Contains(err.Error(), "no sessions") {
			return []*Session{}, nil
		}
		return nil, fmt.Errorf("failed to list sessions: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	var sessions []*Session

	for _, line := range lines {
		if line == "" {
			continue
		}

		parts := strings.SplitN(line, ":", 2)
		sessionName := parts[0]
		workDir := ""
		if len(parts) == 2 {
			workDir = parts[1]
		}

		// Create session object
		sess := &Session{
			Name:        sessionName,
			DisplayName: sessionName,
			WorkDir:     workDir,
		}

		// If it's an agent-deck session, clean up the display name
		if strings.HasPrefix(sessionName, SessionPrefix) {
			sess.DisplayName = strings.TrimPrefix(sessionName, SessionPrefix)
		}

		sessions = append(sessions, sess)
	}

	return sessions, nil
}
