package ui

import (
	"context"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// insertKeySender is the local interface the insert-mode dispatch path uses
// to forward keystrokes. It abstracts the local (tmux control-mode client)
// and remote (SSH RPC) implementations behind a single type so #1102's UI
// changes stay platform-independent and unit-testable.
//
// Mirrors tmux.KeySender deliberately — same shape, decoupled package so the
// UI doesn't pull a tmux symbol into its public surface and tests can supply
// in-process fakes without depending on real tmux or SSH.
type insertKeySender interface {
	SendKeys(text string) error
	SendNamedKey(key string) error
	SendEnter() error
	Close() error
}

// insertTargetRef describes the session insert mode is dispatching to. Either
// a local Instance (with its tmux session bound) or a remote
// (remoteName, remoteID) tuple — never both, never neither.
type insertTargetRef struct {
	// Local target. nil for remote sessions.
	local *session.Instance
	// Remote target name (the SSH remote alias, e.g. "windows"). Empty for
	// local sessions. When non-empty, remoteID must also be set.
	remoteName string
	remoteID   string
}

// isRemote reports whether the target is a remote session.
func (r insertTargetRef) isRemote() bool { return r.remoteName != "" }

// defaultInsertOpenKeySender is the production opener wired up by NewHome.
// It selects the local or remote path based on the target, and falls back to
// returning an error so the caller can degrade to per-call SendKeys (#1102).
//
// Note: this is the ONLY place the UI package decides between local and
// remote KeySender backends. Adding a new backend (e.g. Docker-sandboxed) is
// a one-liner here, not a scatter-shot across insert_mode.go.
func defaultInsertOpenKeySender(target insertTargetRef) (insertKeySender, error) {
	if target.isRemote() {
		return openRemoteInsertKeySender(target.remoteName, target.remoteID)
	}
	if target.local == nil {
		return nil, errInsertNoTarget
	}
	tmuxSess := target.local.GetTmuxSession()
	if tmuxSess == nil {
		return nil, errInsertNoTmuxSession
	}
	ks, err := tmuxSess.OpenKeySender()
	if err != nil {
		return nil, err
	}
	return ks, nil
}

// openRemoteInsertKeySender resolves the remote configuration and returns a
// RemoteKeySender bound to (remoteName, sessionID). Separated out so the
// configuration-load + runner construction is unit-testable without
// touching the UI.
//
// #1112 bug 2: prefers the streaming RemoteKeySender (one persistent
// `ssh ... agent-deck session send-keys --stream` subprocess for the
// entire insert-mode session) over the per-call variant. If the stream
// fails to open (older remote agent-deck without --stream support, ssh
// transient error), falls back to the per-call sender so the feature
// still works — just slower.
func openRemoteInsertKeySender(remoteName, sessionID string) (insertKeySender, error) {
	config, err := session.LoadUserConfig()
	if err != nil {
		return nil, err
	}
	if config == nil {
		return nil, errInsertNoRemoteConfig
	}
	rc, ok := config.Remotes[remoteName]
	if !ok {
		return nil, errInsertNoRemoteConfig
	}
	runner := session.NewSSHRunner(remoteName, rc)
	if streamer, err := session.OpenStreamingRemoteKeySender(runner, sessionID, context.Background()); err == nil {
		return streamer, nil
	}
	return session.NewRemoteKeySender(runner, sessionID, context.Background()), nil
}
