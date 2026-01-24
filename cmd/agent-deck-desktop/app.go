package main

import (
	"context"
	"fmt"
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
	terminals        *TerminalManager // Manages multiple terminals for multi-pane support
	tmux             *TmuxManager
	projectDiscovery *ProjectDiscovery
	quickLaunch      *QuickLaunchManager
	launchConfig     *LaunchConfigManager
	desktopSettings  *DesktopSettingsManager
	savedLayouts     *SavedLayoutsManager
	sshBridge        *SSHBridge
}

// NewApp creates a new App application struct.
func NewApp() *App {
	return &App{
		terminals:        NewTerminalManager(),
		tmux:             NewTmuxManager(),
		projectDiscovery: NewProjectDiscovery(),
		quickLaunch:      NewQuickLaunchManager(),
		launchConfig:     NewLaunchConfigManager(),
		desktopSettings:  NewDesktopSettingsManager(),
		savedLayouts:     NewSavedLayoutsManager(),
		sshBridge:        NewSSHBridge(),
	}
}

// startup is called when the app starts.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.terminals.SetContext(ctx)
	a.terminals.SetSSHBridge(a.sshBridge)
}

// shutdown is called when the app is closing.
func (a *App) shutdown(ctx context.Context) {
	// Clean up SSH connections
	if a.sshBridge != nil {
		a.sshBridge.CloseAll()
	}
	a.terminals.CloseAll()
}

// GetVersion returns the application version.
func (a *App) GetVersion() string {
	return Version
}

// StartTerminal spawns the shell with initial dimensions for a session.
func (a *App) StartTerminal(sessionID string, cols, rows int) error {
	t := a.terminals.GetOrCreate(sessionID)
	return t.Start(cols, rows)
}

// WriteTerminal sends data to the PTY for a session.
func (a *App) WriteTerminal(sessionID, data string) error {
	t := a.terminals.Get(sessionID)
	if t == nil {
		return fmt.Errorf("terminal %s not found", sessionID)
	}
	return t.Write(data)
}

// ResizeTerminal changes the PTY dimensions for a session.
func (a *App) ResizeTerminal(sessionID string, cols, rows int) error {
	t := a.terminals.Get(sessionID)
	if t == nil {
		return fmt.Errorf("terminal %s not found", sessionID)
	}
	return t.Resize(cols, rows)
}

// CloseTerminal terminates the PTY for a session.
func (a *App) CloseTerminal(sessionID string) error {
	return a.terminals.Close(sessionID)
}

// ListSessions returns all Agent Deck sessions.
func (a *App) ListSessions() ([]SessionInfo, error) {
	return a.tmux.ListSessions()
}

// AttachSession attaches to an existing tmux session (direct mode, no history preload).
// DEPRECATED: Use StartTmuxSession instead for the hybrid approach with scrollback support.
func (a *App) AttachSession(sessionID, tmuxSession string, cols, rows int) error {
	t := a.terminals.GetOrCreate(sessionID)
	return t.AttachTmux(tmuxSession, cols, rows)
}

// StartTmuxSession connects to a tmux session using the hybrid approach:
// 1. Fetches and emits sanitized history via terminal:history event
// 2. Attaches PTY for live streaming
//
// This is the preferred method for connecting to tmux sessions as it provides
// full scrollback history while maintaining real-time streaming.
// The sessionID is used to identify which pane/terminal this belongs to.
func (a *App) StartTmuxSession(sessionID, tmuxSession string, cols, rows int) error {
	t := a.terminals.GetOrCreate(sessionID)
	return t.StartTmuxSession(tmuxSession, cols, rows)
}

// StartRemoteTmuxSession connects to a tmux session on a remote host via SSH.
// Uses SSH polling for display updates.
//
// Parameters:
//   - sessionID: identifier for this terminal pane
//   - hostID: SSH host identifier from config.toml [ssh_hosts.X]
//   - tmuxSession: tmux session name on the remote host
//   - cols, rows: initial terminal dimensions
func (a *App) StartRemoteTmuxSession(sessionID, hostID, tmuxSession string, cols, rows int) error {
	t := a.terminals.GetOrCreate(sessionID)
	return t.StartRemoteTmuxSession(hostID, tmuxSession, cols, rows)
}

// GetScrollback returns the scrollback buffer for a tmux session.
func (a *App) GetScrollback(tmuxSession string) (string, error) {
	return a.tmux.GetScrollback(tmuxSession, 10000)
}

