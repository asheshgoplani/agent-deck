package tmux

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runSoftkillHelper implements child-process behaviours selected by env.
// softkillReadyEnv names the readiness-marker path handed to the helper. It is
// namespaced rather than a bare "READY" because the helper inherits the full
// parent environment: a CI runner that already exports READY for its own
// purposes would otherwise point the helper at a path the parent never watches,
// and every spawn would fail its readiness wait.
const softkillReadyEnv = "AGENTDECK_SOFTKILL_READY"

// Dispatched from testmain_test.go's TestMain when SOFTKILL_TEST_HELPER is
// set. We cannot rely on /bin/sh traps because dash/bash on Linux delay trap
// dispatch until the foreground `sleep` returns — SIGTERM-while-sleeping is
// equivalent to SIGKILL from the parent's perspective. A Go helper using
// signal.Notify handles SIGTERM instantly.
//   - "clean": on SIGTERM, touch $MARKER then exit 0 (must run in < grace).
//   - "ignore": install a no-op handler for SIGTERM so it is ignored, then
//     block forever. Parent must fall back to SIGKILL.
//   - "eof_clean": exit 0 on stdin EOF. If SIGTERM arrives first, write
//     $ANTIMARKER and exit 1 (proves the parent took the signal-driven
//     path when it should have taken the EOF path).
//
// Immediately after signal.Notify registers the handler, touch the readiness
// marker (if
// set) so the parent can wait on real readiness instead of guessing — see
// waitForReady's doc comment for why the previous kill(pid,0)+fixed-sleep
// heuristic was flaky (#1776).
func runSoftkillHelper(role string) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM)
	if ready := os.Getenv(softkillReadyEnv); ready != "" {
		_ = os.WriteFile(ready, []byte("ok"), 0o644)
	}
	switch role {
	case "clean":
		<-ch
		if marker := os.Getenv("MARKER"); marker != "" {
			_ = os.WriteFile(marker, []byte("ok"), 0o644)
		}
		os.Exit(0)
	case "ignore":
		// Drain TERM signals indefinitely — parent must SIGKILL.
		go func() {
			for range ch {
			}
		}()
		select {} // block forever
	case "eof_clean":
		// SIGTERM should not arrive on the happy path; if it does,
		// flag the regression.
		go func() {
			<-ch
			if anti := os.Getenv("ANTIMARKER"); anti != "" {
				_ = os.WriteFile(anti, []byte("term"), 0o644)
			}
			os.Exit(1)
		}()
		buf := make([]byte, 1024)
		for {
			_, err := os.Stdin.Read(buf)
			if errors.Is(err, io.EOF) {
				os.Exit(0)
			}
			if err != nil {
				os.Exit(2)
			}
		}
	default:
		os.Exit(2)
	}
}

// spawnHelper starts the test binary in helper mode and returns the cmd.
// Caller is responsible for reaping. Blocks until the helper's SIGTERM
// handler is actually installed (see waitForReady) so a softKill sent
// right after this returns cannot race the child's own startup.
func spawnHelper(t *testing.T, role string, extraEnv ...string) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	require.NoError(t, err)
	cmd := exec.Command(exe, "-test.run=^$") // run no tests in child
	ready := filepath.Join(t.TempDir(), "ready")
	env := append(os.Environ(), "SOFTKILL_TEST_HELPER="+role, softkillReadyEnv+"="+ready)
	env = append(env, extraEnv...)
	cmd.Env = env
	// Isolate child so it doesn't write to the parent's test output.
	cmd.Stdout = nil
	cmd.Stderr = nil
	require.NoError(t, cmd.Start())
	registerOrphanReaper(t, cmd)
	waitForReady(t, ready, 5*time.Second)
	return cmd
}

// spawnHelperInOwnGroup is like spawnHelper but puts the child in its own
// process group via Setpgid. Required for softKillProcessGroup tests:
// without isolation, syscall.Kill(-pgid, ...) on the inherited pgid would
// also signal the test runner itself. Blocks until the helper's SIGTERM
// handler is installed (see waitForReady).
func spawnHelperInOwnGroup(t *testing.T, role string, extraEnv ...string) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	require.NoError(t, err)
	cmd := exec.Command(exe, "-test.run=^$") // run no tests in child
	ready := filepath.Join(t.TempDir(), "ready")
	env := append(os.Environ(), "SOFTKILL_TEST_HELPER="+role, softkillReadyEnv+"="+ready)
	env = append(env, extraEnv...)
	cmd.Env = env
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, cmd.Start())
	registerOrphanReaper(t, cmd)
	waitForReady(t, ready, 5*time.Second)
	return cmd
}

