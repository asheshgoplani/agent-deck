package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Review P2-6 (status-light audit defect B): the hook-lag rule flips a
// session running→waiting at T while its hook file still says running; when
// a Stop hook for the SAME turn lands later (T+8min here) the daemon sees a
// fresh terminal hook candidate for a status it already reported. The
// transcript signal (#2057 dedup: append-only transcript size) identifies
// the turn, so the parent receives exactly one transition and exactly one
// [DONE] for it. Driven through TransitionDaemon.syncProfile on the status-DB
// (tuiAlive) path: the TUI's own status write at T is the flip, no tmux.
func TestAudit_B_HookLagFlipThenLateStopIsOneTransitionOneDone(t *testing.T) {
	inboxTestHome(t)
	profile := "_test-hook-lag-daemon"
	if err := os.MkdirAll(GetHooksDir(), 0o755); err != nil {
		t.Fatal(err)
	}

	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	defer storage.Close()

	now := time.Now()
	parentID := "parent-hook-lag"
	project := t.TempDir()
	child := &Instance{
		ID:              "child-hook-lag",
		Title:           "worker",
		ProjectPath:     project,
		GroupPath:       DefaultGroupPath,
		ParentSessionID: parentID,
		Tool:            "claude",
		Status:          StatusRunning,
		CreatedAt:       now,
		ClaudeSessionID: "session-hook-lag",
	}
	parent := &Instance{
		ID:          parentID,
		Title:       "orchestrator",
		ProjectPath: "/tmp/p",
		GroupPath:   DefaultGroupPath,
		Tool:        "claude",
		Status:      StatusRunning,
		CreatedAt:   now,
	}
	if err := storage.SaveWithGroups([]*Instance{child, parent}, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	// The child's transcript: the turn's records are all flushed by the time
	// the pane shows the summary line, so it does not change between the flip
	// and the late Stop.
	transcript := filepath.Join(GetClaudeConfigDir(), "projects", ConvertToClaudeDirName(project), child.ClaudeSessionID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(transcript), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transcript, []byte(`{"type":"assistant"}`+"\n"+`{"type":"system","subtype":"turn_duration"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	db := storage.GetDB()
	if err := db.RegisterInstance(false); err != nil {
		t.Fatal(err)
	}
	if err := db.Heartbeat(); err != nil {
		t.Fatal(err)
	}
	writeStatus := func(status string) {
		t.Helper()
		if err := db.WriteStatus(child.ID, status, "claude"); err != nil {
			t.Fatalf("write status %s: %v", status, err)
		}
	}
	writeStatus("running")
	if err := db.WriteStatus(parent.ID, "running", "claude"); err != nil {
		t.Fatal(err)
	}
	// The hook file for the turn: UserPromptSubmit, still "running".
	hookPath := filepath.Join(GetHooksDir(), child.ID+".json")
	writeHook := func(fields map[string]any) {
		t.Helper()
		b, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(hookPath, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeHook(map[string]any{"status": "running", "event": "UserPromptSubmit", "session_id": child.ClaudeSessionID, "ts": now.Add(-time.Minute).Unix()})

	d := NewTransitionDaemon()
	d.syncProfile(profile) // baseline: running observed

	// T: the hook-lag rule flips the light (the TUI writes its status row).
	writeStatus("waiting")
	d.syncProfile(profile)

	// T+8min: the Stop hook for the same turn lands, sentinel included. The
	// timestamp is placed eight minutes after the flip so the legacy 90s
	// same-transition window cannot be what suppresses the duplicate; only
	// the transcript identity can.
	writeHook(map[string]any{
		"status": "waiting", "event": "Stop", "session_id": child.ClaudeSessionID,
		"ts":          now.Add(8 * time.Minute).Unix(),
		"done_status": "ok", "done_summary": "turn finished",
	})
	d.syncProfile(profile)
	d.syncProfile(profile) // one more poll: nothing new may appear

	events, err := DrainInboxForParent(parentID)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	var transitions, dones int
	for _, ev := range events {
		if ev.ChildSessionID != child.ID {
			continue
		}
		switch {
		case ev.Kind == transitionKindFinished || ev.DoneStatus != "":
			dones++
			if ev.DoneStatus != "ok" {
				t.Errorf("done status = %q, want ok", ev.DoneStatus)
			}
		case ev.ToStatus == "waiting":
			transitions++
		}
	}
	if transitions != 1 || dones != 1 {
		t.Fatalf("parent inbox: %d transitions, %d [DONE] for one turn (want 1 and 1); events: %+v", transitions, dones, events)
	}
}
