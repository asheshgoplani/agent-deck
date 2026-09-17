package send

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Issue #1793 (the false-negative half): `session send` exited 1 with "Treat
// this as NOT delivered" for messages that were delivered and executed. These
// tests replay recorded pane sequences through the classifier and pin the
// three outcomes: confirmed, delivered-but-unconfirmed (exit 0, says so), and
// failed only on positive evidence.

func replay(t *testing.T, o *Observer, frames ...PaneCapture) Verdict {
	t.Helper()
	for _, f := range frames {
		o.Observe(f)
	}
	return o.Verdict(len(frames), "6s")
}

func gone() PaneCapture {
	return CaptureOutcome("", fmt.Errorf("failed to capture pane: %w", tmux.ErrCaptureGone))
}

// --- shell -----------------------------------------------------------------

// Remote walk 2026-09-17 cases 4d/4e/5b: `echo hello-walk` to a bare bash
// session. The command ran (its output was visible), send still exited 1.
func TestClassify_ShellEcho_OutputAfterSentLineIsConfirmed(t *testing.T) {
	const msg = "echo hello-walk"
	o := NewObserver("shell", msg, Captured("bash-5.2$ \n"))
	v := replay(t, o,
		Captured("bash-5.2$ echo hello-walk\n"),
		Captured("bash-5.2$ echo hello-walk\nhello-walk\nbash-5.2$ \n"),
	)
	if v.Outcome != OutcomeConfirmed {
		t.Fatalf("outcome = %q (%s), want confirmed: the shell echoed the output and a fresh prompt", v.Outcome, v.Message)
	}
	if v.Confirmation != ConfirmationConfirmed {
		t.Fatalf("confirmation = %q, want %q", v.Confirmation, ConfirmationConfirmed)
	}
}

// A command that produces no output yet (still running) is delivered but
// unknown: the line left the prompt as far as anyone can tell, and nothing
// says Enter was swallowed. Exit 0, never "NOT delivered".
func TestClassify_ShellLineStillAtPrompt_IsDeliveredUnconfirmed(t *testing.T) {
	const msg = "sleep 30 && echo finished-walk"
	o := NewObserver("shell", msg, Captured("$ \n"))
	v := replay(t, o, Captured("$ sleep 30 && echo finished-walk\n"))
	if v.Outcome != OutcomeDeliveredUnconfirmed {
		t.Fatalf("outcome = %q (%s), want delivered-unconfirmed", v.Outcome, v.Message)
	}
	if v.Confirmation != ConfirmationUnknown {
		t.Fatalf("confirmation = %q, want %q", v.Confirmation, ConfirmationUnknown)
	}
	if want := "delivered; submission not confirmable for tool shell"; v.Message != want {
		t.Fatalf("message = %q, want %q", v.Message, want)
	}
	if strings.Contains(v.Message, "NOT delivered") {
		t.Fatal("an unconfirmed delivery must never be worded as NOT delivered")
	}
}

// An identical earlier command in the scrollback (its output included) must
// not certify this send: only the LAST occurrence is judged.
func TestClassify_ShellRepeatedCommand_OnlyTheNewCopyCounts(t *testing.T) {
	const msg = "echo hello-walk"
	prior := "$ echo hello-walk\nhello-walk\n$ \n"
	o := NewObserver("shell", msg, Captured(prior))
	// Frame identical to the baseline: nothing arrived.
	if v := replay(t, NewObserver("shell", msg, Captured(prior)), Captured(prior)); v.Outcome != OutcomeNoEvidence {
		t.Fatalf("unchanged pane: outcome = %q, want no_evidence", v.Outcome)
	}
	// The new copy sits at the prompt: arrived, not yet progressed.
	v := replay(t, o, Captured(prior+"$ echo hello-walk\n"))
	if v.Outcome != OutcomeDeliveredUnconfirmed {
		t.Fatalf("new copy at prompt: outcome = %q, want delivered-unconfirmed", v.Outcome)
	}
	// Output below the new copy: confirmed.
	o.Observe(Captured(prior + "$ echo hello-walk\nhello-walk\n$ \n"))
	if v := o.Verdict(3, "6s"); v.Outcome != OutcomeConfirmed {
		t.Fatalf("output after new copy: outcome = %q, want confirmed", v.Outcome)
	}
}

