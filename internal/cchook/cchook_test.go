package cchook_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/cchook"
)

func TestResolveWorktreeHooks_NoFiles(t *testing.T) {
	dir := t.TempDir()
	userDir := t.TempDir()
	managedDir := t.TempDir()

	result := cchook.ResolveWorktreeHooks("WorktreeCreate", dir, userDir, managedDir)

	if result != nil {
		t.Fatalf("expected nil when no settings files exist, got %v", result)
	}
}

func TestResolveWorktreeHooks_SingleProjectHook(t *testing.T) {
	repoDir := t.TempDir()
	userDir := t.TempDir()
	managedDir := t.TempDir()

	// Create project settings with a WorktreeCreate hook
	claudeDir := filepath.Join(repoDir, ".claude")
	if err := os.Mkdir(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	settingsPath := filepath.Join(claudeDir, "settings.json")
	settingsJSON := `{
		"hooks": {
			"WorktreeCreate": [
				{
					"hooks": [
						{"type": "command", "command": "echo project"}
					]
				}
			]
		}
	}`
	if err := os.WriteFile(settingsPath, []byte(settingsJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	result := cchook.ResolveWorktreeHooks("WorktreeCreate", repoDir, userDir, managedDir)

	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(result.Entries))
	}
	if result.Entries[0].Command != "echo project" {
		t.Fatalf("command = %q, want %q", result.Entries[0].Command, "echo project")
	}
	if result.Entries[0].Level != cchook.LevelProject {
		t.Fatalf("level = %v, want %v", result.Entries[0].Level, cchook.LevelProject)
	}
}

func TestResolveWorktreeHooks_AllLevels_PriorityOrder(t *testing.T) {
	repoDir := t.TempDir()
	userDir := t.TempDir()
	managedDir := t.TempDir()

	// Create user settings
	userSettings := filepath.Join(userDir, "settings.json")
	if err := os.WriteFile(userSettings, []byte(`{
		"hooks": {
			"WorktreeCreate": [
				{
					"hooks": [
						{"type": "command", "command": "user-hook"}
					]
				}
			]
		}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create project settings
	claudeDir := filepath.Join(repoDir, ".claude")
	if err := os.Mkdir(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(`{
		"hooks": {
			"WorktreeCreate": [
				{
					"hooks": [
						{"type": "command", "command": "project-hook"}
					]
				}
			]
		}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create local settings
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.local.json"), []byte(`{
		"hooks": {
			"WorktreeCreate": [
				{
					"hooks": [
						{"type": "command", "command": "local-hook"}
					]
				}
			]
		}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create managed settings
	if err := os.WriteFile(filepath.Join(managedDir, "managed-settings.json"), []byte(`{
		"hooks": {
			"WorktreeCreate": [
				{
					"hooks": [
						{"type": "command", "command": "managed-hook"}
					]
				}
			]
		}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	result := cchook.ResolveWorktreeHooks("WorktreeCreate", repoDir, userDir, managedDir)

	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result.Entries) != 4 {
		t.Fatalf("expected 4 entries, got %d", len(result.Entries))
	}

	// Check priority order: user > project > local > managed
	expectedOrder := []struct {
		cmd   string
		level cchook.Level
	}{
		{"user-hook", cchook.LevelUser},
		{"project-hook", cchook.LevelProject},
		{"local-hook", cchook.LevelLocal},
		{"managed-hook", cchook.LevelManaged},
	}

	for i, expected := range expectedOrder {
		if result.Entries[i].Command != expected.cmd {
			t.Fatalf("entry %d command = %q, want %q", i, result.Entries[i].Command, expected.cmd)
		}
		if result.Entries[i].Level != expected.level {
			t.Fatalf("entry %d level = %v, want %v", i, result.Entries[i].Level, expected.level)
		}
	}
}

func TestResolveWorktreeHooks_MalformedJSON_Skipped(t *testing.T) {
	repoDir := t.TempDir()
	userDir := t.TempDir()
	managedDir := t.TempDir()

	// Create malformed user settings
	userSettings := filepath.Join(userDir, "settings.json")
	if err := os.WriteFile(userSettings, []byte(`{invalid json}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create valid project settings
	claudeDir := filepath.Join(repoDir, ".claude")
	if err := os.Mkdir(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(`{
		"hooks": {
			"WorktreeCreate": [
				{
					"hooks": [
						{"type": "command", "command": "valid-hook"}
					]
				}
			]
		}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	result := cchook.ResolveWorktreeHooks("WorktreeCreate", repoDir, userDir, managedDir)

	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result.Entries) != 1 {
		t.Fatalf("expected 1 entry (malformed user settings should be skipped), got %d", len(result.Entries))
	}
	if result.Entries[0].Command != "valid-hook" {
		t.Fatalf("command = %q, want %q", result.Entries[0].Command, "valid-hook")
	}
	if result.Entries[0].Level != cchook.LevelProject {
		t.Fatalf("level = %v, want %v", result.Entries[0].Level, cchook.LevelProject)
	}
}

func TestResolveWorktreeHooks_WorktreeRemove(t *testing.T) {
	repoDir := t.TempDir()
	userDir := t.TempDir()
	managedDir := t.TempDir()

	// Create project settings with a WorktreeRemove hook
	claudeDir := filepath.Join(repoDir, ".claude")
	if err := os.Mkdir(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	settingsPath := filepath.Join(claudeDir, "settings.json")
	settingsJSON := `{
		"hooks": {
			"WorktreeRemove": [
				{
					"hooks": [
						{"type": "command", "command": "cleanup-script.sh"}
					]
				}
			]
		}
	}`
	if err := os.WriteFile(settingsPath, []byte(settingsJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	result := cchook.ResolveWorktreeHooks("WorktreeRemove", repoDir, userDir, managedDir)

	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(result.Entries))
	}
	if result.Entries[0].Command != "cleanup-script.sh" {
		t.Fatalf("command = %q, want %q", result.Entries[0].Command, "cleanup-script.sh")
	}
}