// registerOrphanReaper registers a t.Cleanup that force-kills and reaps cmd,
// guarding against a leaked helper process. It must be called immediately
// after cmd.Start() succeeds — specifically BEFORE waitForReady, whose
// t.Fatalf on a timeout would otherwise abort the test before the caller's
// own (more careful, waitDone-synchronized) cleanup ever gets registered,
// leaving the just-started helper orphaned. The "ignore" role in particular
// drains SIGTERM and blocks forever (see runSoftkillHelper), so an orphaned
// instance of it survives indefinitely on the runner rather than exiting on
// its own — the exact process-exhaustion failure mode this repo has hit
// before (see the tmux hygiene rules in CLAUDE.md).
//
// Cleanups run LIFO, so on the success path this runs after the caller's own
// cleanup and is a cheap no-op (Kill/Wait on an already-reaped process just
// return an error, which is ignored).
//
// The Wait is what actually reaps: a killed-but-unwaited child stays a zombie
// for the lifetime of the test binary, so "kill without wait" is only half a
// cleanup.
//
// Helpers started in their own process group get a group-wide SIGKILL first,
// so a descendant cannot survive the leader. That raw pgid signal is gated on
// a liveness probe: os.Process.Signal reports ErrProcessDone once the process
// has been waited on, and only while it has NOT been waited on is its pid —
// which is also the pgid — guaranteed not to have been recycled. On the normal
// path the caller's own cleanup has already reaped the child, the probe says
// so, and no raw signal is sent at all. Signaling a pgid unconditionally from
// a cleanup is precisely the friendly-fire shape that has bitten this repo
// before, so the probe is load-bearing, not decoration.
func registerOrphanReaper(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	if cmd.Process == nil {
		t.Fatalf("registerOrphanReaper called before a successful cmd.Start()")
	}
	pid := cmd.Process.Pid
	ownGroup := cmd.SysProcAttr != nil && cmd.SysProcAttr.Setpgid
	t.Cleanup(func() {
		// Signal(0) succeeds only while the process has not been reaped, which
		// is exactly when the pid cannot have been handed to anyone else.
		unreaped := cmd.Process.Signal(syscall.Signal(0)) == nil
		if ownGroup && unreaped {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
		_ = cmd.Process.Kill()
		// Must wait, or the killed child lingers as a zombie.
		_, _ = cmd.Process.Wait()
	})
}

// waitForReady polls until the helper's readiness marker file exists, or
// fails the test if the deadline passes first.
//
// This replaces a former kill(pid,0)+fixed-50ms-sleep heuristic
// ("process is visible to the OS, give it a beat to install
// signal.Notify"). That heuristic assumed 50ms was always enough time
// between fork/exec completing and the child's signal.Notify call actually
// registering. Under the -race build the helper binary is instrumented and
// far slower to reach main() under CI load, so the assumption
// intermittently failed: softKillProcess(Group) sent SIGTERM into the gap
// before the handler was installed, the OS default disposition terminated
// the child immediately, and the clean-shutdown marker file the handler
// would have written never appeared — the observed
// "term-handled: no such file or directory" flake (#1776).
//
// The fix is explicit synchronization instead of a bigger guess: the
// helper itself touches the readiness marker the instant signal.Notify
// returns (see
// runSoftkillHelper), and the parent waits on that file rather than on a
// proxy for it.
func waitForReady(t *testing.T, path string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("helper never signaled ready (missing %s) within %s", path, d)
}

// waitForFile polls until path exists or the deadline passes, returning the
// last os.Stat error (nil on success). Used to assert on marker files a child
// writes asynchronously from a signal handler — the write can be delayed on a
// loaded CI runner under -race, so a fixed sleep-then-Stat flakes; polling
// asserts the liveness property ("the file eventually appears") without racing.
func waitForFile(path string, d time.Duration) error {
	deadline := time.Now().Add(d)
	var err error
	for {
		if _, err = os.Stat(path); err == nil {
			return nil
		}
		if !time.Now().Before(deadline) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestKillStaleControlClients_TerminatesCleanlyOnSIGTERM asserts that
// softKillProcess sends SIGTERM first and allows the target to shut down
// cleanly — proven by a marker file the child writes from its SIGTERM
// handler (SIGKILL cannot be trapped, so the marker's existence is
// conclusive evidence the TERM path ran).
//
// Regression guard for #737: the prior implementation sent SIGKILL
// unconditionally, which on macOS Homebrew tmux 3.6a races an unfixed
// NULL-deref in the control-mode notify path and destroys the server.
func TestKillStaleControlClients_TerminatesCleanlyOnSIGTERM(t *testing.T) {
	tmpDir := t.TempDir()
	marker := filepath.Join(tmpDir, "term-handled")

	cmd := spawnHelper(t, "clean", "MARKER="+marker)
	pid := cmd.Process.Pid

	// Reap concurrently so softKillProcess's syscall.Kill(pid, 0) poll
	// sees ESRCH as soon as the child actually exits, instead of seeing
	// a zombie and falsely escalating. Production has the init system
	// reaping stale control clients; the test has to emulate that.
	waitDone := make(chan struct{})
	go func() {
		_, _ = cmd.Process.Wait()
		close(waitDone)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-waitDone
	})

	_ = softKillProcess(pid, 500*time.Millisecond)

	// Let the reaper goroutine finish before asserting on the marker.
	select {
	case <-waitDone:
	case <-time.After(500 * time.Millisecond):
	}

	// Marker must exist — proves the child actually ran its SIGTERM
	// handler. SIGKILL cannot be trapped, so a missing marker means
	// softKillProcess skipped SIGTERM and went straight to SIGKILL —
	// exactly the #737 regression we're guarding against.
	//
	// We deliberately do NOT assert on softKillProcess's return value
	// (usedSIGKILL). When the child is a subprocess of the test binary
	// the zombie cannot be reclaimed until cmd.Wait() returns, and the
	// Go runtime may not schedule the reaper goroutine fast enough for
	// softKillProcess's kill(pid, 0) probe to see ESRCH inside the grace
	// window. In production, control clients are children of tmux (not
	// of agent-deck), so tmux reaps them promptly and ESRCH is observed
	// cleanly. Asserting on marker-existence captures the real
	// regression guarantee (SIGTERM ran before SIGKILL) without the
	// test-specific zombie race.
	//
	// POLL for the marker rather than a single Stat: the child writes it
	// from an async SIGTERM handler, and on a loaded CI runner under -race
	// that write can land after the fixed grace windows above. The property
	// under test is liveness ("the handler eventually ran"), so poll until
	// present with a generous deadline — a genuinely-missing marker (the #737
	// straight-to-SIGKILL regression) still fails after the timeout, while a
	// merely-slow write no longer flakes.
	err := waitForFile(marker, 5*time.Second)
	assert.NoError(t, err, "child's SIGTERM handler must have run (marker file must exist)")

	// The marker is written just before os.Exit, so waitForFile can return
	// while the child is still a zombie. Await the reaper (bounded) before the
	// ESRCH check so the process is actually gone, not merely exiting.
	select {
	case <-waitDone:
	case <-time.After(2 * time.Second):
	}

	// Process should be gone.
	err = syscall.Kill(pid, 0)
	assert.True(t, errors.Is(err, syscall.ESRCH), "child process should be fully reaped; got err=%v", err)
}

// TestKillStaleControlClients_FallsBackToSIGKILL asserts that when the
// target ignores SIGTERM, softKillProcess still kills it via SIGKILL
// within roughly the grace window. This preserves the original
// killStaleControlClients guarantee that stale control clients cannot
// linger indefinitely.
func TestKillStaleControlClients_FallsBackToSIGKILL(t *testing.T) {
	// Helper installs a no-op SIGTERM handler and blocks forever.
	cmd := spawnHelper(t, "ignore")
	pid := cmd.Process.Pid

	waitDone := make(chan struct{})
	go func() {
		_, _ = cmd.Process.Wait()
		close(waitDone)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-waitDone
	})

	start := time.Now()
	usedSIGKILL := softKillProcess(pid, 500*time.Millisecond)
	elapsed := time.Since(start)

	select {
	case <-waitDone:
	case <-time.After(500 * time.Millisecond):
	}

	assert.True(t, usedSIGKILL, "softKillProcess must escalate to SIGKILL when TERM is ignored")
	// Allow generous slack for scheduler jitter — the important
	// invariant is that it didn't hang for seconds.
	assert.Less(t, elapsed, 1500*time.Millisecond, "softKillProcess should return promptly after grace")

	// Confirm the child is actually dead.
	err := syscall.Kill(pid, 0)
	assert.True(t, errors.Is(err, syscall.ESRCH), "child process should be dead after SIGKILL; got err=%v", err)
}

// TestSoftKillProcess_AlreadyDeadIsNoop asserts that calling
// softKillProcess on a PID that is already gone returns false (no
// SIGKILL needed) and does not panic.
func TestSoftKillProcess_AlreadyDeadIsNoop(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exit 0")
	require.NoError(t, cmd.Start())
	registerOrphanReaper(t, cmd) // before any require that could abort pre-Wait
	pid := cmd.Process.Pid
	_, _ = cmd.Process.Wait() // fully reap

	// Race: pid may be recycled on extremely fast systems, but for a
	// freshly-reaped pid within the same goroutine this is stable.
	usedSIGKILL := softKillProcess(pid, 100*time.Millisecond)
	assert.False(t, usedSIGKILL, "already-dead pid should not trigger SIGKILL")
}

// TestControlPipeClose_TerminatesCleanlyOnSIGTERM is the process-group
// analogue of TestKillStaleControlClients_TerminatesCleanlyOnSIGTERM.
// It exercises softKillProcessGroup, the helper now invoked by
// ControlPipe.Close() to tear down the agent-deck-owned `tmux -C` child.
//
// Regression guard for the gap left by #739: that PR softened
// killStaleControlClients but missed ControlPipe.Close(), which still
// SIGKILL'd the active control pipe and continued to crash tmux 3.6a
// via the unfixed NULL-deref in tmux/tmux#4980.
func TestControlPipeClose_TerminatesCleanlyOnSIGTERM(t *testing.T) {
	tmpDir := t.TempDir()
	marker := filepath.Join(tmpDir, "term-handled")

	cmd := spawnHelperInOwnGroup(t, "clean", "MARKER="+marker)
	pid := cmd.Process.Pid
	pgid, err := syscall.Getpgid(pid)
	require.NoError(t, err)
	// Sanity: child must be group leader of its own group, not sharing
	// the test runner's pgid. Without this, kill(-pgid) would also
	// signal the test binary.
	require.Equal(t, pid, pgid, "child must be its own pgroup leader")

	waitDone := make(chan struct{})
	go func() {
		_, _ = cmd.Process.Wait()
		close(waitDone)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-waitDone
	})

	_ = softKillProcessGroup(pgid, 500*time.Millisecond)

	select {
	case <-waitDone:
	case <-time.After(500 * time.Millisecond):
	}

	// Marker existence proves the SIGTERM handler ran — SIGKILL cannot
	// be trapped, so a missing marker means softKillProcessGroup
	// skipped SIGTERM and went straight to SIGKILL. Poll (not a single
	// Stat) for the async handler write — see waitForFile.
	err = waitForFile(marker, 5*time.Second)
	assert.NoError(t, err, "child's SIGTERM handler must have run (marker file must exist)")

	// Marker is written just before os.Exit; await the reaper (bounded) so the
	// child is actually gone before the ESRCH check, not merely exiting.
	select {
	case <-waitDone:
	case <-time.After(2 * time.Second):
	}

	err = syscall.Kill(pid, 0)
	assert.True(t, errors.Is(err, syscall.ESRCH), "child process should be fully reaped; got err=%v", err)
}