// A shell prompt drawn with ❯ (starship, oh-my-zsh) looks like a composer
// line to the Claude helpers. A shell must never be failed for "composer
// still holds the message": the command may simply be running.
func TestClassify_ShellWithComposerGlyph_NeverFailsOnComposerHold(t *testing.T) {
	const msg = "make check-functional"
	o := NewObserver("zsh", msg, Captured("❯ \n"))
	v := replay(t, o, Captured("❯ make check-functional\n"))
	if v.Outcome == OutcomeFailed {
		t.Fatalf("a shell line at its prompt is unknown, not failed: %s", v.Message)
	}
	if v.Outcome != OutcomeDeliveredUnconfirmed {
		t.Fatalf("outcome = %q, want delivered-unconfirmed", v.Outcome)
	}
}

func TestShellProgressed_WrappedLine(t *testing.T) {
	msg := "echo " + strings.Repeat("abcdefghij", 12) + " END-OF-LINE"
	// An 80-column pane wraps the echoed command over two lines.
	echoed := "$ " + msg
	pane := echoed[:80] + "\n" + echoed[80:] + "\n" + strings.Repeat("abcdefghij", 12) + " END-OF-LINE\n$ \n"
	if !ShellProgressed(pane, msg) {
		t.Fatal("wrapped command followed by its output must count as progressed")
	}
	stuck := echoed[:80] + "\n" + echoed[80:] + "\n"
	if ShellProgressed(stuck, msg) {
		t.Fatal("wrapped command with nothing after it must not count as progressed")
	}
}

// --- claude ----------------------------------------------------------------

func claudePane(transcript, composer string) string {
	return transcript + "\n" +
		strings.Repeat("─", 40) + "\n" +
		"❯ " + composer + "\n" +
		strings.Repeat("─", 40) + "\n"
}

// The composer was seen holding the body and then cleared on an idle target:
// the harness took it out of the composer. Confirmed.
func TestClassify_ClaudeComposerHeldThenCleared_IsConfirmed(t *testing.T) {
	const msg = "please re-run the integration suite against staging"
	o := NewObserver("claude", msg, Captured(claudePane("", "")))
	o.ClaudeLike = true
	v := replay(t, o,
		Captured(claudePane("", msg)),
		Captured(claudePane("> "+msg+"\n\n✻ Thinking… (2s · esc to interrupt)", "")),
	)
	if v.Outcome != OutcomeConfirmed {
		t.Fatalf("outcome = %q (%s), want confirmed", v.Outcome, v.Message)
	}
}

// Same frames on a target that was already mid-turn (#1978/#2273): the body
// moving out of the composer is not evidence of this message's turn. Not
// confirmed, but it is not a failure either: delivered, confirmation unknown.
func TestClassify_ClaudeBusyBeforeSend_HeldThenClearedIsDeliveredUnconfirmed(t *testing.T) {
	const msg = "please re-run the integration suite against staging"
	o := NewObserver("claude", msg, Captured(claudePane("✻ Working… (40s · esc to interrupt)", "")))
	o.ClaudeLike, o.BusyBeforeSend = true, true
	v := replay(t, o,
		Captured(claudePane("✻ Working… (40s · esc to interrupt)", msg)),
		Captured(claudePane("✻ Working… (41s · esc to interrupt)", "")),
	)
	if v.Outcome != OutcomeDeliveredUnconfirmed {
		t.Fatalf("outcome = %q (%s), want delivered-unconfirmed", v.Outcome, v.Message)
	}
	if want := "delivered; submission not confirmed within 6s"; v.Message != want {
		t.Fatalf("message = %q, want %q", v.Message, want)
	}
}

// The funccheck "Status waiting" shape: a busy synthetic Claude (hook status
// running, no queue placeholder) whose pane repaints with the body. The body
// arrived, nothing failed: exit 0.
func TestClassify_ClaudeBusyTargetBodyLandsNoQueueAffordance_IsDeliveredUnconfirmed(t *testing.T) {
	const msg = "FUNCHECK_COMPLETE"
	o := NewObserver("claude", msg, Captured("fixture received: FUNCHECK_BUSY\nesc to interrupt\n"))
	o.ClaudeLike, o.BusyBeforeSend = true, true
	v := replay(t, o, Captured("fixture received: FUNCHECK_COMPLETE\nesc to interrupt\n"))
	if v.Outcome != OutcomeDeliveredUnconfirmed {
		t.Fatalf("outcome = %q (%s), want delivered-unconfirmed", v.Outcome, v.Message)
	}
}

