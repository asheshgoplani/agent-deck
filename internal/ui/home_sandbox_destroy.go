package ui

import (
	"context"

	"github.com/asheshgoplani/agent-deck/internal/provider"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// sandboxDestroyDeps are the remote and provider calls a TUI sandbox
// destroy makes; tests replace them.
type sandboxDestroyDeps struct {
	listSessions func(ctx context.Context, name string, rc session.RemoteConfig) ([]session.RemoteSessionInfo, error)
	stopSession  func(ctx context.Context, name string, rc session.RemoteConfig, id string) error
	destroy      func(ctx context.Context, name string) error
}

// withInstallProgress wraps install so the install step reports progress.
func withInstallProgress(opts provider.ProvisionOptions, install func(context.Context, string, session.RemoteConfig) error) provider.ProvisionOptions {
	opts.Install = install
	return opts
}
