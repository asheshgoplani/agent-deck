package main

import (
	"strings"
	"sync/atomic"
	"testing"
)

// Issue #1978 (round 2 of PR #2043): a message delivered to a busy target is
// queued, not lost, and must be reported as such — but "the hook says busy" on
// its own proves nothing about THIS message. The discriminator is token
// movement: a new copy of the body (or a new composer paste marker) appearing
// in the pane relative to the pre-send baseline. Pane text as a snapshot is
// not evidence: an identical heartbeat already on screen would certify a send
// that vanished (#876), and a busy target that silently dropped the message
// would be reported queued.

// busyPaneNoBody is a target mid-turn with nothing of ours on screen.
func busyPaneNoBody() string {
	return strings.Join([]string{
		"  ⎿  streaming output line 1",
		"  ⎿  streaming output line 2",
		"────────────────────────────────────────",
		"❯ ",
		"────────────────────────────────────────",
	}, "\n")
}

// hookSeq scripts the hook probe: each call returns the next (busy, known)
// pair and the last pair repeats.
func hookSeq(pairs ...[2]bool) func() (bool, bool) {
	var idx int32
	return func() (bool, bool) {
		i := int(atomic.AddInt32(&idx, 1) - 1)
		if i >= len(pairs) {
			i = len(pairs) - 1
		}
		return pairs[i][0], pairs[i][1]
	}
}

var (
	probeBusy    = [2]bool{true, true}
	probeIdle    = [2]bool{false, true}
	probeUnknown = [2]bool{false, false}
)

func queuedOpts(probe func() (bool, bool)) sendRetryOptions {
	return sendRetryOptions{
		maxRetries:       25,
		checkDelay:       0,
		verifyDelivery:   true,
		targetBusyByHook: probe,
	}
}

// TestIssue1978_QueuedWhenHookBusyAndBodyArrives is the headline case: the
// target was mid-turn before the send, the body then appears above the
// composer as a queued message, the status heuristic never reports active.
// Verdict: queued, exactly one delivery, zero interrupt keys, no error.
func TestIssue1978_QueuedWhenHookBusyAndBodyArrives(t *testing.T) {
	const msg = "PROBE reply with only OK"
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		// First capture is the pre-send baseline: nothing of ours on screen.
		panes: []string{busyPaneNoBody(), busyPaneWithBody(msg)},
	}

	delivery, err := sendWithRetryTarget(mock, msg, false, queuedOpts(hookSeq(probeBusy)))

	if delivery != deliveryQueued {
		t.Fatalf("delivery = %q, want %q (err=%v)", delivery, deliveryQueued, err)
	}
	if err != nil {
		t.Fatalf("queued delivery returned error: %v", err)
	}
	if n := atomic.LoadInt32(&mock.sendKeysCalls); n != 1 {
		t.Errorf("SendKeysAndEnter called %d times, want exactly 1", n)
	}
	if n := atomic.LoadInt32(&mock.sendCtrlCCalls); n != 0 {
		t.Errorf("SendCtrlC called %d times against a hook-busy target, want 0 (#2033)", n)
	}
}

// TestIssue1978_HookBusyWithoutArrivalIsNotQueued: the hook says busy for the
// whole budget but the body never reaches the pane. That is a busy target
// that dropped the message, and it must keep today's failure verdict — a
// hook signal alone must never manufacture a delivery.
func TestIssue1978_HookBusyWithoutArrivalIsNotQueued(t *testing.T) {
	const msg = "PROBE reply with only OK"
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{busyPaneNoBody()},
	}

	delivery, err := sendWithRetryTarget(mock, msg, false, queuedOpts(hookSeq(probeBusy)))

	if delivery == deliveryQueued {
		t.Fatalf("delivery = queued with the body never on screen — hook-busy is not arrival evidence")
	}
	if err == nil {
		t.Fatalf("a send with no arrival evidence must fail (#876); got delivery=%q err=nil", delivery)
	}
	if n := atomic.LoadInt32(&mock.sendCtrlCCalls); n != 0 {
		t.Errorf("SendCtrlC called %d times, want 0", n)
	}
}

// TestIssue1978_StaleIdenticalBodyIsNotTokenMovement: an identical copy of
// the message (a previous heartbeat) is already on screen before the send and
// no NEW copy appears afterwards. Pane text says "body present"; the token
// count did not move. Not queued.
func TestIssue1978_StaleIdenticalBodyIsNotTokenMovement(t *testing.T) {
	const msg = "heartbeat: report status now"
	stale := busyPaneWithBody(msg)
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{stale, stale},
	}

	delivery, err := sendWithRetryTarget(mock, msg, false, queuedOpts(hookSeq(probeBusy)))

	if delivery == deliveryQueued {
		t.Fatalf("delivery = queued from a pre-existing copy of the body; want the token count to move (#876 phantom)")
	}
	if err == nil {
		t.Fatalf("expected a failure verdict without token movement, got delivery=%q", delivery)
	}
}

