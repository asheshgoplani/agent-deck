package main

import (
	"bytes"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Round 2 of issue #2079: the launch `--no-wait` path (verifyPromptConsumedAfterLaunch,
// via pollPromptConsumed) is the SECOND call site that can report success on a
// truncated paste. sendMessageWhenReady (internal/session/instance.go) already
// withholds Enter when a paste marker declares fewer lines than the message,
// but pollPromptConsumed only ever asked "is the composer empty?" — a
// truncated fragment that got submitted clears the composer exactly like a
// clean delivery, so this poller called that a success too.
//
// These tests exercise the same declared-count check
// (send.ExpectedPasteMarkerLineBreaks / send.CheckPasteMarker) applied to the
// launch --no-wait poller: a marker declaring fewer hard line breaks than the
// message has must be reported as "prompt truncated in transit" instead of
// success, and a consumed-looking composer with no marker at all (for a
// message that expects one) must be reported as unknown — never success. All
// pane strings are synthetic per the sanitization rule.

const (
	// paneTruncatedMarker: composer is clear (Enter was accepted) but the
	// paste marker it collapsed behind declares fewer line breaks (1) than
	// the 3-line message used below has (2) — a truncated fragment was
	// submitted.
	paneTruncatedMarker = "> [Pasted text #1 +1 lines]\n" +
		"some output above\n" +
		"─────────────────────────\n" +
		"❯\n" +
		"─────────────────────────\n"

	// paneFullMarker: composer is clear and the paste marker declares exactly
	// as many hard line breaks as the 3-line message has (Claude's "+M lines"
	// counts line breaks, so 3 lines show "+2 lines") — a clean delivery.
	paneFullMarker = "> [Pasted text #1 +2 lines]\n" +
		"some output above\n" +
		"─────────────────────────\n" +
		"❯\n" +
		"─────────────────────────\n"
)

// threeLineMessage has a non-zero send.ExpectedPasteMarkerLineBreaks, so the
// declared-count check applies (single-line messages carry no count to
// compare and skip the check entirely).
const threeLineMessage = "line one\nline two\nline three"

func TestPollPromptConsumed_TruncatedMarker_ReportsTruncatedNotSuccess(t *testing.T) {
	mock := &mockSendRetryTarget{panes: []string{paneTruncatedMarker}}
	var warn bytes.Buffer

	verifyPromptConsumedAfterLaunch(mock, threeLineMessage, 10*time.Millisecond, time.Millisecond, &warn)

	if got := atomic.LoadInt32(&mock.sendKeysCalls); got != 0 {
		t.Fatalf("a truncated delivery must not be retried (would resubmit/duplicate); SendKeysAndEnter calls=%d want=0", got)
	}
	if warn.Len() == 0 {
		t.Fatal("a truncated marker must be reported, not silently treated as success")
	}
	if !strings.Contains(warn.String(), "prompt truncated in transit") {
		t.Fatalf(`warning must say "prompt truncated in transit"; got %q`, warn.String())
	}
}

func TestPollPromptConsumed_NoMarkerObserved_ReportsUnknownNotSuccess(t *testing.T) {
	// Composer looks consumed (empty) for the whole poll window, but no
	// paste marker ever appears for a message that expects one. Must be
	// reported as unknown, never as success.
	mock := &mockSendRetryTarget{panes: []string{paneConsumed}}
	var warn bytes.Buffer

	verifyPromptConsumedAfterLaunch(mock, threeLineMessage, 10*time.Millisecond, time.Millisecond, &warn)

	if got := atomic.LoadInt32(&mock.sendKeysCalls); got != 0 {
		t.Fatalf("an unconfirmed-but-consumed-looking composer must not be retried (risk of duplicate submission); SendKeysAndEnter calls=%d want=0", got)
	}
	if warn.Len() == 0 {
		t.Fatal("an unconfirmed delivery must be reported, not silently treated as success")
	}
	if !strings.Contains(strings.ToLower(warn.String()), "unknown") {
		t.Fatalf(`warning must mention "unknown"; got %q`, warn.String())
	}
}

func TestPollPromptConsumed_MatchingMarker_StillReportsSuccess(t *testing.T) {
	// Regression guard: a marker that declares at least as many lines as
	// the message must still be treated as a clean, silent success.
	mock := &mockSendRetryTarget{panes: []string{paneFullMarker}}
	var warn bytes.Buffer

	verifyPromptConsumedAfterLaunch(mock, threeLineMessage, 10*time.Millisecond, time.Millisecond, &warn)

	if got := atomic.LoadInt32(&mock.sendKeysCalls); got != 0 {
		t.Fatalf("a clean delivery must not be retried; SendKeysAndEnter calls=%d want=0", got)
	}
	if warn.Len() != 0 {
		t.Fatalf("a clean delivery must not warn; got %q", warn.String())
	}
}

// TestPollPromptConsumed_ExactLaunchPlaceholder_IsSuccess is the rc.3 P1: the
// exact 139-char one-line `launch -m` prompt becomes a 6-line message once
// the completion-sentinel block is appended, and Claude's composer renders it
// as "[Pasted text #1 +5 lines]" (5 hard line breaks). That is a clean
// delivery and must be silent — the previous physical-line comparison (6)
// called it truncated. The transcript echo is soft-wrapped at 60 columns so
// the pane holds far more display rows than the message has lines.
func TestPollPromptConsumed_ExactLaunchPlaceholder_IsSuccess(t *testing.T) {
	line := "Read /tmp/exec-carry-2147/PROMPT.md and execute it autonomously end-to-end. Write RESULTS.md and PR-BODY.md and end with the sentinel line."
	message := applyAssertDone(line, true)
	if strings.Count(message, "\n") != 5 {
		t.Fatalf("fixture: launch message must have 5 hard line breaks, got %d", strings.Count(message, "\n"))
	}
	pane := "> " + line[:60] + "\n" + line[60:120] + "\n" + line[120:] + "\n" +
		"> [Pasted text #1 +5 lines]\n" +
		"─────────────────────────\n" +
		"❯\n" +
		"─────────────────────────\n"
	mock := &mockSendRetryTarget{panes: []string{pane}}
	var warn bytes.Buffer

	verifyPromptConsumedAfterLaunch(mock, message, 10*time.Millisecond, time.Millisecond, &warn)

	if got := atomic.LoadInt32(&mock.sendKeysCalls); got != 0 {
		t.Fatalf("a clean delivery must not be retried; SendKeysAndEnter calls=%d want=0", got)
	}
	if warn.Len() != 0 {
		t.Fatalf("the exact launch placeholder is a clean delivery and must not warn; got %q", warn.String())
	}
}

// TestPollPromptConsumed_LaunchPlaceholderOneShort_IsTruncated keeps #2079's
// protection for the same message: a marker one break short means the head
// of the prompt was swallowed.
func TestPollPromptConsumed_LaunchPlaceholderOneShort_IsTruncated(t *testing.T) {
	message := applyAssertDone("Read the prompt file and execute it.", true)
	pane := "> [Pasted text #1 +4 lines]\n" +
		"─────────────────────────\n" +
		"❯\n" +
		"─────────────────────────\n"
	mock := &mockSendRetryTarget{panes: []string{pane}}
	var warn bytes.Buffer

	verifyPromptConsumedAfterLaunch(mock, message, 10*time.Millisecond, time.Millisecond, &warn)

	if !strings.Contains(warn.String(), "prompt truncated in transit") {
		t.Fatalf(`a marker one break short must be reported as truncated; got %q`, warn.String())
	}
}
