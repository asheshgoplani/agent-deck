package session

import (
	"os"
	"testing"
)

// TestAutoNameDescription_SurvivesSaveLoad confirms the auto_name_description
// column round-trips through SaveWithGroups/LoadWithGroups. If this regresses,
// an archived auto-named session would reload wearing its bare random handle
// instead of the captured Claude task description (the user-reported "loses the
// auto generated name").
func TestAutoNameDescription_SurvivesSaveLoad(t *testing.T) {
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", t.TempDir())
	ClearUserConfigCache()
	t.Cleanup(func() {
		os.Setenv("HOME", origHome)
		ClearUserConfigCache()
	})

	storage, err := NewStorageWithProfile("_autoname_roundtrip")
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	t.Cleanup(func() { storage.Close() })

	const desc = "Refactoring the auth module"
	inst := NewInstanceWithTool("q-7f3a", "/tmp/q", "claude")
	inst.AutoName = true
	inst.SetAutoNameDescription(desc)

	insts := []*Instance{inst}
	tree := NewGroupTree(insts)
	if err := storage.SaveWithGroups(insts, tree); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}

	loaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups: %v", err)
	}
	var got *Instance
	for _, in := range loaded {
		if in.ID == inst.ID {
			got = in
		}
	}
	if got == nil {
		t.Fatal("instance missing after reload")
	}
	if !got.AutoName {
		t.Fatal("AutoName flag did not survive save/load")
	}
	if got.GetAutoNameDescription() != desc {
		t.Fatalf("auto_name_description did not survive save/load: got %q want %q",
			got.GetAutoNameDescription(), desc)
	}
}
