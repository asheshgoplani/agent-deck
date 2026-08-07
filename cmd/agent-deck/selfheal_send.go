package main

import (
	"errors"
	"fmt"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Self-heal's send seam.
//
// The resume executor lives in internal/session, but the verified send path —
// composer-draft guard (#1409), submit verification (#1413), and the Escape+Enter
// escalation that is the only thing which ungates a wedged composer — is
// executeSend here in package main, which internal/session cannot import. So the
// implementation is registered rather than imported. This is the SAME call
// `session nudge` makes (session_nudge_cmd.go), with the same tuning: a self-heal
// resume must not be a looser send than a supervised one.

// errNoTmuxSessionForResume means the instance has no pane to send into. Not a
// delivery failure — there was nothing to deliver to.
var errNoTmuxSessionForResume = errors.New("selfheal: instance has no tmux session")

func init() {
	session.SetSelfHealSender(sendForSelfHeal)
}

// sendForSelfHeal delivers a self-heal resume prompt and returns the delivery
// verdict verbatim, so the audit records the machine-checkable value rather than
// a prose summary of it.
func sendForSelfHeal(inst *session.Instance, message string) (string, error) {
	if inst == nil {
		return "", fmt.Errorf("%w: nil instance", errNoTmuxSessionForResume)
	}
	tmuxSess := inst.GetTmuxSession()
	if tmuxSess == nil {
		return "", fmt.Errorf("%w: %s", errNoTmuxSessionForResume, inst.Title)
	}
	res, err := executeSend(tmuxSess, inst.Tool, message, false, defaultSendTuning())
	return res.delivery, err
}
