package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// Issue #2582: two Hermes entries in one workspace. Restart used to resume the
// newest CLI conversation in the directory (`hermes sessions list --limit 1
// --workspace <dir>`), which is entry B's when B spoke last, so restarting A
// attached A to B's conversation.

const (
	hermesSIDA        = "20261007_210308_fba186"
	hermesSIDB        = "20261009_173544_87f49b"
	hermesSIDDelegate = "20261010_090000_d31e9a"
)

func hermesHook(inst *Instance, event, sid string) {
	inst.UpdateHookStatus(&HookStatus{Status: "running", SessionID: sid, Event: event, UpdatedAt: time.Now()})
}

func hermesHookAt(inst *Instance, event, sid string, at time.Time) {
	inst.UpdateHookStatus(&HookStatus{Status: "running", SessionID: sid, Event: event, UpdatedAt: at})
}

func saveHermesRow(t *testing.T, db *statedb.StateDB, inst *Instance) {
	t.Helper()
	if err := db.SaveInstance(&statedb.InstanceRow{ID: inst.ID, Title: inst.Title, ProjectPath: inst.ProjectPath,
		Tool: "hermes", Status: "idle", CreatedAt: time.Now()}); err != nil {
		t.Fatalf("SaveInstance: %v", err)
	}
}

func persistedHermesID(t *testing.T, db *statedb.StateDB, id string) string {
	t.Helper()
	rows, err := db.LoadInstances()
	if err != nil {
		t.Fatalf("LoadInstances: %v", err)
	}
	for _, r := range rows {
		if r.ID == id {
			return ReadHermesSessionIDFromToolData(r.ToolData)
		}
	}
	return ""
}

const (
	hermesSIDCompressed = "20261011_120000_c0ffee"
	hermesSIDNew        = "20261012_130000_be11ed"
)

func TestHermesRestartResumesOwnSessionInSharedWorkspace(t *testing.T) {
	db := withTempGlobalStateDB(t)
	workspace := t.TempDir()
	// The newest conversation in the shared workspace is B's.
	withFakeHermes(t, `printf 'Title Workspace LastActive ID\n--- --- --- ---\nB ws 1m `+hermesSIDB+`\n'`)

	a := NewInstanceWithTool("hermes-a", workspace, "hermes")
	b := NewInstanceWithTool("hermes-b", workspace, "hermes")
	for _, inst := range []*Instance{a, b} {
		if err := db.SaveInstance(&statedb.InstanceRow{ID: inst.ID, Title: inst.Title, ProjectPath: workspace,
			Tool: "hermes", Status: "idle", CreatedAt: time.Now()}); err != nil {
			t.Fatalf("SaveInstance: %v", err)
		}
	}

	hermesHook(a, "pre_llm_call", hermesSIDA)
	hermesHook(b, "pre_llm_call", hermesSIDB)

	// Restart of A (fresh process after a reboot: id comes from the DB row).
	rows, err := db.LoadInstances()
	if err != nil {
		t.Fatalf("LoadInstances: %v", err)
	}
	var persisted string
	for _, r := range rows {
		if r.ID == a.ID {
			persisted = ReadHermesSessionIDFromToolData(r.ToolData)
		}
	}
	if persisted != hermesSIDA {
		t.Fatalf("persisted hermes_session_id for A = %q, want %q", persisted, hermesSIDA)
	}
	reloaded := NewInstanceWithTool("hermes-a", workspace, "hermes")
	reloaded.ID = a.ID
	reloaded.HermesSessionID = persisted
	reloaded.prepareHermesResume()
	cmd := reloaded.buildHermesCommand("hermes")
	if !strings.Contains(cmd, "--resume "+hermesSIDA) || strings.Contains(cmd, hermesSIDB) {
		t.Fatalf("restart of A must resume its own session %s, got command %q", hermesSIDA, cmd)
	}
	if w := reloaded.ConsumeCodexRestartWarning(); w != "" {
		t.Fatalf("unexpected restart warning with a recorded id: %q", w)
	}
}

