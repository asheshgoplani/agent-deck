package tmux

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

type envCacheEntry struct {
	value string
	// found distinguishes a cached value from a cached miss. Issue #1728: the
	// cache stored only successes, so every Claude-compatible session WITHOUT
	// the variable in its tmux env forked `tmux show-environment` on every
	// metadata sync — down to a 500ms cadence while the session ID is unbound,
	// which is exactly the always-miss case. With ~60 sessions that was the
	// larger half of a sustained 150-350% CPU subprocess storm.
	found bool
	time  time.Time
}

// ReconnectSession creates a Session object for an existing tmux session
// This is used when loading sessions from storage - it properly initializes
// all fields needed for status detection to work correctly
//
// Note: This runs immediate configuration (ConfigureStatusBar).
// For lazy loading during TUI startup, use ReconnectSessionLazy instead.
func ReconnectSession(tmuxName, displayName, workDir, command string) *Session {
	sess := &Session{
		Name:                  tmuxName,
		DisplayName:           displayName,
		WorkDir:               workDir,
		Command:               command,
		Created:               time.Now(), // Approximate - we don't persist this
		startupAt:             time.Time{},
		lastStableStatus:      "waiting",
		toolDetectExpiry:      30 * time.Second,
		injectStatusLine:      true,  // Default: inject status bar
		mouse:                 true,  // Default: mouse on (#730 opt-out)
		terminalChromeEnabled: false, // Default: opt-in (set true via [terminal].iterm_badge)
		configured:            false, // Will be set to true after configuration
		// stateTracker and promptDetector will be created lazily on first status check
	}

	// Configure existing sessions
	if sess.Exists() {
		sess.ConfigureStatusBar()
		sess.ConfigureTerminalTitle()
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
	case "error":
		// "error" is shared by startup timeouts and live tool failures (auth,
		// connection, unavailable model). Do not make that lossy persisted value
		// terminal. GetStatus reclassifies current pane content; the distinctive
		// timeout hold remains recoverable because a reconnected session has no
		// fresh startup generation.
		sess.lastStableStatus = "error"

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
		Name:                  tmuxName,
		DisplayName:           displayName,
		WorkDir:               workDir,
		Command:               command,
		Created:               time.Now(), // Approximate - we don't persist this
		startupAt:             time.Time{},
		lastStableStatus:      "waiting",
		toolDetectExpiry:      30 * time.Second,
		injectStatusLine:      true,  // Default: inject status bar
		mouse:                 true,  // Default: mouse on (#730 opt-out)
		terminalChromeEnabled: false, // Default: opt-in (set true via [terminal].iterm_badge)
		configured:            false, // Explicitly mark as not configured
	}

	// Restore state tracker based on previous status (without running tmux commands)
	switch previousStatus {
	case "error":
		// See ReconnectSessionWithStatus: generic persisted errors must remain
		// recoverable from the pane's current content.
		sess.lastStableStatus = "error"

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
	s.ConfigureTerminalTitle()
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

// KillSessionsWithEnvValue kills agentdeck tmux sessions that have the given
// environment variable set to the given value, excluding the session named
// `excludeName`. This prevents duplicate tmux sessions running the same Claude
// conversation (#596).
func KillSessionsWithEnvValue(envKey, envValue, excludeName string) {
	if envValue == "" {
		return
	}

	socket := DefaultSocketName()
	// Bounded — see tmuxPollTimeout.
	out, err := runBoundedOutput(socket, "list-sessions", "-F", "#{session_name}")
	if err != nil {
		return
	}

	for _, name := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if name == "" || name == excludeName {
			continue
		}
		if !strings.HasPrefix(name, SessionPrefix) {
			continue
		}
		val, err := runBoundedOutput(socket, "show-environment", "-t", name, envKey)
		if err != nil {
			continue
		}
		// Output format: "KEY=value\n"
		line := strings.TrimSpace(string(val))
		if idx := strings.IndexByte(line, '='); idx >= 0 {
			if line[idx+1:] == envValue {
				statusLog.Warn("killing_duplicate_session",
					slog.String("session", logging.SanitizeValue(name)),
					slog.String("env_key", logging.SanitizeValue(envKey)),
					slog.String("env_value", logging.SanitizeValue(envValue)),
					slog.String("kept", logging.SanitizeValue(excludeName)))
				// Bounded — see tmuxMutationTimeout. Best-effort already (error
				// discarded), so a timeout changes nothing but the wait.
				_ = runBoundedMutation(socket, "kill-session", "-t", name)
			}
		}
	}
}

// SetEnvironment sets an environment variable for this tmux session
func (s *Session) SetEnvironment(key, value string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := s.tmuxCmdContext(ctx, "set-environment", "-t", s.Name, key, value)
	// CombinedOutput (not Run) so tmux stderr is folded into the error. A bare
	// "exit status 1" is useless for diagnosing a wedged server (#1579): the
	// real cause ("no server running on ...", "can't find session") lives on
	// stderr, which Run() discards.
	out, err := commandCombinedOutput(cmd)
	if err == nil {
		// Invalidate cache entry so next GetEnvironment sees the new value
		s.envCacheMu.Lock()
		if s.envCache != nil {
			delete(s.envCache, key)
		}
		s.envCacheMu.Unlock()
		return nil
	}
	if trimmed := strings.TrimSpace(string(out)); trimmed != "" {
		return fmt.Errorf("%w: %s", err, trimmed)
	}
	return err
}

// UnsetEnvironment removes an environment variable from this tmux session's
// environment table (`set-environment -u`), so it is no longer inherited by
// panes/processes tmux spawns afterward. Used to clear a stale value set by
// a previous SetEnvironment call once the condition that set it no longer
// applies (#1822 F7) — e.g. CLAUDE_CONFIG_DIR after a session's tool changes
// away from Claude or its account override is removed. Without this, a
// stale value would survive in the tmux session env and be inherited by any
// later respawn, silently pointing a differently-configured pane at the
// wrong account.
func (s *Session) UnsetEnvironment(key string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := s.tmuxCmdContext(ctx, "set-environment", "-u", "-t", s.Name, key)
	out, err := commandCombinedOutput(cmd)
	if err == nil {
		s.envCacheMu.Lock()
		if s.envCache != nil {
			delete(s.envCache, key)
		}
		s.envCacheMu.Unlock()
		return nil
	}
	if trimmed := strings.TrimSpace(string(out)); trimmed != "" {
		return fmt.Errorf("%w: %s", err, trimmed)
	}
	return err
}

// ReadEnvironment reads a fresh session environment without the per-session
// cache. Reading the whole environment distinguishes a missing key (empty,
// nil) from a failed probe; ownership discovery must not guess after failure.
func (s *Session) ReadEnvironment(key string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := commandOutput(s.tmuxCmdContext(ctx, "show-environment", "-t", s.TmuxName()))
	if err != nil {
		return "", err
	}
	prefix := key + "="
	for _, line := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix), nil
		}
	}
	return "", nil
}

