package main

import (
	"strings"
	"testing"
)

// The opt-in Codex submit retry (AGENTDECK_CODEX_SUBMIT_RETRY=1) must be a
// retry only when the submit was really swallowed. The first capture comes
// right after the paste and its Enter, when Codex very often still shows the
// text simply because it has not processed that Enter yet. Pressing Enter on
// that frame is a second Enter on nearly every send, and it races any prompt
// Codex opens right after the submit.

const codexRetryMessage = "Refresh the current project status and save the evidence."

// codexRetryTookMessage is the frame after Codex accepted the send: the
// message is a transcript cell and the composer is back to its placeholder.
func codexRetryTookMessage(message string) string {
	return strings.Join([]string{
		"› run the lint step",
		"",
		"• Ran make lint",
		"  └ ok",
		"",
		"› " + message,
		"",
		"\x1b[1m›\x1b[0m \x1b[2mAsk Codex to do anything\x1b[0m",
		"",
		issue2536CodexFooter,
		"",
	}, "\n")
}

func TestCodexSubmitRetry_NoEnterWhenTurnStartsAfterFirstCapture(t *testing.T) {
	t.Setenv("AGENTDECK_CODEX_SUBMIT_RETRY", "1")
	target := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{issue2536CodexDraft(codexRetryMessage), codexRetryTookMessage(codexRetryMessage)},
	}
	delivery, err := verifyContentArrival(target, codexRetryMessage, sendRetryOptions{tool: "codex", maxRetries: 3},
		sendArrivalBaseline{paneOK: true, statusOK: true}, false)
	if err != nil || delivery != deliverySubmitted {
		t.Fatalf("delivery=%s err=%v, want %s", delivery, err, deliverySubmitted)
	}
	if target.sendEnterCalls != 0 {
		t.Fatalf("a send whose turn started on the next capture got %d extra Enter(s), want 0", target.sendEnterCalls)
	}
}

func TestCodexSubmitRetry_NoEnterWhenComposerClearsOnSecondCapture(t *testing.T) {
	t.Setenv("AGENTDECK_CODEX_SUBMIT_RETRY", "1")
	target := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{issue2536CodexDraft(codexRetryMessage), issue2536CodexEmpty()},
	}
	_, _ = verifyContentArrival(target, codexRetryMessage, sendRetryOptions{tool: "codex", maxRetries: 3},
		sendArrivalBaseline{paneOK: true, statusOK: true}, false)
	if target.sendEnterCalls != 0 {
		t.Fatalf("composer cleared on the second capture but got %d Enter(s), want 0", target.sendEnterCalls)
	}
}

func TestCodexSubmitRetry_SwallowedSubmitGetsExactlyOneRetry(t *testing.T) {
	t.Setenv("AGENTDECK_CODEX_SUBMIT_RETRY", "1")
	for _, maxRetries := range []int{3, 10} {
		target := &mockSendRetryTarget{statuses: []string{"waiting"}, panes: []string{issue2536CodexDraft(codexRetryMessage)}}
		delivery, err := verifyContentArrival(target, codexRetryMessage, sendRetryOptions{tool: "codex", maxRetries: maxRetries},
			sendArrivalBaseline{paneOK: true, statusOK: true}, false)
		if err == nil || delivery != deliveryTypedNotSubmitted {
			t.Fatalf("maxRetries=%d: delivery=%s err=%v, want %s", maxRetries, delivery, err, deliveryTypedNotSubmitted)
		}
		if target.sendEnterCalls != 1 {
			t.Fatalf("maxRetries=%d: swallowed submit got %d Enter(s), want exactly 1", maxRetries, target.sendEnterCalls)
		}
		if target.sendKeysCalls != 0 || target.sendCtrlCCalls != 0 {
			t.Fatalf("maxRetries=%d: retry must not clear or retype input", maxRetries)
		}
	}
}
