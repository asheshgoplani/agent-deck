package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// ---------------------------------------------------------------------------
// Issue #1793: `session send` returned {"success":true,"delivery":"unverified"}
// for a 4095-byte payload that never reached the agent.
//
// The failure direction is the point of these tests: when delivery cannot be
// confirmed, the command must NOT report success. A test suite that only
// checks the happy path is how a fix that delivers nothing gets merged.
// ---------------------------------------------------------------------------

// bigMessage returns a payload at or above the size where an unconfirmed send
// is treated as a failure rather than as "we could not tell".
func bigMessage(n int) string {
	const marker = "ISSUE1793-DISTINCTIVE-PAYLOAD-MARKER "
	return marker + strings.Repeat("x", n-len(marker))
}

// TestIssue1793_LargePayloadNeverSeenInPane_IsAFailureNotAnUnverifiedSuccess
// is the exact reported scenario: a boundary-sized prompt to a non-Claude tool
// (Codex), the pane never shows it, the agent never goes active. The old code
// returned deliveryUnverified with a nil error and the CLI exited 0.
func TestIssue1793_LargePayloadNeverSeenInPane_IsAFailureNotAnUnverifiedSuccess(t *testing.T) {
	msg := bigMessage(4095)
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{"codex composer, empty, nothing of ours in it\n"},
	}

	delivery, err := sendWithRetryTarget(mock, msg, true, sendRetryOptions{
		maxRetries: 4, checkDelay: 0,
	})

	if err == nil {
		t.Fatal("issue #1793: an unconfirmed large send must not report success")
	}
	if delivery != deliveryNoEvidence {
		t.Fatalf("delivery: want %q, got %q", deliveryNoEvidence, delivery)
	}
	if delivery == deliveryUnverified {
		t.Fatal("issue #1793: transport-only success is exactly the phantom this fixes")
	}
}

// TestIssue1793_LargePayloadVisibleInPane_StillInComposerIsAFailure pins the
// positive direction, and pins it through terminal line wrapping: a pane wraps
// long content at its width and capture-pane returns those wraps as newlines,
// so a byte-exact search for the body fails on any real wide message.
// Verification that cannot see a delivered message is worse than none — it
// turns working sends into failures.
//
// The body is sitting at the composer glyph after Enter with nothing taking
// it: that is positive evidence the Enter was not accepted (the reported
// bug), so this is typed_not_submitted, exit 1 — never exit 0.
func TestIssue1793_LargePayloadVisibleInPane_StillInComposerIsAFailure(t *testing.T) {
	msg := bigMessage(4095)

	// Render the message the way a 80-column pane would: hard-wrapped.
	var wrapped strings.Builder
	for i := 0; i < len(msg); i += 80 {
		end := i + 80
		if end > len(msg) {
			end = len(msg)
		}
		wrapped.WriteString(msg[i:end])
		wrapped.WriteString("\n")
	}

	// Index 0 is the pre-send baseline capture, the rest are post-send.
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{"❯ \n", "❯ " + wrapped.String()},
	}

	delivery, err := sendWithRetryTarget(mock, msg, true, sendRetryOptions{
		maxRetries: 4, checkDelay: 0, tool: "codex",
	})
	if err == nil {
		t.Fatal("issue #1793: a message still sitting in the composer after Enter must not report success")
	}
	if delivery != deliveryTypedNotSubmitted {
		t.Fatalf("delivery: want %q, got %q", deliveryTypedNotSubmitted, delivery)
	}
	if fields := (sendDeliveryResult{delivery: delivery}).jsonFields(); fields["submitted"] != false {
		t.Fatalf("typed_not_submitted must report submitted=false in --json, got %v", fields["submitted"])
	}
}

