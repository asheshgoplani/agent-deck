package web

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Store-coherence tests for the production MCP manager.
//
// These exercise defaultMCPManager against real files rather than a fake,
// because the defects they pin are entirely about WHICH file gets read and
// written:
//
//  1. "local" used to read the Claude config's projects[path].mcpServers map
//     while writing <project>/.mcp.json. Attaching one server therefore
//     rewrote .mcp.json from a list that never contained the servers already
//     in it, silently dropping them.
//  2. Every scope hardcoded the Claude helpers, so selecting a Codex, Gemini,
//     Cursor or OpenCode session mutated Claude's configuration.

// mcpStoreEnv points the whole session config layer at a temp HOME and writes
// a config.toml catalog, so GetAvailableMCPs (and therefore filterDefined)
// recognises the names under test.
func mcpStoreEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude-cfg"))
	if err := os.MkdirAll(filepath.Join(home, ".claude-cfg"), 0o755); err != nil {
		t.Fatalf("mkdir claude config dir: %v", err)
	}

	configDir := filepath.Join(home, ".config", "agent-deck")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	const catalog = `
[mcps.alpha]
command = "alpha-server"
description = "alpha"

[mcps.beta]
command = "beta-server"
description = "beta"

[mcps.gamma]
command = "gamma-server"
description = "gamma"
`
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(catalog), 0o600); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)

	if len(session.GetAvailableMCPs()) == 0 {
		t.Skip("catalog did not load from the temp config; skipping rather than asserting vacuously")
	}
	return home
}

