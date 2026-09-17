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

func TestIssue2062DeadLetterListAndShow(t *testing.T) {
	cliInboxTestHome(t)
	seedDeadLetterCLIRecord(t, "dead-child", "parent_removed", time.Now().Add(-2*time.Hour))

	var list bytes.Buffer
	if err := runInbox(&list, []string{"dead-letter", "list", "--json"}); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(list.String(), `"reason":"parent_removed"`) || !strings.Contains(list.String(), `"child_session_id":"dead-child"`) {
		t.Fatalf("stable JSON omitted identity/reason: %s", list.String())
	}
	if strings.Contains(list.String(), "secret payload") {
		t.Fatalf("list exposed payload: %s", list.String())
	}

	records, err := session.ListDeadLetters()
	if err != nil || len(records) != 1 {
		t.Fatalf("records=%+v err=%v", records, err)
	}
	var show bytes.Buffer
	if err := runInbox(&show, []string{"dead-letter", "show", records[0].ID, "--json"}); err != nil {
		t.Fatalf("show: %v", err)
	}
	if !strings.Contains(show.String(), `"age_seconds":`) || !strings.Contains(show.String(), `"payload_summary":`) {
		t.Fatalf("show omitted bounded operator detail: %s", show.String())
	}
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

func TestIssue2062JSONDistinguishesPersistedReasons(t *testing.T) {
	cliInboxTestHome(t)
	reasons := []string{"child_removed", "parent_removed", "orphan", "unresolvable", "no_notify", "self_conductor"}
	for i, reason := range reasons {
		seedDeadLetterCLIRecord(t, "reason-child-"+reason, reason, time.Now().Add(time.Duration(-i)*time.Minute))
	}
	var out bytes.Buffer
	if err := runInbox(&out, []string{"dead-letter", "list", "--json"}); err != nil {
		t.Fatal(err)
	}
	for _, reason := range reasons {
		if !strings.Contains(out.String(), `"reason":"`+reason+`"`) {
			t.Errorf("JSON did not distinguish %q: %s", reason, out.String())
		}
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

func TestIssue2062HelpIsSideEffectFreeAndUnownedCanClear(t *testing.T) {
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
	var help bytes.Buffer
	if err := runInbox(&help, []string{"dead-letter", "help"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(help.String(), "show") || !strings.Contains(help.String(), "purge") {
		t.Fatalf("subcommand help is not specific: %q", help.String())
	}
	records, err := session.ListDeadLetters()
	if err != nil || len(records) != 1 || records[0].Store != "unowned" {
		t.Fatalf("unowned record missing after help: %+v err=%v", records, err)
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
