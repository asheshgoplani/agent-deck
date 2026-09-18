package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestInboxHelpListsOnlyWiredDeadLetterSubcommands pins issue #2062's actual
// state in this tree: only `dead-letter list|show` is implemented
// (runInboxDeadLetter in inbox_deadletter_cmd.go rejects any other verb).
// PR #2230, which adds retry/purge and a TUI Alt+D panel, is still open and
// unmerged upstream — none of that code is present here. The help text must
// therefore keep advertising exactly the wired surface: claiming retry/purge
// exist when they return a plain usage error would be worse than the
// original bug (a discoverable command that immediately fails).
//
// This test is a regression guard, not evidence retry/purge are done: it
// intentionally FAILS the moment someone adds "retry"/"purge" to the help
// text without also wiring runInboxDeadLetter to handle them.
func TestInboxHelpListsOnlyWiredDeadLetterSubcommands(t *testing.T) {
	var buf bytes.Buffer
	printInboxUsage(&buf)
	out := buf.String()

	if !strings.Contains(out, "dead-letter list|show") {
		t.Fatalf("help text must advertise the wired dead-letter subcommands:\n%s", out)
	}
	for _, unwired := range []string{"retry", "purge"} {
		if strings.Contains(out, unwired) {
			t.Fatalf("help text advertises %q, but runInboxDeadLetter does not implement it (PR #2230 unmerged):\n%s", unwired, out)
		}
	}
}

// TestInboxDeadLetterRetryAndPurgeAreNotWired documents the actual gap
// behind issue #2062: `retry` and `purge` are not usage-error stubs waiting
// to be listed in help — they do not exist in runInboxDeadLetter at all.
// Once PR #2230 (or an equivalent) lands, this test should be replaced with
// the acceptance tests test_gap describes (seeded dead-letter records,
// retry success/failure, purge consent gating) rather than updated in place.
func TestInboxDeadLetterRetryAndPurgeAreNotWired(t *testing.T) {
	for _, args := range [][]string{
		{"retry", "deadbeef"},
		{"purge", "--older-than", "30d"},
		{"purge", "--yes"},
	} {
		err := runInboxDeadLetter(&bytes.Buffer{}, args)
		if err == nil {
			t.Fatalf("inbox dead-letter %v unexpectedly succeeded; if this now works, issue #2062's retry/purge feature has landed and this test (and the help text) should be updated together", args)
		}
	}
}
