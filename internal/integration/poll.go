package integration

import (
	"fmt"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestingT is the subset of *testing.T used by polling helpers.
// This allows testing the timeout path without killing the real test.
type TestingT interface {
	Helper()
	Fatalf(format string, args ...any)
}

// WaitForCondition polls condition at the given interval until it returns true
// or the timeout expires. On timeout, it calls t.Fatalf with a descriptive message
// including the timeout duration and description.
func WaitForCondition(t TestingT, timeout, poll time.Duration, desc string, condition func() bool) {
	t.Helper()

	// Check immediately before first tick.
	if condition() {
		return
	}

	deadline := time.After(timeout)
	ticker := time.NewTicker(poll)
	defer ticker.Stop()

	for {
		select {
		case <-deadline:
			t.Fatalf("timed out after %v waiting for: %s", timeout, desc)
			return
		case <-ticker.C:
			if condition() {
				return
			}
		}
	}
}

// WaitForPaneContent polls the tmux pane output until it contains the expected string.
// Uses a 200ms poll interval. Handles nil tmux session gracefully (keeps polling).
func WaitForPaneContent(t TestingT, inst *session.Instance, contains string, timeout time.Duration) {
	t.Helper()

	WaitForCondition(t, timeout, 200*time.Millisecond,
		fmt.Sprintf("pane contains %q", contains),
		func() bool {
			tmuxSess := inst.GetTmuxSession()
			if tmuxSess == nil {
				return false
			}
			content, err := tmuxSess.CapturePaneFresh()
			if err != nil {
				return false
			}
			return strings.Contains(content, contains)
		},
	)
}
