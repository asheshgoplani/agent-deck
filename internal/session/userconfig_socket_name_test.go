package session

import (
	"os"
	"path/filepath"
	"testing"
)

// socket_name is the opt-in config key for socket isolation (issue #687,
// v1.7.50). These tests pin down the three requirements from the RFC:
//   1. Zero-config users keep getting empty string (no -L, pre-v1.7.50
//      behavior preserved byte-for-byte).
//   2. An explicit `[tmux].socket_name = "<name>"` round-trips through the
//      TOML loader into GetSocketName().
//   3. Whitespace-only values are defensively treated as empty — a
//      `socket_name = "   "` typo must not silently strand agent-deck on a
//      server named "   ".

// TestGetTmuxSettings_SocketName_DefaultEmpty: the zero-config install.
// GetSocketName must return "" so every existing call site keeps its
// pre-v1.7.50 plain-tmux invocation.
func TestGetTmuxSettings_SocketName_DefaultEmpty(t *testing.T) {
	tempDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", originalHome)
	ClearUserConfigCache()

	agentDeckDir := filepath.Join(tempDir, ".agent-deck")
	_ = os.MkdirAll(agentDeckDir, 0o700)

	configPath := filepath.Join(agentDeckDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(""), 0o644); err != nil {
		t.Fatalf("write empty config: %v", err)
	}
	ClearUserConfigCache()

	if got := GetTmuxSettings().GetSocketName(); got != "" {
		t.Fatalf("SocketName must default to empty string; got %q", got)
	}
}

// TestGetTmuxSettings_SocketName_Explicit: the primary happy path.
// Users who write `socket_name = "agent-deck"` in their config must see
// that value surface via GetSocketName() so it can be pushed into
// tmux.SetDefaultSocketName at startup.
func TestGetTmuxSettings_SocketName_Explicit(t *testing.T) {
	tempDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", originalHome)
	ClearUserConfigCache()

	agentDeckDir := filepath.Join(tempDir, ".agent-deck")
	_ = os.MkdirAll(agentDeckDir, 0o700)

	configPath := filepath.Join(agentDeckDir, "config.toml")
	configContent := `
[tmux]
socket_name = "agent-deck"
`
	if err := os.WriteFile(configPath, []byte(configContent), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	ClearUserConfigCache()

	if got := GetTmuxSettings().GetSocketName(); got != "agent-deck" {
		t.Fatalf("SocketName round-trip broken; got %q want %q", got, "agent-deck")
	}
}

// TestGetTmuxSettings_SocketName_WhitespaceTrimmed: a defensive check.
// `socket_name = "  agent-deck\t"` — plausible when someone copy-pastes a
// value — must trim to "agent-deck", not leak surrounding whitespace into
// the tmux argv.
func TestGetTmuxSettings_SocketName_WhitespaceTrimmed(t *testing.T) {
	tempDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", originalHome)
	ClearUserConfigCache()

	agentDeckDir := filepath.Join(tempDir, ".agent-deck")
	_ = os.MkdirAll(agentDeckDir, 0o700)

	configPath := filepath.Join(agentDeckDir, "config.toml")
	configContent := `
[tmux]
socket_name = "   agent-deck  "
`
	if err := os.WriteFile(configPath, []byte(configContent), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	ClearUserConfigCache()

	if got := GetTmuxSettings().GetSocketName(); got != "agent-deck" {
		t.Fatalf("SocketName must be trimmed; got %q want %q", got, "agent-deck")
	}
}

// TestGetTmuxSettings_SocketName_WhitespaceOnlyTreatedAsEmpty: a typo-only
// value (`"   "`) must degrade to empty. Otherwise tmux would happily
// create a socket named "   " that nothing else could reach, and the user
// would see "agent-deck is broken" when in fact their config is broken.
func TestGetTmuxSettings_SocketName_WhitespaceOnlyTreatedAsEmpty(t *testing.T) {
	tempDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", originalHome)
	ClearUserConfigCache()

	agentDeckDir := filepath.Join(tempDir, ".agent-deck")
	_ = os.MkdirAll(agentDeckDir, 0o700)

	configPath := filepath.Join(agentDeckDir, "config.toml")
	configContent := `
[tmux]
socket_name = "   "
`
	if err := os.WriteFile(configPath, []byte(configContent), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	ClearUserConfigCache()

	if got := GetTmuxSettings().GetSocketName(); got != "" {
		t.Fatalf("whitespace-only socket_name must resolve to empty; got %q", got)
	}
}