// The body is still sitting in the composer at the end of the budget: the
// Enter was not accepted. Positive evidence, failed.
func TestClassify_ClaudeComposerStillHoldsBody_IsFailed(t *testing.T) {
	const msg = "please re-run the integration suite against staging"
	o := NewObserver("claude", msg, Captured(claudePane("", "")))
	o.ClaudeLike = true
	v := replay(t, o, Captured(claudePane("", msg)), Captured(claudePane("", msg)))
	if v.Outcome != OutcomeFailed || v.Failure != FailureComposerHolds {
		t.Fatalf("outcome = %q/%q (%s), want failed/composer_holds_body", v.Outcome, v.Failure, v.Message)
	}
	if v.Confirmation != ConfirmationFailed {
		t.Fatalf("confirmation = %q, want failed", v.Confirmation)
	}
}

// An open AskUserQuestion / permission menu eats keystrokes as option
// selections. The body is nowhere, and the menu is the reason. Failed, and
// the message says which menu.
func TestClassify_ClaudeInteractiveMenuOpen_IsFailedWithMenuEvidence(t *testing.T) {
	const msg = "please re-run the integration suite against staging"
	menu := "Which approach should I take?\n" +
		"❯ 1. Option A\n" +
		"  2. Option B\n" +
		"\n" +
		"Enter to select · Tab/Arrow keys to navigate · Esc to cancel\n"
	o := NewObserver("claude", msg, Captured(menu))
	o.ClaudeLike = true
	v := replay(t, o, Captured(menu), Captured(menu))
	if v.Outcome != OutcomeFailed || v.Failure != FailureInteractiveMenu {
		t.Fatalf("outcome = %q/%q, want failed/interactive_menu", v.Outcome, v.Failure)
	}
	if want := "AskUserQuestion/permission menu open; message not submitted"; v.Message != want {
		t.Fatalf("message = %q, want %q", v.Message, want)
	}
	perm := "│ Do you want to proceed?\n❯ Yes\n  No, and tell Claude what to do differently\n"
	o2 := NewObserver("claude", msg, Captured(perm))
	o2.ClaudeLike = true
	if v := replay(t, o2, Captured(perm)); v.Failure != FailureInteractiveMenu {
		t.Fatalf("permission dialog: failure = %q, want interactive_menu", v.Failure)
	}
}

// A menu that opened AFTER the turn demonstrably started belongs to the turn,
// not to the delivery.
func TestClassify_TurnStartedOutranksLaterMenu(t *testing.T) {
	const msg = "please re-run the integration suite against staging"
	o := NewObserver("claude", msg, Captured(claudePane("", "")))
	o.ClaudeLike = true
	o.Observe(Captured(claudePane("> "+msg, "")))
	o.NoteTurnStarted()
	o.Observe(Captured("│ Do you want to proceed?\n❯ Yes\n  No, and tell Claude what to do differently\n"))
	if v := o.Verdict(2, "6s"); v.Outcome != OutcomeConfirmed {
		t.Fatalf("outcome = %q (%s), want confirmed", v.Outcome, v.Message)
	}
}

// --- pane gone --------------------------------------------------------------

func TestClassify_PaneGone_IsFailed(t *testing.T) {
	const msg = "please re-run the integration suite against staging"
	o := NewObserver("claude", msg, Captured(claudePane("", "")))
	o.ClaudeLike = true
	v := replay(t, o, gone(), gone())
	if v.Outcome != OutcomeFailed || v.Failure != FailurePaneGone {
		t.Fatalf("outcome = %q/%q, want failed/pane_gone", v.Outcome, v.Failure)
	}
	if want := "target pane is gone; message not submitted"; v.Message != want {
		t.Fatalf("message = %q, want %q", v.Message, want)
	}
}

// A transient capture failure is not a gone pane and proves nothing.
func TestClassify_TransientCaptureError_IsNotFailure(t *testing.T) {
	const msg = "please re-run the integration suite against staging"
	o := NewObserver("claude", msg, Captured(claudePane("", "")))
	v := replay(t, o, CaptureOutcome("", tmux.ErrCaptureTimeout), CaptureOutcome("", errors.New("boom")))
	if v.Outcome != OutcomeNoEvidence {
		t.Fatalf("outcome = %q, want no_evidence", v.Outcome)
	}
}

