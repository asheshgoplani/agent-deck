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
// (config re-read so edits to config.toml take effect without a restart).
//
// A single SUPERVISOR goroutine rescans config on an interval and reconciles
// the set of running hook goroutines: it starts a loop for each newly-enabled
// hook and lets removed/disabled hooks' loops exit. This is what makes live
// add / pause / resume work without a restart (a hook goroutine started once at
// boot would never notice a hook added later, and a disabled hook's loop that
// simply returned could never come back). Each hook loop runs on its own
// goroutine wrapped in safego.Go so a panicking command can never take down the
// TUI. Hook commands run in their own process group with a bounded timeout so a
// daemonizing command cannot wedge its slot forever.
package intervalhook

import (
	"context"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/safego"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// defaultRescanInterval is how often the supervisor re-reads config to pick up
// hooks added / removed / enabled / disabled since the last scan. Held per-Runner
// (r.rescanInterval) rather than as a mutable global so tests can shorten it on
// their own instance without a data race against the supervisor goroutine.
const defaultRescanInterval = 15 * time.Second

// waitDelay bounds how long we wait for a hook's pipes to close after its
// context is cancelled (i.e. after the timeout kill). Without this, a command
// that forks a daemon holding stdout/stderr open would block CombinedOutput
// forever even after the parent is killed.
const waitDelay = 3 * time.Second

// configLoader returns the current set of interval hooks. It is called by the
// supervisor each rescan and by each hook loop each tick so config.toml edits
// are picked up live. Injected for testability.
type configLoader func() map[string]session.IntervalHookSettings

// defaultLoader reads the hooks from the on-disk user config (mtime-cached by
// session.LoadUserConfig, so frequent calls are cheap).
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
	// rescanInterval is the supervisor's config-rescan cadence. Set once in New
	// and never mutated after Start, so the supervisor goroutine reads it
	// race-free. Tests set it on their instance before calling Start.
	rescanInterval time.Duration

	// rootCtx is the parent of every hook run's timeout context; rootCancel
	// cancels it. Stop() calls rootCancel so an in-flight CombinedOutput is
	// killed (via the per-run process-group kill + WaitDelay) as soon as the
	// app quits, instead of continuing to run detached until its own timeout.
	rootCtx    context.Context
	rootCancel context.CancelFunc

	mu     sync.Mutex
	stopCh chan struct{}
	// running guards against overlapping runs of the SAME hook: if a hook's
	// previous invocation is still executing when its next tick fires, the
	// tick is skipped rather than piling up. Keyed by hook name.
	running map[string]bool
	// supervised tracks which hooks currently have a live loop goroutine, so
	// the supervisor starts a loop only for newly-enabled hooks. Keyed by name.
	supervised map[string]bool
	started    bool
}

// New builds a Runner. logger may be nil (panics are still recovered, log
// records dropped). Call Start to launch the supervisor.
func New(logger *slog.Logger) *Runner {
	ctx, cancel := context.WithCancel(context.Background())
	return &Runner{
		logger:         logger,
		load:           defaultLoader,
		rescanInterval: defaultRescanInterval,
		rootCtx:        ctx,
		rootCancel:     cancel,
		stopCh:         make(chan struct{}),
		running:        make(map[string]bool),
		supervised:     make(map[string]bool),
	}
}

// Start launches the supervisor goroutine, which reconciles the running hook
// set against config now and every rescanInterval thereafter. Safe to call
// once (subsequent calls are ignored). Non-blocking — safe on the UI critical
// path (Home.Init).
func (r *Runner) Start() {
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return
	}
	r.started = true
	r.mu.Unlock()

	safego.Go(r.logger, "interval_hook:supervisor", func() {
		r.reconcile() // start any hooks enabled at boot immediately
		ticker := time.NewTicker(r.rescanInterval)
		defer ticker.Stop()
		for {
			select {
			case <-r.stopCh:
				return
			case <-ticker.C:
				r.reconcile()
			}
		}
	})
}

