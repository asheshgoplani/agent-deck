package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/watcher"
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

// TestFormatWatcherDispatchMsg_UsesFullBody pins the second half of the
// Slack-truncation fix: the native conductor-pane delivery path must send the
// full message Body (not the first-line/200-byte Subject), fall back to Subject
// when Body is empty, and collapse newlines so tmux send-keys does not submit
// the line prematurely.
func TestFormatWatcherDispatchMsg_UsesFullBody(t *testing.T) {
	full := "first line\nsecond line that used to be dropped\nthird line"
	evt := watcher.Event{
		Source:   "slack",
		Sender:   "slack:D0B434J6BTR",
		Subject:  "first line", // first-line label the bug used to deliver
		Body:     full,
		RoutedTo: "intelas-conductor",
	}
	msg := formatWatcherDispatchMsg(evt)

	if !strings.Contains(msg, "second line that used to be dropped") || !strings.Contains(msg, "third line") {
		t.Errorf("dispatch msg dropped body lines: %q", msg)
	}
	if strings.ContainsAny(msg, "\n\r") {
		t.Errorf("dispatch msg must be single-line for tmux send-keys, got %q", msg)
	}
	if want := "[slack] slack:D0B434J6BTR: "; !strings.HasPrefix(msg, want) {
		t.Errorf("prefix: want %q, got %q", want, msg)
	}
}

// TestFormatWatcherDispatchMsg_FallsBackToSubject covers v1 / pre-fix events
// that carry no Body — delivery must still produce the Subject rather than an
// empty message.
func TestFormatWatcherDispatchMsg_FallsBackToSubject(t *testing.T) {
	evt := watcher.Event{
		Source:   "slack",
		Sender:   "slack:unknown",
		Subject:  "only a subject",
		Body:     "",
		RoutedTo: "intelas-conductor",
	}
	msg := formatWatcherDispatchMsg(evt)
	if want := "[slack] slack:unknown: only a subject"; msg != want {
		t.Errorf("fallback: want %q, got %q", want, msg)
	}
}

// TestFormatWatcherDispatchMsg_RemoteSessionNotApplicable documents, per the
// internal/ui RemoteSession guideline, why this TUI change needs no
// RemoteSession-specific coverage.
func TestFormatWatcherDispatchMsg_RemoteSessionNotApplicable(t *testing.T) {
	t.Skip("RemoteSession N/A: dispatchWatcherEvent delivers to the conductor's " +
		"tmux pane via send-keys independent of the viewer's local/remote session; " +
		"formatWatcherDispatchMsg is a pure event->string fn. No RemoteSession-specific " +
		"formatting exists to cover (mirrors dispatchHealthAlert). Covered by " +
		"TestFormatWatcherDispatchMsg_UsesFullBody / _FallsBackToSubject.")
}
