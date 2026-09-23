package main

import (
	"fmt"
	"strings"
	"time"
)

// waitFor polls fn every 100ms until it returns true or the deadline passes.
// Screen assertions use this content poll. Short sleeps elsewhere settle
// navigation or record the redraw probe; they do not replace a predicate.
func (s *suite) waitFor(timeout time.Duration, fn func() (bool, error)) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		ok, err := fn()
		if err != nil {
			lastErr = err
		} else if ok {
			return nil
		}
		select {
		case <-s.ctx.Done():
			return s.ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	if lastErr != nil {
		return fmt.Errorf("timed out after %s: %w", timeout, lastErr)
	}
	return fmt.Errorf("timed out after %s waiting for condition", timeout)
}

// capturePane returns the current rendered content of a tmux pane, by
// tmux target (session name, or session:window.pane).
func (s *suite) capturePane(target string) (string, error) {
	out, err := s.exec("tmux", "capture-pane", "-p", "-t", target)
	if err != nil {
		return "", err
	}
	return out, nil
}

// waitForPaneContains blocks until the named tmux target's pane contains
// substr, or the timeout elapses.
func (s *suite) waitForPaneContains(target, substr string, timeout time.Duration) error {
	return s.waitFor(timeout, func() (bool, error) {
		out, err := s.capturePane(target)
		if err != nil {
			return false, err
		}
		return strings.Contains(out, substr), nil
	})
}