// TestControlPipeClose_FallsBackToSIGKILL is the pgroup analogue of
// TestKillStaleControlClients_FallsBackToSIGKILL. When the control
// client ignores SIGTERM, softKillProcessGroup must still reap it via
// SIGKILL within roughly the grace window — preserving the original
// "stuck clients cannot linger" guarantee that the unsoftened SIGKILL
// in Close() previously enforced.
func TestControlPipeClose_FallsBackToSIGKILL(t *testing.T) {
	cmd := spawnHelperInOwnGroup(t, "ignore")
	pid := cmd.Process.Pid
	pgid, err := syscall.Getpgid(pid)
	require.NoError(t, err)
	require.Equal(t, pid, pgid, "child must be its own pgroup leader")

	waitDone := make(chan struct{})
	go func() {
		_, _ = cmd.Process.Wait()
		close(waitDone)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-waitDone
	})

	start := time.Now()
	usedSIGKILL := softKillProcessGroup(pgid, 500*time.Millisecond)
	elapsed := time.Since(start)

	select {
	case <-waitDone:
	case <-time.After(500 * time.Millisecond):
	}

	assert.True(t, usedSIGKILL, "softKillProcessGroup must escalate to SIGKILL when TERM is ignored")
	assert.Less(t, elapsed, 1500*time.Millisecond, "softKillProcessGroup should return promptly after grace")

	err = syscall.Kill(pid, 0)
	assert.True(t, errors.Is(err, syscall.ESRCH), "child process should be dead after SIGKILL; got err=%v", err)
}

