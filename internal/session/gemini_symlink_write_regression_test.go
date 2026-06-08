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

func TestRemoveGeminiHooks_PreservesSymlink(t *testing.T) {
	configDir := t.TempDir()
	link := filepath.Join(configDir, "settings.json")
	symlinkedFile(t, link, "{}")

	if _, err := session.InjectGeminiHooks(configDir); err != nil {
		t.Fatalf("InjectGeminiHooks: %v", err)
	}
	if _, err := session.RemoveGeminiHooks(configDir); err != nil {
		t.Fatalf("RemoveGeminiHooks: %v", err)
	}

	assertStillSymlink(t, link)
}

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
