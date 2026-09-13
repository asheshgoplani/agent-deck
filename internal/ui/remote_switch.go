package ui

// Account and harness switching for sessions a remote deck owns (Shift+P on
// a remote row, or the Edit Session dialog's harness/account rows there).
//
// The switch itself never runs here: transcripts, config dirs and
// credentials are host-local and must not cross hosts. The controller asks
// the remote for `session switch-preview --json`, shows the same
// confirmation the local flow shows from that answer, and on Switch runs the
// remote's own `session switch`. The row updates through the pushed remote
// events like every other remote mutation; a fetch is issued as well.

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// remoteSwitchRunner is the part of session.SSHRunner the remote switch flow
// uses; tests substitute a fake so no SSH is spawned.
type remoteSwitchRunner interface {
	FetchAccountsForHarness(ctx context.Context, harness string) ([]string, error)
	SwitchPreview(ctx context.Context, sessionID, harness, account string) (*session.RemoteSwitchPreview, error)
	SwitchSession(ctx context.Context, sessionID, harness, account string, confirmContextLoss bool) (*session.RemoteSwitchResult, error)
}

// newRemoteSwitchRunner builds the runner for a configured remote.
var newRemoteSwitchRunner = func(remoteName string) (remoteSwitchRunner, error) {
	runner, err := remoteRunnerFor(remoteName)
	if err != nil {
		return nil, err
	}
	return runner, nil
}

// remoteSwitchTimeout bounds a remote switch: the remote stops the session,
// copies or exports its conversation, restarts the harness and waits for
// native readiness evidence, which takes longer than an ordinary verb.
const remoteSwitchTimeout = 3 * time.Minute

// remoteEditAccountsFetchedMsg carries the remote's configured slots for the
// Edit Session dialog opened on one of its rows.
type remoteEditAccountsFetchedMsg struct {
	remoteName string
	sessionID  string
	accounts   map[string][]string
	err        error
}

// remoteSwitchPreviewMsg is the remote's switch-preview answer for the
// target the user chose in the Edit Session dialog.
type remoteSwitchPreviewMsg struct {
	remoteName string
	source     session.RemoteSessionInfo
	harness    string
	account    string
	preview    *session.RemoteSwitchPreview
	err        error
}

// remoteSwitchResultMsg is the outcome of the remote's own `session switch`.
type remoteSwitchResultMsg struct {
	remoteName string
	source     session.RemoteSessionInfo
	harness    string
	account    string
	cross      bool
	result     *session.RemoteSwitchResult
	err        error
}

// remoteSessionInfo returns the cached row for one remote session.
func (h *Home) remoteSessionInfo(remoteName, sessionID string) (session.RemoteSessionInfo, bool) {
	h.remoteSessionsMu.RLock()
	defer h.remoteSessionsMu.RUnlock()
	for _, info := range h.remoteSessions[remoteName] {
		if info.ID == sessionID {
			info.RemoteName = remoteName
			return info, true
		}
	}
	return session.RemoteSessionInfo{}, false
}

// openRemoteEditSession opens the Edit Session dialog for a remote row and
// asks that remote for its account slots.
func (h *Home) openRemoteEditSession(item session.Item) tea.Cmd {
	if item.RemoteSession == nil {
		return nil
	}
	info := *item.RemoteSession
	info.RemoteName = item.RemoteName
	h.editSessionDialog.SetSize(h.width, h.height)
	h.editSessionDialog.ShowRemote(info, item.RemoteName)
	return h.fetchRemoteEditAccounts(item.RemoteName, info.ID)
}