// RefreshScrollback fetches fresh scrollback from the tmux session for a given terminal.
// Called by frontend after resize to bypass xterm.js reflow issues with box-drawing chars.
func (a *App) RefreshScrollback(sessionID string) (string, error) {
	t := a.terminals.Get(sessionID)
	if t == nil {
		return "", nil
	}
	return t.GetScrollback()
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

// UpdateSessionCustomLabel updates the custom label for a session.
// Pass an empty string to remove the custom label.
func (a *App) UpdateSessionCustomLabel(sessionID, customLabel string) error {
	return a.tmux.UpdateSessionCustomLabel(sessionID, customLabel)
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

// LogFrontendDiagnostic writes diagnostic info from frontend to the debug log file.
// This allows Claude to read diagnostic info that would otherwise only be in browser console.
func (a *App) LogFrontendDiagnostic(message string) {
	// LogDiagnostic writes directly to the debug log file and doesn't need a session
	LogDiagnostic(message)
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

// ==================== Desktop Settings Methods ====================

// GetDesktopTheme returns the current desktop theme preference.
// Returns "dark", "light", or "auto".
func (a *App) GetDesktopTheme() string {
	theme, err := a.desktopSettings.GetTheme()
	if err != nil {
		return "dark"
	}
	return theme
}

// SetDesktopTheme sets the desktop theme preference.
// Valid values: "dark", "light", "auto".
func (a *App) SetDesktopTheme(theme string) error {
	return a.desktopSettings.SetTheme(theme)
}

// GetSoftNewlineMode returns the current soft newline key preference.
// Returns: "shift_enter", "alt_enter", "both", or "disabled".
func (a *App) GetSoftNewlineMode() string {
	mode, err := a.desktopSettings.GetSoftNewline()
	if err != nil {
		return "both"
	}
	return mode
}

// SetSoftNewlineMode sets the soft newline key preference.
// Valid values: "shift_enter", "alt_enter", "both", "disabled".
func (a *App) SetSoftNewlineMode(mode string) error {
	return a.desktopSettings.SetSoftNewline(mode)
}

// GetTerminalSettings returns all terminal settings for the frontend.
func (a *App) GetTerminalSettings() map[string]interface{} {
	config, err := a.desktopSettings.GetTerminalConfig()
	if err != nil {
		return map[string]interface{}{
			"softNewline": "both",
			"fontSize":    14,
		}
	}
	return map[string]interface{}{
		"softNewline": config.SoftNewline,
		"fontSize":    config.FontSize,
	}
}

// GetFontSize returns the terminal font size (8-32, default 14).
func (a *App) GetFontSize() int {
	size, err := a.desktopSettings.GetFontSize()
	if err != nil {
		return 14
	}
	return size
}

// SetFontSize sets the terminal font size (clamped to 8-32).
func (a *App) SetFontSize(size int) error {
	return a.desktopSettings.SetFontSize(size)
}

// ==================== SSH Remote Session Methods ====================

// TestSSHConnection tests if a remote host is reachable.
// hostID should match a configured [ssh_hosts.X] section in config.toml.
func (a *App) TestSSHConnection(hostID string) error {
	return a.sshBridge.TestConnection(hostID)
}

// GetSSHHostStatus returns connection status for all configured SSH hosts.
func (a *App) GetSSHHostStatus() []SSHHostStatus {
	statuses := a.sshBridge.GetHostStatus()
	result := make([]SSHHostStatus, len(statuses))
	for i, s := range statuses {
		var lastError string
		if s.LastError != nil {
			lastError = s.LastError.Error()
		}
		result[i] = SSHHostStatus{
			HostID:    s.HostID,
			Connected: s.Connected,
			LastError: lastError,
		}
	}
	return result
}

// ListSSHHosts returns all configured SSH host IDs.
func (a *App) ListSSHHosts() []string {
	return a.sshBridge.ListConfiguredHosts()
}

// SSHHostStatus represents the connection status of an SSH host.
type SSHHostStatus struct {
	HostID    string `json:"hostId"`
	Connected bool   `json:"connected"`
	LastError string `json:"lastError,omitempty"`
}

// ==================== Saved Layouts Methods ====================

// GetSavedLayouts returns all saved layout templates.
func (a *App) GetSavedLayouts() ([]SavedLayout, error) {
	return a.savedLayouts.GetSavedLayouts()
}

// SaveLayout saves a new layout or updates an existing one.
// Returns the saved layout with ID filled in.
func (a *App) SaveLayout(layout SavedLayout) (*SavedLayout, error) {
	return a.savedLayouts.SaveLayout(layout)
}

// DeleteSavedLayout removes a layout by ID.
func (a *App) DeleteSavedLayout(id string) error {
	return a.savedLayouts.DeleteLayout(id)
}

// GetSavedLayoutByID returns a single layout by ID.
func (a *App) GetSavedLayoutByID(id string) (*SavedLayout, error) {
	return a.savedLayouts.GetLayoutByID(id)
}
