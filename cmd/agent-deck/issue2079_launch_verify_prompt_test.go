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
// These tests exercise the same declared-line-count check
// (send.ExpectedPasteMarkerLines / send.PasteMarkerLineCounts) applied to the
// launch --no-wait poller: a marker declaring fewer lines than the message
// must be reported as "prompt truncated in transit" instead of success, and a
// consumed-looking composer with no marker at all (for a message that expects
// one) must be reported as unknown — never success. All pane strings are
// synthetic per the sanitization rule.

const (
	// paneTruncatedMarker: composer is clear (Enter was accepted) but the
	// paste marker it collapsed behind declares fewer lines than the
	// 3-line message used below — a truncated fragment was submitted.
	paneTruncatedMarker = "> [Pasted text #1 +2 lines]\n" +
		"some output above\n" +
		"─────────────────────────\n" +
		"❯\n" +
		"─────────────────────────\n"

	// paneFullMarker: composer is clear and the paste marker declares
	// exactly as many lines as the 3-line message has — a clean delivery.
	paneFullMarker = "> [Pasted text #1 +3 lines]\n" +
		"some output above\n" +
		"─────────────────────────\n" +
		"❯\n" +
		"─────────────────────────\n"
)

// threeLineMessage has a non-zero send.ExpectedPasteMarkerLines, so the
// declared-line-count check applies (single-line messages never collapse
// behind a paste marker and skip the check entirely).
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
