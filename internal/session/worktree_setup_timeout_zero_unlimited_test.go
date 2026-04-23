package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

// Follow-up to #727: @Clindbergh flagged that `setup_timeout_seconds = 0`
// meaning "use default" violates CLI convention. The intuitive reading of
// `0` is "unlimited / no timeout". These tests pin the new semantic before
// any user adopts the old one (v1.7.65 shipped 2 days ago).

// ZeroMeansUnlimited: an explicit `setup_timeout_seconds = 0` in TOML must
// resolve to the unlimited sentinel (duration 0 at the git layer), NOT the
// 60s legacy default.
func TestWorktreeSettings_SetupTimeout_ZeroMeansUnlimited(t *testing.T) {
	tmpDir := t.TempDir()
	configContent := `
[worktree]
setup_timeout_seconds = 0
`
	configPath := filepath.Join(tmpDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var cfg UserConfig
	if _, err := toml.DecodeFile(configPath, &cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// The resolved duration must be the unlimited sentinel — NOT 60s.
	got := cfg.Worktree.SetupTimeout()
	if got != UnlimitedWorktreeSetupTimeout {
		t.Errorf("explicit `setup_timeout_seconds = 0` must resolve to UnlimitedWorktreeSetupTimeout (%v); got %v",
			UnlimitedWorktreeSetupTimeout, got)
	}
	// Guard against someone quietly re-introducing the old semantic via
	// SetupTimeout() returning 60s for an explicit zero.
	if got == DefaultWorktreeSetupTimeout {
		t.Errorf("explicit 0 must not collapse back to DefaultWorktreeSetupTimeout (%v)", DefaultWorktreeSetupTimeout)
	}
}

// UnsetStillDefaultsSixty: a missing [worktree] section or missing
// `setup_timeout_seconds` field must still produce the 60s default — users
// who never set the field keep the legacy behaviour.
func TestWorktreeSettings_SetupTimeout_UnsetStillDefaultsSixty(t *testing.T) {
	tmpDir := t.TempDir()
	// No [worktree] section at all.
	configContent := ``
	configPath := filepath.Join(tmpDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var cfg UserConfig
	if _, err := toml.DecodeFile(configPath, &cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if got, want := cfg.Worktree.SetupTimeout(), 60*time.Second; got != want {
		t.Errorf("unset setup_timeout_seconds must resolve to %v (legacy default); got %v", want, got)
	}
}

// NegativeValueTreatedAsUnset: a user who writes `setup_timeout_seconds = -5`
// gets the 60s default, not an unlimited timeout. Negative is nonsense; we
// refuse to interpret it as "unlimited".
func TestWorktreeSettings_SetupTimeout_NegativeValueDefaultsSixty(t *testing.T) {
	tmpDir := t.TempDir()
	configContent := `
[worktree]
setup_timeout_seconds = -5
`
	configPath := filepath.Join(tmpDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var cfg UserConfig
	if _, err := toml.DecodeFile(configPath, &cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if got, want := cfg.Worktree.SetupTimeout(), 60*time.Second; got != want {
		t.Errorf("negative setup_timeout_seconds must resolve to %v (default); got %v", want, got)
	}
}

// PositiveValueUnchanged: a positive value is still honoured verbatim.
// This guards against a regression where the zero-unlimited flip accidentally
// breaks the `= 300` path that motivated #727.
func TestWorktreeSettings_SetupTimeout_PositiveValueHonoured(t *testing.T) {
	tmpDir := t.TempDir()
	configContent := `
[worktree]
setup_timeout_seconds = 300
`
	configPath := filepath.Join(tmpDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var cfg UserConfig
	if _, err := toml.DecodeFile(configPath, &cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if got, want := cfg.Worktree.SetupTimeout(), 300*time.Second; got != want {
		t.Errorf("SetupTimeout() = %v, want %v", got, want)
	}
}
