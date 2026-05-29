package session

// Audit B11 — a corrupt line in the PRIMARY inbox must not fail the whole drain.
// Both the legacy raw path (ReadAndTruncateInbox) and the #1225 drain
// (DrainInboxForParent) skip the bad line and deliver every valid record around
// it. (The misleading TOCTOU comment on ReadAndTruncateInbox is also corrected
// in inbox.go.)

import (
	"os"
	"path/filepath"
	"testing"
)

func writeRawInbox(t *testing.T, parent, content string) {
	t.Helper()
	path := InboxPathFor(parent)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write inbox: %v", err)
	}
}

func TestB11_ReadAndTruncateInbox_SkipsCorruptLine(t *testing.T) {
	inboxTestHome(t)
	parent := "conductor-b11a-1777700000"

	writeRawInbox(t, parent, `{"child_session_id":"c1","to_status":"waiting","fp":"1"}
not-json-at-all }{
{"child_session_id":"c2","to_status":"error","fp":"2"}
`)

	events, err := ReadAndTruncateInbox(parent)
	if err != nil {
		t.Fatalf("corrupt line must not fail the read: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 valid events (corrupt skipped), got %d: %+v", len(events), events)
	}
	// Truncated.
	if _, err := os.Stat(InboxPathFor(parent)); !os.IsNotExist(err) {
		t.Fatalf("inbox should be removed after read (err=%v)", err)
	}
}

func TestB11_DrainInboxForParent_SkipsCorruptLine(t *testing.T) {
	inboxTestHome(t)
	parent := "conductor-b11b-1777700100"

	writeRawInbox(t, parent, `{"child_session_id":"c1","to_status":"waiting","turn_fingerprint":"c1@aaa"}
{garbage
{"child_session_id":"c2","to_status":"error","turn_fingerprint":"c2@bbb"}
`)

	events, err := DrainInboxForParent(parent)
	if err != nil {
		t.Fatalf("corrupt line must not fail the drain: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 valid deliverables (corrupt skipped), got %d: %+v", len(events), events)
	}
}