// queuedPaneBodyScrolledOff is a mid-turn target still holding a queued
// message (the affordance stays in the composer area) after the body itself
// has scrolled out of the visible pane.
func queuedPaneBodyScrolledOff() string {
	return strings.Join([]string{
		"  ⎿  streaming output line 7",
		"  ⎿  streaming output line 8",
		"────────────────────────────────────────",
		"❯ Press up to edit queued messages",
		"────────────────────────────────────────",
	}, "\n")
}

// TestIssue1978_BodyScrolledOffAfterArrivalIsStillQueued pins the #2033
// shape: the body arrives, then scrolls out of the visible pane while the
// turn keeps streaming and the queue affordance stays. Arrival is latched at
// the moment it was observed, so a later capture without the body does not
// un-deliver the message.
func TestIssue1978_BodyScrolledOffAfterArrivalIsStillQueued(t *testing.T) {
	const msg = "PROBE reply with only OK"
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{busyPaneNoBody(), busyPaneWithBody(msg), queuedPaneBodyScrolledOff()},
	}
	// The probe reads busy only from the third iteration on, so the loop has
	// to carry the arrival it saw on the second frame forward.
	probe := hookSeq(probeBusy, probeUnknown, probeUnknown, probeBusy)

	delivery, err := sendWithRetryTarget(mock, msg, false, queuedOpts(probe))

	if delivery != deliveryQueued || err != nil {
		t.Fatalf("delivery=%q err=%v, want queued with no error", delivery, err)
	}
	if n := atomic.LoadInt32(&mock.sendCtrlCCalls); n != 0 {
		t.Errorf("SendCtrlC called %d times, want 0", n)
	}
}

// TestIssue1978_HookIdleBeforeSendThenBusyIsSubmitted: the target was NOT
// busy before the send and the hook flips to busy once the body has landed.
// That is the target taking THIS message up — a confirmed submission, not a
// queue — and it is reported as such even when the spike-filtered status
// heuristic never manages to read active.
func TestIssue1978_HookIdleBeforeSendThenBusyIsSubmitted(t *testing.T) {
	const msg = "PROBE reply with only OK"
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{emptyComposerPane(), busyPaneWithBody(msg)},
	}
	probe := hookSeq(probeIdle, probeBusy)

	delivery, err := sendWithRetryTarget(mock, msg, false, queuedOpts(probe))

	if delivery != deliverySubmitted || err != nil {
		t.Fatalf("delivery=%q err=%v, want submitted", delivery, err)
	}
	if n := atomic.LoadInt32(&mock.sendKeysCalls); n != 1 {
		t.Errorf("SendKeysAndEnter called %d times, want 1", n)
	}
}

// TestIssue1978_UnknownHookNeverClaimsQueued: without a hook signal the
// verdict is exactly today's, even with the body newly on screen. Only the
// hook may say "busy", and only busy may say "queued".
func TestIssue1978_UnknownHookNeverClaimsQueued(t *testing.T) {
	const msg = "PROBE reply with only OK"
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{busyPaneNoBody(), busyPaneWithBody(msg)},
	}

	delivery, _ := sendWithRetryTarget(mock, msg, false, queuedOpts(hookSeq(probeUnknown)))

	if delivery == deliveryQueued {
		t.Fatalf("delivery = queued without any hook signal")
	}
	if delivery != deliveryTyped {
		t.Fatalf("delivery = %q, want the pre-#2043 verdict %q for arrival without submission", delivery, deliveryTyped)
	}
}

// TestIssue1978_NoWaitQueuedNeedsArrivalToo: the --no-wait budget must
// classify a queued message the same way — and refuse the same way when the
// body never lands.
func TestIssue1978_NoWaitQueuedNeedsArrivalToo(t *testing.T) {
	const msg = "queued no-wait message"

	landed := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{busyPaneNoBody(), busyPaneWithBody(msg)},
	}
	opts := noWaitSendOptions()
	opts.checkDelay = 0
	opts.targetBusyByHook = hookSeq(probeBusy)
	if delivery, err := sendWithRetryTarget(landed, msg, false, opts); delivery != deliveryQueued || err != nil {
		t.Fatalf("--no-wait landed: delivery=%q err=%v, want queued", delivery, err)
	}

	dropped := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{busyPaneNoBody()},
	}
	opts = noWaitSendOptions()
	opts.checkDelay = 0
	opts.targetBusyByHook = hookSeq(probeBusy)
	if delivery, err := sendWithRetryTarget(dropped, msg, false, opts); delivery == deliveryQueued || err == nil {
		t.Fatalf("--no-wait dropped: delivery=%q err=%v, want a failure verdict", delivery, err)
	}
}

