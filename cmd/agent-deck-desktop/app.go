package main

import (
	"context"
)

// Version is set at build time via ldflags.
var Version = "0.1.0-dev"

// App struct holds the application state.
type App struct {
	ctx      context.Context
	terminal *Terminal
	tmux     *TmuxManager
}

// NewApp creates a new App application struct.
func NewApp() *App {
	return &App{
		terminal: NewTerminal(),
		tmux:     NewTmuxManager(),
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
func (a *App) AttachSession(tmuxSession string, cols, rows int) error {
	return a.terminal.AttachTmux(tmuxSession, cols, rows)
}

// GetScrollback returns the scrollback buffer for a tmux session.
func (a *App) GetScrollback(tmuxSession string) (string, error) {
	return a.tmux.GetScrollback(tmuxSession, 10000)
}

// SessionExists checks if a tmux session exists.
func (a *App) SessionExists(tmuxSession string) bool {
	return a.tmux.SessionExists(tmuxSession)
}
