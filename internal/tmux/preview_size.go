package tmux

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// FitDetachedPreview reconciles a detached session window with the viewport
// that will display its captured terminal content. It is intentionally a
// one-shot lifecycle operation: callers invoke it after a restart instead of
// on every preview poll.
//
// A real interactive viewer owns the window geometry, so this is a no-op while
// one is attached. resize-window temporarily pins window-size=manual; restore
// the exact local/inherited policy afterwards so the next attach keeps normal
// shared-view sizing semantics.
func (s *Session) FitDetachedPreview(cols, rows int) error {
	if s == nil || cols < 1 || rows < 1 {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	viewers, err := ListViewers(ctx, s.SocketName, s.Name)
	cancel()
	if err != nil {
		return fmt.Errorf("check preview viewers: %w", err)
	}
	if len(viewers) != 0 {
		return nil
	}

	statusOut, err := runBoundedOutput(s.SocketName, "display-message", "-p", "-t", s.Name, "#{status}")
	if err != nil {
		return fmt.Errorf("read tmux status rows: %w", err)
	}
	paneRows, err := detachedPreviewPaneRows(rows, strings.TrimSpace(string(statusOut)))
	if err != nil {
		return err
	}
	if paneRows < 1 {
		return nil
	}

	localPolicyOut, err := runBoundedOutput(s.SocketName, "show-options", "-wqv", "-t", s.Name, "window-size")
	if err != nil {
		return fmt.Errorf("read window-size policy: %w", err)
	}
	localPolicy := strings.TrimSpace(string(localPolicyOut))

	// Narrow the attach race after the option probes. If a real viewer arrived,
	// leave its geometry alone. Control-mode clients are filtered by ListViewers.
	ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
	viewers, err = ListViewers(ctx, s.SocketName, s.Name)
	cancel()
	if err != nil {
		return fmt.Errorf("recheck preview viewers: %w", err)
	}
	if len(viewers) != 0 {
		return nil
	}

	if err := runBoundedMutation(
		s.SocketName,
		"resize-window", "-t", s.Name,
		"-x", strconv.Itoa(cols),
		"-y", strconv.Itoa(paneRows),
	); err != nil {
		return fmt.Errorf("resize detached preview: %w", err)
	}

	var restoreErr error
	if localPolicy == "" {
		restoreErr = runBoundedMutation(s.SocketName, "set-option", "-wu", "-t", s.Name, "window-size")
	} else {
		restoreErr = runBoundedMutation(s.SocketName, "set-option", "-w", "-t", s.Name, "window-size", localPolicy)
	}
	if restoreErr != nil {
		return fmt.Errorf("restore window-size policy: %w", restoreErr)
	}

	return nil
}

func detachedPreviewPaneRows(rows int, status string) (int, error) {
	switch status {
	case "", "off":
		return rows, nil
	case "on":
		rows--
	default:
		statusRows, err := strconv.Atoi(status)
		if err != nil {
			return 0, fmt.Errorf("unexpected tmux status value %q", status)
		}
		rows -= statusRows
	}
	if rows < 1 {
		return 0, nil
	}
	return rows, nil
}
