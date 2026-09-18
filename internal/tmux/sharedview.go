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

// Shared-attach size policy.
//
// When two people view one session (the maintainer through the remote deck
// at 200x60, a teammate from a terminal at 120x40) tmux has to pick ONE pane
// size for the window. rc.6 (#2186) pinned `window-size=smallest`, which is
// the policy that produces the "small box top-left, dots everywhere else"
// frame for every client larger than the smallest one; `largest` produces
// the mirror image (a synthetic size no client can show completely) as soon
// as the client geometries cross. `latest` sizes the window to the client
// that most recently attached or pressed a key, so the person actually
// using the session always sees it full-size and nobody's idle terminal can
// pin it. tmux < 3.1 has no `latest`; there the closest policy is `largest`
// with aggressive-resize so at least the active window follows its viewers.
//
// Two tmux facts drive where the policy is applied (both proven in the
// #2186 follow-up reproduction):
//
//   - window-size is a WINDOW option. `set-option -t <session> window-size`
//     only configures the session's current window; windows created later
//     take the server default (`latest` since 3.1, `smallest` before). So the
//     policy is applied to every window explicitly (see
//     (*Session).applySharedViewSize) rather than once at the session.
//   - `resize-window` flips a window to `window-size=manual` for good. Any
//     path that once resized the window (an older web bridge, a user's
//     tmux.conf) leaves it pinned, so the policy is re-applied at every
//     attach, not only at creation.
const (
	sharedViewWindowSize         = "latest"
	sharedViewWindowSizeFallback = "largest"
)

// sharedViewOptions lists the per-window options the shared-attach policy
// installs, with the user's [tmux] options overriding each value.
func sharedViewOptions(overrides map[string]string, windowSize string) [][2]string {
	opts := [][2]string{
		{"window-size", windowSize},
		{"aggressive-resize", "on"},
	}
	for i := range opts {
		if v, ok := overrides[opts[i][0]]; ok {
			opts[i][1] = v
		}
	}
	return opts
}

// sharedViewOptionArgs builds one tmux command chain that installs the
// shared-attach policy on every window in targets. A window ID (@n) is
// used rather than session:index so the chain cannot be redirected by a
// renumber between listing and setting. When the caller could not list the
// windows it passes the session name: tmux then configures the session's
// current window, which is the one an attaching client is about to see.
func sharedViewOptionArgs(targets []string, overrides map[string]string, windowSize string) []string {
	return sharedViewSetArgs(targets, overrides, windowSize, "set-option", "-w", "-q")
}

// shellWindowOptionArgs is the NewShellWindow variant of sharedViewOptionArgs:
// -o keeps an option a hook already set on the brand-new window.
func shellWindowOptionArgs(windowID string, overrides map[string]string, windowSize string) []string {
	return sharedViewSetArgs([]string{windowID}, overrides, windowSize, "set-window-option", "-oq")
}

// sharedViewSetArgs chains one `setCmd -t <target> <option> <value>` per
// target and option, separated by ";".
func sharedViewSetArgs(targets []string, overrides map[string]string, windowSize string, setCmd ...string) []string {
	var args []string
	for _, target := range targets {
		for _, opt := range sharedViewOptions(overrides, windowSize) {
			if len(args) > 0 {
				args = append(args, ";")
			}
			args = append(args, setCmd...)
			args = append(args, "-t", target, opt[0], opt[1])
		}
	}
	return args
}

// applySharedViewPolicy runs apply with `latest`, then with the fallback
// policy for a tmux without it. The returned error carries both failures.
func applySharedViewPolicy(apply func(windowSize string) error) error {
	err := apply(sharedViewWindowSize)
	if err == nil {
		return nil
	}
	fallbackErr := apply(sharedViewWindowSizeFallback)
	if fallbackErr == nil {
		return nil
	}
	return fmt.Errorf("%s: %w; %s: %v", sharedViewWindowSize, err, sharedViewWindowSizeFallback, fallbackErr)
}

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
// of the session named sessionName on socketName. It runs at creation and
// before every attach (TUI Enter, `session attach` on a remote, the web and
// embedded clients). A tmux too old for `latest` gets the fallback policy.
// Best effort: a failure is logged and never blocks the attach.
func ApplySharedViewSize(socketName, sessionName string, overrides map[string]string) {
	targets := sharedViewWindowTargets(socketName, sessionName)
	err := applySharedViewPolicy(func(windowSize string) error {
		return runBoundedMutation(socketName, sharedViewOptionArgs(targets, overrides, windowSize)...)
	})
	if err != nil {
		statusLog.Warn("shared_view_size_failed",
			slog.String("session", sessionName),
			slog.String("error", err.Error()))
	}
}

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
	out, err := s.tmuxCmdContext(ctx, "display-message", "-p", "-t", s.Name,
		"#{window_width}x#{window_height}\t#{window-size}").Output()
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
