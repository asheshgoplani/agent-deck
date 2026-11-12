package send

import (
	"errors"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Regression tests for the second adversarial review of WithdrawTyped.

// M1: a message that happens to look like Claude's idle placeholder
// (Try "..." ending in a quote) is still OUR text in the composer: it must be
// removed, never mistaken for an empty composer.
func TestWithdrawTyped_PlaceholderShapedMessageIsOurs(t *testing.T) {
	msg := `Try "make lint" then "make test" and paste the summary "ok"`
	p := &withdrawPane{text: msg}
	res := withdraw(p, msg, true)
	if res.Outcome != WithdrawRemoved || p.text != "" {
		t.Fatalf("res=%+v composer=%q, want Removed and an empty composer", res, p.text)
	}
	// Without a backspace transport it is Left (verified), not "none".
	p = &withdrawPane{text: msg}
	res = withdraw(noBackspacePane{p}, msg, true)
	if res.Outcome != WithdrawLeft || !res.Verified || res.Draft != msg {
		t.Fatalf("res=%+v, want Left verified with the draft recorded", res)
	}
	// A placeholder-shaped body that is NOT a prefix of our message is the
	// idle placeholder: the composer is empty.
	p = &withdrawPane{text: `Try "write a test for <filepath>"`}
	res = withdraw(p, msg, true)
	if res.Outcome != WithdrawNotNeeded || len(p.calls) != 0 {
		t.Fatalf("res=%+v calls=%v, want NotNeeded and no Backspace for the idle placeholder", res, p.calls)
	}
}

// M1: ObserveLeft must not report a placeholder-shaped message as an empty
// composer either.
func TestObserveLeft_PlaceholderShapedMessageIsLeft(t *testing.T) {
	msg := `Try "make lint" then "make test" and paste the summary "ok"`
	p := &withdrawPane{text: msg}
	if res := ObserveLeft(p, msg, ClaudeComposerFullBody, nil); res.Outcome != WithdrawLeft || res.Draft != msg {
		t.Fatalf("res=%+v, want Left with the draft", res)
	}
}

// hidingPane makes the composer unreadable after the first Backspace batch.
type hidingPane struct{ *withdrawPane }

func (h *hidingPane) SendBackspaces(n int) error {
	err := h.withdrawPane.SendBackspaces(n)
	h.mu.Lock()
	h.hidden = true
	h.mu.Unlock()
	return err
}

// L2: after Backspaces were sent, an unreadable frame means the last good
// draft is stale: never report it verified.
func TestWithdrawTyped_UnreadableAfterBackspaceIsUnverified(t *testing.T) {
	msg := strings.Repeat("abcdefghij", 8)
	p := &withdrawPane{text: msg}
	res := withdraw(&hidingPane{p}, msg, true)
	if res.Outcome != WithdrawLeft || res.Verified {
		t.Fatalf("res=%+v, want Left unverified (backspaces were sent, then the composer became unreadable)", res)
	}
	if res.Draft != "" && res.Draft == msg {
		t.Fatalf("Draft = the pre-backspace text %q: stale", res.Draft)
	}
}

// failSecondPane fails the second Backspace batch.
type failSecondPane struct{ *withdrawPane }

func (f *failSecondPane) SendBackspaces(n int) error {
	f.mu.Lock()
	calls := len(f.calls)
	f.mu.Unlock()
	if calls >= 1 {
		f.mu.Lock()
		f.calls = append(f.calls, n)
		f.mu.Unlock()
		return errors.New("send-keys failed")
	}
	return f.withdrawPane.SendBackspaces(n)
}

// L2: a Backspace send error after earlier batches landed leaves an unknown
// amount deleted: unverified.
func TestWithdrawTyped_SendErrorAfterBackspaceIsUnverified(t *testing.T) {
	msg := strings.Repeat("abcdefghij", 8)
	p := &withdrawPane{text: msg}
	res := withdraw(&failSecondPane{p}, msg, true)
	if res.Outcome != WithdrawLeft || res.Verified || res.Reason != WithdrawReasonBackspaceErr {
		t.Fatalf("res=%+v, want Left unverified, backspace send failed", res)
	}
}

// lateBackspacePane applies a Backspace batch only after `lag` further
// captures: slower than WithdrawTyped's change poll.
type lateBackspacePane struct {
	*withdrawPane
	lag     int
	pending int
	wait    int
}

func (l *lateBackspacePane) SendBackspaces(n int) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, n)
	l.pending, l.wait = n, l.lag
	return nil
}

