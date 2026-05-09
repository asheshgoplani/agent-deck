package tmux

import (
	"log/slog"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

// recordPipeDegraded notes one occurrence of the control-mode pipe failing
// and falling back to a subprocess capture-pane. Closes logging-review G14:
// today the per-event statusLog.Debug fires thousands of times an hour and
// is invisible at the default INFO level. Routing through the aggregator
// promotes the signal: one event_summary INFO per flush window with a
// running count, instead of one DEBUG per occurrence buried in 80k lines.
func (s *Session) recordPipeDegraded() {
	logging.Aggregate(logging.CompStatus, "pipe_degraded",
		slog.String("session", s.Name),
	)
}

// recordHashFallbackUsed emits hash_fallback_used at WARN exactly once per
// Session lifetime. Closes logging-review G8: the content-hash fallback is
// the same code path that historically caused the flickering bug
// (docs/BUG_FIXES.md:347), but entry to it is currently silent. A
// once-per-session landmark is a strong diagnostic anchor without the
// noise of a per-call WARN.
func (s *Session) recordHashFallbackUsed() {
	s.hashFallbackOnce.Do(func() {
		statusLog.Warn("hash_fallback_used",
			slog.String("session", s.Name),
		)
	})
}
