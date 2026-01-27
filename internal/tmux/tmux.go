package tmux

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Debug flag - set via environment variable AGENTDECK_DEBUG=1
var debugStatusEnabled = os.Getenv("AGENTDECK_DEBUG") == "1"

func debugLog(format string, args ...interface{}) {
	if debugStatusEnabled {
		log.Printf("[STATUS] "+format, args...)
	}
}

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
// NOTE: We use window_activity (not session_activity) because window_activity updates
// when there's actual terminal output, while session_activity only updates on
// session-level events. This is critical for detecting when Claude is actively working.
func RefreshSessionCache() {
	// Get both session name AND window activity timestamp in single call
	// Using list-windows with -a flag to get all windows across all sessions
	// window_activity updates on actual terminal output (Claude typing)
	// session_activity does NOT update on terminal output, only session-level events
	cmd := exec.Command("tmux", "list-windows", "-a", "-F", "#{session_name}\t#{window_activity}")
	output, err := cmd.Output()
	if err != nil {
		// tmux not running or error - clear cache
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

	// Non-blocking spike detection: track changes across tick cycles
	activityCheckStart  time.Time // When we started tracking for sustained activity
	activityChangeCount int       // How many timestamp changes seen in current window
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

	// mu protects all mutable fields below from concurrent access
	mu sync.Mutex

	// PERFORMANCE: Cache CapturePane content for short duration (500ms)
	// Reduces subprocess spawns during rapid status checks/log events
	cacheMu      sync.RWMutex
	cacheContent string
	cacheTime    time.Time

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

	// Custom patterns for generic tool support
	customToolName       string
	customBusyPatterns   []string
	customPromptPatterns []string
	customDetectPatterns []string
}

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
		}
	}
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

// LogFile returns the path to this session's pipe-pane log file
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
		lastStableStatus: "waiting",
		toolDetectExpiry: 30 * time.Second, // Re-detect tool every 30 seconds
		// stateTracker and promptDetector will be created lazily on first status check
	}
}

// ReconnectSession creates a Session object for an existing tmux session
// This is used when loading sessions from storage - it properly initializes
// all fields needed for status detection to work correctly
func ReconnectSession(tmuxName, displayName, workDir, command string) *Session {
	sess := &Session{
		Name:             tmuxName,
		DisplayName:      displayName,
		WorkDir:          workDir,
		Command:          command,
		Created:          time.Now(), // Approximate - we don't persist this
		lastStableStatus: "waiting",
		toolDetectExpiry: 30 * time.Second,
		// stateTracker and promptDetector will be created lazily on first status check
	}

	// Enable pipe-pane for event-driven status detection
	if sess.Exists() {
		if err := sess.EnablePipePane(); err != nil {
			debugLog("Warning: failed to enable pipe-pane for %s: %v", tmuxName, err)
		}
		// Configure status bar for existing sessions
		sess.ConfigureStatusBar()
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

	// Enable pipe-pane for event-driven status detection
	// (Note: Also called in ReconnectSession, but we ensure it's enabled after state restoration)
	if sess.Exists() {
		if err := sess.EnablePipePane(); err != nil {
			debugLog("Warning: failed to enable pipe-pane for %s: %v", tmuxName, err)
		}
	}

	return sess
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
	return cmd.Run()
}

// GetEnvironment gets an environment variable from this tmux session
// Returns the value or error if not found
func (s *Session) GetEnvironment(key string) (string, error) {
	cmd := exec.Command("tmux", "show-environment", "-t", s.Name, key)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("variable not found or session doesn't exist: %s", key)
	}
	// Output format: "KEY=value\n"
	line := strings.TrimSpace(string(output))
	prefix := key + "="
	if strings.HasPrefix(line, prefix) {
		return strings.TrimPrefix(line, prefix), nil
	}
	return "", fmt.Errorf("variable not found: %s", key)
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
		if err := s.SendKeys(cmdToSend); err != nil {
			return fmt.Errorf("failed to send command: %w", err)
		}
		if err := s.SendEnter(); err != nil {
			return fmt.Errorf("failed to send enter: %w", err)
		}
	}

	// Enable pipe-pane to log output for event-driven status detection
	if err := s.EnablePipePane(); err != nil {
		// Non-fatal: status detection will fall back to polling
		debugLog("Warning: failed to enable pipe-pane for %s: %v", s.Name, err)
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

	// Cache miss/stale - fall back to direct check (spawns subprocess)
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

	// Enable status bar
	_ = exec.Command("tmux", "set-option", "-t", s.Name, "status", "on").Run()

	// Style: dark background with accent colors (Tokyo Night inspired)
	_ = exec.Command("tmux", "set-option", "-t", s.Name, "status-style", "bg=#1a1b26,fg=#a9b1d6").Run()

	// Left side: reserved for notification bar (waiting sessions: ⚡ [1] name [2] name2...)
	// Set length to accommodate multiple waiting sessions
	_ = exec.Command("tmux", "set-option", "-t", s.Name, "status-left-length", "120").Run()

	// Right side: detach hint + session title with folder path
	// The hint uses subtle gray (#565f89) so it doesn't compete with session info
	rightStatus := fmt.Sprintf("#[fg=#565f89]ctrl+q detach#[default] │ 📁 %s | %s ", s.DisplayName, folderName)
	_ = exec.Command("tmux", "set-option", "-t", s.Name, "status-right", rightStatus).Run()
	_ = exec.Command("tmux", "set-option", "-t", s.Name, "status-right-length", "80").Run()
}

