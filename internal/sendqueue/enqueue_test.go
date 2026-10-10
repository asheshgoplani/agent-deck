package sendqueue

import (
	"testing"
	"time"
)

// TestEnqueue_WritesAQueuedRecord: the record Enqueue writes is the one
// `session send --queue` wrote before the lift (#2537).
func TestEnqueue_WritesAQueuedRecord(t *testing.T) {
	t.Setenv("AGENTDECK_EVENTS_BUS", "off")
	dir := t.TempDir()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	rec, err := Enqueue("p", dir, Send{
		SessionID: "s1", SessionTitle: "target", Tool: "claude", ClaudeSessionID: "c1", Running: true,
		Message: "hello", Images: []string{"/tmp/a.png"}, RequireInputPrompt: true,
		Sender: "cli", Deadline: now.Add(DefaultRetryBudget),
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir, rec.SendID)
	if err != nil {
		t.Fatal(err)
	}
	stamp := now.Format(time.RFC3339Nano)
	if got.State != StateQueued || got.Verdict != "queued" || got.Reason != "" || got.TargetStatus != "unknown" {
		t.Fatalf("state %q verdict %q reason %q target_status %q, want queued/queued/\"\"/unknown", got.State, got.Verdict, got.Reason, got.TargetStatus)
	}
	if got.SessionID != "s1" || got.SessionTitle != "target" || got.Tool != "claude" || got.ClaudeSessionID != "c1" {
		t.Fatalf("target fields = %+v", got)
	}
	if got.Message != "hello" || len(got.Images) != 1 || !got.RequireInputPrompt || got.Sender != "cli" {
		t.Fatalf("send fields = %+v", got)
	}
	if got.CreatedAt != stamp || got.UpdatedAt != stamp || got.Deadline != now.Add(DefaultRetryBudget).Format(time.RFC3339Nano) {
		t.Fatalf("created %q updated %q deadline %q", got.CreatedAt, got.UpdatedAt, got.Deadline)
	}
	if targets := PendingTargets(dir); len(targets) != 1 || targets[0] != "s1" {
		t.Fatalf("pending targets = %v, want [s1]", targets)
	}
}

// TestEnqueue_FailsAtOnceForATargetNotRunning: nothing waits for a target
// whose pane is gone; the record is final and no worker is owed.
func TestEnqueue_FailsAtOnceForATargetNotRunning(t *testing.T) {
	t.Setenv("AGENTDECK_EVENTS_BUS", "off")
	dir := t.TempDir()
	rec, err := Enqueue("p", dir, Send{SessionID: "s1", Message: "hello", Sender: "cli"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != StateFailed || rec.Reason != "target not running" || rec.Verdict != "unknown" || !rec.Final() {
		t.Fatalf("record = %+v, want failed: target not running", rec)
	}
	if rec.Deadline != "" {
		t.Fatalf("deadline = %q, want none for a zero Deadline", rec.Deadline)
	}
	if targets := PendingTargets(dir); len(targets) != 0 {
		t.Fatalf("pending targets = %v, want none", targets)
	}
}

// TestSpawnWorkerRejectsInvalidSessionID: a session id that is not a
// plain instance id never reaches the worker's argv.
func TestSpawnWorkerRejectsInvalidSessionID(t *testing.T) {
	for _, id := range []string{"", "-p", "--target=x", "../etc", "a b", "a;rm", "a\nb"} {
		if err := SpawnWorker("", id); err == nil {
			t.Errorf("SpawnWorker(%q) = nil, want error", id)
		}
	}
}
