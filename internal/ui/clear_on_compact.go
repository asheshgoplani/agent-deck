package ui

import (
	"fmt"
	"log/slog"

	"github.com/asheshgoplani/agent-deck/internal/ctxinspect/ctxtext"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// clearOnCompactDue decides whether a conductor's proactive /clear is due, and
// when it is not, says why in one sentence for the log.
//
// The /clear is destructive: it discards the conductor's context on the
// strength of a percentage. So the decision fails closed on the denominator.
// A window inferred from the model id is a guess — the same id has been seen
// on 200k and 1M sessions (issue #2026), and a guess that is too small fires
// the /clear at a fraction of the real window, on a 60s cooldown. A window the
// observed usage has disproved, or one that could not be resolved at all, is
// no better. Only a window somebody established — the AGENTDECK_CONTEXT_WINDOW
// override, or a harness-reported figure — arms the trigger. Left disarmed,
// the conductor falls back to Claude's own compaction, which costs an
// optimisation rather than the conversation.
func clearOnCompactDue(a *session.SessionAnalytics) (bool, string) {
	if a == nil {
		return false, "no analytics yet"
	}
	u := a.ContextUsage()
	switch {
	case u.OverLimit:
		return false, "context window disproved by observed usage (" + ctxtext.WindowUnknownReason(u.Window) + ")"
	case !u.Known:
		return false, "context window unknown (" + ctxtext.WindowUnknownReason(u.Window) + "); " + ctxtext.WindowRemedy() + " to arm the proactive /clear"
	case u.Inferred:
		return false, fmt.Sprintf("context window inferred from model id %q, not established; %s to arm the proactive /clear", a.Model, ctxtext.WindowRemedy())
	case u.Percent < clearOnCompactThreshold:
		return false, fmt.Sprintf("context below the %.0f%% threshold", clearOnCompactThreshold)
	}
	return true, ""
}

// noteClearOnCompactDisarmed logs why a conductor's proactive /clear did not
// fire, once per distinct reason per session. A trigger that silently never
// fires is the same invisible failure as one that fires on a guess; the log
// line names the remedy so an operator can arm it deliberately.
func (h *Home) noteClearOnCompactDisarmed(instanceID, reason string) {
	if h.clearOnCompactDisarmed == nil {
		h.clearOnCompactDisarmed = make(map[string]string)
	}
	if h.clearOnCompactDisarmed[instanceID] == reason {
		return
	}
	h.clearOnCompactDisarmed[instanceID] = reason
	uiLog.Info("conductor_clear_on_compact_disarmed",
		slog.String("session", instanceID),
		slog.String("reason", reason))
}
