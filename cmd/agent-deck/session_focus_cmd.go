package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/telemetry"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
	"github.com/asheshgoplani/agent-deck/internal/ui"
)

// handleSessionAttach attaches to a session interactively
func handleSessionAttach(profile string, args []string) {
	fs := flag.NewFlagSet("session attach", flag.ContinueOnError)

	detachByte := ui.ResolvedDetachByte(session.GetHotkeyOverrides())
	detachLabel := ui.DetachByteLabel(detachByte)

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session attach <id|title>")
		fmt.Println()
		fmt.Println("Attach to a session interactively.")
		fmt.Printf("Press %s to detach.\n", detachLabel)
	}

	if err := parseCLIFlags(fs, normalizeArgs(fs, args)); err != nil {
		exitCLI(1)
	}

	identifier := fs.Arg(0)

	// Load sessions
	_, instances, _, err := loadSessionData(profile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		exitCLI(1)
	}

	// Resolve session (allow current session detection)
	inst, errMsg, errCode := ResolveSessionOrCurrent(identifier, instances)
	if inst == nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", errMsg)
		if errCode == ErrCodeNotFound {
			exitCLI(2)
		}
		exitCLI(1)
		return // unreachable, satisfies staticcheck SA5011
	}

	// Check if session exists
	if !inst.Exists() {
		fmt.Fprintf(os.Stderr, "Error: session '%s' is not running\n", inst.Title)
		exitCLI(1)
	}

	// Attach to the session
	tmuxSession := inst.GetTmuxSession()
	if tmuxSession == nil {
		fmt.Fprintf(os.Stderr, "Error: no tmux session for '%s'\n", inst.Title)
		exitCLI(1)
	}

	// Create context for attach
	ctx := context.Background()

	attachedAt := time.Now()
	if err := tmuxSession.Attach(ctx, detachByte); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to attach: %v\n", err)
		exitCLI(1)
	}
	telemetry.Attached(inst.Tool, telemetry.AttachCLI, time.Since(attachedAt))
}

// liveSwitcher attempts to move the attached terminal straight into a session's
// tmux pane (the Ctrl+b N quick-switch path), so a notification click lands you
// in the session even while the TUI is paused inside another attach. Injected
// into routeFocus so the attached-vs-list routing is unit-testable without a
// real tmux server.
type liveSwitcher interface {
	// switchInto moves the client attached to inst's tmux server into inst's
	// pane. Returns switched=true iff a client was attached and moved; false
	// (no error) when inst has no live pane or no client is attached on its
	// socket, signalling the caller to fall back to a focus_request row.
	switchInto(inst *session.Instance) (bool, error)
}

// tmuxLiveSwitcher is the production liveSwitcher. It mirrors the Ctrl+b N
// quick-switch: tmux switch-client + an ack-signal write, both of which work
// while the Bubble Tea TUI is suspended during tea.Exec.
type tmuxLiveSwitcher struct{}

func (tmuxLiveSwitcher) switchInto(inst *session.Instance) (bool, error) {
	if inst == nil || !inst.Exists() {
		return false, nil
	}
	ts := inst.GetTmuxSession()
	if ts == nil || ts.Name == "" {
		return false, nil
	}
	// Query/switch on the target's own socket: switch-client only works when the
	// attached client and the target session share a tmux server, so a client on
	// a different socket simply yields switched=false and the focus_request
	// fallback takes over (matches the Ctrl+b N same-server limitation).
	return tmux.SwitchAttachedClients(inst.TmuxSocketName, ts.Name, inst.ID)
}

// clientDetacher detaches the user's currently-attached tmux client when the
// live switch could not move it. switch-client cannot cross tmux servers, so a
// notification target on a different socket than the attached session yields
// switched=false; detaching that client makes the paused TUI resume and consume
// the focus_request (attaching the target on its own socket) instead of the
// switch silently waiting for a manual Ctrl+Q. Injected into routeFocus so the
// cross-socket routing is unit-testable without a real tmux server.
type clientDetacher interface {
	// detachClientsOn detaches every real (non-control) client attached on any
	// of sockets. Returns detached=true iff at least one client was detached.
	detachClientsOn(sockets []string) (bool, error)
}

// tmuxClientDetacher is the production clientDetacher.
type tmuxClientDetacher struct{}

func (tmuxClientDetacher) detachClientsOn(sockets []string) (bool, error) {
	return tmux.DetachClientsOnSockets(sockets...)
}

// findFocusInstance returns the instance with the given id, or nil.
func findFocusInstance(instances []*session.Instance, id string) *session.Instance {
	for _, inst := range instances {
		if inst.ID == id {
			return inst
		}
	}
	return nil
}

