package session

import (
	"path/filepath"
	"testing"
)

func TestPersistenceGenerationSurvivesReloadAndOrdinarySave(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	const profile = "_test_persistence_generation_reload"
	const id = "recreated-generation-roundtrip"

	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	original := &Instance{ID: id, Title: "first incarnation", ProjectPath: t.TempDir(), GroupPath: DefaultGroupPath, Tool: "shell", Command: "shell", Status: StatusStopped}
	if err := storage.InsertSessionAndVerify(original, NewGroupTree([]*Instance{original})); err != nil {
		t.Fatal(err)
	}
	if err := storage.DeleteInstance(id); err != nil {
		t.Fatal(err)
	}
	recreated := &Instance{ID: id, Title: "recreated incarnation", ProjectPath: original.ProjectPath, GroupPath: DefaultGroupPath, Tool: "shell", Command: "shell", Status: StatusStopped}
	if err := storage.InsertSessionAndVerify(recreated, NewGroupTree([]*Instance{recreated})); err != nil {
		t.Fatal(err)
	}
	if recreated.PersistenceGeneration == 0 {
		t.Fatal("explicit recreation did not establish a persistence generation")
	}
	if err := storage.Close(); err != nil {
		t.Fatal(err)
	}

	reloadedStorage, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	defer reloadedStorage.Close()
	instances, _, err := reloadedStorage.LoadWithGroups()
	if err != nil || len(instances) != 1 {
		t.Fatalf("reload = %#v, %v", instances, err)
	}
	if instances[0].PersistenceGeneration != recreated.PersistenceGeneration {
		t.Fatalf("generation after reload = %d, want %d", instances[0].PersistenceGeneration, recreated.PersistenceGeneration)
	}
	instances[0].Title = "ordinary save persisted"
	if err := reloadedStorage.SaveWithGroups(instances, NewGroupTree(instances)); err != nil {
		t.Fatal(err)
	}
	verify, _, err := reloadedStorage.LoadWithGroups()
	if err != nil || len(verify) != 1 || verify[0].Title != "ordinary save persisted" {
		t.Fatalf("ordinary save after reload was rejected: %#v, %v", verify, err)
	}
}
