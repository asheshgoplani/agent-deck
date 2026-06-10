package mcppool

import (
	"testing"
)

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
