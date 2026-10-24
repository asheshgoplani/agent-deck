package tmux

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"al.essio.dev/pkg/shellescape"
)

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

	realActivityConfirmed bool // true once a real busy spike has been observed (not just tracker init)

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
	// Scan the whole visible pane, not just the bottom N lines. CLI tools
	// anchor their spinner to a bottom status line, but pi renders its
	// "── ⠹ Working ──" activity banner at the TOP of its UI region with
	// output streaming below it — a bottom-only window misses the banner
	// whenever the pane is tall enough that the banner sits more than a few
	// lines above the footer.
	//
	// UI-border protection is preserved per line: a spinner inside a vertical
	// box frame or after real text (e.g. "│ ⠋ content") is decorative and
	// skipped, while a spinner at the line start — or immediately after a
	// short horizontal rule, as in pi's banner — is a genuine activity
	// indicator.
	for i := len(lines) - 1; i >= 0; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" {
			continue
		}
		for _, ch := range spinnerChars {
			idx := strings.Index(lines[i], ch)
			if idx < 0 {
				continue
			}
			prefix := strings.TrimSpace(lines[i][:idx])
			if prefix == "" || isHorizontalRuleBannerPrefix(prefix) {
				return ch, lines[i], true
			}
		}
	}
	return "", "", false
}

// isHorizontalRuleBannerPrefix reports whether s is a short run of horizontal
// box-drawing dashes — the frame pi puts around its activity label
// ("── ⠹ Working ──"). A line whose spinner is immediately preceded by such a
// rule is an active status banner (the spinner leads the label), not a border
// or a box whose content happens to contain a spinner glyph.
func isHorizontalRuleBannerPrefix(s string) bool {
	const horizontal = "─━═"
	n := 0
	for _, r := range s {
		if !strings.ContainsRune(horizontal, r) {
			return false
		}
		n++
		if n > 8 {
			return false
		}
	}
	return n > 0
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

// SetStartupAtForTest backdates the startup clock so tests outside this
// package (e.g. internal/session, exercising the agent-deck session layer
// rather than this package's own white-box startup-timeout tests) can
// deterministically exhaust startupStateWindow without a real 2-minute wait.
func (s *Session) SetStartupAtForTest(t time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.startupAt = t
}

// MarkInteractiveAt ends the startup phase on out-of-band evidence that the
// agent is running, such as a lifecycle hook event observed at eventAt. Hook
// fast paths skip GetStatus, so without this the startup clock is never
// cleared by pane detection (#2361). GetStatus also probes an overdue pane
// for a live agent before expiring it, but a hook is direct evidence and also
// covers frames the probe cannot read (e.g. Claude's transcript view). Evidence older than the current pane generation is ignored so a
// late hook from a respawned pane cannot vouch for its replacement. Hook
// timestamps have one-second resolution, hence the truncation.
//
// eventAt comes from the hook handler's wall clock, not agent-deck's; the two
// are never synchronized. Accepted consequence: a previous-generation hook
// stamped in the same second as a respawn (including one already accepted
// before the respawn and re-fed afterwards) passes the guard and fails open,
// disarming the watchdog for that generation. Closing this would need a
// per-generation token rather than a timestamp.
func (s *Session) MarkInteractiveAt(eventAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.startupAt.IsZero() || s.startupTimedOut {
		return
	}
	if eventAt.Before(s.startupAt.Truncate(time.Second)) {
		return
	}
	s.startupAt = time.Time{}
}

// expireStartupHandover replaces an alive-but-unowned pane with an inert,
// non-echoing recovery hold when the startup deadline expires. It is called
// without s.mu held; claiming the flag prevents concurrent pollers from
// respawning the pane more than once.
func (s *Session) expireStartupHandover() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.startupTimedOut {
		return true
	}
	if s.startupAt.IsZero() || time.Since(s.startupAt) < startupStateWindow {
		return false
	}
	s.startupTimedOut = true
	s.startupAt = time.Time{}
	s.lastStableStatus = "error"

	restartTarget := s.DisplayName
	if strings.TrimSpace(restartTarget) == "" {
		restartTarget = s.TmuxName()
	}
	message := fmt.Sprintf("Session startup timed out before the agent became interactive.\n\nRecovery: agent-deck session restart %s\n", shellescape.Quote(restartTarget))
	// A tmux pane command normally inherits the pane's controlling terminal on
	// stdin, which is what stty requires. Name /dev/tty explicitly so this does
	// not silently depend on stdin surviving a wrapper change. If the pane has
	// no controlling terminal, stty remains best-effort: the recovery message
	// and inert hold still appear, but typed input may be echoed.
	hold := fmt.Sprintf("printf %%s %s; stty -echo </dev/tty 2>/dev/null || true; exec sleep 2147483647", shellescape.Quote(message))
	wrapped, err := wrapRespawnCommand(hold)
	if err == nil {
		args := append([]string{"respawn-pane", "-k", "-t", s.TmuxName() + ":"}, wrapped...)
		ctx, cancel := context.WithTimeout(context.Background(), tmuxMutationTimeout)
		output, respawnErr := s.tmuxCmdContext(ctx, args...).CombinedOutput()
		respawnErr = annotateDeadline(ctx.Err(), respawnErr)
		cancel()
		if respawnErr != nil {
			statusLog.Warn("startup_timeout_hold_failed", slog.String("session", s.TmuxName()), slog.String("error", respawnErr.Error()), slog.String("output", string(output)))
		}
	} else {
		statusLog.Warn("startup_timeout_hold_wrap_failed", slog.String("session", s.TmuxName()), slog.String("error", err.Error()))
	}
	return true
}

