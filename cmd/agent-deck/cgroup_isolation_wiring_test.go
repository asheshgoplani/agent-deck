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
		// TUI blocks on stdin — detach with its own pgroup and SIGTERM the
		// whole group after a short window so lumberjack has time to flush.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			t.Fatalf("start binary: %v", err)
		}
		time.Sleep(2 * time.Second)
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		_, _ = cmd.Process.Wait()

		// Allow lumberjack to flush after SIGTERM.
		time.Sleep(200 * time.Millisecond)

		logPath := filepath.Join(xdgCacheHome, "agent-deck", "debug.log")
		data, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatalf("OBS-01-WIRE-UP-MISSING: read debug.log at %s: %v", logPath, err)
		}
		if !strings.Contains(string(data), "tmux cgroup isolation:") {
			t.Fatalf("OBS-01-WIRE-UP-MISSING: debug.log at %s missing 'tmux cgroup isolation:' line; contents:\n%s", logPath, data)
		}
	})
}
