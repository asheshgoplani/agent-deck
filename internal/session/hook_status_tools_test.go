package session

import "testing"

// TestHookStatusTool_MatchesInstallerSet locks HookStatusTool to the exact set
// of tools that ship a hook installer (`agent-deck hooks`, `codex-hooks`,
// `gemini-hooks`, `hermes-hooks`, `cursor-hooks`, `pi-hooks` in
// cmd/agent-deck). #2222 was a hand-written copy of this set that silently
// dropped a newly-installed tool (pi); this test exists so the same class of
// miss fails CI instead of shipping.
func TestHookStatusTool_MatchesInstallerSet(t *testing.T) {
	installerBacked := []string{
		"claude", // cmd/agent-deck: `hooks` command
		"codex",  // cmd/agent-deck/codex_hooks_cmd.go
		"gemini", // cmd/agent-deck/gemini_hooks_cmd.go
		"hermes", // cmd/agent-deck/hermes_hooks_cmd.go
		"cursor", // cmd/agent-deck/cursor_hooks_cmd.go
		"pi",     // cmd/agent-deck/pi_hooks_cmd.go
	}
	noInstaller := []string{"opencode", "crush", "muse", "copilot", "deepseek", "shell", "", "unknown-tool"}

	for _, tool := range installerBacked {
		if !HookStatusTool(tool) {
			t.Errorf("HookStatusTool(%q) = false, want true (tool has a hook installer)", tool)
		}
	}
	for _, tool := range noInstaller {
		if HookStatusTool(tool) {
			t.Errorf("HookStatusTool(%q) = true, want false (tool has no hook installer)", tool)
		}
	}
}
