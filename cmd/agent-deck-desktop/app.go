package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Version is set at build time via ldflags.
var Version = "0.1.0-dev"

// App struct holds the application state.
type App struct {
	ctx              context.Context
	terminal         *Terminal
	tmux             *TmuxManager
	projectDiscovery *ProjectDiscovery
	quickLaunch      *QuickLaunchManager
	launchConfig     *LaunchConfigManager
}

// NewApp creates a new App application struct.
func NewApp() *App {
	return &App{
		terminal:         NewTerminal(),
		tmux:             NewTmuxManager(),
		projectDiscovery: NewProjectDiscovery(),
		quickLaunch:      NewQuickLaunchManager(),
		launchConfig:     NewLaunchConfigManager(),
	}
}

// startup is called when the app starts.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.terminal.SetContext(ctx)
}

// GetVersion returns the application version.
func (a *App) GetVersion() string {
	return Version
}

// StartTerminal spawns the shell with initial dimensions.
func (a *App) StartTerminal(cols, rows int) error {
	return a.terminal.Start(cols, rows)
}

// WriteTerminal sends data to the PTY.
func (a *App) WriteTerminal(data string) error {
	return a.terminal.Write(data)
}

// ResizeTerminal changes the PTY dimensions.
func (a *App) ResizeTerminal(cols, rows int) error {
	return a.terminal.Resize(cols, rows)
}

// CloseTerminal terminates the PTY.
func (a *App) CloseTerminal() error {
	return a.terminal.Close()
}

// ListSessions returns all Agent Deck sessions.
func (a *App) ListSessions() ([]SessionInfo, error) {
	return a.tmux.ListSessions()
}

// AttachSession attaches to an existing tmux session (direct mode, no history preload).
// DEPRECATED: Use StartTmuxSession instead for the hybrid approach with scrollback support.
func (a *App) AttachSession(tmuxSession string, cols, rows int) error {
	return a.terminal.AttachTmux(tmuxSession, cols, rows)
}

// StartTmuxSession connects to a tmux session using the hybrid approach:
// 1. Fetches and emits sanitized history via terminal:history event
// 2. Attaches PTY for live streaming
//
// This is the preferred method for connecting to tmux sessions as it provides
// full scrollback history while maintaining real-time streaming.
func (a *App) StartTmuxSession(tmuxSession string, cols, rows int) error {
	return a.terminal.StartTmuxSession(tmuxSession, cols, rows)
}

// GetScrollback returns the scrollback buffer for a tmux session.
func (a *App) GetScrollback(tmuxSession string) (string, error) {
	return a.tmux.GetScrollback(tmuxSession, 10000)
}

// SessionExists checks if a tmux session exists.
func (a *App) SessionExists(tmuxSession string) bool {
	return a.tmux.SessionExists(tmuxSession)
}

// DiscoverProjects finds all projects from configured paths and existing sessions.
func (a *App) DiscoverProjects() ([]ProjectInfo, error) {
	sessions, err := a.ListSessions()
	if err != nil {
		return nil, err
	}
	return a.projectDiscovery.DiscoverProjects(sessions)
}

// RecordProjectUsage records that a project was used (for frecency scoring).
func (a *App) RecordProjectUsage(projectPath string) error {
	return a.projectDiscovery.RecordUsage(projectPath)
}

// CreateSession creates a new tmux session and launches the specified AI tool.
// If configKey is provided, the launch config settings will be applied.
func (a *App) CreateSession(projectPath, title, tool, configKey string) (SessionInfo, error) {
	return a.tmux.CreateSession(projectPath, title, tool, configKey)
}

// GetQuickLaunchFavorites returns all quick launch favorites.
func (a *App) GetQuickLaunchFavorites() ([]QuickLaunchFavorite, error) {
	return a.quickLaunch.GetFavorites()
}

// AddQuickLaunchFavorite adds a project to quick launch.
func (a *App) AddQuickLaunchFavorite(name, path, tool string) error {
	return a.quickLaunch.AddFavorite(name, path, tool)
}

// RemoveQuickLaunchFavorite removes a project from quick launch.
func (a *App) RemoveQuickLaunchFavorite(path string) error {
	return a.quickLaunch.RemoveFavorite(path)
}

// UpdateQuickLaunchShortcut updates the keyboard shortcut for a favorite.
func (a *App) UpdateQuickLaunchShortcut(path, shortcut string) error {
	return a.quickLaunch.UpdateShortcut(path, shortcut)
}

