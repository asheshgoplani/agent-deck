package main

import (
	"bytes"
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

func TestIssue2062PurgeYesClearsUnownedDedupState(t *testing.T) {
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
	if err != nil || len(records) != 1 || records[0].Store != "unowned" {
		t.Fatalf("unowned record missing: %+v err=%v", records, err)
	}
	if err := runInbox(&bytes.Buffer{}, []string{"dead-letter", "purge", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if count, err := session.CountDeadLetterRecords(); err != nil || count != 0 {
		t.Fatalf("warning could not be cleared: count=%d err=%v", count, err)
	}
	if err := session.WriteInboxEvent(session.UnownedInboxID, event); err != nil {
		t.Fatal(err)
	}
	if count, err := session.CountDeadLetterRecords(); err != nil || count != 1 {
		t.Fatalf("purge left stale dedup state: count=%d err=%v", count, err)
	}
}