// GetEnvironment gets an environment variable from this tmux session.
// Uses a cache (30s for hits, 5s for misses — issue #1728) to avoid spawning
// tmux show-environment subprocesses on every poll cycle. Call
// InvalidateEnvCache() after SetEnvironment to clear.
func (s *Session) GetEnvironment(key string) (string, error) {
	// Check cache first. Misses are cached too: an absent variable is the
	// steady state for every session whose ID never binds via tmux env, and
	// un-cached misses re-fork show-environment on every status pass (#1728).
	s.envCacheMu.RLock()
	if s.envCache != nil {
		if entry, ok := s.envCache[key]; ok {
			ttl := envCacheTTL
			if !entry.found {
				ttl = envNegativeCacheTTL
			}
			if time.Since(entry.time) < ttl {
				s.envCacheMu.RUnlock()
				if !entry.found {
					return "", fmt.Errorf("variable not found: %s", key)
				}
				return entry.value, nil
			}
		}
	}
	s.envCacheMu.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := s.tmuxCmdContext(ctx, "show-environment", "-t", s.TmuxName(), key)
	output, err := commandOutput(cmd)
	if err != nil {
		s.storeEnvCacheEntry(key, envCacheEntry{time: time.Now()})
		return "", fmt.Errorf("variable not found or session doesn't exist: %s", key)
	}
	// Output format: "KEY=value\n"
	line := strings.TrimSpace(string(output))
	prefix := key + "="
	if strings.HasPrefix(line, prefix) {
		value := strings.TrimPrefix(line, prefix)
		s.storeEnvCacheEntry(key, envCacheEntry{value: value, found: true, time: time.Now()})
		return value, nil
	}
	s.storeEnvCacheEntry(key, envCacheEntry{time: time.Now()})
	return "", fmt.Errorf("variable not found: %s", key)
}

func (s *Session) storeEnvCacheEntry(key string, entry envCacheEntry) {
	s.envCacheMu.Lock()
	if s.envCache == nil {
		s.envCache = make(map[string]envCacheEntry)
	}
	s.envCache[key] = entry
	s.envCacheMu.Unlock()
}

// InvalidateEnvCache clears the environment variable cache for this session.
// Should be called after SetEnvironment to ensure fresh reads.
func (s *Session) InvalidateEnvCache() {
	s.envCacheMu.Lock()
	s.envCache = nil
	s.envCacheMu.Unlock()
}
