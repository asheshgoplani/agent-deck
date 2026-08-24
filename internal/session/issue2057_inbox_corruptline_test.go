package session

import (
	"strings"
	"testing"
)

func TestIssue2057_CommitToInboxSkipsCorruptLine(t *testing.T) {
	inboxTestHome(t)
	parent := "conductor-2057-corrupt"
	writeRawInbox(t, parent, `{"child_session_id":"child-1","to_status":"waiting","last_output_hash":"turn-1"}
not-json
`)

	err := CommitToInbox(parent, TransitionNotificationEvent{
		ChildSessionID: "child-2",
		ToStatus:       "waiting",
		LastOutputHash: "turn-2",
	})
	if err != nil {
		t.Fatalf("CommitToInbox must skip a corrupt line: %v", err)
	}

	events, err := DrainInboxForParent(parent)
	if err != nil {
		t.Fatalf("drain after corrupt-line commit: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 valid events around corrupt line, got %d", len(events))
	}
}

func TestIssue2057_CommitToInboxSkipsOversizedLine(t *testing.T) {
	inboxTestHome(t)
	parent := "conductor-2057-oversized"
	oversized := strings.Repeat("x", maxInboxLineBytes+1)
	writeRawInbox(t, parent, `{"child_session_id":"child-1","to_status":"waiting","last_output_hash":"turn-1"}
`+oversized+`
`)

	err := CommitToInbox(parent, TransitionNotificationEvent{
		ChildSessionID: "child-2",
		ToStatus:       "waiting",
		LastOutputHash: "turn-2",
	})
	if err != nil {
		t.Fatalf("CommitToInbox must skip an oversized line: %v", err)
	}

	events, err := DrainInboxForParent(parent)
	if err != nil {
		t.Fatalf("drain after oversized-line commit: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 valid events around oversized line, got %d", len(events))
	}
}
