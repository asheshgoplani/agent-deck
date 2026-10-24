package tmux

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

// ErrCaptureTimeout is returned when CapturePane exceeds its timeout.
// Callers should preserve previous state rather than transitioning to error/inactive.
var ErrCaptureTimeout = errors.New("capture-pane timed out")

// ErrCaptureGone means a history capture failed because its target or server
// disappeared. Best-effort response readers can return an empty response;
// this sentinel does not change session status classification.
var ErrCaptureGone = errors.New("capture-pane target is gone")

// captureGoneMarkers are the lower-cased tmux stderr fragments that mean the
// capture target no longer exists. Detection is deliberately conservative:
// capture-pane exits non-zero for many reasons (bad flags, malformed target,
// permissions), so only an explicit "absent" message counts as gone; any
// unrecognized stderr surfaces as a real error. tmux wording varies across
// versions, so the list covers the session/pane/window/server-absence phrasings
// observed across tmux 2.x–3.x.
var captureGoneMarkers = []string{
	"can't find session",
	"can't find pane",
	"can't find window",
	"can't find client",
	"no such session",
	"no server running",
	"lost server",
	"server exited unexpectedly",
}

// captureGoneFromErr reports whether a capture-pane failure was caused by the
// target being gone, by matching tmux's stderr against captureGoneMarkers.
// exec.Cmd.Output() populates (*exec.ExitError).Stderr, so the message is
// available without a separate stderr pipe. Unrecognized stderr (or a
// non-ExitError such as a context kill) returns false so real failures and
// timeouts keep their existing handling.
func captureGoneFromErr(err error) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	stderr := strings.ToLower(strings.TrimSpace(string(exitErr.Stderr)))
	// Connection errors also cover permissions and malformed sockets. Only an
	// explicitly missing socket is benign; never match markers inside its path.
	if strings.HasPrefix(stderr, "error connecting to ") {
		return strings.HasSuffix(stderr, " (no such file or directory)")
	}
	if stderr == "" {
		return false
	}
	for _, marker := range captureGoneMarkers {
		if strings.Contains(stderr, marker) {
			return true
		}
	}
	return false
}

// CapturePane captures the visible pane content.
// Tries control mode pipe first (zero subprocess), falls back to subprocess.
// Uses singleflight to deduplicate concurrent calls.
func (s *Session) CapturePane() (string, error) {
	// Fast path: return cached content if fresh
	s.cacheMu.RLock()
	if s.captureCacheFreshLocked(time.Now()) {
		content := s.cacheContent
		s.cacheMu.RUnlock()
		logging.Aggregate(logging.CompPerf, "capture_pane_cache_hit", slog.String("session", s.TmuxName()))
		return content, nil
	}
	s.cacheMu.RUnlock()

	// Slow path: deduplicate concurrent calls via singleflight.
	v, err, _ := s.captureSf.Do("capture", func() (interface{}, error) {
		// Double-check cache inside singleflight
		s.cacheMu.RLock()
		if s.captureCacheFreshLocked(time.Now()) {
			content := s.cacheContent
			s.cacheMu.RUnlock()
			return content, nil
		}
		s.cacheMu.RUnlock()

		// Try control mode pipe first (zero subprocess)
		if pm := GetPipeManager(); pm != nil {
			pipeStart := time.Now()
			if content, pipeErr := pm.CapturePane(s.TmuxName()); pipeErr == nil {
				s.cacheMu.Lock()
				s.cacheContent = content
				s.cacheTime = time.Now()
				s.cacheMu.Unlock()
				logging.Aggregate(logging.CompPerf, "capture_pane_pipe",
					slog.String("session", s.TmuxName()),
					slog.Duration("elapsed", time.Since(pipeStart)))
				return content, nil
			}
			// Pipe failed: aggregate so today's 5,068/30min DEBUG storm
			// becomes one event_summary INFO per flush window with a
			// running count. See logging-review G14.
			s.recordPipeDegraded()
		}

		// Subprocess fallback: 3s timeout
		finish := logging.TraceOp(perfLog, "capture_pane_subprocess", 200*time.Millisecond,
			slog.String("session", s.TmuxName()))
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		cmd := s.tmuxCmdContext(ctx, "capture-pane", "-t", s.TmuxName(), "-p", "-e")
		output, err := commandOutput(cmd)
		finish()
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
	// Defensive: the singleflight closure above unconditionally returns
	// (string, nil), so this assertion cannot panic today. The comma-ok form
	// guards against future closure refactors that might return a different
	// type and silently introduce a nil-deref panic. (V1.9 §T6 / arch-review §5)
	if s, ok := v.(string); ok {
		return s, nil
	}
	return "", nil
}

// CapturePaneFresh captures pane content via a direct tmux subprocess call.
// Unlike CapturePane(), this bypasses the control-mode pipe and short-lived
// cache to provide a fresh snapshot. Use this for send verification where
// stale pane content can hide unsent composer input.
func (s *Session) CapturePaneFresh() (string, error) {
	s.invalidateCache()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := s.tmuxCmdContext(ctx, "capture-pane", "-t", s.TmuxName(), "-p", "-e")
	output, err := commandOutput(cmd)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", ErrCaptureTimeout
		}
		// A pane that no longer exists is positive evidence for the send
		// verifier (issue #1793: "pane gone" is a failure, not an unknown).
		// Wrapped, so every existing `err != nil` caller is unchanged.
		if captureGoneFromErr(err) {
			return "", fmt.Errorf("failed to capture pane: %w", ErrCaptureGone)
		}
		return "", fmt.Errorf("failed to capture pane: %w", err)
	}

	content := string(output)
	s.cacheMu.Lock()
	s.cacheContent = content
	s.cacheTime = time.Now()
	s.cacheMu.Unlock()

	return content, nil
}

