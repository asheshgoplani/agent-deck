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
	"bytes"
	"context"
	"log/slog"
	"os"
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
// that forks a daemon holding stdout/stderr open would block the run
// forever even after the parent is killed.
const waitDelay = 3 * time.Second

// maxCapturedOutput bounds how much combined stdout+stderr of a hook run is
// retained in memory. Only the first 500 chars ever reach a log line (see
// truncate below); the rest is headroom so the boundary isn't mid-word on
// the log path. Everything past the cap is drained and discarded so a hook
// that spews output (a stray `find /`, a lost redirect) can't grow a
// multi-GB buffer inside the TUI process (#1829).
const maxCapturedOutput = 4 * 1024

// boundedBuffer retains the first max bytes written and silently discards the
// rest, always reporting a full write so the child's stdout/stderr never
// blocks or errors. Handed to exec.Cmd as both Stdout and Stderr (same value,
// so the exec package serializes Writes onto it — no locking needed here).
// Deliberately NOT logging.RingBuffer: that keeps the LAST n bytes, while the
// log contract here has always been a head truncation of the output.
type boundedBuffer struct {
	buf bytes.Buffer
	max int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if remaining := b.max - b.buf.Len(); remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		b.buf.Write(p)
	}
	return n, nil
}

// configLoader returns the current set of interval hooks. It is called by the
// supervisor each rescan and by each hook loop each tick so config.toml edits
// are picked up live. Injected for testability.
type configLoader func() map[string]session.IntervalHookSettings

// defaultTickInterval is the production cadence: the hook's configured
// interval_seconds, clamped by GetIntervalSeconds.
func defaultTickInterval(cfg session.IntervalHookSettings) time.Duration {
	return time.Duration(cfg.GetIntervalSeconds()) * time.Second
}

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
	// tickInterval derives a hook loop's cadence from its settings. Production
	// (New) uses the clamped GetIntervalSeconds; tests inject sub-second
	// cadences to exercise ticker-path behavior without >=5s waits. Set once
	// before Start, never mutated after — same discipline as rescanInterval.
	tickInterval func(session.IntervalHookSettings) time.Duration

	// rootCtx is the parent of every hook run's timeout context; rootCancel
	// cancels it. Stop() calls rootCancel so an in-flight run is killed (via
	// the per-run process-group kill + WaitDelay) as soon as the app quits,
	// instead of continuing to run detached until its own timeout.
	rootCtx    context.Context
	rootCancel context.CancelFunc

	// inFlight counts hook runs currently executing; Stop waits (bounded) on
	// it before returning — see Stop for why that wait is load-bearing.
	// Adds happen under mu with a stopCh check, so no Add can race a Wait
	// that saw zero.
	inFlight sync.WaitGroup

	mu     sync.Mutex
	stopCh chan struct{}
	// supervised tracks which hooks currently have a live loop goroutine, so
	// the supervisor starts a loop only for newly-enabled hooks. Keyed by name.
	supervised map[string]bool
	// startupRan tracks which hooks have already fired their run_at_startup
	// command in this process. It deliberately outlives loop lifecycles: a
	// transient config-load failure kills a loop and the supervisor relaunches
	// it once config recovers, and without this map that relaunch would
	// re-fire the startup command (#1829) — "once on TUI start" must mean once
	// per process, not once per loop. Keyed by hook name.
	startupRan map[string]bool
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
		tickInterval:   defaultTickInterval,
		rootCtx:        ctx,
		rootCancel:     cancel,
		stopCh:         make(chan struct{}),
		supervised:     make(map[string]bool),
		startupRan:     make(map[string]bool),
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

// stopWaitTimeout bounds how long Stop blocks for in-flight runs to die after
// their context is cancelled. A cancelled run returns within waitDelay (the
// pipe-close bound) of the group kill, so this only trips if the system is
// wedged; it keeps Stop from hanging shutdown indefinitely and matches the 5s
// worker-drain bounds in performFinalShutdown.
const stopWaitTimeout = 5 * time.Second