// spawnHelperWithStdinPipe is like spawnHelperInOwnGroup but additionally
// connects an os.Pipe to the child's stdin and returns the writable end
// so the test can close it to deliver EOF to the helper. Used by
// reapWithEOFGrace tests where the production code's contract is "close
// stdin and wait." Blocks until the helper's SIGTERM handler is installed
// (see waitForReady).
func spawnHelperWithStdinPipe(t *testing.T, role string, extraEnv ...string) (*exec.Cmd, io.WriteCloser) {
	t.Helper()
	exe, err := os.Executable()
	require.NoError(t, err)
	cmd := exec.Command(exe, "-test.run=^$")
	ready := filepath.Join(t.TempDir(), "ready")
	env := append(os.Environ(), "SOFTKILL_TEST_HELPER="+role, softkillReadyEnv+"="+ready)
	env = append(env, extraEnv...)
	cmd.Env = env
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	// Close the parent's write end on cleanup, registered before Start so even
	// a failed Start cannot leak the fd. We never call cmd.Wait() (the tests
	// reap via cmd.Process.Wait()), so exec.Cmd never closes it for us: an
	// early return would otherwise leak the fd AND keep the child's stdin open
	// forever, denying it the EOF it is waiting for. Double-close is harmless —
	// the tests that close it themselves get an already-closed error here,
	// which is ignored.
	t.Cleanup(func() { _ = stdin.Close() })
	require.NoError(t, cmd.Start())
	registerOrphanReaper(t, cmd)
	waitForReady(t, ready, 5*time.Second)
	return cmd, stdin
}

