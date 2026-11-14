package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
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
	if _, err := storage.GetDB().WriteLaunchDecision(inst.ID, inst.tmuxSession.TmuxName(), "pi", true); err != nil {
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

// Every successful restart records what it launched, not only the recreate
// path: the respawn-pane fast paths (Claude, Gemini, OpenCode, Codex, Cursor,
// a generic resume) return before its outcome write. A Claude session restarted
// as pi takes the recreate path; restarted as Claude again, with its pane alive
// and a conversation id, it takes the Claude respawn-pane path, which must
// record claude and clear the pi's socket decision.
func TestLaunchedTool_EveryRestartRecordsWhatItLaunched_ClaudePiClaude(t *testing.T) {
	skipIfNoTmuxBinary(t)
	piSocketShortData(t)
	fakePiVersion(t, "1.3.0")
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\nexec sleep 300\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	storage, err := NewStorageWithProfile("_test_launched_restart_" + randomString(8))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	prev := statedb.GetGlobal()
	statedb.SetGlobal(storage.GetDB())
	t.Cleanup(func() { statedb.SetGlobal(prev) })

	inst := NewInstanceWithTool("launched-restart", t.TempDir(), "claude")
	inst.Command = "claude"
	inst.IdentityInjectionDisabled = true
	inst.GroupPath = DefaultGroupPath
	instances := []*Instance{inst}
	if err := storage.SaveWithGroups(instances, NewGroupTree(instances)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sess := inst.GetTmuxSession(); sess != nil {
			_ = sess.Kill()
		}
		joinSpawnWatchers(t, inst)
	})
	want := func(step, tool string, exported bool) {
		t.Helper()
		if got := storedLaunchedTool(t, storage, inst.ID); got != tool {
			t.Errorf("%s: stored LaunchedTool = %q, want %q", step, got, tool)
		}
		if got := storedPiSocketExported(t, storage, inst.ID); got != exported {
			t.Errorf("%s: stored PiSocketExported = %v, want %v", step, got, exported)
		}
	}
	if err := inst.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	want("after the Claude start", "claude", false)

	inst.Tool, inst.Command = "pi", "pi"
	if err := inst.Restart(); err != nil {
		t.Fatalf("Restart as pi: %v", err)
	}
	if !inst.PiSocketExported {
		t.Fatal("test setup: the pi restart did not export the socket")
	}
	want("after the restart as pi", "pi", true)

	inst.Tool, inst.Command = "claude", "claude"
	inst.ClaudeSessionID = "4f2b9a3e-1c7d-4e8f-9a6b-2d3c4e5f6a7b"
	if sess := inst.GetTmuxSession(); sess == nil || !sess.Exists() {
		t.Fatal("test setup: the pane is gone, so the restart would not take the respawn-pane path")
	}
	if err := inst.Restart(); err != nil {
		t.Fatalf("Restart as Claude: %v", err)
	}
	want("after the respawn-pane restart as Claude", "claude", false)
}