// TestIssue1793_LargePayloadInTranscriptNotComposer_IsDeliveredUnconfirmed is
// the other half of the same frame: the body landed in the pane and the
// composer is empty again, but no submission signal arrived in the window.
// Nothing showed it failing, so this is delivered with confirmation unknown
// (exit 0), never "NOT delivered".
func TestIssue1793_LargePayloadInTranscriptNotComposer_IsDeliveredUnconfirmed(t *testing.T) {
	msg := bigMessage(4095)
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{"› \n", "› " + msg + "\n› \n"},
	}
	delivery, err := sendWithRetryTarget(mock, msg, true, sendRetryOptions{
		maxRetries: 4, checkDelay: 0, tool: "codex",
	})
	if err != nil {
		t.Fatalf("a delivered body with the composer clear is not a failure: %v", err)
	}
	if delivery != deliveryDelivered {
		t.Fatalf("delivery: want %q, got %q", deliveryDelivered, delivery)
	}
	fields := (sendDeliveryResult{delivery: delivery}).jsonFields()
	if fields["submitted"] != false || fields["confirmation"] != "unknown" {
		t.Fatalf("delivered must carry submitted=false, confirmation=unknown; got %v", fields)
	}
}

// TestIssue1793_ClaudePath_BodyVisibleNeverSubmitted_IsDeliveredUnconfirmed is
// the Claude half. The Claude verification loop used to treat "the body is
// visible in the pane" as delivery evidence and, at the end of its budget,
// return deliverySubmitted on the strength of it; the fix for that then went
// too far the other way and reported the same frame as "NOT delivered" (the
// false negative of #1978/#2071 and the remote-walk `echo` case).
//
// Body visible, composer not holding it, no menu, no pane loss, agent never
// seen going active: arrival without a signal either way. That is delivered
// with confirmation unknown — exit 0, submitted=false — not submitted and not
// a failure.
func TestIssue1793_ClaudePath_BodyVisibleNeverSubmitted_IsDeliveredUnconfirmed(t *testing.T) {
	const msg = "please re-run the integration suite against staging"
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{"some prior output\n" + msg + "\n"},
	}

	delivery, err := sendWithRetryTarget(mock, msg, false, sendRetryOptions{
		maxRetries: 5, checkDelay: 0, verifyDelivery: true, tool: "claude",
	})
	if err != nil {
		t.Fatalf("issue #1793: arrival without a failure signal must not be an error: %v", err)
	}
	if delivery == deliverySubmitted {
		t.Fatal("issue #1793: the Claude path must not promote visible body text to submitted")
	}
	if delivery != deliveryDelivered {
		t.Fatalf("delivery: want %q, got %q", deliveryDelivered, delivery)
	}
}

// TestIssue1793_ClaudePath_BodyStillAtComposer_IsAFailure pins the failure
// direction on the same path: the body sitting at the composer glyph after
// every Enter retry is positive evidence and exits 1.
func TestIssue1793_ClaudePath_BodyStillAtComposer_IsAFailure(t *testing.T) {
	const msg = "please re-run the integration suite against staging"
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{"some prior output\n❯ " + msg + "\n"},
	}
	delivery, err := sendWithRetryTarget(mock, msg, false, sendRetryOptions{
		maxRetries: 5, checkDelay: 0, verifyDelivery: true, tool: "claude",
	})
	if err == nil {
		t.Fatal("issue #1793: a body still in the composer must not report success")
	}
	if delivery != deliveryTypedNotSubmitted {
		t.Fatalf("delivery: want %q, got %q", deliveryTypedNotSubmitted, delivery)
	}
}

// TestIssue1793_ClaudePath_ActiveTransitionStillReportsSubmitted guards the
// other direction: the fix above must not stop the Claude path recognising a
// genuinely accepted turn.
func TestIssue1793_ClaudePath_ActiveTransitionStillReportsSubmitted(t *testing.T) {
	const msg = "please re-run the integration suite against staging"
	mock := &mockSendRetryTarget{
		statuses: []string{"active", "active"},
		panes:    []string{""},
	}

	delivery, err := sendWithRetryTarget(mock, msg, false, sendRetryOptions{
		maxRetries: 5, checkDelay: 0, verifyDelivery: true,
	})
	if err != nil {
		t.Fatalf("an agent that went active accepted the turn: %v", err)
	}
	if delivery != deliverySubmitted {
		t.Fatalf("delivery: want %q, got %q", deliverySubmitted, delivery)
	}
}

