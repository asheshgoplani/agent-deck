package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestLogCgroupIsolationDecision_WiredIntoBootstrap is the end-to-end gate
// proving the OBS-01 call site in main.go actually fires on TUI startup. Two
// independent arms catch two distinct regression classes:
//
//   - "wire_up_line_exists" — line-level grep over main.go. Fails fast (no
//     subprocess required) when a future refactor deletes the call line.
//   - "tui_startup_emits_line" — subprocess integration. Builds the binary,
//     launches the TUI under an isolated HOME, kills it after a short window,
//     and greps the resulting debug.log for the canonical OBS-01 substring.
//     Catches the failure mode where the call line is present but unreachable
//     (e.g. moved after an early os.Exit) — line-level grep cannot detect this.
//
// Both arms must be GREEN simultaneously for OBS-01 to be considered wired.
func TestLogCgroupIsolationDecision_WiredIntoBootstrap(t *testing.T) {
	t.Run("wire_up_line_exists", func(t *testing.T) {
		// Line-level grep — catches "call line deleted in a refactor" regressions.
		data, err := os.ReadFile("main.go")
		if err != nil {
			t.Fatalf("read main.go: %v", err)
		}
		if !strings.Contains(string(data), "session.LogCgroupIsolationDecision()") {
			t.Fatalf("OBS-01-WIRE-UP-MISSING: main.go does not contain session.LogCgroupIsolationDecision() call")
		}
	})

	t.Run("tui_startup_emits_line", func(t *testing.T) {
		// Behavior-level subprocess gate — catches "call line present but
		// unreachable" wire-up bugs (e.g. placed after an early os.Exit).
		if testing.Short() {
			t.Skip("skipping subprocess integration test in short mode")
		}
		tmpHome := t.TempDir()
		xdgConfigHome := filepath.Join(tmpHome, ".config")
		xdgDataHome := filepath.Join(tmpHome, ".local", "share")
		xdgCacheHome := filepath.Join(tmpHome, ".cache")
		if err := os.MkdirAll(filepath.Join(xdgCacheHome, "agent-deck"), 0o755); err != nil {
			t.Fatal(err)
		}
		binPath := filepath.Join(t.TempDir(), "agent-deck-test")
		build := exec.Command("go", "build", "-o", binPath, ".")
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("go build: %v\noutput: %s", err, out)
		}

		// Strip TMUX* and AGENTDECK_* env from the parent process so the
		// nested-session guard in main.go (isNestedSession → GetCurrentSessionID)
		// does not early-exit when the test runs under an outer
		// agent-deck-managed tmux session. Without this filter, the binary
		// prints "Cannot launch the agent-deck TUI inside an agent-deck
		// session" on stderr and never reaches logging.Init.
		var env []string
		for _, kv := range os.Environ() {
			if strings.HasPrefix(kv, "TMUX") {
				continue
			}
			if strings.HasPrefix(kv, "AGENTDECK_") {
				continue
			}
			if strings.HasPrefix(kv, "HOME=") {
				continue
			}
			env = append(env, kv)
		}
		env = append(env,
			"HOME="+tmpHome,
			"XDG_CONFIG_HOME="+xdgConfigHome,
			"XDG_DATA_HOME="+xdgDataHome,
			"XDG_CACHE_HOME="+xdgCacheHome,
			"AGENTDECK_DEBUG=1",
			"AGENTDECK_PROFILE=test-obs01",
			"TERM=dumb",
		)
		cmd := exec.Command(binPath)
		cmd.Env = env
		// TUI blocks on stdin — detach with its own pgroup so it can be
		// SIGTERM'd as a unit once we're done with it (see the deferred
		// cleanup below, which runs on both the success and failure path).
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			t.Fatalf("start binary: %v", err)
		}
		// Register the reap IMMEDIATELY after a successful Start, before any
		// wait/poll that can fail the test: every early-return path below
		// (including t.Fatalf) must still terminate and reap the TUI, or the
		// subprocess is orphaned for the lifetime of the runner.
		t.Cleanup(func() { terminateTUI(cmd) })

		// Poll for the OBS-01 line instead of assuming a fixed 2s startup +
		// 200ms lumberjack-flush budget is always enough. This test builds
		// its own binary AND launches it as a subprocess while the rest of
		// `go test -race ./...` runs concurrently — under CI scheduler
		// contention the fixed budget intermittently elapses before the TUI
		// even reaches session.LogCgroupIsolationDecision(), which is a
		// deadline-too-short flake, not a real wiring regression (#1776:
		// "52s then fails" — the elapsed time is dominated by this test's
		// own go build, leaving the fixed sleeps with no slack). Poll with a
		// generous overall ceiling so a genuine wiring regression still
		// fails, just not on a coin flip.
		logPath := filepath.Join(xdgCacheHome, "agent-deck", "debug.log")
		deadline := time.Now().Add(20 * time.Second)
		var data []byte
		var readErr error
		for time.Now().Before(deadline) {
			data, readErr = os.ReadFile(logPath)
			if readErr == nil && strings.Contains(string(data), "tmux cgroup isolation:") {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		if readErr != nil {
			t.Fatalf("OBS-01-WIRE-UP-MISSING: read debug.log at %s: %v", logPath, readErr)
		}
		t.Fatalf("OBS-01-WIRE-UP-MISSING: debug.log at %s missing 'tmux cgroup isolation:' line after 20s; contents:\n%s", logPath, data)
	})
}

// terminateTUI shuts the started TUI subprocess down and reaps it, without
// ever blocking indefinitely.
//
// SIGTERM goes to the whole process group (the child was started with
// Setpgid) so lumberjack gets a chance to flush and any grandchild goes with
// it. If the TUI declines to exit — a bubbletea program on TERM=dumb has no
// obligation to — the wait would otherwise hang until the package's test
// timeout with the subprocess still running, so escalate to SIGKILL after a
// short grace. Wait() is called exactly once, on the goroutine that owns it,
// and is what actually reaps the child rather than leaving a zombie.
func terminateTUI(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	// Signal first, start the waiter second: no reap can be in flight while
	// the signal is being delivered, so the pgid cannot have been recycled
	// under us. The later SIGKILL is likewise only reached while done is
	// still open, i.e. while Wait has provably not returned.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		_, _ = cmd.Process.Wait()
		close(done)
	}()
	select {
	case <-done:
		return
	case <-time.After(5 * time.Second):
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	<-done
}
