package mcppool

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// systemdRunAvailable returns true on Linux when systemd-run is on PATH and
// the user's systemd instance is reachable. Tests that depend on a real
// scope being registered must skip if this returns false.
func systemdRunAvailable() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	if _, err := exec.LookPath("systemd-run"); err != nil {
		return false
	}
	if err := exec.Command("systemctl", "--user", "is-system-running").Run(); err != nil {
		// is-system-running returns non-zero for "degraded" too; that's fine.
		// Only skip if the user manager can't be reached at all (exit > 4).
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() < 5 {
			return true
		}
		return false
	}
	return true
}

// TestWrapMCPCommand_LinuxArgvShape verifies the wrapped argv on Linux
// when systemd-run is available and isolation is enabled. This is the
// regression-gate test: if the wrapper is removed or bypassed, this test
// fails.
func TestWrapMCPCommand_LinuxArgvShape(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux-only: argv shape uses systemd-run")
	}
	if _, err := exec.LookPath("systemd-run"); err != nil {
		t.Skip("systemd-run not on PATH")
	}
	t.Setenv("AGENT_DECK_MCP_ISOLATION", "1")

	cmd, args, wrapped, unit := wrapMCPCommand("sess-abc", "context7", "/usr/bin/npm", []string{"exec", "@upstash/context7-mcp"})

	if !wrapped {
		t.Fatalf("wrapMCPCommand: expected wrapped=true on Linux with systemd-run available; got false")
	}
	if !strings.HasSuffix(cmd, "systemd-run") {
		t.Errorf("cmd: want suffix 'systemd-run', got %q", cmd)
	}
	if !strings.HasPrefix(unit, "mcp-") || !strings.HasSuffix(unit, ".scope") {
		t.Errorf("unit name: want mcp-...scope, got %q", unit)
	}
	if !strings.Contains(unit, "context7") {
		t.Errorf("unit name should embed mcp name 'context7', got %q", unit)
	}

	joined := strings.Join(args, " ")
	wants := []string{
		"--user",
		"--scope",
		"--slice=mcp-pool.slice",
		"MemoryMax=1G",
		"CPUWeight=50",
		"TasksMax=200",
	}
	for _, w := range wants {
		if !strings.Contains(joined, w) {
			t.Errorf("argv missing %q; full args: %v", w, args)
		}
	}

	// The original command and its args must come AFTER the `--` sentinel
	// so user-supplied args can never be interpreted as systemd-run flags.
	sentinelIdx := -1
	for i, a := range args {
		if a == "--" {
			sentinelIdx = i
			break
		}
	}
	if sentinelIdx < 0 {
		t.Fatalf("expected `--` separator in args: %v", args)
	}
	if sentinelIdx+1 >= len(args) || args[sentinelIdx+1] != "/usr/bin/npm" {
		t.Errorf("expected /usr/bin/npm right after `--`, got args=%v", args)
	}
	if sentinelIdx+3 >= len(args) || args[sentinelIdx+2] != "exec" || args[sentinelIdx+3] != "@upstash/context7-mcp" {
		t.Errorf("expected original args after command, got tail=%v", args[sentinelIdx+1:])
	}
}

// TestWrapMCPCommand_DisabledByEnv verifies the env-var opt-out.
func TestWrapMCPCommand_DisabledByEnv(t *testing.T) {
	t.Setenv("AGENT_DECK_MCP_ISOLATION", "0")
	cmd, args, wrapped, unit := wrapMCPCommand("s", "m", "/bin/echo", []string{"hi"})
	if wrapped {
		t.Errorf("expected wrapped=false with AGENT_DECK_MCP_ISOLATION=0")
	}
	if cmd != "/bin/echo" {
		t.Errorf("cmd should pass through unchanged; got %q", cmd)
	}
	if len(args) != 1 || args[0] != "hi" {
		t.Errorf("args should pass through unchanged; got %v", args)
	}
	if unit != "" {
		t.Errorf("unit should be empty when not wrapped; got %q", unit)
	}
}

