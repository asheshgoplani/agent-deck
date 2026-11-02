package ui

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/provider"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// sandboxDestroyTimeout bounds the provider destroy, as the CLI does.
const sandboxDestroyTimeout = 5 * time.Minute

// sandboxOpDoneLinger is how long a finished sandbox operation's last line
// stays on screen.
const sandboxOpDoneLinger = 8 * time.Second

// sandboxDestroyDeps are the remote and provider calls a TUI sandbox
// destroy makes; tests replace them.
type sandboxDestroyDeps struct {
	listSessions func(ctx context.Context, name string, rc session.RemoteConfig) ([]session.RemoteSessionInfo, error)
	stopSession  func(ctx context.Context, name string, rc session.RemoteConfig, id string) error
	destroy      func(ctx context.Context, name string) error
	// lock takes the remote's sandbox lock (nil: provider.LockRemote).
	lock func(name string) (func(), error)
}

func defaultSandboxDestroyDeps() *sandboxDestroyDeps {
	return &sandboxDestroyDeps{
		listSessions: func(ctx context.Context, name string, rc session.RemoteConfig) ([]session.RemoteSessionInfo, error) {
			sessions, _, err := session.NewSSHRunner(name, rc).FetchSessions(ctx)
			return sessions, err
		},
		stopSession: func(ctx context.Context, name string, rc session.RemoteConfig, id string) error {
			return session.NewSSHRunner(name, rc).StopSession(ctx, id)
		},
		destroy: provider.DestroyRemote,
		lock:    provider.LockRemote,
	}
}

// sandboxOp is one sandbox create or destroy in flight, shown as a status
// line above the list until it ends (and briefly after).
type sandboxOp struct {
	remote  string // the remote a destroy works on; "" for a create
	label   string // "new sandbox (docker)", "destroying sandbox sb1"
	step    string // last progress line
	started time.Time
	doneAt  time.Time // zero while running
}

// startSandboxOp registers an operation and returns its id. Event-loop
// goroutine only, like every sandboxOps access.
func (h *Home) startSandboxOp(label, step string) uint64 {
	now := time.Now()
	for id, op := range h.sandboxOps {
		if !op.doneAt.IsZero() && now.Sub(op.doneAt) > sandboxOpDoneLinger {
			delete(h.sandboxOps, id)
		}
	}
	if h.sandboxOps == nil {
		h.sandboxOps = make(map[uint64]*sandboxOp)
	}
	h.sandboxOpSeq++
	h.sandboxOps[h.sandboxOpSeq] = &sandboxOp{label: label, step: step, started: now}
	return h.sandboxOpSeq
}

func (h *Home) setSandboxOpStep(id uint64, step string) {
	if op := h.sandboxOps[id]; op != nil {
		op.step = step
	}
}

// endSandboxOp ends an operation and returns its last step. A non-empty
// result line keeps it on screen for sandboxOpDoneLinger; an empty one
// removes it at once.
func (h *Home) endSandboxOp(id uint64, result string) string {
	op := h.sandboxOps[id]
	if op == nil {
		return ""
	}
	last := op.step
	if result == "" {
		delete(h.sandboxOps, id)
		return last
	}
	op.step = result
	op.label = ""
	op.doneAt = time.Now()
	return last
}