// TestIssue1978_QueuedIsSuccessButNotSubmittedInJSON pins the machine-readable
// contract callers were told to use (#1413/#1793): queued carries
// `submitted: false` — exit 0 says "delivered", the boolean says "not yet an
// accepted turn".
func TestIssue1978_QueuedIsSuccessButNotSubmittedInJSON(t *testing.T) {
	fields := sendDeliveryResult{delivery: deliveryQueued}.jsonFields()
	if fields["delivery"] != deliveryQueued {
		t.Fatalf("delivery field = %v", fields["delivery"])
	}
	if submitted, _ := fields["submitted"].(bool); submitted {
		t.Fatalf("queued must report submitted=false")
	}
}

// TestIssue1978_NonClaudeArrivalPathReportsSubmittedOnHookEdge: tools that
// take the content-arrival path (codex, gemini) also have hooks. A body that
// newly appears as the hook flips from idle to busy is the harness
// acknowledging the submission, instead of the #1793 "typed" failure.
func TestIssue1978_NonClaudeArrivalPathReportsSubmittedOnHookEdge(t *testing.T) {
	const msg = "PROBE reply with only OK"
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{busyPaneNoBody(), busyPaneWithBody(msg)},
	}
	opts := queuedOpts(hookSeq(probeIdle, probeBusy))
	opts.tool = "codex"

	delivery, err := sendWithRetryTarget(mock, msg, true, opts)

	if delivery != deliverySubmitted || err != nil {
		t.Fatalf("delivery=%q err=%v, want submitted", delivery, err)
	}
}

// TestIssue1978_TurnTrackingScope pins which sends read their own turn record
// out of the transcript: Claude sends with a real prompt. Slash commands are
// recorded by Claude as command meta records, never as the typed text, so
// they keep the timestamp path instead of timing out; non-Claude tools keep
// their best-effort adapters.
func TestIssue1978_TurnTrackingScope(t *testing.T) {
	cases := []struct {
		tool, msg string
		want      bool
	}{
		{"claude", "summarize", true},
		{"claude", "/compact", false},
		{"claude", "  /clear", false},
		{"codex", "summarize", false},
	}
	for _, c := range cases {
		if got := sendTracksTurn(c.tool, c.msg); got != c.want {
			t.Errorf("sendTracksTurn(%q, %q) = %v, want %v", c.tool, c.msg, got, c.want)
		}
	}
}

// --- Codex review of PR #2273 (dc54fdef): queued only on acknowledgement ---

// busyPaneBodyNoAffordance is a mid-turn target with the body freshly on
// screen but WITHOUT Claude's queued-messages affordance: the body is there,
// nothing says the harness queued it.
func busyPaneBodyNoAffordance(msg string) string {
	return strings.Join([]string{
		"  ⎿  streaming output line 1",
		"❯ " + msg,
		"────────────────────────────────────────",
		"❯ ",
		"────────────────────────────────────────",
	}, "\n")
}

// TestIssue1978_QueuedNeedsTheQueueAffordance: hook busy before and after,
// the body newly on screen, but no "queued messages" affordance. That is an
// inference, not an acknowledgement, and must not be reported queued.
func TestIssue1978_QueuedNeedsTheQueueAffordance(t *testing.T) {
	const msg = "PROBE reply with only OK"
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{busyPaneNoBody(), busyPaneBodyNoAffordance(msg)},
	}
	delivery, err := sendWithRetryTarget(mock, msg, false, queuedOpts(hookSeq(probeBusy)))
	if delivery == deliveryQueued || delivery == deliverySubmitted || err == nil {
		t.Fatalf("delivery=%q err=%v: a busy target without the queue affordance must not be reported delivered", delivery, err)
	}
}

// TestIssue1978_ActiveHeuristicIsNotSubmissionOnABusyTarget: the target was
// mid-turn before the send; its activity belongs to that turn. Two "active"
// reads with the body on screen used to return submitted without any
// evidence about THIS message.
func TestIssue1978_ActiveHeuristicIsNotSubmissionOnABusyTarget(t *testing.T) {
	const msg = "PROBE reply with only OK"
	mock := &mockSendRetryTarget{
		statuses: []string{"active"},
		panes:    []string{busyPaneNoBody(), busyPaneBodyNoAffordance(msg)},
	}
	delivery, _ := sendWithRetryTarget(mock, msg, false, queuedOpts(hookSeq(probeBusy)))
	if delivery == deliverySubmitted {
		t.Fatalf("delivery = submitted from the in-flight turn's activity")
	}
}

