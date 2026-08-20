package session

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/desktopnotify"
)

func readAuditRecords(t *testing.T, path string) []NotificationAuditRecord {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open audit log: %v", err)
	}
	defer f.Close()
	var out []NotificationAuditRecord
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var rec NotificationAuditRecord
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
			t.Fatalf("parse audit record %q: %v", scanner.Text(), err)
		}
		out = append(out, rec)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan audit log: %v", err)
	}
	return out
}

// A suppressed notification is the one an operator cannot see any other way:
// nothing reaches the screen and, before this audit, nothing reached a log
// either. Both outcomes must land in the same stream.
func TestSendDesktopNotificationAuditsSentAndSuppressed(t *testing.T) {
	auditPath := filepath.Join(t.TempDir(), "notification-audit.jsonl")
	t.Setenv(notificationAuditEnv, auditPath)
	setDesktopNotificationsEnabled(t, true)

	original := desktopNotificationSender
	desktopNotificationSender = func(desktopnotify.SourceEvent) error { return nil }
	t.Cleanup(func() { desktopNotificationSender = original })

	top := &Instance{ID: "top-1", Title: "top session"}
	child := &Instance{ID: "child-1", Title: "child session", ParentSessionID: "top-1"}

	if suppressed, err := sendDesktopNotification(top, desktopnotify.SourceEvent{
		SessionID: top.ID, Title: top.Title, ToStatus: "waiting",
	}); suppressed || err != nil {
		t.Fatalf("top-level send: suppressed=%v err=%v", suppressed, err)
	}
	if suppressed, err := sendDesktopNotification(child, desktopnotify.SourceEvent{
		SessionID: child.ID, Title: child.Title, ToStatus: "waiting",
	}); !suppressed || err != nil {
		t.Fatalf("child send: suppressed=%v err=%v", suppressed, err)
	}

	records := readAuditRecords(t, auditPath)
	if len(records) != 2 {
		t.Fatalf("expected 2 audit records, got %d: %+v", len(records), records)
	}
	if records[0].Decision != "sent" || records[0].IID != "top-1" || records[0].Class != string(desktopnotify.Attention) {
		t.Fatalf("unexpected top-level record: %+v", records[0])
	}
	if records[1].Decision != "suppressed" || records[1].Reason != "parented_child" || records[1].Parent != "top-1" {
		t.Fatalf("unexpected child record: %+v", records[1])
	}
	for _, rec := range records {
		if rec.Src != "deck-desktop" || rec.TS == "" || rec.Epoch <= 0 {
			t.Fatalf("record missing audit envelope: %+v", rec)
		}
	}
}

// An event the transport cannot classify shows nothing to the human; the audit
// has to say so rather than implying a banner appeared.
func TestSendDesktopNotificationAuditsUnclassifiedEvent(t *testing.T) {
	auditPath := filepath.Join(t.TempDir(), "notification-audit.jsonl")
	t.Setenv(notificationAuditEnv, auditPath)
	setDesktopNotificationsEnabled(t, true)

	original := desktopNotificationSender
	desktopNotificationSender = func(desktopnotify.SourceEvent) error { return nil }
	t.Cleanup(func() { desktopNotificationSender = original })

	inst := &Instance{ID: "top-2", Title: "top session"}
	if _, err := sendDesktopNotification(inst, desktopnotify.SourceEvent{
		SessionID: inst.ID, Title: inst.Title, ToStatus: "running",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}

	records := readAuditRecords(t, auditPath)
	if len(records) != 1 || records[0].Class != "unclassified" {
		t.Fatalf("expected one unclassified record, got %+v", records)
	}
}
