package session_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// These regression tests pin the symlink-preserving write for the Gemini config
// writers: a dotfiles-managed ~/.gemini/settings.json that is a symlink must be
// updated through the link, leaving the symlink intact. See internal/atomicfile.

// TestInjectGeminiHooks_PreservesSymlink verifies InjectGeminiHooks writes
// through a symlinked settings.json: the link survives and the real target
// receives the hook command.
func TestInjectGeminiHooks_PreservesSymlink(t *testing.T) {
	configDir := t.TempDir()
	link := filepath.Join(configDir, "settings.json")
	realPath := symlinkedFile(t, link, "{}")

	if _, err := session.InjectGeminiHooks(configDir); err != nil {
		t.Fatalf("InjectGeminiHooks: %v", err)
	}

	assertStillSymlink(t, link)
	data, err := os.ReadFile(realPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "hook-handler") {
		t.Fatalf("hooks not written through symlink to target; got: %s", data)
	}
}

// TestRemoveGeminiHooks_PreservesSymlink verifies RemoveGeminiHooks writes
// through a symlinked settings.json: the link survives, removal reports work
// done, and the hook command is gone from the real target (not just a no-op).
func TestRemoveGeminiHooks_PreservesSymlink(t *testing.T) {
	configDir := t.TempDir()
	link := filepath.Join(configDir, "settings.json")
	realPath := symlinkedFile(t, link, "{}")

	if _, err := session.InjectGeminiHooks(configDir); err != nil {
		t.Fatalf("InjectGeminiHooks: %v", err)
	}
	removed, err := session.RemoveGeminiHooks(configDir)
	if err != nil {
		t.Fatalf("RemoveGeminiHooks: %v", err)
	}
	if !removed {
		t.Fatal("expected hooks to be removed")
	}

	assertStillSymlink(t, link)
	data, err := os.ReadFile(realPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "hook-handler") {
		t.Fatalf("hooks not removed from symlink target; got: %s", data)
	}
}

// TestWriteGeminiMCPSettings_PreservesSymlink verifies WriteGeminiMCPSettings
// writes through a symlinked settings.json: the link survives and the real
// target receives the mcpServers block.
func TestWriteGeminiMCPSettings_PreservesSymlink(t *testing.T) {
	configFile := filepath.Join(session.GetGeminiConfigDir(), "settings.json")
	realPath := symlinkedFile(t, configFile, "{}")

	if err := session.WriteGeminiMCPSettings(nil); err != nil {
		t.Fatalf("WriteGeminiMCPSettings: %v", err)
	}

	assertStillSymlink(t, configFile)
	data, err := os.ReadFile(realPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "mcpServers") {
		t.Fatalf("mcpServers not written through symlink to target; got: %s", data)
	}
}
