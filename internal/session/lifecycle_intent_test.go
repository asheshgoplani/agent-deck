package session

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

func TestRecoverLifecycleIntentsFinalizesCommittedRemoval(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	storage, err := NewStorageWithProfile("_test_lifecycle_recovery")
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	inst := NewInstance("recover-remove", t.TempDir())
	inst.ID = "recover-remove"
	if err := inst.prepareRepositorySessionTemp(); err != nil {
		t.Fatal(err)
	}
	tempRoot := inst.repositorySessionTempDir()
	if err := storage.SaveWithGroups([]*Instance{inst}, NewGroupTree([]*Instance{inst})); err != nil {
		t.Fatal(err)
	}
	payload := LifecycleIntentPayload(inst, "", "")
	intent, err := PrepareLifecycleIntent(storage, inst.ID, LifecycleIntentRemove, payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.db.DeleteInstance(inst.ID, intent.Token); err != nil {
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
	if _, err := os.Lstat(tempRoot); !os.IsNotExist(err) {
		t.Fatalf("startup recovery left repository session temp: %s", tempRoot)
	}
	intents, err := storage.db.LifecycleIntents()
	if err != nil || len(intents) != 0 {
		t.Fatalf("startup recovery left intents %#v, %v", intents, err)
	}
}

func TestCompleteRemovalLifecycleCleansRepositorySessionTemp(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	inst := NewInstance("complete-remove-temp", project)
	inst.ID = "complete-remove-temp"
	if err := inst.prepareRepositorySessionTemp(); err != nil {
		t.Fatal(err)
	}
	tempRoot := inst.repositorySessionTempDir()
	if err := os.WriteFile(filepath.Join(tempRoot, "owned.log"), []byte("owned"), 0o600); err != nil {
		t.Fatal(err)
	}

	storage, err := NewStorageWithProfile("_test_complete_remove_temp")
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	payload := LifecycleIntentPayload(inst, "", "")
	intent, err := PrepareLifecycleIntent(storage, inst.ID, LifecycleIntentRemove, payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := CompleteLifecycleIntent(storage, intent); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(tempRoot); !os.IsNotExist(err) {
		t.Fatalf("repository session temp survived completed removal lifecycle: %s", tempRoot)
	}
}

func TestCompleteLifecycleIntentStaleTokenDoesNotCleanNewerTemp(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	inst := NewInstance("stale-complete", project)
	inst.ID = "stale-complete"
	if err := inst.prepareRepositorySessionTemp(); err != nil {
		t.Fatal(err)
	}
	tempRoot := inst.repositorySessionTempDir()
	storage, err := NewStorageWithProfile("_test_stale_complete_temp")
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	payload := LifecycleIntentPayload(inst, "", "")
	stale, err := PrepareLifecycleIntent(storage, inst.ID, LifecycleIntentRemove, payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.db.CompleteLifecycleIntent(stale.InstanceID, stale.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareLifecycleIntent(storage, inst.ID, LifecycleIntentRemove, payload); err != nil {
		t.Fatal(err)
	}
	if err := CompleteLifecycleIntent(storage, stale); !errors.Is(err, statedb.ErrLifecycleIntentOwnership) {
		t.Fatalf("stale completion error=%v, want ownership error", err)
	}
	if _, err := os.Lstat(tempRoot); err != nil {
		t.Fatalf("stale completion removed newer temp root: %v", err)
	}
}

func TestCompleteLifecycleIntentRejectsReusedIDBeforeTempCleanup(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	old := NewInstance("old", project)
	old.ID = "reused-complete"
	storage, err := NewStorageWithProfile("_test_reused_complete_temp")
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	if err := storage.InsertSessionAndVerify(old, NewGroupTree([]*Instance{old})); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	old = loaded[0]
	stale, err := PrepareLifecycleIntent(storage, old.ID, LifecycleIntentRemove, LifecycleIntentPayload(old, "", ""))
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.db.DeleteInstance(old.ID, stale.Token); err != nil {
		t.Fatal(err)
	}
	fresh := NewInstance("fresh", project)
	fresh.ID = old.ID
	if err := fresh.prepareRepositorySessionTemp(); err != nil {
		t.Fatal(err)
	}
	tempRoot := fresh.repositorySessionTempDir()
	if err := storage.InsertSessionAndVerify(fresh, NewGroupTree([]*Instance{fresh})); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareLifecycleIntent(storage, fresh.ID, LifecycleIntentRemove, LifecycleIntentPayload(fresh, "", "")); err != nil {
		t.Fatal(err)
	}
	if err := CompleteLifecycleIntent(storage, stale); !errors.Is(err, statedb.ErrLifecycleIntentOwnership) {
		t.Fatalf("reused ID completion error=%v, want ownership error", err)
	}
	if _, err := os.Lstat(tempRoot); err != nil {
		t.Fatalf("stale generation removed fresh temp root: %v", err)
	}
}

func TestCompleteLifecycleIntentRejectsMismatchedPayloadBeforeTempCleanup(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	target := NewInstance("target", project)
	target.ID = "completion-target"
	other := NewInstance("other", project)
	other.ID = "completion-other"
	if err := other.prepareRepositorySessionTemp(); err != nil {
		t.Fatal(err)
	}
	tempRoot := other.repositorySessionTempDir()
	storage, err := NewStorageWithProfile("_test_mismatched_complete_temp")
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	intent, err := PrepareLifecycleIntent(storage, target.ID, LifecycleIntentRemove, LifecycleIntentPayload(target, "", ""))
	if err != nil {
		t.Fatal(err)
	}
	if err := AdvanceLifecycleIntent(storage, intent, "row-deleted", LifecycleIntentPayload(other, "", "")); err != nil {
		t.Fatal(err)
	}
	if err := CompleteLifecycleIntent(storage, intent); err == nil {
		t.Fatal("mismatched payload completion succeeded")
	}
	if _, err := os.Lstat(tempRoot); err != nil {
		t.Fatalf("mismatched payload removed other temp root: %v", err)
	}
}

func TestRecoverLifecycleIntentsCleansWorktreeFinishTemp(t *testing.T) {
	for _, phase := range []string{"merged", "worktree-removed"} {
		t.Run(phase, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
			project := filepath.Join(root, "project")
			if err := os.MkdirAll(project, 0o755); err != nil {
				t.Fatal(err)
			}
			storage, err := NewStorageWithProfile("_test_worktree_finish_temp_" + phase)
			if err != nil {
				t.Fatal(err)
			}
			defer storage.Close()
			inst := NewInstance("worktree-temp", project)
			inst.ID = "worktree-temp-" + phase
			inst.WorktreePath = filepath.Join(root, "worktree")
			inst.WorktreeRepoRoot = filepath.Join(root, "repo")
			if err := inst.prepareRepositorySessionTemp(); err != nil {
				t.Fatal(err)
			}
			tempRoot := inst.repositorySessionTempDir()
			if err := storage.InsertSessionAndVerify(inst, NewGroupTree([]*Instance{inst})); err != nil {
				t.Fatal(err)
			}
			loaded, _, err := storage.LoadWithGroups()
			if err != nil {
				t.Fatal(err)
			}
			inst = loaded[0]
			payload := LifecycleIntentPayload(inst, inst.WorktreePath, "")
			intent, err := PrepareLifecycleIntent(storage, inst.ID, LifecycleIntentWorktreeFinish, payload)
			if err != nil {
				t.Fatal(err)
			}
			if err := AdvanceLifecycleIntent(storage, intent, phase, payload); err != nil {
				t.Fatal(err)
			}
			if phase == "merged" {
				original := lifecycleRemoveWorktree
				lifecycleRemoveWorktree = func(*Instance) (bool, error) { return true, nil }
				t.Cleanup(func() { lifecycleRemoveWorktree = original })
			}
			if err := RecoverLifecycleIntents(storage, loaded); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(tempRoot); !os.IsNotExist(err) {
				t.Fatalf("recovered %s worktree finish left session temp: %v", phase, err)
			}
		})
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
			inst.WorktreePath = filepath.Join(root, "worktree")
			inst.WorktreeRepoRoot = filepath.Join(root, "repo")
			var removals atomic.Int32
			if tc.phase == "merged" {
				if err := os.MkdirAll(inst.WorktreePath, 0o755); err != nil {
					t.Fatal(err)
				}
				original := lifecycleRemoveWorktree
				lifecycleRemoveWorktree = func(target *Instance) (bool, error) {
					removals.Add(1)
					return true, os.RemoveAll(target.WorktreePath)
				}
				t.Cleanup(func() { lifecycleRemoveWorktree = original })
			}
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
			if tc.phase == "merged" {
				if removals.Load() != 1 {
					t.Fatalf("worktree removals=%d", removals.Load())
				}
				if _, err := os.Stat(inst.WorktreePath); !os.IsNotExist(err) {
					t.Fatalf("worktree survived: %v", err)
				}
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
	if err := first.InsertSessionAndVerify(inst, NewGroupTree([]*Instance{inst})); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := first.LoadWithGroups()
	if err != nil || len(loaded) != 1 {
		t.Fatalf("load=%#v, %v", loaded, err)
	}
	inst = loaded[0]
	payload := LifecycleIntentPayload(inst, "", "")
	intent, err := PrepareLifecycleIntent(first, inst.ID, LifecycleIntentRemove, payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.db.DeleteInstance(inst.ID, intent.Token); err != nil {
		t.Fatal(err)
	}
	var cleanupCount atomic.Int32
	lifecycleRecoveryMutation = func(name string) {
		if name == "queue-discard" {
			cleanupCount.Add(1)
		}
	}
	t.Cleanup(func() { lifecycleRecoveryMutation = nil })
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
	if cleanupCount.Load() != 1 {
		t.Fatalf("destructive cleanup count=%d", cleanupCount.Load())
	}
}

func TestRecoverLifecycleIntentsRenewsBlockedClaimAndCleansExactlyOnce(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	first, err := NewStorageWithProfile("_test_blocked_claim_renewal")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := NewStorageWithProfile("_test_blocked_claim_renewal")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	inst := NewInstance("blocked", t.TempDir())
	inst.ID = "blocked-renewal"
	if err := first.InsertSessionAndVerify(inst, NewGroupTree([]*Instance{inst})); err != nil {
		t.Fatal(err)
	}
	loaded, _, _ := first.LoadWithGroups()
	payload := LifecycleIntentPayload(loaded[0], "", "")
	intent, err := PrepareLifecycleIntent(first, inst.ID, LifecycleIntentRemove, payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.db.DeleteInstance(inst.ID, intent.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := EnqueueRuntimeMessage(inst.ID, "cleanup"); err != nil {
		t.Fatal(err)
	}
	oldLease, oldInterval := statedb.LifecycleRecoveryClaimLease, lifecycleClaimRenewInterval
	statedb.LifecycleRecoveryClaimLease, lifecycleClaimRenewInterval = time.Second, 100*time.Millisecond
	t.Cleanup(func() { statedb.LifecycleRecoveryClaimLease, lifecycleClaimRenewInterval = oldLease, oldInterval })
	entered, release := make(chan struct{}), make(chan struct{})
	lifecycleAfterGenerationRead = func(statedb.LifecycleIntent) {
		lifecycleAfterGenerationRead = nil
		close(entered)
		<-release
	}
	t.Cleanup(func() { lifecycleAfterGenerationRead = nil })
	var cleanups atomic.Int32
	lifecycleRecoveryMutation = func(name string) {
		if name == "queue-discard" {
			cleanups.Add(1)
		}
	}
	t.Cleanup(func() { lifecycleRecoveryMutation = nil })
	firstDone := make(chan error, 1)
	go func() { firstDone <- RecoverLifecycleIntents(first, nil) }()
	<-entered
	claimedBefore, err := first.db.LifecycleIntents()
	if err != nil || len(claimedBefore) != 1 || claimedBefore[0].RecoveryOwner == "" {
		t.Fatalf("initial claim=%#v, %v", claimedBefore, err)
	}
	time.Sleep(2200 * time.Millisecond)
	claimedAfter, err := first.db.LifecycleIntents()
	if err != nil || len(claimedAfter) != 1 || claimedAfter[0].RecoveryOwner != claimedBefore[0].RecoveryOwner || claimedAfter[0].RecoveryClaimedAt <= claimedBefore[0].RecoveryClaimedAt {
		t.Fatalf("blocked claim was not renewed: before=%#v after=%#v err=%v", claimedBefore, claimedAfter, err)
	}
	if err := RecoverLifecycleIntents(second, nil); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if cleanups.Load() != 1 {
		t.Fatalf("cleanup count=%d", cleanups.Load())
	}
	intents, _ := first.db.LifecycleIntents()
	if len(intents) != 0 || RuntimeQueueHasPending(inst.ID) {
		t.Fatalf("recovery incomplete intents=%#v", intents)
	}
}

func TestRecoverLifecycleIntentsLostRenewalCancelsBeforeMutation(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	storage, err := NewStorageWithProfile("_test_lost_renewal")
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	inst := NewInstance("lost", t.TempDir())
	inst.ID = "lost-renewal"
	if err := storage.InsertSessionAndVerify(inst, NewGroupTree([]*Instance{inst})); err != nil {
		t.Fatal(err)
	}
	loaded, _, _ := storage.LoadWithGroups()
	payload := LifecycleIntentPayload(loaded[0], "", "")
	intent, err := PrepareLifecycleIntent(storage, inst.ID, LifecycleIntentRemove, payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.db.DeleteInstance(inst.ID, intent.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := EnqueueRuntimeMessage(inst.ID, "preserve"); err != nil {
		t.Fatal(err)
	}
	oldInterval := lifecycleClaimRenewInterval
	lifecycleClaimRenewInterval = 50 * time.Millisecond
	t.Cleanup(func() { lifecycleClaimRenewInterval = oldInterval })
	entered, release := make(chan struct{}), make(chan struct{})
	lifecycleAfterGenerationRead = func(statedb.LifecycleIntent) { lifecycleAfterGenerationRead = nil; close(entered); <-release }
	t.Cleanup(func() { lifecycleAfterGenerationRead = nil })
	var mutations atomic.Int32
	lifecycleRecoveryMutation = func(string) { mutations.Add(1) }
	t.Cleanup(func() { lifecycleRecoveryMutation = nil })
	done := make(chan error, 1)
	go func() { done <- RecoverLifecycleIntents(storage, nil) }()
	<-entered
	if _, err := storage.db.DB().Exec("UPDATE lifecycle_intents SET recovery_owner='stolen' WHERE instance_id=?", inst.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	close(release)
	if err := <-done; err == nil {
		t.Fatal("lost ownership was not surfaced")
	}
	if mutations.Load() != 0 || !RuntimeQueueHasPending(inst.ID) {
		t.Fatalf("mutation=%d queue=%v", mutations.Load(), RuntimeQueueHasPending(inst.ID))
	}
}

func TestRecoverLifecycleIntentsClaimedDeleteRejectsTakeoverAtBoundary(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	storage, err := NewStorageWithProfile("_test_claimed_delete_takeover")
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	inst := NewInstance("takeover", t.TempDir())
	inst.ID = "claimed-delete-takeover"
	if err := storage.InsertSessionAndVerify(inst, NewGroupTree([]*Instance{inst})); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	inst = loaded[0]
	payload := LifecycleIntentPayload(inst, inst.WorktreePath, "")
	intent, err := PrepareLifecycleIntent(storage, inst.ID, LifecycleIntentWorktreeFinish, payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := AdvanceLifecycleIntent(storage, intent, "worktree-removed", payload); err != nil {
		t.Fatal(err)
	}
	if _, err := EnqueueRuntimeMessage(inst.ID, "preserve"); err != nil {
		t.Fatal(err)
	}
	var takeover atomic.Bool
	lifecycleRecoveryMutation = func(name string) {
		if name != "claimed-delete" || !takeover.CompareAndSwap(false, true) {
			return
		}
		if _, err := storage.db.DB().Exec("UPDATE lifecycle_intents SET recovery_owner='new-owner' WHERE instance_id=?", inst.ID); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { lifecycleRecoveryMutation = nil })
	err = RecoverLifecycleIntents(storage, loaded)
	if err == nil || !errors.Is(err, statedb.ErrLifecycleIntentOwnership) {
		t.Fatalf("takeover error=%v", err)
	}
	if exists, err := storage.InstanceExists(inst.ID); err != nil || !exists {
		t.Fatalf("claimed delete changed row: %v, %v", exists, err)
	}
	if !RuntimeQueueHasPending(inst.ID) {
		t.Fatal("claimed delete takeover discarded queue")
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
	if err := storage.db.DeleteInstance(old.ID, intent.Token); err != nil {
		t.Fatal(err)
	}
	if err := AdvanceLifecycleIntent(storage, intent, "row-deleted", payload); err != nil {
		t.Fatal(err)
	}
	lifecycleAfterGenerationRead = func(statedb.LifecycleIntent) {
		lifecycleAfterGenerationRead = nil
		fresh := NewInstance("fresh", t.TempDir())
		fresh.ID = old.ID
		if err := storage.InsertSessionAndVerify(fresh, NewGroupTree([]*Instance{fresh})); err != nil {
			t.Fatal(err)
		}
		if _, err := EnqueueRuntimeMessage(fresh.ID, "fresh"); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { lifecycleAfterGenerationRead = nil })
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

func TestRecoverLifecycleIntentsStaleGenerationSurfacesLiveOwnershipLoss(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	storage, err := NewStorageWithProfile("_test_stale_generation_owner_loss")
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	inst := NewInstance("stale-owner", t.TempDir())
	inst.ID = "stale-owner-loss"
	if err := storage.InsertSessionAndVerify(inst, NewGroupTree([]*Instance{inst})); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	inst = loaded[0]
	payload := LifecycleIntentPayload(inst, "", "")
	intent, err := PrepareLifecycleIntent(storage, inst.ID, LifecycleIntentRemove, payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := AdvanceLifecycleIntent(storage, intent, "row-deleted", payload); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.db.DB().Exec("UPDATE instance_tombstones SET generation=generation+1 WHERE id=?", inst.ID); err != nil {
		t.Fatal(err)
	}
	fresh, _, err := storage.LoadWithGroups()
	if err != nil || fresh[0].PersistenceGeneration == intent.Generation {
		t.Fatalf("fresh=%#v, %v", fresh, err)
	}
	lifecycleAfterGenerationRead = func(statedb.LifecycleIntent) {
		lifecycleAfterGenerationRead = nil
		if _, err := storage.db.DB().Exec("UPDATE lifecycle_intents SET recovery_owner='stolen-owner' WHERE instance_id=?", inst.ID); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { lifecycleAfterGenerationRead = nil })
	originalExists := lifecycleInstanceExists
	lifecycleInstanceExists = func(*Instance) bool { return true }
	t.Cleanup(func() { lifecycleInstanceExists = originalExists })
	err = RecoverLifecycleIntents(storage, fresh)
	if err == nil || !errors.Is(err, statedb.ErrLifecycleIntentOwnership) {
		t.Fatalf("live ownership loss error=%v", err)
	}
	intents, _ := storage.db.LifecycleIntents()
	if len(intents) != 1 || intents[0].Token != intent.Token {
		t.Fatalf("active stale intent disappeared: %#v", intents)
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
	if err := storage.db.DeleteInstance(old.ID, intent.Token); err != nil {
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
