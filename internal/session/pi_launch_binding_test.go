package session

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
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

// Forge (p5): a start whose tmux name is taken spawns under a new one
// (tmux.Session.Start), and not every start is followed by a save. The launch write records
// the name it spawned under with the decision, so the daemon, which loads the
// row, still trusts it.
func TestPiLaunchDecision_AStartUnderATakenTmuxNameRecordsTheNameItSpawnedUnder(t *testing.T) {
	storage, inst := piStartFixture(t, "1.3.0", false)
	taken := inst.tmuxSession.TmuxName()
	blocker := tmux.NewSession("pi-start-blocker", t.TempDir())
	blocker.Name, blocker.SocketName = taken, inst.tmuxSession.SocketName
	if err := blocker.Start("sleep 300"); err != nil {
		t.Fatalf("test setup: the blocker did not start: %v", err)
	}
	t.Cleanup(func() { _ = blocker.Kill() })
	if err := inst.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		if sess := inst.GetTmuxSession(); sess != nil {
			_ = sess.Kill()
		}
		joinSpawnWatchers(t, inst)
	})
	spawned := inst.tmuxSession.TmuxName()
	if spawned == taken || !inst.PiSocketExported {
		t.Fatalf("test setup: the start spawned under %q (taken %q) with PiSocketExported %v", spawned, taken, inst.PiSocketExported)
	}
	var row *Instance
	for _, in := range mustLoad(t, storage) {
		if in.ID == inst.ID {
			row = in
		}
	}
	if row == nil {
		t.Fatal("instance missing from storage")
	}
	if got := row.tmuxSession.TmuxName(); got != spawned {
		t.Errorf("storage names tmux session %q, want %q, the one the start spawned under", got, spawned)
	}
	if !row.PiSocketExported || row.LaunchedTool != "pi" {
		t.Errorf("the daemon reads PiSocketExported %v, LaunchedTool %q after a start under a taken name, want true and \"pi\"", row.PiSocketExported, row.LaunchedTool)
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

// CodeRabbit (write_restart_outcome.go:170): the launch write changes
// tmux_session, so it moves last_modified like the restart outcome does, or an
// open TUI never reloads the name the start spawned under; and the launching
// process can tell the bump is its own (LaunchRecordStamps), so its own save
// does not read it as another process's change.
func TestPiLaunchDecision_TheLaunchWriteIsAChangeOtherProcessesSeeAndThisOneOwns(t *testing.T) {
	storage, inst := piStartFixture(t, "1.3.0", false)
	before, err := storage.GetDB().LastModified()
	if err != nil {
		t.Fatal(err)
	}
	taken := inst.tmuxSession.TmuxName()
	blocker := tmux.NewSession("pi-stamp-blocker", t.TempDir())
	blocker.Name, blocker.SocketName = taken, inst.tmuxSession.SocketName
	if err := blocker.Start("sleep 300"); err != nil {
		t.Fatalf("test setup: the blocker did not start: %v", err)
	}
	t.Cleanup(func() { _ = blocker.Kill() })
	if err := inst.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		if sess := inst.GetTmuxSession(); sess != nil {
			_ = sess.Kill()
		}
		joinSpawnWatchers(t, inst)
	})
	if inst.tmuxSession.TmuxName() == taken {
		t.Fatal("test setup: the start reused the taken name")
	}
	after, err := storage.GetDB().LastModified()
	if err != nil {
		t.Fatal(err)
	}
	if after == before {
		t.Fatal("the launch wrote a new tmux_session without moving last_modified: an open TUI keeps the taken name")
	}
	if stamps := inst.LaunchRecordStamps(); !stamps.SoleWriterSince(before, after) {
		t.Errorf("the launch write is not recognisable as this process's own: stamps %+v, before %d, current %d", stamps, before, after)
	}
}

// CodeRabbit (instance.go:1796): a launch whose targeted write fails is not
// handed to a later full save. That save holds this process's view, and by then
// another launch may have recorded its own decision under the same tmux name
// (a respawn keeps it), which the snapshot merge cannot tell from the value
// this process loaded. Only a launch that found no row is left to the first
// save.
func TestPiLaunchDecision_AFailedLaunchWriteIsNotCarriedByALaterFullSave(t *testing.T) {
	storage, err := NewStorageWithProfile("_test_pi_bind_fail_" + randomString(8))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	prev := statedb.GetGlobal()
	statedb.SetGlobal(storage.GetDB())
	t.Cleanup(func() { statedb.SetGlobal(prev) })
	seed := NewInstanceWithTool("pi-bind-fail", t.TempDir(), "pi")
	seed.GroupPath = DefaultGroupPath
	if err := storage.SaveWithGroups([]*Instance{seed}, NewGroupTree([]*Instance{seed})); err != nil {
		t.Fatal(err)
	}
	name := seed.tmuxSession.TmuxName()
	// An earlier launch without the socket, as its launch write records it.
	if _, err := storage.GetDB().WriteLaunchDecision(seed.ID, name, "pi", false); err != nil {
		t.Fatal(err)
	}
	var inst *Instance
	loaded := mustLoad(t, storage)
	for _, in := range loaded {
		if in.ID == seed.ID {
			inst = in
		}
	}
	if inst == nil {
		t.Fatal("instance missing from storage")
	}

	// This process launches with the socket and its write fails.
	db := storage.GetDB().DB()
	if _, err := db.Exec(`CREATE TRIGGER fail_launch_write BEFORE UPDATE OF tool_data ON instances
		WHEN json_extract(NEW.tool_data, '$.pi_socket_exported') = 1 BEGIN SELECT RAISE(ABORT, 'injected'); END`); err != nil {
		t.Fatal(err)
	}
	inst.PiSocketExported = true
	inst.persistLaunchDecision()
	if _, err := db.Exec(`DROP TRIGGER fail_launch_write`); err != nil {
		t.Fatal(err)
	}
	if storedPiSocketExported(t, storage, inst.ID) {
		t.Fatal("test setup: the injected failure did not stop the launch write")
	}
	// Another process respawns the pane under the same name without the socket
	// and records that: the row reads as it did when this process loaded it.
	if _, err := storage.GetDB().WriteLaunchDecision(inst.ID, name, "pi", false); err != nil {
		t.Fatal(err)
	}
	// This process saves in full.
	if err := storage.SaveWithGroups(loaded, NewGroupTree(loaded)); err != nil {
		t.Fatal(err)
	}
	if storedPiSocketExported(t, storage, inst.ID) {
		t.Error("a full save wrote a failed launch's pi_socket_exported over the newer launch's decision: the pane without the socket is never typed into")
	}
}
