package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestInbox_WriteAppendsJSONL verifies that WriteInboxEvent appends a single
// JSONL line per call and that the file lives under
// ~/.agent-deck/inboxes/<parent>.jsonl. Append-only is the load-bearing
// invariant — the consumer reads-then-truncates atomically, so writers must
// never overwrite or seek-past existing data.
func TestInbox_WriteAppendsJSONL(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("AGENT_DECK_PROFILE", "")
	ClearUserConfigCache()
	t.Cleanup(func() { ClearUserConfigCache() })

	parent := "parent-inbox-write"
	ev1 := TransitionNotificationEvent{
		ChildSessionID:  "child-a",
		ChildTitle:      "worker",
		Profile:         "_test",
		FromStatus:      "running",
		ToStatus:        "waiting",
		Timestamp:       time.Now(),
		TargetSessionID: parent,
		TargetKind:      "parent",
	}
	ev2 := ev1
	ev2.ChildSessionID = "child-b"

	if err := WriteInboxEvent(parent, ev1); err != nil {
		t.Fatalf("WriteInboxEvent ev1: %v", err)
	}
	if err := WriteInboxEvent(parent, ev2); err != nil {
		t.Fatalf("WriteInboxEvent ev2: %v", err)
	}

	path := InboxPathFor(parent)
	if !strings.HasSuffix(path, filepath.Join("inboxes", parent+".jsonl")) {
		t.Fatalf("inbox path malformed: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read inbox: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 JSONL lines, got %d (data=%q)", len(lines), data)
	}
	if !strings.Contains(lines[0], "child-a") || !strings.Contains(lines[1], "child-b") {
		t.Fatalf("inbox preserves write order: %q", data)
	}
}

// TestInbox_ReadAndTruncateReturnsThenEmpties is the consumer contract used
// by `agent-deck inbox <session>`: read all pending events, then truncate so
// the next call returns an empty slice. A consumer crash between read and
// truncate would cause re-delivery, which is acceptable; lost events are
// not. (We accept dup-on-crash, refuse loss-on-crash.)
func TestInbox_ReadAndTruncateReturnsThenEmpties(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("AGENT_DECK_PROFILE", "")
	ClearUserConfigCache()
	t.Cleanup(func() { ClearUserConfigCache() })

	parent := "parent-inbox-read"
	for i := 0; i < 3; i++ {
		ev := TransitionNotificationEvent{
			ChildSessionID:  "child-r",
			ChildTitle:      "worker",
			Profile:         "_test",
			FromStatus:      "running",
			ToStatus:        "waiting",
			Timestamp:       time.Now(),
			TargetSessionID: parent,
			TargetKind:      "parent",
		}
		if err := WriteInboxEvent(parent, ev); err != nil {
			t.Fatalf("WriteInboxEvent: %v", err)
		}
	}

	got, err := ReadAndTruncateInbox(parent)
	if err != nil {
		t.Fatalf("ReadAndTruncateInbox: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 events, got %d", len(got))
	}

	again, err := ReadAndTruncateInbox(parent)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("expected empty after truncate, got %d", len(again))
	}
}

// TestInbox_ReadMissingFileReturnsEmpty makes sure the consumer command
// is safe to run on a session that has never received a deferred event.
// An ENOENT on the inbox file is normal, not an error.
func TestInbox_ReadMissingFileReturnsEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("AGENT_DECK_PROFILE", "")
	ClearUserConfigCache()
	t.Cleanup(func() { ClearUserConfigCache() })

	got, err := ReadAndTruncateInbox("never-written")
	if err != nil {
		t.Fatalf("missing file should not error, got %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty slice, got %d", len(got))
	}
}
