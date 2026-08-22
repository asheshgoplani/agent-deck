package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Issues #1409 + #1413: the `session send` path must be prompt-state aware.
//
// #1409: an automated send against a composer holding a half-typed operator
// draft merges with it and submits the merged prompt. The send path must
// hold-and-retry while the composer is busy and, at the bound, save-clear-
// restore the operator draft around the automated send.
//
// #1413: after text + Enter, the message can sit typed-but-unsubmitted while
// the CLI exits 0. The send path must verify submission, retry Enter
// (bounded), and surface a machine-checkable delivery status (nonzero exit +
// `delivery` field in --json) when the message never submits.
// ---------------------------------------------------------------------------

// claudeComposer renders a Claude-style composer block (with dividers) holding
// text as the current input. Empty text renders the empty/ready composer.
func claudeComposer(text string) string {
	div := strings.Repeat("─", 40)
	return "some prior output\n" + div + "\n❯ " + text + "\n" + div + "\n  auto mode on\n"
}

// --- #1413: sendWithRetryTarget reports a delivery status -------------------

func TestSendWithRetryTarget_TypedNotSubmitted_ErrorsWithStatus(t *testing.T) {
	const msg = "instruct the worker to re-run CI now"
	// The composer holds the sent message for the whole budget: one recovery
	// Enter is attempted, and exhaustion must classify the result as
	// typed_not_submitted instead of silently succeeding (issue #1413).
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{claudeComposer(msg)},
	}
	delivery, err := sendWithRetryTarget(mock, msg, false, sendRetryOptions{
		maxRetries: 6, checkDelay: 0, verifyDelivery: true,
	})
	if err == nil {
		t.Fatal("issue #1413: typed-but-unsubmitted message must surface an error, got nil")
	}
	if delivery != deliveryTypedNotSubmitted {
		t.Fatalf("delivery status: want %q, got %q", deliveryTypedNotSubmitted, delivery)
	}
	if !strings.Contains(err.Error(), "not submitted") {
		t.Errorf("error should describe the unsubmitted state, got: %v", err)
	}
	if got := atomic.LoadInt32(&mock.sendEnterCalls); got != 1 {
		t.Errorf("expected exactly one bounded recovery Enter, got %d", got)
	}
}

