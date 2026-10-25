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

// ASK-102: install and uninstall rewrite only agent-deck's own entries; other
// top-level keys and per-hook fields (timeout, ...) survive the round trip.
func TestCursorHooks_PreservesUnknownFields(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "hooks.json")
	seed := `{"version":1,"extra":true,"hooks":{"afterFileEdit":[{"command":"fmt.sh","timeout":30}],"stop":[{"command":"mine","timeout":5}]}}`
	if err := os.WriteFile(p, []byte(seed), 0644); err != nil {
		t.Fatal(err)
	}

	check := func(stage string) {
		t.Helper()
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Extra *bool                       `json:"extra"`
			Hooks map[string][]map[string]any `json:"hooks"`
		}
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		if got.Extra == nil || !*got.Extra {
			t.Errorf("%s: top-level extra dropped", stage)
		}
		if e := got.Hooks["afterFileEdit"]; len(e) != 1 || e[0]["timeout"] != float64(30) {
			t.Errorf("%s: afterFileEdit timeout lost: %v", stage, e)
		}
		found := false
		for _, h := range got.Hooks["stop"] {
			if h["command"] == "mine" {
				found = true
				if h["timeout"] != float64(5) {
					t.Errorf("%s: user stop timeout lost: %v", stage, h)
				}
			}
		}
		if !found {
			t.Errorf("%s: user stop hook missing", stage)
		}
	}

	if ok, err := InjectCursorHooks(dir); err != nil || !ok {
		t.Fatalf("inject: %v %v", ok, err)
	}
	check("after inject")
	if !CheckCursorHooksInstalled(dir) {
		t.Fatal("not installed")
	}
	if ok, err := RemoveCursorHooks(dir); err != nil || !ok {
		t.Fatalf("remove: %v %v", ok, err)
	}
	check("after remove")
}

// A hooks.json holding only JSON null installed fresh before the raw rewrite;
// it must not start failing AutoInstallCursorHooks on every TUI start.
func TestInjectCursorHooks_NullRootInstallsFresh(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "hooks.json")
	if err := os.WriteFile(p, []byte("null\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if ok, err := InjectCursorHooks(dir); err != nil || !ok {
		t.Fatalf("inject on null root: %v %v", ok, err)
	}
	if !CheckCursorHooksInstalled(dir) {
		t.Fatal("hooks not installed after null-root inject")
	}
}
