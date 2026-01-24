package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestFrecencyScoring tests the frecency calculation algorithm
func TestFrecencyScoring(t *testing.T) {
	// Create a temp directory for test data
	tmpDir := t.TempDir()
	frecencyPath := filepath.Join(tmpDir, "frecency.json")

	pd := &ProjectDiscovery{
		frecencyPath: frecencyPath,
		frecency:     &FrecencyData{Projects: make(map[string]ProjectUsage)},
		configPath:   filepath.Join(tmpDir, "config.toml"), // Non-existent
	}

	// Test: unused project has score 0
	score := pd.calculateFrecencyScore("/some/path")
	if score != 0 {
		t.Errorf("Expected score 0 for unused project, got %f", score)
	}

	// Test: project used today gets 100x multiplier
	pd.frecency.Projects["/today/project"] = ProjectUsage{
		UseCount:   5,
		LastUsedAt: time.Now(),
	}
	score = pd.calculateFrecencyScore("/today/project")
	expectedScore := 5.0 * 100 // 5 uses * 100 (today multiplier)
	if score != expectedScore {
		t.Errorf("Expected score %f for today's project, got %f", expectedScore, score)
	}

	// Test: project used 3 days ago gets 70x multiplier (this week)
	pd.frecency.Projects["/week/project"] = ProjectUsage{
		UseCount:   3,
		LastUsedAt: time.Now().Add(-3 * 24 * time.Hour),
	}
	score = pd.calculateFrecencyScore("/week/project")
	expectedScore = 3.0 * 70 // 3 uses * 70 (this week multiplier)
	if score != expectedScore {
		t.Errorf("Expected score %f for this week's project, got %f", expectedScore, score)
	}

	// Test: project used 15 days ago gets 50x multiplier (this month)
	pd.frecency.Projects["/month/project"] = ProjectUsage{
		UseCount:   10,
		LastUsedAt: time.Now().Add(-15 * 24 * time.Hour),
	}
	score = pd.calculateFrecencyScore("/month/project")
	expectedScore = 10.0 * 50 // 10 uses * 50 (this month multiplier)
	if score != expectedScore {
		t.Errorf("Expected score %f for this month's project, got %f", expectedScore, score)
	}

	// Test: project used 60 days ago gets 30x multiplier (this quarter)
	pd.frecency.Projects["/quarter/project"] = ProjectUsage{
		UseCount:   2,
		LastUsedAt: time.Now().Add(-60 * 24 * time.Hour),
	}
	score = pd.calculateFrecencyScore("/quarter/project")
	expectedScore = 2.0 * 30 // 2 uses * 30 (this quarter multiplier)
	if score != expectedScore {
		t.Errorf("Expected score %f for this quarter's project, got %f", expectedScore, score)
	}

	// Test: project used 120 days ago gets 10x multiplier (older)
	pd.frecency.Projects["/old/project"] = ProjectUsage{
		UseCount:   8,
		LastUsedAt: time.Now().Add(-120 * 24 * time.Hour),
	}
	score = pd.calculateFrecencyScore("/old/project")
	expectedScore = 8.0 * 10 // 8 uses * 10 (older multiplier)
	if score != expectedScore {
		t.Errorf("Expected score %f for old project, got %f", expectedScore, score)
	}
}

