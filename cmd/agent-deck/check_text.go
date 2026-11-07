package main

// Plain-text versions of the TUI's quick check page (internal/ui
// onboarding.go), printed when the TUI cannot start: tmux is missing, or it
// was started inside another tmux without the opt-in (#560, kept by the
// owner in v1.7.18 and again for onboarding, ASK-211). Same title, rows and
// short words as the in-TUI page, so a new user sees one style either way.

import (
	"fmt"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/ui"
)

const checkTitle = "Agent Deck · quick check"

// checkRow is one "✓ name   detail" line, the name padded like the TUI page.
func checkRow(mark, name, detail string) string {
	return fmt.Sprintf("%s %-12s %s", mark, name, detail)
}

// checkIndent lines up a follow-up line (an install command) under a row's
// detail column.
const checkIndent = "               "

// tmuxInstallLines are the install commands for goos, the same lines the
// TUI page shows (one shared list in internal/ui).
func tmuxInstallLines(goos string) []string {
	return ui.TmuxInstallLines(goos)
}

// tmuxMissingCheckText is what `agent-deck` (the TUI) prints when tmux is
// not installed.
func tmuxMissingCheckText(goos, version, searchedPath string) string {
	var b strings.Builder
	b.WriteString(checkTitle + "\n\n")
	b.WriteString(checkRow("✗", "tmux", "not found. Every session runs inside tmux.") + "\n")
	for _, l := range tmuxInstallLines(goos) {
		b.WriteString(checkIndent + l + "\n")
	}
	b.WriteString(checkRow("✓", "agent-deck", "v"+version) + "\n\n")
	b.WriteString("Install tmux, then run agent-deck again.\n")
	if searchedPath != "" {
		b.WriteString("\nSearched PATH: " + searchedPath + "\n")
	}
	return b.String()
}

// outerTmuxRefusalText is what the TUI prints when it is started inside
// another tmux without AGENT_DECK_ALLOW_OUTER_TMUX=1. It still exits: the
// refusal is the owner's call (#560); only the words changed.
func outerTmuxRefusalText(tmuxVersion, version string) string {
	tmuxDetail := "you are inside it"
	if tmuxVersion != "" {
		tmuxDetail = tmuxVersion + " · you are inside it"
	}
	var b strings.Builder
	b.WriteString(checkTitle + "\n\n")
	b.WriteString("You are inside tmux. agent-deck runs its own tmux sessions, and\n")
	b.WriteString("Ctrl+Q would bring you back to this tmux, not to a plain shell.\n\n")
	b.WriteString(checkRow("✓", "tmux", tmuxDetail) + "\n")
	b.WriteString(checkRow("✓", "agent-deck", "v"+version) + "\n\n")
	b.WriteString("Detach (Ctrl+B d) and run agent-deck from a plain shell.\n")
	b.WriteString("Or run it here anyway:\n\n")
	b.WriteString("  AGENT_DECK_ALLOW_OUTER_TMUX=1 agent-deck\n\n")
	b.WriteString("CLI commands work inside tmux: agent-deck list, agent-deck add <path>.\n")
	return b.String()
}

// tmuxVersionLine is `tmux -V` ("tmux 3.5a"), or "" when it cannot be read
// within a second (the TUI page's probe, with the filtered child env).
func tmuxVersionLine() string {
	v, _ := ui.TmuxVersion()
	return v
}