// EnablePipePane enables tmux pipe-pane to stream output to a log file
// This is used for event-driven status detection via fsnotify
func (s *Session) EnablePipePane() error {
	logFile := s.LogFile()

	// Ensure log directory exists
	logDir := filepath.Dir(logFile)
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return fmt.Errorf("failed to create log dir: %w", err)
	}

	// Enable pipe-pane: stream pane output to log file
	cmd := exec.Command("tmux", "pipe-pane", "-t", s.Name, "-o", fmt.Sprintf("cat >> '%s'", logFile))
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to enable pipe-pane: %w", err)
	}

	return nil
}

// DisablePipePane disables pipe-pane logging
func (s *Session) DisablePipePane() error {
	cmd := exec.Command("tmux", "pipe-pane", "-t", s.Name)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to disable pipe-pane for %s: %w", s.Name, err)
	}
	return nil
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
	// Enable mouse support
	mouseCmd := exec.Command("tmux", "set-option", "-t", s.Name, "mouse", "on")
	if err := mouseCmd.Run(); err != nil {
		return err
	}

	// Enable OSC 52 clipboard integration
	// This allows tmux to copy directly to system clipboard in supported terminals
	// (Warp, iTerm2, Alacritty, kitty, WezTerm, Windows Terminal, VS Code, etc.)
	clipboardCmd := exec.Command("tmux", "set-option", "-t", s.Name, "set-clipboard", "on")
	if err := clipboardCmd.Run(); err != nil {
		// Non-fatal: older tmux versions may not support this
		debugLog("%s: failed to enable clipboard: %v", s.DisplayName, err)
	}

	// Enable escape sequence passthrough for modern terminal features (tmux 3.2+)
	// This allows:
	// - OSC 8: Clickable hyperlinks/file paths in Warp, iTerm2, kitty, etc.
	// - OSC 52: Clipboard integration (apps inside tmux can set clipboard)
	// - Image protocols: Inline images in supported terminals
	// Uses -q flag to silently ignore on older tmux versions
	passthroughCmd := exec.Command("tmux", "set-option", "-t", s.Name, "-q", "allow-passthrough", "on")
	if err := passthroughCmd.Run(); err != nil {
		// Non-fatal: tmux < 3.2 doesn't support this option
		debugLog("%s: failed to enable passthrough (tmux < 3.2?): %v", s.DisplayName, err)
	}

	// Enable hyperlink support in terminal features (tmux 3.4+, server-wide option)
	// This tells tmux to track hyperlinks like it tracks colors/attributes
	// Required for OSC 8 hyperlinks to work - passthrough alone isn't enough
	hyperlinkCmd := exec.Command("tmux", "set", "-asq", "terminal-features", ",*:hyperlinks")
	if err := hyperlinkCmd.Run(); err != nil {
		// Non-fatal: tmux < 3.4 doesn't support hyperlinks in terminal-features
		debugLog("%s: failed to enable hyperlinks (tmux < 3.4?): %v", s.DisplayName, err)
	}

	// Set large history limit for AI agent sessions (default is 2000)
	// AI agents produce a lot of output, so we need more scrollback
	historyCmd := exec.Command("tmux", "set-option", "-t", s.Name, "history-limit", "10000")
	if err := historyCmd.Run(); err != nil {
		// Non-fatal: history limit is a nice-to-have
		debugLog("%s: failed to set history-limit: %v", s.DisplayName, err)
	}

	// Reduce escape-time for responsive Vim/editor usage (default 500ms is too slow)
	// 10ms is a good balance between responsiveness and SSH reliability
	escapeCmd := exec.Command("tmux", "set-option", "-t", s.Name, "escape-time", "10")
	if err := escapeCmd.Run(); err != nil {
		// Non-fatal: escape-time is a nice-to-have
		debugLog("%s: failed to set escape-time: %v", s.DisplayName, err)
	}

	return nil
}

