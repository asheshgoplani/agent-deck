package session

import (
	"github.com/asheshgoplani/agent-deck/internal/send"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// PromptDetectorTool maps a session tool to the prompt detector arm that
// reads its pane: Claude-compatible tools read as "claude", Codex-compatible
// ones as "codex", anything else as itself.
func PromptDetectorTool(tool string) string {
	switch {
	case IsClaudeCompatible(tool):
		return "claude"
	case IsCodexCompatible(tool):
		return "codex"
	}
	return tool
}

// InteractiveMenuOpen reports whether content (a pane capture with ANSI
// already stripped) shows an open selection menu for tool: an
// AskUserQuestion picker, a permission dialog or a Codex approval. While one
// is open, keys typed into the pane choose an option (digits, y/n/a, Escape),
// so no automated path may type there. This is the rule `session send`'s
// prompt guard applies before every key batch.
func InteractiveMenuOpen(tool, content string) bool {
	return tmux.NewPromptDetector(PromptDetectorTool(tool)).ClassifySubstate(content) == tmux.SubstateInteractiveMenu
}

// PaneMenuOpen is InteractiveMenuOpen on a raw capture (ANSI intact), widened
// by send.DialogOpen: a selection dialog drawn under a live spinner, or one
// whose footer says "Enter to confirm" / "Esc to cancel", is still a menu even
// when the substate reads "running". send's guard refuses that case through
// its composer check (send.ComposerCodeDialog).
func PaneMenuOpen(tool, raw string) bool {
	content := tmux.StripANSI(raw)
	return InteractiveMenuOpen(tool, content) || send.DialogOpen(content)
}

// PaneBusy reports whether raw (a pane capture, ANSI intact) shows tool's
// agent working on a turn right now: the substate reads "running" (a live
// spinner, an "esc to interrupt" hint, a token timer). Only tools with a
// detector arm (Claude, Codex) can read as busy; any other tool is never
// busy here. session ref uses it with the instance status to keep a
// vim-mode Escape away from a working Claude turn, which Escape interrupts.
func PaneBusy(tool, raw string) bool {
	return tmux.NewPromptDetector(PromptDetectorTool(tool)).ClassifySubstate(tmux.StripANSI(raw)) == tmux.SubstateRunning
}
