package watcher

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

// Notifier is implemented by concrete alert sinks (conductor Telegram/Slack/Discord
// bridge). The HealthBridge calls Notify once per triggered alert after debounce.
type Notifier interface {
	Notify(ctx context.Context, alert Alert) error
}

// Alert is a single outbound alert emitted by the HealthBridge.
type Alert struct {
	WatcherName string
	Trigger     string
	Message     string
	Timestamp   time.Time
}

// Valid Trigger values.
const (
	TriggerSilenceDetected           = "silence_detected"
	TriggerErrorThresholdExceeded    = "error_threshold_exceeded"
	TriggerAdapterTeardownUnexpected = "adapter_teardown_unexpected"
)

// Config controls HealthBridge behavior. Zero-value-safe.
type Config struct {
	Enabled        bool
	Channels       []string
	DebounceWindow time.Duration // defaults to 15*time.Minute when zero
}

// HealthBridge subscribes to an engine health signal and fans alerts to a Notifier
// with per-(watcher x trigger) debounce. Resilient to notifier errors and panics.
type HealthBridge struct {
	cfg      Config
	notifier Notifier
	source   <-chan HealthState
	log      *slog.Logger

	mu       sync.Mutex
	lastSent map[string]time.Time // key = watcherName + "|" + trigger

	clock func() time.Time // test-injectable; defaults to time.Now
}

// NewHealthBridge constructs a bridge. Does not start it. Call Run with a context
// to begin consuming from source.
func NewHealthBridge(cfg Config, notifier Notifier, source <-chan HealthState) *HealthBridge {
	if cfg.DebounceWindow <= 0 {
		cfg.DebounceWindow = 15 * time.Minute
	}
	return &HealthBridge{
		cfg:      cfg,
		notifier: notifier,
		source:   source,
		log:      logging.ForComponent(logging.CompWatcher),
		lastSent: make(map[string]time.Time),
		clock:    time.Now,
	}
}

// setClockForTest is a test-only hook for deterministic debounce assertions.
// Not part of the public surface.
func (b *HealthBridge) setClockForTest(fn func() time.Time) {
	b.clock = fn
}

// Run blocks until ctx is done or source is closed. Honors cfg.Enabled.
// Never returns a non-nil error (notifier failures are logged; ctx cancel returns nil).
func (b *HealthBridge) Run(ctx context.Context) error {
	if !b.cfg.Enabled {
		b.log.Info("health_bridge_disabled")
		<-ctx.Done()
		return nil
	}
	b.log.Info("health_bridge_started",
		slog.Int("channels", len(b.cfg.Channels)),
		slog.Duration("debounce_window", b.cfg.DebounceWindow),
	)
	for {
		select {
		case <-ctx.Done():
			b.mu.Lock()
			b.lastSent = make(map[string]time.Time)
			b.mu.Unlock()
			return nil
		case state, ok := <-b.source:
			if !ok {
				return nil
			}
			trigger := b.triggerFor(state)
			if trigger == "" {
				continue
			}
			b.maybeEmit(ctx, state.WatcherName, trigger, state.Message)
		}
	}
}

// triggerFor maps a HealthState to a trigger string, or "" if no alert applies.
func (b *HealthBridge) triggerFor(state HealthState) string {
	switch state.Status {
	case HealthStatusWarning:
		if strings.Contains(state.Message, "no events") {
			return TriggerSilenceDetected
		}
		if strings.Contains(state.Message, "consecutive errors") {
			return TriggerErrorThresholdExceeded
		}
	case HealthStatusError:
		if strings.Contains(state.Message, "consecutive errors") {
			return TriggerErrorThresholdExceeded
		}
	}
	return ""
}

// maybeEmit applies debounce and dispatches to the notifier with panic/error recovery.
func (b *HealthBridge) maybeEmit(ctx context.Context, watcherName, trigger, message string) {
	key := watcherName + "|" + trigger
	now := b.clock()

	b.mu.Lock()
	last, seen := b.lastSent[key]
	if seen && now.Sub(last) < b.cfg.DebounceWindow {
		b.mu.Unlock()
		return
	}
	b.lastSent[key] = now
	b.mu.Unlock()

	alert := Alert{
		WatcherName: watcherName,
		Trigger:     trigger,
		Message:     message,
		Timestamp:   now,
	}

	// Protect against notifier errors and panics. Never crash the bridge or engine.
	func() {
		defer func() {
			if r := recover(); r != nil {
				b.log.Warn("health_bridge_notifier_panic",
					slog.String("watcher", watcherName),
					slog.String("trigger", trigger),
					slog.Any("panic", r),
				)
			}
		}()
		if err := b.notifier.Notify(ctx, alert); err != nil {
			b.log.Warn("health_bridge_notifier_error",
				slog.String("watcher", watcherName),
				slog.String("trigger", trigger),
				slog.String("error", err.Error()),
			)
		}
	}()
}