// focusOtherSockets returns the distinct tmux socket names used by instances,
// excluding exclude (the target's own socket, where the live switch already
// looked). Order-preserving and deduped. These are the sockets that may host
// the user's currently-attached client when the target lives elsewhere.
func focusOtherSockets(instances []*session.Instance, exclude string) []string {
	seen := map[string]bool{exclude: true}
	var out []string
	for _, inst := range instances {
		s := inst.TmuxSocketName
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// routeFocus drives `session focus <id>`. With --attach it first tries a live
// switch-while-attached (so the click lands you straight in the session even
// when the TUI is paused inside another attach); if no client is attached to the
// target's tmux server it falls back to the foreground focus_request row, which
// the TUI consumes on its next tick. Without --attach it always writes the
// (select-only) focus_request. Split out of handleSessionFocus so it is
// unit-testable without os.Exit; switcher is injected for the same reason.
func routeFocus(db *statedb.StateDB, instances []*session.Instance, id string, nowNano int64, attach bool, switcher liveSwitcher, detacher clientDetacher) error {
	if id == "" {
		return fmt.Errorf("session focus requires an <id>")
	}
	inst := findFocusInstance(instances, id)
	if inst == nil {
		return fmt.Errorf("%w: %q", errFocusNotFound, id)
	}
	if attach && switcher != nil {
		// The contract reserves (false, nil) for the benign fallback (no live
		// pane / no client attached on the socket); a non-nil error is a real
		// tmux failure that must surface, not be silently swallowed into the
		// fallback path.
		switched, err := switcher.switchInto(inst)
		if err != nil {
			return err
		}
		if switched {
			return nil
		}
		// Live switch couldn't move the client: it's attached to a different tmux
		// server than the target (switch-client can't cross servers). Write the
		// focus_request FIRST, then detach that client so agent-deck's paused
		// attach returns and the resumed TUI consumes the row on its next tick —
		// attaching the target on its own socket, instead of waiting for a manual
		// Ctrl+Q. When no client is attached elsewhere (e.g. the TUI is already in
		// the list view), the detach is a harmless no-op and the row is consumed
		// normally.
		if detacher != nil {
			if err := session.WriteFocusRequestAttach(db, id, nowNano, attach); err != nil {
				return err
			}
			// The focus_request is already persisted, so a detach failure still
			// leaves the row to be consumed on the next tick — but surface it so
			// the immediate-switch path's failure isn't hidden.
			if _, err := detacher.detachClientsOn(focusOtherSockets(instances, inst.TmuxSocketName)); err != nil {
				return err
			}
			return nil
		}
	}
	return session.WriteFocusRequestAttach(db, id, nowNano, attach)
}

// tuiClientDetacher detaches one tmux client by pid: the client the TUI is
// paused in, as it recorded under session.TUIAttachKey. Injected into
// routeRemoteFocus so it is unit-testable without a real tmux server.
type tuiClientDetacher interface {
	detachClient(socket string, pid int) (bool, error)
}

// tmuxPIDDetacher is the production tuiClientDetacher.
type tmuxPIDDetacher struct{}

func (tmuxPIDDetacher) detachClient(socket string, pid int) (bool, error) {
	return tmux.DetachClientByPID(socket, pid)
}

// routeRemoteFocus drives `session focus --remote <name> <id>` (#2704). The id
// belongs to the remote, so it is not checked here (no SSH): the TUI looks it
// up in its remote list. With --attach it writes the request FIRST and then,
// when the TUI recorded that it is paused inside a local attach, detaches that
// one client so the TUI comes back and consumes the request. Any other client
// (web viewers, other terminals, the embedded pane) stays attached, and with
// no record (TUI on the list, embedded layout, or an older TUI that would
// drop the request anyway) nothing is detached. Select-only never detaches.
func routeRemoteFocus(db *statedb.StateDB, remote, id string, nowNano int64, attach bool, remoteExists func(string) (bool, error), detacher tuiClientDetacher) error {
	if id == "" {
		return fmt.Errorf("session focus requires an <id>")
	}
	exists, err := remoteExists(remote)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%w: %q", errFocusRemoteUnknown, remote)
	}
	if err := session.WriteFocusRequestFull(db, session.FocusRequest{ID: id, TS: nowNano, Attach: attach, Remote: remote}); err != nil {
		return err
	}
	if !attach || detacher == nil {
		return nil
	}
	rec, ok := session.ReadTUIAttach(db)
	if !ok {
		return nil
	}
	// The request is already persisted, so a detach failure still leaves it
	// to be consumed once the user is back in the list; surface it anyway.
	_, err = detacher.detachClient(rec.Socket, rec.ClientPID)
	return err
}

// focusRemoteExists reports whether name is a configured remote.
func focusRemoteExists(name string) (bool, error) {
	_, exists, err := resolveRemoteConfig(name)
	return exists, err
}

// resolveAndWriteFocus validates id against the loaded instances and, on a
// match, writes the focus_request row. Retained as the switcher-less path
// (select-only / no live switch); delegates to routeFocus.
func resolveAndWriteFocus(db *statedb.StateDB, instances []*session.Instance, id string, nowNano int64, attach bool) error {
	return routeFocus(db, instances, id, nowNano, attach, nil, nil)
}

// handleSessionFocus signals the running TUI (same profile) to select <id> on
// its next poll. Fire-and-forget: no stdout on success. Unknown id exits 2.
// With --attach, the TUI opens/attaches the session instead of only selecting it.
func handleSessionFocus(profile string, args []string) {
	fs := flag.NewFlagSet("session focus", flag.ContinueOnError)
	attach := fs.Bool("attach", false, "Open/attach the session, not just select it")
	remote := fs.String("remote", "", "Focus a session of this configured remote (see 'agent-deck remote list'); <id> is the remote's own session id")
	jsonOutput := fs.Bool("json", false, "Output the written request as JSON")
	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session focus [--remote <name>] <id> [--attach] [--json]")
		fmt.Println()
		fmt.Println("Signal the running agent-deck TUI (same profile) to reveal and")
		fmt.Println("select the session with the given instance id on its next refresh.")
		fmt.Println("With --attach, the TUI opens/attaches the session (as if you")
		fmt.Println("pressed Enter on it) instead of only moving the cursor.")
		fmt.Println("With --remote, <id> is a session on that remote: the TUI selects")
		fmt.Println("its row under Remotes. Nothing local is created.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}
	if err := parseCLIFlags(fs, normalizeArgs(fs, args)); err != nil {
		exitCLI(1)
	}

	id := fs.Arg(0)
	out := NewCLIOutput(*jsonOutput, false)
	fail := func(err error) {
		code := ErrCodeInvalidOperation
		if errors.Is(err, errFocusNotFound) || errors.Is(err, errFocusRemoteUnknown) {
			code = ErrCodeNotFound
		}
		if *jsonOutput {
			out.Error(err.Error(), code)
		} else {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
		if code == ErrCodeNotFound {
			exitCLI(2)
		}
		exitCLI(1)
	}

	storage, instances, _, err := loadSessionData(profile)
	if err != nil {
		fail(err)
	}

	db := storage.GetDB()
	if db == nil {
		fail(errors.New("no state database available"))
	}

	var switcher liveSwitcher
	var detacher clientDetacher
	if *attach {
		switcher = tmuxLiveSwitcher{}
		detacher = tmuxClientDetacher{}
	}
	if *remote != "" {
		var tuiDetacher tuiClientDetacher
		if *attach {
			tuiDetacher = tmuxPIDDetacher{}
		}
		err = routeRemoteFocus(db, *remote, id, time.Now().UnixNano(), *attach, focusRemoteExists, tuiDetacher)
	} else {
		err = routeFocus(db, instances, id, time.Now().UnixNano(), *attach, switcher, detacher)
	}
	if err != nil {
		fail(err)
	}
	if *jsonOutput {
		// Same envelope keys as the error path (success/error/code).
		data := map[string]interface{}{
			"success": true,
			"id":      id,
			"attach":  *attach,
		}
		if *remote != "" {
			data["remote"] = *remote
		}
		out.Success("", data)
	}
}

// formatViewersLine renders a viewer list for humans: the names, sizes and
// activity of everyone attached, "none" for nobody, "unknown" when tmux
// could not be asked.
func formatViewersLine(viewers []tmux.Viewer, known bool) string {
	switch {
	case !known:
		return "unknown"
	case len(viewers) == 0:
		return "none"
	}
	return tmux.FormatViewers(viewers)
}

// handleSessionViewers prints who is attached to a session: the CLI form of
// the TUI's viewers badge and the "also viewing" notice on attach.
func handleSessionViewers(profile string, args []string) {
	fs := flag.NewFlagSet("session viewers", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session viewers [id|title] [--json]")
		fmt.Println()
		fmt.Println("List the terminals attached to a session (who else is viewing it).")
		fmt.Println("If no ID is provided, auto-detects the current session.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}
	if err := parseCLIFlags(fs, normalizeArgs(fs, args)); err != nil {
		exitCLI(1)
	}
	out := NewCLIOutput(*jsonOutput, false)

	_, instances, _, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		exitCLI(1)
	}
	inst, errMsg, errCode := ResolveSessionOrCurrent(fs.Arg(0), instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		if errCode == ErrCodeNotFound {
			exitCLI(2)
		}
		exitCLI(1)
		return // unreachable, satisfies staticcheck SA5011
	}
	viewers, known := inst.Viewers(context.Background())
	data := map[string]interface{}{
		"id":    inst.ID,
		"title": inst.Title,
		"known": known,
	}
	if known {
		data["viewers"] = viewers
	}
	var human strings.Builder
	fmt.Fprintf(&human, "Session: %s\n", inst.Title)
	fmt.Fprintf(&human, "Viewers: %s\n", formatViewersLine(viewers, known))
	for _, v := range viewers {
		fmt.Fprintf(&human, "  %s\t%s\n", v.Label(time.Now()), v.TTY)
	}
	out.Print(human.String(), data)
}
