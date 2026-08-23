package ui

import "github.com/asheshgoplani/agent-deck/internal/session"

// tuiSessionResultText deliberately owns no identity, freshness, or formatting
// logic. CLI and TUI both render the session package's semantic result object.
func tuiSessionResultText(result session.SessionResult) string {
	return session.FormatSessionResult(result)
}
