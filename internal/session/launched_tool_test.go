package session

import (
	"testing"
)

// The launched tool is what the pane runs. A tool edit is FieldRestartRequired:
// it changes the stored tool at once, but the pane keeps the old harness until
// the next restart. So launched_tool lives in the tool_data extras zone and only
// the launch's targeted writes set it; a full save, from this process or from
// one that never launched the session, carries it forward.
func TestLaunchedTool_AFullSaveKeepsItAndOnlyALaunchWriteChangesIt(t *testing.T) {
	storage, err := NewStorageWithProfile("_test_launched_tool_" + randomString(8))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	inst := NewInstanceWithTool("launched", t.TempDir(), "pi")
	inst.GroupPath = DefaultGroupPath
	save := func() {
		t.Helper()
		instances := []*Instance{inst}
		if err := storage.SaveWithGroups(instances, NewGroupTree(instances)); err != nil {
			t.Fatal(err)
		}
	}
	save()
	if got := storedLaunchedTool(t, storage, inst.ID); got != "" {
		t.Fatalf("a row no launch wrote holds LaunchedTool = %q, want empty", got)
	}
	if err := storage.GetDB().WriteLaunchDecision(inst.ID, "pi", true); err != nil {
		t.Fatal(err)
	}
	if got := storedLaunchedTool(t, storage, inst.ID); got != "pi" {
		t.Fatalf("after the launch write, LaunchedTool = %q, want \"pi\"", got)
	}

	// An edit to claude, saved in full by a process that loaded the row (so the
	// registry merge sees its view of every field) and holds no launched tool in
	// memory: only a write of the field could clear the stored one.
	loaded, _, err := storage.LoadWithGroups()
	if err != nil || len(loaded) != 1 {
		t.Fatalf("reload: %v (%d rows)", err, len(loaded))
	}
	inst = loaded[0]
	if inst.LaunchedTool != "pi" || !inst.PiSocketExported {
		t.Fatalf("test setup: loaded LaunchedTool %q, PiSocketExported %v", inst.LaunchedTool, inst.PiSocketExported)
	}
	inst.Tool, inst.LaunchedTool = "claude", ""
	save()
	if got := storedLaunchedTool(t, storage, inst.ID); got != "pi" {
		t.Errorf("a full save after a tool edit left LaunchedTool = %q, want \"pi\": the pane still runs pi until a restart", got)
	}
	if !storedPiSocketExported(t, storage, inst.ID) {
		t.Fatal("test setup: the edit was saved with PiSocketExported false")
	}

	// The restart launches claude: its outcome write records it, and the socket
	// decision with it.
	if _, err := storage.GetDB().WriteRestartOutcomeWithLaunch(inst.ID, "agentdeck_launched_tool", "running", "claude", false); err != nil {
		t.Fatal(err)
	}
	if got := storedLaunchedTool(t, storage, inst.ID); got != "claude" {
		t.Errorf("after the restart outcome, LaunchedTool = %q, want \"claude\"", got)
	}
	if storedPiSocketExported(t, storage, inst.ID) {
		t.Error("after a claude restart, PiSocketExported is still set")
	}
}
