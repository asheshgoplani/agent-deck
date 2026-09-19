package tmux

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// ErrPipeConnectBackoff is returned by PipeManager.Connect while a session
// whose last connect attempts failed is inside its backoff window.
var ErrPipeConnectBackoff = errors.New("control pipe connect backing off after repeated failures")

const (
	// connectBackoffMin is the pause after the first failed connect; it
	// doubles per failure up to connectBackoffMax.
	connectBackoffMin = 2 * time.Second
	connectBackoffMax = time.Minute
	// connectSummaryEvery is how often a session still being refused is
	// mentioned in the log.
	connectSummaryEvery = time.Minute
)

// connectBackoff is the pause after the nth consecutive failure.
func connectBackoff(failures int) time.Duration {
	d := connectBackoffMin
	for i := 1; i < failures && d < connectBackoffMax; i++ {
		d *= 2
	}
	return min(d, connectBackoffMax)
}

// connectBudget bounds how often PipeManager.Connect may try to open a
// control pipe to a session that keeps failing. Several callers (the live
// set reconciler, the tick fallback, the reviver) all ask for the same
// session, each attempt spawns tmux and three retries, and a session whose
// handshake always fails produced about a thousand log lines a minute
// (2026-09-19). The budget refuses attempts inside a per-session window that
// doubles from 2s to a minute, counts what it refused, and writes one
// summary line a minute instead of one per attempt.
type connectBudget struct {
	mu      sync.Mutex
	now     func() time.Time
	log     *slog.Logger
	entries map[string]*connectFailure
}

type connectFailure struct {
	failures  int
	until     time.Time
	lastErr   string
	refused   int       // attempts refused since the last summary
	summaryAt time.Time // when the last summary was written
}

func newConnectBudget(now func() time.Time, log *slog.Logger) *connectBudget {
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = pipeLog
	}
	return &connectBudget{now: now, log: log, entries: map[string]*connectFailure{}}
}

// allow reports whether an attempt for session may start now.
func (b *connectBudget) allow(session string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	e, ok := b.entries[session]
	if !ok {
		return nil
	}
	now := b.now()
	if !now.Before(e.until) {
		return nil
	}
	e.refused++
	if e.summaryAt.IsZero() || now.Sub(e.summaryAt) >= connectSummaryEvery {
		b.log.Warn("pipe_connect_suppressed",
			slog.String("session", session),
			slog.Int("failures", e.failures),
			slog.Int("refused", e.refused),
			slog.Duration("next_attempt_in", e.until.Sub(now).Round(time.Millisecond)),
			slog.String("last_error", e.lastErr))
		e.summaryAt = now
		e.refused = 0
	}
	return fmt.Errorf("%w: %s (%d failures, next attempt in %s)", ErrPipeConnectBackoff, session, e.failures, e.until.Sub(now).Round(time.Second))
}

// failed records a failed attempt and arms the next window.
func (b *connectBudget) failed(session string, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	e, ok := b.entries[session]
	if !ok {
		e = &connectFailure{}
		b.entries[session] = e
	}
	e.failures++
	e.until = b.now().Add(connectBackoff(e.failures))
	if err != nil {
		e.lastErr = err.Error()
	}
}

// succeeded clears the session's budget, noting the recovery when attempts
// had been refused.
func (b *connectBudget) succeeded(session string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	e, ok := b.entries[session]
	if !ok {
		return
	}
	delete(b.entries, session)
	b.log.Info("pipe_connect_recovered", slog.String("session", session), slog.Int("failures", e.failures))
}
