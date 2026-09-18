package tmux

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

// configErrorViewWindow bounds how long an attach watches for tmux's
// config-error view. tmux enters that view synchronously while it processes
// the attach, so the pane state is final as soon as the client is attached;
// the window only covers a slow server or a client that is still connecting.
const configErrorViewWindow = 2 * time.Second

// configErrorViewPoll is the interval between pane-state probes.
const configErrorViewPoll = 100 * time.Millisecond

// DismissConfigErrorView leaves tmux's config-error view if the session's
// active pane is sitting in it, and reports whether it did.
//
// When ~/.tmux.conf has an error (g14: `invalid option: extended-keys-format`
// on tmux 3.4), tmux records the cause at server start and shows it in the
// FIRST client that attaches by switching the active pane into view-mode
// (window renamed "[tmux]", a `[1/1]` marker top-right). agent-deck creates
// its servers detached, so that first client is the attach view of the deck
// itself: the user sees the warning line over the agent's pane, every key goes
// to the copy/view key table instead of the agent, and because the mode
// survives a detach the same frame comes back on every later attach. The
// underlying pane is alive the whole time (capture-pane shows the agent).
//
// Only view-mode is cancelled: copy-mode is what a user gets by scrolling back
// in another client and must be left alone. Polling stops early once a client
// is attached and the pane is not in view-mode, so remotes with a clean config
// pay one or two probes, not the whole window.
func (s *Session) DismissConfigErrorView(ctx context.Context, window time.Duration) bool {
	deadline := time.Now().Add(window)
	for {
		attached, mode, err := s.activePaneMode(ctx)
		if err == nil {
			if mode == "view-mode" {
				if err := s.tmuxCmdContext(ctx, "send-keys", "-t", s.Name, "-X", "cancel").Run(); err == nil {
					statusLog.Info("config_error_view_dismissed", slog.String("session", s.Name))
					return true
				}
			} else if attached {
				// The pane state is final once a client is attached: either
				// no mode at all or one the user owns (copy-mode).
				return false
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(configErrorViewPoll):
		}
	}
}

// activePaneMode reports whether a client is attached to the session and the
// mode (empty when none) of the current window's active pane, which is the
// pane tmux uses to display config causes.
func (s *Session) activePaneMode(ctx context.Context) (attached bool, mode string, err error) {
	out, err := s.tmuxCmdContext(ctx, "display-message", "-p", "-t", s.Name,
		"#{session_attached}\t#{pane_mode}").Output()
	if err != nil {
		return false, "", err
	}
	// Trim only the newline: an empty mode leaves a trailing tab that a
	// TrimSpace would swallow along with the field separator.
	attachedField, mode, ok := strings.Cut(strings.TrimRight(string(out), "\r\n"), "\t")
	if !ok {
		return false, "", nil
	}
	return attachedField != "0", strings.TrimSpace(mode), nil
}
