package tmux

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// issue2938Pane is the restarted pi pane from issue #2938: the previous
// generation's startup-timeout hold text survived RespawnPane (scrollback is
// preserved by default) above the new pi child idle at its composer. The issue
// quotes the banner and the pi footer shape; the frame is assembled from those
// with neutral names.
func issue2938Pane(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "pane_status", "pi-issue2938-restarted-stale-timeout-banner.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// A generation that armed a startup clock and then became interactive (the
// SessionStart hook's MarkInteractiveAt, or any settle path) must not read the
// previous generation's preserved banner as its own error.
func TestIssue2938_InteractiveGenerationIgnoresStaleTimeoutBanner(t *testing.T) {
	s := NewSession("issue2938-interactive", t.TempDir())
	s.Command = "pi"
	s.MarkInteractiveAt(time.Now().Add(time.Second))
	if !s.startupAt.IsZero() {
		t.Fatal("precondition: MarkInteractiveAt did not clear the startup clock")
	}

	content := issue2938Pane(t)
	if s.hasErrorBannerIndicator(content) {
		t.Fatal("stale startup-timeout banner from the previous generation flipped an interactive restarted session to error")
	}
	if !s.hasPromptIndicator(content) {
		t.Fatal("precondition: pi composer not recognised in the fixture")
	}
}

// The fallback the banner check exists for still holds: a reconnected session
// (no startup clock was ever armed in this process) and a generation that
// really timed out both read the hold text as the error reason.
func TestIssue2938_TimeoutBannerStillCurrentWhenOwned(t *testing.T) {
	content := issue2938Pane(t)

	reconnected := ReconnectSessionLazy("issue2938-reconnected", "my-pi", t.TempDir(), "pi", "error")
	if !reconnected.hasErrorBannerIndicator(content) {
		t.Fatal("reconnected session lost the startup-timeout reason")
	}

	timedOut := NewSession("issue2938-timed-out", t.TempDir())
	timedOut.Command = "pi"
	timedOut.startupTimedOut = true
	timedOut.startupAt = time.Time{}
	if !timedOut.hasErrorBannerIndicator(content) {
		t.Fatal("current generation's own startup timeout no longer reads as error")
	}
}

// End to end through GetStatus on a real pane: restart a pane whose new
// generation shows the old banner above an idle pi composer, mark it
// interactive the way the hook does, and the status must not be error.
func TestIssue2938_GetStatusRestartedPaneWithStaleBannerIsNotError(t *testing.T) {
	fixture := filepath.Join("testdata", "pane_status", "pi-issue2938-restarted-stale-timeout-banner.txt")
	abs, err := filepath.Abs(fixture)
	if err != nil {
		t.Fatal(err)
	}
	s := NewSession("issue2938-getstatus", t.TempDir())
	if err := s.Start("sleep 300"); err != nil {
		t.Fatalf("start pane: %v", err)
	}
	t.Cleanup(func() { _ = s.Kill() })

	if err := s.RespawnPane("cat " + abs + "; exec sleep 300"); err != nil {
		t.Fatalf("respawn pane: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.invalidateCache()
		out, _ := s.CapturePane()
		if containsFold(out, "session startup timed out") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("fixture never appeared in pane: %q", out)
		}
		time.Sleep(50 * time.Millisecond)
	}
	s.MarkInteractiveAt(time.Now().Add(time.Second))

	status, err := s.GetStatus()
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if status == "error" {
		t.Fatal("restarted interactive pane reported error from the previous generation's banner")
	}
}