// CapturePrimaryPaneFresh is CapturePaneFresh for the managed agent window,
// whichever window is active: a reader that answers the agent's prompts must
// not read an auxiliary window the operator opened.
func (s *Session) CapturePrimaryPaneFresh() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := s.tmuxCmdContext(ctx, "capture-pane", "-t", s.primaryWindowTarget(), "-p", "-e")
	output, err := commandOutput(cmd)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", ErrCaptureTimeout
		}
		if captureGoneFromErr(err) {
			return "", fmt.Errorf("failed to capture pane: %w", ErrCaptureGone)
		}
		return "", fmt.Errorf("failed to capture pane: %w", err)
	}
	return string(output), nil
}

// CaptureFullHistory captures the scrollback history (limited to last 2000 lines for performance)
func (s *Session) CaptureFullHistory() (string, error) {
	// Limit to last 2000 lines to balance content availability with memory usage
	// AI agent conversations can be long - 2000 lines captures ~40-80 screens of content
	// Bounded — see tmuxPollTimeout. The TUI calls this on a cadence, and it
	// was the single largest CPU sink in the 2026-07-21 incident: three
	// concurrent `capture-pane -S -2000` clients, each spinning >20 minutes.
	output, err := s.runBoundedOutput("capture-pane", "-t", s.TmuxName(), "-p", "-e", "-S", "-2000")
	if err != nil {
		if captureGoneFromErr(err) {
			return "", ErrCaptureGone
		}
		return "", fmt.Errorf("failed to capture history: %w", err)
	}
	return string(output), nil
}

// CaptureHistoryLines captures the last n lines of the pane's scrollback,
// preserving ANSI colors (-e). Unlike CaptureFullHistory (capped at 2000 for
// the preview pane) this is used by the in-attach scrollback pager (#1491),
// which needs a deep enough window to reach the start of a long session, so it
// runs with a generous timeout and an explicit -S -<n> lower bound.
//
// n is clamped to at least 1. A one-off subprocess with a 10s timeout keeps a
// pathological capture from wedging the UI; on timeout it returns
// ErrCaptureTimeout so the caller can surface a non-fatal message.
func (s *Session) CaptureHistoryLines(n int) (string, error) {
	if n < 1 {
		n = 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := s.tmuxCmdContext(ctx, "capture-pane", "-t", s.TmuxName(), "-p", "-e", "-S", fmt.Sprintf("-%d", n))
	output, err := commandOutput(cmd)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", ErrCaptureTimeout
		}
		if captureGoneFromErr(err) {
			return "", ErrCaptureGone
		}
		return "", fmt.Errorf("failed to capture history: %w", err)
	}
	return string(output), nil
}

// CapturePrimaryFullHistory captures the managed agent window even when an
// auxiliary tmux window is currently active.
func (s *Session) CapturePrimaryFullHistory() (string, error) {
	// Bounded for the same reason as CaptureFullHistory: this is polled on a
	// cadence, and an unreaped capture-pane client spins at 100% CPU forever.
	output, err := s.runBoundedOutput("capture-pane", "-t", s.primaryWindowTarget(), "-p", "-e", "-S", "-2000")
	if err != nil {
		return "", fmt.Errorf("failed to capture primary history: %w", err)
	}
	return string(output), nil
}

// CaptureWindowFullHistory captures the scrollback history of a specific window (last 2000 lines).
func (s *Session) CaptureWindowFullHistory(windowIndex int) (string, error) {
	target := fmt.Sprintf("%s:%d", s.TmuxName(), windowIndex)
	// Bounded — see tmuxPollTimeout.
	output, err := s.runBoundedOutput("capture-pane", "-t", target, "-p", "-e", "-S", "-2000")
	if err != nil {
		return "", fmt.Errorf("failed to capture window %d history: %w", windowIndex, err)
	}
	return string(output), nil
}
