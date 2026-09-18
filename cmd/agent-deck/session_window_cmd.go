package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// `agent-deck session window` — CLI parity for the TUI's window-row 'd' /
// kill-window confirm (internal/ui: ConfirmKillWindow, tmux.Session.KillWindow).
// A TUI-only destructive action needs a scriptable equivalent; this is it.

var (
	errSessionWindowNotFound = errors.New("session window not found")
	// errSessionWindowConfirmRequired is the CLI's stand-in for the TUI's
	// confirm dialog: nothing is killed until the caller passes --yes.
	errSessionWindowConfirmRequired = errors.New("refusing to close a window without --yes")
)

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
	fmt.Println("Usage: agent-deck session window <command> <id|title> <window> [options]")
	fmt.Println()
	fmt.Println("Manage the extra tmux windows inside one session (a shell opened next to")
	fmt.Println("the agent, for instance). Mirrors the TUI's 'd' on a window sub-row.")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  close <id> <window> --yes   Kill one tmux window, leaving the session's")
	fmt.Println("                              other windows intact. <window> is the index")
	fmt.Println("                              shown in the TUI ([1]) or the stable tmux id (@12).")
	fmt.Println("                              Without --yes nothing is killed: the window it")
	fmt.Println("                              would close is printed and the command exits 1.")
	fmt.Println("  kill                        Alias for close.")
	fmt.Println()
	fmt.Println("Options:")
	fmt.Println("  --yes, --force              Actually kill the window (the CLI equivalent of")
	fmt.Println("                              answering 'y' in the TUI's confirm dialog)")
	fmt.Println("  --json                      Machine output: {session, window_id, window_index,")
	fmt.Println("                              window_name, closed}")
	fmt.Println()
	fmt.Println("The session's last remaining window is refused, same as the TUI guard: use")
	fmt.Println("'session stop' to end the whole session instead.")
}

func printSessionWindowCloseUsage() {
	fmt.Println("Usage: agent-deck session window close <id|title> <window-index|@window-id> --yes [--json]")
	fmt.Println()
	fmt.Println("Kill one tmux window in a session. Requires --yes (or --force); refuses the")
	fmt.Println("session's last window. --json prints {session, window_id, window_index,")
	fmt.Println("window_name, closed}.")
}

// handleSessionWindowClose parses `session window close <id> <window>` and
// reports the outcome of closeSessionWindow, which carries the kill's
// identity guard.
func handleSessionWindowClose(profile string, args []string) {
	fs := flag.NewFlagSet("session window close", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	yes := fs.Bool("yes", false, "Actually kill the window (without this only the target is printed)")
	force := fs.Bool("force", false, "Alias for --yes")
	fs.Usage = printSessionWindowCloseUsage
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}
	if fs.NArg() < 2 {
		printSessionWindowCloseUsage()
		os.Exit(1)
	}
	out := NewCLIOutput(*jsonOutput, false)
	inst := resolveOwnershipTarget(profile, fs.Arg(0), out)
	ref := fs.Arg(1)

	win, err := closeSessionWindow(inst, ref, *yes || *force)
	if errors.Is(err, errSessionWindowNotFound) {
		out.Error(fmt.Sprintf("window %s not found: %v", ref, err), ErrCodeNotFound)
		os.Exit(2)
	}
	if err != nil {
		// Every remaining refusal names the window that was resolved.
		label := describeSessionWindow(win)
		switch {
		case errors.Is(err, errSessionWindowConfirmRequired):
			out.ErrorWithData(fmt.Sprintf("would close %s in session %s; pass --yes to close it", label, inst.Title),
				ErrCodeInvalidOperation, sessionWindowPayload(inst, win, false, "confirm_required"))
		case errors.Is(err, tmux.ErrLastWindow):
			out.ErrorWithData(fmt.Sprintf("not killing %s: it is the session's last window", label),
				ErrCodeInvalidOperation, sessionWindowPayload(inst, win, false, "last_window"))
		case errors.Is(err, tmux.ErrWindowChanged):
			out.ErrorWithData(fmt.Sprintf("not killing %s: it changed since it was looked up, refusing", label),
				ErrCodeInvalidOperation, sessionWindowPayload(inst, win, false, "window_changed"))
		default:
			out.Error(fmt.Sprintf("kill %s: %v", label, err), ErrCodeInvalidOperation)
		}
		os.Exit(1)
	}
	out.Success(fmt.Sprintf("closed %s in session %s", describeSessionWindow(win), inst.Title), sessionWindowPayload(inst, win, true, ""))
}