// TestReapWithEOFGrace_FastPathOnEOF asserts that when the child exits
// cleanly on stdin EOF, reapWithEOFGrace returns usedFallback=false and
// does NOT send any signal to the child. Antimarker absence is the
// conclusive evidence — its presence would mean SIGTERM was delivered.
//
// This is the core regression guard for the controlpipe.go EOF-clean
// shutdown fix: the whole point is to avoid signaling the child when
// stdin EOF is sufficient (it is, in 1-4ms — see PLAN.md "Empirical
// validation").
func TestReapWithEOFGrace_FastPathOnEOF(t *testing.T) {
	tmpDir := t.TempDir()
	antiMarker := filepath.Join(tmpDir, "term-fired")

	cmd, stdin := spawnHelperWithStdinPipe(t, "eof_clean", "ANTIMARKER="+antiMarker)

	// Mirror production: caller closes stdin, then reapWithEOFGrace runs
	// reap (cmd.Wait wrapped) with a timeout.
	require.NoError(t, stdin.Close())

	once := sync.Once{}
	reap := func() {
		once.Do(func() {
			_, _ = cmd.Process.Wait()
		})
	}

	// Generous eofGrace so race-instrumented children have time to exit
	// (the production setting of 200ms is sized against non-instrumented
	// tmux which exits in 1-4ms; under -race the same Go child easily
	// takes >200ms). The assertion that matters is usedFallback=false +
	// antimarker-absence, not strict timing — those prove the EOF path
	// completed without a signal.
	usedFallback := reapWithEOFGrace(reap, cmd.Process, 5*time.Second, 500*time.Millisecond)

	assert.False(t, usedFallback, "EOF-clean child must not trigger signal-driven fallback")

	_, err := os.Stat(antiMarker)
	assert.True(t, errors.Is(err, os.ErrNotExist),
		"antimarker must not exist — its existence proves SIGTERM was delivered (regression: code went down the signal path)")
}