// Stop terminates the supervisor and all hook loops, and cancels any in-flight
// hook run: closing stopCh ends the loops, and rootCancel cancels the shared
// context every runOnce derives its timeout from, so a command mid-execution is
// killed (via its process-group kill + WaitDelay) rather than left running
// detached until its own TimeoutSeconds. Safe to call once; idempotent.
func (r *Runner) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	select {
	case <-r.stopCh:
		// already closed
	default:
		close(r.stopCh)
	}
	if r.rootCancel != nil {
		r.rootCancel()
	}
}

// reconcile scans config and launches a loop goroutine for each enabled hook
// that isn't already supervised. Disabled/removed hooks are left to their own
// loops to notice and exit (which clears their supervised flag). This is the
// mechanism behind live add / pause / resume.
func (r *Runner) reconcile() {
	hooks := r.load()
	for name, cfg := range hooks {
		if !cfg.GetEnabled() || strings.TrimSpace(cfg.Command) == "" {
			continue
		}
		r.mu.Lock()
		alreadyUp := r.supervised[name]
		if !alreadyUp {
			r.supervised[name] = true
		}
		r.mu.Unlock()
		if alreadyUp {
			continue
		}
		name, runAtStartup := name, cfg.RunAtStartup // capture
		safego.Go(r.logger, "interval_hook:"+name, func() {
			r.runLoop(name, runAtStartup)
		})
	}
}

// runLoop drives a single hook. The cadence and command are re-read from config
// each tick (via r.currentHook) so live edits take effect; if the hook is
// removed or disabled, the loop exits and clears its supervised flag so the
// supervisor will restart it on re-enable.
func (r *Runner) runLoop(name string, runAtStartup bool) {
	defer func() {
		r.mu.Lock()
		delete(r.supervised, name)
		r.mu.Unlock()
	}()

	if runAtStartup {
		if cfg, ok := r.currentHook(name); ok {
			r.runOnce(name, cfg)
		}
	}

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
				// Hook removed or disabled at runtime — exit; the supervisor
				// re-launches this loop if the hook is re-enabled later.
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
	// Derive from the Runner's root context (set in New) so Stop() cancels an
	// in-flight run instead of leaving it to finish detached.
	ctx, cancel := context.WithTimeout(r.rootCtx, timeout)
	defer cancel()

	// The command is user-authored config (config.toml
	// [interval_hooks.<name>].command), run intentionally on the user's own
	// machine, exactly like a crontab entry. It is passed as a single argv
	// element to `bash -lc`, matching the vetted convention in
	// internal/tmux.buildBashLCCommand. No external/untrusted input reaches it.
	// #nosec G204
	cmd := exec.CommandContext(ctx, bashPath(), "-lc", cfg.Command)
	// Run the command in its OWN process group so a hook that forks children
	// (e.g. daemonizes) can be killed as a group on timeout, not just the
	// direct child. CommandContext kills only cmd.Process; the negative-PID
	// kill below reaches the whole group.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Kill the process GROUP (negative pgid). Falls back to the single
		// process if the group signal fails.
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	// Bound how long we wait for pipes to close after Cancel fires, so a
	// forked daemon holding stdout/stderr open cannot block us forever.
	cmd.WaitDelay = waitDelay

	start := time.Now()
	out, err := cmd.CombinedOutput()
	elapsed := time.Since(start)

	if r.logger != nil {
		switch {
		case err != nil:
			r.logger.Warn("interval_hook_failed",
				slog.String("hook", name),
				slog.Duration("elapsed", elapsed),
				slog.Any("error", err),
				slog.String("output", truncate(string(out), 500)),
			)
		default:
			// INFO (not DEBUG): a periodic exec primitive should leave a
			// visible trace each time it fires, per maintainer review on #1628.
			r.logger.Info("interval_hook_ran",
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
