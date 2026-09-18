package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// These tests exercise the retry/purge/help surface added back for #2062
// (originally carry/2230, PR #2230 by nandanadileep, reapplied on top of
// #2111's DeadLetterRecord/InspectDeadLetters after the type collision
// documented in RESULTS.md). `list`/`show` themselves, and their exact
// output shape, remain #2111's inspection surface and are covered by
// TestDeadLetterInspectionPreservesBothRawStores and
// TestDeadLetterInspectionRejectsUnsafeOrUnknownRequests in
// inbox_deadletter_cmd_test.go — this file uses session.ListDeadLetters
// directly (the management-side reader) to find IDs to retry/purge, exactly
// as the TUI panel does.

func seedDeadLetterCLIRecord(t *testing.T, child, reason string, at time.Time) string {
	t.Helper()
	path := session.DeadLetterPathFor(child)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	raw := `{"child_session_id":"` + child + `","child_title":"worker","profile":"default","target_session_id":"missing-parent","from_status":"running","to_status":"waiting","timestamp":"` + at.Format(time.RFC3339Nano) + `","attempts":5,"dead_letter_reason":"` + reason + `","done_summary":"secret payload that must not be printed in full"}` + "\n"
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestIssue2062RetryMissingTargetRetainsRecord(t *testing.T) {
	cliInboxTestHome(t)
	seedDeadLetterCLIRecord(t, "removed-child", "child_removed", time.Now())
	records, _ := session.ListDeadLetters()

	err := runInbox(&bytes.Buffer{}, []string{"dead-letter", "retry", records[0].ID})
	if err == nil || !strings.Contains(err.Error(), "no longer exists") {
		t.Fatalf("retry must fail honestly, got %v", err)
	}
	remaining, _ := session.ListDeadLetters()
	if len(remaining) != 1 {
		t.Fatalf("failed retry removed record: %+v", remaining)
	}
}

func TestIssue2062SuccessfulRetryRemovesOnlyDeliveredRecord(t *testing.T) {
	cliInboxTestHome(t)
	parent := session.NewInstance("parent", t.TempDir())
	parent.ID = "live-parent"
	child := session.NewInstance("child", t.TempDir())
	child.ID = "retry-child"
	child.ParentSessionID = parent.ID
	saveInboxResolutionSessions(t, "default", parent, child)
	seedDeadLetterCLIRecord(t, child.ID, "parent_removed", time.Now())
	seedDeadLetterCLIRecord(t, "leave-me", "orphan", time.Now())
	records, _ := session.ListDeadLetters()
	var retryID string
	for _, record := range records {
		if record.ChildSessionID == child.ID {
			retryID = record.ID
		}
	}
	if retryID == "" {
		t.Fatal("retry record not found")
	}
	if err := runInbox(&bytes.Buffer{}, []string{"dead-letter", "retry", retryID}); err != nil {
		t.Fatalf("retry: %v", err)
	}
	delivered, err := session.DrainInboxForParent(parent.ID)
	if err != nil || len(delivered) != 1 || delivered[0].ChildSessionID != child.ID {
		t.Fatalf("delivered=%+v err=%v", delivered, err)
	}
	remaining, _ := session.ListDeadLetters()
	if len(remaining) != 1 || remaining[0].ChildSessionID != "leave-me" {
		t.Fatalf("retry removed the wrong records: %+v", remaining)
	}
	if err := runInbox(&bytes.Buffer{}, []string{"dead-letter", "retry", retryID}); err == nil {
		t.Fatal("removed record was redelivered")
	}
}

func TestIssue2062PurgeRequiresConsentOrAgeBound(t *testing.T) {
	cliInboxTestHome(t)
	seedDeadLetterCLIRecord(t, "old-child", "orphan", time.Now().Add(-48*time.Hour))

	if err := runInbox(&bytes.Buffer{}, []string{"dead-letter", "purge"}); err == nil {
		t.Fatal("unconfirmed unbounded purge succeeded")
	}
	if records, _ := session.ListDeadLetters(); len(records) != 1 {
		t.Fatal("unsafe purge deleted a record")
	}
	if err := runInbox(&bytes.Buffer{}, []string{"dead-letter", "purge", "--older-than", "24h"}); err != nil {
		t.Fatalf("bounded purge: %v", err)
	}
	if records, _ := session.ListDeadLetters(); len(records) != 0 {
		t.Fatalf("bounded purge retained old record: %+v", records)
	}
}

func TestIssue2062HelpListsAllFourSubcommands(t *testing.T) {
	cliInboxTestHome(t)
	var help bytes.Buffer
	if err := runInbox(&help, []string{"dead-letter", "help"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"list", "show", "retry", "purge"} {
		if !strings.Contains(help.String(), want) {
			t.Fatalf("subcommand help missing %q: %q", want, help.String())
		}
	}
}

// TestIssue2062PurgeYesNeverTouchesUnowned is the r2 follow-up: purge --yes
// (and --older-than) must never remove an _unowned record, because that
// ledger has no ack path — only SweepInboxByTTL may reclaim it (see
// session.UnownedPurgeSkipReason and unowned_inbox.go). Earlier behavior
// (superseded here) let purge silently erase the only evidence a remote
// session had stalled; this test now asserts the opposite and checks the
// human-readable skip count purge reports.
func TestIssue2062PurgeYesNeverTouchesUnowned(t *testing.T) {
	cliInboxTestHome(t)
	event := session.TransitionNotificationEvent{
		ChildSessionID:   "unowned-child",
		Profile:          "default",
		FromStatus:       "running",
		ToStatus:         "waiting",
		Timestamp:        time.Now().Add(-time.Hour),
		DeadLetterReason: "orphan",
	}
	if err := session.WriteInboxEvent(session.UnownedInboxID, event); err != nil {
		t.Fatal(err)
	}
	seedDeadLetterCLIRecord(t, "dead-letter-child", "orphan", time.Now())
	records, err := session.ListDeadLetters()
	if err != nil || len(records) != 2 {
		t.Fatalf("seed failed: %+v err=%v", records, err)
	}

	var out bytes.Buffer
	if err := runInbox(&out, []string{"dead-letter", "purge", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Purged 1 dead-letter record(s); skipped 1 _unowned record(s)") {
		t.Fatalf("purge summary missing skip count: %q", out.String())
	}
	remaining, err := session.ListDeadLetters()
	if err != nil || len(remaining) != 1 || remaining[0].Store != "unowned" {
		t.Fatalf("purge must leave the _unowned record alone: %+v err=%v", remaining, err)
	}
	if count, err := session.CountDeadLetterRecords(); err != nil || count != 1 {
		t.Fatalf("unowned record should still be pending: count=%d err=%v", count, err)
	}
}

// TestIssue2062PurgeSingleUnownedRecordRefused covers the TUI/single-ID path
// (session.PurgeDeadLetter): it must refuse an _unowned record outright
// rather than silently skipping it, since there is no bulk summary line to
// report the skip through.
func TestIssue2062PurgeSingleUnownedRecordRefused(t *testing.T) {
	cliInboxTestHome(t)
	event := session.TransitionNotificationEvent{
		ChildSessionID:   "unowned-child",
		Profile:          "default",
		FromStatus:       "running",
		ToStatus:         "waiting",
		Timestamp:        time.Now().Add(-time.Hour),
		DeadLetterReason: "orphan",
	}
	if err := session.WriteInboxEvent(session.UnownedInboxID, event); err != nil {
		t.Fatal(err)
	}
	records, err := session.ListDeadLetters()
	if err != nil || len(records) != 1 {
		t.Fatalf("seed failed: %+v err=%v", records, err)
	}
	if err := session.PurgeDeadLetter(records[0].ID); err == nil {
		t.Fatal("PurgeDeadLetter must refuse an _unowned record")
	}
	remaining, err := session.ListDeadLetters()
	if err != nil || len(remaining) != 1 {
		t.Fatalf("refused purge must not remove the record: %+v err=%v", remaining, err)
	}
}

// TestIssue2062RetryJSONShape and TestIssue2062PurgeJSONShape cover the r2
// --json parity follow-up: retry/purge now emit the same per-record
// {id, action, outcome, reason} shape as each other.
func TestIssue2062RetryJSONShape(t *testing.T) {
	cliInboxTestHome(t)
	parent := session.NewInstance("parent", t.TempDir())
	parent.ID = "live-parent"
	child := session.NewInstance("child", t.TempDir())
	child.ID = "retry-json-child"
	child.ParentSessionID = parent.ID
	saveInboxResolutionSessions(t, "default", parent, child)
	seedDeadLetterCLIRecord(t, child.ID, "parent_removed", time.Now())
	records, _ := session.ListDeadLetters()
	if len(records) != 1 {
		t.Fatalf("seed failed: %+v", records)
	}

	var out bytes.Buffer
	if err := runInbox(&out, []string{"dead-letter", "retry", "--json", records[0].ID}); err != nil {
		t.Fatalf("retry --json: %v", err)
	}
	var results []session.DeadLetterActionOutcome
	if err := json.Unmarshal(out.Bytes(), &results); err != nil {
		t.Fatalf("retry --json output not valid JSON array: %v (%q)", err, out.String())
	}
	if len(results) != 1 || results[0].ID != records[0].ID || results[0].Action != "retry" || results[0].Outcome != "delivered" {
		t.Fatalf("unexpected retry --json shape: %+v", results)
	}
}

func TestIssue2062PurgeJSONShape(t *testing.T) {
	cliInboxTestHome(t)
	event := session.TransitionNotificationEvent{
		ChildSessionID:   "unowned-child",
		Profile:          "default",
		FromStatus:       "running",
		ToStatus:         "waiting",
		Timestamp:        time.Now().Add(-time.Hour),
		DeadLetterReason: "orphan",
	}
	if err := session.WriteInboxEvent(session.UnownedInboxID, event); err != nil {
		t.Fatal(err)
	}
	seedDeadLetterCLIRecord(t, "purge-json-child", "orphan", time.Now())

	var out bytes.Buffer
	if err := runInbox(&out, []string{"dead-letter", "purge", "--json", "--yes"}); err != nil {
		t.Fatalf("purge --json: %v", err)
	}
	var results []session.DeadLetterActionOutcome
	if err := json.Unmarshal(out.Bytes(), &results); err != nil {
		t.Fatalf("purge --json output not valid JSON array: %v (%q)", err, out.String())
	}
	if len(results) != 2 {
		t.Fatalf("expected one removed and one skipped record, got: %+v", results)
	}
	var sawRemoved, sawSkipped bool
	for _, r := range results {
		if r.Action != "purge" {
			t.Fatalf("unexpected action: %+v", r)
		}
		switch r.Outcome {
		case "removed":
			sawRemoved = true
		case "skipped":
			sawSkipped = true
			if r.Reason == "" {
				t.Fatalf("skipped record must carry a reason: %+v", r)
			}
		default:
			t.Fatalf("unexpected outcome: %+v", r)
		}
	}
	if !sawRemoved || !sawSkipped {
		t.Fatalf("expected both a removed and a skipped record: %+v", results)
	}
}
