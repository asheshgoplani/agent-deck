package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestInjectClaudeHooks_Fresh(t *testing.T) {
	tmpDir := t.TempDir()

	installed, err := InjectClaudeHooks(tmpDir)
	if err != nil {
		t.Fatalf("InjectClaudeHooks failed: %v", err)
	}
	if !installed {
		t.Error("Expected hooks to be newly installed")
	}

	// Read settings.json and verify hooks are present
	data, err := os.ReadFile(filepath.Join(tmpDir, "settings.json"))
	if err != nil {
		t.Fatalf("Failed to read settings.json: %v", err)
	}

	var settings map[string]json.RawMessage
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("Failed to parse settings.json: %v", err)
	}

	hooksRaw, ok := settings["hooks"]
	if !ok {
		t.Fatal("settings.json missing 'hooks' key")
	}

	var hooks map[string]json.RawMessage
	if err := json.Unmarshal(hooksRaw, &hooks); err != nil {
		t.Fatalf("Failed to parse hooks: %v", err)
	}

	// Verify all expected events are present
	expectedEvents := []string{"SessionStart", "UserPromptSubmit", "Stop", "PermissionRequest", "Notification", "SessionEnd", "PreCompact"}
	for _, event := range expectedEvents {
		if _, ok := hooks[event]; !ok {
			t.Errorf("Missing hook event: %s", event)
		}
	}

	// Verify the hook command is correct
	var matchers []claudeHookMatcher
	if err := json.Unmarshal(hooks["SessionStart"], &matchers); err != nil {
		t.Fatalf("Failed to parse SessionStart matchers: %v", err)
	}
	if len(matchers) == 0 {
		t.Fatal("SessionStart has no matchers")
	}
	if len(matchers[0].Hooks) == 0 {
		t.Fatal("SessionStart matcher has no hooks")
	}
	if matchers[0].Hooks[0].Command != agentDeckHookCommand {
		t.Errorf("Hook command = %q, want %q", matchers[0].Hooks[0].Command, agentDeckHookCommand)
	}
	if !matchers[0].Hooks[0].Async {
		t.Error("Hook should be async")
	}
}