// Stop terminates the supervisor and all hook loops, and cancels any in-flight
// hook run: closing stopCh ends the loops, and rootCancel cancels the shared
// context every runOnce derives its timeout from, so a command mid-execution is
// killed (via its process-group kill + WaitDelay) rather than left running
// detached until its own TimeoutSeconds.
//
// Stop then WAITS (bounded by stopWaitTimeout) for in-flight runs to finish.
// The wait is what makes Stop safe to call immediately before os.Exit, as the
// signal handler in cmd/agent-deck/main.go does: context cancellation only
// schedules the group SIGKILL on an exec watchdog goroutine, and an os.Exit
// right after a non-waiting Stop would preempt that goroutine, orphaning the
// hook's process group until its own timeout (#1829).
//
// Safe to call multiple times; concurrent callers each wait.
func (r *Runner) Stop() {
	r.mu.Lock()
	select {
	case <-r.stopCh:
		// already closed
	default:
		close(r.stopCh)
	}
	if r.rootCancel != nil {
		r.rootCancel()
	}
	r.mu.Unlock()

	done := make(chan struct{})
	go func() {
		r.inFlight.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(stopWaitTimeout):
		if r.logger != nil {
			r.logger.Warn("interval_hook_stop_wait_timeout")
		}
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

	// The startupRan check comes before the currentHook config load so a
	// relaunched loop (config flap recovery, disable/re-enable) skips the
	// load entirely instead of fetching a cfg it will discard. The flag is
	// set only once the hook is confirmed live, so a hook that vanished
	// between reconcile and here keeps its startup slot for later.
	if runAtStartup {
		r.mu.Lock()
		already := r.startupRan[name]
		r.mu.Unlock()
		if !already {
			if cfg, ok := r.currentHook(name); ok {
				r.mu.Lock()
				r.startupRan[name] = true
				r.mu.Unlock()
				r.runOnce(name, cfg)
			}
		}
	}

	cfg, ok := r.currentHook(name)
	if !ok {
		return
	}
	interval := r.tickInterval(cfg)
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
			// A run that outlasted its interval left one pending tick in the
			// ticker's buffer; consuming it in the next select iteration would
			// start a back-to-back run. Drop it instead, and log the drop —
			// the documented behavior ("tick is dropped, logged, not
			// stacked"). Each hook runs synchronously on this one loop
			// goroutine, so this drain is the only place an overlapping tick
			// can ever be observed (#1829).
			select {
			case <-ticker.C:
				if r.logger != nil {
					r.logger.Warn("interval_hook_overlap_skipped", slog.String("hook", name))
				}
			default:
			}
			if newInterval := r.tickInterval(cfg); newInterval != interval {
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

// runOnce executes the hook's command once, bounded by its timeout. Overlap of
// the same hook cannot happen here: each hook is driven synchronously by its
// single loop goroutine, and runLoop drops (and logs) any tick that fired
// mid-run.
func (r *Runner) runOnce(name string, cfg session.IntervalHookSettings) {
	r.mu.Lock()
	select {
	case <-r.stopCh:
		// Shutting down — never start new work after Stop began, so Stop's
		// inFlight.Wait can't miss a late Add.
		r.mu.Unlock()
		return
	default:
	}
	r.inFlight.Add(1)
	r.mu.Unlock()

	defer r.inFlight.Done()

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
		// Falls back to the single process if the group signal fails.
		if err := killGroup(cmd.Process); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	// Bound how long we wait for pipes to close after Cancel fires, so a
	// forked daemon holding stdout/stderr open cannot block us forever.
	cmd.WaitDelay = waitDelay

	// Capture combined output through a bounded buffer instead of
	// CombinedOutput: the latter accumulates the entire stream in memory
	// before the log-time truncation, so a hook that unexpectedly spews
	// output could OOM the TUI (#1829). Stdout and Stderr share the one
	// writer, which the exec package serializes, preserving CombinedOutput's
	// interleaving semantics.
	capture := &boundedBuffer{max: maxCapturedOutput}
	cmd.Stdout = capture
	cmd.Stderr = capture

	start := time.Now()
	err := cmd.Run()
	elapsed := time.Since(start)

	// Enforce the documented slot contract ("a hook that forks children can't
	// outlive its slot") on EVERY completion: cmd.Cancel's group kill fires
	// only on the timeout/cancel path, so a hook that backgrounds a child and
	// exits 0 would leak it — one fresh orphan per interval (#1829). The pgid
	// equals the just-reaped leader's pid and stays reserved while any group
	// member lives; if the group is already empty this is a no-op ESRCH.
	_ = killGroup(cmd.Process)

	if r.logger != nil {
		switch {
		case err != nil:
			r.logger.Warn("interval_hook_failed",
				slog.String("hook", name),
				slog.Duration("elapsed", elapsed),
				slog.Any("error", err),
				slog.String("output", truncate(capture.buf.String(), 500)),
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

// Global runner registry, mirroring tmux.SetPipeManager/GetPipeManager: the
// TUI (internal/ui.Home) registers its Runner here so the signal-exit path in
// cmd/agent-deck/main.go — which has no access to the Home value — can stop
// in-flight hooks before os.Exit (#1829).
var (
	globalRunner   *Runner
	globalRunnerMu sync.RWMutex
)

// SetGlobal records the process-wide Runner (called once at TUI startup).
func SetGlobal(r *Runner) {
	globalRunnerMu.Lock()
	globalRunner = r
	globalRunnerMu.Unlock()
}

// GetGlobal returns the process-wide Runner, or nil if none was registered
// (e.g. headless web mode, which never starts interval hooks).
func GetGlobal() *Runner {
	globalRunnerMu.RLock()
	defer globalRunnerMu.RUnlock()
	return globalRunner
}

// killGroup SIGKILLs p's process group (negative pgid — every hook command
// runs as a group leader via Setpgid). Nil-safe; returns the kill error so
// callers can fall back or ignore ESRCH as fits their path.
func killGroup(p *os.Process) error {
	if p == nil {
		return nil
	}
	return syscall.Kill(-p.Pid, syscall.SIGKILL)
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
