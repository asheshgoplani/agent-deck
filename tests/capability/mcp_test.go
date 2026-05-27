//go:build capability_e2e

package capability

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupStubMCP registers a trivial stdio MCP under [mcps.<name>] in the
// sandbox config.toml so `mcp attach` resolves it. The MCP is never started;
// the capability under test is the registry write to .mcp.json, not the
// server actually running. It appends to whatever config.toml already exists
// (so it composes with setupEchobot's [tools.echobot] block).
func setupStubMCP(t *testing.T, c *capSandbox, name string) {
	t.Helper()
	cfgDir := filepath.Join(c.Home, ".agent-deck")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir .agent-deck: %v", err)
	}
	cfgPath := filepath.Join(cfgDir, "config.toml")
	existing, _ := os.ReadFile(cfgPath) // ignore missing
	block := fmt.Sprintf(`
[mcps.%s]
command = "true"
args = ["--noop"]
description = "deterministic stub MCP for capability tests"
`, name)
	if err := os.WriteFile(cfgPath, append(existing, []byte(block)...), 0o644); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
}

// mcpJSONLocalNames reads the project's .mcp.json directly and returns the
// server names it declares. Reading the file (rather than trusting CLI text)
// is the real-effect assertion: a human inspecting .mcp.json sees exactly this.
func mcpJSONLocalNames(t *testing.T, projectPath string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(projectPath, ".mcp.json"))
	if err != nil {
		return nil
	}
	var doc struct {
		McpServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse .mcp.json: %v\nraw: %s", err, raw)
	}
	names := make([]string, 0, len(doc.McpServers))
	for n := range doc.McpServers {
		names = append(names, n)
	}
	return names
}

func contains(haystack []string, want string) bool {
	for _, h := range haystack {
		if h == want {
			return true
		}
	}
	return false
}

// TestCapability_MCP_AttachDetach proves `mcp attach` writes a real .mcp.json
// entry into the session's project dir, and `mcp detach` removes it. The
// assertion reads the .mcp.json bytes on disk; the stub MCP is never executed.
//
// Surfaces: CLI (mcp attach/detach) + Persistence (.mcp.json). The MCP "loads
// in a live agent" path needs a real agent to introspect its tools and is the
// documented Tier N gap (capability id mcp-loads). See capability-gaps.md.
func TestCapability_MCP_AttachDetach(t *testing.T) {
	c := newCapSandbox(t)
	setupStubMCP(t, c, "stubmcp")

	c.run(t, "add", "-c", "bash", "-t", "cap-mcp", c.WorkDir)
	row, ok := c.findByTitle(t, "cap-mcp")
	if !ok {
		t.Fatalf("add did not create the session row.\nrows: %+v", c.list(t))
	}

	// Failure mode: attaching an MCP that is not in config.toml is refused and
	// writes no .mcp.json, so a typo can't silently create a broken config.
	if out, err := c.try("mcp", "attach", "cap-mcp", "does-not-exist"); err == nil {
		t.Fatalf("attaching an unknown MCP should be refused, got success:\n%s", out)
	}
	if names := mcpJSONLocalNames(t, row.Path); len(names) != 0 {
		t.Fatalf("a refused attach must not write .mcp.json, found: %v", names)
	}

	// Happy path: attach the stub locally; .mcp.json must now declare it.
	c.run(t, "mcp", "attach", "cap-mcp", "stubmcp")
	if names := mcpJSONLocalNames(t, row.Path); !contains(names, "stubmcp") {
		t.Fatalf("after attach, .mcp.json should declare stubmcp, found: %v", names)
	}

	// Boundary: re-attaching the same MCP is refused as already-attached and
	// leaves exactly one entry (no duplicate server stanza).
	if out, err := c.try("mcp", "attach", "cap-mcp", "stubmcp"); err == nil {
		t.Fatalf("re-attaching an already-attached MCP should be refused, got:\n%s", out)
	}
	if names := mcpJSONLocalNames(t, row.Path); len(names) != 1 {
		t.Fatalf("re-attach must not duplicate the entry, found: %v", names)
	}

	// Detach removes it; .mcp.json no longer declares stubmcp.
	c.run(t, "mcp", "detach", "cap-mcp", "stubmcp")
	if names := mcpJSONLocalNames(t, row.Path); contains(names, "stubmcp") {
		t.Fatalf("after detach, .mcp.json should not declare stubmcp, found: %v", names)
	}

	// Display proof: the attached view a human checks (mcp attached -q lists
	// the live local attachment) plus the raw .mcp.json the assertion read,
	// captured at the attached point by re-attaching for the snapshot.
	c.run(t, "mcp", "attach", "cap-mcp", "stubmcp")
	attached := c.run(t, "mcp", "attached", "cap-mcp")
	rawJSON, _ := os.ReadFile(filepath.Join(row.Path, ".mcp.json"))
	snapshot(t, "mcp-attach", "$ agent-deck mcp attached cap-mcp\n"+
		strings.TrimRight(attached, "\n")+
		"\n\n.mcp.json on disk:\n"+strings.TrimSpace(string(rawJSON)))
}
