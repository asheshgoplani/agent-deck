package intervalhook

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/integration"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/testutil/logassert"
)

// newTestRunner builds a Runner with an injected loader (no config.toml I/O),
// mirroring New's field init (incl. the root context) so runOnce/Stop behave
// exactly as in production.
func newTestRunner(load configLoader) *Runner {
	ctx, cancel := context.WithCancel(context.Background())
	return &Runner{
		logger:         nil,
		load:           load,
		rescanInterval: defaultRescanInterval,
		tickInterval:   defaultTickInterval,
		rootCtx:        ctx,
		rootCancel:     cancel,
		stopCh:         make(chan struct{}),
		supervised:     make(map[string]bool),
		startupRan:     make(map[string]bool),
	}
}

// readPid parses a pid that a hook command wrote via `echo $$ > file` (or
// `$!`), returning 0 while the file is missing or incomplete.
func readPid(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}

// requirePid returns the pid recorded at path, failing the test if absent,
// and guarantees the process cannot leak past the test.
func requirePid(t *testing.T, path string) int {
	t.Helper()
	pid := readPid(path)
	if pid == 0 {
		t.Fatalf("no valid pid recorded in %s", path)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	return pid
}

func TestRunOnce_ExecutesCommand(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	hook := session.IntervalHookSettings{
		Command: "printf ok > " + marker,
	}
	r := newTestRunner(nil)
	r.runOnce("test", hook)

	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("command did not run (marker not written): %v", err)
	}
	if string(got) != "ok" {
		t.Fatalf("marker = %q, want %q", string(got), "ok")
	}
}

// TestRunOnce_OutputCaptureIsBounded is the regression test for #1829 item 1:
// a hook that unexpectedly spews output (a stray `find /`, a script that loses
// its redirect) must not have all of it buffered in memory before the log-time
// truncation — with CombinedOutput a multi-GB stream could OOM the TUI. The
// proxy for "unbounded buffering" is cumulative heap allocation across the run:
// a bounded capture stays orders of magnitude below the output size.
func TestRunOnce_OutputCaptureIsBounded(t *testing.T) {
	r := newTestRunner(nil)
	const outputBytes = 64 << 20 // 64 MiB of hook output
	hook := session.IntervalHookSettings{
		Command: fmt.Sprintf("head -c %d /dev/zero", outputBytes),
	}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	r.runOnce("spew", hook)
	runtime.ReadMemStats(&after)

	allocated := after.TotalAlloc - before.TotalAlloc
	if allocated > outputBytes/2 {
		t.Fatalf("runOnce allocated %d bytes while capturing a %d-byte output stream (unbounded capture); want allocations well below the stream size", allocated, outputBytes)
	}
}

// TestRunOnce_KillsBackgroundedChildrenOnCompletion is the regression test for
// #1829 item 3: config-reference.md promises "a hook that forks children (or
// daemonizes) can't outlive its slot", but the process-group kill lived only in
// cmd.Cancel, which fires on the timeout/cancel path — a hook that backgrounds
// a child and exits 0 immediately leaked it. The group must be killed on EVERY
// run completion. The child redirects its stdio so it doesn't hold the run's
// output pipes open (that would only add WaitDelay latency, not change the leak).
func TestRunOnce_KillsBackgroundedChildrenOnCompletion(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "bg.pid")
	r := newTestRunner(nil)
	r.runOnce("bg", session.IntervalHookSettings{
		Command: fmt.Sprintf("sleep 30 >/dev/null 2>&1 & echo $! > %s", pidFile),
	})
	pid := requirePid(t, pidFile)

	// The child must be gone shortly after runOnce returns (allow a brief
	// window for the orphan to be reaped after SIGKILL).
	integration.WaitForCondition(t, 2*time.Second, 20*time.Millisecond,
		"backgrounded child killed with its slot after a normal exit",
		func() bool { return syscall.Kill(pid, 0) != nil })
}

