package session

import (
	"os"
	"os/exec"
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

	rec := CompletionRecord{
		ChildID: childID,
		Profile: profile,
		Title:   "worker",
		Status:  "ok",
		Summary: "feature shipped",
	}
	// Issue #1225: DeliverCompletion commits to the parent's durable outbox and
	// returns true when the record durably landed (safe to ack).
	if !n.DeliverCompletion(rec) {
		t.Fatalf("DeliverCompletion returned not-committed for a live parent")
	}

	inbox := readInboxLines(t, parentID)
	if len(inbox) != 1 {
		t.Fatalf("idle parent inbox has %d records, want exactly 1: %+v", len(inbox), inbox)
	}
	if inbox[0].TargetSessionID != parentID {
		t.Errorf("committed to %q, want parent %q", inbox[0].TargetSessionID, parentID)
	}
	if inbox[0].Kind != transitionKindFinished || inbox[0].DoneStatus != "ok" {
		t.Errorf("record missing done outcome: kind=%q status=%q", inbox[0].Kind, inbox[0].DoneStatus)
	}

	// A second identical commit must NOT add a duplicate pending record
	// (per-child last-wins keeps exactly one).
	if !n.DeliverCompletion(rec) {
		t.Fatalf("second DeliverCompletion returned not-committed")
	}
	if got := readInboxLines(t, parentID); len(got) != 1 {
		t.Fatalf("last-wins: inbox has %d records after second commit, want 1", len(got))
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
	t.Cleanup(d.notifier.Close)

	// Parent still down: replay must NOT commit (record stays unacked, nothing
	// resolves to a parent inbox).
	d.ReplayUnackedCompletions(profile)
	d.notifier.Flush()
	if got := readInboxLines(t, parentID); len(got) != 0 {
		t.Fatalf("parent down: inbox has %d records, want 0", len(got))
	}

	// Conductor restarts (parent row resolvable again).
	addParentRow(t, profile, parentID, childID)

	// First replay after restart commits exactly once to the parent inbox.
	d.ReplayUnackedCompletions(profile)
	d.notifier.Flush()
	if got := readInboxLines(t, parentID); len(got) != 1 {
		t.Fatalf("after restart: inbox has %d records, want exactly 1", len(got))
	}

	// Subsequent replays must NOT re-commit (record is acked); last-wins also
	// guarantees at most one pending record per child regardless.
	d.ReplayUnackedCompletions(profile)
	d.notifier.Flush()
	if got := readInboxLines(t, parentID); len(got) != 1 {
		t.Fatalf("no-double-wake: inbox has %d records after re-replay, want 1", len(got))
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
