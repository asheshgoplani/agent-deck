package tmux

import (
	"os"

	"golang.org/x/term"
)

// Birth size for a detached `tmux new-session`.
//
// #1694: a detached new-session with no -x/-y is born at tmux's 80x24
// default-size, and agent-deck runs the tool as the pane's INITIAL PROCESS
// (RunCommandAsInitialProcess), so the tool paints its first frames into an
// 80x24 window. The attach client arrives later at the real terminal size and
// window-size=largest grows the window — but a TUI that fixed its layout
// geometry on the first paint keeps drawing the 80-column layout into the
// wider pane. OpenCode does exactly that and renders visibly clipped; tools
// that reflow on SIGWINCH (Claude Code, Codex) hid the gap for two releases.
//
// This is the other half of #1167, which pre-sized only the ATTACH client's
// PTY (see StartAttachPTY) and left the session's own creation at the default.
// aggressive-resize cannot rescue the birth size either: it is a no-op under
// window-size=largest, which Session.Start pins.
const (
	// tmuxDefaultCols/Rows are tmux's own birth size for a detached session.
	// They are a floor, not a target: a host terminal narrower than this must
	// not birth a pane MORE clipped than tmux's default already is, and
	// window-size=largest shrinks the window to the real client on attach.
	tmuxDefaultCols = 80
	tmuxDefaultRows = 24

	// headlessInitialCols/Rows are the birth size when no controlling
	// terminal is reachable: web/xterm.js sessions, watcher- and cron-spawned
	// launches, `agent-deck launch` with redirected output. Deliberately
	// generous — wider than any realistic client — so the first paint is
	// never the clipped one.
	headlessInitialCols = 200
	headlessInitialRows = 50
)

// terminalSizeProbe reports the size of the terminal agent-deck itself runs
// on. Swappable seam so tests can drive both the TTY and the no-TTY branch
// deterministically, without needing a real terminal.
var terminalSizeProbe = probeTerminalSize

// probeTerminalSize tries stdout, then stderr, then stdin — the first that is
// a terminal wins. stdout is the TUI's terminal; stderr survives `>file`
// redirection; stdin covers the rest. /dev/tty is deliberately NOT opened: in
// a daemonized parent that can block, and it would leak a descriptor on every
// session spawn.
func probeTerminalSize() (cols, rows int, ok bool) {
	for _, f := range []*os.File{os.Stdout, os.Stderr, os.Stdin} {
		if f == nil {
			continue
		}
		w, h, err := term.GetSize(int(f.Fd()))
		if err == nil && w > 0 && h > 0 {
			return w, h, true
		}
	}
	return 0, 0, false
}

// InitialWindowSize returns the cols/rows a detached `new-session` must be
// born at: the real terminal size when there is one (floored at tmux's own
// 80x24 default), else the generous headless size.
//
// Every `new-session` spawn in this repo has to pass these as -x/-y —
// enforced by TestNewSessionSpawnsCarryExplicitSize.
func InitialWindowSize() (cols, rows int) {
	cols, rows, ok := terminalSizeProbe()
	if !ok {
		return headlessInitialCols, headlessInitialRows
	}
	if cols < tmuxDefaultCols {
		cols = tmuxDefaultCols
	}
	if rows < tmuxDefaultRows {
		rows = tmuxDefaultRows
	}
	return cols, rows
}
