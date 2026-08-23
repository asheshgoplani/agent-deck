package tmux

import (
	"strings"
	"testing"
	"time"
)

func TestIssue1892_RestartedGenerationIgnoresPreservedTimeoutBanner(t *testing.T) {
	s := NewSession("issue1892-restarted", t.TempDir())
	s.startupTimedOut = false // RespawnPane resets this for the new generation.

	content := "Session startup timed out before the agent became interactive.\n\n❯ ready"
	if s.hasErrorBannerIndicator(content) {
		t.Fatal("timeout evidence from the previous pane generation poisoned the restarted generation")
	}
}

func TestIssue1892_RespawnCannotCrossTimeoutGenerationClaim(t *testing.T) {
	s := NewSession("issue1892-generation-race", t.TempDir())
	if err := s.Start("sleep 300"); err != nil {
		t.Fatalf("start inert pane: %v", err)
	}
	t.Cleanup(func() { _ = s.Kill() })

	oldPID, _ := s.getPaneProcessTree()
	s.mu.Lock() // models expireStartupHandover's claimed generation
	done := make(chan error, 1)
	go func() { done <- s.RespawnPane("sleep 300") }()
	time.Sleep(150 * time.Millisecond)
	newPID, _ := s.getPaneProcessTree()
	if newPID != oldPID {
		s.mu.Unlock()
		<-done
		t.Fatalf("respawn replaced pane while timeout generation claim was held: pid %d -> %d", oldPID, newPID)
	}
	s.mu.Unlock()
	if err := <-done; err != nil {
		t.Fatalf("respawn after generation claim released: %v", err)
	}
}

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
