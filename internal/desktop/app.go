// Package desktop provides the native desktop app functionality for Agent Deck.
package desktop

import (
	"context"
)

// Version is set at build time via ldflags
var Version = "0.1.0-dev"

// App struct holds the application state
type App struct {
	ctx context.Context
}

// NewApp creates a new App application struct
func NewApp() *App {
	return &App{}
}

// Startup is called when the app starts. The context is saved
// so we can call the runtime methods
func (a *App) Startup(ctx context.Context) {
	a.ctx = ctx
}

// GetVersion returns the application version
func (a *App) GetVersion() string {
	return Version
}