// TestRecordUsage tests usage recording and persistence
func TestRecordUsage(t *testing.T) {
	tmpDir := t.TempDir()
	frecencyPath := filepath.Join(tmpDir, "frecency.json")

	pd := &ProjectDiscovery{
		frecencyPath: frecencyPath,
		frecency:     &FrecencyData{Projects: make(map[string]ProjectUsage)},
		configPath:   filepath.Join(tmpDir, "config.toml"),
	}

	projectPath := "/my/project"

	// Record first usage
	err := pd.RecordUsage(projectPath)
	if err != nil {
		t.Fatalf("RecordUsage failed: %v", err)
	}

	// Verify usage was recorded
	usage := pd.frecency.Projects[projectPath]
	if usage.UseCount != 1 {
		t.Errorf("Expected UseCount 1, got %d", usage.UseCount)
	}
	if usage.LastUsedAt.IsZero() {
		t.Error("Expected LastUsedAt to be set")
	}

	// Record another usage
	err = pd.RecordUsage(projectPath)
	if err != nil {
		t.Fatalf("RecordUsage failed: %v", err)
	}

	usage = pd.frecency.Projects[projectPath]
	if usage.UseCount != 2 {
		t.Errorf("Expected UseCount 2, got %d", usage.UseCount)
	}

	// Verify file was written
	data, err := os.ReadFile(frecencyPath)
	if err != nil {
		t.Fatalf("Failed to read frecency file: %v", err)
	}

	var savedFrecency FrecencyData
	if err := json.Unmarshal(data, &savedFrecency); err != nil {
		t.Fatalf("Failed to parse frecency file: %v", err)
	}

	if savedFrecency.Projects[projectPath].UseCount != 2 {
		t.Errorf("Expected persisted UseCount 2, got %d", savedFrecency.Projects[projectPath].UseCount)
	}
}

// TestLoadFrecency tests loading frecency data from disk
func TestLoadFrecency(t *testing.T) {
	tmpDir := t.TempDir()
	frecencyPath := filepath.Join(tmpDir, "frecency.json")

	// Create a frecency file with test data
	testData := FrecencyData{
		Projects: map[string]ProjectUsage{
			"/project/a": {UseCount: 5, LastUsedAt: time.Now()},
			"/project/b": {UseCount: 3, LastUsedAt: time.Now().Add(-24 * time.Hour)},
		},
	}
	data, _ := json.Marshal(testData)
	os.WriteFile(frecencyPath, data, 0600)

	// Create ProjectDiscovery and load
	pd := &ProjectDiscovery{
		frecencyPath: frecencyPath,
		frecency:     &FrecencyData{Projects: make(map[string]ProjectUsage)},
		configPath:   filepath.Join(tmpDir, "config.toml"),
	}
	pd.loadFrecency()

	// Verify data was loaded
	if len(pd.frecency.Projects) != 2 {
		t.Errorf("Expected 2 projects, got %d", len(pd.frecency.Projects))
	}

	if pd.frecency.Projects["/project/a"].UseCount != 5 {
		t.Errorf("Expected UseCount 5 for project/a, got %d", pd.frecency.Projects["/project/a"].UseCount)
	}
}

// TestIsProject tests project detection logic
func TestIsProject(t *testing.T) {
	tmpDir := t.TempDir()

	pd := &ProjectDiscovery{
		frecencyPath: filepath.Join(tmpDir, "frecency.json"),
		frecency:     &FrecencyData{Projects: make(map[string]ProjectUsage)},
		configPath:   filepath.Join(tmpDir, "config.toml"),
	}

	// Create test directories
	emptyDir := filepath.Join(tmpDir, "empty")
	os.MkdirAll(emptyDir, 0755)

	gitProject := filepath.Join(tmpDir, "git-project")
	os.MkdirAll(filepath.Join(gitProject, ".git"), 0755)

	npmProject := filepath.Join(tmpDir, "npm-project")
	os.MkdirAll(npmProject, 0755)
	os.WriteFile(filepath.Join(npmProject, "package.json"), []byte("{}"), 0644)

	goProject := filepath.Join(tmpDir, "go-project")
	os.MkdirAll(goProject, 0755)
	os.WriteFile(filepath.Join(goProject, "go.mod"), []byte("module test"), 0644)

	rustProject := filepath.Join(tmpDir, "rust-project")
	os.MkdirAll(rustProject, 0755)
	os.WriteFile(filepath.Join(rustProject, "Cargo.toml"), []byte("[package]"), 0644)

	pythonProject := filepath.Join(tmpDir, "python-project")
	os.MkdirAll(pythonProject, 0755)
	os.WriteFile(filepath.Join(pythonProject, "pyproject.toml"), []byte("[project]"), 0644)

	claudeProject := filepath.Join(tmpDir, "claude-project")
	os.MkdirAll(claudeProject, 0755)
	os.WriteFile(filepath.Join(claudeProject, "CLAUDE.md"), []byte("# Claude"), 0644)

	tests := []struct {
		name     string
		path     string
		expected bool
	}{
		{"empty directory", emptyDir, false},
		{"git project", gitProject, true},
		{"npm project", npmProject, true},
		{"go project", goProject, true},
		{"rust project", rustProject, true},
		{"python project", pythonProject, true},
		{"claude project", claudeProject, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := pd.isProject(tt.path)
			if result != tt.expected {
				t.Errorf("isProject(%s) = %v, want %v", tt.path, result, tt.expected)
			}
		})
	}
}

