package main

import (
	"errors"
	"sync/atomic"
	"testing"
)

func TestCodexSubmitRetryOptIn(t *testing.T) {
	const message = "Refresh the current project status and save the evidence."
	for _, enabled := range []string{"", "1"} {
		t.Run("enabled="+enabled, func(t *testing.T) {
			t.Setenv("AGENTDECK_CODEX_SUBMIT_RETRY", enabled)
			target := &mockSendRetryTarget{statuses: []string{"waiting"}, panes: []string{issue2536CodexDraft(message)}}
			opts := sendRetryOptions{tool: "codex", maxRetries: 3, turnAdvanced: func() bool {
				return atomic.LoadInt32(&target.sendEnterCalls) > 0
			}}
			baseline := sendArrivalBaseline{paneOK: true, statusOK: true}
			delivery, err := verifyContentArrival(target, message, opts, baseline, false)
			if enabled == "1" {
				if err != nil || delivery != deliverySubmitted || target.sendEnterCalls != 1 {
					t.Fatalf("delivery=%s err=%v Enter=%d", delivery, err, target.sendEnterCalls)
				}
			} else if err == nil || delivery != deliveryTypedNotSubmitted || target.sendEnterCalls != 0 {
				t.Fatalf("default changed: delivery=%s err=%v Enter=%d", delivery, err, target.sendEnterCalls)
			}
			if target.sendKeysCalls != 0 || target.sendCtrlCCalls != 0 {
				t.Fatal("retry must not clear or retype input")
			}
		})
	}
}

func TestCodexSubmitRetryProtectsOtherInput(t *testing.T) {
	const message = "Refresh the current project status and save the evidence."
	t.Setenv("AGENTDECK_CODEX_SUBMIT_RETRY", "1")
	cases := []struct {
		name, tool, pane string
		captureError     error
	}{
		{"foreign draft", "codex", issue2536CodexDraft("My unfinished user draft"), nil},
		{"foreign continuation", "codex", issue2536CodexDraft(message + " plus my new instruction"), nil},
		{"empty composer", "codex", issue2536CodexEmpty(), nil},
		{"approval", "codex", "Would you like to run this command?\n› 1. Yes, proceed (y)\n  2. No\nPress enter to confirm or esc to cancel", nil},
		{"unreadable", "codex", "", errors.New("capture failed")},
		{"other harness", "gemini", issue2536CodexDraft(message), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := &mockSendRetryTarget{statuses: []string{"waiting"}, panes: []string{tc.pane}, paneErrs: []error{tc.captureError}}
			_, _ = verifyContentArrival(target, message, sendRetryOptions{tool: tc.tool, maxRetries: 3}, sendArrivalBaseline{paneOK: true, statusOK: true}, false)
			if target.sendEnterCalls != 0 || target.sendKeysCalls != 0 || target.sendCtrlCCalls != 0 {
				t.Fatal("protected input received a keystroke")
			}
		})
	}
}

func TestCodexSubmitRetryStaysBoundedWithoutAcceptance(t *testing.T) {
	t.Setenv("AGENTDECK_CODEX_SUBMIT_RETRY", "1")
	const message = "Refresh the current project status and save the evidence."
	target := &mockSendRetryTarget{statuses: []string{"waiting"}, panes: []string{issue2536CodexDraft(message)}}
	delivery, err := verifyContentArrival(target, message, sendRetryOptions{tool: "codex", maxRetries: 3}, sendArrivalBaseline{paneOK: true, statusOK: true}, false)
	if err == nil || delivery != deliveryTypedNotSubmitted || target.sendEnterCalls != 2 {
		t.Fatalf("delivery=%s err=%v Enter=%d", delivery, err, target.sendEnterCalls)
	}
}