func TestHermesDelegatedAgentIDIsIgnored(t *testing.T) {
	a := NewInstanceWithTool("hermes-a", t.TempDir(), "hermes")
	hermesHook(a, "pre_llm_call", hermesSIDA)
	hermesHook(a, "pre_tool_call", hermesSIDDelegate) // delegate_task child
	if a.HermesSessionID != hermesSIDA {
		t.Fatalf("delegated agent id took over the binding: got %q, want %q", a.HermesSessionID, hermesSIDA)
	}
}

func TestHermesNewSessionAfterFinalizeRebinds(t *testing.T) {
	a := NewInstanceWithTool("hermes-a", t.TempDir(), "hermes")
	hermesHook(a, "pre_llm_call", hermesSIDA)
	a.UpdateHookStatus(&HookStatus{Status: "dead", SessionID: hermesSIDA, Event: "on_session_finalize", UpdatedAt: time.Now()})
	hermesHook(a, "on_session_start", hermesSIDB) // /new in the same process
	if a.HermesSessionID != hermesSIDB {
		t.Fatalf("after the owned session finalized, the next session must bind: got %q, want %q", a.HermesSessionID, hermesSIDB)
	}
	hermesHook(a, "pre_tool_call", hermesSIDDelegate)
	if a.HermesSessionID != hermesSIDB {
		t.Fatalf("rebind window must close after one bind: got %q", a.HermesSessionID)
	}
}

func TestHermesRestartWithoutOwnedIDStartsFreshAndWarns(t *testing.T) {
	workspace := t.TempDir()
	withFakeHermes(t, `printf 'Title Workspace LastActive ID\n--- --- --- ---\nB ws 1m `+hermesSIDB+`\n'`)
	a := NewInstanceWithTool("hermes-a", workspace, "hermes")
	a.prepareHermesResume()
	if cmd := a.buildHermesCommand("hermes"); strings.Contains(cmd, "--resume") {
		t.Fatalf("no recorded id: must not resume a neighbour's session, got %q", cmd)
	}
	if w := a.ConsumeCodexRestartWarning(); !strings.Contains(w, "no conversation is recorded") || !strings.Contains(w, hermesSIDB) {
		t.Fatalf("missing ownership warning, got %q", w)
	}
}

func TestHermesSessionIDToolDataRoundTrip(t *testing.T) {
	td := WriteHermesSessionIDToToolData(json.RawMessage(`{"other":1}`), hermesSIDA)
	if got := ReadHermesSessionIDFromToolData(td); got != hermesSIDA {
		t.Fatalf("round trip = %q, want %q (raw %s)", got, hermesSIDA, td)
	}
	if !strings.Contains(string(td), `"other":1`) {
		t.Fatalf("sibling key dropped: %s", td)
	}
	// An empty id omits the key so a full save cannot wipe the stored binding.
	if got := WriteHermesSessionIDToToolData(json.RawMessage(`{"other":1}`), ""); string(got) != `{"other":1}` {
		t.Fatalf("empty id must leave tool_data untouched, got %s", got)
	}
	merged := statedb.MergeToolDataExtras(json.RawMessage(`{"hermes_session_id":"`+hermesSIDA+`"}`), json.RawMessage(`{}`))
	if got := ReadHermesSessionIDFromToolData(merged); got != hermesSIDA {
		t.Fatalf("full save dropped the binding: %s", merged)
	}
}