// TestRunOnce_FailureLogsCapturedOutput pins the logging contract across the
// CombinedOutput→boundedBuffer change: a failing hook's log record must still
// carry the (truncated) combined output it produced.
func TestRunOnce_FailureLogsCapturedOutput(t *testing.T) {
	cap := logassert.NewCapture()
	r := newTestRunner(nil)
	r.logger = slog.New(cap)

	r.runOnce("boom", session.IntervalHookSettings{Command: "echo boom-output; exit 3"})

	rec := cap.MustOne(t, "interval_hook_failed")
	if got := rec.String("output"); !strings.Contains(got, "boom-output") {
		t.Fatalf("interval_hook_failed record lacks the command output; output = %q", got)
	}
}

func TestRunOnce_TimeoutKillsCommand(t *testing.T) {
	// A 1s-timeout hook running `sleep 5` must return well before 5s.
	r := newTestRunner(nil)
	hook := session.IntervalHookSettings{Command: "sleep 5", TimeoutSeconds: 1}
	start := time.Now()
	r.runOnce("slow", hook)
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("timeout not enforced: runOnce took %v (want < 3s)", elapsed)
	}
}

// TestStop_CancelsInFlightRun is the regression test for the #1628 review item:
// Stop() must cancel a hook mid-execution (via the shared root context), not
// leave it running until its own (long) timeout. A `sleep 30` hook with a large
// timeout is started; Stop() during the run must let runOnce return promptly.
func TestStop_CancelsInFlightRun(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "inflight.pid")
	r := newTestRunner(nil)
	// Long command + long timeout: without cancellation, runOnce would block ~30s.
	hook := session.IntervalHookSettings{
		Command:        fmt.Sprintf("echo $$ > %s; sleep 30", pidFile),
		TimeoutSeconds: 30,
	}

	done := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		r.runOnce("inflight", hook)
		done <- time.Since(start)
	}()

	// Wait for the run to actually start. Failing hard on the deadline
	// matters (#1829 item 6): without it the test called Stop() regardless
	// and could pass vacuously when the run never started.
	integration.WaitForCondition(t, 2*time.Second, 10*time.Millisecond,
		"in-flight run started (pid file written)",
		func() bool { return readPid(pidFile) != 0 })

	r.Stop() // must cancel the in-flight run's context

	select {
	case elapsed := <-done:
		if elapsed > 5*time.Second {
			t.Fatalf("Stop did not cancel the in-flight run: runOnce took %v (want < 5s)", elapsed)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("in-flight run did not return within 6s after Stop (not cancelled)")
	}
}

// TestStop_WaitsForInFlightKillDelivery is the regression test for #1829
// item 2 (runner side): the signal-exit path in cmd/agent-deck/main.go calls
// os.Exit immediately after Stop(), and hook commands run in their own process
// group — detached from the terminal's hangup safety net — so if Stop returns
// before the group SIGKILL actually lands, the hook is orphaned and keeps
// running until its own timeout (up to 24h). Stop must therefore block
// (bounded) until in-flight runs are dead, not just fire the cancel and return:
// context cancellation only schedules the kill on the exec watchdog goroutine,
// which os.Exit would never let run.
func TestStop_WaitsForInFlightKillDelivery(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "hook.pid")
	r := newTestRunner(nil)
	hook := session.IntervalHookSettings{
		// $$ is the bash pid — the process-group leader runOnce kills.
		Command: fmt.Sprintf("echo $$ > %s; sleep 30", pidFile),
		// Long timeout: only Stop can end this run early.
		IntervalSeconds: 86400, TimeoutSeconds: 86400,
	}
	go r.runOnce("inflight", hook)

	// Wait for the hook process to exist.
	integration.WaitForCondition(t, 2*time.Second, 10*time.Millisecond,
		"hook process wrote its pid",
		func() bool { return readPid(pidFile) != 0 })
	pid := requirePid(t, pidFile)

	r.Stop()

	// No polling, no grace: by the time Stop returns, the kill must have been
	// delivered and the process reaped (runOnce's Wait returned), exactly the
	// guarantee the signal handler needs before os.Exit.
	if syscall.Kill(pid, 0) == nil {
		t.Fatalf("hook process (pid %d) still alive when Stop returned; os.Exit on the signal path would orphan it", pid)
	}
}

