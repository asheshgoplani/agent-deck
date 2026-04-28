package session

import (
	"os"
	"path/filepath"
	"testing"
)

// withTempHomeAndConfig sets HOME to a temp dir, writes the given config.toml
// contents (or no file when contents is empty), clears the user-config cache,
// and registers cleanup that restores HOME and clears the cache again. It
// returns the temp dir for tests that need to inspect the path.
func withTempHomeAndConfig(t *testing.T, contents string) string {
	t.Helper()
	tempDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	t.Cleanup(func() {
		os.Setenv("HOME", originalHome)
		ClearUserConfigCache()
	})
	ClearUserConfigCache()

	if contents != "" {
		agentDeckDir := filepath.Join(tempDir, ".agent-deck")
		if err := os.MkdirAll(agentDeckDir, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(agentDeckDir, "config.toml"), []byte(contents), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
	}
	return tempDir
}

func TestWebSettings_DefaultsTrueWhenAbsent(t *testing.T) {
	withTempHomeAndConfig(t, `
[claude]
config_dir = "~/.claude"
`)
	if !GetWebMutationsEnabled() {
		t.Errorf("GetWebMutationsEnabled() = false, want true when [web] is absent")
	}
}

func TestWebSettings_DefaultsTrueWhenNoConfigFile(t *testing.T) {
	withTempHomeAndConfig(t, "")
	if !GetWebMutationsEnabled() {
		t.Errorf("GetWebMutationsEnabled() = false, want true when config.toml is missing")
	}
}

func TestWebSettings_ExplicitTrue(t *testing.T) {
	withTempHomeAndConfig(t, `
[web]
mutations_enabled = true
`)
	if !GetWebMutationsEnabled() {
		t.Errorf("GetWebMutationsEnabled() = false, want true when explicitly enabled")
	}
}

func TestWebSettings_ExplicitFalse(t *testing.T) {
	withTempHomeAndConfig(t, `
[web]
mutations_enabled = false
`)
	if GetWebMutationsEnabled() {
		t.Errorf("GetWebMutationsEnabled() = true, want false when explicitly disabled")
	}
}
