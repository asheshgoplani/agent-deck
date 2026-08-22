package main

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestIssue1877_AutoParentIdentityMustResolve(t *testing.T) {
	t.Setenv("AGENT_DECK_SESSION_ID", "")
	t.Setenv("AGENTDECK_INSTANCE_ID", "stale-parent-1877")
	parent, unresolved := resolveAutoParentInstanceChecked(nil)
	if parent != nil || unresolved != "stale-parent-1877" {
		t.Fatalf("stale injected identity must fail loudly: parent=%v unresolved=%q", parent, unresolved)
	}
}

func TestIssue1877_InboxDrainReportsDeadLettersAndIsNonZero(t *testing.T) {
	cliInboxTestHome(t)
	registerInboxDrainTarget(t, "parent-1877")
	event := session.TransitionNotificationEvent{
		ChildSessionID: "dead-child-1877", Profile: "default",
		FromStatus: "running", ToStatus: "error", Timestamp: time.Now(),
		DeadLetterReason: "unresolvable", Attempts: 5,
	}
	raw := []byte(`{"child_session_id":"dead-child-1877","profile":"default","from_status":"running","to_status":"error","timestamp":"` + event.Timestamp.Format(time.RFC3339Nano) + `","attempts":5,"dead_letter_reason":"unresolvable"}` + "\n")
	if err := os.MkdirAll(session.DeadLetterDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(session.DeadLetterPathFor(event.ChildSessionID), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err := runInbox(&out, []string{"drain", "parent-1877"})
	var pending *deadLettersPendingError
	if !errors.As(err, &pending) || inboxExitCode(err) == 0 {
		t.Fatalf("dead letters must make drain non-clean: err=%v", err)
	}
	if !strings.Contains(out.String(), "1 dead-lettered event") {
		t.Fatalf("drain must report non-zero dead-letter count, got %q", out.String())
	}
}

func TestIssue1877_CorruptNonEmptyDeadLetterIsNotReportedClean(t *testing.T) {
	cliInboxTestHome(t)
	registerInboxDrainTarget(t, "parent-corrupt-1877")
	if err := os.MkdirAll(session.DeadLetterDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(session.DeadLetterPathFor("truncated-1877"), []byte(`{"child_session_id":"truncated`), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err := runInbox(&out, []string{"drain", "parent-corrupt-1877"})
	var pending *deadLettersPendingError
	if !errors.As(err, &pending) || pending.count != 1 || inboxExitCode(err) != 4 {
		t.Fatalf("non-empty corrupt dead-letter must be loud: output=%q err=%v", out.String(), err)
	}
}

func TestIssue2007_InboxDrainCountsUnownedDiscoveryEntries(t *testing.T) {
	cliInboxTestHome(t)
	registerInboxDrainTarget(t, "parent-2007")
	event := session.TransitionNotificationEvent{
		ChildSessionID: "unowned-child-2007", Profile: "default",
		FromStatus: "running", ToStatus: "error", Timestamp: time.Now(),
	}
	if err := session.WriteInboxEvent(session.UnownedInboxID, event); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err := runInbox(&out, []string{"drain", "parent-2007"})
	var pending *deadLettersPendingError
	if !errors.As(err, &pending) || pending.count != 1 || inboxExitCode(err) != 4 {
		t.Fatalf("unowned discovery must keep drain non-clean: output=%q err=%v", out.String(), err)
	}
	if !strings.Contains(out.String(), "1 dead-lettered event") {
		t.Fatalf("unowned count missing from drain warning: %q", out.String())
	}
}