// TestDiscoverProjects tests the full project discovery flow
func TestDiscoverProjects(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a scan path with some projects
	scanPath := filepath.Join(tmpDir, "code")
	os.MkdirAll(scanPath, 0755)

	// Create test projects
	project1 := filepath.Join(scanPath, "project1")
	os.MkdirAll(project1, 0755)
	os.WriteFile(filepath.Join(project1, "go.mod"), []byte("module p1"), 0644)

	project2 := filepath.Join(scanPath, "project2")
	os.MkdirAll(project2, 0755)
	os.WriteFile(filepath.Join(project2, "package.json"), []byte("{}"), 0644)

	// Create a nested project
	nestedPath := filepath.Join(scanPath, "org", "project3")
	os.MkdirAll(nestedPath, 0755)
	os.WriteFile(filepath.Join(nestedPath, "Cargo.toml"), []byte("[package]"), 0644)

	// Create node_modules (should be ignored)
	nodeModules := filepath.Join(scanPath, "node_modules", "somepkg")
	os.MkdirAll(nodeModules, 0755)
	os.WriteFile(filepath.Join(nodeModules, "package.json"), []byte("{}"), 0644)

	// Create a hidden directory (should be ignored)
	hiddenDir := filepath.Join(scanPath, ".hidden-project")
	os.MkdirAll(hiddenDir, 0755)
	os.WriteFile(filepath.Join(hiddenDir, "go.mod"), []byte("module hidden"), 0644)

	// Create config.toml with scan_paths
	configPath := filepath.Join(tmpDir, "config.toml")
	configContent := `
[project_discovery]
scan_paths = ["` + scanPath + `"]
max_depth = 2
ignore_patterns = ["node_modules", ".git", "vendor"]
`
	os.WriteFile(configPath, []byte(configContent), 0644)

	pd := &ProjectDiscovery{
		frecencyPath: filepath.Join(tmpDir, "frecency.json"),
		frecency:     &FrecencyData{Projects: make(map[string]ProjectUsage)},
		configPath:   configPath,
	}

	// Discover projects (no existing sessions)
	projects, err := pd.DiscoverProjects([]SessionInfo{})
	if err != nil {
		t.Fatalf("DiscoverProjects failed: %v", err)
	}

	// Should find 3 projects (project1, project2, project3)
	// node_modules and hidden should be ignored
	if len(projects) != 3 {
		t.Errorf("Expected 3 projects, got %d", len(projects))
		for _, p := range projects {
			t.Logf("Found: %s", p.Path)
		}
	}

	// Verify node_modules was ignored
	for _, p := range projects {
		if filepath.Base(p.Path) == "somepkg" {
			t.Error("node_modules project should have been ignored")
		}
	}

	// Verify hidden was ignored
	for _, p := range projects {
		if filepath.Base(p.Path) == ".hidden-project" {
			t.Error("hidden project should have been ignored")
		}
	}
}

