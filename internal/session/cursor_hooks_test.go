package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInjectCursorHooks_Fresh(t *testing.T) {
	tmpDir := t.TempDir()

	installed, err := InjectCursorHooks(tmpDir)
	if err != nil {
		t.Fatalf("InjectCursorHooks failed: %v", err)
	}
	if !installed {
		t.Fatal("expected hooks to be newly installed")
	}

	data, err := os.ReadFile(filepath.Join(tmpDir, "hooks.json"))
	if err != nil {
		t.Fatalf("read hooks.json: %v", err)
	}
	var cfg cursorHooksConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse hooks.json: %v", err)
	}
	if cfg.Version != 1 {
		t.Fatalf("version = %d, want 1", cfg.Version)
	}
	for _, event := range cursorHookEventNames {
		if !cursorEventHasAgentDeckHook(cfg.Hooks[event]) {
			t.Fatalf("event %s missing agent-deck hook", event)
		}
	}
}

func TestInjectCursorHooks_PreservesExistingHooks(t *testing.T) {
	tmpDir := t.TempDir()
	orig := `{
  "version": 1,
  "hooks": {
    "stop": [{ "command": "./my-stop.sh" }]
  }
}`
	if err := os.WriteFile(filepath.Join(tmpDir, "hooks.json"), []byte(orig), 0644); err != nil {
		t.Fatalf("seed hooks.json: %v", err)
	}

	installed, err := InjectCursorHooks(tmpDir)
	if err != nil {
		t.Fatalf("InjectCursorHooks failed: %v", err)
	}
	if !installed {
		t.Fatal("expected hooks to be installed")
	}

	data, err := os.ReadFile(filepath.Join(tmpDir, "hooks.json"))
	if err != nil {
		t.Fatalf("read hooks.json: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "./my-stop.sh") {
		t.Fatal("expected existing stop hook preserved")
	}
	if !strings.Contains(text, agentDeckCursorHookCommand) {
		t.Fatal("expected agent-deck hook appended")
	}
}

func TestInjectCursorHooks_Idempotent(t *testing.T) {
	tmpDir := t.TempDir()
	if _, err := InjectCursorHooks(tmpDir); err != nil {
		t.Fatalf("first install: %v", err)
	}
	installed, err := InjectCursorHooks(tmpDir)
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if installed {
		t.Fatal("expected idempotent install to return false")
	}
}

func TestRemoveCursorHooks(t *testing.T) {
	tmpDir := t.TempDir()
	if _, err := InjectCursorHooks(tmpDir); err != nil {
		t.Fatalf("install: %v", err)
	}
	removed, err := RemoveCursorHooks(tmpDir)
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !removed {
		t.Fatal("expected hooks removed")
	}
	if CheckCursorHooksInstalled(tmpDir) {
		t.Fatal("hooks should not be installed after remove")
	}
}

func TestCheckCursorHooksInstalled(t *testing.T) {
	tmpDir := t.TempDir()
	if CheckCursorHooksInstalled(tmpDir) {
		t.Fatal("expected not installed on empty dir")
	}
	if _, err := InjectCursorHooks(tmpDir); err != nil {
		t.Fatalf("install: %v", err)
	}
	if !CheckCursorHooksInstalled(tmpDir) {
		t.Fatal("expected installed after inject")
	}
}