// TestIssue1793_ConfirmationFollowsTheEvidence pins the contract the CLI
// exposes: JSON, exit code and human text must agree. Failing statuses carry
// submitted=false and confirmation=failed; delivered carries submitted=false
// and confirmation=unknown; only submitted and Claude's own queued
// acknowledgement are confirmed.
func TestIssue1793_ConfirmationFollowsTheEvidence(t *testing.T) {
	// The command maps a non-nil sendErr to ErrorWithData + os.Exit(1); the
	// statuses that must travel that path are pinned here so a future edit
	// cannot quietly route one into the success branch.
	for _, failing := range []string{deliveryNoEvidence, deliveryLineTooLong, deliveryTypedNotSubmitted, deliveryMenuOpen, deliveryPaneGone} {
		fields := (sendDeliveryResult{delivery: failing}).jsonFields()
		if fields["submitted"] != false {
			t.Errorf("%q must report submitted=false, got %v", failing, fields["submitted"])
		}
		if fields["confirmation"] != "failed" {
			t.Errorf("%q must report confirmation=failed, got %v", failing, fields["confirmation"])
		}
	}
	fields := (sendDeliveryResult{delivery: deliveryDelivered}).jsonFields()
	if fields["submitted"] != false || fields["confirmation"] != "unknown" || fields["delivery"] != "delivered" {
		t.Errorf("delivered must be submitted=false, confirmation=unknown: %v", fields)
	}
	if got := (sendDeliveryResult{delivery: deliverySubmitted}).jsonFields()["confirmation"]; got != "confirmed" {
		t.Errorf("submitted must be confirmation=confirmed, got %v", got)
	}
	if got := (sendDeliveryResult{delivery: deliveryQueued}).jsonFields()["confirmation"]; got != "confirmed" {
		t.Errorf("queued is Claude's own acknowledgement: confirmation=confirmed, got %v", got)
	}
}

// TestIssue1793_IdenticalMessageAlreadyOnScreen_IsNotEvidence guards the
// nastiest false positive available to a "is the body in the pane?" check.
// Automated senders repeat themselves — heartbeats, inbox nudges, retries of
// the same body. If the previous copy is still on screen, a naive containment
// check certifies the NEXT send even when that one vanished. Only an increase
// in occurrences counts.
func TestIssue1793_IdenticalMessageAlreadyOnScreen_IsNotEvidence(t *testing.T) {
	msg := bigMessage(4095)
	// The same body is already in the pane before the send, and the pane
	// never changes afterwards: the new send went nowhere.
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{"❯ " + msg + "\n"},
	}

	delivery, err := sendWithRetryTarget(mock, msg, true, sendRetryOptions{
		maxRetries: 4, checkDelay: 0,
	})
	if err == nil {
		t.Fatal("a leftover copy of an identical message must not certify a send that vanished")
	}
	if delivery != deliveryNoEvidence {
		t.Fatalf("delivery: want %q, got %q", deliveryNoEvidence, delivery)
	}
}

