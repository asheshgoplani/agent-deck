package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestQuickLaunchGetFavorites(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "quick-launch.toml")

	qlm := &QuickLaunchManager{configPath: configPath}

	// Empty config returns empty list
	favorites, err := qlm.GetFavorites()
	if err != nil {
		t.Fatalf("GetFavorites failed: %v", err)
	}
	if len(favorites) != 0 {
		t.Errorf("Expected 0 favorites, got %d", len(favorites))
	}
}

func TestQuickLaunchAddFavorite(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "quick-launch.toml")

	qlm := &QuickLaunchManager{configPath: configPath}

	// Add a favorite
	err := qlm.AddFavorite("API Server", "/projects/api", "claude")
	if err != nil {
		t.Fatalf("AddFavorite failed: %v", err)
	}

	// Verify it was added
	favorites, err := qlm.GetFavorites()
	if err != nil {
		t.Fatalf("GetFavorites failed: %v", err)
	}
	if len(favorites) != 1 {
		t.Fatalf("Expected 1 favorite, got %d", len(favorites))
	}

	if favorites[0].Name != "API Server" {
		t.Errorf("Expected name 'API Server', got '%s'", favorites[0].Name)
	}
	if favorites[0].Path != "/projects/api" {
		t.Errorf("Expected path '/projects/api', got '%s'", favorites[0].Path)
	}
	if favorites[0].Tool != "claude" {
		t.Errorf("Expected tool 'claude', got '%s'", favorites[0].Tool)
	}

	// Add another favorite
	err = qlm.AddFavorite("Web Client", "/projects/web", "gemini")
	if err != nil {
		t.Fatalf("AddFavorite failed: %v", err)
	}

	favorites, _ = qlm.GetFavorites()
	if len(favorites) != 2 {
		t.Errorf("Expected 2 favorites, got %d", len(favorites))
	}
}

func TestQuickLaunchAddDuplicate(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "quick-launch.toml")

	qlm := &QuickLaunchManager{configPath: configPath}

	// Add initial favorite
	qlm.AddFavorite("API", "/projects/api", "claude")

	// Add duplicate with different name/tool - should update
	err := qlm.AddFavorite("API Server", "/projects/api", "gemini")
	if err != nil {
		t.Fatalf("AddFavorite (duplicate) failed: %v", err)
	}

	favorites, _ := qlm.GetFavorites()
	if len(favorites) != 1 {
		t.Fatalf("Expected 1 favorite (updated), got %d", len(favorites))
	}

	// Verify the name and tool were actually updated
	if favorites[0].Name != "API Server" {
		t.Errorf("Expected updated name 'API Server', got '%s'", favorites[0].Name)
	}
	if favorites[0].Tool != "gemini" {
		t.Errorf("Expected updated tool 'gemini', got '%s'", favorites[0].Tool)
	}
}

func TestQuickLaunchRemoveFavorite(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "quick-launch.toml")

	qlm := &QuickLaunchManager{configPath: configPath}

	// Add favorites
	qlm.AddFavorite("API", "/projects/api", "claude")
	qlm.AddFavorite("Web", "/projects/web", "gemini")

	// Remove one
	err := qlm.RemoveFavorite("/projects/api")
	if err != nil {
		t.Fatalf("RemoveFavorite failed: %v", err)
	}

	favorites, _ := qlm.GetFavorites()
	if len(favorites) != 1 {
		t.Fatalf("Expected 1 favorite after removal, got %d", len(favorites))
	}

	if favorites[0].Path != "/projects/web" {
		t.Errorf("Wrong favorite remained, expected web, got %s", favorites[0].Path)
	}
}

func TestQuickLaunchLoadExistingConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "quick-launch.toml")

	// Create a config file manually
	content := `
[[favorites]]
name = "Test Project"
path = "/test/path"
tool = "claude"

[[favorites]]
name = "Another"
path = "/another/path"
tool = "gemini"
shortcut = "cmd+shift+a"
`
	os.WriteFile(configPath, []byte(content), 0600)

	qlm := &QuickLaunchManager{configPath: configPath}

	favorites, err := qlm.GetFavorites()
	if err != nil {
		t.Fatalf("GetFavorites failed: %v", err)
	}

	if len(favorites) != 2 {
		t.Fatalf("Expected 2 favorites, got %d", len(favorites))
	}

	if favorites[0].Name != "Test Project" {
		t.Errorf("Expected first name 'Test Project', got '%s'", favorites[0].Name)
	}
	if favorites[1].Shortcut != "cmd+shift+a" {
		t.Errorf("Expected shortcut 'cmd+shift+a', got '%s'", favorites[1].Shortcut)
	}
}

