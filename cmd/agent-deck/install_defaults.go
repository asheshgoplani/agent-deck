package main

import (
	"fmt"
	"os"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// deferredInstallNotice holds the one-time install-defaults notice for a TUI
// start, printed after the TUI exits so the alternate screen does not hide it.
var deferredInstallNotice string

// applyInstallDefaults runs session.ApplyInstallDefaults (see its doc) and
// prints its one-time notice to stderr now, or after the TUI when tui is set.
// An error is reported and never stops the command; the install keeps
// today's values either way.
func applyInstallDefaults(tui bool) {
	notice, err := session.ApplyInstallDefaults()
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-deck: warning: %v\n", err)
	}
	if notice == "" {
		return
	}
	if tui {
		deferredInstallNotice = notice
		return
	}
	fmt.Fprintln(os.Stderr, notice)
}

func printDeferredInstallNotice() {
	if deferredInstallNotice != "" {
		fmt.Fprintln(os.Stderr, deferredInstallNotice)
		deferredInstallNotice = ""
	}
}
