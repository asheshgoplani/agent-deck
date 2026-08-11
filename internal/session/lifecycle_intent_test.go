package session

import (
	"path/filepath"
	"testing"
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
