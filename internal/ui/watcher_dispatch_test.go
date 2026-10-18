package ui

import (
	"errors"
	"strings"
	"testing"
)

// fakeConductorPane scripts pane captures and statuses so the verify-retry loop
// in deliverToConductorPaneTuned can be exercised without a real tmux session.
// captures[i]/statuses[i] are returned by the i-th CapturePaneFresh/GetStatus
// call; the last entry repeats once the script is exhausted (empty script ->
// "" / no error).
type fakeConductorPane struct {
	captures      []string
	statuses      []string
	captureErr    error
	statusErr     error
	sendKeysErr   error
	sendEnterErr  error
	sendKeysCalls int
	enterCalls    int
	captureCalls  int
	statusCalls   int
	ctrlCCalls    int
	chunkedCalls  int
	chunkedText   string
}

func (f *fakeConductorPane) SendCtrlC() error {
	f.ctrlCCalls++
	return nil
}

func (f *fakeConductorPane) SendKeysChunked(text string) error {
	f.chunkedCalls++
	f.chunkedText = text
	return nil
}

func (f *fakeConductorPane) SendKeysAndEnter(string) error {
	f.sendKeysCalls++
	return f.sendKeysErr
}

func (f *fakeConductorPane) SendEnter() error {
	f.enterCalls++
	return f.sendEnterErr
}

func scriptedEntry(seq []string, i int) string {
	if i >= len(seq) {
		if len(seq) == 0 {
			return ""
		}
		return seq[len(seq)-1]
	}
	return seq[i]
}

func (f *fakeConductorPane) CapturePaneFresh() (string, error) {
	i := f.captureCalls
	f.captureCalls++
	if f.captureErr != nil {
		return "", f.captureErr
	}
	return scriptedEntry(f.captures, i), nil
}

func (f *fakeConductorPane) GetStatus() (string, error) {
	i := f.statusCalls
	f.statusCalls++
	if f.statusErr != nil {
		return "", f.statusErr
	}
	return scriptedEntry(f.statuses, i), nil
}

// composerWith renders a minimal introspectable (claude-style) composer block
// holding msg as unsent input — the on-screen state observed when the trailing
// Enter was swallowed.
func composerWith(msg string) string {
	div := strings.Repeat("─", 40)
	return "some prior output\n" + div + "\n❯ " + msg + "\n" + div + "\n  auto mode on\n"
}

// emptyComposer renders an introspectable composer with no pending input (Enter
// was accepted).
func emptyComposer() string {
	div := strings.Repeat("─", 40)
	return "some prior output\n" + div + "\n❯ \n" + div + "\n  auto mode on\n"
}

// nonComposerPane renders a pane with no introspectable composer (e.g. codex /
// cursor: no prompt marker, no divider lines). Submission can only be confirmed
// via the status signal for such agents.
func nonComposerPane() string {
	return "codex>\nrunning task\nsome output line\n"
}

