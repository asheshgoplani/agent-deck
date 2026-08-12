package session

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

func TestRecoverLifecycleIntentsFinalizesCommittedRemoval(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	storage, err := NewStorageWithProfile("_test_lifecycle_recovery")
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	inst := NewInstance("recover-remove", t.TempDir())
	inst.ID = "recover-remove"
	if err := storage.SaveWithGroups([]*Instance{inst}, NewGroupTree([]*Instance{inst})); err != nil {
		t.Fatal(err)
	}
	payload := LifecycleIntentPayload(inst, "", "")
	intent, err := PrepareLifecycleIntent(storage, inst.ID, LifecycleIntentRemove, payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.db.DeleteInstance(inst.ID); err != nil {
		t.Fatal(err)
	}
	if err := AdvanceLifecycleIntent(storage, intent, "row-deleted", payload); err != nil {
		t.Fatal(err)
	}
	if _, err := EnqueueRuntimeMessage(inst.ID, "orphaned queue"); err != nil {
		t.Fatal(err)
	}
	if err := RecoverLifecycleIntents(storage, nil); err != nil {
		t.Fatal(err)
	}
	if RuntimeQueueHasPending(inst.ID) {
		t.Fatal("startup recovery left committed removal queue")
	}
	intents, err := storage.db.LifecycleIntents()
	if err != nil || len(intents) != 0 {
		t.Fatalf("startup recovery left intents %#v, %v", intents, err)
	}
}

func TestRecoverLifecycleIntentsArchiveAndWorktreePhases(t *testing.T) {
	for _, tc := range []struct {
		name       string
		kind       string
		phase      string
		archived   bool
		wantRow    bool
		wantQueue  bool
		wantIntent bool
	}{
		{name: "archive committed", kind: LifecycleIntentArchive, phase: "archived", archived: true, wantRow: true},
		{name: "worktree prepared", kind: LifecycleIntentWorktreeFinish, phase: "prepared", wantRow: true, wantQueue: true, wantIntent: true},
		{name: "worktree merged restart", kind: LifecycleIntentWorktreeFinish, phase: "merged"},
		{name: "worktree removed", kind: LifecycleIntentWorktreeFinish, phase: "worktree-removed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
			storage, err := NewStorageWithProfile("_test_phase_" + tc.phase)
			if err != nil {
				t.Fatal(err)
			}
			defer storage.Close()
			inst := NewInstance(tc.name, t.TempDir())
			inst.ID = "phase-id"
			inst.WorktreePath = filepath.Join(root, "already-removed-worktree")
			inst.WorktreeRepoRoot = filepath.Join(root, "repo")
			if tc.archived {
				inst.ArchivedAt = time.Now().UTC()
			}
			if err := storage.InsertSessionAndVerify(inst, NewGroupTree([]*Instance{inst})); err != nil {
				t.Fatal(err)
			}
			loaded, _, _ := storage.LoadWithGroups()
			inst = loaded[0]
			payload := LifecycleIntentPayload(inst, inst.WorktreePath, "")
			intent, err := PrepareLifecycleIntent(storage, inst.ID, tc.kind, payload)
			if err != nil {
				t.Fatal(err)
			}
			if tc.phase != "prepared" {
				if err := AdvanceLifecycleIntent(storage, intent, tc.phase, payload); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := EnqueueRuntimeMessage(inst.ID, "phase queue"); err != nil {
				t.Fatal(err)
			}
			if err := RecoverLifecycleIntents(storage, loaded); err != nil {
				t.Fatal(err)
			}
			exists, err := storage.db.InstanceExists(inst.ID)
			if err != nil || exists != tc.wantRow {
				t.Fatalf("row exists = %v, %v; want %v", exists, err, tc.wantRow)
			}
			if pending := RuntimeQueueHasPending(inst.ID); pending != tc.wantQueue {
				t.Fatalf("queue pending = %v, want %v", pending, tc.wantQueue)
			}
			intents, _ := storage.db.LifecycleIntents()
			if got := len(intents) == 1; got != tc.wantIntent {
				t.Fatalf("intent retained = %v, want %v (%#v)", got, tc.wantIntent, intents)
			}
		})
	}
}

func TestRecoverLifecycleIntentsConcurrentCompletionIsIdempotent(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	first, err := NewStorageWithProfile("_test_concurrent_recovery")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := NewStorageWithProfile("_test_concurrent_recovery")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	inst := NewInstance("concurrent", t.TempDir())
	inst.ID = "concurrent-recovery"
	payload := LifecycleIntentPayload(inst, "", "")
	intent, err := PrepareLifecycleIntent(first, inst.ID, LifecycleIntentRemove, payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := AdvanceLifecycleIntent(first, intent, "row-deleted", payload); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, storage := range []*Storage{first, second} {
		wg.Add(1)
		go func(storage *Storage) {
			defer wg.Done()
			errs <- RecoverLifecycleIntents(storage, nil)
		}(storage)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	intents, err := first.db.LifecycleIntents()
	if err != nil || len(intents) != 0 {
		t.Fatalf("concurrent recovery did not finalize exactly once: %#v, %v", intents, err)
	}
}

func TestRecoverLifecycleIntentsRereadsGenerationAfterSnapshot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	storage, err := NewStorageWithProfile("_test_snapshot_reuse")
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	old := NewInstance("old", t.TempDir())
	old.ID = "snapshot-reuse"
	if err := storage.InsertSessionAndVerify(old, NewGroupTree([]*Instance{old})); err != nil {
		t.Fatal(err)
	}
	snapshot, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	payload := LifecycleIntentPayload(snapshot[0], "", "")
	intent, err := PrepareLifecycleIntent(storage, old.ID, LifecycleIntentRemove, payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.db.DeleteInstance(old.ID); err != nil {
		t.Fatal(err)
	}
	if err := AdvanceLifecycleIntent(storage, intent, "row-deleted", payload); err != nil {
		t.Fatal(err)
	}
	lifecycleBeforeRecoveryClaim = func(statedb.LifecycleIntent) {
		lifecycleBeforeRecoveryClaim = nil
		fresh := NewInstance("fresh", t.TempDir())
		fresh.ID = old.ID
		if err := storage.InsertSessionAndVerify(fresh, NewGroupTree([]*Instance{fresh})); err != nil {
			t.Fatal(err)
		}
		if _, err := EnqueueRuntimeMessage(fresh.ID, "fresh"); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { lifecycleBeforeRecoveryClaim = nil })
	if err := RecoverLifecycleIntents(storage, snapshot); err != nil {
		t.Fatal(err)
	}
	if exists, err := storage.InstanceExists(old.ID); err != nil || !exists {
		t.Fatalf("reused row=%v, %v", exists, err)
	}
	queued, err := PeekRuntimeQueue(old.ID)
	if err != nil || len(queued) != 1 || queued[0].Message != "fresh" {
		t.Fatalf("reused queue=%#v, %v", queued, err)
	}
}

func TestRecoverLifecycleIntentsLeavesPreparedRemovalUntouched(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	storage, err := NewStorageWithProfile("_test_prepared_removal")
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	inst := NewInstance("prepared-remove", t.TempDir())
	inst.ID = "prepared-remove"
	if err := storage.InsertSessionAndVerify(inst, NewGroupTree([]*Instance{inst})); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := storage.LoadWithGroups()
	if err != nil || len(loaded) != 1 {
		t.Fatalf("load = %#v, %v", loaded, err)
	}
	inst = loaded[0]
	payload := LifecycleIntentPayload(inst, "", "")
	if _, err := PrepareLifecycleIntent(storage, inst.ID, LifecycleIntentRemove, payload); err != nil {
		t.Fatal(err)
	}
	if _, err := EnqueueRuntimeMessage(inst.ID, "must survive"); err != nil {
		t.Fatal(err)
	}
	if err := RecoverLifecycleIntents(storage, loaded); err != nil {
		t.Fatal(err)
	}
	if !RuntimeQueueHasPending(inst.ID) {
		t.Fatal("prepared removal discarded queue")
	}
	if exists, err := storage.db.InstanceExists(inst.ID); err != nil || !exists {
		t.Fatalf("prepared removal changed row: %v, %v", exists, err)
	}
	intents, _ := storage.db.LifecycleIntents()
	if len(intents) != 1 || intents[0].Phase != "prepared" {
		t.Fatalf("prepared intent was finalized: %#v", intents)
	}
}

func TestRecoverLifecycleIntentsDoesNotTouchReusedIDGeneration(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	storage, err := NewStorageWithProfile("_test_reused_generation")
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	old := NewInstance("old", t.TempDir())
	old.ID = "reused-id"
	if err := storage.InsertSessionAndVerify(old, NewGroupTree([]*Instance{old})); err != nil {
		t.Fatal(err)
	}
	rows, _, _ := storage.LoadWithGroups()
	old = rows[0]
	payload := LifecycleIntentPayload(old, "", "")
	intent, err := PrepareLifecycleIntent(storage, old.ID, LifecycleIntentRemove, payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.db.DeleteInstance(old.ID); err != nil {
		t.Fatal(err)
	}
	if err := AdvanceLifecycleIntent(storage, intent, "row-deleted", payload); err != nil {
		t.Fatal(err)
	}
	fresh := NewInstance("fresh", t.TempDir())
	fresh.ID = old.ID
	if err := storage.InsertSessionAndVerify(fresh, NewGroupTree([]*Instance{fresh})); err != nil {
		t.Fatal(err)
	}
	rows, _, err = storage.LoadWithGroups()
	if err != nil || len(rows) != 1 || rows[0].PersistenceGeneration == old.PersistenceGeneration {
		t.Fatalf("fresh generation = %#v, %v", rows, err)
	}
	if _, err := EnqueueRuntimeMessage(fresh.ID, "fresh queue"); err != nil {
		t.Fatal(err)
	}
	if err := RecoverLifecycleIntents(storage, rows); err != nil {
		t.Fatal(err)
	}
	if exists, err := storage.db.InstanceExists(fresh.ID); err != nil || !exists {
		t.Fatalf("recovery deleted reused ID: %v, %v", exists, err)
	}
	queued, err := PeekRuntimeQueue(fresh.ID)
	if err != nil || len(queued) != 1 || queued[0].Message != "fresh queue" {
		t.Fatalf("recovery changed reused queue: %#v, %v", queued, err)
	}
}