// TestDiscoverProjectsWithSessions tests session boosting
func TestDiscoverProjectsWithSessions(t *testing.T) {
	tmpDir := t.TempDir()

	pd := &ProjectDiscovery{
		frecencyPath: filepath.Join(tmpDir, "frecency.json"),
		frecency:     &FrecencyData{Projects: make(map[string]ProjectUsage)},
		configPath:   filepath.Join(tmpDir, "config.toml"), // No config = no scan paths
	}

	// Create sessions
	sessions := []SessionInfo{
		{ID: "sess1", ProjectPath: "/projects/api", Tool: "claude"},
		{ID: "sess2", ProjectPath: "/projects/web", Tool: "gemini"},
	}

	projects, err := pd.DiscoverProjects(sessions)
	if err != nil {
		t.Fatalf("DiscoverProjects failed: %v", err)
	}

	// Should find 2 projects from sessions
	if len(projects) != 2 {
		t.Errorf("Expected 2 projects, got %d", len(projects))
	}

	// All should have sessions
	for _, p := range projects {
		if !p.HasSession {
			t.Errorf("Project %s should have HasSession=true", p.Path)
		}
	}

	// Session projects should have boosted score (1000+)
	for _, p := range projects {
		if p.Score < 1000 {
			t.Errorf("Session project %s should have score >= 1000, got %f", p.Path, p.Score)
		}
	}
}

// TestGetSettings tests config loading with defaults
func TestGetSettings(t *testing.T) {
	tmpDir := t.TempDir()

	t.Run("no config file returns defaults", func(t *testing.T) {
		pd := &ProjectDiscovery{
			frecencyPath: filepath.Join(tmpDir, "frecency.json"),
			frecency:     &FrecencyData{Projects: make(map[string]ProjectUsage)},
			configPath:   filepath.Join(tmpDir, "nonexistent.toml"),
		}

		settings := pd.getSettings()

		if settings.MaxDepth != 2 {
			t.Errorf("Expected default MaxDepth 2, got %d", settings.MaxDepth)
		}
		if len(settings.ScanPaths) != 0 {
			t.Errorf("Expected empty ScanPaths, got %v", settings.ScanPaths)
		}
		if len(settings.IgnorePatterns) == 0 {
			t.Error("Expected default IgnorePatterns")
		}
	})

	t.Run("config file overrides defaults", func(t *testing.T) {
		configPath := filepath.Join(tmpDir, "config.toml")
		configContent := `
[project_discovery]
scan_paths = ["/custom/path"]
max_depth = 3
ignore_patterns = ["custom_ignore"]
`
		os.WriteFile(configPath, []byte(configContent), 0644)

		pd := &ProjectDiscovery{
			frecencyPath: filepath.Join(tmpDir, "frecency.json"),
			frecency:     &FrecencyData{Projects: make(map[string]ProjectUsage)},
			configPath:   configPath,
		}

		settings := pd.getSettings()

		if settings.MaxDepth != 3 {
			t.Errorf("Expected MaxDepth 3, got %d", settings.MaxDepth)
		}
		if len(settings.ScanPaths) != 1 || settings.ScanPaths[0] != "/custom/path" {
			t.Errorf("Expected ScanPaths [/custom/path], got %v", settings.ScanPaths)
		}
		if len(settings.IgnorePatterns) != 1 || settings.IgnorePatterns[0] != "custom_ignore" {
			t.Errorf("Expected IgnorePatterns [custom_ignore], got %v", settings.IgnorePatterns)
		}
	})

	t.Run("tilde expansion in scan paths", func(t *testing.T) {
		configPath := filepath.Join(tmpDir, "config-tilde.toml")
		configContent := `
[project_discovery]
scan_paths = ["~/code", "~/projects"]
`
		os.WriteFile(configPath, []byte(configContent), 0644)

		pd := &ProjectDiscovery{
			frecencyPath: filepath.Join(tmpDir, "frecency.json"),
			frecency:     &FrecencyData{Projects: make(map[string]ProjectUsage)},
			configPath:   configPath,
		}

		settings := pd.getSettings()
		home, _ := os.UserHomeDir()

		for _, path := range settings.ScanPaths {
			if path[0] == '~' {
				t.Errorf("Tilde was not expanded in path: %s", path)
			}
			if !filepath.IsAbs(path) {
				t.Errorf("Path should be absolute: %s", path)
			}
			if home != "" && len(path) > len(home) && path[:len(home)] != home {
				t.Errorf("Path should start with home directory: %s", path)
			}
		}
	})
}
