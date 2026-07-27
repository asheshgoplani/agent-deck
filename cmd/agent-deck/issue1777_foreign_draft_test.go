package main

import (
	"strings"
	"sync/atomic"
	"testing"
)

// Issue #1777: a Claude autosuggestion can materialize in the composer as
// REAL, normal-coloured (\e[39m) unsubmitted input. The send-verify loop's
// fallback Enter nudges must never fire into that state — a bare Enter would
// submit an instruction no operator wrote.

// claudeComposerANSI renders a composer block with ANSI attributes intact,
// mirroring `tmux capture-pane -e` output (claudeComposer in
// issue1409_1413_send_guard_test.go is the stripped equivalent).
func claudeComposerANSI(body string) string {
	div := strings.Repeat("─", 40)
	return "some prior output\n" + div + "\n\x1b[39m❯ " + body + "\n" + div + "\n  auto mode on\n"
}

func TestSendWithRetryTarget_ForeignComposerContentBlocksEnterNudges(t *testing.T) {
	// A materialized suggestion (one of the reporter's live specimens) is
	// parked in the composer; the target never goes active. Every waiting
	// and ambiguous check would previously nudge a bare Enter — submitting
	// the foreign text. With the attribution gate no Enter may fire.
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{claudeComposerANSI("upgrade agent-deck on carrollton and archive the five error sessions")},
	}
	_, _ = sendWithRetryTarget(mock, "status update please", false, sendRetryOptions{
		maxRetries: 6, checkDelay: 0, maxFullResends: -1,
	})
	if got := atomic.LoadInt32(&mock.sendEnterCalls); got != 0 {
		t.Fatalf("issue #1777: bare Enter nudges must not fire while foreign content is parked in the composer, got %d", got)
	}
	if got := atomic.LoadInt32(&mock.sendCtrlCCalls); got != 0 {
		t.Fatalf("full resends are disabled; nothing may clear the pane, got %d Ctrl+C calls", got)
	}
}

func TestSendWithRetryTarget_OwnParkedMessageStillNudged(t *testing.T) {
	// Recovery regression guard: the composer holding OUR message is exactly
	// the state the retry Enter exists for — the gate must not block it.
	const msg = "instruct the worker to re-run CI now"
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{claudeComposerANSI(msg)},
	}
	_, _ = sendWithRetryTarget(mock, msg, false, sendRetryOptions{
		maxRetries: 4, checkDelay: 0, maxFullResends: -1,
	})
	if got := atomic.LoadInt32(&mock.sendEnterCalls); got == 0 {
		t.Fatal("agent-deck's own parked message must still receive recovery Enters")
	}
}

func TestSendWithRetryTarget_GhostSuggestionDoesNotBlockNudges(t *testing.T) {
	// A still-dim ghost (\e[2m) means the composer buffer is empty: nudges
	// stay allowed (they submit nothing), so delivery recovery for panes
	// showing only a suggestion is unchanged.
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{claudeComposerANSI("\x1b[2mConfirm the audit row after the next heartbeat\x1b[0m")},
	}
	_, _ = sendWithRetryTarget(mock, "status update please", false, sendRetryOptions{
		maxRetries: 4, checkDelay: 0, maxFullResends: -1,
	})
	if got := atomic.LoadInt32(&mock.sendEnterCalls); got == 0 {
		t.Fatal("a dim ghost holds no buffer content; Enter nudges must remain allowed")
	}
}
