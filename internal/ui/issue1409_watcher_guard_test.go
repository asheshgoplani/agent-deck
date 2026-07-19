package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/send"
)

// guardedFakePane extends fakeConductorPane semantics with composer-clear
// behavior for the #1409 guard: the pane shows draftPane until SendCtrlC is
// called, then an empty composer until SendKeysAndEnter runs, then the
// scripted postSendCaptures (last entry repeats).
type guardedFakePane struct {
	draftPane        string
	postSendCaptures []string

	ctrlCCalls    int
	sendKeysCalls int
	enterCalls    int
	chunkedCalls  int
	chunkedText   string
	postSendIdx   int

	restoredBeforeSend bool
}

func (f *guardedFakePane) SendKeysAndEnter(string) error {
	f.sendKeysCalls++
	return nil
}

func (f *guardedFakePane) SendEnter() error {
	f.enterCalls++
	return nil
}

func (f *guardedFakePane) SendCtrlC() error {
	f.ctrlCCalls++
	return nil
}

func (f *guardedFakePane) SendKeysChunked(text string) error {
	f.chunkedCalls++
	f.chunkedText = text
	if f.sendKeysCalls == 0 {
		f.restoredBeforeSend = true
	}
	return nil
}

func (f *guardedFakePane) GetStatus() (string, error) { return "waiting", nil }

func (f *guardedFakePane) CapturePaneFresh() (string, error) {
	if f.sendKeysCalls > 0 {
		i := f.postSendIdx
		f.postSendIdx++
		if len(f.postSendCaptures) == 0 {
			return emptyComposer(), nil
		}
		if i >= len(f.postSendCaptures) {
			i = len(f.postSendCaptures) - 1
		}
		return f.postSendCaptures[i], nil
	}
	if f.ctrlCCalls > 0 {
		return emptyComposer(), nil
	}
	return f.draftPane, nil
}

func testConductorGuardOpts() send.ComposerGuardOptions {
	return send.ComposerGuardOptions{
		HoldWait:     0,
		PollInterval: time.Millisecond,
		ClearWait:    50 * time.Millisecond,
	}
}

// TestDeliverToConductorPaneGuarded_SaveClearRestore: a watcher dispatch
// against a composer holding a half-typed operator draft must not merge with
// it (#1409): the draft is cleared before the send and restored (without
// Enter) after the delivery is confirmed.
func TestDeliverToConductorPaneGuarded_SaveClearRestore(t *testing.T) {
	p := &guardedFakePane{
		draftPane:        composerWith("instruct deploy ag"),
		postSendCaptures: []string{emptyComposer()},
	}
	err := deliverToConductorPaneGuarded(p, "[slack] u: hi", testConductorGuardOpts(), 40, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.ctrlCCalls != 1 {
		t.Fatalf("expected 1 Ctrl+C to clear the operator draft, got %d", p.ctrlCCalls)
	}
	if p.chunkedText != "instruct deploy ag" {
		t.Fatalf("restored draft: want %q, got %q", "instruct deploy ag", p.chunkedText)
	}
	if p.restoredBeforeSend {
		t.Fatal("draft restore must happen after the automated delivery")
	}
}

// TestDeliverToConductorPaneGuarded_HoldsWhileDraftClearsItself: when the
// operator submits/erases their draft within the hold window, no Ctrl+C and
// no restore happen.
func TestDeliverToConductorPaneGuarded_HoldsWhileDraftClearsItself(t *testing.T) {
	p := &fakeConductorPane{captures: []string{
		composerWith("operator wip"), // guard: busy
		emptyComposer(),              // guard: cleared on its own
		emptyComposer(),              // verify loop: submitted
	}}
	opts := testConductorGuardOpts()
	opts.HoldWait = 500 * time.Millisecond
	err := deliverToConductorPaneGuarded(p, "[slack] u: hi", opts, 40, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.ctrlCCalls != 0 {
		t.Fatalf("hold succeeded; must not Ctrl+C, got %d", p.ctrlCCalls)
	}
	if p.chunkedCalls != 0 {
		t.Fatalf("nothing saved; must not restore, got %d chunked calls", p.chunkedCalls)
	}
}

// TestDeliverToConductorPaneGuarded_NoRestoreOnUnconfirmedDelivery: when the
// dispatch itself is never confirmed submitted, the saved draft must NOT be
// typed back into a composer that may still hold the automated message.
func TestDeliverToConductorPaneGuarded_NoRestoreOnUnconfirmedDelivery(t *testing.T) {
	msg := "[slack] u: stuck forever"
	p := &guardedFakePane{
		draftPane:        composerWith("operator wip"),
		postSendCaptures: []string{composerWith(msg)},
	}
	err := deliverToConductorPaneGuarded(p, msg, testConductorGuardOpts(), 5, 0)
	if err == nil {
		t.Fatal("expected delivery-not-confirmed error")
	}
	if p.chunkedCalls != 0 {
		t.Fatal("draft must not be restored when delivery is unconfirmed")
	}
}

// TestDeliverToConductorPane_PassesThroughEmptyComposer keeps the production
// wrapper behavior intact for the common case: empty composer, immediate
// delivery, no guard side effects.
func TestDeliverToConductorPane_PassesThroughEmptyComposer(t *testing.T) {
	p := &guardedFakePane{
		draftPane:        emptyComposer(),
		postSendCaptures: []string{emptyComposer()},
	}
	err := deliverToConductorPaneGuarded(p, "[slack] u: hi", testConductorGuardOpts(), 40, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.ctrlCCalls != 0 || p.chunkedCalls != 0 {
		t.Fatalf("empty composer must be side-effect free, got ctrlC=%d chunked=%d",
			p.ctrlCCalls, p.chunkedCalls)
	}
	if p.sendKeysCalls != 1 {
		t.Fatalf("expected 1 SendKeysAndEnter, got %d", p.sendKeysCalls)
	}
}

// Guard suggestion-placeholder handling at the dispatch layer too: Claude's
// idle "Try ..." hint must not be mistaken for an operator draft.
func TestDeliverToConductorPaneGuarded_IgnoresSuggestionPlaceholder(t *testing.T) {
	p := &guardedFakePane{
		draftPane:        composerWith(`Try "write a test for <filepath>"`),
		postSendCaptures: []string{emptyComposer()},
	}
	err := deliverToConductorPaneGuarded(p, "[slack] u: hi", testConductorGuardOpts(), 40, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.ctrlCCalls != 0 || p.chunkedCalls != 0 {
		t.Fatalf("placeholder must not trigger save-clear-restore, got ctrlC=%d chunked=%d",
			p.ctrlCCalls, p.chunkedCalls)
	}
}

// sanity: the strings used by these tests parse the way the guard expects.
func TestComposerFixturesParse(t *testing.T) {
	if !send.ComposerHasDraft(composerWith("instruct deploy ag"), nil) {
		t.Fatal("fixture composerWith must register as a draft")
	}
	if send.ComposerHasDraft(emptyComposer(), nil) {
		t.Fatal("fixture emptyComposer must not register as a draft")
	}
	if !strings.Contains(emptyComposer(), "❯") {
		t.Fatal("fixture must render a composer marker")
	}
}
