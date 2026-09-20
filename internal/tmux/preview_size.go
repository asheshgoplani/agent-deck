package tmux

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// PreviewSizer fits newly selected/replaced windows and viewport changes.
// Remember the last request so independent passive previews do not continually
// fight over one shared window. Real attach clients always take precedence.
type PreviewSizer struct {
	mu   sync.Mutex
	last string
}

// Fit reconciles the selected window with a detached terminal viewport.
// windowIndex < 0 follows the current window, matching CaptureFullHistory.
func (p *PreviewSizer) Fit(s *Session, windowIndex, cols, rows int) error {
	if cols < 1 || rows < 1 {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	target := s.Name
	if windowIndex >= 0 {
		target = s.windowTarget(windowIndex)
	}
	out, err := commandOutput(s.tmuxCmdContext(ctx, "display-message", "-p", "-t", target,
		"#{window_id}|#{window-size}|#{status}|#{session_attached}|#{window_linked}|#{window_panes}|#{pane_pid}|#{window_width}|#{window_height}",
		";", "list-clients", "-t", s.Name, "-F", "#{client_control_mode}"))
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	fields := strings.Split(lines[0], "|")
	if len(fields) != 9 {
		return fmt.Errorf("unexpected preview window state %q", out)
	}
	// Deck's background control pipes impose no size. Do not let those
	// prevent a fit; sized control clients retain precedence when the automatic
	// window policy is restored below. Interactive viewers are never resized.
	for _, control := range lines[1:] {
		if control != "1" {
			p.last = ""
			return nil
		}
	}
	controls := strconv.Itoa(len(lines) - 1)
	if fields[3] != controls || fields[4] != "0" || fields[5] != "1" {
		p.last = ""
		return nil
	}
	policy := fields[1]
	switch policy {
	case "manual":
		p.last = ""
		return nil
	case "latest", "largest", "smallest":
	default:
		return fmt.Errorf("unexpected window-size policy %q", policy)
	}
	// Use the immutable window ID, not a possibly renumbered window index.
	id, err := strconv.Atoi(strings.TrimPrefix(fields[0], "@"))
	if err != nil || id < 0 || !strings.HasPrefix(fields[0], "@") {
		return fmt.Errorf("invalid window ID %q", fields[0])
	}
	window := fmt.Sprintf("@%d", id)
	switch fields[2] {
	case "on":
		rows--
	case "off":
	default:
		count, err := strconv.Atoi(fields[2])
		if err != nil {
			return err
		}
		rows -= count
	}
	if rows < 1 {
		return nil
	}
	// A replacement pane (including CLI respawn) changes pane_pid even when
	// its window ID is retained. A selection or viewport change changes this key.
	key := fmt.Sprintf("%s/%s/%s/%s/%s/%dx%d", s.SocketName, s.Name, fields[0], fields[6], controls, cols, rows)
	if p.last == key {
		return nil
	}
	if fields[7] == strconv.Itoa(cols) && fields[8] == strconv.Itoa(rows) {
		p.last = key
		return nil
	}
	local, err := commandOutput(s.tmuxCmdContext(ctx, "show-options", "-wqv", "-t", window, "window-size"))
	if err != nil {
		return err
	}
	restore := "set-option -w -t " + window + " window-size " + policy
	if strings.TrimSpace(string(local)) == "" {
		restore = "set-option -wu -t " + window + " window-size"
	}
	// Recheck ownership in tmux immediately before mutation; a viewer may have
	// attached while we queried the options. if-shell -F evaluates a tmux format,
	// never a shell. resize-window temporarily sets manual, so restore the exact
	// local/inherited policy in the same command queue.
	guard := "#{&&:#{==:#{session_attached}," + controls + "},#{&&:#{==:#{window_linked},0},#{&&:#{==:#{window_panes},1},#{==:#{window-size}," + policy + "}}}}"
	commands := fmt.Sprintf("resize-window -t %s -x %d -y %d ; %s", window, cols, rows, restore)
	out, err = commandOutput(s.tmuxCmdContext(ctx, "if-shell", "-F", "-t", window, guard, commands+" ; display-message -p resized"))
	if err == nil && strings.TrimSpace(string(out)) == "resized" {
		p.last = key
	}
	return err
}
