package tmux

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/creack/pty"
)

// Shared attach: the size policy before every attach.
//
// The multi-client size policy itself (window-size=latest, aggressive-resize
// on; `largest` on a tmux without `latest`) is defined once, in
// windowPolicyOptions (window_policy.go): Start() applies it to the initial
// window, NewShellWindow to windows Deck opens and the after-new-window hook
// to windows opened by hand. Two tmux facts (both proven in the #2186
// follow-up reproduction) mean it also has to be re-applied right before a
// client attaches, which is what this file does:
//
//   - window-size is a WINDOW option. A session created by an older build
//     (rc.6 pinned `smallest`, the policy that gives every client larger than
//     the smallest one the "small box top-left, dots everywhere else" frame)
//     keeps its windows' values until something sets them again, so the
//     policy is applied to every existing window, not once at the session.
//   - `resize-window` flips a window to `window-size=manual` for good. Any
//     path that once resized the window (an older web bridge, a user's
//     tmux.conf) leaves it pinned until the policy is re-applied.
//
// The user's [tmux] options win over the default here exactly as on the
// creation paths: the same table resolves them.

// sharedViewWindowTargets lists the session's window IDs, falling back to
// the session name (current window) when tmux cannot be asked.
func sharedViewWindowTargets(socketName, sessionName string) []string {
	out, err := runBoundedOutput(socketName, "list-windows", "-t", sessionName, "-F", "#{window_id}")
	if err != nil {
		return []string{sessionName}
	}
	var targets []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if id := strings.TrimSpace(line); strings.HasPrefix(id, "@") {
			targets = append(targets, id)
		}
	}
	if len(targets) == 0 {
		return []string{sessionName}
	}
	return targets
}

// ApplySharedViewSize installs the shared-attach size policy on every window
// of the session named sessionName on socketName. It runs before every
// attach (TUI Enter, `session attach` on a remote, the web and embedded
// clients). A tmux too old for `latest` gets the fallback policy, as on
// every other path. Best effort: a failure is logged and never blocks the
// attach.
func ApplySharedViewSize(socketName, sessionName string, overrides map[string]string) {
	targets := sharedViewWindowTargets(socketName, sessionName)
	args := windowPolicyArgs(targets, overrides, hostTmuxVersionString(), "set-option", "-w", "-q")
	if len(args) == 0 {
		return
	}
	if err := runBoundedMutation(socketName, args...); err != nil {
		statusLog.Warn("shared_view_size_failed",
			slog.String("session", sessionName),
			slog.String("error", err.Error()))
	}
}

// applySharedViewSize is ApplySharedViewSize for this session, with its
// own [tmux] option overrides.
func (s *Session) applySharedViewSize() {
	ApplySharedViewSize(s.SocketName, s.Name, s.OptionOverrides)
}

// prepareSharedAttach runs right before a client is attached: it records who
// is already viewing (for the "also viewing" notice) and installs the size
// policy so the new client gets the window at its own size.
func (s *Session) prepareSharedAttach(ctx context.Context) []Viewer {
	others, _ := ListViewers(ctx, s.SocketName, s.Name)
	s.applySharedViewSize()
	return others
}

// sharedAttachSettle is how long finishSharedAttach waits for tmux to
// register the new client and resize the window before judging the fit.
const sharedAttachSettle = 500 * time.Millisecond

// finishSharedAttach runs beside a live attach: it tells the new client who
// else is viewing, then checks that the window really follows this client
// (tty is the attaching terminal, whose size is the client's).
func (s *Session) finishSharedAttach(ctx context.Context, others []Viewer, tty *os.File) {
	s.announceOtherViewers(ctx, others, configErrorViewWindow)
	select {
	case <-ctx.Done():
		return
	case <-time.After(sharedAttachSettle):
	}
	if ws, err := pty.GetsizeFull(tty); err == nil {
		s.logSharedViewFit(ctx, int(ws.Cols), int(ws.Rows))
	}
}

// logSharedViewFit is the post-attach diagnostic for a window that is still
// smaller than the terminal that just attached. Under `latest` the attaching
// client becomes the window's latest client, so this only fires when the
// policy could not take effect: a user override (`window-size=smallest` in
// [tmux] options or tmux.conf), a `manual` size re-pinned between apply and
// attach, or a tmux without `latest`. It records who else is attached and at
// what size so the debug log names the client that pins the window. tmux
// refuses `refresh-client -C` for anything but a control client ("not a
// control client", tmux >= 3.2), so there is no size the attaching client
// could claim here; nobody else is ever detached.
func (s *Session) logSharedViewFit(ctx context.Context, cols, rows int) {
	if cols <= 0 || rows <= 0 {
		return
	}
	out, err := commandOutput(s.tmuxCmdContext(ctx, "display-message", "-p", "-t", s.Name,
		"#{window_width}x#{window_height}\t#{window-size}"))
	if err != nil {
		return
	}
	size, policy, _ := strings.Cut(strings.TrimRight(string(out), "\r\n"), "\t")
	width, height, ok := parseSize(size)
	// A one-row status line is not the window's to fill.
	if !ok || (width >= cols && height >= rows-1) {
		return
	}
	viewers, _ := ListViewers(ctx, s.SocketName, s.Name)
	statusLog.Debug("shared_view_window_smaller_than_client",
		slog.String("session", s.Name),
		slog.String("client", fmt.Sprintf("%dx%d", cols, rows)),
		slog.String("window", size),
		slog.String("window_size_policy", policy),
		slog.String("other_clients", FormatViewers(viewers)))
}
