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
	var cfg struct {
		Version int                          `json:"version"`
		Hooks   map[string][]json.RawMessage `json:"hooks"`
	}
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
	if _, ok := cfg.Hooks[cursorCommsHookEvent]; ok {
		t.Fatalf("%s installed with the ledger off", cursorCommsHookEvent)
	}
}

// Comms Ledger (docs/comms.md): with the ledger off an install made by an
// older agent-deck is left byte for byte; with it on, afterAgentResponse is
// added and every user field on every hook survives the rewrite.
func TestInjectCursorHooks_LedgerGatesAfterAgentResponseAndKeepsUserFields(t *testing.T) {
	tmpDir := t.TempDir()
	orig := `{
  "version": 1,
  "customTopLevel": {"keep": true},
  "hooks": {
    "sessionStart": [{"command": "agent-deck hook-handler"}],
    "sessionEnd": [{"command": "agent-deck hook-handler"}],
    "beforeSubmitPrompt": [{"command": "agent-deck hook-handler"}],
    "preToolUse": [{"command": "agent-deck hook-handler"}],
    "postToolUse": [{"command": "agent-deck hook-handler"}],
    "stop": [{"command": "./my-stop.sh", "timeout": 30, "loop_limit": 3, "failClosed": true}, {"command": "agent-deck hook-handler"}],
    "afterAgentThought": [{"type": "prompt", "prompt": "summarise"}]
  }
}`
	path := filepath.Join(tmpDir, "hooks.json")
	if err := os.WriteFile(path, []byte(orig), 0644); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(SetCommsLedgerForTest(false))
	if !CheckCursorHooksInstalled(tmpDir) {
		t.Fatal("a pre-ledger install must count as installed with the ledger off")
	}
	installed, err := InjectCursorHooks(tmpDir)
	if err != nil || installed {
		t.Fatalf("ledger off: installed=%v err=%v, want no-op", installed, err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != orig {
		t.Fatalf("ledger off rewrote hooks.json:\n%s", after)
	}

	restore := SetCommsLedgerForTest(true)
	defer restore()
	if CheckCursorHooksInstalled(tmpDir) {
		t.Fatal("ledger on: the comms event is missing, so not installed")
	}
	installed, err = InjectCursorHooks(tmpDir)
	if err != nil || !installed {
		t.Fatalf("ledger on: installed=%v err=%v", installed, err)
	}
	after, _ = os.ReadFile(path)
	text := string(after)
	for _, want := range []string{`"timeout": 30`, `"loop_limit": 3`, `"failClosed": true`, `"type": "prompt"`, `"prompt": "summarise"`, `"customTopLevel"`, `"./my-stop.sh"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("user field %s lost:\n%s", want, text)
		}
	}
	f, _, err := readCursorHooksFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cursorEventHasAgentDeckHook(f.hooks[cursorCommsHookEvent]) || len(f.hooks["stop"]) != 2 {
		t.Fatalf("ledger on install: %s", text)
	}
	if !CheckCursorHooksInstalled(tmpDir) {
		t.Fatal("not installed after the ledger-on install")
	}
	if again, _ := InjectCursorHooks(tmpDir); again {
		t.Fatal("second ledger-on install must be a no-op")
	}

	// Uninstall strips the comms event too, whatever the switch says now.
	restore()
	removed, err := RemoveCursorHooks(tmpDir)
	if err != nil || !removed {
		t.Fatalf("remove: removed=%v err=%v", removed, err)
	}
	f, _, _ = readCursorHooksFile(path)
	if _, ok := f.hooks[cursorCommsHookEvent]; ok {
		t.Fatal("afterAgentResponse survived uninstall")
	}
	if len(f.hooks["stop"]) != 1 || cursorEntryCommand(f.hooks["stop"][0]) != "./my-stop.sh" {
		t.Fatalf("user stop hook lost on uninstall: %v", f.hooks["stop"])
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

// Regression test for issue #1672: TUI startup silently reinstalled Cursor
// hooks after `cursor-hooks uninstall`. AutoInstallCursorHooks must honor the
// durable opt-out ([cursor] hooks_enabled = false) instead of unconditionally
// injecting whenever the cursor binary is on PATH.
func TestAutoInstallCursorHooks_RespectsDurableOptOut(t *testing.T) {
	tmpDir := t.TempDir()
	disabled := false
	cfg := &UserConfig{Cursor: CursorSettings{HooksEnabled: &disabled}}

	installed, err := AutoInstallCursorHooks(cfg, tmpDir)
	if err != nil {
		t.Fatalf("AutoInstallCursorHooks failed: %v", err)
	}
	if installed {
		t.Fatal("hooks were installed despite [cursor] hooks_enabled = false")
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "hooks.json")); !os.IsNotExist(err) {
		t.Fatal("hooks.json was created despite [cursor] hooks_enabled = false")
	}
}

func TestAutoInstallCursorHooks_InstallsByDefault(t *testing.T) {
	for _, cfg := range []*UserConfig{nil, {}} {
		tmpDir := t.TempDir()
		installed, err := AutoInstallCursorHooks(cfg, tmpDir)
		if err != nil {
			t.Fatalf("AutoInstallCursorHooks failed: %v", err)
		}
		if !installed {
			t.Fatalf("expected hooks to be installed for cfg=%v", cfg)
		}
		if !CheckCursorHooksInstalled(tmpDir) {
			t.Fatal("hooks not present after AutoInstallCursorHooks")
		}
	}
}

func TestAutoInstallCursorHooks_NoopWhenAlreadyInstalled(t *testing.T) {
	tmpDir := t.TempDir()
	if _, err := InjectCursorHooks(tmpDir); err != nil {
		t.Fatalf("seed install failed: %v", err)
	}
	installed, err := AutoInstallCursorHooks(&UserConfig{}, tmpDir)
	if err != nil {
		t.Fatalf("AutoInstallCursorHooks failed: %v", err)
	}
	if installed {
		t.Fatal("expected no reinstall when hooks already present")
	}
}

func TestCursorSettings_GetHooksEnabled(t *testing.T) {
	var c CursorSettings
	if !c.GetHooksEnabled() {
		t.Fatal("default GetHooksEnabled() = false, want true")
	}
	v := false
	c.HooksEnabled = &v
	if c.GetHooksEnabled() {
		t.Fatal("GetHooksEnabled() = true with hooks_enabled = false")
	}
	v2 := true
	c.HooksEnabled = &v2
	if !c.GetHooksEnabled() {
		t.Fatal("GetHooksEnabled() = false with hooks_enabled = true")
	}
}

// The `cursor-hooks uninstall` opt-out must survive a config round-trip: it is
// written to config.toml and read back by LoadUserConfig on the next TUI start.
func TestSetCursorHooksEnabled_RoundTrip(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)
	isolateConfigHomeXDG(t)

	if err := SetCursorHooksEnabled(false); err != nil {
		t.Fatalf("SetCursorHooksEnabled(false) failed: %v", err)
	}
	ClearUserConfigCache()
	cfg, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig failed: %v", err)
	}
	if cfg.Cursor.GetHooksEnabled() {
		t.Fatal("opt-out did not persist: GetHooksEnabled() = true after SetCursorHooksEnabled(false)")
	}

	// Explicit install clears the opt-out back to the default.
	if err := SetCursorHooksEnabled(true); err != nil {
		t.Fatalf("SetCursorHooksEnabled(true) failed: %v", err)
	}
	ClearUserConfigCache()
	cfg, err = LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig failed: %v", err)
	}
	if !cfg.Cursor.GetHooksEnabled() {
		t.Fatal("SetCursorHooksEnabled(true) did not restore the default")
	}
}
