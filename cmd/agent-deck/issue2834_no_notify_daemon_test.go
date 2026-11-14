package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Issue #2834: with no notify-daemon running, nothing writes completion
// records, so `inbox drain` printed "No pending events." and the fleet line
// said "0 done" — indistinguishable from "nothing finished". Both now say
// plainly when no writer is running, and stay quiet when one is.

const issue2834Marker = "no notify-daemon running"

func registerIssue2834Fleet(t *testing.T, parentID string) {
	t.Helper()
	storage, err := session.NewStorageWithProfile("")
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	defer func() { _ = storage.Close() }()
	parent := session.NewInstance("parent-2834", t.TempDir())
	parent.ID = parentID
	child := session.NewInstance("child-2834", t.TempDir())
	child.ID = parentID + "-child"
	child.ParentSessionID = parentID
	instances := []*session.Instance{parent, child}
	if err := storage.SaveWithGroups(instances, session.NewGroupTreeWithGroups(instances, nil)); err != nil {
		t.Fatalf("save fleet: %v", err)
	}
}

func TestIssue2834_InboxDrainWarnsWhenNoNotifyDaemon(t *testing.T) {
	cliInboxTestHome(t)
	registerInboxDrainTarget(t, "parent-2834")

	var out bytes.Buffer
	if err := runInbox(&out, []string{"drain", "parent-2834"}); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if !strings.Contains(out.String(), issue2834Marker) {
		t.Fatalf("empty drain with no notify-daemon must say so, got %q", out.String())
	}
}

func TestIssue2834_InboxDrainJSONKeepsStdoutAndWarnsOnStderr(t *testing.T) {
	cliInboxTestHome(t)
	registerInboxDrainTarget(t, "parent-2834j")

	var out bytes.Buffer
	stderr := captureStderr(t, func() {
		if err := runInbox(&out, []string{"drain", "--json", "parent-2834j"}); err != nil {
			t.Errorf("drain --json: %v", err)
		}
	})
	if got := strings.TrimSpace(out.String()); got != "[]" {
		t.Fatalf("--json stdout must stay the bare array, got %q", got)
	}
	if !strings.Contains(stderr, issue2834Marker) {
		t.Fatalf("--json drain with no notify-daemon must warn on stderr, got %q", stderr)
	}
}

func TestIssue2834_InboxDrainQuietWhenNotifyDaemonRunning(t *testing.T) {
	cliInboxTestHome(t)
	registerInboxDrainTarget(t, "parent-2834r")
	session.WriteNotifyHeartbeat()

	var out bytes.Buffer
	if err := runInbox(&out, []string{"drain", "parent-2834r"}); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if strings.Contains(out.String(), issue2834Marker) {
		t.Fatalf("drain must not warn while a notify-daemon is running, got %q", out.String())
	}
}

func TestIssue2834_FleetLineSaysWhenNoNotifyDaemon(t *testing.T) {
	cliInboxTestHome(t)
	registerIssue2834Fleet(t, "parent-2834f")

	got := renderChildrenContextSummary("parent-2834f")
	first, _, _ := strings.Cut(got, "\n")
	if !strings.HasPrefix(first, "[agent-deck fleet] 1 children:") {
		t.Fatalf("fleet line missing, got %q", got)
	}
	if !strings.Contains(first, issue2834Marker) {
		t.Fatalf("fleet header must say no notify-daemon is running, got %q", first)
	}

	session.WriteNotifyHeartbeat()
	got = renderChildrenContextSummary("parent-2834f")
	if strings.Contains(got, issue2834Marker) {
		t.Fatalf("fleet line must not warn while a notify-daemon is running, got %q", got)
	}
}