func TestSendWithRetryTarget_ReportsSubmittedOnActive(t *testing.T) {
	mock := &mockSendRetryTarget{
		statuses: []string{"active"},
		panes:    []string{""},
	}
	delivery, err := sendWithRetryTarget(mock, "hello", false, sendRetryOptions{
		maxRetries: 4, checkDelay: 0, verifyDelivery: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if delivery != deliverySubmitted {
		t.Fatalf("delivery status: want %q, got %q", deliverySubmitted, delivery)
	}
}

func TestSendWithRetryTarget_SkipVerifyReportsUnverified(t *testing.T) {
	mock := &mockSendRetryTarget{statuses: []string{"waiting"}, panes: []string{""}}
	delivery, err := sendWithRetryTarget(mock, "hello", true, sendRetryOptions{
		maxRetries: 2, checkDelay: 0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if delivery != deliveryUnverified {
		t.Fatalf("delivery status: want %q, got %q", deliveryUnverified, delivery)
	}
}

func TestSendWithRetryTarget_ReportsNoEvidenceStatus(t *testing.T) {
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{""},
	}
	delivery, err := sendWithRetryTarget(mock, "hello", false, sendRetryOptions{
		maxRetries: 4, checkDelay: 0, verifyDelivery: true,
	})
	if err == nil {
		t.Fatal("expected #876 no-evidence error")
	}
	if delivery != deliveryNoEvidence {
		t.Fatalf("delivery status: want %q, got %q", deliveryNoEvidence, delivery)
	}
}

// --- #1409: executeSend guards the composer ---------------------------------

// guardedSendMock implements sendRetryTarget with composer-clear semantics:
// the pane shows draftPane until SendCtrlC is called, then emptyPane until
// SendKeysAndEnter is called, then the entries of postSendPanes (last entry
// repeats). Statuses come from postSendStatuses once SendKeysAndEnter ran
// (before that, "waiting").
type guardedSendMock struct {
	draftPane        string
	postSendPanes    []string
	postSendStatuses []string

	ctrlCCalls    int32
	sendKeysCalls int32
	enterCalls    int32
	chunkedCalls  int32
	chunkedText   string
	postSendIdx   int32
	statusIdx     int32

	// restoredAfterSend records whether SendKeysChunked was called only
	// after SendKeysAndEnter (the automated delivery must come first).
	restoredBeforeSend bool

	// chunkedErr, when set, makes SendKeysChunked fail — simulating a draft
	// restore that cannot type the saved draft back onto the composer.
	chunkedErr error
}

func (m *guardedSendMock) SendKeysAndEnter(string) error {
	atomic.AddInt32(&m.sendKeysCalls, 1)
	return nil
}

func (m *guardedSendMock) GetStatus() (string, error) {
	if atomic.LoadInt32(&m.sendKeysCalls) == 0 || len(m.postSendStatuses) == 0 {
		return "waiting", nil
	}
	i := int(atomic.AddInt32(&m.statusIdx, 1)) - 1
	if i >= len(m.postSendStatuses) {
		i = len(m.postSendStatuses) - 1
	}
	return m.postSendStatuses[i], nil
}

func (m *guardedSendMock) SendEnter() error {
	atomic.AddInt32(&m.enterCalls, 1)
	return nil
}

func (m *guardedSendMock) SendCtrlC() error {
	atomic.AddInt32(&m.ctrlCCalls, 1)
	return nil
}

func (m *guardedSendMock) SendKeysChunked(text string) error {
	atomic.AddInt32(&m.chunkedCalls, 1)
	m.chunkedText = text
	if atomic.LoadInt32(&m.sendKeysCalls) == 0 {
		m.restoredBeforeSend = true
	}
	return m.chunkedErr
}

func (m *guardedSendMock) CapturePaneFresh() (string, error) {
	if atomic.LoadInt32(&m.sendKeysCalls) > 0 {
		if len(m.postSendPanes) == 0 {
			return claudeComposer(""), nil
		}
		i := int(atomic.AddInt32(&m.postSendIdx, 1)) - 1
		if i >= len(m.postSendPanes) {
			i = len(m.postSendPanes) - 1
		}
		return m.postSendPanes[i], nil
	}
	if atomic.LoadInt32(&m.ctrlCCalls) > 0 {
		return claudeComposer(""), nil
	}
	return m.draftPane, nil
}

func testGuardTuning(retry sendRetryOptions) sendExecTuning {
	return sendExecTuning{
		guardHold:      0,
		guardPoll:      time.Millisecond,
		guardClearWait: 50 * time.Millisecond,
		preflightWait:  100 * time.Millisecond,
		preflightPoll:  time.Millisecond,
		settleDelay:    0,
		retry:          retry,
	}
}

func TestExecuteSend_HoldsWhileOperatorDraftPresent(t *testing.T) {
	// The operator's draft clears on its own (they submit or erase it) before
	// the hold bound: no Ctrl+C, no save, normal delivery.
	mock := &mockSendRetryTarget{
		statuses: []string{"active", "active"},
		panes: []string{
			claudeComposer("instruct deploy ag"), // guard: busy
			claudeComposer("instruct deploy ag"), // guard: still busy
			claudeComposer(""),                   // guard: operator cleared it
			"",                                   // verification loop
		},
	}
	tun := testGuardTuning(sendRetryOptions{maxRetries: 5, checkDelay: 0, verifyDelivery: true})
	tun.guardHold = 500 * time.Millisecond
	res, err := executeSend(mock, "claude", "[EVENT] child waiting", false, tun)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := atomic.LoadInt32(&mock.sendCtrlCCalls); got != 0 {
		t.Fatalf("issue #1409: hold-and-retry succeeded, must not Ctrl+C, got %d", got)
	}
	if res.draftSaved != "" {
		t.Fatalf("no draft should be saved when the hold succeeds, got %q", res.draftSaved)
	}
	if res.delivery != deliverySubmitted {
		t.Fatalf("delivery: want %q, got %q", deliverySubmitted, res.delivery)
	}
	if got := atomic.LoadInt32(&mock.sendKeysCalls); got != 1 {
		t.Fatalf("expected exactly 1 SendKeysAndEnter, got %d", got)
	}
}

func TestExecuteSend_SaveClearRestoreAroundBusyComposer(t *testing.T) {
	// The operator draft never clears on its own: at the hold bound the guard
	// must save it, clear the composer, deliver the automated message, and
	// restore the draft after delivery is confirmed.
	mock := &guardedSendMock{
		draftPane:        claudeComposer("instruct deploy ag"),
		postSendPanes:    []string{""},
		postSendStatuses: []string{"active", "active"},
	}
	tun := testGuardTuning(sendRetryOptions{maxRetries: 5, checkDelay: 0, verifyDelivery: true})
	res, err := executeSend(mock, "claude", "[EVENT] child waiting", false, tun)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := atomic.LoadInt32(&mock.ctrlCCalls); got != 1 {
		t.Fatalf("expected 1 Ctrl+C to clear the busy composer, got %d", got)
	}
	if res.draftSaved != "instruct deploy ag" {
		t.Fatalf("saved draft: want %q, got %q", "instruct deploy ag", res.draftSaved)
	}
	if !res.draftRestored {
		t.Fatal("issue #1409: operator draft must be restored after delivery")
	}
	if mock.chunkedText != "instruct deploy ag" {
		t.Fatalf("restored text: want %q, got %q", "instruct deploy ag", mock.chunkedText)
	}
	if mock.restoredBeforeSend {
		t.Fatal("draft restore must happen after the automated delivery, not before")
	}
	if res.delivery != deliverySubmitted {
		t.Fatalf("delivery: want %q, got %q", deliverySubmitted, res.delivery)
	}
}

func TestExecuteSend_NoRestoreWhenTypedNotSubmitted(t *testing.T) {
	// If the automated message itself ends typed-but-unsubmitted, restoring
	// the operator draft would merge it into the stuck composer — exactly the
	// #1409 collision. The draft must stay saved (surfaced to the caller),
	// not retyped.
	const msg = "[EVENT] child waiting on approval gate"
	mock := &guardedSendMock{
		draftPane:        claudeComposer("instruct deploy ag"),
		postSendPanes:    []string{claudeComposer(msg)},
		postSendStatuses: []string{"waiting"},
	}
	tun := testGuardTuning(sendRetryOptions{maxRetries: 4, checkDelay: 0, verifyDelivery: true})
	res, err := executeSend(mock, "claude", msg, false, tun)
	if err == nil {
		t.Fatal("expected typed_not_submitted error")
	}
	if res.delivery != deliveryTypedNotSubmitted {
		t.Fatalf("delivery: want %q, got %q", deliveryTypedNotSubmitted, res.delivery)
	}
	if res.draftRestored || atomic.LoadInt32(&mock.chunkedCalls) != 0 {
		t.Fatal("draft must NOT be restored into a composer stuck holding the automated message")
	}
	if res.draftSaved != "instruct deploy ag" {
		t.Fatalf("saved draft must be surfaced for recovery, got %q", res.draftSaved)
	}
}

func TestExecuteSend_RestoreFailureIsSurfacedNotSwallowed(t *testing.T) {
	// Delivery succeeds and the guard cleared the operator draft, but typing
	// the draft back fails (SendKeysChunked errors). The draft is no longer on
	// screen, so the result must flag the restore failure and keep draftSaved
	// for recovery — not report a clean success that silently lost the draft.
	mock := &guardedSendMock{
		draftPane:        claudeComposer("instruct deploy ag"),
		postSendPanes:    []string{""},
		postSendStatuses: []string{"active", "active"},
		chunkedErr:       errors.New("tmux send-keys failed"),
	}
	tun := testGuardTuning(sendRetryOptions{maxRetries: 5, checkDelay: 0, verifyDelivery: true})
	res, err := executeSend(mock, "claude", "[EVENT] child waiting", false, tun)
	if err != nil {
		t.Fatalf("delivery should still succeed (the automated message went through): %v", err)
	}
	if res.delivery != deliverySubmitted {
		t.Fatalf("delivery: want %q, got %q", deliverySubmitted, res.delivery)
	}
	if res.draftSaved != "instruct deploy ag" {
		t.Fatalf("saved draft must be retained for recovery, got %q", res.draftSaved)
	}
	if res.draftRestored {
		t.Fatal("draftRestored must be false when the type-back failed")
	}
	if !res.draftRestoreFailed {
		t.Fatal("draftRestoreFailed must be set so the lost draft is surfaced, not swallowed")
	}
	if _, ok := res.jsonFields()["draft_restore_failed"]; !ok {
		t.Fatal("draft_restore_failed must appear in jsonFields for machine-readable recovery")
	}
}

func TestExecuteSend_NoWaitStillGuardsComposer(t *testing.T) {
	// --no-wait skips the readiness wait, NOT the composer guard (#1409) or
	// submit verification (#1413).
	mock := &guardedSendMock{
		draftPane:        claudeComposer("half typed operator message"),
		postSendPanes:    []string{""},
		postSendStatuses: []string{"active", "active"},
	}
	tun := testGuardTuning(noWaitSendOptions())
	tun.retry.checkDelay = 0
	res, err := executeSend(mock, "claude", "[INBOX] wake", true, tun)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := atomic.LoadInt32(&mock.ctrlCCalls); got != 1 {
		t.Fatalf("--no-wait must still clear a busy composer, got %d Ctrl+C calls", got)
	}
	if !res.draftRestored || mock.chunkedText != "half typed operator message" {
		t.Fatalf("--no-wait must still restore the draft, got restored=%v text=%q",
			res.draftRestored, mock.chunkedText)
	}
	if got := atomic.LoadInt32(&mock.sendKeysCalls); got != 1 {
		t.Fatalf("#479 guard: exactly 1 SendKeysAndEnter expected, got %d", got)
	}
}

func TestExecuteSend_NonClaudeToolSkipsGuard(t *testing.T) {
	// Composer introspection is Claude-shaped; non-Claude tools must not pay
	// the guard (no captures-before-send semantics change, no Ctrl+C).
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{claudeComposer("looks like a draft")},
	}
	tun := testGuardTuning(sendRetryOptions{maxRetries: 2, checkDelay: 0})
	res, err := executeSend(mock, "codex", "run tests", false, tun)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := atomic.LoadInt32(&mock.sendCtrlCCalls); got != 0 {
		t.Fatalf("non-Claude tool must not be composer-guarded, got %d Ctrl+C calls", got)
	}
	if res.delivery != deliveryUnverified {
		t.Fatalf("delivery: want %q (non-Claude skips verify), got %q", deliveryUnverified, res.delivery)
	}
}

func TestExecuteSend_HappyPathUnchanged(t *testing.T) {
	// Empty composer, message accepted immediately: one send, no Ctrl+C, no
	// chunked restore, submitted.
	mock := &mockSendRetryTarget{
		statuses: []string{"active", "active"},
		panes:    []string{claudeComposer(""), ""},
	}
	tun := testGuardTuning(sendRetryOptions{maxRetries: 5, checkDelay: 0, verifyDelivery: true})
	res, err := executeSend(mock, "claude", "status update please", false, tun)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.delivery != deliverySubmitted || res.draftSaved != "" || res.draftRestored {
		t.Fatalf("happy path regressed: %+v", res)
	}
	if got := atomic.LoadInt32(&mock.sendKeysCalls); got != 1 {
		t.Fatalf("expected 1 SendKeysAndEnter, got %d", got)
	}
	if got := atomic.LoadInt32(&mock.sendCtrlCCalls); got != 0 {
		t.Fatalf("expected 0 SendCtrlC, got %d", got)
	}
}

// --- production tuning bounds ------------------------------------------------

func TestNoWaitSendTuning_GuardLatencyIsSmallAndBounded(t *testing.T) {
	tun := noWaitSendTuning()
	if tun.guardHold <= 0 || tun.guardHold > 3*time.Second {
		t.Fatalf("--no-wait composer hold must be small and bounded (0 < hold <= 3s), got %v", tun.guardHold)
	}
	if tun.guardClearWait <= 0 || tun.guardClearWait > 2*time.Second {
		t.Fatalf("--no-wait clear wait must be bounded, got %v", tun.guardClearWait)
	}
	if !tun.retry.verifyDelivery {
		t.Fatal("--no-wait must keep delivery verification on (#876/#1413)")
	}
}

func TestDefaultSendTuning_GuardBounds(t *testing.T) {
	tun := defaultSendTuning()
	if tun.guardHold <= 0 || tun.guardHold > 15*time.Second {
		t.Fatalf("default composer hold must be bounded (0 < hold <= 15s), got %v", tun.guardHold)
	}
	if !tun.retry.verifyDelivery {
		t.Fatal("default path must keep delivery verification on (#876/#1413)")
	}
}

// --- #1413: machine-checkable --json contract --------------------------------

func TestCLIOutput_ErrorWithDataIncludesDeliveryStatus(t *testing.T) {
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	out := NewCLIOutput(true, false)
	out.ErrorWithData("message typed but not submitted", ErrCodeDeliveryFailed, map[string]interface{}{
		"delivery":    deliveryTypedNotSubmitted,
		"saved_draft": "instruct deploy ag",
	})
	_ = w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatalf("error output is not valid JSON: %v\n%s", err, buf.String())
	}
	if payload["success"] != false {
		t.Errorf("success: want false, got %v", payload["success"])
	}
	if payload["delivery"] != deliveryTypedNotSubmitted {
		t.Errorf("delivery: want %q, got %v", deliveryTypedNotSubmitted, payload["delivery"])
	}
	if payload["code"] != ErrCodeDeliveryFailed {
		t.Errorf("code: want %q, got %v", ErrCodeDeliveryFailed, payload["code"])
	}
	if payload["saved_draft"] != "instruct deploy ag" {
		t.Errorf("saved_draft: want preserved draft, got %v", payload["saved_draft"])
	}
}
