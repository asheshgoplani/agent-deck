package intervalhook

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
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
		rootCtx:        ctx,
		rootCancel:     cancel,
		stopCh:         make(chan struct{}),
		running:        make(map[string]bool),
		supervised:     make(map[string]bool),
	}
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

func TestRunOnce_OverlapSkipped(t *testing.T) {
	// Mark a hook as already running; runOnce must skip (not write the marker).
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	r := newTestRunner(nil)
	r.mu.Lock()
	r.running["busy"] = true
	r.mu.Unlock()

	r.runOnce("busy", session.IntervalHookSettings{Command: "printf ok > " + marker})

	if _, err := os.Stat(marker); err == nil {
		t.Fatal("overlapping run was not skipped: marker was written")
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
	r := newTestRunner(nil)
	// Long command + long timeout: without cancellation, runOnce would block ~30s.
	hook := session.IntervalHookSettings{Command: "sleep 30", TimeoutSeconds: 30}

	done := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		r.runOnce("inflight", hook)
		done <- time.Since(start)
	}()

	// Wait for the run to actually start (the running flag is set under mu).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		up := r.running["inflight"]
		r.mu.Unlock()
		if up {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

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