// Kill terminates the tmux session
func (s *Session) Kill() error {
	// Disable pipe-pane first
	_ = s.DisablePipePane()

	// Remove log file
	logFile := s.LogFile()
	os.Remove(logFile) // Ignore errors

	// Kill the tmux session
	cmd := exec.Command("tmux", "kill-session", "-t", s.Name)
	return cmd.Run()
}

// RespawnPane kills the current process in the pane and starts a new command
// This is more reliable than sending Ctrl+C and waiting for shell prompt
// The -k flag kills the current process before respawning
func (s *Session) RespawnPane(command string) error {
	if !s.Exists() {
		return fmt.Errorf("session does not exist: %s", s.Name)
	}
	s.invalidateCache()

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

	log.Printf("[MCP-DEBUG] RespawnPane executing: tmux %v", args)
	cmd := exec.Command("tmux", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("[MCP-DEBUG] RespawnPane error: %v, output: %s", err, string(output))
		return fmt.Errorf("failed to respawn pane: %w (output: %s)", err, string(output))
	}
	log.Printf("[MCP-DEBUG] RespawnPane output: %s", string(output))

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

	// Cache miss/stale - fall back to direct check (spawns subprocess)
	cmd := exec.Command("tmux", "display-message", "-t", s.Name, "-p", "#{window_activity}")
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

// CapturePane captures the visible pane content
func (s *Session) CapturePane() (string, error) {
	s.cacheMu.RLock()
	// Return cached content if it's less than 500ms old
	if s.cacheContent != "" && time.Since(s.cacheTime) < 500*time.Millisecond {
		content := s.cacheContent
		s.cacheMu.RUnlock()
		return content, nil
	}
	s.cacheMu.RUnlock()

	// -J joins wrapped lines and trims trailing spaces so hashes don't change on resize
	cmd := exec.Command("tmux", "capture-pane", "-t", s.Name, "-p", "-J")
	output, err := cmd.Output()

	if err != nil {
		return "", fmt.Errorf("failed to capture pane: %w", err)
	}

	content := string(output)

	s.cacheMu.Lock()
	s.cacheContent = content
	s.cacheTime = time.Now()
	s.cacheMu.Unlock()

	return content, nil
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
	debugLog("%s: AckSnapshot → acknowledged, cleared cooldown", shortName)
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
		debugLog("%s: INACTIVE (session not found)", shortName)
		return "inactive", nil
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

		if err == nil {
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
			debugLog("%s: needsBusyCheck busy=%v lastLine=%q", shortName, isExplicitlyBusy, lastLine)

			// Update content hash for spike detection (used later to confirm real activity)
			cleanContent := s.normalizeContent(content)
			currentHash := s.hashContent(cleanContent)
			if currentHash != "" {
				s.stateTracker.lastHash = currentHash
			}

			// GREEN only if explicit busy indicator found
			// Content hash changes alone should NOT trigger GREEN here - they must
			// go through spike detection (2+ changes in 1s) to filter cursor blinks
			if isExplicitlyBusy {
				s.stateTracker.lastChangeTime = time.Now()
				s.stateTracker.acknowledged = false
				s.stateTracker.lastActivityTimestamp = currentTS
				s.lastStableStatus = "active"
				debugLog("%s: BUSY_INDICATOR → active", shortName)
				return "active", nil
			}
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
		}
		s.lastStableStatus = "waiting"
		debugLog("%s: INIT → waiting", shortName)
		return "waiting", nil
	}

	// Restored session (lastActivityTimestamp == 0)
	if s.stateTracker.lastActivityTimestamp == 0 {
		s.stateTracker.lastActivityTimestamp = currentTS
		if s.stateTracker.acknowledged {
			s.lastStableStatus = "idle"
			debugLog("%s: IDLE (restored session, acknowledged)", shortName)
			return "idle", nil
		}
		if s.lastStableStatus != "waiting" {
			s.stateTracker.waitingSince = time.Now()
		}
		s.lastStableStatus = "waiting"
		debugLog("%s: WAITING (restored session, not acknowledged)", shortName)
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
			debugLog("%s: ACTIVITY_START ts=%d→%d count=1", shortName, oldTS, currentTS)
		} else {
			// Within detection window - count this change
			s.stateTracker.activityChangeCount++
			debugLog("%s: ACTIVITY_COUNT ts=%d→%d count=%d", shortName, oldTS, currentTS, s.stateTracker.activityChangeCount)

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

					// Update content hash for tracking (but NOT used for GREEN decision)
					cleanContent := s.normalizeContent(content)
					currentHash := s.hashContent(cleanContent)
					if currentHash != "" {
						s.stateTracker.lastHash = currentHash
					}

					// Only GREEN if explicit busy indicator found
					// Content hash changes alone are NOT reliable - cursor blinks,
					// terminal redraws, and status bar updates can cause hash changes
					if isExplicitlyBusy {
						s.stateTracker.lastChangeTime = now
						s.stateTracker.acknowledged = false
						s.stateTracker.activityCheckStart = time.Time{} // Reset window
						s.stateTracker.activityChangeCount = 0
						s.lastStableStatus = "active"
						debugLog("%s: SUSTAINED CONFIRMED (busy indicator) → active", shortName)
						return "active", nil
					}

					// No busy indicator - spike was false positive (cursor blink, status bar, etc.)
					debugLog("%s: SUSTAINED REJECTED (no busy indicator)", shortName)
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
				debugLog("%s: SPIKE_EXPIRED count=1 (filtered)", shortName)
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
		debugLog("%s: SPIKE_WINDOW_PENDING → keeping %s (not flashing green)", shortName, s.lastStableStatus)
		if s.lastStableStatus != "" {
			return s.lastStableStatus, nil
		}
		// Fallback if no previous status
		debugLog("%s: WAITING (spike window fallback)", shortName)
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
			debugLog("%s: STILL_BUSY (verified) → keeping active", shortName)
			return "active", nil
		}
		debugLog("%s: NO_LONGER_BUSY → transitioning from active", shortName)
	}

	// No busy indicator found - check acknowledged state
	if s.stateTracker.acknowledged {
		s.lastStableStatus = "idle"
		debugLog("%s: IDLE (acknowledged, no busy indicator)", shortName)
		return "idle", nil
	}
	// Track when we transition to waiting (not already waiting)
	if s.lastStableStatus != "waiting" {
		s.stateTracker.waitingSince = time.Now()
	}
	s.lastStableStatus = "waiting"
	debugLog("%s: WAITING (not acknowledged, no busy indicator)", shortName)
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
		s.mu.Lock()
		s.lastStableStatus = "inactive"
		s.mu.Unlock()
		debugLog("%s: FALLBACK INACTIVE (capture failed: %v)", shortName, err)
		return "inactive", nil
	}

	if s.hasBusyIndicator(content) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.ensureStateTrackerLocked()
		s.stateTracker.lastChangeTime = time.Now()
		s.stateTracker.acknowledged = false
		s.lastStableStatus = "active"
		debugLog("%s: FALLBACK ACTIVE (busy indicator found)", shortName)
		return "active", nil
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
		s.lastStableStatus = "waiting"
		debugLog("%s: FALLBACK INIT → waiting", shortName)
		return "waiting", nil
	}

	if s.stateTracker.lastHash == "" {
		s.stateTracker.lastHash = currentHash
		if s.stateTracker.acknowledged {
			s.lastStableStatus = "idle"
			debugLog("%s: FALLBACK IDLE (restored, acknowledged)", shortName)
			return "idle", nil
		}
		if s.lastStableStatus != "waiting" {
			s.stateTracker.waitingSince = time.Now()
		}
		s.lastStableStatus = "waiting"
		debugLog("%s: FALLBACK WAITING (restored, not acknowledged)", shortName)
		return "waiting", nil
	}

	// Update hash for tracking, but do NOT trigger GREEN based on hash change alone
	// The busy indicator check above (hasBusyIndicator) already handles the GREEN case
	// Hash changes can occur from cursor blinks, terminal redraws, status bar updates, etc.
	if s.stateTracker.lastHash != currentHash {
		s.stateTracker.lastHash = currentHash
		debugLog("%s: FALLBACK hash updated (no status change)", shortName)
	}

	// No busy indicator found - check acknowledged state
	if s.stateTracker.acknowledged {
		s.lastStableStatus = "idle"
		debugLog("%s: FALLBACK IDLE (acknowledged, no busy indicator)", shortName)
		return "idle", nil
	}
	// Track when we transition to waiting (not already waiting)
	if s.lastStableStatus != "waiting" {
		s.stateTracker.waitingSince = time.Now()
	}
	s.lastStableStatus = "waiting"
	debugLog("%s: FALLBACK WAITING (not acknowledged, no busy indicator)", shortName)
	return "waiting", nil
}

