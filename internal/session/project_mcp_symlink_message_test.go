package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The refusal is the user's only clue when a deliberate link (dotfiles,
// monorepo) stops being written: it must name the link inside the project, the
// file it points at, and the setting that turns the writer off.
func TestWriteMCPJsonFromConfig_RefusalNamesLinkTargetAndOptOut(t *testing.T) {
	proj, outside := symlinkProjectFixture(t, ".mcp.json")
	err := WriteMCPJsonFromConfig(proj, []string{"cat"})
	if err == nil {
		t.Fatal("expected a refusal")
	}
	msg := err.Error()
	link := filepath.Join(proj, ".mcp.json")
	for _, want := range []string{link, outside, "manage_mcp_json = false"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error does not mention %q: %s", want, msg)
		}
	}
	if strings.Count(msg, outside) != 1 {
		t.Errorf("error names the target %d times, want once: %s", strings.Count(msg, outside), msg)
	}
}

// RefreshStalePluginPins used to write "<file>.tmp" with os.WriteFile, which
// followed a symlink planted at that name and overwrote its target before the
// rename. A repo shipping ".mcp.json.tmp -> ~/somefile" must not get it written.
func TestRefreshStalePluginPins_IgnoresPlantedTmpSymlink(t *testing.T) {
	tmp := t.TempDir()
	profileDir := filepath.Join(tmp, "profile")
	proj := filepath.Join(tmp, "proj")
	pluginRoot := filepath.Join(profileDir, "plugins", "cache", "mkt", "plug")
	v2 := filepath.Join(pluginRoot, "2.0.0")
	for _, d := range []string{proj, v2} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	stale := filepath.Join(pluginRoot, "1.0.0", "server.js") // 1.0.0 no longer installed
	data, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{
		"plug": map[string]any{"command": "node", "args": []string{stale}},
	}})
	mcpFile := filepath.Join(proj, ".mcp.json")
	if err := os.WriteFile(mcpFile, data, 0o644); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(tmp, "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, mcpFile+".tmp"); err != nil {
		t.Fatal(err)
	}

	n, err := RefreshStalePluginPins(mcpFile, []string{profileDir})
	if err != nil || n != 1 {
		t.Fatalf("RefreshStalePluginPins = %d, %v; want 1, nil", n, err)
	}
	if got, _ := os.ReadFile(victim); string(got) != "keep" {
		t.Fatalf("planted .tmp link target overwritten: %q", got)
	}
	if got, _ := os.ReadFile(mcpFile); !strings.Contains(string(got), filepath.Join(v2, "server.js")) {
		t.Fatalf(".mcp.json not refreshed: %s", got)
	}
}