// A pane that answered again after a gone reading was not gone.
func TestClassify_PaneBackAfterGoneReading_IsNotFailed(t *testing.T) {
	const msg = "echo hello-walk"
	o := NewObserver("shell", msg, Captured("$ \n"))
	v := replay(t, o, gone(), Captured("$ echo hello-walk\nhello-walk\n$ \n"))
	if v.Outcome != OutcomeConfirmed {
		t.Fatalf("outcome = %q, want confirmed", v.Outcome)
	}
}

// --- #2071: multi-line reflow -------------------------------------------------

// tmux reflows a multi-paragraph body so "produce.\n\nAlso" renders as
// "produce.Also". The arrival check is whitespace-insensitive, so the body is
// seen as arrived; with the agent visibly thinking this is at worst
// delivered-unconfirmed, never "send dropped silently".
func TestClassify_MultiLineReflow_IsNotDroppedSilently(t *testing.T) {
	msg := "Please rebuild the index and report what it would produce.\n\n" +
		"Also list every nuisance you hit.\n\n" +
		"Report back with the sentinel."
	reflowed := "> Please rebuild the index and report what it would produce.Also list every nuisance you hit.Report back with the sentinel.\n" +
		"\n✻ Whirring… (57s · thinking with high effort · esc to interrupt)\n"
	o := NewObserver("claude", msg, Captured(claudePane("", "")))
	o.ClaudeLike = true
	v := replay(t, o, Captured(reflowed))
	if v.Outcome == OutcomeNoEvidence || v.Outcome == OutcomeFailed {
		t.Fatalf("outcome = %q (%s): a reflowed body is still an arrived body", v.Outcome, v.Message)
	}
	if !o.BodyArrived() {
		t.Fatal("reflowed body must count as arrived")
	}
	// With the activity transition the caller observed, it is confirmed.
	o.NoteTurnStarted()
	if v := o.Verdict(1, "6s"); v.Outcome != OutcomeConfirmed {
		t.Fatalf("outcome = %q, want confirmed once the turn started", v.Outcome)
	}
}

// --- wording / tool table ------------------------------------------------------

func TestUnconfirmedMessage_ToolWording(t *testing.T) {
	cases := []struct{ tool, window, want string }{
		{"shell", "6s", "delivered; submission not confirmable for tool shell"},
		{"bash", "6s", "delivered; submission not confirmable for tool bash"},
		{"", "6s", "delivered; submission not confirmable for tool unknown"},
		{"claude", "6s", "delivered; submission not confirmed within 6s"},
		{"codex", "2s", "delivered; submission not confirmed within 2s"},
		{"gemini", "", "delivered; submission not confirmed"},
	}
	for _, tc := range cases {
		if got := UnconfirmedMessage(tc.tool, tc.window); got != tc.want {
			t.Errorf("UnconfirmedMessage(%q,%q) = %q, want %q", tc.tool, tc.window, got, tc.want)
		}
	}
}

func TestClassify_NoEvidence_IsUnknownNotFailed(t *testing.T) {
	v := Classify(DeliveryEvidence{Tool: "claude"})
	if v.Outcome != OutcomeNoEvidence || v.Confirmation != ConfirmationUnknown {
		t.Fatalf("empty evidence: %+v", v)
	}
}

// A body taller than the window CurrentComposerPrompt scans, still sitting
// at the composer glyph after Enter, is positive evidence all the same.
func TestClassify_TallWrappedBodyStillAtComposer_IsFailed(t *testing.T) {
	msg := "ISSUE1793-DISTINCTIVE-PAYLOAD-MARKER " + strings.Repeat("x", 4000)
	var wrapped strings.Builder
	wrapped.WriteString("❯ ")
	for i := 0; i < len(msg); i += 80 {
		end := i + 80
		if end > len(msg) {
			end = len(msg)
		}
		wrapped.WriteString(msg[i:end] + "\n")
	}
	o := NewObserver("codex", msg, Captured("› \n"))
	v := replay(t, o, Captured(wrapped.String()), Captured(wrapped.String()))
	if v.Outcome != OutcomeFailed || v.Failure != FailureComposerHolds {
		t.Fatalf("outcome = %q/%q (%s), want failed/composer_holds_body", v.Outcome, v.Failure, v.Message)
	}
	// The same body in the transcript above an EMPTY composer is not held.
	o2 := NewObserver("codex", msg, Captured("› \n"))
	if v := replay(t, o2, Captured(wrapped.String()+"answer\n› \n")); v.Outcome != OutcomeDeliveredUnconfirmed {
		t.Fatalf("outcome = %q (%s), want delivered-unconfirmed", v.Outcome, v.Message)
	}
}