func (l *lateBackspacePane) CapturePaneFresh() (string, error) {
	l.mu.Lock()
	if l.pending > 0 {
		if l.wait == 0 {
			r := []rune(l.text)
			l.text = string(r[:len(r)-min(l.pending, len(r))])
			l.pending = 0
		} else {
			l.wait--
		}
	}
	l.mu.Unlock()
	return l.withdrawPane.CapturePaneFresh()
}

// L2: NoProgress re-captures after a settle delay and records what is
// actually in the composer, not the pre-batch draft.
func TestWithdrawTyped_NoProgressRecordsSettledDraft(t *testing.T) {
	msg := "our own message, typed and stuck in the composer"
	p := &withdrawPane{text: msg}
	l := &lateBackspacePane{withdrawPane: p, lag: withdrawPolls}
	res := withdraw(l, msg, true)
	if len(p.calls) == 0 {
		t.Fatal("setup: no Backspace sent")
	}
	// The late batch lands on the very next frame: that is what the
	// composer holds once withdrawal has stopped.
	_, _ = l.CapturePaneFresh()
	if res.Outcome != WithdrawLeft || res.Reason != WithdrawReasonNoProgress {
		t.Fatalf("res=%+v, want Left, backspace made no progress", res)
	}
	if res.Draft != NormalizePromptText(p.text) {
		t.Fatalf("res=%+v, Draft %q is not what the composer holds now (%q)", res, res.Draft, p.text)
	}
}

// H1: chip ownership is the exact label this send's own paste showed before
// Enter, passed in; never inferred from the composer's current frame.
func TestWithdrawTyped_OnlyTheOwnedChipLabelIsOurs(t *testing.T) {
	msg := "one\ntwo\nthree\nfour"
	for _, tc := range []struct {
		name, owned string
	}{
		{"no chip known to be ours", ""},
		{"our chip was #1", "[Pasted text #1 +3 lines]"},
	} {
		p := &withdrawPane{text: "[Pasted text #2 +5 lines]"}
		res := WithdrawTyped(p, msg, tc.owned, ClaudeComposerFullBody, nil, true)
		if res.Outcome != WithdrawLeft || res.Verified || len(p.calls) != 0 || p.text != "[Pasted text #2 +5 lines]" {
			t.Fatalf("%s: res=%+v calls=%v composer=%q, want Left unverified and the human's chip untouched", tc.name, res, p.calls, p.text)
		}
	}
	p := &withdrawPane{text: "[Pasted text #2 +5 lines]"}
	if res := WithdrawTyped(p, msg, "[Pasted text #2 +5 lines]", ClaudeComposerFullBody, nil, true); res.Outcome != WithdrawRemoved || p.text != "" {
		t.Fatalf("owned chip: res=%+v composer=%q, want Removed", res, p.text)
	}
}

// H1: TypeAndSubmitReport records the chip its own post-paste, pre-Enter
// frame showed, exactly.
func TestTypeAndSubmitReport_RecordsOwnedChipFromPrePasteFrame(t *testing.T) {
	msg := "one\ntwo\nthree\nfour"
	pane := &chipSender{label: "[Pasted text #3 +3 lines]"}
	rep, err := TypeAndSubmitReport(pane, msg, ExpectedPasteMarkerLineBreaks(msg), false, true, ClaudeComposerFullBody)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if rep.OwnedChip != "[Pasted text #3 +3 lines]" {
		t.Fatalf("OwnedChip = %q", rep.OwnedChip)
	}
	// Without empty-before provenance no chip is ours.
	pane = &chipSender{label: "[Pasted text #3 +3 lines]"}
	if rep, _ := TypeAndSubmitReport(pane, msg, ExpectedPasteMarkerLineBreaks(msg), false, false, ClaudeComposerFullBody); rep.OwnedChip != "" {
		t.Fatalf("OwnedChip = %q without composerEmptyBefore", rep.OwnedChip)
	}
}

// chipSender shows label in the composer once typed; Enter empties it.
type chipSender struct {
	label string
	typed bool
}

func (c *chipSender) CapturePaneFresh() (string, error) {
	if c.typed {
		return renderComposer(c.label), nil
	}
	return renderComposer(""), nil
}
func (c *chipSender) SendEnter() error              { c.typed = false; return nil }
func (c *chipSender) SendKeysAndEnter(string) error { return nil }
func (c *chipSender) SendKeysAndEnterChecked(_ string, capture func() (string, error), check tmux.PostPasteCheck) error {
	c.typed = true
	pane, capErr := capture()
	if ok, err := check(pane, capErr); !ok {
		return err
	}
	return c.SendEnter()
}
