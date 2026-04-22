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
	if pollPromptConsumed(target, message, maxWait, pollInterval) {
		return
	}
	_ = target.SendKeysAndEnter(message)
	if pollPromptConsumed(target, message, maxWait, pollInterval) {
		return
	}
	if warn != nil {
		fmt.Fprintln(warn, "warning: launch prompt may not have been consumed by claude after retry; session may still be on the welcome screen")
	}
}

// pollPromptConsumed returns true once the pane shows a rendered composer
// whose input line no longer contains the message — i.e., Enter was
// accepted. Returns false on timeout. Capture errors are treated as "not
// yet consumed" so we keep polling until the budget expires.
func pollPromptConsumed(target sendRetryTarget, message string, maxWait, pollInterval time.Duration) bool {
	if pollInterval <= 0 {
		pollInterval = 100 * time.Millisecond
	}
	deadline := time.Now().Add(maxWait)
	for {
		if raw, err := target.CapturePaneFresh(); err == nil {
			content := tmux.StripANSI(raw)
			if send.HasCurrentComposerPrompt(content) && !send.HasUnsentComposerPrompt(content, message) {
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