// TestReapWithEOFGrace_FallbackOnHungChild asserts that when the child
// ignores stdin EOF AND ignores SIGTERM, reapWithEOFGrace escalates all
// the way to SIGKILL and returns usedFallback=true. This guarantees a
// stuck client cannot linger indefinitely — preserves the safety
// guarantee the original SIGKILL provided, while keeping the fast path
// signal-free.
func TestReapWithEOFGrace_FallbackOnHungChild(t *testing.T) {
	// "ignore" helper: ignores SIGTERM and never reads stdin. EOF won't
	// reach it (it's not reading); SIGTERM is drained; only SIGKILL ends
	// it. The exact fallback worst case.
	cmd := spawnHelperInOwnGroup(t, "ignore")
	pid := cmd.Process.Pid
	pgid, err := syscall.Getpgid(pid)
	require.NoError(t, err)
	require.Equal(t, pid, pgid, "child must be its own pgroup leader")

	once := sync.Once{}
	reap := func() {
		once.Do(func() {
			_, _ = cmd.Process.Wait()
		})
	}

	start := time.Now()
	// Short eofGrace so the test runs fast; killGrace controls SIGTERM→SIGKILL.
	usedFallback := reapWithEOFGrace(reap, cmd.Process, 50*time.Millisecond, 200*time.Millisecond)
	elapsed := time.Since(start)

	assert.True(t, usedFallback, "hung child must trigger signal-driven fallback")
	// Worst case: 50ms eofGrace + 200ms killGrace + scheduler slack.
	assert.Less(t, elapsed, 1*time.Second,
		"reapWithEOFGrace must return promptly after fallback (got %v)", elapsed)

	err = syscall.Kill(pid, 0)
	assert.True(t, errors.Is(err, syscall.ESRCH),
		"child must be fully reaped after fallback; got err=%v", err)
}

// TestReapWithEOFGrace_AlreadyDeadIsNoop asserts that calling
// reapWithEOFGrace on a child that has already exited returns
// usedFallback=false and does not panic — covers the race where the
// child wins the EOF/signal race before reapWithEOFGrace even starts.
func TestReapWithEOFGrace_AlreadyDeadIsNoop(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exit 0")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, cmd.Start())
	registerOrphanReaper(t, cmd) // before any require that could abort pre-Wait
	proc := cmd.Process

	// Already-reaped: the once-guarded reap completes instantly.
	once := sync.Once{}
	reaped := false
	reap := func() {
		once.Do(func() {
			_, _ = proc.Wait()
			reaped = true
		})
	}

	usedFallback := reapWithEOFGrace(reap, proc, 100*time.Millisecond, 100*time.Millisecond)
	assert.False(t, usedFallback, "already-exiting child should not trigger fallback")
	assert.True(t, reaped, "reap function must have been called")
}

// TestSoftKillProcessGroup_AlreadyDeadIsNoop asserts that calling
// softKillProcessGroup on a pgid whose group is already empty returns
// false (no SIGKILL escalation) and does not panic — mirrors
// TestSoftKillProcess_AlreadyDeadIsNoop.
func TestSoftKillProcessGroup_AlreadyDeadIsNoop(t *testing.T) {
	// Spawn a single-process group, reap it, then soft-kill the empty pgid.
	cmd := exec.Command("sh", "-c", "exit 0")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, cmd.Start())
	registerOrphanReaper(t, cmd) // Getpgid below can fail the test pre-Wait
	pid := cmd.Process.Pid
	pgid, err := syscall.Getpgid(pid)
	require.NoError(t, err)
	_, _ = cmd.Process.Wait() // fully reap; group is now empty

	usedSIGKILL := softKillProcessGroup(pgid, 100*time.Millisecond)
	assert.False(t, usedSIGKILL, "already-empty pgroup should not trigger SIGKILL")
}
