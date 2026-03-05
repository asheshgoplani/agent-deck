package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestGeminiHooksInstallUninstall(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	handleGeminiHooksInstall()

	configPath := filepath.Join(session.GetGeminiConfigDir(), "settings.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, `"hooks"`) {
		t.Fatal("expected hooks section in settings.json")
	}
	if !strings.Contains(text, `"agent-deck hook-handler"`) {
		t.Fatal("expected agent-deck hook command")
	}

	handleGeminiHooksUninstall()

	data, err = os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read settings.json after uninstall: %v", err)
	}
	text = string(data)
	if strings.Contains(text, `"agent-deck hook-handler"`) {
		t.Fatal("expected agent-deck hook command removed")
	}
}

func TestGetGeminiConfigDirForHooks(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	got := getGeminiConfigDirForHooks()
	if !strings.HasSuffix(got, ".gemini") {
		t.Fatalf("getGeminiConfigDirForHooks() = %q, want ~/.gemini suffix", got)
	}
}