// TestIssue1793_LargePayload_IdleAgentGoingActiveIsSubmitted: an agent that
// was idle and starts working necessarily received what it is working on,
// even if its TUI never echoes the body. That is the one signal here strong
// enough to claim submission.
func TestIssue1793_LargePayload_IdleAgentGoingActiveIsSubmitted(t *testing.T) {
	msg := bigMessage(4095)
	// Index 0 is the pre-send baseline: the agent was idle, so going active
	// afterwards is a transition attributable to this send.
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting", "active"},
		panes:    []string{"❯ \n", "thinking…\n"},
	}

	delivery, err := sendWithRetryTarget(mock, msg, true, sendRetryOptions{
		maxRetries: 4, checkDelay: 0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if delivery != deliverySubmitted {
		t.Fatalf("delivery: want %q, got %q", deliverySubmitted, delivery)
	}
}

// TestIssue1793_AlreadyActiveAgent_IsNotEvidenceOfArrival is the second half
// of the same trap as the leftover-copy test, and the more dangerous one: a
// pane that was ALREADY working is still working a moment later whether or not
// it received anything. Accepting "it is active now" as proof would hand back
// success for a message that vanished into a busy agent — the #1793 phantom,
// reintroduced through the status signal instead of the pane signal. Only a
// transition from not-active to active counts.
func TestIssue1793_AlreadyActiveAgent_IsNotEvidenceOfArrival(t *testing.T) {
	msg := bigMessage(4095)
	// Busy before the send, busy throughout, and the body never appears.
	mock := &mockSendRetryTarget{
		statuses: []string{"active"},
		panes:    []string{"thinking…\n"},
	}

	delivery, err := sendWithRetryTarget(mock, msg, true, sendRetryOptions{
		maxRetries: 4, checkDelay: 0,
	})
	if err == nil {
		t.Fatal("an agent that was already busy before the send does not prove the send arrived")
	}
	if delivery != deliveryNoEvidence {
		t.Fatalf("delivery: want %q, got %q", deliveryNoEvidence, delivery)
	}
}

// TestIssue1793_SmallPayloadKeepsTheBestEffortContract: below the size where
// canonical-buffer loss can happen, an unmatched pane is far more likely to be
// a rendering quirk than a lost message. Those keep reporting `unverified`
// rather than becoming a wall of new failures — the honesty fix must not turn
// into a false-alarm generator.
func TestIssue1793_SmallPayloadKeepsTheBestEffortContract(t *testing.T) {
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{"nothing of ours here\n"},
	}
	delivery, err := sendWithRetryTarget(mock, "please re-run the integration suite", true, sendRetryOptions{
		maxRetries: 4, checkDelay: 0,
	})
	if err != nil {
		t.Fatalf("small unmatched sends must stay best-effort, got error: %v", err)
	}
	if delivery != deliveryUnverified {
		t.Fatalf("delivery: want %q, got %q", deliveryUnverified, delivery)
	}
}

// TestIssue1793_UnverifiableMessageSaysSoInsteadOfGuessing: a message with no
// distinctive token cannot be looked for at all. That is "verification
// impossible", which must be reported as unverified — not silently upgraded
// to success-with-evidence and not downgraded to a fabricated failure.
func TestIssue1793_UnverifiableMessageSaysSoInsteadOfGuessing(t *testing.T) {
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{"y\n"},
	}
	delivery, err := sendWithRetryTarget(mock, "y", true, sendRetryOptions{
		maxRetries: 4, checkDelay: 0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if delivery != deliveryUnverified {
		t.Fatalf("delivery: want %q, got %q", deliveryUnverified, delivery)
	}
}

// TestIssue1793_LargeMultilinePayloadIsNotFailedForItsTotalSize is the
// regression for a contradiction in an earlier cut of this fix: the transport
// refuses on the longest LINE (canonical buffering is per line), but the CLI
// gated its hard failure on the TOTAL payload size. A 20 KB body of 80-byte
// lines transports fine — the tmux suite proves it against a real pane — yet
// would have been reported as lost whenever the agent's TUI does not echo it.
func TestIssue1793_LargeMultilinePayloadIsNotFailedForItsTotalSize(t *testing.T) {
	// 20 KB total, longest line 80 bytes: far above any total-size threshold,
	// far below every canonical line buffer.
	body := strings.Repeat(strings.Repeat("m", 79)+"\n", 250)
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{"a pane that never echoes anything\n"},
	}

	delivery, err := sendWithRetryTarget(mock, body, true, sendRetryOptions{
		maxRetries: 4, checkDelay: 0,
	})
	if err != nil {
		t.Fatalf("a %d-byte payload whose longest line is 79 bytes is deliverable and must not be "+
			"reported as lost: %v", len(body), err)
	}
	if delivery != deliveryUnverified {
		t.Fatalf("delivery: want %q, got %q", deliveryUnverified, delivery)
	}
}

// TestIssue1793_TokenlessOverLongLineIsNotAFreePass closes the door a
// verification check leaves open by construction: a payload with no content
// distinctive enough to search for. If such a message also carries a line long
// enough to be eaten whole, "we could not look" must not come back as exit 0 —
// that is the reported bug reached through the token-less path.
func TestIssue1793_TokenlessOverLongLineIsNotAFreePass(t *testing.T) {
	// Whitespace only: messageDeliveryToken yields nothing to search for.
	body := strings.Repeat(" ", 4095)
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{"\n"},
	}

	delivery, err := sendWithRetryTarget(mock, body, true, sendRetryOptions{
		maxRetries: 4, checkDelay: 0,
	})
	if err == nil {
		t.Fatal("an unverifiable message with an over-long line must not report success")
	}
	if delivery != deliveryNoEvidence {
		t.Fatalf("delivery: want %q, got %q", deliveryNoEvidence, delivery)
	}
}

