package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

// T1: [worktree].setup_timeout_seconds parses into WorktreeSettings.SetupTimeoutSeconds.
// Reporter @Clindbergh in GH #724: 60s hardcoded is too tight for install-deps + DB-setup
// scripts, so users need a way to raise it via config.toml.
func TestWorktreeSettings_SetupTimeoutSeconds_ParsesFromTOML(t *testing.T) {
	tmpDir := t.TempDir()
	configContent := `
[worktree]
setup_timeout_seconds = 120
`
	configPath := filepath.Join(tmpDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var cfg UserConfig
	if _, err := toml.DecodeFile(configPath, &cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if cfg.Worktree.SetupTimeoutSeconds == nil {
		t.Fatalf("Worktree.SetupTimeoutSeconds = nil, want *120 (pointer disambiguates unset from explicit zero — see #727 follow-up)")
	}
	if got, want := *cfg.Worktree.SetupTimeoutSeconds, 120; got != want {
		t.Errorf("*Worktree.SetupTimeoutSeconds = %d, want %d", got, want)
	}
}

// T3: Zero-value WorktreeSettings (pointer nil = field unset) resolves to 60s
// for backward compatibility. This is the path every install hit before #727
// and every install that does not adopt [worktree].setup_timeout_seconds.
func TestWorktreeSettings_SetupTimeout_DefaultSixtySeconds(t *testing.T) {
	var w WorktreeSettings // zero value: SetupTimeoutSeconds == nil

	if got, want := w.SetupTimeout(), 60*time.Second; got != want {
		t.Errorf("SetupTimeout() = %v, want %v (backward-compat default)", got, want)
	}
}

// T3b: A positive SetupTimeoutSeconds is honoured.
func TestWorktreeSettings_SetupTimeout_HonoursConfiguredValue(t *testing.T) {
	v := 300
	w := WorktreeSettings{SetupTimeoutSeconds: &v}

	if got, want := w.SetupTimeout(), 300*time.Second; got != want {
		t.Errorf("SetupTimeout() = %v, want %v", got, want)
	}
}
