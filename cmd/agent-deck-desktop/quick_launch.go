package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// QuickLaunchFavorite represents a pinned project
type QuickLaunchFavorite struct {
	Name     string `json:"name" toml:"name"`
	Path     string `json:"path" toml:"path"`
	Tool     string `json:"tool" toml:"tool"`
	Shortcut string `json:"shortcut,omitempty" toml:"shortcut,omitempty"` // For Phase 4
}

// QuickLaunchConfig represents the quick-launch.toml file
type QuickLaunchConfig struct {
	Favorites []QuickLaunchFavorite `toml:"favorites"`
}

// QuickLaunchManager manages quick launch favorites
type QuickLaunchManager struct {
	configPath string
}

// NewQuickLaunchManager creates a new quick launch manager
func NewQuickLaunchManager() *QuickLaunchManager {
	home, _ := os.UserHomeDir()
	return &QuickLaunchManager{
		configPath: filepath.Join(home, ".agent-deck", "quick-launch.toml"),
	}
}

// loadConfig loads the quick launch config from disk
func (qlm *QuickLaunchManager) loadConfig() (*QuickLaunchConfig, error) {
	config := &QuickLaunchConfig{
		Favorites: []QuickLaunchFavorite{},
	}

	data, err := os.ReadFile(qlm.configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return config, nil
		}
		return nil, err
	}

	if err := toml.Unmarshal(data, config); err != nil {
		return nil, err
	}

	// Expand ~ in paths
	home, _ := os.UserHomeDir()
	for i := range config.Favorites {
		if strings.HasPrefix(config.Favorites[i].Path, "~/") {
			config.Favorites[i].Path = filepath.Join(home, config.Favorites[i].Path[2:])
		}
	}

	return config, nil
}

// saveConfig saves the quick launch config to disk
func (qlm *QuickLaunchManager) saveConfig(config *QuickLaunchConfig) error {
	// Ensure directory exists
	dir := filepath.Dir(qlm.configPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	// Create file content with header
	content := "# Quick Launch Favorites\n"
	content += "# Pin projects for instant access from the Quick Launch Bar\n\n"

	// Encode favorites
	for _, fav := range config.Favorites {
		content += "[[favorites]]\n"
		content += "name = \"" + fav.Name + "\"\n"
		content += "path = \"" + fav.Path + "\"\n"
		content += "tool = \"" + fav.Tool + "\"\n"
		if fav.Shortcut != "" {
			content += "shortcut = \"" + fav.Shortcut + "\"\n"
		}
		content += "\n"
	}

	return os.WriteFile(qlm.configPath, []byte(content), 0600)
}

// GetFavorites returns all quick launch favorites
func (qlm *QuickLaunchManager) GetFavorites() ([]QuickLaunchFavorite, error) {
	config, err := qlm.loadConfig()
	if err != nil {
		return nil, err
	}
	return config.Favorites, nil
}

// AddFavorite adds a new favorite to quick launch
func (qlm *QuickLaunchManager) AddFavorite(name, path, tool string) error {
	config, err := qlm.loadConfig()
	if err != nil {
		return err
	}

	// Check if already exists
	for i := range config.Favorites {
		if config.Favorites[i].Path == path {
			// Already exists, update it
			config.Favorites[i].Name = name
			config.Favorites[i].Tool = tool
			return qlm.saveConfig(config)
		}
	}

	// Add new favorite
	config.Favorites = append(config.Favorites, QuickLaunchFavorite{
		Name: name,
		Path: path,
		Tool: tool,
	})

	return qlm.saveConfig(config)
}

// RemoveFavorite removes a favorite by path
func (qlm *QuickLaunchManager) RemoveFavorite(path string) error {
	config, err := qlm.loadConfig()
	if err != nil {
		return err
	}

	// Find and remove
	newFavorites := make([]QuickLaunchFavorite, 0, len(config.Favorites))
	for _, fav := range config.Favorites {
		if fav.Path != path {
			newFavorites = append(newFavorites, fav)
		}
	}

	config.Favorites = newFavorites
	return qlm.saveConfig(config)
}

// UpdateShortcut updates the shortcut for a favorite (for Phase 4)
func (qlm *QuickLaunchManager) UpdateShortcut(path, shortcut string) error {
	config, err := qlm.loadConfig()
	if err != nil {
		return err
	}

	for i := range config.Favorites {
		if config.Favorites[i].Path == path {
			config.Favorites[i].Shortcut = shortcut
			return qlm.saveConfig(config)
		}
	}

	return nil // Not found, no error
}
