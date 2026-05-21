// Issue #1143: central poll watcher that auto-stops sessions whose tmux pane
// content hasn't changed for `IdleTimeoutSecs` seconds.
//
// Design (per RFC in PR body):
//
//   - One watcher per agent-deck process (TUI or daemon). It walks every
//     instance on each Tick — no per-session goroutine.
//   - The "idle" signal is the FNV-1a hash of the tmux capture-pane content.
//     Anything cheaper (status transitions) doesn't catch the dormant-worker
//     case the self-improvement report flagged on 2026-05-21.
//   - On trigger: Stop callback fires + a single JSONL row is appended to
//     ~/.agent-deck/logs/session-lifecycle.jsonl with action
//     "idle-timeout-expired".
//   - Idempotent: once a session is stopped, its lastSeen entry is dropped so
//     a Restart() re-enters the watcher fresh.
package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

var idleLog = logging.ForComponent(logging.CompSession)

// SessionLifecycleEvent is a single row in session-lifecycle.jsonl.
type SessionLifecycleEvent struct {
	InstanceID string `json:"instance_id"`
	Action     string `json:"action"` // currently only "idle-timeout-expired"
	Reason     string `json:"reason,omitempty"`
	Timestamp  int64  `json:"ts"`
}

const ReasonIdleTimeoutExpired = "idle-timeout-expired"

var sessionLifecycleLogMu sync.Mutex

// GetSessionLifecycleLogPath returns ~/.agent-deck/logs/session-lifecycle.jsonl.
// Distinct from session-id-lifecycle.jsonl (which logs bind/rebind), this
// covers process-level lifecycle decisions like idle-timeout-expired.
func GetSessionLifecycleLogPath() string {
	agentDeckDir, err := GetAgentDeckDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".agent-deck", "logs", "session-lifecycle.jsonl")
	}
	return filepath.Join(agentDeckDir, "logs", "session-lifecycle.jsonl")
}

// WriteSessionLifecycleEvent appends a single JSONL row.
func WriteSessionLifecycleEvent(ev SessionLifecycleEvent) error {
	if ev.Timestamp == 0 {
		ev.Timestamp = time.Now().Unix()
	}

	logPath := GetSessionLifecycleLogPath()
	if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
		return fmt.Errorf("create session lifecycle log dir: %w", err)
	}

	line, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("marshal session lifecycle event: %w", err)
	}
	line = append(line, '\n')

	sessionLifecycleLogMu.Lock()
	defer sessionLifecycleLogMu.Unlock()

	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644) //nolint:gosec // log file
	if err != nil {
		return fmt.Errorf("open session lifecycle log: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("write session lifecycle event: %w", err)
	}
	return nil
}

// IdleTimeoutWatcherConfig wires the watcher to its environment. All fields
// are optional and default to production behavior; tests inject Now / Capture
// / Stop so they don't touch real tmux or real clocks.
type IdleTimeoutWatcherConfig struct {
	// Now is the clock source. Defaults to time.Now.
	Now func() time.Time
	// Capture returns the current tmux pane content for the instance.
	// Defaults to instance.tmuxSession.CapturePane().
	Capture func(*Instance) (string, error)
	// Stop is invoked once when an instance has been idle for >= its
	// IdleTimeoutSecs. Defaults to inst.Kill().
	Stop func(*Instance) error
	// LogEvent persists a "session lifecycle" row. Defaults to
	// WriteSessionLifecycleEvent.
	LogEvent func(SessionLifecycleEvent) error
}

// IdleTimeoutWatcher polls running sessions, tracks per-instance pane-content
// hashes, and triggers Stop when content stays unchanged longer than
// IdleTimeoutSecs.
//
// Safe for use from one goroutine. Tick is the unit of work; production code
// drives Tick from an existing 2-second ticker (e.g. statusWorker in the TUI)
// and ticks roughly once per minute via Start.
type IdleTimeoutWatcher struct {
	cfg IdleTimeoutWatcherConfig

	mu       sync.Mutex
	lastSeen map[string]idleSeenEntry
}

type idleSeenEntry struct {
	hash       uint64
	lastChange time.Time
}