func TestQuickLaunchTildeExpansion(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "quick-launch.toml")

	// Create a config file with tilde paths
	content := `
[[favorites]]
name = "Home Project"
path = "~/projects/test"
tool = "claude"
`
	os.WriteFile(configPath, []byte(content), 0600)

	qlm := &QuickLaunchManager{configPath: configPath}

	favorites, err := qlm.GetFavorites()
	if err != nil {
		t.Fatalf("GetFavorites failed: %v", err)
	}

	if len(favorites) != 1 {
		t.Fatalf("Expected 1 favorite, got %d", len(favorites))
	}

	home, _ := os.UserHomeDir()
	expectedPath := filepath.Join(home, "projects/test")

	if favorites[0].Path != expectedPath {
		t.Errorf("Tilde not expanded: expected '%s', got '%s'", expectedPath, favorites[0].Path)
	}
}

func TestQuickLaunchUpdateShortcut(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "quick-launch.toml")

	qlm := &QuickLaunchManager{configPath: configPath}

	// Add favorite
	qlm.AddFavorite("API", "/projects/api", "claude")

	// Update shortcut
	err := qlm.UpdateShortcut("/projects/api", "cmd+shift+a")
	if err != nil {
		t.Fatalf("UpdateShortcut failed: %v", err)
	}

	favorites, _ := qlm.GetFavorites()
	if favorites[0].Shortcut != "cmd+shift+a" {
		t.Errorf("Expected shortcut 'cmd+shift+a', got '%s'", favorites[0].Shortcut)
	}
}

func TestQuickLaunchSpecialCharacters(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "quick-launch.toml")

	qlm := &QuickLaunchManager{configPath: configPath}

	// Test cases with special characters that could break TOML if not escaped
	testCases := []struct {
		name string
		path string
		tool string
	}{
		{
			name: `Project with "quotes"`,
			path: "/projects/quotes",
			tool: "claude",
		},
		{
			name: `Path with \backslashes\`,
			path: `/projects/back\slash`,
			tool: "gemini",
		},
		{
			name: "Name with\nnewline",
			path: "/projects/newline",
			tool: "claude",
		},
		{
			name: `Mixed "special" chars\n`,
			path: "/projects/mixed",
			tool: "opencode",
		},
	}

	// Add all favorites with special characters
	for _, tc := range testCases {
		err := qlm.AddFavorite(tc.name, tc.path, tc.tool)
		if err != nil {
			t.Fatalf("AddFavorite failed for %q: %v", tc.name, err)
		}
	}

	// Verify the file was written and can be parsed
	favorites, err := qlm.GetFavorites()
	if err != nil {
		t.Fatalf("GetFavorites failed after adding special chars: %v", err)
	}

	if len(favorites) != len(testCases) {
		t.Fatalf("Expected %d favorites, got %d", len(testCases), len(favorites))
	}

	// Verify each favorite was saved and loaded correctly
	for i, tc := range testCases {
		if favorites[i].Name != tc.name {
			t.Errorf("Favorite %d: expected name %q, got %q", i, tc.name, favorites[i].Name)
		}
		if favorites[i].Path != tc.path {
			t.Errorf("Favorite %d: expected path %q, got %q", i, tc.path, favorites[i].Path)
		}
		if favorites[i].Tool != tc.tool {
			t.Errorf("Favorite %d: expected tool %q, got %q", i, tc.tool, favorites[i].Tool)
		}
	}

	// Also verify the raw file content is valid TOML by reading it directly
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("Failed to read config file: %v", err)
	}

	// The file should start with the header comments
	content := string(data)
	if !strings.Contains(content, "# Quick Launch Favorites") {
		t.Error("Config file missing header comment")
	}
}
