package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Recall phase 1: the Claude adoption arbitration is the writer of harness
// session links. A cold-start bind writes the authoritative row via
// WriteClaudeSessionBinding, a live hook confirming a minted id writes it via
// confirmClaudeSessionLink; a rejected candidate that was bound earlier has
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

// TestUpdateHookStatus_BothLinkWritersProduceARow covers the two Claude link
// call sites: an id agent-deck minted itself (assigned directly at launch,
// confirmed by the first hook in the equality branch) and an id the hook
// arbitration bound (bindClaudeSessionFromHook). Each yields exactly one
// authoritative session_links row, repeated hooks do not multiply it, and a
// rejected candidate is retracted without touching the confirmed link.
func TestUpdateHookStatus_BothLinkWritersProduceARow(t *testing.T) {
	const profile = "_test_recall_links_minted"
	_, storage := bootstrapDaemonProfile(t, profile)
	db := storage.GetDB()

	projDir := filepath.Join(os.Getenv("HOME"), "realproject")
	foreignTmp := filepath.Join(os.Getenv("HOME"), "fake-tmpdir", "T")
	for _, d := range []string{projDir, foreignTmp} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	const minted = "11111111-1111-4111-8111-111111111111"
	const hookBound = "22222222-2222-4222-8222-222222222222"
	const foreign = "33333333-3333-4333-8333-333333333333"

	// Call site 1: the minted-id path. buildCommand assigns the id directly
	// (instance.go --session-id), so the instance reaches its first hook
	// already bound and no bind/rebind ever fires.
	mintedInst := &Instance{
		ID: "inst-recall-minted", Title: "minted", ProjectPath: projDir, GroupPath: DefaultGroupPath,
		Tool: "claude", Status: StatusRunning, CreatedAt: time.Now(), ClaudeSessionID: minted,
	}
	// Call site 2: the hook-bind path (cold start, no id yet).
	boundInst := &Instance{
		ID: "inst-recall-bound", Title: "bound", ProjectPath: projDir, GroupPath: DefaultGroupPath,
		Tool: "claude", Status: StatusRunning, CreatedAt: time.Now(),
	}
	if err := storage.SaveWithGroups([]*Instance{mintedInst, boundInst}, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	if links, _ := db.ListSessionLinks(mintedInst.ID); len(links) != 0 {
		t.Fatalf("minted id must not be linked before a hook confirms it: %+v", links)
	}

	for _, ev := range []string{"SessionStart", "UserPromptSubmit", "PreToolUse"} {
		mintedInst.UpdateHookStatus(&HookStatus{Status: "running", SessionID: minted, Event: ev, UpdatedAt: time.Now(), Cwd: projDir})
		boundInst.UpdateHookStatus(&HookStatus{Status: "running", SessionID: hookBound, Event: ev, UpdatedAt: time.Now(), Cwd: projDir})
	}
	for _, tc := range []struct {
		inst *Instance
		want string
	}{{mintedInst, minted}, {boundInst, hookBound}} {
		if tc.inst.ClaudeSessionID != tc.want {
			t.Fatalf("%s: ClaudeSessionID = %q, want %q", tc.inst.ID, tc.inst.ClaudeSessionID, tc.want)
		}
		links, err := db.ListSessionLinks(tc.inst.ID)
		if err != nil {
			t.Fatalf("ListSessionLinks(%s): %v", tc.inst.ID, err)
		}
		if len(links) != 1 || links[0].Harness != "claude" || links[0].NativeID != tc.want || !links[0].Authoritative {
			t.Fatalf("%s: links = %+v; want one authoritative claude/%s row", tc.inst.ID, links, tc.want)
		}
	}

	// Adoption rejection: a foreign-cwd candidate on the minted instance is
	// retracted (no row) and the confirmed minted link stays authoritative.
	if err := db.UpsertSessionLink(mintedInst.ID, "claude", foreign, "", false); err != nil {
		t.Fatal(err)
	}
	mintedInst.UpdateHookStatus(&HookStatus{Status: "running", SessionID: foreign, Event: "PreToolUse", UpdatedAt: time.Now().Add(time.Second), Cwd: foreignTmp})
	if mintedInst.ClaudeSessionID != minted {
		t.Fatalf("foreign-cwd candidate rebound the minted instance to %q", mintedInst.ClaudeSessionID)
	}
	links, _ := db.ListSessionLinks(mintedInst.ID)
	if len(links) != 1 || links[0].NativeID != minted || !links[0].Authoritative {
		t.Fatalf("links after reject = %+v; want only authoritative %s (rejected %s retracted)", links, minted, foreign)
	}
}
