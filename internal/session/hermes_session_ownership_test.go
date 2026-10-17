package session

import (
	"encoding/json"
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