// Acknowledge marks the session as "seen" by the user
// Call this when user attaches to the session
func (s *Session) Acknowledge() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.ensureStateTrackerLocked()
	s.stateTracker.acknowledged = true
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
	s.stateTracker.waitingSince = time.Now() // Track when session became waiting for ordering
	s.lastStableStatus = "waiting"
}

// SignalFileActivity signals that file output was detected (from LogWatcher)
// This directly triggers GREEN status by updating the cooldown timer
// Call this when pipe-pane log file is written to
func (s *Session) SignalFileActivity() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.ensureStateTrackerLocked()
	s.stateTracker.lastChangeTime = time.Now()
	s.stateTracker.acknowledged = false
	s.lastStableStatus = "active"
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

// hasBusyIndicator checks if the terminal shows explicit busy indicators
// This is a quick check used in GetStatus() to detect active processing
//
// Busy indicators for Claude Code:
// - PRIMARY: "ctrl+c to interrupt" - Current Claude Code (2024+), always present when working
// - FALLBACK: "esc to interrupt" - Older Claude Code versions
// - BACKUP: Spinner characters (braille dots) in last 5 lines
func (s *Session) hasBusyIndicator(content string) bool {
	shortName := s.DisplayName
	if len(shortName) > 12 {
		shortName = shortName[:12]
	}

	// Get last 25 lines for analysis, skipping trailing blank lines
	// tmux capture-pane returns the full terminal buffer including blank lines at the end
	// Need 25 lines (not 20) because Claude's todo list and status bar can push
	// the interrupt message further up from the bottom
	lines := strings.Split(content, "\n")

	// Strip trailing blank lines
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}

	start := len(lines) - 25
	if start < 0 {
		start = 0
	}
	lastLines := lines[start:]
	recentContent := strings.ToLower(strings.Join(lastLines, "\n"))

	// ═══════════════════════════════════════════════════════════════════════
	// CHECK 1: "ctrl+c to interrupt" - PRIMARY indicator for Claude Code (2024+)
	// This text ALWAYS appears when Claude is actively working
	// ═══════════════════════════════════════════════════════════════════════
	if strings.Contains(recentContent, "ctrl+c to interrupt") {
		debugLog("%s: BUSY_REASON=ctrl+c to interrupt", shortName)
		return true
	}

	// ═══════════════════════════════════════════════════════════════════════
	// CHECK 2: "esc to interrupt" - FALLBACK for older Claude Code versions
	// Older versions showed "esc to interrupt" instead of "ctrl+c to interrupt"
	// ═══════════════════════════════════════════════════════════════════════
	if strings.Contains(recentContent, "esc to interrupt") {
		debugLog("%s: BUSY_REASON=esc to interrupt (fallback)", shortName)
		return true
	}

	// ═══════════════════════════════════════════════════════════════════════
	// CHECK 2b: "esc to cancel" - Gemini CLI busy indicator
	// Gemini shows "esc to cancel" while actively processing
	// ═══════════════════════════════════════════════════════════════════════
	if strings.Contains(recentContent, "esc to cancel") {
		debugLog("%s: BUSY_REASON=esc to cancel (Gemini)", shortName)
		return true
	}

	// ═══════════════════════════════════════════════════════════════════════
	// CHECK 3: OpenCode "esc interrupt" - PRIMARY indicator for OpenCode
	// OpenCode shows "esc interrupt" at the bottom status bar when actively working
	// This is equivalent to Claude's "ctrl+c to interrupt"
	// ═══════════════════════════════════════════════════════════════════════
	if strings.Contains(recentContent, "esc interrupt") {
		debugLog("%s: BUSY_REASON=esc interrupt (OpenCode)", shortName)
		return true
	}

	// ═══════════════════════════════════════════════════════════════════════
	// CHECK 4: Custom busy patterns from config.toml
	// Allows custom tools to define their own busy indicators
	// ═══════════════════════════════════════════════════════════════════════
	if len(s.customBusyPatterns) > 0 {
		for _, pattern := range s.customBusyPatterns {
			if strings.Contains(recentContent, strings.ToLower(pattern)) {
				debugLog("%s: BUSY_REASON=custom pattern=%q", shortName, pattern)
				return true
			}
		}
	}

	// ═══════════════════════════════════════════════════════════════════════
	// CHECK 5: Spinner characters - BACKUP indicator
	// Braille spinner dots from cli-spinners "dots" pattern
	// Check last 5 lines (spinners appear at status line)
	// ═══════════════════════════════════════════════════════════════════════
	spinnerChars := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	last5 := lastLines
	if len(last5) > 5 {
		last5 = last5[len(last5)-5:]
	}

	for _, line := range last5 {
		// Skip lines starting with box-drawing characters (e.g., │├└─┌┐┘┤┬┴┼)
		// These are UI borders that can contain braille-like chars as rendering artifacts
		trimmedLine := strings.TrimSpace(line)
		if len(trimmedLine) > 0 {
			r := []rune(trimmedLine)[0]
			if r == '│' || r == '├' || r == '└' || r == '─' || r == '┌' || r == '┐' || r == '┘' || r == '┤' || r == '┬' || r == '┴' || r == '┼' || r == '╭' || r == '╰' || r == '╮' || r == '╯' {
				continue
			}
		}
		for _, spinner := range spinnerChars {
			if strings.Contains(line, spinner) {
				debugLog("%s: BUSY_REASON=spinner char=%q", shortName, spinner)
				return true
			}
		}
	}

	return false
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
	debugLog("%s: isSustainedActivity changes=%d sustained=%v", s.DisplayName, changes, isSustained)
	return isSustained
}

