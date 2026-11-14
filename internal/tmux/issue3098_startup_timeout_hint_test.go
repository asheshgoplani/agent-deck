package tmux

import (
	"strings"
	"testing"
	"time"
)

// issue3098TimeoutHold expires a real pane's startup clock and returns the
// hold text the pane ends up showing.
func issue3098TimeoutHold(t *testing.T, name, hint string) string {
	t.Helper()
	skipIfNoTmuxBinary(t)
	s := NewSession(name, t.TempDir())
	s.DisplayName = "my pi"
	if err := s.Start("sleep 300"); err != nil {
		t.Fatalf("start pane: %v", err)
	}
	t.Cleanup(func() { _ = s.Kill() })
	if hint != "" {
		s.SetStartupTimeoutHint(hint)
	}
	s.mu.Lock()
	s.startupAt = time.Now().Add(-startupStateWindow - time.Second)
	s.mu.Unlock()
	if !s.expireStartupHandover() {
		t.Fatal("expired startup was not claimed")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.invalidateCache()
		out, _ := s.CapturePane()
		if strings.Contains(out, "Recovery: agent-deck session restart") {
			return StripANSI(out)
		}
		if time.Now().After(deadline) {
			t.Fatalf("hold text never appeared in pane: %q", out)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A session that knows why its startup could only be judged from the screen
// (the session layer sets this for pi without its agent-deck extension,
// #3098) says so in the hold, between the banner and the recovery command.
func TestIssue3098_TimeoutHoldShowsHint(t *testing.T) {
	hint := "Run 'agent-deck pi-hooks install' first."
	out := issue3098TimeoutHold(t, "issue3098-hint", hint)

	banner := strings.Index(out, "Session startup timed out before the agent became interactive.")
	at := strings.Index(out, hint)
	recovery := strings.Index(out, "Recovery: agent-deck session restart 'my pi'")
	if banner < 0 || at < 0 || recovery < 0 {
		t.Fatalf("hold is missing the banner, hint or recovery line:\n%s", out)
	}
	if !(banner < at && at < recovery) {
		t.Fatalf("hint must sit between the banner and the recovery line:\n%s", out)
	}
}

// Without a hint the hold is the banner and the recovery command, as before.
func TestIssue3098_TimeoutHoldWithoutHintIsUnchanged(t *testing.T) {
	out := issue3098TimeoutHold(t, "issue3098-no-hint", "")

	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, strings.TrimSpace(l))
		}
	}
	want := []string{
		"Session startup timed out before the agent became interactive.",
		"Recovery: agent-deck session restart 'my pi'",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("hold without a hint changed:\n got %q\nwant %q", lines, want)
	}
}

// The hint does not change how the hold reads back: the timed-out generation
// still reports its banner as the error reason.
func TestIssue3098_HintedHoldStillReadsAsTimeout(t *testing.T) {
	out := issue3098TimeoutHold(t, "issue3098-banner", "Run 'agent-deck pi-hooks install' first.")
	s := NewSession("issue3098-banner-reader", t.TempDir())
	s.startupTimedOut = true
	if !s.hasErrorBannerIndicator(out) {
		t.Fatalf("hinted hold no longer reads as a startup timeout:\n%s", out)
	}
}