// TestWrapMCPCommand_FallbackWhenSystemdRunMissing verifies graceful fallback
// when systemd-run is not on PATH (covers Docker containers, minimal images,
// and similar Linux-but-no-systemd environments).
func TestWrapMCPCommand_FallbackWhenSystemdRunMissing(t *testing.T) {
	t.Setenv("AGENT_DECK_MCP_ISOLATION", "1")

	orig := lookupSystemdRun
	t.Cleanup(func() { lookupSystemdRun = orig })
	lookupSystemdRun = func() string { return "" }

	cmd, args, wrapped, _ := wrapMCPCommand("s", "m", "/bin/echo", []string{"hi"})
	if wrapped {
		t.Errorf("expected wrapped=false when systemd-run missing")
	}
	if cmd != "/bin/echo" || len(args) != 1 || args[0] != "hi" {
		t.Errorf("expected pass-through; got cmd=%q args=%v", cmd, args)
	}
}

// TestWrapMCPCommand_NonLinuxFallback verifies the wrapper is a no-op
// on macOS/Windows even with isolation enabled, so dev workflows on
// those platforms aren't broken.
func TestWrapMCPCommand_NonLinuxFallback(t *testing.T) {
	t.Setenv("AGENT_DECK_MCP_ISOLATION", "1")
	orig := runtimeGOOS
	t.Cleanup(func() { runtimeGOOS = orig })
	runtimeGOOS = "darwin"

	cmd, _, wrapped, _ := wrapMCPCommand("s", "m", "/bin/echo", []string{"hi"})
	if wrapped {
		t.Errorf("expected wrapped=false on darwin")
	}
	if cmd != "/bin/echo" {
		t.Errorf("expected pass-through on darwin; got %q", cmd)
	}
}

// TestWrapMCPCommand_DefaultIsOnForLinux: with no env var set, isolation
// should default to ON on Linux (the cascade-prevention default).
func TestWrapMCPCommand_DefaultIsOnForLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux-only")
	}
	if _, err := exec.LookPath("systemd-run"); err != nil {
		t.Skip("systemd-run not on PATH")
	}
	// Setenv-then-unset to guarantee a clean state.
	t.Setenv("AGENT_DECK_MCP_ISOLATION", "")
	_, _, wrapped, _ := wrapMCPCommand("s", "m", "/bin/true", nil)
	if !wrapped {
		t.Errorf("default isolation should be ON on Linux when env unset")
	}
}

// TestWrapMCPCommand_ScopeAppearsInCgls launches a real /bin/sleep through
// the wrapper, then verifies the resulting unit is registered with the
// user's systemd instance via `systemctl --user is-active`.
func TestWrapMCPCommand_ScopeAppearsInCgls(t *testing.T) {
	if !systemdRunAvailable() {
		t.Skip("systemd-run / user manager unavailable")
	}
	t.Setenv("AGENT_DECK_MCP_ISOLATION", "1")

	cmd, args, wrapped, unit := wrapMCPCommand("smoke", "appears", "/bin/sleep", []string{"5"})
	if !wrapped {
		t.Fatalf("expected wrapping in this environment")
	}

	proc := exec.Command(cmd, args...)
	if err := proc.Start(); err != nil {
		t.Fatalf("start wrapped sleep: %v", err)
	}
	t.Cleanup(func() {
		if proc.Process != nil {
			_ = proc.Process.Kill()
		}
		_, _ = proc.Process.Wait()
	})

	// Give systemd-run a moment to register the scope.
	deadline := time.Now().Add(3 * time.Second)
	var lastState string
	for time.Now().Before(deadline) {
		out, _ := exec.Command("systemctl", "--user", "is-active", unit).CombinedOutput()
		lastState = strings.TrimSpace(string(out))
		if lastState == "active" || lastState == "activating" {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("scope %q never became active; last state=%q", unit, lastState)
}
