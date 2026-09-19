package main

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/send"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Issue #1793, the false-negative half (#1978, #2071, remote walk 2026-09-17
// cases 4d/4e/5b, funccheck "Status waiting"): `session send` exited 1 with
// "Treat this as NOT delivered" for messages that were delivered and executed.
//
// Delivery has three outcomes and the exit code follows the evidence:
//   - confirmed: a positive submission signal (exit 0, submitted=true)
//   - delivered, unconfirmed: the body reached the pane, Enter was sent, no
//     signal either way (exit 0, submitted=false, confirmation=unknown)
//   - failed: positive evidence — pane gone, body still in the composer,
//     interactive menu open (exit 1 with the evidence line)

func gonePaneErr() error {
	return fmt.Errorf("failed to capture pane: %w", tmux.ErrCaptureGone)
}

// Remote walk case 4d: `echo hello-walk` to a bare bash session. The command
// ran and printed; send still exited 1. For a shell the confirmation is the
// pane itself: the sent line was consumed and output / a fresh prompt followed.
func TestIssue1793_ShellEchoWithOutput_IsSubmitted(t *testing.T) {
	const msg = "echo hello-walk"
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes: []string{
			"bash-5.2$ \n",
			"bash-5.2$ echo hello-walk\nhello-walk\nbash-5.2$ \n",
		},
	}
	res, err := executeSend(mock, "shell", msg, false, sendExecTuning{
		retry: sendRetryOptions{maxRetries: 10, checkDelay: 0, verifyDelivery: true},
	})
	if err != nil {
		t.Fatalf("a shell that echoed the output took the line: %v", err)
	}
	if res.delivery != deliverySubmitted {
		t.Fatalf("delivery = %q, want %q", res.delivery, deliverySubmitted)
	}
	if n := atomic.LoadInt32(&mock.sendKeysCalls); n != 1 {
		t.Fatalf("SendKeysAndEnter called %d times, want 1", n)
	}
}

// A shell command that is still running (nothing after the sent line) cannot
// be confirmed and cannot be failed: delivered, confirmation unknown, exit 0,
// and the note names the tool that has no submission signal.
func TestIssue1793_ShellLineStillAtPrompt_IsDeliveredUnconfirmed(t *testing.T) {
	const msg = "sleep 30 && echo finished-walk"
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{"$ \n", "$ sleep 30 && echo finished-walk\n"},
	}
	res, err := executeSend(mock, "shell", msg, false, sendExecTuning{
		retry: sendRetryOptions{maxRetries: 10, checkDelay: 0, verifyDelivery: true},
	})
	if err != nil {
		t.Fatalf("no positive failure evidence: must not error: %v", err)
	}
	if res.delivery != deliveryDelivered {
		t.Fatalf("delivery = %q, want %q", res.delivery, deliveryDelivered)
	}
	if want := "delivered; submission not confirmable for tool shell"; res.note != want {
		t.Fatalf("note = %q, want %q", res.note, want)
	}
	fields := res.jsonFields()
	if fields["delivery"] != "delivered" || fields["confirmation"] != "unknown" || fields["submitted"] != false {
		t.Fatalf("--json contract: %v", fields)
	}
}

// The funccheck "Status waiting" shape: a Claude target whose hook status was
// already running (busy before the send), the body lands, no queued-messages
// placeholder, no active edge attributable to this message. #2273 keeps this
// out of `queued` (no acknowledgement) and out of `submitted` (busy before);
// it is delivered with confirmation unknown — exit 0 — not "NOT delivered".
func TestIssue1793_ClaudeBusyBeforeSendBodyLands_IsDeliveredUnconfirmedNotFailed(t *testing.T) {
	const msg = "FUNCHECK_COMPLETE"
	mock := &mockSendRetryTarget{
		statuses: []string{"active"},
		panes: []string{
			"fixture received: FUNCHECK_BUSY\nesc to interrupt\n",
			"fixture received: FUNCHECK_COMPLETE\nesc to interrupt\n",
		},
	}
	opts := queuedOpts(hookSeq(probeBusy))
	opts.tool = "claude"
	opts.maxRetries, opts.checkDelay = 30, 0
	delivery, err := sendWithRetryTarget(mock, msg, false, opts)
	if err != nil {
		t.Fatalf("busy Claude target, body landed, nothing failed: must not error: %v", err)
	}
	if delivery == deliverySubmitted || delivery == deliveryQueued {
		t.Fatalf("delivery = %q: #2273 forbids submitted/queued on inference for a busy target", delivery)
	}
	if delivery != deliveryDelivered {
		t.Fatalf("delivery = %q, want %q", delivery, deliveryDelivered)
	}
	if n := atomic.LoadInt32(&mock.sendKeysCalls); n != 1 {
		t.Fatalf("SendKeysAndEnter called %d times, want 1", n)
	}
	if n := atomic.LoadInt32(&mock.sendCtrlCCalls); n != 0 {
		t.Fatalf("Ctrl+C sent %d times, want 0", n)
	}
}