func readMCPJSONNames(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var doc struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	names := make([]string, 0, len(doc.MCPServers))
	for name := range doc.MCPServers {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// TestLocalAttachPreservesServersAlreadyInMcpJSON is the regression test for
// the read/write store mismatch. `beta` exists only in .mcp.json; attaching
// `alpha` must not remove it.
func TestLocalAttachPreservesServersAlreadyInMcpJSON(t *testing.T) {
	mcpStoreEnv(t)
	project := t.TempDir()
	target := MCPTarget{Tool: "claude", ProjectPath: project}
	mgr := NewDefaultMCPManager()

	// Seed .mcp.json with beta through the same writer production uses, so the
	// file shape is exactly what the app would have produced.
	if err := session.WriteMCPJsonFromConfig(project, []string{"beta"}); err != nil {
		t.Fatalf("seed .mcp.json: %v", err)
	}
	mcpJSON := filepath.Join(project, ".mcp.json")
	if got := readMCPJSONNames(t, mcpJSON); !slices.Contains(got, "beta") {
		t.Fatalf("precondition failed: .mcp.json should contain beta, got %v", got)
	}

	// The local list must reflect .mcp.json, the file the local writer targets.
	attached, err := mgr.ListAttached(target)
	if err != nil {
		t.Fatalf("ListAttached: %v", err)
	}
	if !slices.Contains(attached["local"], "beta") {
		t.Fatalf("local scope should read .mcp.json and report beta, got %v", attached["local"])
	}

	if err := mgr.Attach(target, "alpha", "local"); err != nil {
		t.Fatalf("Attach alpha local: %v", err)
	}

	got := readMCPJSONNames(t, mcpJSON)
	if !slices.Contains(got, "alpha") {
		t.Errorf("attach did not add alpha to .mcp.json: %v", got)
	}
	if !slices.Contains(got, "beta") {
		t.Errorf("attaching alpha DROPPED beta from .mcp.json: %v — "+
			"the local read and the local write must target the same store", got)
	}
}

// TestLocalDetachPreservesOtherServers is the mirror case: removing one server
// must not take the rest with it.
func TestLocalDetachPreservesOtherServers(t *testing.T) {
	mcpStoreEnv(t)
	project := t.TempDir()
	target := MCPTarget{Tool: "claude", ProjectPath: project}
	mgr := NewDefaultMCPManager()

	if err := session.WriteMCPJsonFromConfig(project, []string{"alpha", "beta"}); err != nil {
		t.Fatalf("seed .mcp.json: %v", err)
	}
	if err := mgr.Detach(target, "alpha", "local"); err != nil {
		t.Fatalf("Detach: %v", err)
	}
	got := readMCPJSONNames(t, filepath.Join(project, ".mcp.json"))
	if slices.Contains(got, "alpha") {
		t.Errorf("detach left alpha behind: %v", got)
	}
	if !slices.Contains(got, "beta") {
		t.Errorf("detaching alpha DROPPED beta: %v", got)
	}
}

// TestNonClaudeToolDoesNotTouchClaudeConfig is the regression test for the
// hardcoded Claude helpers. A Codex session's attach must leave Claude's
// config byte-identical.
func TestNonClaudeToolDoesNotTouchClaudeConfig(t *testing.T) {
	home := mcpStoreEnv(t)
	project := t.TempDir()
	mgr := NewDefaultMCPManager()

	claudeConfig := filepath.Join(home, ".claude-cfg", ".claude.json")
	seed := []byte(`{"mcpServers":{"alpha":{"command":"alpha-server"}}}`)
	if err := os.WriteFile(claudeConfig, seed, 0o600); err != nil {
		t.Fatalf("seed claude config: %v", err)
	}

	// Codex is supported by the MCP manager but keeps its servers in its own
	// config, so this must not write Claude's file.
	target := MCPTarget{Tool: "codex", ProjectPath: project}
	if err := mgr.Attach(target, "beta", "global"); err != nil {
		t.Fatalf("attach beta in codex global scope: %v", err)
	}

	// Confirm the write actually landed in Codex's own store. Without this the
	// two assertions below would hold trivially for a no-op attach, and the
	// test would pass for the wrong reason.
	attached, err := mgr.ListAttached(target)
	if err != nil {
		t.Fatalf("ListAttached: %v", err)
	}
	if !slices.Contains(attached["global"], "beta") {
		t.Fatalf("codex attach did not reach the codex store; global=%v", attached["global"])
	}

	after, readErr := os.ReadFile(claudeConfig)
	if readErr != nil {
		t.Fatalf("read claude config: %v", readErr)
	}
	if string(after) != string(seed) {
		t.Errorf("a codex session's MCP attach modified Claude's config.\nbefore: %s\nafter:  %s", seed, after)
	}

	// It also must not write the project's Claude-style .mcp.json.
	if _, err := os.Stat(filepath.Join(project, ".mcp.json")); err == nil {
		t.Error("a codex session's MCP attach created a Claude-style .mcp.json in the project")
	}
}

// TestScopesAreRefusedWhenTheToolDoesNotHaveThem pins the honest-refusal
// behaviour: rather than redirecting an impossible scope to Claude's store,
// the manager says the scope does not exist for that tool.
func TestScopesAreRefusedWhenTheToolDoesNotHaveThem(t *testing.T) {
	mcpStoreEnv(t)
	project := t.TempDir()
	mgr := NewDefaultMCPManager()

	for _, tc := range []struct {
		tool  string
		scope string
	}{
		{"codex", "local"},  // Codex has no project-local MCP file
		{"codex", "user"},   // ~/.claude.json is Claude's alone
		{"gemini", "local"}, // Gemini is global-only
		{"cursor", "user"},
		{"opencode", "user"},
	} {
		t.Run(tc.tool+"/"+tc.scope, func(t *testing.T) {
			err := mgr.Attach(MCPTarget{Tool: tc.tool, ProjectPath: project}, "alpha", tc.scope)
			if err == nil {
				t.Fatalf("attaching in scope %q for tool %q should be refused, not silently redirected", tc.scope, tc.tool)
			}
		})
	}
}

// TestClaudeProjectEntriesAreTheirOwnScope replaces an earlier test that
// asserted projects[path].mcpServers belonged to the GLOBAL bucket, mirroring
// how the TUI dialog merges the two for display.
//
// That grouping was wrong for a read/write API: the global write path only
// touches the root mcpServers map, so a "global" detach of a project entry
// reported success and left it attached. The scopes are modelled separately
// now, and each is written where it is reported. See
// TestClaudeProjectScopeIsWrittenWhereItIsReported for the write half.
func TestClaudeProjectEntriesAreTheirOwnScope(t *testing.T) {
	home := mcpStoreEnv(t)
	project := t.TempDir()

	cfg := map[string]any{
		"projects": map[string]any{
			project: map[string]any{
				"mcpServers": map[string]any{"gamma": map[string]any{"command": "gamma-server"}},
			},
		},
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude-cfg", ".claude.json"), data, 0o600); err != nil {
		t.Fatalf("write claude config: %v", err)
	}

	attached, err := NewDefaultMCPManager().ListAttached(MCPTarget{Tool: "claude", ProjectPath: project})
	if err != nil {
		t.Fatalf("ListAttached: %v", err)
	}
	if !slices.Contains(attached["project"], "gamma") {
		t.Errorf("projects[path] entries belong to the project scope, got project=%v", attached["project"])
	}
	if slices.Contains(attached["global"], "gamma") {
		t.Errorf("project entries must not be folded into global (the global write path cannot "+
			"remove them), got global=%v", attached["global"])
	}
	if slices.Contains(attached["local"], "gamma") {
		t.Errorf("project entries must not be reported as local (.mcp.json), got %v", attached["local"])
	}
}