// TestIssue1793_FailedBaselineDisablesTheSignalItBelongsTo: a baseline that
// could not be read is not a baseline of zero. If the pre-send capture fails
// and the pane already holds a copy of a repeated message, treating the
// missing baseline as 0 would read that stale copy as a fresh arrival.
func TestIssue1793_FailedBaselineDisablesTheSignalItBelongsTo(t *testing.T) {
	msg := bigMessage(4095)
	mock := &mockSendRetryTarget{
		statuses: []string{"active"}, // busy before and after: no transition
		panes:    []string{"❯ " + msg + "\n"},
		paneErrs: []error{errors.New("capture failed")}, // baseline unreadable
	}

	delivery, err := sendWithRetryTarget(mock, msg, true, sendRetryOptions{
		maxRetries: 4, checkDelay: 0,
	})
	if err == nil {
		t.Fatal("with no readable baseline, a pre-existing copy must not certify the send")
	}
	if delivery != deliveryNoEvidence {
		t.Fatalf("delivery: want %q, got %q", deliveryNoEvidence, delivery)
	}
}

// TestIssue1793_CanonicalOverflowIsItsOwnDeliveryStatus: when the transport
// refuses because the pane's canonical buffer cannot hold the line, nothing
// was typed. That is a distinct, non-retryable outcome and must not be
// flattened into the generic send_failed bucket, so callers can tell "the
// composer is untouched, change the message" from "the pipe broke, try again".
func TestIssue1793_CanonicalOverflowIsItsOwnDeliveryStatus(t *testing.T) {
	mock := &mockSendRetryTarget{
		sendKeysErr: fmt.Errorf("send to pane: %w", &tmux.CanonicalOverflowError{
			LineBytes:  4095,
			LimitBytes: 4095,
			TTY:        "/dev/pts/7",
		}),
	}

	delivery, err := sendWithRetryTarget(mock, bigMessage(4095), true, sendRetryOptions{
		maxRetries: 4, checkDelay: 0,
	})
	if err == nil {
		t.Fatal("a refused over-long line must be an error")
	}
	if delivery != deliveryLineTooLong {
		t.Fatalf("delivery: want %q, got %q", deliveryLineTooLong, delivery)
	}
	if !strings.Contains(err.Error(), "canonical") {
		t.Errorf("error should explain the canonical-buffer cause, got: %v", err)
	}
}

// TestIssue1793_CanonicalOverflowSurvivesTheClaudeVerifiedPath: the same
// refusal must classify identically when the Claude verification loop is in
// play, not just on the skip-verify path.
func TestIssue1793_CanonicalOverflowSurvivesTheClaudeVerifiedPath(t *testing.T) {
	mock := &mockSendRetryTarget{
		sendKeysErr: &tmux.CanonicalOverflowError{LineBytes: 2000, LimitBytes: 1024, TTY: "/dev/ttys001"},
	}
	delivery, err := sendWithRetryTarget(mock, bigMessage(2000), false, sendRetryOptions{
		maxRetries: 4, checkDelay: 0, verifyDelivery: true,
	})
	if err == nil {
		t.Fatal("a refused over-long line must be an error on the verified path too")
	}
	if delivery != deliveryLineTooLong {
		t.Fatalf("delivery: want %q, got %q", deliveryLineTooLong, delivery)
	}
}

// TestIssue1793_DeliveryStatusReachesTheJSONContract pins that the new
// statuses are actually machine-readable by the callers that key off them
// (watchers, conductors, bridges), rather than only existing in Go.
func TestIssue1793_DeliveryStatusReachesTheJSONContract(t *testing.T) {
	for _, status := range []string{deliveryLineTooLong, deliveryNoEvidence, deliveryDelivered, deliveryMenuOpen, deliveryPaneGone} {
		res := sendDeliveryResult{delivery: status}
		fields := res.jsonFields()
		if fields["delivery"] != status {
			t.Errorf("delivery %q missing from --json fields: %v", status, fields)
		}
	}
}
