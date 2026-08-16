package watcher

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

const healthPanicStackLogInterval = 5 * time.Minute

// recoveredAdapter contains HealthCheck and Teardown panics for every registered
// adapter and gives each Setup attempt one rollback opportunity. Setup and Listen
// retain their existing engine-level behavior and are not panic recovery boundaries.
type recoveredAdapter struct {
	raw    WatcherAdapter
	config AdapterConfig
	log    *slog.Logger

	teardownMu         sync.Mutex
	teardownDone       bool
	teardownErr        error
	healthPanic        atomic.Bool
	healthLogMu        sync.Mutex
	lastHealthPanicLog time.Time
	now                func() time.Time
}

func newRecoveredAdapter(raw WatcherAdapter, config AdapterConfig, logger *slog.Logger) WatcherAdapter {
	return &recoveredAdapter{raw: raw, config: config, log: logger, now: time.Now}
}

func (a *recoveredAdapter) Setup(ctx context.Context, config AdapterConfig) error {
	// A registration is normally set up once. Resetting the teardown generation
	// here also keeps a future explicit retry from consuming its final cleanup on
	// an earlier failed attempt.
	a.teardownMu.Lock()
	a.teardownDone = false
	a.teardownErr = nil
	a.teardownMu.Unlock()

	err := a.raw.Setup(ctx, config)
	if err != nil {
		// Setup is not transactional. Roll back partial resources here; engines
		// that skip failed entries and engines that teardown every entry both
		// compose with the decorator's exactly-once Teardown.
		if cleanupErr := a.Teardown(); cleanupErr != nil {
			a.log.Warn("adapter_failed_setup_cleanup_error",
				slog.String("watcher", a.config.Name),
				slog.String("type", a.config.Type),
				slog.String("error", cleanupErr.Error()),
			)
		}
	}
	return err
}

func (a *recoveredAdapter) Listen(ctx context.Context, events chan<- Event) error {
	return a.raw.Listen(ctx, events)
}

func (a *recoveredAdapter) Teardown() (err error) {
	a.teardownMu.Lock()
	defer a.teardownMu.Unlock()
	if a.teardownDone {
		return a.teardownErr
	}
	a.teardownDone = true
	defer func() {
		if recovered := recover(); recovered != nil {
			a.logPanic("adapter_teardown_panic", recovered)
			a.teardownErr = fmt.Errorf("watcher adapter teardown panic (%T)", recovered)
			err = a.teardownErr
		}
	}()
	a.teardownErr = a.raw.Teardown()
	return a.teardownErr
}

func (a *recoveredAdapter) HealthCheck() (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if a.healthPanic.CompareAndSwap(false, true) && a.shouldLogHealthPanic() {
				a.logPanic("adapter_health_panic", recovered)
			}
			err = fmt.Errorf("watcher adapter health check panic (%T)", recovered)
		}
	}()
	err = a.raw.HealthCheck()
	if err == nil {
		a.healthPanic.Store(false)
	}
	return err
}

func (a *recoveredAdapter) shouldLogHealthPanic() bool {
	a.healthLogMu.Lock()
	defer a.healthLogMu.Unlock()
	now := a.now()
	if !a.lastHealthPanicLog.IsZero() && now.Before(a.lastHealthPanicLog.Add(healthPanicStackLogInterval)) {
		return false
	}
	a.lastHealthPanicLog = now
	return true
}

// logPanic intentionally records only the dynamic type. runtime.Stack captures
// the current goroutine without serializing the recovered value, which may
// contain credentials.
func (a *recoveredAdapter) logPanic(event string, recovered any) {
	stack := make([]byte, 64<<10)
	n := runtime.Stack(stack, false)
	a.log.Error(event,
		slog.String("watcher", a.config.Name),
		slog.String("type", a.config.Type),
		slog.String("panic_type", fmt.Sprintf("%T", recovered)),
		slog.String("stack", string(stack[:n])),
	)
}