// sandboxStatusLines are the status lines of the sandbox operations, oldest
// first. They sit above the list, so neither the footer clamp nor the error
// auto-dismiss can hide them.
func (h *Home) sandboxStatusLines(now time.Time) []string {
	if len(h.sandboxOps) == 0 {
		return nil
	}
	ids := make([]uint64, 0, len(h.sandboxOps))
	for id := range h.sandboxOps {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	running := lipgloss.NewStyle().Foreground(ColorYellow)
	done := lipgloss.NewStyle().Foreground(ColorGreen)
	var lines []string
	for _, id := range ids {
		op := h.sandboxOps[id]
		if !op.doneAt.IsZero() {
			if now.Sub(op.doneAt) <= sandboxOpDoneLinger {
				lines = append(lines, done.Render(truncateDisplay(" ✓ "+op.step, max(1, h.width), "…")))
			}
			continue
		}
		secs := int(now.Sub(op.started).Seconds())
		lines = append(lines, running.Render(truncateDisplay(fmt.Sprintf(" ◌ %s: %s  (%ds)", op.label, op.step, secs), max(1, h.width), "…")))
	}
	return lines
}

// sandboxStatusHeight is how many rows the sandbox status lines take.
func (h *Home) sandboxStatusHeight() int {
	return len(h.sandboxStatusLines(time.Now()))
}

// destroyRunning reports whether a destroy of remoteName is still running
// in this TUI.
func (h *Home) destroyRunning(remoteName string) bool {
	for _, op := range h.sandboxOps {
		if op.remote == remoteName && op.doneAt.IsZero() {
			return true
		}
	}
	return false
}

// sandboxRemote returns the config of remote name when it is a provider
// sandbox.
func sandboxRemote(name string) (session.RemoteConfig, bool) {
	cfg, err := session.LoadUserConfig()
	if err != nil || cfg == nil {
		return session.RemoteConfig{}, false
	}
	rc, ok := cfg.Remotes[name]
	if !ok || !provider.IsSandbox(rc) {
		return session.RemoteConfig{}, false
	}
	return rc, true
}

// openDestroySandboxConfirm opens the destroy confirmation for a sandbox
// remote's host row, naming how many of its sessions will be stopped. It
// returns false (and does nothing) for a plain remote.
func (h *Home) openDestroySandboxConfirm(remoteName string) bool {
	rc, ok := sandboxRemote(remoteName)
	if !ok {
		return false
	}
	if h.destroyRunning(remoteName) {
		h.confirmDialog.ShowNotice("Sandbox Destroy Running",
			fmt.Sprintf("Sandbox %s is already being destroyed.\n\nWait for that destroy to end; its result shows above the list.", remoteName))
		return true
	}
	h.remoteSessionsMu.RLock()
	count := liveSessionCount(h.remoteSessions[remoteName])
	h.remoteSessionsMu.RUnlock()
	h.confirmDialog.ShowDestroySandbox(remoteName, provider.DisplayKind(rc), rc.SandboxID, count)
	return true
}

// sandboxDestroyedMsg reports the end of a TUI sandbox destroy.
type sandboxDestroyedMsg struct {
	op         uint64
	remoteName string
	sandboxID  string
	provider   string
	stopped    int
	// refused is set when nothing was changed (the err says why).
	refused bool
	// busy is set when it was refused because another destroy (or a create
	// of the same name) of this remote runs.
	busy bool
	err  error
}

// destroySandboxCmd destroys sandbox remote remoteName after the user agreed
// to destroy sandbox confirmedID and stop `confirmed` sessions. It follows
// the CLI's `remote destroy` rule: the sessions are listed again first, and
// when they cannot be listed, or more run now than the user agreed to stop,
// nothing is changed. Nothing is changed either when the remote now points
// at another sandbox than the one confirmed.
func (h *Home) destroySandboxCmd(remoteName, confirmedID string, confirmed int) tea.Cmd {
	deps := h.sandboxDestroyDeps
	if deps == nil {
		deps = defaultSandboxDestroyDeps()
	}
	if h.destroyRunning(remoteName) {
		return func() tea.Msg {
			return sandboxDestroyedMsg{remoteName: remoteName, refused: true, busy: true,
				err: fmt.Errorf("sandbox %s is already being destroyed", remoteName)}
		}
	}
	lock := deps.lock
	if lock == nil {
		lock = provider.LockRemote
	}
	op := h.startSandboxOp("destroying sandbox "+remoteName, "listing its sessions...")
	h.sandboxOps[op].remote = remoteName
	baseCtx := h.ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	return func() tea.Msg {
		msg := sandboxDestroyedMsg{op: op, remoteName: remoteName}
		// Held until the destroy ends, so a CLI destroy of this remote or a
		// create of the same name cannot run at the same time.
		unlock, err := lock(remoteName)
		if err != nil {
			msg.refused, msg.busy, msg.err = true, errors.Is(err, provider.ErrRemoteBusy), err
			return msg
		}
		defer unlock()
		rc, ok := sandboxRemote(remoteName)
		if !ok {
			msg.refused = true
			msg.err = fmt.Errorf("remote %s is no longer a configured sandbox", remoteName)
			return msg
		}
		msg.sandboxID, msg.provider = rc.SandboxID, rc.Provider
		if rc.SandboxID != confirmedID {
			msg.refused = true
			msg.err = fmt.Errorf("remote %s now points at sandbox %s, not the sandbox %s you agreed to destroy", remoteName, rc.SandboxID, confirmedID)
			return msg
		}
		listCtx, cancelList := context.WithTimeout(baseCtx, rc.GetCommandTimeout())
		sessions, err := deps.listSessions(listCtx, remoteName, rc)
		cancelList()
		if err != nil {
			msg.refused = true
			msg.err = fmt.Errorf("could not list the sessions on %s: %w", remoteName, err)
			return msg
		}
		if live := liveSessionCount(sessions); live > confirmed {
			msg.refused = true
			msg.err = fmt.Errorf("%s now has %s to stop, more than the %d you agreed to", remoteName, pluralSessionCount(live), confirmed)
			return msg
		}
		for _, s := range sessions {
			if s.Status == "stopped" {
				continue
			}
			stopCtx, cancelStop := context.WithTimeout(baseCtx, rc.GetCommandTimeout())
			err := deps.stopSession(stopCtx, remoteName, rc, s.ID)
			cancelStop()
			if err != nil {
				// The destroy below ends it anyway, as in the CLI.
				uiLog.Warn("sandbox_destroy_stop_failed", slog.String("remote", remoteName), slog.String("session", s.ID), slog.String("error", err.Error()))
				continue
			}
			msg.stopped++
		}
		destroyCtx, cancelDestroy := context.WithTimeout(baseCtx, sandboxDestroyTimeout)
		defer cancelDestroy()
		msg.err = deps.destroy(destroyCtx, remoteName)
		return msg
	}
}

// handleSandboxDestroyed shows the outcome: a short result line on
// success (the remote's rows drop out with the next fetch), a notice in
// plain words otherwise.
func (h *Home) handleSandboxDestroyed(msg sandboxDestroyedMsg) tea.Cmd {
	if msg.err != nil {
		h.endSandboxOp(msg.op, "")
		body := fmt.Sprintf("Sandbox %s was not destroyed:\n\n%s\n\n", msg.remoteName, msg.err)
		if msg.busy {
			body += "Nothing was changed. Wait for the other create or destroy\nof this remote to end."
		} else if msg.refused {
			body += fmt.Sprintf("Nothing was changed. Press d again to retry, or run\n'agent-deck remote destroy %s --force' to destroy it anyway.", msg.remoteName)
		} else if errors.Is(msg.err, provider.ErrDestroyedCleanupFailed) {
			body += fmt.Sprintf("The sandbox itself was destroyed; only agent-deck's own\ncleanup did not finish. If %s is still listed, press d\nagain to finish it, or run 'agent-deck remote destroy %s'.", msg.remoteName, msg.remoteName)
		} else {
			body += "Its sessions may have been stopped; the sandbox and its remote\nare still there, so you can retry with d."
		}
		h.confirmDialog.ShowNotice("⚠  Sandbox Not Destroyed", body)
		return nil
	}
	uiLog.Info("sandbox_destroyed", slog.String("remote", msg.remoteName), slog.String("provider", msg.provider), slog.Int("stopped", msg.stopped))
	h.endSandboxOp(msg.op, fmt.Sprintf("destroyed sandbox %s (%s, provider %s), stopped %s, removed the remote",
		msg.remoteName, msg.sandboxID, msg.provider, pluralSessionCount(msg.stopped)))
	return h.fetchRemoteSessions
}

// sandboxProvisionFailedNotice says in plain words why a new sandbox could
// not be made, at which step, and whether anything was left behind.
func sandboxProvisionFailedNotice(providerName, step string, err error) string {
	var b strings.Builder
	fmt.Fprintf(&b, "agent-deck could not set up a new sandbox with provider %q.\n\n", providerName)
	if step != "" {
		fmt.Fprintf(&b, "It stopped at: %s\n", step)
	}
	fmt.Fprintf(&b, "Reason: %s\n\n", err)
	text := err.Error()
	if strings.Contains(text, "rollback failed") || strings.Contains(text, "by hand") || strings.Contains(text, "could not check for a leftover") {
		b.WriteString("The sandbox may still exist: check the provider and remove it\nby hand (see the reason above). No session was created.")
	} else {
		b.WriteString("Nothing was left behind and no session was created.")
	}
	return b.String()
}

// withInstallProgress wraps install so the install step reports progress.
func withInstallProgress(opts provider.ProvisionOptions, install func(context.Context, string, session.RemoteConfig) error) provider.ProvisionOptions {
	progress := opts.Progress
	opts.Install = func(ctx context.Context, name string, rc session.RemoteConfig) error {
		if progress != nil {
			progress("Installing agent-deck on the sandbox...")
		}
		return install(ctx, name, rc)
	}
	return opts
}

// liveSessionCount counts the sessions a destroy has to stop (all but the
// stopped ones, which the CLI's remote destroy skips too).
func liveSessionCount(sessions []session.RemoteSessionInfo) int {
	n := 0
	for _, s := range sessions {
		if s.Status != "stopped" {
			n++
		}
	}
	return n
}

// pluralSessionCount renders n as "1 session" or "<n> sessions".
func pluralSessionCount(n int) string {
	if n == 1 {
		return "1 session"
	}
	return fmt.Sprintf("%d sessions", n)
}
