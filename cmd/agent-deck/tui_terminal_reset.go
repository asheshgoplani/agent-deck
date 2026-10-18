package main

import (
	"fmt"
	"io"
	"os"
	"sync/atomic"

	"golang.org/x/term"

	"github.com/asheshgoplani/agent-deck/internal/ui"
)

// tuiRunning is set right before Bubble Tea's Run and cleared when it
// returns. Only then does the terminal hold TUI modes: a signal in a
// headless mode (`web --no-tui`) or during startup must not write escapes.
var tuiRunning atomic.Bool

// terminalResetOutput and terminalResetIsTTY are the terminal the resets
// write to; tests replace them.
var (
	terminalResetOutput io.Writer = os.Stdout
	terminalResetIsTTY            = func() bool { return term.IsTerminal(int(os.Stdout.Fd())) }
)

// signalTerminalCleanup runs from the signal handler before os.Exit: hand the
// outer terminal's cursor and pointer back if the embedded terminal owned
// them (deferred releases do not survive os.Exit), then, while the TUI runs
// on a terminal, the full reset including leaving the alternate screen.
func signalTerminalCleanup() {
	runEmbeddedTerminalCleanup()
	if tuiRunning.Load() && terminalResetIsTTY() {
		ui.ResetTerminalOnSignal(terminalResetOutput)
	}
}

// reportTUIRunError runs when Bubble Tea's Run returned an error (a panic
// or a kill), before exitCLI(1). Bubble Tea's restore covers only some mouse
// modes and exitCLI skips the deferred keyboard restores, so the modes are
// turned off; the alternate screen was already left, so it is not left
// again (that would move the cursor back over the printed panic).
func reportTUIRunError(runErr error) {
	runEmbeddedTerminalCleanup()
	if terminalResetIsTTY() {
		ui.ResetTerminalAfterRunError(terminalResetOutput)
	}
	fmt.Fprintf(terminalResetOutput, "Error: %v\n", runErr)
}
