package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"golang.org/x/term"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/ui"
)

// errAttachNoTTY signals that an interactive attach was requested (e.g. via
// `--attach`) without a usable terminal: stdin and stdout must BOTH be TTYs.
// Callers map this to a non-zero exit while leaving the session created and
// running — only the interactive attach step is refused, never silently.
var errAttachNoTTY = errors.New("attach requires an interactive terminal")

// stdinStdoutIsTerminal reports whether both stdin and stdout are connected to
// an interactive terminal. Attach drives a PTY against the current terminal, so
// both must be TTYs; a pipe/redirect on either side (scripts, CI, conductor,
// `--json` consumers) makes interactive attach impossible.
func stdinStdoutIsTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

// attachInstanceInteractive attaches the current process to inst's local tmux
// pane and blocks until the user detaches (with the configured detach key).
//
// It returns errAttachNoTTY when there is no interactive terminal, so the
// caller can report it loudly and exit non-zero while leaving the (already
// created/started) session intact. Any other error (no tmux pane, attach
// failure) is returned verbatim. The session must already be started before
// this is called.
func attachInstanceInteractive(inst *session.Instance) error {
	if !stdinStdoutIsTerminal() {
		return errAttachNoTTY
	}
	tmuxSession := inst.GetTmuxSession()
	if tmuxSession == nil {
		return fmt.Errorf("no tmux session for '%s'", inst.Title)
	}
	detachByte := ui.ResolvedDetachByte(session.GetHotkeyOverrides())
	return tmuxSession.Attach(context.Background(), detachByte)
}