func describeSessionWindow(win tmux.WindowInfo) string {
	return fmt.Sprintf("window %d (%s) %q", win.Index, win.ID, win.Name)
}

// sessionWindowPayload is the --json shape of `session window close`: the
// window it acted on (or would act on), named by every handle a caller has.
// reason is omitted on success (closed true) and names why a refusal path
// left the window alone otherwise (e.g. "last_window", "window_changed",
// "confirm_required").
func sessionWindowPayload(inst *session.Instance, win tmux.WindowInfo, closed bool, reason string) map[string]interface{} {
	payload := map[string]interface{}{
		"session":      inst.ID,
		"window_id":    win.ID,
		"window_index": win.Index,
		"window_name":  win.Name,
		"closed":       closed,
	}
	if reason != "" {
		payload["reason"] = reason
	}
	return payload
}

// resolveSessionWindow looks up one live window of inst's tmux session from
// a user-supplied reference: the TUI's window index ("1") or the stable tmux
// window id ("@12").
func resolveSessionWindow(inst *session.Instance, ref string) (tmux.WindowInfo, error) {
	tmuxSess := inst.GetTmuxSession()
	if tmuxSess == nil {
		return tmux.WindowInfo{}, fmt.Errorf("%w: session has no live tmux window", errSessionWindowNotFound)
	}
	windowID := ref
	if !strings.HasPrefix(ref, "@") {
		index, err := strconv.Atoi(ref)
		if err != nil {
			return tmux.WindowInfo{}, fmt.Errorf("%w: %q is neither a window index nor a window id (@N)", errSessionWindowNotFound, ref)
		}
		if windowID, err = tmuxSess.WindowID(index); err != nil {
			return tmux.WindowInfo{}, fmt.Errorf("%w: %v", errSessionWindowNotFound, err)
		}
	}
	win, err := tmuxSess.Window(windowID)
	if err != nil {
		return tmux.WindowInfo{}, fmt.Errorf("%w: no window %s in session %s", errSessionWindowNotFound, windowID, tmuxSess.Name)
	}
	return win, nil
}

// closeSessionWindow resolves ref (index or @id) to one live window of inst's
// tmux session and, when confirmed, kills it. It mirrors the TUI's
// ConfirmKillWindow action: the window is identified by its stable id and
// tmux.Session.KillWindow only kills if that id is still in the session with
// the same name and other windows remain (tmux.ErrWindowChanged /
// tmux.ErrLastWindow). Without confirmed it returns the resolved window and
// errSessionWindowConfirmRequired so the caller can show what --yes would
// close. The resolved window is returned alongside any error once it is
// known, so refusals can name it.
func closeSessionWindow(inst *session.Instance, ref string, confirmed bool) (tmux.WindowInfo, error) {
	win, err := resolveSessionWindow(inst, ref)
	if err != nil {
		return tmux.WindowInfo{}, err
	}
	if !confirmed {
		return win, errSessionWindowConfirmRequired
	}
	tmuxSess := inst.GetTmuxSession()
	if err := tmuxSess.KillWindow(win.ID, win.Name); err != nil {
		return win, err
	}
	tmux.RemoveCachedWindow(tmuxSess.Name, win.ID)
	return win, nil
}
