package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Recall phase 1: the Claude adoption arbitration is the only writer of
// harness session links. A cold-start bind writes the authoritative row via
// WriteClaudeSessionBinding; a rejected candidate that was bound earlier has
// its row retracted, so the recall index can never bind that transcript to
// this instance again.
func TestUpdateHookStatus_SessionLinkWrittenOnBindRetractedOnReject(t *testing.T) {
	const profile = "_test_recall_links"
	_, storage := bootstrapDaemonProfile(t, profile)
	db := storage.GetDB()

	projDir := filepath.Join(os.Getenv("HOME"), "realproject")
	foreignTmp := filepath.Join(os.Getenv("HOME"), "fake-tmpdir", "T")
	for _, d := range []string{projDir, foreignTmp} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	const sessA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	inst := &Instance{
		ID:          "inst-recall-links",
		Title:       "links",
		ProjectPath: projDir,
		GroupPath:   DefaultGroupPath,
		Tool:        "claude",
		Status:      StatusRunning,
		CreatedAt:   time.Now(),
	}
	if err := storage.SaveWithGroups([]*Instance{inst}, nil); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Cold start: the first candidate binds and its link is authoritative.
	inst.UpdateHookStatus(&HookStatus{Status: "running", SessionID: sessA, Event: "PreToolUse", UpdatedAt: time.Now(), Cwd: projDir})
	if inst.ClaudeSessionID != sessA {
		t.Fatalf("cold start did not bind %s (got %q)", sessA, inst.ClaudeSessionID)
	}
	links, err := db.ListSessionLinks(inst.ID)
	if err != nil {
		t.Fatalf("ListSessionLinks: %v", err)
	}
	if len(links) != 1 || links[0].Harness != "claude" || links[0].NativeID != sessA || !links[0].Authoritative {
		t.Fatalf("links after bind = %+v; want one authoritative claude/%s row", links, sessA)
	}

	// Simulate an earlier binding history: sessA was superseded by sessB, so
	// sessA is now a non-authoritative link. Then sessA comes back from a
	// foreign cwd and is rejected: its row must be retracted, sessB's kept.
	const sessB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	if err := db.WriteClaudeSessionBinding(inst.ID, sessB, time.Now()); err != nil {
		t.Fatal(err)
	}
	inst.ClaudeSessionID = sessB
	inst.UpdateHookStatus(&HookStatus{Status: "running", SessionID: sessA, Event: "PreToolUse", UpdatedAt: time.Now().Add(time.Second), Cwd: foreignTmp})
	if inst.ClaudeSessionID != sessB {
		t.Fatalf("foreign-cwd candidate rebound the instance to %q", inst.ClaudeSessionID)
	}
	links, _ = db.ListSessionLinks(inst.ID)
	if len(links) != 1 || links[0].NativeID != sessB || !links[0].Authoritative {
		t.Fatalf("links after reject = %+v; want only authoritative %s (rejected %s retracted)", links, sessB, sessA)
	}
}
