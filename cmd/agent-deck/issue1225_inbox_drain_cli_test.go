package main

// Issue #1225 Step 2 (CLI surface) — `agent-deck inbox drain <self>` is the
// heartbeat consumption path: the conductor runs it as the first step of every
// heartbeat. It must collapse last-wins, dedup re-delivery via turn_fingerprint,
// and offer a --json shape for machine consumption.

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func cliInboxTestHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("AGENT_DECK_PROFILE", "")
	session.ResetInboxFingerprintCacheForTest()
}

func registerInboxDrainTarget(t *testing.T, id string) {
	t.Helper()
	storage, err := session.NewStorageWithProfile("")
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	inst := session.NewInstance("inbox-target", t.TempDir())
	inst.ID = id
	instances := []*session.Instance{inst}
	if err := storage.SaveWithGroups(instances, session.NewGroupTreeWithGroups(instances, nil)); err != nil {
		t.Fatalf("save inbox target: %v", err)
	}
}

// Case 2 (idle parent drains on heartbeat): a committed completion is surfaced
// by `inbox drain`, and a second drain finds nothing (exactly-once).
func TestIssue1225_InboxDrainCLI_HeartbeatDeliversThenEmpty(t *testing.T) {
	cliInboxTestHome(t)
	parent := "conductor-cli-1777000200"
	registerInboxDrainTarget(t, parent)
	if err := session.CommitToInbox(parent, session.TransitionNotificationEvent{
		ChildSessionID: "child-cli-1", ChildTitle: "fix-auth", Profile: "personal",
		FromStatus: "running", ToStatus: "waiting",
		LastOutputHash: "h1", Timestamp: time.Now(),
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	var buf bytes.Buffer
	if err := runInbox(&buf, []string{"drain", parent}); err != nil {
		t.Fatalf("inbox drain: %v", err)
	}
	if !strings.Contains(buf.String(), "child-cli-1") || !strings.Contains(buf.String(), "fix-auth") {
		t.Fatalf("drain output missing completion:\n%s", buf.String())
	}

	buf.Reset()
	if err := runInbox(&buf, []string{"drain", parent}); err != nil {
		t.Fatalf("second drain: %v", err)
	}
	if !strings.Contains(buf.String(), "No pending") {
		t.Fatalf("second drain should be empty, got:\n%s", buf.String())
	}
}

// --json gives the heartbeat/machine path a structured payload it can inject.
func TestIssue1225_InboxDrainCLI_JSONShape(t *testing.T) {
	cliInboxTestHome(t)
	parent := "conductor-cli-1777000210"
	registerInboxDrainTarget(t, parent)
	if err := session.CommitToInbox(parent, session.TransitionNotificationEvent{
		ChildSessionID: "child-cli-2", ChildTitle: "worker", Profile: "personal",
		FromStatus: "running", ToStatus: "waiting", LastOutputHash: "h2", Timestamp: time.Now(),
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	var buf bytes.Buffer
	if err := runInbox(&buf, []string{"drain", "--json", parent}); err != nil {
		t.Fatalf("inbox drain --json: %v", err)
	}
	var payload []session.TransitionNotificationEvent
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatalf("drain --json must emit a JSON array, got %q: %v", buf.String(), err)
	}
	if len(payload) != 1 || payload[0].ChildSessionID != "child-cli-2" {
		t.Fatalf("unexpected json payload: %+v", payload)
	}
}

// Boundary: draining an empty inbox is not an error.
func TestIssue1225_InboxDrainCLI_EmptyInbox(t *testing.T) {
	cliInboxTestHome(t)
	registerInboxDrainTarget(t, "conductor-cli-empty")
	var buf bytes.Buffer
	if err := runInbox(&buf, []string{"drain", "conductor-cli-empty"}); err != nil {
		t.Fatalf("drain empty: %v", err)
	}
	if !strings.Contains(buf.String(), "No pending") {
		t.Fatalf("empty drain should report no pending, got: %s", buf.String())
	}
}

// Back-compat: the legacy `inbox <id>` (no drain subcommand) still works.
func TestIssue1225_InboxCLI_LegacyFormStillWorks(t *testing.T) {
	cliInboxTestHome(t)
	parent := "conductor-cli-legacy"
	if err := session.CommitToInbox(parent, session.TransitionNotificationEvent{
		ChildSessionID: "child-legacy", ChildTitle: "w", Profile: "personal",
		FromStatus: "running", ToStatus: "waiting", Timestamp: time.Now(),
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	var buf bytes.Buffer
	if err := runInbox(&buf, []string{parent}); err != nil {
		t.Fatalf("legacy inbox: %v", err)
	}
	if !strings.Contains(buf.String(), "child-legacy") {
		t.Fatalf("legacy form should still drain, got: %s", buf.String())
	}
}
