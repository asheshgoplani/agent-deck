package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// ProjectInfo represents a discovered project for the frontend
type ProjectInfo struct {
	Path       string  `json:"path"`
	Name       string  `json:"name"`
	Score      float64 `json:"score"`      // Frecency score
	HasSession bool    `json:"hasSession"` // Has existing session?
	Tool       string  `json:"tool"`       // Tool if session exists
	SessionID  string  `json:"sessionId"`  // Session ID if exists
}

// ProjectDiscoverySettings defines settings for discovering projects
type ProjectDiscoverySettings struct {
	ScanPaths      []string `toml:"scan_paths"`
	MaxDepth       int      `toml:"max_depth"`
	IgnorePatterns []string `toml:"ignore_patterns"`
}

// FrecencyData stores project usage history for frecency scoring
type FrecencyData struct {
	Projects map[string]ProjectUsage `json:"projects"`
}

// ProjectUsage tracks usage stats for a single project
type ProjectUsage struct {
	UseCount   int       `json:"useCount"`
	LastUsedAt time.Time `json:"lastUsedAt"`
}

// ProjectDiscovery handles project scanning and ranking
type ProjectDiscovery struct {
	frecencyPath string
	frecency     *FrecencyData
	configPath   string
}

// NewProjectDiscovery creates a new project discovery instance
func NewProjectDiscovery() *ProjectDiscovery {
	home, _ := os.UserHomeDir()
	frecencyPath := filepath.Join(home, ".agent-deck", "frecency.json")
	configPath := filepath.Join(home, ".agent-deck", "config.toml")

	pd := &ProjectDiscovery{
		frecencyPath: frecencyPath,
		frecency:     &FrecencyData{Projects: make(map[string]ProjectUsage)},
		configPath:   configPath,
	}

	// Load frecency data
	pd.loadFrecency()

	return pd
}

// loadFrecency loads frecency data from disk
func (pd *ProjectDiscovery) loadFrecency() {
	data, err := os.ReadFile(pd.frecencyPath)
	if err != nil {
		return
	}

	var frecency FrecencyData
	if err := json.Unmarshal(data, &frecency); err != nil {
		return
	}

	if frecency.Projects == nil {
		frecency.Projects = make(map[string]ProjectUsage)
	}

	pd.frecency = &frecency
}

