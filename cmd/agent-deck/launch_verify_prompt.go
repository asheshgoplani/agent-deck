package main

import (
	"fmt"
	"io"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/send"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
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
	if pollPromptConsumed(target, message, maxWait, pollInterval) {
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
	if pollPromptConsumed(target, message, maxWait, pollInterval) {
		return
	}
	if warn != nil {
		fmt.Fprintln(warn, "warning: launch prompt may not have been consumed by claude after retry; session may still be on the welcome screen")
	}
}

// pollPromptConsumed returns true once the pane shows a rendered composer
// that is genuinely empty — i.e., Enter was accepted and nothing (ours or
// foreign) is sitting in the input line. Returns false on timeout. Capture
// errors are treated as "not yet consumed" so we keep polling until the
// budget expires.
//
// Consumed must NOT be inferred merely from "our message isn't in the
// composer" (#1777): a materialized autosuggestion or an unrelated draft
// parked in the composer instead of our message would satisfy that weaker
// check and short-circuit this poll as a false success, skipping the
// attribution gate below entirely — the retry-withhold-and-warn path could
// then never fire because the caller believes delivery already succeeded.
// ComposerHasDraft reports true for ANY visible draft, ours or foreign, so
// only a truly empty (or suggestion/placeholder) composer counts as consumed.
func pollPromptConsumed(target sendRetryTarget, message string, maxWait, pollInterval time.Duration) bool {
	if pollInterval <= 0 {
		pollInterval = 100 * time.Millisecond
	}
	deadline := time.Now().Add(maxWait)
	for {
		if raw, err := target.CapturePaneFresh(); err == nil {
			content := tmux.StripANSI(raw)
			if send.HasCurrentComposerPrompt(content) && !send.ComposerHasDraft(raw, tmux.StripANSI) {
				return true
			}
		}
		if !time.Now().Before(deadline) {
			return false
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
