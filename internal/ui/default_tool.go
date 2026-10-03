package ui

import (
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// effectiveDefaultTool is the tool a new session starts with when none was
// picked: the configured default_tool, or claude when it is unset. Launch
// paths and the [display] hide_default_tool_badge option share it so both
// agree on what "the default tool" is.
func effectiveDefaultTool(configured string) string {
	if tool := strings.TrimSpace(configured); tool != "" {
		return tool
	}
	return "claude"
}

// hiddenToolBadgeFor returns the tool whose row badge is hidden under
// [display] hide_default_tool_badge, or "" when the option is off.
func hiddenToolBadgeFor(cfg *session.UserConfig) string {
	if cfg == nil || !cfg.Display.HideDefaultToolBadge {
		return ""
	}
	return effectiveDefaultTool(cfg.DefaultTool)
}

// toolBadgeHidden reports whether a row running tool skips its tool badge.
func (h *Home) toolBadgeHidden(tool string) bool {
	return h.hiddenToolBadge != "" && strings.EqualFold(strings.TrimSpace(tool), h.hiddenToolBadge)
}
