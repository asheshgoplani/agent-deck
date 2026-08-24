package session

// PR #2064 round-2 P1 guards: the primer rides SessionStart additionalContext,
// which Claude Code only reads from SYNCHRONOUS hooks. These tests pin (a) the
// table entry, (b) the async→sync upgrade of an existing install, and (c) the
// spawn-time ensure path that covers CLI-only installs.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSessionStartHookIsSynchronous pins Async:false for SessionStart. If
// this flips back to async, Claude Code silently discards the hook's stdout
// and the v1.16.0 context primer stops reaching every default install —
// while all fixture-level primer tests keep passing. Do not "optimize" this
// back to async without moving primer delivery to another channel first.
func TestSessionStartHookIsSynchronous(t *testing.T) {
	for _, cfg := range hookEventConfigs {
		if cfg.Event == "SessionStart" {
			if cfg.Async {
				t.Fatalf("SessionStart must be synchronous: async hooks' stdout (additionalContext) is ignored by Claude Code, which no-ops the context primer on default installs (PR #2064 P1)")
			}
			return
		}
	}
	t.Fatalf("SessionStart missing from hookEventConfigs")
}

// TestInjectClaudeHooks_UpgradesAsyncSessionStart: a settings.json written by
// a pre-1.16 binary (SessionStart async:true) must be flipped to sync in
// place by the next InjectClaudeHooks run — the exact upgrade path default
// installs take.
func TestInjectClaudeHooks_UpgradesAsyncSessionStart(t *testing.T) {
	dir := t.TempDir()
	stale := `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"agent-deck hook-handler","async":true}]}]}}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(stale), 0644); err != nil {
		t.Fatal(err)
	}

	installed, err := InjectClaudeHooks(dir)
	if err != nil {
		t.Fatalf("InjectClaudeHooks: %v", err)
	}
	if !installed {
		t.Fatalf("stale async SessionStart must NOT count as already-installed")
	}

	data, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Hooks map[string][]claudeHookMatcher `json:"hooks"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	for _, m := range settings.Hooks["SessionStart"] {
		for _, h := range m.Hooks {
			if strings.Contains(h.Command, agentDeckHookCommand) && h.Async {
				t.Fatalf("SessionStart entry still async after upgrade: %s", data)
			}
		}
	}
}

// TestEnsureClaudeHooksForSpawn_InstallsIntoFreshConfigDir: CLI-only installs
// never run the TUI's InjectClaudeHooks; the spawn path must install the
// hooks itself or the primer never injects there.
func TestEnsureClaudeHooksForSpawn_InstallsIntoFreshConfigDir(t *testing.T) {
	origConfigDir := os.Getenv("CLAUDE_CONFIG_DIR")
	origHome := os.Getenv("HOME")
	home := t.TempDir()
	os.Setenv("HOME", home)
	os.Unsetenv("CLAUDE_CONFIG_DIR")
	ClearUserConfigCache()
	t.Cleanup(func() {
		if origConfigDir != "" {
			os.Setenv("CLAUDE_CONFIG_DIR", origConfigDir)
		} else {
			os.Unsetenv("CLAUDE_CONFIG_DIR")
		}
		os.Setenv("HOME", origHome)
		ClearUserConfigCache()
	})

	inst := &Instance{ID: "s1", Title: "w", GroupPath: "g", Tool: "claude", ProjectPath: t.TempDir()}
	inst.ensureClaudeHooksForSpawn()

	settingsPath := filepath.Join(home, ".claude", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("spawn ensure did not create %s: %v", settingsPath, err)
	}
	if !strings.Contains(string(data), agentDeckHookCommand) {
		t.Fatalf("settings.json missing agent-deck hooks: %s", data)
	}
	var settings struct {
		Hooks map[string][]claudeHookMatcher `json:"hooks"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range settings.Hooks["SessionStart"] {
		for _, h := range m.Hooks {
			if strings.Contains(h.Command, agentDeckHookCommand) {
				found = true
				if h.Async {
					t.Fatalf("spawn-installed SessionStart hook must be sync")
				}
			}
		}
	}
	if !found {
		t.Fatalf("SessionStart hook missing after spawn ensure: %s", data)
	}
}
