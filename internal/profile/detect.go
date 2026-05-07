package profile

import (
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// DetectCurrentProfile returns the active agent-deck profile for the current
// process environment.
//
// As of issue #881, profile resolution lives in session.GetEffectiveProfile —
// every consumer (TUI, web /api/sessions, storage, push, costs) routes through
// that single function so TUI and web cannot disagree on which profile is
// active. This helper is preserved for callers that previously imported the
// profile package; new code should call session.GetEffectiveProfile("")
// directly.
//
// Resolution priority (defined in session.GetEffectiveProfile):
//  1. AGENTDECK_PROFILE environment variable
//  2. CLAUDE_CONFIG_DIR environment variable (e.g. ~/.claude-work -> "work")
//  3. config.json default_profile
//  4. "default"
func DetectCurrentProfile() string {
	return session.GetEffectiveProfile("")
}