func TestPreCompactHookIsSynchronous(t *testing.T) {
	tmpDir := t.TempDir()

	if _, err := InjectClaudeHooks(tmpDir); err != nil {
		t.Fatalf("InjectClaudeHooks failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(tmpDir, "settings.json"))
	if err != nil {
		t.Fatalf("Failed to read settings.json: %v", err)
	}
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("Failed to parse settings: %v", err)
	}

	var hooks map[string]json.RawMessage
	if err := json.Unmarshal(settings["hooks"], &hooks); err != nil {
		t.Fatalf("Failed to parse hooks: %v", err)
	}

	var matchers []claudeHookMatcher
	if err := json.Unmarshal(hooks["PreCompact"], &matchers); err != nil {
		t.Fatalf("Failed to parse PreCompact matchers: %v", err)
	}

	if len(matchers) == 0 || len(matchers[0].Hooks) == 0 {
		t.Fatal("PreCompact has no hooks")
	}

	hook := matchers[0].Hooks[0]
	if hook.Async {
		t.Error("PreCompact hook must be synchronous (Async should be false)")
	}
	if hook.Command != agentDeckHookCommand {
		t.Errorf("PreCompact hook command = %q, want %q", hook.Command, agentDeckHookCommand)
	}
}

func TestInjectClaudeHooks_PreservesExisting(t *testing.T) {
	tmpDir := t.TempDir()

	// Write existing settings with a custom setting and user hook
	existing := map[string]json.RawMessage{
		"apiKey": json.RawMessage(`"sk-test-123"`),
		"hooks": json.RawMessage(`{
			"SessionStart": [{"hooks": [{"type": "command", "command": "my-custom-hook"}]}]
		}`),
	}
	data, _ := json.MarshalIndent(existing, "", "  ")
	if err := os.WriteFile(filepath.Join(tmpDir, "settings.json"), data, 0644); err != nil {
		t.Fatalf("Failed to write settings.json: %v", err)
	}

	installed, err := InjectClaudeHooks(tmpDir)
	if err != nil {
		t.Fatalf("InjectClaudeHooks failed: %v", err)
	}
	if !installed {
		t.Error("Expected hooks to be installed")
	}

	// Verify existing setting is preserved
	readData, err := os.ReadFile(filepath.Join(tmpDir, "settings.json"))
	if err != nil {
		t.Fatalf("Failed to read settings.json: %v", err)
	}
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(readData, &settings); err != nil {
		t.Fatalf("Failed to parse settings: %v", err)
	}

	if string(settings["apiKey"]) != `"sk-test-123"` {
		t.Errorf("apiKey was not preserved: %s", settings["apiKey"])
	}

	// Verify user hook is preserved alongside agent-deck hook
	var hooks map[string]json.RawMessage
	if err := json.Unmarshal(settings["hooks"], &hooks); err != nil {
		t.Fatalf("Failed to parse hooks: %v", err)
	}

	var matchers []claudeHookMatcher
	if err := json.Unmarshal(hooks["SessionStart"], &matchers); err != nil {
		t.Fatalf("Failed to parse SessionStart matchers: %v", err)
	}

	// Should have the original matcher with user hook, plus agent-deck's hook appended
	foundCustom := false
	foundAgentDeck := false
	for _, m := range matchers {
		for _, h := range m.Hooks {
			if h.Command == "my-custom-hook" {
				foundCustom = true
			}
			if h.Command == agentDeckHookCommand {
				foundAgentDeck = true
			}
		}
	}

	if !foundCustom {
		t.Error("User's custom hook was not preserved")
	}
	if !foundAgentDeck {
		t.Error("Agent-deck hook was not added")
	}
}

func TestInjectClaudeHooks_Idempotent(t *testing.T) {
	tmpDir := t.TempDir()

	// First install
	installed1, err := InjectClaudeHooks(tmpDir)
	if err != nil {
		t.Fatalf("First install failed: %v", err)
	}
	if !installed1 {
		t.Error("First install should return true")
	}

	// Second install should be a no-op
	installed2, err := InjectClaudeHooks(tmpDir)
	if err != nil {
		t.Fatalf("Second install failed: %v", err)
	}
	if installed2 {
		t.Error("Second install should return false (already installed)")
	}

	// Verify no duplicate hooks
	data, err := os.ReadFile(filepath.Join(tmpDir, "settings.json"))
	if err != nil {
		t.Fatalf("Failed to read settings.json: %v", err)
	}
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("Failed to parse settings: %v", err)
	}

	var hooks map[string]json.RawMessage
	if err := json.Unmarshal(settings["hooks"], &hooks); err != nil {
		t.Fatalf("Failed to parse hooks: %v", err)
	}

	var matchers []claudeHookMatcher
	if err := json.Unmarshal(hooks["SessionStart"], &matchers); err != nil {
		t.Fatalf("Failed to parse SessionStart matchers: %v", err)
	}

	hookCount := 0
	for _, m := range matchers {
		for _, h := range m.Hooks {
			if h.Command == agentDeckHookCommand {
				hookCount++
			}
		}
	}
	if hookCount != 1 {
		t.Errorf("Expected 1 agent-deck hook, got %d (duplication bug)", hookCount)
	}
}

func TestRemoveClaudeHooks(t *testing.T) {
	tmpDir := t.TempDir()

	// Install first
	if _, err := InjectClaudeHooks(tmpDir); err != nil {
		t.Fatalf("InjectClaudeHooks failed: %v", err)
	}

	// Remove
	removed, err := RemoveClaudeHooks(tmpDir)
	if err != nil {
		t.Fatalf("RemoveClaudeHooks failed: %v", err)
	}
	if !removed {
		t.Error("Expected hooks to be removed")
	}

	// Verify hooks are gone
	if CheckClaudeHooksInstalled(tmpDir) {
		t.Error("Hooks should not be installed after removal")
	}
}