// Precompiled regex patterns for dynamic content stripping
// These are compiled once at package init for performance
var (
	// Matches Claude Code status line: "(45s · 1234 tokens · ctrl+c to interrupt)"
	dynamicStatusPattern = regexp.MustCompile(`\([^)]*\d+s\s*·[^)]*tokens[^)]*\)`)

	// Matches whimsical thinking words with timing info (e.g., "⠋ Flibbertigibbeting... (25s · 340 tokens)")
	// Requires spinner prefix to avoid matching normal English words like "processing" or "computing"
	// Updated to include all 90 Claude Code whimsical words
	thinkingPattern = regexp.MustCompile(`[⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏]\s*(?i)(` + whimsicalWordsPattern + `)[^(]*\([^)]*\)`)

	// Progress bar patterns for normalization (Fix 2.1)
	// These cause hash changes when progress updates
	progressBarPattern = regexp.MustCompile(`\[=*>?\s*\]\s*\d+%`)                  // [====>   ] 45%
	downloadPattern    = regexp.MustCompile(`\d+\.?\d*[KMGT]?B/\d+\.?\d*[KMGT]?B`) // 1.2MB/5.6MB
	percentagePattern  = regexp.MustCompile(`\b\d{1,3}%`)                          // 45% (word boundary to avoid false matches)

	// Time patterns like "12:34" or "12:34:56" that change every second
	// Gemini and other tools show timestamps that cause hash changes
	timePattern = regexp.MustCompile(`\b\d{1,2}:\d{2}(?::\d{2})?\b`)
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

	// Strip braille spinner characters (used by Claude Code and others)
	// These animate while processing and cause hash changes
	spinners := []rune{'⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'}
	for _, r := range spinners {
		result = strings.ReplaceAll(result, string(r), "")
	}

	// Strip dynamic time/token counters that change every second
	// This prevents flickering when Claude Code shows "(45s · 1234 tokens · ctrl+c to interrupt)"
	// which updates to "(46s · 1234 tokens · ctrl+c to interrupt)" one second later
	result = dynamicStatusPattern.ReplaceAllString(result, "(STATUS)")
	result = thinkingPattern.ReplaceAllString(result, "$1...")

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
	// Match 3 or more consecutive newlines and replace with 2
	re := regexp.MustCompile(`\n{3,}`)
	return re.ReplaceAllString(content, "\n\n")
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
			log.Printf("[WaitForReady] Attempt %d: CapturePane error: %v", attempts, err)
			time.Sleep(pollInterval)
			continue
		}

		busy := s.hasBusyIndicator(content)
		prompt := hasPrompt(content)

		if attempts%10 == 0 { // Log every 10th attempt (every second)
			log.Printf("[WaitForReady] Attempt %d: busy=%v, prompt=%v", attempts, busy, prompt)
		}

		// Check: NOT busy AND has prompt
		if !busy && prompt {
			log.Printf("[WaitForReady] READY detected after %d attempts (%.1fs)", attempts, float64(attempts)*0.1)
			return true // Ready for input!
		}

		time.Sleep(pollInterval)
	}

	log.Printf("[WaitForReady] TIMEOUT after %d attempts", attempts)
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
	// Send the command text
	if err := s.SendKeys(command); err != nil {
		return err
	}
	// Press Enter to execute
	return s.SendEnter()
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

	debugLog("Truncated log %s: %d -> %d lines", filepath.Base(logPath), len(lines), len(truncatedLines))
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
				debugLog("Failed to truncate %s: %v", entry.Name(), err)
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
			debugLog("Failed to remove orphaned log %s: %v", entry.Name(), err)
			continue
		}

		removed++
		freedBytes += size
		debugLog("Removed orphaned log: %s (%.1f KB)", entry.Name(), float64(size)/1024)
	}

	return removed, freedBytes, nil
}