// Review blocker 1 on #2696: hook events arrive as samples, so the first id an
// entry sees may be a delegate_task child's. A delegate's tool call seen first
// must not bind; the parent's next turn edge must.
func TestHermesDelegateEventSeenFirstDoesNotBind(t *testing.T) {
	db := withTempGlobalStateDB(t)
	a := NewInstanceWithTool("hermes-a", t.TempDir(), "hermes")
	saveHermesRow(t, db, a)

	hermesHook(a, "pre_tool_call", hermesSIDDelegate)
	hermesHook(a, "post_api_request", hermesSIDDelegate)
	if a.HermesSessionID != "" {
		t.Fatalf("delegate tool/api event bound the entry: got %q", a.HermesSessionID)
	}
	hermesHook(a, "post_llm_call", hermesSIDA)
	if a.HermesSessionID != hermesSIDA {
		t.Fatalf("parent turn edge must bind: got %q, want %q", a.HermesSessionID, hermesSIDA)
	}
	if got := persistedHermesID(t, db, a.ID); got != hermesSIDA {
		t.Fatalf("persisted id = %q, want %q", got, hermesSIDA)
	}
}

// An entry saved before ids were recorded: the first sample after upgrade is a
// delegate's, then the parent speaks. Same contract as above, starting from a
// persisted row with no id.
func TestHermesExistingEntryWithoutIDAfterUpgrade(t *testing.T) {
	db := withTempGlobalStateDB(t)
	workspace := t.TempDir()
	a := NewInstanceWithTool("hermes-a", workspace, "hermes")
	saveHermesRow(t, db, a)
	if got := persistedHermesID(t, db, a.ID); got != "" {
		t.Fatalf("precondition: upgraded row has id %q", got)
	}
	hermesHook(a, "pre_tool_call", hermesSIDDelegate)
	hermesHook(a, "pre_llm_call", hermesSIDA)
	hermesHook(a, "pre_tool_call", hermesSIDDelegate)
	if a.HermesSessionID != hermesSIDA || persistedHermesID(t, db, a.ID) != hermesSIDA {
		t.Fatalf("upgraded entry bound %q (persisted %q), want %q",
			a.HermesSessionID, persistedHermesID(t, db, a.ID), hermesSIDA)
	}
}

// Before any parent edge is seen, a lone entry in its directory restarts into
// the newest conversation there, as it did before #2582, so upgrading does not
// regress the single-entry case.
func TestHermesRestartWithoutIDSingleEntryResumesNewest(t *testing.T) {
	db := withTempGlobalStateDB(t)
	workspace := t.TempDir()
	withFakeHermes(t, `printf 'Title Workspace LastActive ID\n--- --- --- ---\nA ws 1m `+hermesSIDA+`\n'`)
	a := NewInstanceWithTool("hermes-a", workspace, "hermes")
	saveHermesRow(t, db, a)
	other := NewInstanceWithTool("other-dir", t.TempDir(), "hermes")
	saveHermesRow(t, db, other)

	a.prepareHermesResume()
	if cmd := a.buildHermesCommand("hermes"); !strings.Contains(cmd, "--resume "+hermesSIDA) {
		t.Fatalf("lone entry must resume the newest conversation in its directory, got %q", cmd)
	}
	if w := a.ConsumeCodexRestartWarning(); w != "" {
		t.Fatalf("unexpected warning for a lone entry: %q", w)
	}
}

// Same restart with a second Hermes entry in the directory: the newest
// conversation may be the neighbour's, so start fresh and warn.
func TestHermesRestartWithoutIDSharedWorkspaceStartsFresh(t *testing.T) {
	db := withTempGlobalStateDB(t)
	workspace := t.TempDir()
	withFakeHermes(t, `printf 'Title Workspace LastActive ID\n--- --- --- ---\nB ws 1m `+hermesSIDB+`\n'`)
	a := NewInstanceWithTool("hermes-a", workspace, "hermes")
	b := NewInstanceWithTool("hermes-b", workspace, "hermes")
	saveHermesRow(t, db, a)
	saveHermesRow(t, db, b)

	a.prepareHermesResume()
	if cmd := a.buildHermesCommand("hermes"); strings.Contains(cmd, "--resume") {
		t.Fatalf("shared directory: must not resume a neighbour's session, got %q", cmd)
	}
	if w := a.ConsumeCodexRestartWarning(); !strings.Contains(w, "no conversation is recorded") {
		t.Fatalf("missing ownership warning, got %q", w)
	}
}

