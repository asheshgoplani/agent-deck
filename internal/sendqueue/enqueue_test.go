package sendqueue

import (
	"os"
	"sync"
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

// TestEnqueueOnce_QueuesASendWithAKeyOnce: the same key queues one record
// while that record is kept, even once it is final (#2537: a routed watcher
// event replayed or routed again is not delivered twice).
func TestEnqueueOnce_QueuesASendWithAKeyOnce(t *testing.T) {
	t.Setenv("AGENTDECK_EVENTS_BUS", "off")
	dir := t.TempDir()
	s := Send{SessionID: "s1", Message: "hello", Sender: "watcher:hook", Key: "watcher:w1:k1", WaitWhileStopped: true}
	first, created, err := EnqueueOnce("p", dir, s, time.Now())
	if err != nil || !created {
		t.Fatalf("first EnqueueOnce: created %v, %v", created, err)
	}
	again, created, err := EnqueueOnce("p", dir, s, time.Now())
	if err != nil || created || again.SendID != first.SendID {
		t.Fatalf("second EnqueueOnce: created %v id %s (first %s), %v", created, again.SendID, first.SendID, err)
	}
	if _, err := Update(dir, first.SendID, time.Now(), func(r *Record) { r.State = StateLanded }); err != nil {
		t.Fatal(err)
	}
	if after, created, err := EnqueueOnce("p", dir, s, time.Now()); err != nil || created || after.SendID != first.SendID {
		t.Fatalf("EnqueueOnce after landing: created %v, %v", created, err)
	}
	other, created, err := EnqueueOnce("p", dir, Send{SessionID: "s1", Message: "hello", Key: "watcher:w1:k2", WaitWhileStopped: true}, time.Now())
	if err != nil || !created || other.SendID == first.SendID {
		t.Fatalf("another key: created %v, %v", created, err)
	}
	if recs, _ := List(dir, ""); len(recs) != 2 {
		t.Fatalf("%d records, want 2", len(recs))
	}
}

// TestEnqueueOnce_WaitsForAStoppedTarget: a send that waits for its target
// is queued, not failed, while the target is not running.
func TestEnqueueOnce_WaitsForAStoppedTarget(t *testing.T) {
	t.Setenv("AGENTDECK_EVENTS_BUS", "off")
	dir := t.TempDir()
	rec, _, err := EnqueueOnce("p", dir, Send{SessionID: "s1", Message: "m", Key: "k", WaitWhileStopped: true}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir, rec.SendID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateQueued || !got.WaitWhileStopped || got.Key != "k" || got.Deadline != "" {
		t.Fatalf("record = %+v, want queued, waiting while stopped, keyed, no deadline", got)
	}
	if _, _, err := EnqueueOnce("p", dir, Send{SessionID: "s1", Message: "m"}, time.Now()); err == nil {
		t.Fatal("EnqueueOnce without a key succeeded")
	}
}

// TestEnqueueOnce_AKeyNamingNoRecordQueuesAgain: a crash between the key
// and the record leaves a key naming nothing; the next call queues the send.
func TestEnqueueOnce_AKeyNamingNoRecordQueuesAgain(t *testing.T) {
	t.Setenv("AGENTDECK_EVENTS_BUS", "off")
	dir := t.TempDir()
	if err := writeFileAtomic(KeyPath(dir, "k"), []byte("01ARZ3NDEKTSV4RRFFQ69G5FAV")); err != nil {
		t.Fatal(err)
	}
	rec, created, err := EnqueueOnce("p", dir, Send{SessionID: "s1", Message: "m", Key: "k", WaitWhileStopped: true}, time.Now())
	if err != nil || !created {
		t.Fatalf("created %v, %v", created, err)
	}
	if again, created, _ := EnqueueOnce("p", dir, Send{SessionID: "s1", Message: "m", Key: "k", WaitWhileStopped: true}, time.Now()); created || again.SendID != rec.SendID {
		t.Fatalf("the replaced key does not name the new record")
	}
}

// TestPrune_DropsTheKeyWithItsRecord: once a finished record is pruned its
// key no longer holds the send back.
func TestPrune_DropsTheKeyWithItsRecord(t *testing.T) {
	t.Setenv("AGENTDECK_EVENTS_BUS", "off")
	dir := t.TempDir()
	old := time.Now().Add(-2 * RetainFinished)
	rec, _, err := EnqueueOnce("p", dir, Send{SessionID: "s1", Message: "m", Key: "k", WaitWhileStopped: true}, old)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Update(dir, rec.SendID, old, func(r *Record) { r.State = StateLanded }); err != nil {
		t.Fatal(err)
	}
	Prune(dir, time.Now().Add(-RetainFinished))
	if _, err := Load(dir, rec.SendID); err != ErrUnknown {
		t.Fatalf("record after prune: %v", err)
	}
	if _, err := os.Stat(KeyPath(dir, "k")); !os.IsNotExist(err) {
		t.Fatalf("key after prune: %v", err)
	}
	if _, created, err := EnqueueOnce("p", dir, Send{SessionID: "s1", Message: "m", Key: "k", WaitWhileStopped: true}, time.Now()); err != nil || !created {
		t.Fatalf("EnqueueOnce after prune: created %v, %v", created, err)
	}
}

// TestEnqueueOnce_ConcurrentCallersQueueOneRecord: callers racing with one
// key (two processes, say) still queue a single record.
func TestEnqueueOnce_ConcurrentCallersQueueOneRecord(t *testing.T) {
	t.Setenv("AGENTDECK_EVENTS_BUS", "off")
	dir := t.TempDir()
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec, _, err := EnqueueOnce("p", dir, Send{SessionID: "s1", Message: "m", Key: "k", WaitWhileStopped: true}, time.Now())
			if err != nil {
				t.Error(err)
				return
			}
			ids <- rec.SendID
		}()
	}
	wg.Wait()
	close(ids)
	seen := map[string]bool{}
	for id := range ids {
		seen[id] = true
	}
	if recs, _ := List(dir, ""); len(seen) != 1 || len(recs) != 1 {
		t.Fatalf("%d distinct ids and %d records, want 1 and 1", len(seen), len(recs))
	}
}
