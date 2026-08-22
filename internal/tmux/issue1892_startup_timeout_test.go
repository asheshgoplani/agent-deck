package tmux

import (
	"strings"
	"testing"
	"time"
)

// TestIssue1892_StartupWithoutAgentSignalTimesOutHonesty reproduces the stuck
// startup handover with a real tmux pane.  The pane process remains alive but
// never renders either an agent prompt or a busy signal, exactly the condition
// from #1892.  Once the bounded startup window is exhausted, that is a failed
// handover, not an ordinary waiting session.
func TestIssue1892_StartupWithoutAgentSignalTimesOutHonesty(t *testing.T) {
	s := NewSession("issue1892-timeout", t.TempDir())
	if err := s.Start("sleep 300"); err != nil {
		t.Fatalf("start inert pane: %v", err)
	}
	t.Cleanup(func() { _ = s.Kill() })

	s.mu.Lock()
	s.startupAt = time.Now().Add(-startupStateWindow - time.Second)
	s.lastStableStatus = "starting"
	s.mu.Unlock()

	status, err := s.GetStatus()
	if err != nil {
		t.Fatalf("GetStatus after startup deadline: %v", err)
	}
	if status != "error" {
		t.Fatalf("stuck startup status = %q, want error", status)
	}

	var pane string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		pane, err = s.CapturePaneFresh()
		if err == nil && strings.Contains(strings.ToLower(StripANSI(pane)), "timed out") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("capture timed-out pane: %v", err)
	}
	for _, want := range []string{"timed out", "agent-deck session restart"} {
		if !strings.Contains(strings.ToLower(StripANSI(pane)), want) {
			t.Fatalf("timed-out pane does not contain %q:\n%s", want, pane)
		}
	}

	// The hold text is the cross-process evidence. A fresh status reader has no
	// in-memory startup clock, but must still report the timeout honestly.
	reloaded := ReconnectSessionLazy(s.Name, s.DisplayName, s.WorkDir, s.Command, "error")
	if got, reloadErr := reloaded.GetStatus(); reloadErr != nil || got != "error" {
		t.Fatalf("reloaded status = %q, %v; want durable error", got, reloadErr)
	}
}
