package tmux

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"al.essio.dev/pkg/shellescape"
)

// issue2361StuckPaneContent is the #1892 stuck-pane content verbatim (see
// PLAN-detect-before-expire.md): a pane whose process stays alive but never
// renders a busy signal or a prompt. It is the regression guard for this
// change — the watchdog's new "detect before expire" probe must still let
// this exact content expire.
const issue2361StuckPaneContent = `  Session is starting — showing its transcript until it appears. Ctrl+Z t
^[zfadsffa  ^[^[^[^[^[^[^[^[^[[A^[[Aq^[[A^[[Aqqqqqqqq
^[[I^[[117;5u^[[117;5u^[[115;5u^[[108;5u`

// startPaneWithContent starts a real tmux pane that prints content verbatim
// (via printf, the same construction expireStartupHandover uses for its own
// hold message) and then sleeps, and blocks until the printed content has
// actually landed in the pane. tool is applied to s.Command AFTER Start so
// hasBusyIndicator/hasPromptIndicator infer the given tool rather than the
// printf/sleep command line Start() otherwise records as s.Command.
func startPaneWithContent(t *testing.T, name, content, tool, readyMarker string) *Session {
	t.Helper()
	s := NewSession(name, t.TempDir())
	cmd := fmt.Sprintf("printf %%s %s; sleep 300", shellescape.Quote(content))
	if err := s.Start(cmd); err != nil {
		t.Fatalf("start pane: %v", err)
	}
	t.Cleanup(func() { _ = s.Kill() })
	s.Command = tool

	deadline := time.Now().Add(3 * time.Second)
	for {
		pane, err := s.CapturePaneFresh()
		if err == nil && strings.Contains(StripANSI(pane), readyMarker) {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatalf("pane content never appeared (marker %q, err=%v); last capture:\n%s", readyMarker, err, pane)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestIssue2361_PromptOverdueResolvesWithoutRespawn is the "Prompt, overdue"
// case: a pane rendering a prompt hasPromptIndicator accepts for tool
// "claude" (realisticClaudeDoneContent, reused from status_fixes_test.go's
// BenchmarkHasPromptIndicator "with_prompt" case), with startupAt backdated
// past startupStateWindow. GetStatus must resolve the overdue clock via the
// new alive probe instead of expiring the pane.
func TestIssue2361_PromptOverdueResolvesWithoutRespawn(t *testing.T) {
	s := startPaneWithContent(t, "issue2361-prompt-overdue", realisticClaudeDoneContent, "claude", "Cooked for 32s")

	oldPID, _ := s.getPaneProcessTree()

	s.mu.Lock()
	s.startupAt = time.Now().Add(-startupStateWindow - time.Second)
	s.lastStableStatus = "starting"
	s.mu.Unlock()

	status, err := s.GetStatus()
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if status == "error" {
		t.Fatal("overdue prompt pane was expired into the timeout hold")
	}

	newPID, _ := s.getPaneProcessTree()
	if newPID != oldPID {
		t.Fatalf("pane was respawned though its prompt should have resolved the overdue startup: pid %d -> %d", oldPID, newPID)
	}

	s.mu.Lock()
	startupAtAfter := s.startupAt
	s.mu.Unlock()
	if !startupAtAfter.IsZero() {
		t.Fatalf("startupAt not cleared after the alive probe resolved the overdue window: %v", startupAtAfter)
	}
}

// TestIssue2361_BusyOverdueResolvesWithoutRespawn is the "Busy, overdue"
// case: a pane showing a busy indicator hasBusyIndicator accepts for tool
// "claude" (reused from TestBusyIndicatorDetection's "ctrl+c to interrupt"
// case), with startupAt backdated past startupStateWindow. Same invariant as
// the prompt case: resolve, don't expire.
func TestIssue2361_BusyOverdueResolvesWithoutRespawn(t *testing.T) {
	const busyFixture = "Working on task...\nctrl+c to interrupt\n"
	s := startPaneWithContent(t, "issue2361-busy-overdue", busyFixture, "claude", "ctrl+c to interrupt")

	oldPID, _ := s.getPaneProcessTree()

	s.mu.Lock()
	s.startupAt = time.Now().Add(-startupStateWindow - time.Second)
	s.lastStableStatus = "starting"
	s.mu.Unlock()

	status, err := s.GetStatus()
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if status == "error" {
		t.Fatal("overdue busy pane was expired into the timeout hold")
	}

	newPID, _ := s.getPaneProcessTree()
	if newPID != oldPID {
		t.Fatalf("pane was respawned though its busy signal should have resolved the overdue startup: pid %d -> %d", oldPID, newPID)
	}

	s.mu.Lock()
	startupAtAfter := s.startupAt
	s.mu.Unlock()
	if !startupAtAfter.IsZero() {
		t.Fatalf("startupAt not cleared after the alive probe resolved the overdue window: %v", startupAtAfter)
	}
}

// TestIssue2361_StuckContentStillExpires is the regression guard for #1892:
// an inert pane printing the exact stuck content from that issue (neither a
// busy signal nor a prompt) must still expire into the timeout hold once
// startupAt is backdated past the window. This is the case that proves the
// new probe cannot loosen #1892 beyond what detection already accepted.
func TestIssue2361_StuckContentStillExpires(t *testing.T) {
	s := startPaneWithContent(t, "issue2361-stuck-still-expires", issue2361StuckPaneContent, "claude", "zfadsffa")

	s.mu.Lock()
	s.startupAt = time.Now().Add(-startupStateWindow - time.Second)
	s.lastStableStatus = "starting"
	s.mu.Unlock()

	status, err := s.GetStatus()
	if err != nil {
		t.Fatalf("GetStatus after startup deadline: %v", err)
	}
	if status != "error" {
		t.Fatalf("stuck #1892 pane status = %q, want error", status)
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
}

// TestIssue2361_AliveProbeCannotCrossGenerationClaim is the generation guard:
// a probe that observed generation A's startupAt must not clear generation
// B's startupAt if a respawn lands between the probe and the guarded clear.
// Modeled on TestIssue1892_RespawnCannotCrossTimeoutGenerationClaim's seam
// style, using the afterStartupAliveProbe test hook added alongside
// startupShowsAgentAlive for exactly this race window.
func TestIssue2361_AliveProbeCannotCrossGenerationClaim(t *testing.T) {
	s := startPaneWithContent(t, "issue2361-generation-race", realisticClaudeDoneContent, "claude", "Cooked for 32s")

	s.mu.Lock()
	observedStartupAt := time.Now().Add(-startupStateWindow - time.Second)
	s.startupAt = observedStartupAt
	s.lastStableStatus = "starting"
	s.mu.Unlock()

	var respawnErr error
	s.afterStartupAliveProbe = func() {
		// Fires after startupShowsAgentAlive() has already returned true for
		// the observed generation, but before GetStatus re-locks to clear it.
		// A respawn here publishes a brand-new generation's startupAt under
		// the same mutex GetStatus is about to use for its guarded clear.
		respawnErr = s.RespawnPane("sleep 300")
	}

	status, err := s.GetStatus()
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if respawnErr != nil {
		t.Fatalf("respawn during the alive-probe window: %v", respawnErr)
	}
	if status == "error" {
		t.Fatalf("GetStatus returned error status across the generation race: %q", status)
	}

	s.mu.Lock()
	newStartupAt := s.startupAt
	s.mu.Unlock()
	if newStartupAt.IsZero() {
		t.Fatal("alive probe for the OLD generation cleared the NEW generation's startupAt")
	}
	if newStartupAt.Equal(observedStartupAt) {
		t.Fatal("RespawnPane did not publish a fresh startupAt before the probe's guarded clear ran")
	}
}

// A pane title survives respawn-pane, so a previous generation's braille
// spinner title must not vouch for a replacement that shows neither busy nor
// prompt. The overdue probe ignores the title; the stuck pane still expires.
func TestIssue2361_StaleSpinnerTitleDoesNotVouchForStuckPane(t *testing.T) {
	s := startPaneWithContent(t, "issue2361-stale-title", issue2361StuckPaneContent, "claude", "zfadsffa")
	// s.tmuxCmd is the socket-aware factory, so this targets the test's
	// isolated tmux server, never the user's.
	if out, err := s.tmuxCmd("select-pane", "-t", s.Name+":", "-T", "⠋ working").CombinedOutput(); err != nil {
		t.Fatalf("set pane title: %v: %s", err, out)
	}

	RefreshPaneInfoCache()
	info, ok := GetCachedPaneInfo(s.Name)
	if !ok || AnalyzePaneTitle(info.Title, info.CurrentCommand) != TitleStateWorking {
		t.Fatalf("precondition: pane title not seen as working (title=%q, cached=%v)", info.Title, ok)
	}

	s.mu.Lock()
	s.startupAt = time.Now().Add(-startupStateWindow - time.Second)
	s.lastStableStatus = "starting"
	s.mu.Unlock()

	status, err := s.GetStatus()
	if err != nil {
		t.Fatalf("GetStatus after startup deadline: %v", err)
	}
	if status != "error" {
		t.Fatalf("stuck pane with a spinner title: status = %q, want error", status)
	}
}
