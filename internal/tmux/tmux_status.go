package tmux

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

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
	finish := logging.TraceOp(perfLog, "get_status", 100*time.Millisecond,
		slog.String("session", s.TmuxName()))
	defer finish()

	shortName := s.DisplayName
	if len(shortName) > 12 {
		shortName = shortName[:12]
	}

	if !s.Exists() {
		s.mu.Lock()
		s.lastStableStatus = "inactive"
		// No live pane → no live substate; clear it so CachedSubstate (used by
		// the transition daemon + TUI) cannot emit/show a stale error substate
		// for a stopped session.
		s.lastSubstate = SubstateNone
		s.lastSubstateDetail = ""
		s.mu.Unlock()
		statusLog.Debug("session_inactive", slog.String("session", shortName))
		return "inactive", nil
	}

	// Pane dead (process exited with remain-on-exit on) = inactive.
	if s.IsPaneDead() {
		s.mu.Lock()
		s.lastStableStatus = "inactive"
		s.lastSubstate = SubstateNone
		s.lastSubstateDetail = ""
		s.mu.Unlock()
		statusLog.Debug("pane_dead", slog.String("session", shortName))
		return "inactive", nil
	}

	s.mu.Lock()
	observedStartupAt := s.startupAt
	startupOverdue := !observedStartupAt.IsZero() &&
		time.Since(observedStartupAt) >= startupStateWindow &&
		!s.startupTimedOut
	s.mu.Unlock()

	if startupOverdue && s.startupShowsAgentAlive() {
		// The pane already shows the agent alive by the same tests normal
		// detection uses to end startup: resolve the overdue clock instead of
		// expiring a pane that would otherwise walk straight into normal
		// detection below. This is what makes the watchdog safe without
		// relying on a caller (e.g. the hook fast path, #2361) to have polled
		// GetStatus recently enough to have cleared startupAt itself.
		if s.afterStartupAliveProbe != nil {
			s.afterStartupAliveProbe()
		}
		s.mu.Lock()
		timedOut := s.startupTimedOut
		if s.startupAt.Equal(observedStartupAt) && !timedOut {
			s.startupAt = time.Time{}
			statusLog.Debug("startup_overdue_but_alive", slog.String("session", shortName))
		}
		s.mu.Unlock()
		if timedOut {
			// A concurrent poll expired this generation between the probe and
			// the clear; report it the same way the expiry path does.
			return "error", nil
		}
	} else if s.expireStartupHandover() {
		if s.afterStartupTimeoutClaim != nil {
			s.afterStartupTimeoutClaim()
		}
		if s.startupTimeoutIsCurrent() {
			return "error", nil
		}
	}

	// FAST PATH: Title-based state detection for Claude Code sessions.
	// Claude Code sets pane titles via OSC sequences: Braille spinner while working,
	// ✳ markers when done. One character check replaces full CapturePane + content scan.
	if paneInfo, ok := GetCachedPaneInfo(s.TmuxName()); ok {
		titleState := AnalyzePaneTitle(paneInfo.Title, paneInfo.CurrentCommand)
		switch titleState {
		case TitleStateWorking:
			// Braille spinner in title = actively working. Short-circuit completely.
			s.mu.Lock()
			s.ensureStateTrackerLocked()
			s.stateTracker.lastChangeTime = time.Now()
			s.stateTracker.realActivityConfirmed = true
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
		rawContent, err := s.CapturePane()
		s.mu.Lock()

		// Strip ANSI escape sequences for pattern matching.
		// CapturePane now returns ANSI-rich content (via -e flag) for display,
		// but status detection needs plain text for reliable string matching.
		content := s.prepareFrame(StripANSI(rawContent))

		if errors.Is(err, ErrCaptureTimeout) {
			// Timeout: preserve previous state to avoid false RED flashing
			if s.lastStableStatus != "" {
				statusLog.Debug(
					"capture_timeout_preserve",
					slog.String("session", shortName),
					slog.String("status", s.lastStableStatus),
				)
				return s.lastStableStatus, nil
			}
			// No previous state, fall through to default logic
			statusLog.Debug("capture_timeout_no_previous", slog.String("session", shortName))
		} else if err == nil {
			s.ensureStateTrackerLocked()

			// Honest Status v2: compute the additive substate from the content we
			// already captured (pure string ops; no extra pane capture). This
			// keeps lastSubstate fresh for the reporting layers.
			s.classifyFrameLocked(content)

			// Record whether THIS sample is specifically a credential failure.
			// auth-401 as a substate also covers a dropped socket, which IS
			// restart-recoverable; only a genuine credential failure justifies
			// holding the session out of the automatic boot paths. Evaluated on
			// every readable sample so the verdict always describes the latest
			// state of the pane — a recovery clears it, and only the death
			// itself (which classifies nothing) leaves it standing as the
			// post-mortem evidence. See session/auth_hold.go.
			s.noteSampleAuthFailureLocked(
				s.lastSubstate == SubstateAuth401 && s.isAuthFailureIndicator(content),
				content,
			)

			// Honest Status v2: a model-unavailable no-op loop ("X is currently
			// unavailable" / "Crunched for 0s") is the Fable-down case that this
			// feature exists to surface. It must short-circuit to "error" BEFORE
			// the busy check: the "✶ Crunched for 0s" completion line carries a
			// decorative asterisk that hasBusyIndicator would otherwise misread
			// as an active spinner and report "running" — the exact false-alive
			// this feature fixes. classifySubstate already excluded a real
			// (non-zero) crunch, so only the genuine no-op reaches here.
			if s.lastSubstate == SubstateModelUnavailable {
				s.resetPromptNoBusyHoldLocked()
				s.lastStableStatus = "error"
				s.startupAt = time.Time{}
				statusLog.Debug("model_unavailable_noop", slog.String("session", shortName))
				return "error", nil
			}

			// A TERMINAL auth/connection-failure banner (#1400) routes to "error"
			// BEFORE the busy check: a real 401 stops the spinner, so a stale busy
			// glyph lingering in the same window must not mask the failure as
			// "running". hasErrorBannerIndicator already EXCLUDES the in-flight
			// retry case (rendered behind the "⎿" tool-result connector with a
			// live spinner), so a session that is genuinely retrying is NOT
			// matched here and still reaches the busy check below — preserving
			// #1400's "a retry in progress is still working" intent. The substate
			// (in s.lastSubstate) names WHICH failure for the TUI glyph.
			if s.hasErrorBannerIndicator(content) {
				s.resetPromptNoBusyHoldLocked()
				s.lastStableStatus = "error"
				s.startupAt = time.Time{}
				statusLog.Debug("error_banner_detected", slog.String("session", shortName), slog.String("substate", string(s.lastSubstate)))
				return "error", nil
			}

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
				s.stateTracker.realActivityConfirmed = true
				s.stateTracker.acknowledged = false
				s.resetPromptNoBusyHoldLocked()
				s.stateTracker.lastActivityTimestamp = currentTS
				s.lastStableStatus = "active"
				s.startupAt = time.Time{}
				statusLog.Debug("busy_indicator_active", slog.String("session", shortName))
				return "active", nil
			}

			// Foreground turn ended by handing off to a background agent
			// ("Waiting for N background agent to finish"): Claude resumes by
			// itself, no spinner is drawn, so the busy check above misses it.
			// Keep it green until the agent reports back. A shell counter at
			// the prompt is deliberately NOT in this rule (A5, background_work.go).
			if s.markBackgroundWorkActiveLocked(content, currentTS, shortName) {
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

			// (Auth/connection-failure banners and the model-unavailable no-op
			// already routed to "error" above, before the busy check, so by here
			// the session is neither wedged nor busy.)

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
			statusLog.Debug(
				"activity_start",
				slog.String("session", shortName),
				slog.Int64("old_ts", oldTS),
				slog.Int64("new_ts", currentTS),
				slog.Int("count", 1),
			)
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
					content = s.prepareFrame(StripANSI(content))

					// Check for explicit busy indicator (spinner, "ctrl+c to interrupt")
					isExplicitlyBusy := s.hasBusyIndicator(content)

					// Only GREEN if explicit busy indicator found
					// Content hash changes alone are NOT reliable - cursor blinks,
					// terminal redraws, and status bar updates can cause hash changes
					if isExplicitlyBusy {
						s.stateTracker.lastChangeTime = now
						s.stateTracker.realActivityConfirmed = true
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

					// Error banner takes precedence over prompt detection (#1400).
					if s.hasErrorBannerIndicator(content) {
						s.resetPromptNoBusyHoldLocked()
						s.stateTracker.activityCheckStart = time.Time{}
						s.stateTracker.activityChangeCount = 0
						s.lastStableStatus = "error"
						s.startupAt = time.Time{}
						statusLog.Debug("sustained_error_banner", slog.String("session", shortName))
						return "error", nil
					}

					// Background work in flight keeps the session green here too:
					// the workflow row's redraws drive the spike, so without this the
					// prompt check below would flip it to waiting and fire a
					// premature completion (mirrors the busy-check path above).
					if s.markBackgroundWorkActiveLocked(content, currentTS, shortName) {
						s.stateTracker.activityCheckStart = time.Time{}
						s.stateTracker.activityChangeCount = 0
						return "active", nil
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
		statusLog.Debug(
			"spike_window_pending",
			slog.String("session", shortName),
			slog.String("status", s.lastStableStatus),
		)
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
		if captureErr == nil {
			content = s.prepareFrame(StripANSI(content))
		}
		if captureErr == nil && s.hasBusyIndicator(content) {
			// Busy indicator is authoritative (includes spinner grace period).
			s.resetPromptNoBusyHoldLocked()
			s.startupAt = time.Time{}
			statusLog.Debug("still_busy", slog.String("session", shortName))
			return "active", nil
		}
		// Error banner takes precedence over prompt detection (#1400).
		if captureErr == nil && s.hasErrorBannerIndicator(content) {
			s.resetPromptNoBusyHoldLocked()
			s.lastStableStatus = "error"
			s.startupAt = time.Time{}
			statusLog.Debug("error_banner_recheck", slog.String("session", shortName))
			return "error", nil
		}
		// Background work still in flight keeps the session green on a poll
		// that saw no new pane activity too (issue #2473): without this a
		// workflow whose row stopped redrawing for one tick would drop to
		// waiting here. Checked after the error banner, like the main path.
		if captureErr == nil && s.markBackgroundWorkActiveLocked(content, currentTS, shortName) {
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
	// Sticky error (#1400): an error-banner verdict persists across polls that
	// skip the pane capture (no new activity). Without this, the error would
	// surface for one poll and settle back to "waiting" even though the banner
	// is still on screen. Cleared by new activity (re-captures and
	// re-evaluates: busy/prompt/banner) or by user acknowledgment above.
	if s.lastStableStatus == "error" {
		statusLog.Debug("error_banner_sticky", slog.String("session", shortName))
		return "error", nil
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
	// Once-per-session WARN landmark; closes logging-review G8.
	s.recordHashFallbackUsed()

	shortName := s.DisplayName
	if len(shortName) > 12 {
		shortName = shortName[:12]
	}

	rawContent, err := s.CapturePane()
	if err != nil {
		if errors.Is(err, ErrCaptureTimeout) {
			// Timeout: preserve previous state instead of going inactive
			s.mu.Lock()
			prev := s.lastStableStatus
			s.mu.Unlock()
			if prev != "" {
				statusLog.Debug(
					"fallback_timeout_preserve",
					slog.String("session", shortName),
					slog.String("status", prev),
				)
				return prev, nil
			}
		}
		s.mu.Lock()
		s.lastStableStatus = "inactive"
		s.mu.Unlock()
		statusLog.Debug("fallback_inactive", slog.String("session", shortName), slog.String("error", err.Error()))
		return "inactive", nil
	}

	// Strip ANSI for reliable pattern matching (CapturePane now returns ANSI-rich content)
	s.mu.Lock()
	content := s.prepareFrame(StripANSI(rawContent))
	s.mu.Unlock()

	// Keep precedence aligned with the main path:
	// 1) busy (authoritative), 2) prompt, 3) waiting/idle.
	if s.hasBusyIndicator(content) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.ensureStateTrackerLocked()
		s.stateTracker.lastChangeTime = time.Now()
		s.stateTracker.realActivityConfirmed = true
		s.stateTracker.acknowledged = false
		s.resetPromptNoBusyHoldLocked()
		s.lastStableStatus = "active"
		s.startupAt = time.Time{}
		statusLog.Debug("fallback_active", slog.String("session", shortName))
		return "active", nil
	}

	// Error banner takes precedence over prompt detection (#1400).
	if s.hasErrorBannerIndicator(content) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.ensureStateTrackerLocked()
		s.resetPromptNoBusyHoldLocked()
		s.lastStableStatus = "error"
		s.startupAt = time.Time{}
		statusLog.Debug("fallback_error_banner", slog.String("session", shortName))
		return "error", nil
	}

	// Background work in flight keeps the session green (issue #2473),
	// mirroring the main path's order: busy, error banner, background work,
	// prompt.
	s.mu.Lock()
	if s.markBackgroundWorkActiveLocked(content, 0, shortName) {
		s.mu.Unlock()
		return "active", nil
	}
	s.mu.Unlock()

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

// IsAcknowledged returns whether the session has been acknowledged by the user.
// Used by the hook fast path to distinguish waiting (orange) from idle (gray).
func (s *Session) IsAcknowledged() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stateTracker == nil {
		return false
	}
	return s.stateTracker.acknowledged
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

// LastObservedActivity returns the last time a real busy spike was
// observed for this tracker, plus a bool reporting whether such a spike
// has ever happened in this tracker's lifetime. When the bool is false
// the time is the zero value, so callers that miss the bool check still
// get a sentinel they can detect.
func (s *Session) LastObservedActivity() (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stateTracker == nil || !s.stateTracker.realActivityConfirmed {
		return time.Time{}, false
	}
	return s.stateTracker.lastChangeTime, true
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
	// An open menu at the tail of a Claude frame outranks any busy cue
	// (including the spinner grace period): the turn is blocked on a choice.
	if s.claudeMenuOutranksBusy(content) {
		return false
	}
	// Always use spinner movement detection regardless of resolvedPatterns
	return s.hasBusyIndicatorResolved(content)
}

// isClaudeTool reports whether this session is running Claude Code, used to gate
// Claude-shaped pane heuristics (e.g. the background-work footer). Reads cached
// tool fields without locking; GetStatus callers already hold s.mu.
func (s *Session) isClaudeTool() bool {
	return strings.EqualFold(inferToolFromSessionFields(s.detectedTool, s.customToolName, s.launchCommand()), "claude")
}

// bgWorkCacheTTL bounds how often BackgroundWorkSince captures the pane while a
// session sits at the prompt. CapturePane has its own 500ms cache; this adds a
// matching ceiling so foreground changes cannot be hidden by a longer cache.
const bgWorkCacheTTL = 500 * time.Millisecond

// BackgroundWorkPending reports whether a Claude session at the prompt still has
// background work in flight that the pane alone proves (a shell counter does
// not, A5). See BackgroundWorkSince.
func (s *Session) BackgroundWorkPending() bool {
	work, blocked, _ := s.BackgroundWorkSince(time.Time{})
	return work.PaneProven().Running() && !blocked
}

// BackgroundWorkSince returns the background work (issue #2473) a Claude
// session's pane shows in flight: a workflow row short of its last step, a turn
// awaiting background agents / workflows, or live shells / monitors in the
// footer (shells recorded raw: the session layer decides them against the
// transcript, A5). It captures the pane itself — for the UpdateStatus hook fast path,
// which short-circuits before GetStatus and so has no captured content — and
// caches the result briefly (bgWorkCacheTTL). A cached verdict older than
// notBefore is not reused: the caller passes the hook event time, so a Stop
// that lands after the last probe (the background work just reported back)
// is judged on a fresh frame, not on a verdict taken while the work still ran.
// blocked reports that the same frame shows something that outranks the work
// (an open menu or an error, see backgroundWorkOutrankedLocked): the caller
// must not promote the session to running for it. Returns the zero value for
// non-Claude sessions. foregroundBusy reports a live spinner above the composer in the same frame.
// Safe to call WITHOUT holding s.mu (acquires it
// internally; releases it for the slow capture).
func (s *Session) BackgroundWorkSince(notBefore time.Time) (work BackgroundWork, blocked bool, foregroundBusy bool) {
	s.mu.Lock()
	if !s.isClaudeTool() {
		s.mu.Unlock()
		return BackgroundWork{}, false, false
	}
	if !s.bgWorkCheckedAt.IsZero() && time.Since(s.bgWorkCheckedAt) < bgWorkCacheTTL &&
		!s.bgWorkCheckedAt.Before(notBefore) {
		work, blocked, foregroundBusy = s.bgWork, s.bgWorkBlocked, s.bgWorkForegroundBusy
		s.mu.Unlock()
		return work, blocked, foregroundBusy
	}
	s.mu.Unlock()

	rawContent, err := s.CapturePane()
	if err != nil {
		// Don't cache a capture failure as "no background work": refreshing the
		// TTL would suppress retries for the full window and could let the
		// waiting hook fire a premature completion. Keep the previous value and
		// leave bgWorkCheckedAt unchanged so the next call re-captures.
		// Old foreground evidence cannot promote a newer waiting hook.
		s.mu.Lock()
		work, blocked, foregroundBusy = s.bgWork, s.bgWorkBlocked, false
		s.mu.Unlock()
		return work, blocked, foregroundBusy
	}
	stripped := StripANSI(rawContent)
	work = ParseClaudeBackgroundWork(stripped)

	s.mu.Lock()
	content := trimClaudeTrailingRoster(stripped)
	blocked = s.backgroundWorkOutrankedLocked(content)
	foregroundBusy = hasClaudeLiveSpinner(content)
	s.bgWorkForegroundBusy = foregroundBusy
	s.bgWork, s.bgWorkBlocked = work, blocked
	s.bgWorkCheckedAt = time.Now()
	s.lastBackgroundWork, s.lastBackgroundBlocked = work, blocked
	s.mu.Unlock()
	return work, blocked, foregroundBusy
}

// CachedBackgroundWork returns the background work the last classified pane
// frame showed, WITHOUT capturing the pane (TUI render path).
func (s *Session) CachedBackgroundWork() BackgroundWork {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastBackgroundWork
}

// CachedBackgroundWorkBlocked reports whether the last classified or probed
// pane frame shows something that outranks background work (an open menu or
// an error, see backgroundWorkOutrankedLocked), WITHOUT capturing the pane.
func (s *Session) CachedBackgroundWorkBlocked() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastBackgroundBlocked
}

// backgroundWorkOutrankedLocked reports whether a (stripped, roster-trimmed)
// Claude frame shows something background work must never turn green: an
// open selection menu (the turn is blocked on the operator; the same rule as
// claudeMenuOutranksBusy, #2185), a terminal error banner (auth-401, dead
// connection, #1400) or the model-unavailable no-op. Such a frame keeps its
// waiting / error verdict while a workflow keeps running under it (issue
// #2473). Caller holds s.mu.
func (s *Session) backgroundWorkOutrankedLocked(content string) bool {
	return hasOpenInteractiveMenu(content) || s.hasErrorBannerIndicator(content) || hasModelUnavailableNoop(content)
}

// markBackgroundWorkActiveLocked applies the "keep green while background work is
// in flight" state update when a Claude session is at the prompt but work it
// started is still running: a workflow or background agents (issue #2473).
// A footer shell counter alone never fires it (A5): Claude Code never reaps a
// long-lived run_in_background shell, so only the session layer, with a fresh
// background Bash launch in the transcript, may keep such a frame running.
// Monitors are watching and never fire it either. Returns true when it fired (caller should
// return "active"). The verdict comes from the frame prepareFrame recorded
// (the untrimmed frame) or, failing that, from content itself (raw or
// stripped; StripANSI is idempotent). Never fires on a frame that shows an
// open menu or an error (backgroundWorkOutrankedLocked). Must be called with
// s.mu held.
func (s *Session) markBackgroundWorkActiveLocked(content string, currentTS int64, shortName string) bool {
	if !s.isClaudeTool() {
		return false
	}
	stripped := StripANSI(content)
	if !s.lastBackgroundWork.PaneProven().Running() && !claudeBackgroundWorkPending(stripped) {
		return false
	}
	// An open menu or an error outranks background work: the frame stays
	// waiting / error even while a workflow row is drawn under it.
	if s.backgroundWorkOutrankedLocked(stripped) {
		return false
	}
	s.ensureStateTrackerLocked()
	s.stateTracker.lastChangeTime = time.Now()
	s.stateTracker.realActivityConfirmed = true
	s.stateTracker.acknowledged = false
	s.resetPromptNoBusyHoldLocked()
	if currentTS != 0 {
		s.stateTracker.lastActivityTimestamp = currentTS
	}
	s.lastStableStatus = "active"
	s.startupAt = time.Time{}
	statusLog.Debug("background_work_active", slog.String("session", shortName),
		slog.String("work", s.lastBackgroundWork.Summary()))
	return true
}

var defaultResolvedPatternsCache sync.Map // map[string]*ResolvedPatterns

func inferToolFromSessionFields(detected, custom, command string) string {
	if detected != "" {
		return strings.ToLower(detected)
	}
	if custom != "" {
		return strings.ToLower(custom)
	}
	return detectToolFromCommand(command)
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

// isOpenCodeBlockArtLine recognizes rows made only of logo blocks and spaces.
// Solid or shaded pulses remain possible live spinners. Logo rows also
// contain partial blocks (such as ▀), which distinguish them from those pulses.
func isOpenCodeBlockArtLine(line string) bool {
	blocks := 0
	hasPartialBlock := false
	for _, r := range strings.TrimSpace(StripANSI(line)) {
		switch {
		case r == ' ' || r == '\t':
		case r >= '▀' && r <= '▟' && r != '░' && r != '▒' && r != '▓':
			blocks++
			hasPartialBlock = hasPartialBlock || r != '█'
		default:
			return false
		}
	}
	return blocks > 1 && hasPartialBlock
}

// blankOpenCodeBlockArtLines empties OpenCode logo rows so the spinner scan
// does not read the welcome logo as a pulse frame.
func blankOpenCodeBlockArtLines(content string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if isOpenCodeBlockArtLine(line) {
			lines[i] = ""
		}
	}
	return strings.Join(lines, "\n")
}

// hasOpenCodeQuestionUI reports whether OpenCode's question or permission help
// bar ("enter submit", "esc dismiss") is visible near the bottom of the pane.
func hasOpenCodeQuestionUI(content string) bool {
	recent := strings.ToLower(StripANSI(strings.Join(lastNLines(content, 25), "\n")))
	return strings.Contains(recent, "enter submit") || strings.Contains(recent, "esc dismiss")
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

	tool := inferToolFromSessionFields(s.detectedTool, s.customToolName, s.launchCommand())
	patterns := s.resolvedPatterns
	if patterns == nil {
		patterns = defaultResolvedPatternsForTool(tool)
	}

	// Get spinner chars from resolved/default patterns, fallback to defaults
	spinnerChars := defaultSpinnerChars()
	if patterns != nil && len(patterns.SpinnerChars) > 0 {
		spinnerChars = patterns.SpinnerChars
	}

	// Find spinner in terminal content. OpenCode's welcome logo uses the same
	// full blocks as its pulse spinner, so its block-art rows are left out of
	// the spinner scan only; busy patterns below still see the whole pane.
	spinnerContent := content
	if strings.EqualFold(tool, "opencode") {
		spinnerContent = blankOpenCodeBlockArtLines(content)
	}
	char, spinnerLine, found := findSpinnerInContent(spinnerContent, spinnerChars)

	// Get or create spinner tracker
	s.ensureStateTrackerLocked()
	tracker := s.stateTracker.spinnerTracker

	if strings.EqualFold(tool, "codex") && codexLiveStatusLine(content) {
		tracker.MarkBusy()
		statusLog.Debug("busy_codex_status_line", slog.String("session", shortName))
		return true
	}

	// BusyPatterns (regex + string) are authoritative because they capture
	// real active-line semantics for each tool.
	if patterns != nil {
		recentLines := lastNLines(content, 25)
		recentContent := strings.Join(recentLines, "\n")
		regexContent := recentContent
		if tool == "pi" {
			regexContent = piBusyPatternContent(content)
		}
		for _, re := range patterns.BusyRegexps {
			if re.MatchString(regexContent) {
				tracker.MarkBusy()
				statusLog.Debug(
					"busy_pattern_match",
					slog.String("session", shortName),
					slog.String("pattern", re.String()),
				)
				return true
			}
		}
		lowerContent := strings.ToLower(recentContent)
		// "esc to interrupt" always appears in the last 1-2 status bar lines of the
		// pane. Limiting to 3 lines prevents matching model-generated prose output
		// that happens to contain the phrase (e.g. conductor status reports).
		statusBarLines := lastNLines(content, 3)
		for _, str := range patterns.BusyStrings {
			lowerStr := strings.ToLower(str)
			if !strings.Contains(lowerContent, lowerStr) {
				continue
			}
			if strings.Contains(lowerStr, "interrupt") {
				var hasContext bool
				if strings.EqualFold(tool, "codex") {
					hasContext = hasCodexInterruptBusyProvenanceLines(statusBarLines, lowerStr)
				} else {
					hasContext = hasInterruptBusyContext(statusBarLines, lowerStr, spinnerChars)
				}
				if !hasContext {
					statusLog.Debug("busy_string_ignored_no_context",
						slog.String("session", shortName),
						slog.String("pattern", str))
					continue
				}
			}
			tracker.MarkBusy()
			statusLog.Debug("busy_string_match", slog.String("session", shortName), slog.String("pattern", str))
			return true
		}
	}
	isClaude := strings.EqualFold(tool, "claude")

	// OpenCode's question and permission UI can draw pulse glyphs in static
	// progress bars (#255). That UI waits for the user, so its help bar makes
	// the glyphs decorative. Busy patterns above remain authoritative. The
	// composer placeholder ("Ask anything") stays visible while OpenCode works,
	// so it must not hide a live pulse.
	if found && strings.EqualFold(tool, "opencode") && hasOpenCodeQuestionUI(content) {
		found = false
	}

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
	tool := inferToolFromSessionFields(s.detectedTool, s.customToolName, s.launchCommand())
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

// hasErrorBannerIndicator reports whether the pane shows an error banner the
// tool itself rendered (auth failure / dead connection — see
// PromptDetector.HasErrorBanner, issue #1400). Checked AFTER the busy
// indicator (busy is authoritative: an API-error retry in progress is still
// working) and BEFORE prompt detection (after a terminal failure the tool
// redraws its input prompt below the banner, so prompt detection alone would
// report "waiting" for a session that cannot make progress).
func (s *Session) hasErrorBannerIndicator(content string) bool {
	// Agent-deck's own startup hold is tool-neutral. Only recognize its text
	// while the current/restored generation owns the timeout; tmux preserves old
	// pane contents across RespawnPane, so text alone is stale-prone evidence.
	// A live generation owns timeout evidence only after it has actually timed
	// out. A reconnected session has no startup clock, so the hold text itself
	// is the durable, timeout-specific reason. RespawnPane publishes a fresh
	// startupAt before unlocking, preventing preserved old scrollback from
	// poisoning the new generation.
	if (s.startupTimedOut || s.startupAt.IsZero()) && strings.Contains(strings.ToLower(StripANSI(content)), "session startup timed out before the agent became interactive") {
		return true
	}
	tool := inferToolFromSessionFields(s.detectedTool, s.customToolName, s.launchCommand())
	if tool == "" {
		return false
	}
	// Reuse cached detector if tool hasn't changed (avoids allocation per call)
	if s.cachedPromptDetector == nil || s.cachedPromptDetectorTool != tool {
		s.cachedPromptDetector = NewPromptDetector(tool)
		s.cachedPromptDetectorTool = tool
	}
	return s.cachedPromptDetector.HasErrorBanner(content)
}

// isAuthFailureIndicator reports whether the pane content shows a CREDENTIAL
// failure banner specifically (not merely any error banner). Tool is inferred
// from the session's fields exactly as hasErrorBannerIndicator does, so the two
// verdicts always agree on which tool's renderings are being read.
func (s *Session) isAuthFailureIndicator(content string) bool {
	tool := inferToolFromSessionFields(s.detectedTool, s.customToolName, s.launchCommand())
	if tool == "" {
		return false
	}
	return IsAuthFailureContent(tool, content)
}

// classifySubstate computes the additive Honest-Status-v2 substate for the
// pane content (model-unavailable, auth-401, idle-at-empty-prompt, running).
// Tool is inferred from the session's fields; non-claude tools yield
// SubstateNone. Pure with respect to session state.
func (s *Session) classifySubstate(content string) Substate {
	tool := inferToolFromSessionFields(s.detectedTool, s.customToolName, s.launchCommand())
	if tool == "" {
		return SubstateNone
	}
	if s.cachedPromptDetector == nil || s.cachedPromptDetectorTool != tool {
		s.cachedPromptDetector = NewPromptDetector(tool)
		s.cachedPromptDetectorTool = tool
	}
	return s.cachedPromptDetector.ClassifySubstate(content)
}

// substateDetailLocked returns the detail for the substate classifySubstate
// just computed from content. Caller holds s.mu; classifySubstate leaves the
// detector nil for a session whose tool cannot be inferred.
func (s *Session) substateDetailLocked(content string) string {
	if s.cachedPromptDetector == nil {
		return ""
	}
	return s.cachedPromptDetector.SubstateDetail(content)
}

// GetSubstate captures the pane once and returns the additive Honest-Status-v2
// substate (see Substate). It is an independent read used by the status-reporting
// layers (CLI status --json, TUI label/glyph, transition events); it does NOT
// influence the canonical status returned by GetStatus, so existing status
// behavior stays byte-stable. Returns SubstateNone on a dead/absent pane, a
// non-claude tool; a capture failure retains the previous substate.
func (s *Session) GetSubstate() Substate {
	sub, _, _ := s.getSubstate()
	return sub
}

// GetSubstateWithLiveSpinner returns status and foreground evidence from the
// same successful capture. A failed read is unknown, not cached evidence that
// can promote a newly waiting session back to running.
func (s *Session) GetSubstateWithLiveSpinner() (Substate, bool) {
	sub, liveSpinner, err := s.getSubstate()
	if err != nil {
		return SubstateNone, false
	}
	return sub, liveSpinner
}

func (s *Session) getSubstate() (Substate, bool, error) {
	if !s.Exists() || s.IsPaneDead() {
		// A dead/absent pane has no live substate; clear the cached value so a
		// stale auth/model-unavailable glyph does not linger on a stopped
		// session in the TUI.
		s.mu.Lock()
		s.lastSubstate = SubstateNone
		s.lastSubstateDetail = ""
		s.mu.Unlock()
		return SubstateNone, false, nil
	}
	rawContent, err := s.CapturePane()
	if err != nil {
		s.mu.Lock()
		cached := s.lastSubstate
		s.mu.Unlock()
		return cached, false, err
	}
	// Hold s.mu across classifySubstate: it mutates the shared
	// cachedPromptDetector, which GetStatus also touches under the same lock.
	s.mu.Lock()
	content := s.prepareFrame(StripANSI(rawContent))
	sub := s.classifyFrameLocked(content)
	liveSpinner := s.isClaudeTool() && hasClaudeLiveSpinner(content)
	s.mu.Unlock()
	return sub, liveSpinner, nil
}

// classifyFrameLocked records everything a captured (ANSI-stripped) pane
// frame tells the reporting layers — the substate, its detail and the
// completed-turn verdict for the hook-lag rule — and returns the substate.
// Pure string ops; no capture. Shared by GetStatus and GetSubstate so the
// two reads can never record different things from the same frame. Caller
// holds s.mu.
func (s *Session) classifyFrameLocked(content string) Substate {
	s.lastSubstate = s.classifySubstate(content)
	s.lastSubstateDetail = s.substateDetailLocked(content)
	// Background work in flight (issue #2473) refines a frame that is
	// otherwise at the prompt. prepareFrame read it from the untrimmed frame,
	// which still carries the workflow row the roster trim removes. A live
	// foreground cue (running), an error, or an open menu keeps its verdict.
	// A shell counter alone refines nothing (A5, WithoutShells).
	if work := s.lastBackgroundWork.PaneProven(); s.isClaudeTool() && work.InFlight() && !s.lastBackgroundBlocked {
		switch s.lastSubstate {
		case SubstateNone, SubstateIdleAtEmptyPrompt, SubstateBackgroundWork, SubstateWatching:
			s.lastSubstate = work.Substate()
			s.lastSubstateDetail = work.Summary()
		}
	}
	s.recordCompletedTurnSampleLocked(content)
	return s.lastSubstate
}

// CachedSubstateDetail returns the detail recorded with the last substate
// classification (GetStatus / GetSubstate), without capturing the pane. ""
// when the substate carries none.
func (s *Session) CachedSubstateDetail() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastSubstateDetail
}

// CachedSubstate returns the last substate computed by GetStatus/GetSubstate
// WITHOUT capturing the pane. Use it on the TUI render hot path, where the
// background status loop already keeps the value fresh and a per-row capture
// would be too expensive.
func (s *Session) CachedSubstate() Substate {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastSubstate
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

// Precompiled regex patterns for dynamic content stripping
// These are compiled once at package init for performance
var (
	// Matches Claude Code status line: "(45s · 1234 tokens · ctrl+c to interrupt)" and "(35s · ↑ 673 tokens)"
	dynamicStatusPattern = regexp.MustCompile(`\([^)]*\d+s\s*·[^)]*(?:tokens|↑|↓)[^)]*\)`)

	// Claude Code 2.1.25+ active spinner: symbol + unicode ellipsis (U+2026)
	// Matches: "✳ Gusting…", "✻ Adding mcp-proxy subcommand…" (single or multi-word)
	// Does NOT match done state: "✻ Worked for 54s" (no ellipsis)
	// Anchored to line start to prevent mid-line · in welcome banner from false-positiving
	claudeSpinnerActivePattern = regexp.MustCompile(`(?m)^[·✳✽✶✻✢]\s*.+…`)

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
	percentagePattern  = regexp.MustCompile(
		`\b\d{1,3}%`,
	) // 45% (word boundary to avoid false matches)

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

	// Strip dynamic time/token counters that change every second. This must
	// run before the spinner strip: the counter's "·" separators are in the
	// spinner rune set, and once they are gone the pattern never matches, so
	// every tick changed the hash.
	result = dynamicStatusPattern.ReplaceAllString(result, "(STATUS)")

	// Strip spinner characters that animate and cause hash changes
	// Single-pass O(n) removal using map lookup instead of 16 sequential ReplaceAll calls
	result = StripSpinnerRunes(result)

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
	// tmux capture-pane can include trailing spaces
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