// Review blocker 2 on #2696: context compression rotates session_id without
// on_session_finalize (the new session links back through parent_session_id).
// The continued conversation's next turn edge must move the binding, then a
// later /new must bind too, and restart must resume the latest one.
func TestHermesCompressionRotationThenNewRebinds(t *testing.T) {
	db := withTempGlobalStateDB(t)
	workspace := t.TempDir()
	a := NewInstanceWithTool("hermes-a", workspace, "hermes")
	saveHermesRow(t, db, a)

	hermesHook(a, "pre_llm_call", hermesSIDA)
	hermesHook(a, "pre_tool_call", hermesSIDDelegate)
	// Compression mid-turn: no finalize, the turn ends under the new id.
	hermesHook(a, "post_api_request", hermesSIDCompressed)
	hermesHook(a, "post_llm_call", hermesSIDCompressed)
	if a.HermesSessionID != hermesSIDCompressed {
		t.Fatalf("compression rotation not followed: got %q, want %q", a.HermesSessionID, hermesSIDCompressed)
	}
	hermesHook(a, "pre_tool_call", hermesSIDDelegate)
	if a.HermesSessionID != hermesSIDCompressed {
		t.Fatalf("delegate took over after compression: got %q", a.HermesSessionID)
	}

	// /new after the compression.
	hermesHook(a, "on_session_start", hermesSIDNew)
	if a.HermesSessionID != hermesSIDNew {
		t.Fatalf("/new after compression not bound: got %q, want %q", a.HermesSessionID, hermesSIDNew)
	}
	if got := persistedHermesID(t, db, a.ID); got != hermesSIDNew {
		t.Fatalf("persisted id = %q, want %q", got, hermesSIDNew)
	}

	reloaded := NewInstanceWithTool("hermes-a", workspace, "hermes")
	reloaded.ID = a.ID
	reloaded.HermesSessionID = persistedHermesID(t, db, a.ID)
	reloaded.prepareHermesResume()
	if cmd := reloaded.buildHermesCommand("hermes"); !strings.Contains(cmd, "--resume "+hermesSIDNew) {
		t.Fatalf("restart must resume the latest conversation %s, got %q", hermesSIDNew, cmd)
	}
}

// A re-fed sample older than the current binding must not roll it back.
func TestHermesStaleParentSampleDoesNotRollBack(t *testing.T) {
	a := NewInstanceWithTool("hermes-a", t.TempDir(), "hermes")
	now := time.Now()
	hermesHookAt(a, "post_llm_call", hermesSIDA, now.Add(-time.Minute))
	hermesHookAt(a, "pre_llm_call", hermesSIDB, now)
	hermesHookAt(a, "post_llm_call", hermesSIDA, now.Add(-time.Minute))
	if a.HermesSessionID != hermesSIDB {
		t.Fatalf("stale sample rolled the binding back: got %q, want %q", a.HermesSessionID, hermesSIDB)
	}
}

// A delegate fires a hook per tool and API call; the reject line is written
// once per distinct candidate, not once per event.
func TestHermesRejectLogIsRateLimited(t *testing.T) {
	a := NewInstanceWithTool("hermes-a", t.TempDir(), "hermes")
	hermesHook(a, "pre_llm_call", hermesSIDA)
	for n := 0; n < 5; n++ {
		hermesHook(a, "pre_tool_call", hermesSIDDelegate)
		hermesHook(a, "post_tool_call", hermesSIDDelegate)
	}
	data, _ := os.ReadFile(GetSessionIDLifecycleLogPath())
	count := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.Contains(line, a.ID) && strings.Contains(line, `"reject"`) && strings.Contains(line, hermesSIDDelegate) {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("reject lines for the delegate = %d, want 1", count)
	}
}

func hermesSubagentHookAt(inst *Instance, event, sid string, at time.Time) {
	inst.UpdateHookStatus(&HookStatus{Status: "running", SessionID: sid, Event: event, Platform: "subagent", UpdatedAt: at})
}