func TestStart_RunAtStartupFiresImmediately(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "startup")
	load := func() map[string]session.IntervalHookSettings {
		return map[string]session.IntervalHookSettings{
			"boot": {
				Command:      "printf up > " + marker,
				RunAtStartup: true,
				// Large interval so only the startup run fires during the test.
				IntervalSeconds: 3600,
			},
		}
	}
	r := newTestRunner(load)
	r.Start()
	defer r.Stop()

	// Poll briefly for the startup run.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			return // success
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("run_at_startup command did not fire within 2s")
}

// TestRunAtStartup_FiresOncePerProcessAcrossConfigFlaps is the regression test
// for #1829 item 4: a transient config-load failure (e.g. a briefly-malformed
// config.toml during a live edit) makes the hook's loop exit; the supervisor
// relaunches it on the next rescan once config loads again. run_at_startup must
// NOT re-fire on that relaunch — it is documented as "once immediately on TUI
// start", not "once per loop lifecycle".
func TestRunAtStartup_FiresOncePerProcessAcrossConfigFlaps(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "startup-runs")

	var mu sync.Mutex
	loadable := true // false simulates LoadUserConfig transiently failing
	// The trailing sleep keeps the startup run in flight long enough for the
	// test to flip loadable=false before the loop's post-startup config read,
	// so the loop deterministically observes the flap and exits.
	hook := map[string]session.IntervalHookSettings{
		"boot": {
			Command:         "echo run >> " + marker + "; sleep 1",
			RunAtStartup:    true,
			IntervalSeconds: 3600, // only startup runs matter here
		},
	}
	load := func() map[string]session.IntervalHookSettings {
		mu.Lock()
		defer mu.Unlock()
		if !loadable {
			return nil
		}
		return hook
	}

	r := newTestRunner(load)
	r.rescanInterval = 100 * time.Millisecond
	r.Start()
	defer r.Stop()

	countRuns := func() int {
		data, err := os.ReadFile(marker)
		if err != nil {
			return 0
		}
		return strings.Count(string(data), "run")
	}
	supervised := func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.supervised["boot"]
	}

	// 1. The startup run fires (marker written; the command then sleeps).
	// Flip the config to failing while the run is still in flight, so the
	// loop's post-startup config read sees the flap and the loop exits.
	integration.WaitForCondition(t, 3*time.Second, 10*time.Millisecond,
		"initial startup run", func() bool { return countRuns() == 1 })
	mu.Lock()
	loadable = false
	mu.Unlock()
	integration.WaitForCondition(t, 3*time.Second, 10*time.Millisecond,
		"hook loop exit on load failure", func() bool { return !supervised() })

	// 3. Config recovers: the supervisor relaunches the loop.
	mu.Lock()
	loadable = true
	mu.Unlock()
	integration.WaitForCondition(t, 3*time.Second, 10*time.Millisecond,
		"loop relaunch after recovery", func() bool { return supervised() })

	// 4. The relaunched loop must NOT re-fire the startup command. Give a
	// buggy implementation ample time to do so before declaring success.
	time.Sleep(500 * time.Millisecond)
	if got := countRuns(); got != 1 {
		t.Fatalf("run_at_startup fired %d times across a config flap; want exactly 1 per TUI launch", got)
	}
}

func TestStart_DisabledHookDoesNotRun(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "should-not-exist")
	disabled := false
	load := func() map[string]session.IntervalHookSettings {
		return map[string]session.IntervalHookSettings{
			"off": {
				Command:      "printf x > " + marker,
				RunAtStartup: true,
				Enabled:      &disabled,
			},
		}
	}
	r := newTestRunner(load)
	r.Start()
	defer r.Stop()

	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("disabled hook ran")
	}
}

func TestStart_Idempotent(t *testing.T) {
	// Start launches an async supervisor; calling it twice must not launch a
	// second one. We can't count loader calls (the supervisor loads on its own
	// goroutine + on a timer), so assert the started guard directly.
	load := func() map[string]session.IntervalHookSettings { return nil }
	r := newTestRunner(load)
	r.Start()
	r.Start() // second call must be a no-op (started guard)
	defer r.Stop()

	r.mu.Lock()
	started := r.started
	r.mu.Unlock()
	if !started {
		t.Fatal("Start did not set the started guard")
	}
	// A second Start must not have re-armed anything: Stop must cleanly close
	// once (a double-close would panic). This exercises the guard end-to-end.
}