// saveFrecency saves frecency data to disk
func (pd *ProjectDiscovery) saveFrecency() error {
	data, err := json.MarshalIndent(pd.frecency, "", "  ")
	if err != nil {
		return err
	}

	// Ensure directory exists
	dir := filepath.Dir(pd.frecencyPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	return os.WriteFile(pd.frecencyPath, data, 0600)
}

// RecordUsage records that a project was used
func (pd *ProjectDiscovery) RecordUsage(projectPath string) error {
	usage := pd.frecency.Projects[projectPath]
	usage.UseCount++
	usage.LastUsedAt = time.Now()
	pd.frecency.Projects[projectPath] = usage

	return pd.saveFrecency()
}

// getSettings loads project discovery settings from config.toml
func (pd *ProjectDiscovery) getSettings() ProjectDiscoverySettings {
	defaults := ProjectDiscoverySettings{
		ScanPaths:      []string{},
		MaxDepth:       2,
		IgnorePatterns: []string{"node_modules", ".git", "vendor", "__pycache__", ".venv", "dist", "build"},
	}

	// Try to load from config.toml
	data, err := os.ReadFile(pd.configPath)
	if err != nil {
		return defaults
	}

	// Parse just the project_discovery section
	var config struct {
		ProjectDiscovery ProjectDiscoverySettings `toml:"project_discovery"`
	}

	if err := toml.Unmarshal(data, &config); err != nil {
		return defaults
	}

	settings := config.ProjectDiscovery

	// Apply defaults for unset values
	if settings.MaxDepth <= 0 {
		settings.MaxDepth = defaults.MaxDepth
	}
	if len(settings.IgnorePatterns) == 0 {
		settings.IgnorePatterns = defaults.IgnorePatterns
	}

	// Expand ~ in scan paths
	home, _ := os.UserHomeDir()
	expandedPaths := make([]string, 0, len(settings.ScanPaths))
	for _, p := range settings.ScanPaths {
		if strings.HasPrefix(p, "~/") {
			p = filepath.Join(home, p[2:])
		}
		expandedPaths = append(expandedPaths, p)
	}
	settings.ScanPaths = expandedPaths

	return settings
}

// calculateFrecencyScore calculates the frecency score for a project
func (pd *ProjectDiscovery) calculateFrecencyScore(projectPath string) float64 {
	usage, ok := pd.frecency.Projects[projectPath]
	if !ok {
		return 0
	}

	// Calculate recency multiplier
	now := time.Now()
	daysSince := now.Sub(usage.LastUsedAt).Hours() / 24

	var recencyMultiplier float64
	switch {
	case daysSince < 1:
		recencyMultiplier = 100 // Today
	case daysSince < 7:
		recencyMultiplier = 70 // This week
	case daysSince < 30:
		recencyMultiplier = 50 // This month
	case daysSince < 90:
		recencyMultiplier = 30 // This quarter
	default:
		recencyMultiplier = 10 // Older
	}

	return float64(usage.UseCount) * recencyMultiplier
}

// DiscoverProjects finds all projects from configured paths and existing sessions
func (pd *ProjectDiscovery) DiscoverProjects(sessions []SessionInfo) ([]ProjectInfo, error) {
	settings := pd.getSettings()

	// Build a map of existing session paths
	sessionPaths := make(map[string]SessionInfo)
	for _, s := range sessions {
		if s.ProjectPath != "" {
			sessionPaths[s.ProjectPath] = s
		}
	}

	// Collect all discovered projects
	projectMap := make(map[string]*ProjectInfo)

	// Add all projects from existing sessions (highest priority)
	for path, s := range sessionPaths {
		projectMap[path] = &ProjectInfo{
			Path:       path,
			Name:       filepath.Base(path),
			Score:      pd.calculateFrecencyScore(path) + 1000, // Boost existing sessions
			HasSession: true,
			Tool:       s.Tool,
			SessionID:  s.ID,
		}
	}

	// Scan configured paths
	for _, scanPath := range settings.ScanPaths {
		pd.scanDirectory(scanPath, 0, settings.MaxDepth, settings.IgnorePatterns, projectMap)
	}

	// Convert to slice and sort by score
	projects := make([]ProjectInfo, 0, len(projectMap))
	for _, p := range projectMap {
		// Calculate score if not already set
		if p.Score == 0 {
			p.Score = pd.calculateFrecencyScore(p.Path)
		}
		projects = append(projects, *p)
	}

	// Sort by score (descending)
	sort.Slice(projects, func(i, j int) bool {
		return projects[i].Score > projects[j].Score
	})

	return projects, nil
}

// scanDirectory recursively scans for projects
func (pd *ProjectDiscovery) scanDirectory(path string, depth, maxDepth int, ignorePatterns []string, projects map[string]*ProjectInfo) {
	if depth > maxDepth {
		return
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		name := entry.Name()

		// Skip hidden directories and ignored patterns
		if strings.HasPrefix(name, ".") {
			continue
		}

		skip := false
		for _, pattern := range ignorePatterns {
			if name == pattern {
				skip = true
				break
			}
		}
		if skip {
			continue
		}

		subPath := filepath.Join(path, name)

		// Check if this is a project (has .git, package.json, go.mod, etc.)
		if pd.isProject(subPath) {
			if _, exists := projects[subPath]; !exists {
				projects[subPath] = &ProjectInfo{
					Path:       subPath,
					Name:       name,
					Score:      pd.calculateFrecencyScore(subPath),
					HasSession: false,
				}
			}
		}

		// Continue scanning subdirectories
		if depth < maxDepth {
			pd.scanDirectory(subPath, depth+1, maxDepth, ignorePatterns, projects)
		}
	}
}

// isProject checks if a directory looks like a project
func (pd *ProjectDiscovery) isProject(path string) bool {
	projectMarkers := []string{
		".git",
		"package.json",
		"go.mod",
		"Cargo.toml",
		"pyproject.toml",
		"requirements.txt",
		"pom.xml",
		"build.gradle",
		"Makefile",
		".svn",
		"CLAUDE.md", // Agent Deck specific
	}

	for _, marker := range projectMarkers {
		if _, err := os.Stat(filepath.Join(path, marker)); err == nil {
			return true
		}
	}

	return false
}
