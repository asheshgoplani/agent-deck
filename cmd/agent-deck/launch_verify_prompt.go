package main

import (
	"fmt"
	"io"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/send"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// promptConsumeOutcome classifies what a poll of the pane found. It exists
// because "the composer looks empty" (the pre-#2079 definition of consumed)
// is necessary but not sufficient: issue #2079 is exactly a slow-mounting
// composer that swallows the leading bytes of a paste while every
// downstream signal — including a cleared composer after Enter — reads
// identically to a clean delivery. promptConsumed must therefore also
// confirm the "[Pasted text #N +M lines]" marker's declared M against the
// message's real line count before it is safe to call this a success.
type promptConsumeOutcome int

const (
	// promptNotConsumed: the composer still shows unsent/foreign content, or
	// never rendered at all. Caller may retry.
	promptNotConsumed promptConsumeOutcome = iota
	// promptConsumed: composer is clear and, for a multi-line message, its
	// paste marker declares at least as many lines as the message has.
	promptConsumed
	// promptTruncated: composer is clear, but the paste marker declares
	// FEWER lines than the message has — the framed paste landed short and
	// only the tail (or a truncated fragment) was submitted.
	promptTruncated
	// promptUnknown: composer is clear but no paste marker was ever
	// observed for a message that expects one. Delivery integrity cannot be
	// confirmed either way, so this must never be reported as success.
	promptUnknown
)

// verifyPromptConsumedAfterLaunch observes whether claude actually consumed
// the -m prompt after `agent-deck launch -m "..." --no-wait`.
//
// Bug (v1.7.64): the initial tmux send-keys + Enter occasionally races
// claude's welcome-screen Enter handler, leaving the prompt typed in the
// composer but never submitted. Session then sits in "waiting" forever. The
// pre-existing launch path only granted 1.2s of verification (8×150ms),
// which is far too short to observe and recover from that race on cold
// starts with MCPs.
//
// Semantics:
//  1. Poll the pane for up to maxWait. "Consumed" = composer rendered AND
//     the message text is no longer visible in the input line.
//  2. If still unconsumed, retry send-keys exactly once.
//  3. Poll again for up to maxWait.
//  4. If still unconsumed, emit a warning to `warn` and return.
//
// Never returns an error — this is best-effort verification layered on top
// of the existing --no-wait contract.
func verifyPromptConsumedAfterLaunch(
	target sendRetryTarget,
	message string,
	maxWait, pollInterval time.Duration,
	warn io.Writer,
) {
	verifyPromptConsumedAfterLaunchAttributed(target, message, false, maxWait, pollInterval, warn)
}

// verifyPromptConsumedAfterLaunchAttributed is verifyPromptConsumedAfterLaunch
// with the #1777 provenance made explicit. ownPasteMarker carries the caller's
// pre-send observation that the composer held no "[Pasted text …]" marker
// (composerPasteFree, captured before the initial send): a marker seen now can
// then only be the collapsed rendering of our own prompt.
//
// This matters because the transport frames every multi-line body as a
// bracketed paste (issue #1855), so Claude collapsing the launch prompt behind
// a paste marker is the NORMAL outcome for a multi-line `launch -m`, not a
// rare foreign-paste event. Without the provenance, the attribution gate reads
// our own marker as a foreign draft and permanently withholds the one recovery
// retry this function exists to make — the v1.7.64 swallowed-Enter race would
// leave every multi-line prompt sitting unsubmitted.
func verifyPromptConsumedAfterLaunchAttributed(
	target sendRetryTarget,
	message string,
	ownPasteMarker bool,
	maxWait, pollInterval time.Duration,
	warn io.Writer,
) {
	switch pollPromptConsumed(target, message, maxWait, pollInterval) {
	case promptConsumed:
		return
	case promptTruncated:
		warnTruncated(warn)
		return
	case promptUnknown:
		warnUnknown(warn)
		return
	}
	// The retry types the message and presses Enter, so it submits whatever
	// the composer holds at that moment. If that is content agent-deck cannot
	// attribute — a materialized autosuggestion, an operator draft — the
	// retry would submit it with our prompt appended (#1777). Withhold it and
	// let the warning below surface the unconsumed prompt instead.
	attrib := send.EnterAttribution{Message: message, OwnPasteMarker: ownPasteMarker}
	capture := send.CaptureOutcome(target.CapturePaneFresh())
	if attrib.EnterWouldSubmitForeignDraft(capture, tmux.StripANSI) {
		if warn != nil {
			fmt.Fprintln(warn, "warning: launch prompt not consumed and the composer holds unattributable content; skipping the retry so nothing unauthored is submitted")
		}
		return
	}
	if ownPasteMarker && capture.OK && send.ComposerHoldsPasteMarker(capture.Raw, tmux.StripANSI) {
		// The composer holds our own collapsed paste marker: the body already
		// arrived and only the Enter was lost. A bare Enter is the whole
		// recovery — retyping the message would append a second copy of the
		// prompt behind the marker and submit both.
		_ = target.SendEnter()
	} else {
		_ = target.SendKeysAndEnter(message)
	}
	switch pollPromptConsumed(target, message, maxWait, pollInterval) {
	case promptConsumed:
		return
	case promptTruncated:
		warnTruncated(warn)
		return
	case promptUnknown:
		warnUnknown(warn)
		return
	}
	if warn != nil {
		fmt.Fprintln(warn, "warning: launch prompt may not have been consumed by claude after retry; session may still be on the welcome screen")
	}
}

// warnTruncated reports issue #2079's truncated-paste outcome: the composer
// cleared (Enter was accepted) but the paste marker it collapsed behind
// declares fewer line breaks than the message actually has, so a fragment —
// not the whole prompt — was submitted. Must never be reported as success.
func warnTruncated(warn io.Writer) {
	if warn != nil {
		fmt.Fprintln(warn, "warning: prompt truncated in transit: the composer's paste marker declares fewer line breaks than the launch prompt has; a fragment, not the whole prompt, was submitted")
	}
}

// warnUnknown reports that delivery integrity could not be confirmed either
// way: the composer cleared, but no paste marker was ever observed for a
// message that expects one. Must never be reported as success.
func warnUnknown(warn io.Writer) {
	if warn != nil {
		fmt.Fprintln(warn, "warning: launch prompt delivery unknown: composer appears clear but no paste marker was visible to confirm the message arrived intact")
	}
}

// pollPromptConsumed polls the pane for up to maxWait and classifies what it
// finds as a promptConsumeOutcome. "Consumed" starts from the same base
// signal as before #2079 — a rendered composer that is genuinely empty, i.e.
// Enter was accepted and nothing (ours or foreign) is sitting in the input
// line — but that alone is not sufficient to call it a success (issue
// #2079): the launch --no-wait path's initial send-keys + Enter can race a
// slow-mounting composer that swallows the leading bytes of a paste, and a
// truncated fragment being submitted reads identically to a clean delivery
// on every signal this function used to check. So once the base "consumed"
// shape is observed for a multi-line message, the count declared on Claude's
// "[Pasted text #N +M lines]" collapse marker (present anywhere in the pane —
// the transcript still shows it after submission) is checked against the
// message's hard line-break count (send.CheckPasteMarker: M counts line
// breaks, not lines, and never display rows) before promptConsumed is
// returned.
//
// Consumed must NOT be inferred merely from "our message isn't in the
// composer" (#1777): a materialized autosuggestion or an unrelated draft
// parked in the composer instead of our message would satisfy that weaker
// check and short-circuit this poll as a false success, skipping the
// attribution gate below entirely — the retry-withhold-and-warn path could
// then never fire because the caller believes delivery already succeeded.
// ComposerHasDraft reports true for ANY visible draft, ours or foreign, so
// only a truly empty (or suggestion/placeholder) composer counts as consumed.
//
// Returns promptTruncated the moment a marker declares fewer line breaks than
// expected — that signal is final, not a render-lag artifact, so there is no
// reason to keep polling. A consumed-looking pane with no marker at all is
// held as a candidate (render lag: the marker may not have painted yet) and
// only classified promptUnknown if the budget expires without one ever
// appearing; a pane that never even reaches the consumed shape times out as
// promptNotConsumed, unchanged from before #2079.
func pollPromptConsumed(target sendRetryTarget, message string, maxWait, pollInterval time.Duration) promptConsumeOutcome {
	if pollInterval <= 0 {
		pollInterval = 100 * time.Millisecond
	}
	expectedBreaks := send.ExpectedPasteMarkerLineBreaks(message)
	deadline := time.Now().Add(maxWait)
	sawConsumedWithoutMarker := false
	for {
		if raw, err := target.CapturePaneFresh(); err == nil {
			content := tmux.StripANSI(raw)
			if send.HasCurrentComposerPrompt(content) && !send.ComposerHasDraft(raw, tmux.StripANSI) {
				if expectedBreaks == 0 {
					return promptConsumed
				}
				verdict, _ := send.CheckPasteMarker(content, expectedBreaks)
				switch verdict {
				case send.PasteMarkerAbsent:
					sawConsumedWithoutMarker = true
				case send.PasteMarkerTruncated:
					return promptTruncated
				default:
					return promptConsumed
				}
			}
		}
		if !time.Now().Before(deadline) {
			if sawConsumedWithoutMarker {
				return promptUnknown
			}
			return promptNotConsumed
		}
		remaining := time.Until(deadline)
		sleep := pollInterval
		if remaining < sleep {
			sleep = remaining
		}
		if sleep > 0 {
			time.Sleep(sleep)
		}
	}
}