func TestRemoveClaudeHooks_PreservesUserHooks(t *testing.T) {
	tmpDir := t.TempDir()

	// Write settings with both user and agent-deck hooks
	existing := map[string]json.RawMessage{
		"hooks": json.RawMessage(`{
			"SessionStart": [
				{"hooks": [{"type": "command", "command": "my-custom-hook"}, {"type": "command", "command": "agent-deck hook-handler", "async": true}]}
			]
		}`),
	}
	data, _ := json.MarshalIndent(existing, "", "  ")
	if err := os.WriteFile(filepath.Join(tmpDir, "settings.json"), data, 0644); err != nil {
		t.Fatalf("Failed to write settings.json: %v", err)
	}

	// Remove agent-deck hooks
	removed, err := RemoveClaudeHooks(tmpDir)
	if err != nil {
		t.Fatalf("RemoveClaudeHooks failed: %v", err)
	}
	if !removed {
		t.Error("Expected hooks to be removed")
	}

	// Verify user hook is preserved
	readData, err := os.ReadFile(filepath.Join(tmpDir, "settings.json"))
	if err != nil {
		t.Fatalf("Failed to read settings.json: %v", err)
	}
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(readData, &settings); err != nil {
		t.Fatalf("Failed to parse settings: %v", err)
	}

	var hooks map[string]json.RawMessage
	if err := json.Unmarshal(settings["hooks"], &hooks); err != nil {
		t.Fatalf("Failed to parse hooks: %v", err)
	}

	var matchers []claudeHookMatcher
	if err := json.Unmarshal(hooks["SessionStart"], &matchers); err != nil {
		t.Fatalf("Failed to parse SessionStart matchers: %v", err)
	}

	foundCustom := false
	foundAgentDeck := false
	for _, m := range matchers {
		for _, h := range m.Hooks {
			if h.Command == "my-custom-hook" {
				foundCustom = true
			}
			if h.Command == agentDeckHookCommand {
				foundAgentDeck = true
			}
		}
	}

	if !foundCustom {
		t.Error("User hook should be preserved")
	}
	if foundAgentDeck {
		t.Error("Agent-deck hook should be removed")
	}
}

func TestCheckClaudeHooksInstalled(t *testing.T) {
	tmpDir := t.TempDir()

	// Not installed yet
	if CheckClaudeHooksInstalled(tmpDir) {
		t.Error("Hooks should not be installed initially")
	}

	// Install
	if _, err := InjectClaudeHooks(tmpDir); err != nil {
		t.Fatalf("InjectClaudeHooks failed: %v", err)
	}

	// Should be installed
	if !CheckClaudeHooksInstalled(tmpDir) {
		t.Error("Hooks should be installed after InjectClaudeHooks")
	}

	// Remove
	if _, err := RemoveClaudeHooks(tmpDir); err != nil {
		t.Fatalf("RemoveClaudeHooks failed: %v", err)
	}

	// Should not be installed
	if CheckClaudeHooksInstalled(tmpDir) {
		t.Error("Hooks should not be installed after RemoveClaudeHooks")
	}
}

func TestNotificationMatcher(t *testing.T) {
	tmpDir := t.TempDir()

	if _, err := InjectClaudeHooks(tmpDir); err != nil {
		t.Fatalf("InjectClaudeHooks failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(tmpDir, "settings.json"))
	if err != nil {
		t.Fatalf("Failed to read settings.json: %v", err)
	}
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("Failed to parse settings: %v", err)
	}

	var hooks map[string]json.RawMessage
	if err := json.Unmarshal(settings["hooks"], &hooks); err != nil {
		t.Fatalf("Failed to parse hooks: %v", err)
	}

	// Notification event should have a matcher pattern
	var matchers []claudeHookMatcher
	if err := json.Unmarshal(hooks["Notification"], &matchers); err != nil {
		t.Fatalf("Failed to parse Notification matchers: %v", err)
	}

	if len(matchers) == 0 {
		t.Fatal("Notification has no matchers")
	}
	if matchers[0].Matcher != "permission_prompt|elicitation_dialog" {
		t.Errorf("Notification matcher = %q, want %q", matchers[0].Matcher, "permission_prompt|elicitation_dialog")
	}
}
