package ui

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/provider"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/update"
)

// sandboxProvisionTimeout bounds one TUI-driven provisioning (create, ssh
// wait, install). Provision rolls the sandbox back on its own when it fails.
const sandboxProvisionTimeout = 15 * time.Minute

// sandboxProgressMsg carries one provisioning progress line; events is
// read again for the next line or the final sandboxProvisionedMsg.
type sandboxProgressMsg struct {
	provider string
	line     string
	events   <-chan tea.Msg
}

// sandboxProvisionedMsg reports the end of a TUI-driven provisioning.
type sandboxProvisionedMsg struct {
	provider   string
	remoteName string
	opts       session.RemoteAddOptions
	err        error
}

// remoteHostLabel is what the TUI shows as a remote's host: the sandbox
// kind for a provider remote (its ssh alias means nothing to the user),
// otherwise the configured host.
func remoteHostLabel(rc session.RemoteConfig) string {
	if kind := provider.DisplayKind(rc); kind != "" {
		return kind
	}
	return rc.Host
}

// newDialogWhereChoicesFromConfig loads the Where row entries for a local
// opening of the new-session dialog. A config error leaves only this
// machine, so the dialog opens exactly as before.
func newDialogWhereChoicesFromConfig() []whereChoice {
	cfg, err := session.LoadUserConfig()
	if err != nil {
		return newDialogWhereChoices(nil)
	}
	return newDialogWhereChoices(cfg)
}

// submitNewDialogWhere handles a submit whose Where row picked a remote or
// a new sandbox. An existing remote goes through the same create path as a
// dialog opened on a remote row; a sandbox is provisioned first, off the UI
// loop, and the session is created on it when that succeeds.
func (h *Home) submitNewDialogWhere(where whereChoice) tea.Cmd {
	opts := h.newDialog.GetWhereRemoteCreateOptions()
	h.newDialog.Hide()
	h.clearError()
	if where.kind == whereRemote {
		return h.remoteCreateFunc()(where.name, opts)
	}
	h.setError(fmt.Errorf("new sandbox (%s): provisioning", where.name))
	return h.provisionSandboxCmd(where.name, opts)
}

func (h *Home) remoteCreateFunc() func(string, session.RemoteAddOptions) tea.Cmd {
	if h.remoteCreateSink != nil {
		return h.remoteCreateSink
	}
	return h.createRemoteSessionWithOptions
}

// provisionSandboxCmd runs provider.Provision in a command. Its progress
// lines and then its result travel, in order, through one channel that a
// second command reads, so a late line can never overwrite the outcome.
// Progress sends never block Provision: when the buffer is full (one slot
// stays free for the result) the line is dropped.
func (h *Home) provisionSandboxCmd(providerName string, opts session.RemoteAddOptions) tea.Cmd {
	provision := h.sandboxProvisioner
	if provision == nil {
		provision = defaultSandboxProvisioner
	}
	baseCtx := h.ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	profile := h.profile
	events := make(chan tea.Msg, 16)
	run := func() tea.Msg {
		ctx, cancel := context.WithTimeout(baseCtx, sandboxProvisionTimeout)
		defer cancel()
		res, err := provision(ctx, provider.ProvisionOptions{
			Provider: providerName,
			Profile:  profile,
			Progress: func(line string) {
				if len(events) >= cap(events)-1 {
					return
				}
				events <- sandboxProgressMsg{provider: providerName, line: line, events: events}
			},
		})
		msg := sandboxProvisionedMsg{provider: providerName, opts: opts, err: err}
		if err == nil && res != nil {
			msg.remoteName = res.RemoteName
		} else if err == nil {
			msg.err = errors.New("provisioning returned no sandbox")
		}
		events <- msg // the reader stops after this one
		return nil
	}
	return tea.Batch(run, waitSandboxEvent(events))
}

func waitSandboxEvent(events <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg { return <-events }
}

// handleSandboxProgress shows the line in the footer and waits for the next
// event.
func (h *Home) handleSandboxProgress(msg sandboxProgressMsg) tea.Cmd {
	h.setError(fmt.Errorf("new sandbox (%s): %s", msg.provider, msg.line))
	return waitSandboxEvent(msg.events)
}

// handleSandboxProvisioned creates the session on the new sandbox remote,
// or shows why provisioning failed (nothing is created then).
func (h *Home) handleSandboxProvisioned(msg sandboxProvisionedMsg) tea.Cmd {
	if msg.err != nil {
		h.setError(fmt.Errorf("new sandbox (%s) failed: %w", msg.provider, msg.err))
		return nil
	}
	h.clearError()
	uiLog.Info("sandbox_provisioned", slog.String("provider", msg.provider), slog.String("remote", msg.remoteName))
	return h.remoteCreateFunc()(msg.remoteName, msg.opts)
}

// defaultSandboxProvisioner is provider.Provision with the remote-add
// install step: keep a working agent-deck, otherwise deploy this build.
func defaultSandboxProvisioner(ctx context.Context, opts provider.ProvisionOptions) (*provider.Provisioned, error) {
	opts.Install = installOnSandbox
	return provider.Provision(ctx, opts)
}

func installOnSandbox(ctx context.Context, name string, rc session.RemoteConfig) error {
	runner := session.NewSSHRunner(name, rc)
	if remoteVersion, found := runner.CheckBinary(ctx); found {
		if update.CompareVersions(remoteVersion, Version) < 0 {
			uiLog.Info("sandbox_agent_deck_older", slog.String("remote", name), slog.String("version", remoteVersion))
		}
		return nil
	}
	if _, err := session.DeployRemoteBinary(ctx, runner, Version, session.RemoteUpdateOptions{}); err != nil {
		return fmt.Errorf("installing agent-deck v%s: %w", Version, err)
	}
	return nil
}
