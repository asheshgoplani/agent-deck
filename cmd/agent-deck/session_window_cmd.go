package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// `agent-deck session window` — CLI parity for the TUI's window-row 'd' /
// kill-window confirm (internal/ui: ConfirmKillWindow, tmux.Session.KillWindow).
// A TUI-only destructive action needs a scriptable equivalent; this is it.

var errSessionWindowNotFound = errors.New("session window not found")

func handleSessionWindow(profile string, args []string) {
	if len(args) == 0 {
		printSessionWindowHelp()
		os.Exit(1)
	}
	switch args[0] {
	case "close", "kill":
		handleSessionWindowClose(profile, args[1:])
	case "help", "--help", "-h":
		printSessionWindowHelp()
	default:
		fmt.Fprintf(os.Stderr, "Error: unknown session window command: %s\n", args[0])
		printSessionWindowHelp()
		os.Exit(1)
	}
}

func printSessionWindowHelp() {
	fmt.Println("Usage: agent-deck session window <command> <id|title> <window-index> [options]")
	fmt.Println()
	fmt.Println("Manage the extra tmux windows inside one session (a shell opened next to")
	fmt.Println("the agent, for instance). Mirrors the TUI's 'd' on a window sub-row.")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  close <id> <window-index>   Kill one tmux window, leaving the session's")
	fmt.Println("                              other windows intact (--json for machine output)")
	fmt.Println()
	fmt.Println("The session's last remaining window is refused, same as the TUI guard: use")
	fmt.Println("'session stop' to end the whole session instead.")
}

// handleSessionWindowClose parses `session window close <id> <index>` and
// reports the outcome of closeSessionWindow, which carries the kill's
// identity guard.
func handleSessionWindowClose(profile string, args []string) {
	fs := flag.NewFlagSet("session window close", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session window close <id|title> <window-index> [--json]")
		fmt.Println()
		fmt.Println("Kill one tmux window in a session. Refuses if it is the session's last window.")
	}
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}
	out := NewCLIOutput(*jsonOutput, false)
	inst := resolveOwnershipTarget(profile, fs.Arg(0), out)

	index, err := strconv.Atoi(fs.Arg(1))
	if err != nil {
		out.Error("window index is required and must be an integer (see 'agent-deck session show "+fs.Arg(0)+"')", ErrCodeInvalidOperation)
		os.Exit(1)
	}

	if err := closeSessionWindow(inst, index); err != nil {
		switch {
		case errors.Is(err, errSessionWindowNotFound):
			out.Error(fmt.Sprintf("window %d not found: %v", index, err), ErrCodeNotFound)
			os.Exit(2)
		case errors.Is(err, tmux.ErrLastWindow):
			out.Error(fmt.Sprintf("not killing window %d: it is the session's last window", index), ErrCodeInvalidOperation)
		case errors.Is(err, tmux.ErrWindowChanged):
			out.Error(fmt.Sprintf("not killing window %d: it changed since it was looked up, refusing", index), ErrCodeInvalidOperation)
		default:
			out.Error(fmt.Sprintf("kill window %d: %v", index, err), ErrCodeInvalidOperation)
		}
		os.Exit(1)
	}
	out.Success(fmt.Sprintf("killed window %d in session %s", index, inst.Title), map[string]interface{}{
		"success": true,
		"session": inst.ID,
		"window":  index,
	})
}

// closeSessionWindow kills window `index` of inst's live tmux session. It
// mirrors the TUI's ConfirmKillWindow action exactly: the window's stable id
// is looked up immediately before the kill, and tmux.Session.KillWindow only
// kills if that id still matches what tmux reports live, atomically with the
// session's other windows remaining — the same liveness-is-not-identity guard
// as the confirm dialog (see tmux.ErrLastWindow / tmux.ErrWindowChanged),
// just without a human in between the lookup and the kill.
func closeSessionWindow(inst *session.Instance, index int) error {
	tmuxSess := inst.GetTmuxSession()
	if tmuxSess == nil {
		return fmt.Errorf("%w: session has no live tmux window", errSessionWindowNotFound)
	}
	windowID, err := tmuxSess.WindowID(index)
	if err != nil {
		return fmt.Errorf("%w: %v", errSessionWindowNotFound, err)
	}
	if err := tmuxSess.KillWindow(index, windowID); err != nil {
		return err
	}
	tmux.RemoveCachedWindow(tmuxSess.Name, index)
	return nil
}
