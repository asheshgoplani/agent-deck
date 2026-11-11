package session

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

// The launch decision (pi_socket_exported, launched_tool) lives in tool_data
// extras, which every binary's full save carries forward. A binary that does not
// record the decision (an older one, or a TUI left open across the upgrade) can
// relaunch the pi without the socket and leave the key behind; the decision is
// therefore trusted only while the row still names the tmux session it was
// recorded for. The seeds below are raw SQL, as such a binary leaves the row.

// seedLaunchDecision writes the launch decision into the row as a launch of
// this version records it, bound to tmuxSession.
func seedLaunchDecision(t *testing.T, storage *Storage, id, tool, tmuxSession string, exported bool) {
	t.Helper()
	flag := "json('false')"
	if exported {
		flag = "json('true')"
	}
	if _, err := storage.GetDB().DB().Exec(`UPDATE instances SET tool_data = json_set(COALESCE(tool_data, '{}'),
		'$.pi_socket_exported', `+flag+`, '$.launched_tool', ?, '$.launch_tmux_session', ?) WHERE id = ?`,
		tool, tmuxSession, id); err != nil {
		t.Fatal(err)
	}
}

// storedToolData is the row's raw tool_data.
func storedToolData(t *testing.T, storage *Storage, id string) map[string]any {
	t.Helper()
	var raw string
	if err := storage.GetDB().DB().QueryRow(`SELECT tool_data FROM instances WHERE id = ?`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	m := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// The maintainer's sequence: a launch of this version records the socket, an
// older binary restarts the pi (a new tmux session, the socket left off, no
// decision written) and its full save carries the key forward. The daemon then
// reads the pi as launched without the socket, and its doorbell is typed.
func TestPiLaunchDecision_AFlagRecordedForAnotherTmuxSessionIsNotTrusted(t *testing.T) {
	profile := uniqueProfile("_test_pi_bind_")
	w := newPiWakeFixtureIn(t, profile, replyWith("submitted"))
	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	launched := w.parent.tmuxSession.TmuxName()
	seedLaunchDecision(t, storage, w.parent.ID, "pi", launched, true)
	if got := storedPiSocketExported(t, storage, w.parent.ID); !got {
		t.Fatal("test setup: the decision recorded for the row's own tmux session is not read back")
	}

	// The older binary's restart: its outcome write names the new tmux session
	// and leaves tool_data alone; its full save carries the key forward.
	if _, err := storage.GetDB().WriteRestartOutcome(w.parent.ID, "agentdeck_pi-parent_0ldb1n", "waiting", "pi"); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.SaveWithGroups(loaded, NewGroupTree(loaded)); err != nil {
		t.Fatal(err)
	}
	if td := storedToolData(t, storage, w.parent.ID); td["pi_socket_exported"] != true {
		t.Fatalf("test setup: the full save did not carry the stale key forward: %v", td)
	}
	var parent *Instance
	for _, in := range mustLoad(t, storage) {
		if in.ID == w.parent.ID {
			parent = in
		}
	}
	if parent == nil {
		t.Fatal("parent missing from storage")
	}
	if parent.PiSocketExported {
		t.Error("a pi socket decision recorded for another tmux session is trusted: the doorbell is never typed")
	}
	if parent.LaunchedTool != "" {
		t.Errorf("LaunchedTool %q recorded for another tmux session is trusted, want empty (the stored tool)", parent.LaunchedTool)
	}

	// The daemon's view of the parent, as loaded: the socket is not there, so the
	// doorbell is typed.
	w.parent.PiSocketExported, w.parent.LaunchedTool = parent.PiSocketExported, parent.LaunchedTool
	piWakeSocketFor = func(string, string) string { return filepath.Join(filepath.Dir(w.ext.path), "none.sock") }
	if _, err := wakeSend(w.parent, profile, "agent-deck: 1 new (#r1 shipped)", "01JXBINDTEST0000000000000A", []string{"r1"}); err != nil {
		t.Fatalf("wakeSend: %v", err)
	}
	if typed := w.typed.waitTyped(t, 1); len(typed) != 1 {
		t.Errorf("typed %q: a pi an older binary relaunched without the socket was not woken", typed)
	}
}

// A process that launched the pi with the socket and saves in full afterwards
// must not write its view of the decision over a newer launch's (CodeRabbit,
// instance.go): the decision is only the launch's to write.
func TestPiLaunchDecision_AFullSaveNeverWritesItsViewOverANewerLaunch(t *testing.T) {
	storage, err := NewStorageWithProfile("_test_pi_bind_save_" + randomString(8))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	inst := NewInstanceWithTool("pi-bind-save", t.TempDir(), "pi")
	inst.GroupPath = DefaultGroupPath
	instances := []*Instance{inst}
	if err := storage.SaveWithGroups(instances, NewGroupTree(instances)); err != nil {
		t.Fatal(err)
	}
	name := inst.tmuxSession.TmuxName()
	// This process launches with the socket and records it.
	inst.PiSocketExported, inst.LaunchedTool = true, "pi"
	seedLaunchDecision(t, storage, inst.ID, "pi", name, true)
	// Another process relaunches it without the socket and records that.
	seedLaunchDecision(t, storage, inst.ID, "pi", name, false)
	// This process saves in full, with its view still true.
	if err := storage.SaveWithGroups(instances, NewGroupTree(instances)); err != nil {
		t.Fatal(err)
	}
	if td := storedToolData(t, storage, inst.ID); td["pi_socket_exported"] == true {
		t.Errorf("a full save wrote its stale pi_socket_exported over the newer launch's decision: %v", td)
	}
	if storedPiSocketExported(t, storage, inst.ID) {
		t.Error("the daemon reads the stale true: the relaunched pi's doorbell is never typed")
	}
}

func mustLoad(t *testing.T, storage *Storage) []*Instance {
	t.Helper()
	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	return instances
}