// TestIssue1978_TurnAdvancementIsSubmission: the message's own user record
// in the transcript is the authoritative signal and wins even when the pane
// and status heuristics show nothing.
func TestIssue1978_TurnAdvancementIsSubmission(t *testing.T) {
	const msg = "PROBE reply with only OK"
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{busyPaneNoBody()},
	}
	opts := queuedOpts(hookSeq(probeBusy))
	var polls int32
	opts.turnAdvanced = func() bool { return atomic.AddInt32(&polls, 1) >= 3 }
	delivery, err := sendWithRetryTarget(mock, msg, false, opts)
	if delivery != deliverySubmitted || err != nil {
		t.Fatalf("delivery=%q err=%v, want submitted on turn advancement", delivery, err)
	}
	if n := atomic.LoadInt32(&mock.sendKeysCalls); n != 1 {
		t.Errorf("SendKeysAndEnter called %d times, want 1", n)
	}
}

// composerWithNewPasteMarker is a codex-style composer holding a freshly
// collapsed paste: bytes sitting unsent, the opposite of a delivery.
func composerWithNewPasteMarker() string {
	return strings.Join([]string{
		"  ⎿  streaming output",
		"────────────────────────────────────────",
		"› [Pasted text #1 +12 lines]",
		"────────────────────────────────────────",
	}, "\n")
}

// TestIssue1978_NonClaudeNewPasteMarkerIsNeverASuccess: on the content-arrival
// path a paste marker the composer newly holds, with the hook busy, used to
// become a queued success. A swallowed Enter must stay a failure.
func TestIssue1978_NonClaudeNewPasteMarkerIsNeverASuccess(t *testing.T) {
	msg := "line one\nline two\nline three"
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{busyPaneNoBody(), composerWithNewPasteMarker()},
	}
	opts := queuedOpts(hookSeq(probeBusy))
	opts.tool = "codex"
	delivery, err := sendWithRetryTarget(mock, msg, true, opts)
	if delivery == deliveryQueued || delivery == deliverySubmitted || err == nil {
		t.Fatalf("delivery=%q err=%v: a composer holding a new paste marker is not a delivery", delivery, err)
	}
}

// TestIssue1978_NonClaudeBusyBeforeSendIsNotQueued: the content-arrival path
// has no queue acknowledgement to read, so a target that was already busy
// keeps the #1793 verdict rather than an inferred queued.
func TestIssue1978_NonClaudeBusyBeforeSendIsNotQueued(t *testing.T) {
	const msg = "PROBE reply with only OK"
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{busyPaneNoBody(), busyPaneWithBody(msg)},
	}
	opts := queuedOpts(hookSeq(probeBusy))
	opts.tool = "codex"
	delivery, err := sendWithRetryTarget(mock, msg, true, opts)
	if delivery != deliveryTyped || err == nil {
		t.Fatalf("delivery=%q err=%v, want the #1793 typed verdict", delivery, err)
	}
}

// --- Round 2 review of #2273: the affordance is a UI element, not a substring

// busyPaneMentioningQueuedMessages: ordinary transcript text (the assistant's
// reply and our own prompt) contains the words of the affordance, but the
// composer itself is empty. Nothing here is Claude acknowledging a queue.
func busyPaneMentioningQueuedMessages(msg string) string {
	return strings.Join([]string{
		"⏺ I will explain how queued messages work: press up to edit queued messages",
		"  when the composer shows that placeholder.",
		"❯ " + msg,
		"────────────────────────────────────────",
		"❯ ",
		"────────────────────────────────────────",
	}, "\n")
}

func TestIssue1978_QueueAffordanceIsTheComposerElementNotASubstring(t *testing.T) {
	const msg = "PROBE: do queued messages survive a restart?"
	if claudeQueueAcknowledged(busyPaneMentioningQueuedMessages(msg)) {
		t.Fatal("free-text mention of queued messages counted as the composer placeholder")
	}
	if claudeQueueAcknowledged("some output\n❯ Press up to edit queued messages") {
		t.Fatal("placeholder text without a divider-framed composer counted as the affordance")
	}
	if !claudeQueueAcknowledged(busyPaneWithBody(msg)) {
		t.Fatal("the real composer placeholder was not recognised")
	}

	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{busyPaneNoBody(), busyPaneMentioningQueuedMessages(msg)},
	}
	delivery, err := sendWithRetryTarget(mock, msg, false, queuedOpts(hookSeq(probeBusy)))
	if delivery == deliveryQueued || delivery == deliverySubmitted || err == nil {
		t.Fatalf("delivery=%q err=%v: the words in ordinary output must not yield a queued success", delivery, err)
	}
}
