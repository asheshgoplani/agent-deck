// Package intervalhook runs user-configured shell commands on a wall-clock
// interval while the TUI is running, independent of session activity.
//
// It is a general-purpose "cron inside the TUI" primitive: each configured
// hook (see session.IntervalHookSettings) is a shell command plus a cadence.
// Typical uses are a periodic sync, a health probe, or a poll that dispatches
// work to sessions via the `agent-deck session` CLI. The runner owns no domain
// logic — it just executes the command on schedule and logs the outcome.
//
// Design mirrors internal/sysinfo.Collector (background goroutine +
// time.NewTicker + stopCh) and internal/session.StartMaintenanceWorker
// (config re-read each tick so edits to config.toml take effect without a
// restart). Each hook runs on its own goroutine, wrapped in safego.Go so a
// panicking command can never take down the TUI.
package intervalhook

import (
	"context"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/safego"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// configLoader returns the current set of interval hooks. It is called once
// per tick so config.toml edits are picked up live (add/remove/pause a hook,
// change its cadence) without restarting the TUI. Injected for testability.
type configLoader func() map[string]session.IntervalHookSettings

// defaultLoader reads the hooks from the on-disk user config (mtime-cached by
// session.LoadUserConfig, so per-tick calls are cheap).
func defaultLoader() map[string]session.IntervalHookSettings {
	cfg, err := session.LoadUserConfig()
	if err != nil || cfg == nil {
		return nil
	}
	return cfg.IntervalHooks
}

// Runner supervises all configured interval hooks.
type Runner struct {
	logger *slog.Logger
	load   configLoader

	mu     sync.Mutex
	stopCh chan struct{}
	// running guards against overlapping runs of the SAME hook: if a hook's
	// previous invocation is still executing when its next tick fires, the
	// tick is skipped rather than piling up. Keyed by hook name.
	running map[string]bool
	started bool
}

// New builds a Runner. logger may be nil (panics are still recovered, log
// records dropped). Call Start to launch the supervising goroutines.
func New(logger *slog.Logger) *Runner {
	return &Runner{
		logger:  logger,
		load:    defaultLoader,
		stopCh:  make(chan struct{}),
		running: make(map[string]bool),
	}
}

// Start launches one supervising goroutine per configured hook. It is a no-op
// if there are no enabled hooks, and safe to call once (subsequent calls are
// ignored). Non-blocking: all work happens on background goroutines, so it is
// safe to call on the UI's critical path (Home.Init).
func (r *Runner) Start() {
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return
	}
	r.started = true
	hooks := r.load()
	r.mu.Unlock()

	for name, cfg := range hooks {
		if !cfg.GetEnabled() {
			continue
		}
		name, cfg := name, cfg // capture
		safego.Go(r.logger, "interval_hook:"+name, func() {
			r.runLoop(name, cfg.RunAtStartup)
		})
	}
}

// Stop terminates all hook goroutines. Safe to call once.
func (r *Runner) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.started {
		return
	}
	select {
	case <-r.stopCh:
		// already closed
	default:
		close(r.stopCh)
	}
}

// runLoop drives a single hook. The cadence and command are re-read from config
// each tick (via r.currentHook) so live edits take effect; if the hook is
// removed or disabled, the loop exits cleanly.
func (r *Runner) runLoop(name string, runAtStartup bool) {
	if runAtStartup {
		if cfg, ok := r.currentHook(name); ok {
			r.runOnce(name, cfg)
		}
	}

	// Seed the ticker from the current interval; re-arm it if the interval
	// changes across ticks (StartMaintenanceWorker re-reads config per tick,
	// but its cadence is fixed — here the cadence itself is user-tunable).
	cfg, ok := r.currentHook(name)
	if !ok {
		return
	}
	interval := time.Duration(cfg.GetIntervalSeconds()) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-r.stopCh:
			return
		case <-ticker.C:
			cfg, ok := r.currentHook(name)
			if !ok {
				// Hook removed or disabled at runtime — stop supervising it.
				return
			}
			r.runOnce(name, cfg)
			if newInterval := time.Duration(cfg.GetIntervalSeconds()) * time.Second; newInterval != interval {
				interval = newInterval
				ticker.Reset(interval)
			}
		}
	}
}

// currentHook re-reads the named hook from config, returning false if it is
// gone or disabled.
func (r *Runner) currentHook(name string) (session.IntervalHookSettings, bool) {
	hooks := r.load()
	cfg, ok := hooks[name]
	if !ok || !cfg.GetEnabled() || strings.TrimSpace(cfg.Command) == "" {
		return session.IntervalHookSettings{}, false
	}
	return cfg, true
}

// runOnce executes the hook's command once, bounded by its timeout. Overlapping
// runs of the same hook are skipped: if the previous invocation is still going
// when this tick fires, we log and return rather than stack processes.
func (r *Runner) runOnce(name string, cfg session.IntervalHookSettings) {
	r.mu.Lock()
	if r.running[name] {
		r.mu.Unlock()
		if r.logger != nil {
			r.logger.Warn("interval_hook_overlap_skipped", slog.String("hook", name))
		}
		return
	}
	r.running[name] = true
	r.mu.Unlock()

	defer func() {
		r.mu.Lock()
		delete(r.running, name)
		r.mu.Unlock()
	}()

	timeout := time.Duration(cfg.GetTimeoutSeconds()) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// #nosec G204 -- The command is user-authored config (config.toml
	// [interval_hooks.<name>].command), run intentionally on the user's own
	// machine, exactly like a crontab entry. It is passed as a single argv
	// element to `bash -lc`, matching the vetted convention in
	// internal/tmux.buildBashLCCommand. No external/untrusted input reaches it.
	cmd := exec.CommandContext(ctx, bashPath(), "-lc", cfg.Command)
	start := time.Now()
	out, err := cmd.CombinedOutput()
	elapsed := time.Since(start)

	if r.logger != nil {
		if err != nil {
			r.logger.Warn("interval_hook_failed",
				slog.String("hook", name),
				slog.Duration("elapsed", elapsed),
				slog.Any("error", err),
				slog.String("output", truncate(string(out), 500)),
			)
		} else {
			r.logger.Debug("interval_hook_ran",
				slog.String("hook", name),
				slog.Duration("elapsed", elapsed),
			)
		}
	}
}

// bashPath resolves the bash binary, falling back to the conventional path.
func bashPath() string {
	if p, err := exec.LookPath("bash"); err == nil {
		return p
	}
	return "/bin/bash"
}

// truncate bounds captured output in a log line.
func truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "…(truncated)"
}