// TestRunLoop_TickDuringRunIsDroppedAndLogged is the regression test for #1829
// item 5, exercising the REAL ticker path (the old overlap test hand-set the
// running flag, a state the synchronous loop can never reach on its own).
// Documented contract: "if a run is still going when the next tick fires, that
// tick is dropped (logged, not stacked)". time.Ticker retains one pending
// tick, so without an explicit drain a long run is followed by an immediate
// back-to-back run and no drop is ever logged.
func TestRunLoop_TickDuringRunIsDroppedAndLogged(t *testing.T) {
	cap := logassert.NewCapture()
	load := func() map[string]session.IntervalHookSettings {
		return map[string]session.IntervalHookSettings{
			// Each run (350ms) spans several 100ms ticks.
			"slow": {Command: "sleep 0.35", IntervalSeconds: 3600},
		}
	}
	r := newTestRunner(load)
	r.logger = slog.New(cap)
	// Sub-second cadence for the test; production derives this from the
	// clamped (>=5s) GetIntervalSeconds, which would make this test crawl.
	r.tickInterval = func(session.IntervalHookSettings) time.Duration { return 100 * time.Millisecond }
	r.Start()
	defer r.Stop()

	// Wait until at least two runs completed, so at least one tick has fired
	// mid-run and been handled one way or the other.
	integration.WaitForCondition(t, 5*time.Second, 10*time.Millisecond,
		"two completed hook runs",
		func() bool { return len(cap.WithMessage("interval_hook_ran")) >= 2 })
	if len(cap.WithMessage("interval_hook_overlap_skipped")) == 0 {
		t.Fatal("no interval_hook_overlap_skipped logged although every run spans multiple ticks; pending tick was consumed as a back-to-back run instead of being dropped")
	}
}

// TestGlobalRunnerRegistry covers the SetGlobal/GetGlobal pair the signal-exit
// path in cmd/agent-deck/main.go depends on (#1829 item 2): the handler has no
// access to the ui.Home value owning the Runner, so it reaches it through this
// registry, exactly like tmux.GetPipeManager in the same handler.
func TestGlobalRunnerRegistry(t *testing.T) {
	t.Cleanup(func() { SetGlobal(nil) })

	SetGlobal(nil)
	if got := GetGlobal(); got != nil {
		t.Fatalf("GetGlobal() = %v with no runner registered; want nil", got)
	}
	r := newTestRunner(nil)
	SetGlobal(r)
	if got := GetGlobal(); got != r {
		t.Fatalf("GetGlobal() did not return the registered runner")
	}
}

// TestSupervisor_LiveReEnable is the regression test for the #1628 review item:
// a hook disabled at boot must START running once it is re-enabled in config,
// without a restart. The supervisor rescans, so re-enabling brings it to life.
func TestSupervisor_LiveReEnable(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "reenabled")

	var mu sync.Mutex
	enabled := false // starts DISABLED
	load := func() map[string]session.IntervalHookSettings {
		mu.Lock()
		en := enabled
		mu.Unlock()
		e := en
		return map[string]session.IntervalHookSettings{
			"toggle": {
				Command:         "printf on > " + marker,
				RunAtStartup:    true,
				IntervalSeconds: 3600, // only the run-at-startup fire matters
				Enabled:         &e,
			},
		}
	}
	// Short rescan so the test doesn't wait the production 15s. Set on the
	// instance BEFORE Start (never mutated after), so the supervisor goroutine
	// reads it race-free.
	r := newTestRunner(load)
	r.rescanInterval = 150 * time.Millisecond

	r.Start()
	defer r.Stop()

	// While disabled, the marker must NOT appear.
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("disabled hook ran before being enabled")
	}

	// Re-enable in config; the supervisor's next rescan should launch it.
	mu.Lock()
	enabled = true
	mu.Unlock()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			return // success: hook came to life after re-enable, no restart
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("re-enabled hook did not start within 3s (live re-enable broken)")
}