// startupTimeoutIsCurrent reports whether the timeout claim still belongs to
// the pane generation GetStatus is about to describe. A successful respawn
// clears the claim while publishing its fresh startup clock under the same
// mutex, so this is the status return's generation-validation point.
func (s *Session) startupTimeoutIsCurrent() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.startupTimedOut
}

// startupShowsAgentAlive reports whether an overdue pane already shows the
// agent alive (#2361), using the same content predicates normal detection uses
// to end startup, over the same prepareFrame-trimmed capture: a frame that
// hasBusyIndicator or hasPromptIndicator accepts. A pane this returns true for
// is one detection would classify as active or waiting, so letting it through
// cannot loosen #1892 beyond what detection already accepts inside the window.
//
// The pane title is deliberately not consulted: it survives respawn-pane, so a
// previous generation's spinner title would vouch for a replacement that never
// started.
//
// Called WITHOUT s.mu held (CapturePane manages its own locking). It does not
// set lastStableStatus, substate, or startupAt. It is not strictly pure:
// hasBusyIndicator may allocate stateTracker and, on a busy match, stamp the
// spinner tracker's busy time. That is the same bookkeeping the next detection
// pass does over the same cached capture.
//
// Capture errors, including ErrCaptureTimeout, return false: the watchdog
// falls back to its existing (pre-#2361) behaviour in that case.
func (s *Session) startupShowsAgentAlive() bool {
	rawContent, err := s.CapturePane()
	if err != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	content := s.prepareFrame(StripANSI(rawContent))
	return s.hasBusyIndicator(content) || s.hasPromptIndicator(content)
}

// GetWindowActivity returns Unix timestamp of last tmux window activity
// Uses cached data when available (refreshed by RefreshSessionCache)
// Falls back to direct tmux call if cache is stale
func (s *Session) GetWindowActivity() (int64, error) {
	// Try cache first (O(1) map lookup, no subprocess)
	if activity, cacheValid := sessionActivityFromCache(s.TmuxName()); cacheValid {
		return activity, nil
	}

	// When PipeManager is active, route through pipe (zero subprocess)
	if pm := GetPipeManager(); pm != nil {
		return pm.GetWindowActivity(s.TmuxName())
	}

	// No PipeManager: fall back to direct check (spawns subprocess)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := s.tmuxCmdContext(ctx, "display-message", "-t", s.TmuxName(), "-p", "#{window_activity}")
	output, err := commandOutput(cmd)
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
	activity, valid := sessionActivityFromCache(s.TmuxName())
	if valid {
		return activity
	}
	return 0
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
	statusLog.Debug(
		"is_sustained_activity",
		slog.String("session", s.DisplayName),
		slog.Int("changes", changes),
		slog.Bool("sustained", isSustained),
	)
	return isSustained
}