// TestDeliverToConductorPane_SubmitsOnFirstEnter: when the composer is clear
// right after SendKeysAndEnter, delivery succeeds without re-pressing Enter.
func TestDeliverToConductorPane_SubmitsOnFirstEnter(t *testing.T) {
	p := &fakeConductorPane{captures: []string{emptyComposer()}}
	if err := deliverToConductorPaneTuned(p, "[slack] u: hi", 40, 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.sendKeysCalls != 1 {
		t.Errorf("SendKeysAndEnter calls: want 1, got %d", p.sendKeysCalls)
	}
	if p.enterCalls != 0 {
		t.Errorf("no retry Enter expected when composer is clear, got %d", p.enterCalls)
	}
}

// TestDeliverToConductorPane_RetriesEnterUntilAccepted reproduces the
// swallowed-Enter drop: the message sits unsent in the composer, so the loop
// must re-press Enter and succeed once the composer clears.
func TestDeliverToConductorPane_RetriesEnterUntilAccepted(t *testing.T) {
	msg := "[slack] alice: please re-check delivery once more"
	p := &fakeConductorPane{captures: []string{
		composerWith(msg), // still unsent -> re-press Enter
		composerWith(msg), // still unsent -> re-press Enter
		emptyComposer(),   // accepted
	}}
	if err := deliverToConductorPaneTuned(p, msg, 40, 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.enterCalls != 2 {
		t.Errorf("retry Enter presses: want 2, got %d", p.enterCalls)
	}
}

// TestDeliverToConductorPane_ErrorsWhenNeverAccepted: if the message stays
// unsent for the whole budget, the function reports the drop (so the caller can
// log it) instead of silently succeeding.
func TestDeliverToConductorPane_ErrorsWhenNeverAccepted(t *testing.T) {
	msg := "[slack] u: stuck forever"
	p := &fakeConductorPane{captures: []string{composerWith(msg)}}
	err := deliverToConductorPaneTuned(p, msg, 5, 0)
	if err == nil {
		t.Fatal("expected error when message never leaves the composer")
	}
	if p.enterCalls != 5 {
		t.Errorf("want one retry Enter per check (5), got %d", p.enterCalls)
	}
}

// TestDeliverToConductorPane_SendKeysErrorPropagates: a hard send-keys failure
// is returned immediately without entering the verify loop.
func TestDeliverToConductorPane_SendKeysErrorPropagates(t *testing.T) {
	p := &fakeConductorPane{sendKeysErr: errors.New("tmux gone")}
	if err := deliverToConductorPaneTuned(p, "x", 40, 0); err == nil {
		t.Fatal("expected SendKeysAndEnter error to propagate")
	}
	if p.captureCalls != 0 {
		t.Errorf("verify loop must not run after send failure, got %d captures", p.captureCalls)
	}
}

// TestDeliverToConductorPane_StatusActiveIsToolAgnosticSuccess: for an agent
// without an introspectable composer (codex/cursor), a status transition to
// "active" is the success signal — no composer capture is even required.
func TestDeliverToConductorPane_StatusActiveIsToolAgnosticSuccess(t *testing.T) {
	p := &fakeConductorPane{statuses: []string{"active"}, captures: []string{nonComposerPane()}}
	if err := deliverToConductorPaneTuned(p, "[slack] u: hi", 40, 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.enterCalls != 0 {
		t.Errorf("active status should succeed without retry Enter, got %d", p.enterCalls)
	}
	if p.captureCalls != 0 {
		t.Errorf("active status should short-circuit before capture, got %d", p.captureCalls)
	}
}

// TestDeliverToConductorPane_NonComposerSwallowRecoversViaStatus: a non-Claude
// agent whose first Enter was dropped is recovered by bounded fallback Enters,
// then confirmed once status flips to active.
func TestDeliverToConductorPane_NonComposerSwallowRecoversViaStatus(t *testing.T) {
	p := &fakeConductorPane{
		statuses: []string{"idle", "idle", "active"},
		captures: []string{nonComposerPane()},
	}
	if err := deliverToConductorPaneTuned(p, "[slack] u: hi", 40, 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.enterCalls != 2 {
		t.Errorf("want 2 fallback Enters before active, got %d", p.enterCalls)
	}
}

// TestDeliverToConductorPane_NonComposerBlindEntersAreBounded: with no composer
// introspection and no active transition, fallback Enters are capped (so a
// delivered-but-idle agent is not spammed) and the drop is reported.
func TestDeliverToConductorPane_NonComposerBlindEntersAreBounded(t *testing.T) {
	p := &fakeConductorPane{captures: []string{nonComposerPane()}} // status always ""
	err := deliverToConductorPaneTuned(p, "[slack] u: hi", 40, 0)
	if err == nil {
		t.Fatal("expected error when no submission signal is ever observed")
	}
	if p.enterCalls != blindEnterCap {
		t.Errorf("fallback Enters must be capped at %d, got %d", blindEnterCap, p.enterCalls)
	}
}

// TestDeliverToConductorPane_RetryEnterErrorPropagates: a tmux rejection on the
// retry Enter is surfaced as the real error, not a generic "not confirmed"
// timeout.
func TestDeliverToConductorPane_RetryEnterErrorPropagates(t *testing.T) {
	msg := "[slack] u: hi"
	p := &fakeConductorPane{
		captures:     []string{composerWith(msg)}, // stays unsent -> triggers retry Enter
		sendEnterErr: errors.New("tmux pane gone"),
	}
	err := deliverToConductorPaneTuned(p, msg, 40, 0)
	if err == nil || !strings.Contains(err.Error(), "tmux pane gone") {
		t.Fatalf("want wrapped SendEnter error, got %v", err)
	}
	if p.enterCalls != 1 {
		t.Errorf("should abort after the first failing Enter, got %d", p.enterCalls)
	}
}
