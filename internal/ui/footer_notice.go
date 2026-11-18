package ui

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// footerNotice is a message for the footer line that reports something that
// worked ("archived 'x'", "Copied 3 lines"). It travels in h.err so it shares
// the footer row, its reserved height and the 5 s auto-dismiss with real
// errors, but it is drawn with a check mark instead of the warning sign.
type footerNotice struct{ text string }

func (n footerNotice) Error() string { return n.text }

// isFooterNotice reports whether a footer message is a success notice.
func isFooterNotice(err error) bool {
	var n footerNotice
	return errors.As(err, &n)
}

// setNotice shows a success message in the footer line.
func (h *Home) setNotice(format string, args ...any) {
	h.err = footerNotice{text: fmt.Sprintf(format, args...)}
	h.errTime = time.Now()
	uiLog.Info("ui_notice", slog.String("msg", h.err.Error()))
}

// lineCount words a number of lines: "1 line", "3 lines".
func lineCount(n int) string {
	if n == 1 {
		return "1 line"
	}
	return fmt.Sprintf("%d lines", n)
}

// explainKeyNeedsSession says in the footer why a session key did nothing on
// the row under the cursor: a group header, a window row or a remote row.
// action names what the key does ("fork", "copy output").
func (h *Home) explainKeyNeedsSession(action string) {
	if h.cursor < 0 || h.cursor >= len(h.flatItems) {
		h.setError(fmt.Errorf("%s: no session selected", action))
		return
	}
	switch h.flatItems[h.cursor].Type {
	case session.ItemTypeGroup, session.ItemTypeRemoteGroup:
		h.setError(fmt.Errorf("%s: select a session first (this row is a group)", action))
	case session.ItemTypeWindow:
		h.setError(fmt.Errorf("%s: select the session row, not one of its windows", action))
	case session.ItemTypeRemoteSession:
		h.setError(fmt.Errorf("%s: not available for remote sessions", action))
	default:
		h.setError(fmt.Errorf("%s: not available for this row", action))
	}
}

// forkNotPossible is the footer reason for f/F on a session that cannot fork,
// in the words `agent-deck session fork` uses.
func forkNotPossible(inst *session.Instance) error {
	return fmt.Errorf("session '%s' cannot be forked: no resumable session for tool %s", inst.Title, inst.Tool)
}
