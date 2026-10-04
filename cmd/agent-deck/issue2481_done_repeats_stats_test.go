package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Issue #2481: identical done repeats are counted, so the counter must be
// visible in `inbox stats` (text and JSON) and in the ledger summary the CLI
// and TUI agents views share.
func TestIssue2481_InboxStatsShowsDoneRepeats(t *testing.T) {
	var buf bytes.Buffer
	printInboxStats(&buf, session.InboxStats{Parent: "p1", StartedAt: time.Now(), RecordsUrgent: 1, DoneRepeats: 4})
	out := buf.String()
	if !strings.Contains(out, "done_repeats=4") {
		t.Fatalf("inbox stats must print the done repeat counter:\n%s", out)
	}
	if !strings.Contains(out, "signal ratio 20%") {
		t.Fatalf("done repeats are suppressed turns in the signal ratio:\n%s", out)
	}
	entry := session.CompletionLedgerEntry{Status: "ok", Summary: "board at zero", Repeats: 4}
	if got := entry.DisplaySummary(); got != "board at zero (repeated 4x, not delivered)" {
		t.Fatalf("ledger display: %q", got)
	}
	if got := (session.CompletionLedgerEntry{Status: "fail"}).DisplaySummary(); got != "reported fail" {
		t.Fatalf("empty summary display: %q", got)
	}
}
