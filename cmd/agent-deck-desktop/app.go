package main

import (
	"context"
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
}

// NewApp creates a new App application struct.
func NewApp() *App {
	return &App{
		terminal:         NewTerminal(),
		tmux:             NewTmuxManager(),
		projectDiscovery: NewProjectDiscovery(),
		quickLaunch:      NewQuickLaunchManager(),
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

// AttachSession attaches to an existing tmux session.
// DEPRECATED: Use StartTmuxPolling instead for clean scrollback handling.
func (a *App) AttachSession(tmuxSession string, cols, rows int) error {
	return a.terminal.AttachTmux(tmuxSession, cols, rows)
}

// StartTmuxPolling begins polling a tmux session.
// This is the preferred method for attaching to tmux sessions as it
// avoids cursor position conflicts when scrollback is pre-loaded.
func (a *App) StartTmuxPolling(tmuxSession string, cols, rows int) error {
	return a.terminal.StartTmuxPolling(tmuxSession, cols, rows)
}

// SendTmuxInput sends user input to the tmux session.
// Use this instead of WriteTerminal when in tmux polling mode.
func (a *App) SendTmuxInput(data string) error {
	return a.terminal.SendTmuxInput(data)
}

// ResizeTmuxPane resizes the tmux pane.
// Use this instead of ResizeTerminal when in tmux polling mode.
func (a *App) ResizeTmuxPane(cols, rows int) error {
	return a.terminal.ResizeTmuxPane(cols, rows)
}

// IsTmuxPolling returns whether we're in tmux polling mode.
func (a *App) IsTmuxPolling() bool {
	return a.terminal.IsTmuxPolling()
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
func (a *App) CreateSession(projectPath, title, tool string) (SessionInfo, error) {
	return a.tmux.CreateSession(projectPath, title, tool)
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