// Review blocker on #2696: a delegate_task child is AIAgent(platform="subagent")
// running its own turns in the parent's process, so it fires on_session_start,
// pre_llm_call and post_llm_call with its own id (extra.platform="subagent").
// Those turn edges must never bind or rotate, and no parent post_llm_call
// follows here (interrupted parent turn / delegation outliving it).
func TestHermesSubagentTurnEdgesNeverBind(t *testing.T) {
	db := withTempGlobalStateDB(t)
	a := NewInstanceWithTool("hermes-a", t.TempDir(), "hermes")
	saveHermesRow(t, db, a)
	t0 := time.Now()

	// Delegate turn edges arrive first, before the parent has bound.
	hermesSubagentHookAt(a, "on_session_start", hermesSIDDelegate, t0)
	hermesSubagentHookAt(a, "pre_llm_call", hermesSIDDelegate, t0.Add(time.Second))
	if a.HermesSessionID != "" {
		t.Fatalf("delegate turn edge bound an unbound entry: got %q", a.HermesSessionID)
	}

	a.UpdateHookStatus(&HookStatus{Status: "running", SessionID: hermesSIDA, Event: "pre_llm_call",
		Platform: "cli", UpdatedAt: t0.Add(2 * time.Second)})
	if a.HermesSessionID != hermesSIDA {
		t.Fatalf("parent turn edge must bind: got %q, want %q", a.HermesSessionID, hermesSIDA)
	}

	// Delegate turn edges between parent turns, newer than the binding.
	hermesSubagentHookAt(a, "pre_llm_call", hermesSIDDelegate, t0.Add(3*time.Second))
	hermesSubagentHookAt(a, "post_llm_call", hermesSIDDelegate, t0.Add(4*time.Second))
	hermesSubagentHookAt(a, "on_session_start", hermesSIDDelegate, t0.Add(5*time.Second))
	if a.HermesSessionID != hermesSIDA {
		t.Fatalf("delegate turn edge took over binding: got %s want %s", a.HermesSessionID, hermesSIDA)
	}
	if got := persistedHermesID(t, db, a.ID); got != hermesSIDA {
		t.Fatalf("persisted id = %q, want %q", got, hermesSIDA)
	}

	// A real parent rotation (compression, /new) still rebinds afterwards.
	hermesHookAt(a, "pre_llm_call", hermesSIDCompressed, t0.Add(6*time.Second))
	if a.HermesSessionID != hermesSIDCompressed {
		t.Fatalf("parent rotation after delegate events must rebind: got %q", a.HermesSessionID)
	}
}

// The platform recorded by the hook handler must survive every reader that
// feeds UpdateHookStatus: the watcher (processFile / scanDisk) and the
// transition daemon's readHookStatusFile.
func TestHookStatusReadersCarryPlatform(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	id := "hermes-platform-reader"
	hooksDir := GetHooksDir()
	if err := os.MkdirAll(hooksDir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"status":"running","session_id":"` + hermesSIDDelegate + `","event":"pre_llm_call","ts":` +
		jsonInt(time.Now().Unix()) + `,"platform":"subagent"}`
	if err := os.WriteFile(filepath.Join(hooksDir, id+".json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if hs := readHookStatusFile(id); hs == nil || hs.Platform != "subagent" {
		t.Fatalf("readHookStatusFile platform = %+v, want subagent", hs)
	}
	w := newTestWatcher(t, hooksDir)
	w.processFile(filepath.Join(hooksDir, id+".json"))
	if hs := w.GetHookStatus(id); hs == nil || hs.Platform != "subagent" {
		t.Fatalf("watcher processFile platform = %+v, want subagent", hs)
	}
	if hs := w.scanDisk()[id]; hs == nil || hs.Platform != "subagent" {
		t.Fatalf("watcher scanDisk platform = %+v, want subagent", hs)
	}
}

func jsonInt(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
