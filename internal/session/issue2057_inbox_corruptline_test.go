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

// An oversized line is discarded through its terminating newline. The discard
// loop must survive bufio.ErrBufferFull, which ReadSlice returns for every
// buffer-sized chunk of the remainder. maxInboxLineBytes+1 is the one width
// that never fills the reader again, so it cannot cover this path alone.
func TestIssue2057_CommitToInboxSkipsMultiBufferOversizedLine(t *testing.T) {
	inboxTestHome(t)
	parent := "conductor-2057-oversized-multibuffer"
	oversized := strings.Repeat("x", maxInboxLineBytes+512*1024)
	writeRawInbox(t, parent, `{"child_session_id":"child-1","to_status":"waiting","last_output_hash":"turn-1"}
`+oversized+`
{"child_session_id":"child-3","to_status":"waiting","last_output_hash":"turn-3"}
`)

	err := CommitToInbox(parent, TransitionNotificationEvent{
		ChildSessionID: "child-2",
		ToStatus:       "waiting",
		LastOutputHash: "turn-2",
	})
	if err != nil {
		t.Fatalf("CommitToInbox must skip a multi-buffer oversized line: %v", err)
	}

	events, err := DrainInboxForParent(parent)
	if err != nil {
		t.Fatalf("drain after multi-buffer oversized line: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("expected 3 valid events around oversized line, got %d", len(events))
	}
}