// fetchRemoteEditAccounts asks the remote for the slots of both harness
// families that have named accounts. A family the remote cannot answer for
// (a remote too old for `accounts --harness codex`) simply offers "inherit";
// the remote's preview still refuses an unknown slot by name.
func (h *Home) fetchRemoteEditAccounts(remoteName, sessionID string) tea.Cmd {
	return func() tea.Msg {
		msg := remoteEditAccountsFetchedMsg{remoteName: remoteName, sessionID: sessionID, accounts: map[string][]string{}}
		runner, err := newRemoteSwitchRunner(remoteName)
		if err != nil {
			msg.err = err
			return msg
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		for _, harness := range []string{"claude", "codex"} {
			names, err := runner.FetchAccountsForHarness(ctx, harness)
			if err != nil {
				if harness == "claude" {
					msg.err = err
				}
				continue
			}
			msg.accounts[harness] = names
		}
		return msg
	}
}

// applyRemoteEditAccounts hands the slots to the dialog when it is still
// open for that remote session; anything else is a late answer and dropped.
func (h *Home) applyRemoteEditAccounts(msg remoteEditAccountsFetchedMsg) {
	if !h.editSessionDialog.IsVisible() || h.editSessionDialog.RemoteName() != msg.remoteName || h.editSessionDialog.SessionID() != msg.sessionID {
		return
	}
	if msg.err != nil {
		h.editSessionDialog.SetError(fmt.Sprintf("could not list accounts on %s: %v", msg.remoteName, msg.err))
	}
	h.editSessionDialog.SetRemoteAccounts(msg.accounts)
}

// commitRemoteEditSession is Enter in the Edit Session dialog for a remote
// row. A title edit goes through the remote's rename; a harness or account
// change asks the remote for a switch preview first and never runs the
// switch from a plain save.
func (h *Home) commitRemoteEditSession() tea.Cmd {
	remoteName, sessionID := h.editSessionDialog.RemoteName(), h.editSessionDialog.SessionID()
	info, ok := h.remoteSessionInfo(remoteName, sessionID)
	if !ok {
		h.editSessionDialog.Hide()
		h.setError(fmt.Errorf("session no longer listed on %s", remoteName))
		return nil
	}
	changes := h.editSessionDialog.GetChanges(remoteEditInstance(info))
	if len(changes) == 0 {
		h.editSessionDialog.Hide()
		return nil
	}
	var harness, account, newTitle string
	switching := false
	for _, c := range changes {
		switch c.Field {
		case session.FieldTool:
			if strings.TrimSpace(c.Value) != "" && c.Value != info.Tool {
				harness, switching = c.Value, true
			}
		case session.FieldAccount:
			// Clearing the slot is not a switch locally either (there is no
			// target dir to move the conversation into).
			if strings.TrimSpace(c.Value) != "" {
				account, switching = c.Value, true
			}
		case session.FieldTitle:
			newTitle = c.Value
		}
	}
	if switching {
		if newTitle != "" {
			h.editSessionDialog.SetError("save harness/account switch separately from other edits")
			return nil
		}
		if harness == "" {
			harness = info.Tool
		}
		h.setRemotePending(sessionID, "previewing…")
		return h.previewRemoteSwitch(remoteName, info, harness, account)
	}
	h.editSessionDialog.Hide()
	if newTitle == "" {
		return nil
	}
	oldTitle := h.setRemoteSessionTitle(remoteName, sessionID, newTitle)
	return h.renameRemoteSession(remoteName, sessionID, oldTitle, newTitle)
}

// previewRemoteSwitch asks the remote what the switch would do.
func (h *Home) previewRemoteSwitch(remoteName string, source session.RemoteSessionInfo, harness, account string) tea.Cmd {
	return func() tea.Msg {
		msg := remoteSwitchPreviewMsg{remoteName: remoteName, source: source, harness: harness, account: account}
		runner, err := newRemoteSwitchRunner(remoteName)
		if err != nil {
			msg.err = err
			return msg
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		msg.preview, msg.err = runner.SwitchPreview(ctx, source.ID, harness, account)
		return msg
	}
}

// handleRemoteSwitchPreview turns the remote's preview into the same
// confirmation the local flow shows, or into the refusal notice.
func (h *Home) handleRemoteSwitchPreview(msg remoteSwitchPreviewMsg) tea.Cmd {
	h.setRemotePending(msg.source.ID, "")
	if session.IsRemoteInterrupted(msg.err) {
		return h.remoteOutcomeUnknown(msg.remoteName)
	}
	if msg.err != nil {
		h.confirmDialog.ShowNotice("Switch preview failed on "+msg.remoteName, msg.err.Error())
		return nil
	}
	cross := session.CanonicalSwitchHarnessForUI(msg.harness) != session.CanonicalSwitchHarnessForUI(msg.source.Tool)
	if msg.preview.Refusal != nil {
		title := "Account switch refused on " + msg.remoteName
		if cross {
			title = "Harness transfer refused on " + msg.remoteName
		}
		body := fmt.Sprintf("on %s: %s", msg.remoteName, msg.preview.Refusal.Message)
		if msg.preview.Refusal.Code != "" {
			body += "\n\nrefusal code: " + msg.preview.Refusal.Code
		}
		h.confirmDialog.ShowNotice(title, body)
		return nil
	}
	h.confirmDialog.SetSize(h.width, h.height)
	if cross {
		h.editSessionDialog.Hide()
		h.confirmDialog.ShowRemoteCrossHarnessTransfer(msg.remoteName, msg.source, msg.harness, msg.account, msg.preview.Exclusions, msg.preview.Warnings)
		return nil
	}
	h.confirmDialog.ShowRemoteSwitchAccount(msg.remoteName, msg.source, msg.harness, msg.account, msg.preview.Warnings)
	return nil
}

// confirmRemoteSwitch is the Switch/Transfer button of a remote switch
// confirmation. The row must still be what the user confirmed.
func (h *Home) confirmRemoteSwitch() tea.Cmd {
	remoteName, sessionID := h.confirmDialog.GetRemoteName(), h.confirmDialog.GetTargetID()
	harness, account := h.confirmDialog.TargetHarness(), h.confirmDialog.TargetAccount()
	cross := h.confirmDialog.GetConfirmType() == ConfirmCrossHarnessTransfer
	info, ok := h.remoteSessionInfo(remoteName, sessionID)
	if !ok || !h.confirmDialog.RemoteSourceMatches(&info) {
		what := "Account switch cancelled"
		if cross {
			what = "Harness transfer cancelled"
		}
		h.confirmDialog.ShowNotice(what, fmt.Sprintf("The session changed on %s while this confirmation was open. Review the current row and confirm a new switch.", remoteName))
		return nil
	}
	h.confirmDialog.Hide()
	h.editSessionDialog.Hide()
	return h.switchRemoteSession(remoteName, info, harness, account, cross)
}

// switchRemoteSession runs the remote's own `session switch`.
func (h *Home) switchRemoteSession(remoteName string, source session.RemoteSessionInfo, harness, account string, cross bool) tea.Cmd {
	h.markRemoteAction(remoteName, "switch", source.ID)
	h.setRemotePending(source.ID, "switching…")
	return func() tea.Msg {
		msg := remoteSwitchResultMsg{remoteName: remoteName, source: source, harness: harness, account: account, cross: cross}
		runner, err := newRemoteSwitchRunner(remoteName)
		if err != nil {
			msg.err = err
			return msg
		}
		ctx, cancel := context.WithTimeout(context.Background(), remoteSwitchTimeout)
		defer cancel()
		msg.result, msg.err = runner.SwitchSession(ctx, source.ID, harness, account, cross)
		return msg
	}
}

// handleRemoteSwitchResult reports the remote's outcome with the same
// verified / pending / failed wording the local flow uses, names the remote,
// and patches the cached row for a confirmed native switch.
func (h *Home) handleRemoteSwitchResult(msg remoteSwitchResultMsg) tea.Cmd {
	h.setRemotePending(msg.source.ID, "")
	h.noteRemoteActionSettled(msg.remoteName)
	if session.IsRemoteInterrupted(msg.err) {
		return h.remoteOutcomeUnknown(msg.remoteName)
	}
	what := "Account switch"
	if msg.cross {
		what = "Harness transfer"
	}
	took := h.remoteActionTook(msg.remoteName, "switch", msg.source.ID)
	if msg.err != nil {
		body := msg.err.Error()
		if r := msg.result; r != nil {
			body += fmt.Sprintf("\n\non %s: status=%s; recovery_required=%t", msg.remoteName, presentRemoteStatus(r.Status), r.RecoveryRequired)
			if r.TargetID != "" {
				body += "; target_id=" + r.TargetID
			}
		} else {
			body += "\n\non " + msg.remoteName
		}
		h.confirmDialog.ShowNotice(what+" failed on "+msg.remoteName, body)
		return h.fetchRemoteSessions
	}
	r := msg.result
	if r == nil {
		h.confirmDialog.ShowNotice(what+" failed on "+msg.remoteName, "the remote returned no result")
		return h.fetchRemoteSessions
	}
	switch {
	case r.Pending || (!r.TargetReady && !r.DestinationReady):
		body := fmt.Sprintf("on %s: status=%s", msg.remoteName, presentRemoteStatus(r.Status))
		if r.TargetID != "" {
			body += fmt.Sprintf("; new %s target %s created", msg.harness, r.TargetID)
		} else {
			body += fmt.Sprintf("; account changed to %q", displayConfirmAccount(msg.account))
		}
		if r.MissingContract != "" {
			body += "; waiting for " + r.MissingContract
		}
		body += ". The restarted harness has not reported native readiness yet."
		body += " " + remoteSourceDisposition(msg.remoteName, r, msg.cross)
		h.confirmDialog.ShowNotice(what+" pending on "+msg.remoteName, body)
	case msg.cross:
		h.confirmDialog.ShowNotice(what+" verified on "+msg.remoteName,
			fmt.Sprintf("New %s target %s is verified and ready on %s%s. %s", msg.harness, r.TargetID, msg.remoteName, took, remoteSourceDisposition(msg.remoteName, r, true)))
	default:
		h.patchRemoteSession(msg.remoteName, msg.source.ID, func(info *session.RemoteSessionInfo) {
			info.Account = msg.account
			if r.NewTool != "" {
				info.Tool = r.NewTool
			}
		})
		body := fmt.Sprintf("on %s: %s/%s → %s/%s (%s); status=%s; readiness=ready%s",
			msg.remoteName, msg.source.Tool, displayConfirmAccount(r.OldAccount), msg.harness, displayConfirmAccount(r.NewAccount), r.Continuity, presentRemoteStatus(r.Status), took)
		if len(r.LossDisclosure) > 0 {
			body += "\n\nNot preserved: " + strings.Join(r.LossDisclosure, "; ")
		}
		h.confirmDialog.ShowNotice(what+" verified on "+msg.remoteName, body)
	}
	return h.fetchRemoteSessions
}

// remoteSourceDisposition states exactly what the remote did to the source
// row, from the remote's own report: a ready cross-harness target archives
// it as superseded (reversible); otherwise it is untouched.
func remoteSourceDisposition(remoteName string, r *session.RemoteSwitchResult, cross bool) string {
	if r.SourceWasArchived(cross) {
		by := r.SourceSupersededBy
		if by == "" {
			by = r.TargetID
		}
		return fmt.Sprintf("The source session is archived on %s as superseded by %s (reversible; see the archived view).", remoteName, by)
	}
	return fmt.Sprintf("The source session is unchanged on %s.", remoteName)
}

func presentRemoteStatus(status string) string {
	if strings.TrimSpace(status) == "" {
		return "unknown"
	}
	return status
}
