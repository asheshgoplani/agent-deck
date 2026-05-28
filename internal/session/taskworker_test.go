package session

import (
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Issue #1214: kernel-exact task-worker completion. A worker dispatched to do a
// discrete task is run ONE-SHOT through a thin wrapper. When it EXITS, the
// kernel delivers that edge exactly once (cmd.Wait), the wrapper parses the last
// #1186 sentinel (last-wins), writes a durable completion record, and wakes the
// parent's live session exactly once. Restart-safe: an unacked record is
// replayed once on the next daemon pass and never double-fires.
//
// These tests pin the four spike-proven properties (exactly-once, last-wins,
// idle-wake, restart-safe) plus the daemon-staleness version guard. They are
// written test-first: every symbol below must compile-fail on pre-#1214 code.

// seedChildOnly persists ONLY the child (its parent row absent), modelling the
// "parent process down at completion" state for the restart-durability test.
func seedChildOnly(t *testing.T, profile, parentID string) (childID string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("AGENT_DECK_PROFILE", "")
	ClearUserConfigCache()
	t.Cleanup(func() { ClearUserConfigCache() })
	if err := os.MkdirAll(home+"/.agent-deck", 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	defer storage.Close()
	child := &Instance{
		ID:              "child-worker-1214",
		Title:           "worker",
		ProjectPath:     "/tmp/c1214",
		GroupPath:       DefaultGroupPath,
		ParentSessionID: parentID,
		Tool:            "claude",
		Status:          StatusWaiting,
		CreatedAt:       time.Now(),
	}
	if err := storage.SaveWithGroups([]*Instance{child}, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	return child.ID
}

// addParentRow saves the parent conductor alongside the existing child so the
// next delivery attempt resolves a live parent (the conductor "came back up").
func addParentRow(t *testing.T, profile, parentID, childID string) {
	t.Helper()
	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	defer storage.Close()
	parent := &Instance{
		ID:          parentID,
		Title:       "conductor-1214",
		ProjectPath: "/tmp/p1214",
		GroupPath:   DefaultGroupPath,
		Tool:        "claude",
		Status:      StatusIdle,
		CreatedAt:   time.Now(),
	}
	child := &Instance{
		ID:              childID,
		Title:           "worker",
		ProjectPath:     "/tmp/c1214",
		GroupPath:       DefaultGroupPath,
		ParentSessionID: parentID,
		Tool:            "claude",
		Status:          StatusWaiting,
		CreatedAt:       time.Now(),
	}
	if err := storage.SaveWithGroups([]*Instance{parent, child}, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
}

// --- Property 1: exactly-once active wake of an idle parent ------------------

func TestTaskWorker_DeliverCompletion_WakesIdleParentExactlyOnce(t *testing.T) {
	profile := "_test-1214-deliver-once"
	parentID, childID := seedDoneParentChild(t, profile)

	n := NewTransitionNotifier()
	t.Cleanup(n.Close)
	var sent atomic.Int32
	var gotTarget, gotMsg string
	n.sender = func(_ /*profile*/, targetID, message string) error {
		sent.Add(1)
		gotTarget, gotMsg = targetID, message
		return nil
	}

	rec := CompletionRecord{
		ChildID: childID,
		Profile: profile,
		Title:   "worker",
		Status:  "ok",
		Summary: "feature shipped",
	}
	if !n.DeliverCompletion(rec) {
		t.Fatalf("DeliverCompletion returned not-delivered for a live parent")
	}
	if got := sent.Load(); got != 1 {
		t.Fatalf("idle parent woken %d times, want exactly 1", got)
	}
	if gotTarget != parentID {
		t.Errorf("woke %q, want parent %q", gotTarget, parentID)
	}
	if !strings.Contains(gotMsg, "[DONE]") || !strings.Contains(gotMsg, "status=ok") {
		t.Errorf("wake message missing done outcome: %q", gotMsg)
	}
}

// --- Property 2: last-wins sentinel parse + exit-derived fallback -----------

func TestTaskWorker_DeriveCompletion_LastWinsAndExitFallback(t *testing.T) {
	out := "starting\n" +
		"===AGENTDECK_DONE=== status=fail summary=transient retry\n" +
		"retrying\n" +
		"===AGENTDECK_DONE=== status=ok summary=done at last\n"
	rec := deriveCompletion("c", "p", "worker", out, 0)
	if rec.Status != "ok" || rec.Summary != "done at last" {
		t.Fatalf("last-wins failed: status=%q summary=%q", rec.Status, rec.Summary)
	}

	// No sentinel + non-zero exit => fail (a worker that crashed without
	// asserting is a failed task, not a silent success).
	rec = deriveCompletion("c", "p", "worker", "boom\n", 2)
	if rec.Status != "fail" {
		t.Fatalf("non-zero exit without sentinel: status=%q, want fail", rec.Status)
	}
	if rec.ExitCode != 2 {
		t.Fatalf("exit code not captured: %d", rec.ExitCode)
	}

	// No sentinel + clean exit => ok (a one-shot worker that exits 0 finished).
	rec = deriveCompletion("c", "p", "worker", "all good\n", 0)
	if rec.Status != "ok" {
		t.Fatalf("clean exit without sentinel: status=%q, want ok", rec.Status)
	}
}

// --- Property 3: kernel exit captured exactly once via cmd.Wait -------------

func TestTaskWorker_RunTaskWorker_CapturesSentinelAndExit(t *testing.T) {
	profile := "_test-1214-run"
	_, childID := seedDoneParentChild(t, profile)

	cmd := exec.Command("sh", "-c",
		"echo working; echo '===AGENTDECK_DONE=== status=ok summary=built it'; exit 0")
	rec, err := RunTaskWorker(childID, profile, "worker", cmd)
	if err != nil {
		t.Fatalf("RunTaskWorker: %v", err)
	}
	if rec.Status != "ok" || rec.Summary != "built it" {
		t.Fatalf("captured completion wrong: %+v", rec)
	}
	if !CompletionRecordExists(profile, childID) {
		t.Fatalf("durable completion record not written for %s", childID)
	}
	recs, err := LoadCompletionRecords(profile)
	if err != nil {
		t.Fatalf("LoadCompletionRecords: %v", err)
	}
	if len(recs) != 1 || recs[0].ChildID != childID || recs[0].Acked {
		t.Fatalf("record not durable+unacked: %+v", recs)
	}
}

// --- Property 4: restart-safe replay — no miss, no double-wake --------------

func TestTaskWorker_ReplayUnacked_DeliversOncePerChildAcrossRestart(t *testing.T) {
	profile := "_test-1214-replay"
	parentID := "parent-conductor-1214"
	childID := seedChildOnly(t, profile, parentID)

	// Wrapper finished while the parent (conductor) was DOWN: durable record
	// written, but no live parent to wake yet.
	if err := WriteCompletionRecord(CompletionRecord{
		ChildID: childID, Profile: profile, Title: "worker",
		Status: "ok", Summary: "done while parent down", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("WriteCompletionRecord: %v", err)
	}

	d := NewTransitionDaemon()
	var sent atomic.Int32
	d.notifier.sender = func(_, _, _ string) error { sent.Add(1); return nil }
	t.Cleanup(d.notifier.Close)

	// Parent still down: replay must NOT deliver (record stays unacked).
	d.ReplayUnackedCompletions(profile)
	d.notifier.Flush()
	if got := sent.Load(); got != 0 {
		t.Fatalf("parent down: woke %d times, want 0", got)
	}

	// Conductor restarts (parent row resolvable again).
	addParentRow(t, profile, parentID, childID)

	// First replay after restart delivers exactly once.
	d.ReplayUnackedCompletions(profile)
	d.notifier.Flush()
	if got := sent.Load(); got != 1 {
		t.Fatalf("after restart: woke %d times, want exactly 1", got)
	}

	// Subsequent replays must NOT re-fire (record is acked).
	d.ReplayUnackedCompletions(profile)
	d.notifier.Flush()
	if got := sent.Load(); got != 1 {
		t.Fatalf("no-double-wake: woke %d times after re-replay, want 1", got)
	}
}

// --- STEP 1: daemon never goes stale — version recycle guard ----------------

func TestShouldRecycleForVersion(t *testing.T) {
	cases := []struct {
		running, onDisk string
		want            bool
	}{
		{"1.9.42", "1.9.43", true},  // upgraded on disk -> recycle
		{"1.9.42", "1.9.42", false}, // same -> keep running
		{"", "1.9.43", false},       // unknown running -> never recycle (avoid flap)
		{"1.9.42", "", false},       // unknown on-disk -> never recycle
		{" 1.9.42 ", "1.9.42", false},
	}
	for _, c := range cases {
		if got := ShouldRecycleForVersion(c.running, c.onDisk); got != c.want {
			t.Errorf("ShouldRecycleForVersion(%q,%q)=%v want %v", c.running, c.onDisk, got, c.want)
		}
	}
}
