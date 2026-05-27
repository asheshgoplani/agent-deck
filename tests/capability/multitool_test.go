//go:build capability_e2e

package capability

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// setupTwoStubTools installs the echobot stub script into the sandbox and
// registers it under TWO distinct tool names in config.toml. Both tools share
// the same deterministic script (it prints "ECHOBOT READY" and echoes input),
// so the test proves agent-deck can stand up and message more than one tool
// kind without depending on a real agent's creds. Returns the two tool names.
func setupTwoStubTools(t *testing.T, c *capSandbox) (string, string) {
	t.Helper()

	src, err := filepath.Abs(filepath.Join("testdata", "echobot.sh"))
	if err != nil {
		t.Fatalf("resolve echobot.sh: %v", err)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read echobot.sh: %v", err)
	}
	scriptPath := filepath.Join(c.Home, "echobot.sh")
	if err := os.WriteFile(scriptPath, data, 0o755); err != nil {
		t.Fatalf("write echobot.sh: %v", err)
	}

	cfgDir := filepath.Join(c.Home, ".agent-deck")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir .agent-deck: %v", err)
	}
	toolA, toolB := "echobot", "parrot"
	cfg := fmt.Sprintf(`[tools.%s]
command = %q
icon = "E"
prompt_patterns = ["ECHOBOT READY"]
busy_patterns = ["WORKING"]

[tools.%s]
command = %q
icon = "P"
prompt_patterns = ["ECHOBOT READY"]
busy_patterns = ["WORKING"]
`, toolA, scriptPath, toolB, scriptPath)
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
	return toolA, toolB
}

// TestCapability_MultiTool_Readiness proves agent-deck brings more than one
// tool kind to readiness and round-trips a message through each. Two distinct
// custom tools are launched; each must reach an active status AND echo back the
// unique token it was sent. Real-agent tools (claude/codex/gemini) need creds
// and a network and stay Tier N; this proves the launch+readiness machinery is
// tool-agnostic using deterministic stand-ins.
//
// Surfaces: CLI (launch -m) + Remote (LocalSession readiness + send) +
// Persistence (per-tool registry rows).
func TestCapability_MultiTool_Readiness(t *testing.T) {
	c := newCapSandbox(t)
	toolA, toolB := setupTwoStubTools(t, c)

	type probe struct {
		tool  string
		title string
		token string
		pane  string
	}
	probes := []probe{
		{tool: toolA, title: "cap-tool-a", token: "PINGA-cap-e2e"},
		{tool: toolB, title: "cap-tool-b", token: "PINGB-cap-e2e"},
	}

	for i := range probes {
		p := &probes[i]
		c.run(t, "launch", c.WorkDir, "-c", p.tool, "-t", p.title, "-m", p.token)
		defer c.stopQuietly(p.title)

		row, ok := c.findByTitle(t, p.title)
		if !ok {
			t.Fatalf("launch of tool %q did not create row %q.\nrows: %+v", p.tool, p.title, c.list(t))
		}
		if row.Tool != p.tool {
			t.Errorf("row tool = %q, want %q", row.Tool, p.tool)
		}
		if got, ok := c.waitForStatus(t, p.title, 10*time.Second, "running", "starting", "idle", "waiting"); !ok {
			t.Fatalf("tool %q never reached an active status; last = %q", p.tool, got)
		}

		want := "ECHO:" + p.token
		pane, ok := c.waitForPaneContains(t, p.title, want, 20*time.Second)
		if !ok {
			t.Fatalf("tool %q never echoed %q (launch+readiness+send broken for this tool).\nlast pane:\n%s", p.tool, want, pane)
		}
		p.pane = echoExchange(pane, p.token)
	}

	// Display proof: both tools, each distilled to the token sent and the reply
	// echoed back, proving two independent tool kinds reached readiness.
	var b strings.Builder
	for _, p := range probes {
		fmt.Fprintf(&b, "tool %q (%s):\n%s\n\n", p.tool, p.title, p.pane)
	}
	snapshot(t, "multitool", strings.TrimSpace(b.String()))
}