// The note on the Claude path names the window the signal was awaited in.
func TestIssue1793_ClaudeUnconfirmedNote_NamesTheWindow(t *testing.T) {
	const msg = "please re-run the integration suite against staging"
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes: []string{
			claudeComposer(""), // guard: composer free
			"prior output\n> " + msg + "\n" + claudeComposer(""), // verification: body in transcript, composer clear
		},
	}
	retry := noWaitSendOptions()
	retry.checkDelay = 0
	res, err := executeSend(mock, "claude", msg, false, testGuardTuning(retry))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.delivery != deliveryDelivered {
		t.Fatalf("delivery = %q, want %q", res.delivery, deliveryDelivered)
	}
	// noWaitSendOptions: 30 checks; with checkDelay zeroed the window is 0s.
	if want := "delivered; submission not confirmed within 0s"; res.note != want {
		t.Fatalf("note = %q, want %q", res.note, want)
	}
	if got := verificationWindow(30, noWaitSendOptions().checkDelay); got != "6s" {
		t.Fatalf("real --no-wait window = %q, want 6s", got)
	}
}

// An open AskUserQuestion / permission menu swallows keystrokes as option
// selections: positive evidence, exit 1, and the message names the menu.
func TestIssue1793_ClaudeInteractiveMenuOpen_IsFailedWithMenuEvidence(t *testing.T) {
	const msg = "please re-run the integration suite against staging"
	menu := "Which approach should I take?\n" +
		"❯ 1. Option A\n" +
		"  2. Option B\n" +
		"\n" +
		"Enter to select · Tab/Arrow keys to navigate · Esc to cancel\n"
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{menu},
	}
	opts := queuedOpts(hookSeq(probeIdle))
	opts.tool = "claude"
	delivery, err := sendWithRetryTarget(mock, msg, false, opts)
	if err == nil {
		t.Fatal("a message typed into an open menu was not submitted: must error")
	}
	if delivery != deliveryMenuOpen {
		t.Fatalf("delivery = %q, want %q", delivery, deliveryMenuOpen)
	}
	if want := "AskUserQuestion/permission menu open; message not submitted"; err.Error() != want {
		t.Fatalf("err = %q, want %q", err.Error(), want)
	}
	if got := (sendDeliveryResult{delivery: delivery}).jsonFields()["confirmation"]; got != "failed" {
		t.Fatalf("confirmation = %v, want failed", got)
	}
}

// The pane disappeared during verification: positive evidence, exit 1.
func TestIssue1793_PaneGone_IsFailed(t *testing.T) {
	const msg = "please re-run the integration suite against staging"
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{"❯ \n", "", "", "", "", ""},
		paneErrs: []error{nil, gonePaneErr(), gonePaneErr(), gonePaneErr(), gonePaneErr(), gonePaneErr()},
	}
	opts := queuedOpts(hookSeq(probeIdle))
	opts.tool = "claude"
	opts.maxRetries = 3
	delivery, err := sendWithRetryTarget(mock, msg, false, opts)
	if err == nil || delivery != deliveryPaneGone {
		t.Fatalf("delivery=%q err=%v, want %q with an error", delivery, err, deliveryPaneGone)
	}
	if want := "target pane is gone; message not submitted"; err.Error() != want {
		t.Fatalf("err = %q, want %q", err.Error(), want)
	}
}

// The same on the non-Claude arrival path.
func TestIssue1793_PaneGoneOnArrivalPath_IsFailed(t *testing.T) {
	const msg = "echo hello-walk"
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{"$ \n", "$ echo hello-walk\n", "", "", ""},
		paneErrs: []error{nil, nil, gonePaneErr(), gonePaneErr(), gonePaneErr()},
	}
	delivery, err := sendWithRetryTarget(mock, msg, true, sendRetryOptions{maxRetries: 4, checkDelay: 0, tool: "shell"})
	if err == nil || delivery != deliveryPaneGone {
		t.Fatalf("delivery=%q err=%v, want %q with an error", delivery, err, deliveryPaneGone)
	}
}

// The wording contract: no success or unconfirmed line ever says "NOT
// delivered"; every failure line carries its evidence.
func TestIssue1793_NoVerdictSaysNotDeliveredWithoutEvidence(t *testing.T) {
	for _, tool := range []string{"shell", "bash", "claude", "codex", "gemini", "cursor", ""} {
		if strings.Contains(send.UnconfirmedMessage(tool, "6s"), "NOT delivered") {
			t.Errorf("%q: unconfirmed wording must not say NOT delivered", tool)
		}
	}
	for _, tool := range []string{"shell", "bash", "zsh", "sh", "fish", ""} {
		if send.ToolHasSubmissionSignal(tool) {
			t.Errorf("%q: a shell has no submission signal", tool)
		}
	}
	for _, tool := range []string{"claude", "codex", "gemini", "cursor", "hermes", "pi", "opencode"} {
		if !send.ToolHasSubmissionSignal(tool) {
			t.Errorf("%q: has a composer or hooks", tool)
		}
		if !session.UsesClaudeDeliveryVerify(tool) && session.IsClaudeCompatible(tool) {
			t.Errorf("%q: registry disagreement", tool)
		}
	}
}

// sendSuccessData carries the three-outcome fields through to --json.
func TestIssue1793_SuccessDataCarriesConfirmation(t *testing.T) {
	inst := &session.Instance{ID: "id-1", Title: "walk"}
	data := sendSuccessData(inst, "echo hello-walk", sendDeliveryResult{delivery: deliveryDelivered, transport: "tmux"}, false)
	if data["delivery"] != "delivered" || data["confirmation"] != "unknown" || data["submitted"] != false || data["success"] != true {
		t.Fatalf("--json for delivered: %v", data)
	}
	data = sendSuccessData(inst, "echo hello-walk", sendDeliveryResult{delivery: deliverySubmitted, transport: "tmux"}, false)
	if data["confirmation"] != "confirmed" || data["submitted"] != true {
		t.Fatalf("--json for submitted: %v", data)
	}
}