// NewIdleTimeoutWatcher constructs a watcher with production defaults filled
// in for any nil config callback.
func NewIdleTimeoutWatcher(cfg IdleTimeoutWatcherConfig) *IdleTimeoutWatcher {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Capture == nil {
		cfg.Capture = defaultCapture
	}
	if cfg.Stop == nil {
		cfg.Stop = defaultStop
	}
	if cfg.LogEvent == nil {
		cfg.LogEvent = WriteSessionLifecycleEvent
	}
	return &IdleTimeoutWatcher{
		cfg:      cfg,
		lastSeen: map[string]idleSeenEntry{},
	}
}

// Tick scans the given instances. Sessions with IdleTimeoutSecs <= 0 are
// skipped (and their tracking state is cleared so toggling off via SetField
// works on the next tick). Non-running sessions are skipped too.
func (w *IdleTimeoutWatcher) Tick(instances []*Instance) {
	w.mu.Lock()
	defer w.mu.Unlock()

	now := w.cfg.Now()
	for _, inst := range instances {
		if inst == nil {
			continue
		}
		if inst.IdleTimeoutSecs <= 0 {
			delete(w.lastSeen, inst.ID)
			continue
		}
		if !idleTimeoutWatchable(inst) {
			delete(w.lastSeen, inst.ID)
			continue
		}

		content, err := w.cfg.Capture(inst)
		if err != nil {
			// transient capture failure (tmux race, snapshot truncate) —
			// don't reset state, just try again next tick.
			continue
		}
		h := hashPaneContent(content)
		prev, ok := w.lastSeen[inst.ID]
		if !ok || prev.hash != h {
			w.lastSeen[inst.ID] = idleSeenEntry{hash: h, lastChange: now}
			continue
		}
		elapsed := now.Sub(prev.lastChange)
		threshold := time.Duration(inst.IdleTimeoutSecs) * time.Second
		if elapsed < threshold {
			continue
		}
		// idle threshold exceeded — trigger stop.
		if stopErr := w.cfg.Stop(inst); stopErr != nil {
			idleLog.Warn("idle_timeout_stop_failed",
				slog.String("instance_id", inst.ID),
				slog.String("error", stopErr.Error()),
			)
			continue
		}
		if logErr := w.cfg.LogEvent(SessionLifecycleEvent{
			InstanceID: inst.ID,
			Action:     ReasonIdleTimeoutExpired,
			Reason: fmt.Sprintf(
				"no tmux pane output change for %ds (threshold=%ds)",
				int64(elapsed/time.Second), inst.IdleTimeoutSecs,
			),
		}); logErr != nil {
			idleLog.Warn("idle_timeout_log_failed",
				slog.String("instance_id", inst.ID),
				slog.String("error", logErr.Error()),
			)
		}
		delete(w.lastSeen, inst.ID)
	}
}

// Start runs Tick at the given interval until ctx is cancelled. instances is
// re-fetched each tick via the snapshot func; pass the parent TUI/daemon's
// instance accessor so the watcher always sees fresh state.
func (w *IdleTimeoutWatcher) Start(ctx context.Context, interval time.Duration, snapshot func() []*Instance) {
	if interval <= 0 {
		interval = 60 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if snapshot == nil {
				continue
			}
			w.Tick(snapshot())
		}
	}
}

func idleTimeoutWatchable(inst *Instance) bool {
	switch inst.Status {
	case StatusRunning, StatusWaiting, StatusIdle:
		return true
	default:
		return false
	}
}

func hashPaneContent(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}

func defaultCapture(inst *Instance) (string, error) {
	if inst == nil {
		return "", errors.New("nil instance")
	}
	tm := inst.GetTmuxSession()
	if tm == nil {
		return "", errors.New("no tmux session bound")
	}
	return tm.CapturePane()
}

func defaultStop(inst *Instance) error {
	if inst == nil {
		return errors.New("nil instance")
	}
	return inst.Kill()
}

// ParseIdleTimeoutFlag parses a --idle-timeout flag value: a Go-style
// duration like "30m", "1h", "24h". Empty string and "0" mean disabled
// (returns 0). Negative durations are rejected so a typo like "-1s" surfaces
// loudly instead of silently disabling. Returns the value in seconds (int64).
func ParseIdleTimeoutFlag(value string) (int64, error) {
	if value == "" || value == "0" {
		return 0, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("invalid --idle-timeout %q: %w (use Go duration like 30m, 1h, 24h)", value, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("invalid --idle-timeout %q: negative durations not allowed (use 0 to disable)", value)
	}
	return int64(d / time.Second), nil
}
