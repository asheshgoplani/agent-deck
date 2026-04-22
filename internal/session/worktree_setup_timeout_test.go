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

	if got, want := cfg.Worktree.SetupTimeoutSeconds, 120; got != want {
		t.Errorf("Worktree.SetupTimeoutSeconds = %d, want %d", got, want)
	}
}

// T3: Default (zero-value) WorktreeSettings.SetupTimeout() returns 60s for
// backward compatibility with every install that never set the new field.
func TestWorktreeSettings_SetupTimeout_DefaultSixtySeconds(t *testing.T) {
	var w WorktreeSettings // zero value: SetupTimeoutSeconds == 0

	if got, want := w.SetupTimeout(), 60*time.Second; got != want {
		t.Errorf("SetupTimeout() = %v, want %v (backward-compat default)", got, want)
	}
}

// T3b: A positive SetupTimeoutSeconds is honoured.
func TestWorktreeSettings_SetupTimeout_HonoursConfiguredValue(t *testing.T) {
	w := WorktreeSettings{SetupTimeoutSeconds: 300}

	if got, want := w.SetupTimeout(), 300*time.Second; got != want {
		t.Errorf("SetupTimeout() = %v, want %v", got, want)
	}
}
