package session_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

type kimiHookEntry struct {
	Event   string `toml:"event"`
	Command string `toml:"command"`
}

func readKimiHookEntries(t *testing.T, configDir string) []kimiHookEntry {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(configDir, "config.toml"))
	if err != nil {
		t.Fatalf("read Kimi config: %v", err)
	}
	var cfg struct {
		Hooks []kimiHookEntry `toml:"hooks"`
	}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse Kimi config: %v", err)
	}
	return cfg.Hooks
}

func TestInjectKimiHooksFreshInstall(t *testing.T) {
	dir := t.TempDir()
	installed, err := session.InjectKimiHooks(dir)
	if err != nil {
		t.Fatalf("InjectKimiHooks: %v", err)
	}
	if !installed {
		t.Fatal("fresh install reported no change")
	}

	entries := readKimiHookEntries(t, dir)
	wantEvents := []string{
		"SessionStart", "TurnStarted", "UserPromptSubmit", "PreToolUse",
		"PostToolUse", "PostToolUseFailure", "Stop", "Interrupt",
		"PermissionRequest", "StopFailure", "SessionEnd",
	}
	if len(entries) != len(wantEvents) {
		t.Fatalf("installed %d hooks, want %d: %+v", len(entries), len(wantEvents), entries)
	}
	for i, want := range wantEvents {
		wantCommand := "agent-deck hook-handler"
		if want == "PreToolUse" {
			wantCommand = "AGENTDECK_PRETOOL_SYNC=1 agent-deck hook-handler"
		}
		if entries[i].Event != want || entries[i].Command != wantCommand {
			t.Errorf("hook %d = %+v, want event %q with command %q", i, entries[i], want, wantCommand)
		}
	}
	if !session.CheckKimiHooksInstalled(dir) {
		t.Fatal("installed hooks are not reported as installed")
	}
}
