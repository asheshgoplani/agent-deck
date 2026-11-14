// Package termbg stops agent-deck from asking the terminal for its background
// colour at process start, and lets the TUI ask for it when it boots.
//
// bubbletea v1 runs lipgloss.HasDarkBackground() from its package init, in
// every process that links it. On a TTY that writes OSC 11 ; ? and CSI 6 n and
// then reads stdin raw for up to termenv.OSCTimeout (5 s) waiting for the
// reply, dropping any other byte it reads. A terminal that does not answer
// (many embedded and web terminals do not) therefore stalled every CLI
// command, `session attach` included, by 5 s and ate what the user typed
// meanwhile (#3105). Only the TUI renders adaptive colours (the bubbles
// textarea), so only the TUI needs the answer.
//
// Importing this package (main does) is enough. Its init runs before
// bubbletea's: Go initializes packages in import-path order among those whose
// imports are ready, this package imports only lipgloss, and
// "github.com/asheshgoplani/..." sorts before "github.com/charmbracelet/...".
// Keep it that way: this package must not import bubbletea or anything that
// does.
package termbg

import "github.com/charmbracelet/lipgloss"

// assumeDark is what a process that never asks gets. It is termenv's own
// answer when the terminal does not reply (default black background).
const assumeDark = true

func init() {
	// Marks the default renderer's background as known, so the
	// HasDarkBackground call in bubbletea's init returns without a query.
	lipgloss.SetHasDarkBackground(assumeDark)
}

// DetectForTUI asks the terminal for its background colour, as bubbletea's
// init used to for every process, and records the answer on the default
// lipgloss renderer. Call it on the TUI path only, before the bubbletea
// program takes over the terminal, so the reply is read here and not as
// keystrokes by the program.
func DetectForTUI() {
	lipgloss.SetHasDarkBackground(lipgloss.DefaultRenderer().Output().HasDarkBackground())
}