// GetQuickLaunchBarVisibility returns whether the quick launch bar should be shown.
func (a *App) GetQuickLaunchBarVisibility() (bool, error) {
	return a.quickLaunch.GetBarVisibility()
}

// SetQuickLaunchBarVisibility sets whether the quick launch bar should be shown.
func (a *App) SetQuickLaunchBarVisibility(show bool) error {
	return a.quickLaunch.SetBarVisibility(show)
}

// UpdateQuickLaunchFavoriteName updates the display name for a favorite.
func (a *App) UpdateQuickLaunchFavoriteName(path, name string) error {
	return a.quickLaunch.UpdateFavoriteName(path, name)
}

// MarkSessionAccessed updates the last_accessed_at timestamp for a session.
// Call this when a user selects/opens a session to keep the list sorted by recency.
func (a *App) MarkSessionAccessed(sessionID string) error {
	return a.tmux.MarkSessionAccessed(sessionID)
}

// GetGitBranch returns the current git branch for a given directory.
// Returns empty string if not a git repository or on error.
func (a *App) GetGitBranch(projectPath string) string {
	projectPath = expandHome(projectPath)

	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = projectPath
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// IsGitWorktree returns true if the given path is a git worktree (not the main clone).
// Worktrees have a .git file pointing to the main repo, not a .git directory.
func (a *App) IsGitWorktree(projectPath string) bool {
	projectPath = expandHome(projectPath)

	// Check if .git is a file (worktree) vs directory (main clone)
	gitPath := filepath.Join(projectPath, ".git")
	info, err := os.Stat(gitPath)
	if err != nil {
		return false
	}
	// If .git is a file, it's a worktree (contains "gitdir: /path/to/main/.git/worktrees/...")
	return !info.IsDir()
}

// expandHome expands ~ to the user's home directory.
func expandHome(path string) string {
	if strings.HasPrefix(path, "~") {
		home, err := exec.Command("sh", "-c", "echo $HOME").Output()
		if err == nil {
			path = filepath.Join(strings.TrimSpace(string(home)), path[1:])
		}
	}
	return path
}

// GetProjectRoots returns the configured project scan paths.
// Used by the frontend to compute relative paths for display.
func (a *App) GetProjectRoots() []string {
	settings := a.projectDiscovery.getSettings()
	return settings.ScanPaths
}

// ==================== Launch Config Methods ====================

// GetLaunchConfigs returns all launch configurations.
func (a *App) GetLaunchConfigs() ([]LaunchConfigInfo, error) {
	return a.launchConfig.GetLaunchConfigs()
}

// GetLaunchConfigsForTool returns launch configs for a specific tool.
func (a *App) GetLaunchConfigsForTool(tool string) ([]LaunchConfigInfo, error) {
	return a.launchConfig.GetLaunchConfigsForTool(tool)
}

// GetLaunchConfig returns a single launch config by key.
func (a *App) GetLaunchConfig(key string) (*LaunchConfigInfo, error) {
	return a.launchConfig.GetLaunchConfig(key)
}

// GetDefaultLaunchConfig returns the default config for a tool, or nil if none set.
func (a *App) GetDefaultLaunchConfig(tool string) (*LaunchConfigInfo, error) {
	return a.launchConfig.GetDefaultLaunchConfig(tool)
}

// SaveLaunchConfig creates or updates a launch configuration.
func (a *App) SaveLaunchConfig(key, name, tool, description string, dangerousMode bool, mcpConfigPath string, extraArgs []string, isDefault bool) error {
	return a.launchConfig.SaveLaunchConfig(key, name, tool, description, dangerousMode, mcpConfigPath, extraArgs, isDefault)
}

// DeleteLaunchConfig removes a launch configuration.
func (a *App) DeleteLaunchConfig(key string) error {
	return a.launchConfig.DeleteLaunchConfig(key)
}

// ValidateMCPConfigPath validates an MCP config path and returns the MCP names.
func (a *App) ValidateMCPConfigPath(path string) ([]string, error) {
	return a.launchConfig.ValidateMCPConfigPath(path)
}

// GenerateConfigKey generates a unique config key from tool and name.
func (a *App) GenerateConfigKey(tool, name string) string {
	return a.launchConfig.GenerateConfigKey(tool, name)
}