// RunLogMaintenance performs all log maintenance tasks based on settings
// This should be called once at startup and optionally periodically
func RunLogMaintenance(maxSizeMB int, maxLines int, removeOrphans bool) {
	// Truncate large files
	truncated, err := TruncateLargeLogFiles(maxSizeMB, maxLines)
	if err != nil {
		debugLog("Log truncation error: %v", err)
	} else if truncated > 0 {
		debugLog("Truncated %d large log files", truncated)
	}

	// Remove orphaned logs
	if removeOrphans {
		removed, freed, err := CleanupOrphanedLogs()
		if err != nil {
			debugLog("Orphan cleanup error: %v", err)
		} else if removed > 0 {
			debugLog("Removed %d orphaned logs (freed %.1f MB)", removed, float64(freed)/(1024*1024))
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
func RefreshStatusBarImmediate() error {
	// Get all connected clients
	cmd := exec.Command("tmux", "list-clients", "-F", "#{client_name}")
	output, err := cmd.Output()
	if err != nil {
		// No clients connected or tmux not running - not an error
		return nil
	}

	// Refresh each client's status bar
	clients := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, client := range clients {
		if client != "" {
			_ = exec.Command("tmux", "refresh-client", "-S", "-t", client).Run()
		}
	}
	return nil
}

// GetAttachedSessions returns the names of tmux sessions that have clients attached.
// Used to detect which session the user is currently viewing.
func GetAttachedSessions() ([]string, error) {
	cmd := exec.Command("tmux", "list-clients", "-F", "#{session_name}")
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	var sessions []string
	for _, line := range lines {
		if line != "" {
			sessions = append(sessions, line)
		}
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
// After unbinding, restores the default behavior where number keys select windows.
func UnbindKey(key string) error {
	// First unbind our custom binding
	_ = exec.Command("tmux", "unbind-key", key).Run()

	// Restore default: number keys select windows
	// bind-key 1 select-window -t :1
	cmd := exec.Command("tmux", "bind-key", key, "select-window", "-t", ":"+key)
	return cmd.Run()
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
