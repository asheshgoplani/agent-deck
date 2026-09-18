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

// Messaging audit P1-4 (#2101): `inbox drain` exited 4 whenever ANY dead
// letter existed anywhere, with no ack path, so a conductor heartbeat that
// checks the exit code looped on it forever. Exit 4 is now opt-in
// (--strict); the default prints per-store counts and exits 0.
func seedOneDeadLetterAndOneUnowned(t *testing.T) {
	t.Helper()
	raw := []byte(`{"child_session_id":"dead-child-2101","profile":"default","from_status":"running","to_status":"error","timestamp":"` + time.Now().Format(time.RFC3339Nano) + `","attempts":5,"dead_letter_reason":"unresolvable"}` + "\n")
	if err := os.MkdirAll(session.DeadLetterDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(session.DeadLetterPathFor("dead-child-2101"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := session.WriteInboxEvent(session.UnownedInboxID, session.TransitionNotificationEvent{
		ChildSessionID: "unowned-child-2101", Profile: "default",
		FromStatus: "running", ToStatus: "error", Timestamp: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestIssue2101_InboxDrainDefaultExitsZeroWithPerStoreCounts(t *testing.T) {
	cliInboxTestHome(t)
	registerInboxDrainTarget(t, "parent-2101")
	seedOneDeadLetterAndOneUnowned(t)

	var out bytes.Buffer
	if err := runInbox(&out, []string{"drain", "parent-2101"}); err != nil {
		t.Fatalf("drain without --strict must exit 0: err=%v (exit %d)", err, inboxExitCode(err))
	}
	text := out.String()
	if !strings.Contains(text, "dead-letter: 1") || !strings.Contains(text, "_unowned: 1") {
		t.Fatalf("drain must print per-store counts, got %q", text)
	}
}

func TestIssue2101_InboxDrainStrictKeepsExit4(t *testing.T) {
	cliInboxTestHome(t)
	registerInboxDrainTarget(t, "parent-2101s")
	seedOneDeadLetterAndOneUnowned(t)

	var out bytes.Buffer
	err := runInbox(&out, []string{"drain", "--strict", "parent-2101s"})
	var pending *deadLettersPendingError
	if !errors.As(err, &pending) || pending.count != 2 || inboxExitCode(err) != 4 {
		t.Fatalf("--strict must keep the exit-4 contract: err=%v", err)
	}
}

func TestIssue2101_InboxDrainJSONReportsCountsAndExitsZero(t *testing.T) {
	cliInboxTestHome(t)
	registerInboxDrainTarget(t, "parent-2101j")
	seedOneDeadLetterAndOneUnowned(t)

	var out bytes.Buffer
	if err := runInbox(&out, []string{"drain", "--json", "parent-2101j"}); err != nil {
		t.Fatalf("--json without --strict must exit 0: %v", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(out.String()), "[") {
		t.Fatalf("--json output must stay a JSON array on stdout, got %q", out.String())
	}
}
