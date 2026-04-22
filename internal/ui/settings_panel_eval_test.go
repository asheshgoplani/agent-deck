//go:build eval_smoke

package ui

// Behavioral eval for the Settings TUI save path (issue #710).
//
// Why this lives in internal/ui/ and not tests/eval/: Go's internal-package
// rule prevents tests/eval/... from importing internal/ui. The eval is still
// part of the eval_smoke tier — it runs only under `-tags eval_smoke`. See
// tests/eval/README.md.
//
// Motivation: #710 (and the original report half of #687) — the Settings TUI
// silently dropped the entire [tmux] config table on save because
// SettingsPanel.GetConfig forgot to copy `config.Tmux` from originalConfig.
// Unit tests on GetConfig caught the in-memory regression after the fact;
// this eval guards the user-observable claim end-to-end: "open settings,
// change something unrelated, save → my [tmux] block is still on disk."
//
// We could not catch this earlier because no test exercised the full
// LoadUserConfig → SettingsPanel → GetConfig → SaveUserConfig → re-read
// round-trip. That coverage gap is what this file closes.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestEval_SettingsTUI_SavePreservesTmux is the guard for #710 at the
// save-round-trip layer. It writes a config.toml with a [tmux] block to a
// scratch HOME, loads it through the Settings panel exactly as the TUI
// would, mutates a NON-tmux field (theme), calls SaveUserConfig with the
// panel's GetConfig() output, and then re-parses config.toml from disk to
// assert the tmux block is intact.
//
// Without the #710 fix, the final reload returns zero-valued TmuxSettings
// and the assertions below fail with "InjectStatusLine = nil".
func TestEval_SettingsTUI_SavePreservesTmux(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	configDir := filepath.Join(homeDir, ".agent-deck")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}

	// Seed a config.toml with [tmux] populated. These three fields cover
	// all three Tmux struct member shapes: *bool, *bool, string.
	seedTOML := `# Agent Deck Configuration
theme = "dark"

[tmux]
inject_status_line = false
launch_in_user_scope = true
detach_key = "C-q"
`
	configPath := filepath.Join(configDir, session.UserConfigFileName)
	if err := os.WriteFile(configPath, []byte(seedTOML), 0o600); err != nil {
		t.Fatalf("seed config.toml: %v", err)
	}

	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)

	// Replay the TUI flow: load existing config into the panel, change a
	// non-tmux field (theme), then ask the panel for the to-be-saved config.
	loaded, err := session.LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	if loaded.Tmux.InjectStatusLine == nil || *loaded.Tmux.InjectStatusLine != false {
		t.Fatalf("seed sanity: loaded inject_status_line = %v, want false ptr", loaded.Tmux.InjectStatusLine)
	}

	// Mirror what SettingsPanel.Show() does at runtime: LoadConfig populates
	// the visible widget state, originalConfig retains the full snapshot for
	// pass-through preservation in GetConfig.
	panel := NewSettingsPanel()
	panel.LoadConfig(loaded)
	panel.originalConfig = loaded
	panel.selectedTheme = 1 // toggle dark → light, a non-tmux change

	saved := panel.GetConfig()
	if err := session.SaveUserConfig(saved); err != nil {
		t.Fatalf("SaveUserConfig: %v", err)
	}

	// Re-read from disk to mimic the next agent-deck launch. Clear the
	// in-process cache first so we hit the file rather than a stale handle.
	session.ClearUserConfigCache()
	reloaded, err := session.LoadUserConfig()
	if err != nil {
		t.Fatalf("reload after save: %v", err)
	}

	if reloaded.Theme != "light" {
		t.Fatalf("non-tmux change lost: Theme = %q, want %q", reloaded.Theme, "light")
	}
	if reloaded.Tmux.InjectStatusLine == nil {
		t.Fatalf("Tmux.InjectStatusLine dropped on save (#710 regression). The Settings TUI save path is silently zeroing the [tmux] table again — re-add `config.Tmux = s.originalConfig.Tmux` in SettingsPanel.GetConfig.")
	}
	if *reloaded.Tmux.InjectStatusLine != false {
		t.Fatalf("Tmux.InjectStatusLine flipped: got %v, want false", *reloaded.Tmux.InjectStatusLine)
	}
	if reloaded.Tmux.LaunchInUserScope == nil || *reloaded.Tmux.LaunchInUserScope != true {
		t.Fatalf("Tmux.LaunchInUserScope dropped: got %v", reloaded.Tmux.LaunchInUserScope)
	}
	if reloaded.Tmux.DetachKey != "C-q" {
		t.Fatalf("Tmux.DetachKey dropped: got %q, want %q", reloaded.Tmux.DetachKey, "C-q")
	}
}
