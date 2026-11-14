package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// A pi child's later runs wake its parent only if the inbox can tell the runs
// apart. The extension's turn_end writes the status file (waiting) before the
// run's agent_settled exists, so the settle cannot be a status edge: it MERGES the
// run's identity into the file turn_end wrote and leaves everything else as it
// was. The next turn_start rebuilds the file and so erases it.
//
// Every sequence below is one pi produces: session_start, then per LLM response
// turn_start (running) and turn_end (waiting), and agent_settled after the run's
// last turn_end. The v4+ extension also sends a capability bit (`settles`) and a
// run token (`run_id`, minted at agent_start) on turn_start, turn_end and
// agent_settled; the handler persists them as `settles` and `pi_run_id`.

func piHookStatusFile(t *testing.T, instanceID string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(session.GetHooksDir(), instanceID+".json"))
	if err != nil {
		t.Fatalf("no hook status file for %s: %v", instanceID, err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func piHookEnv(t *testing.T, instance string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("AGENTDECK_INSTANCE_ID", instance)
}

// piEdge is one extension emit of a v4+ extension. run "" is an old extension
// (no capability bit, no run token).
func piEdge(event, run string) string {
	if run == "" {
		return fmt.Sprintf(`{"hook_event_name":%q,"source":"pi","cwd":"/tmp"}`, event)
	}
	return fmt.Sprintf(`{"hook_event_name":%q,"source":"pi","cwd":"/tmp","settles":true,"run_id":%q}`, event, run)
}

// piSettlePayload is agent_settled for a run: the v3 fixture's text and ids plus
// the run token.
func piSettlePayload(run string) string {
	return fmt.Sprintf(`{"hook_event_name":"agent_settled","source":"pi","cwd":"/tmp","session_id":"pi-sess-1","turn_id":"entry-77","run_id":%q,"settles":true}`, run)
}

var piSettleKeys = []string{"pi_settle_turn_id", "pi_settle_session_id", "pi_settled_at"}

// agent_settled merges pi_settle_turn_id, pi_settle_session_id and pi_settled_at
// into the file turn_end wrote; status, event and the timestamps are untouched and
// no status edge is made.
func TestHookHandler_PiSettleMergesItsIdentityAndLeavesTheStatusAlone(t *testing.T) {
	piHookEnv(t, "inst-pi-turn")
	runHookHandlerWith(t, piEdge("turn_start", "run-A"))
	runHookHandlerWith(t, piEdge("turn_end", "run-A"))
	before := piHookStatusFile(t, "inst-pi-turn")
	if before["event"] != "turn_end" || before["status"] != "waiting" {
		t.Fatalf("test setup: turn_end wrote %v", before)
	}
	runHookHandlerWith(t, piSettlePayload("run-A"))
	after := piHookStatusFile(t, "inst-pi-turn")
	for _, k := range []string{"status", "event", "ts", "received_at_ns"} {
		if !reflect.DeepEqual(before[k], after[k]) {
			t.Errorf("the settle changed %s: %v -> %v (it must be no status edge)", k, before[k], after[k])
		}
	}
	if after["pi_settle_turn_id"] != "entry-77" || after["pi_settle_session_id"] != "pi-sess-1" {
		t.Errorf("settle identity = %v / %v, want entry-77 / pi-sess-1", after["pi_settle_turn_id"], after["pi_settle_session_id"])
	}
	if at, _ := after["pi_settled_at"].(float64); at <= 0 {
		t.Errorf("pi_settled_at = %v, want a time", after["pi_settled_at"])
	}
}

// The handler persists the capability bit and the run token from turn_start and
// turn_end; a payload without them (an old extension) leaves both out.
func TestHookHandler_PiPersistsTheSettlesBitAndTheRunToken(t *testing.T) {
	piHookEnv(t, "inst-pi-bit")
	for _, event := range []string{"turn_start", "turn_end"} {
		runHookHandlerWith(t, piEdge(event, "run-A"))
		got := piHookStatusFile(t, "inst-pi-bit")
		if got["settles"] != true || got["pi_run_id"] != "run-A" {
			t.Errorf("%s: settles = %v, pi_run_id = %v, want true and run-A", event, got["settles"], got["pi_run_id"])
		}
	}
	runHookHandlerWith(t, piEdge("turn_start", ""))
	got := piHookStatusFile(t, "inst-pi-bit")
	if _, has := got["settles"]; has {
		t.Errorf("settles = %v written for an old extension's turn_start", got["settles"])
	}
	if _, has := got["pi_run_id"]; has {
		t.Errorf("pi_run_id = %v written for an old extension's turn_start", got["pi_run_id"])
	}
}

// The next turn_start rebuilds the file: the previous run's settle is gone.
func TestHookHandler_PiTurnStartErasesTheSettle(t *testing.T) {
	piHookEnv(t, "inst-pi-erase")
	runHookHandlerWith(t, piEdge("turn_start", "run-A"))
	runHookHandlerWith(t, piEdge("turn_end", "run-A"))
	runHookHandlerWith(t, piSettlePayload("run-A"))
	runHookHandlerWith(t, piEdge("turn_start", "run-B"))
	got := piHookStatusFile(t, "inst-pi-erase")
	if got["event"] != "turn_start" || got["status"] != "running" {
		t.Fatalf("status file %v", got)
	}
	for _, k := range piSettleKeys {
		if _, has := got[k]; has {
			t.Errorf("%s survived the next turn_start: %v", k, got[k])
		}
	}
}

// A turn_end on its own carries no settle.
func TestHookHandler_PiTurnEndWritesNoSettle(t *testing.T) {
	piHookEnv(t, "inst-pi-noturn")
	runHookHandlerWith(t, piEdge("turn_start", "run-A"))
	runHookHandlerWith(t, piEdge("turn_end", "run-A"))
	got := piHookStatusFile(t, "inst-pi-noturn")
	for _, k := range piSettleKeys {
		if _, has := got[k]; has {
			t.Errorf("%s = %v written by a turn_end", k, got[k])
		}
	}
}

// A settle that arrives after a newer turn_start (it was slow) must not merge
// into that file: it is the new run's, and keeps turn_start / running.
func TestHookHandler_PiSettleNeverOverwritesANewerTurnStart(t *testing.T) {
	piHookEnv(t, "inst-pi-lost")
	runHookHandlerWith(t, piEdge("turn_start", "run-A"))
	runHookHandlerWith(t, piEdge("turn_end", "run-A"))
	runHookHandlerWith(t, piEdge("turn_start", "run-B"))
	started := piHookStatusFile(t, "inst-pi-lost")
	runHookHandlerWith(t, piSettlePayload("run-A")) // the old run's settle, late
	got := piHookStatusFile(t, "inst-pi-lost")
	if got["event"] != "turn_start" || got["status"] != "running" {
		t.Fatalf("a late settle rewrote the new run's status file: %v", got)
	}
	for _, k := range piSettleKeys {
		if _, has := got[k]; has {
			t.Errorf("%s = %v merged into the newer run's file", k, got[k])
		}
	}
	if !reflect.DeepEqual(started["ts"], got["ts"]) || !reflect.DeepEqual(started["received_at_ns"], got["received_at_ns"]) {
		t.Errorf("the dropped settle still rewrote the file's timestamps: %v -> %v", started, got)
	}
}

// The run token: run A's late settle must not attach to run B's turn_end, even
// though both runs are the same session and B's file is a turn_end.
func TestHookHandler_PiALateSettleForRunADoesNotAttachToRunBsTurnEnd(t *testing.T) {
	piHookEnv(t, "inst-pi-runtoken")
	runHookHandlerWith(t, piEdge("turn_start", "run-A"))
	runHookHandlerWith(t, piEdge("turn_end", "run-A"))
	runHookHandlerWith(t, piEdge("turn_start", "run-B"))
	runHookHandlerWith(t, piEdge("turn_end", "run-B"))
	runHookHandlerWith(t, piSettlePayload("run-A")) // A's settle, after B's turn_end landed
	got := piHookStatusFile(t, "inst-pi-runtoken")
	for _, k := range piSettleKeys {
		if _, has := got[k]; has {
			t.Fatalf("%s = %v: run A's settle attached to run B's turn_end", k, got[k])
		}
	}
	runHookHandlerWith(t, piSettlePayload("run-B")) // B's own settle still merges
	if got := piHookStatusFile(t, "inst-pi-runtoken"); got["pi_settle_turn_id"] != "entry-77" {
		t.Errorf("run B's own settle did not merge: %v", got)
	}
}

// A settle only belongs to the turn_end of its own session.
func TestHookHandler_PiSettleOfAnotherSessionIsDropped(t *testing.T) {
	piHookEnv(t, "inst-pi-session")
	runHookHandlerWith(t, `{"hook_event_name":"turn_start","source":"pi","cwd":"/tmp","session_id":"other-sess"}`)
	runHookHandlerWith(t, `{"hook_event_name":"turn_end","source":"pi","cwd":"/tmp","session_id":"other-sess"}`)
	runHookHandlerWith(t, fixture(t, "pi_agentsettled_v3.json")) // session pi-sess-1
	got := piHookStatusFile(t, "inst-pi-session")
	if _, has := got["pi_settle_turn_id"]; has {
		t.Errorf("a settle of session pi-sess-1 merged into session other-sess's turn_end: %v", got)
	}
}

// A resumed pi (--continue) starts with session_start: a fresh run start; the
// previous run's settle is gone and cannot be taken for the new run's.
func TestHookHandler_PiResumeIsAFreshRunStart(t *testing.T) {
	piHookEnv(t, "inst-pi-resume")
	runHookHandlerWith(t, piEdge("turn_start", "run-A"))
	runHookHandlerWith(t, piEdge("turn_end", "run-A"))
	runHookHandlerWith(t, piSettlePayload("run-A"))
	runHookHandlerWith(t, `{"hook_event_name":"session_start","source":"pi","cwd":"/tmp"}`)
	got := piHookStatusFile(t, "inst-pi-resume")
	if got["event"] != "session_start" || got["status"] != "waiting" {
		t.Fatalf("status file %v", got)
	}
	for _, k := range piSettleKeys {
		if _, has := got[k]; has {
			t.Errorf("%s survived a resume's session_start: %v", k, got[k])
		}
	}
}

// ---- unsettled runs are carried forward --------------------------------------------

// piOrphans reads the file's pi_orphan_runs: [{run_id, turn_end_ns}, ...].
func piOrphans(t *testing.T, instanceID string) []map[string]any {
	t.Helper()
	raw, _ := piHookStatusFile(t, instanceID)["pi_orphan_runs"].([]any)
	var out []map[string]any
	for _, e := range raw {
		m, _ := e.(map[string]any)
		out = append(out, m)
	}
	return out
}

func orphanIDs(list []map[string]any) []string {
	var ids []string
	for _, m := range list {
		ids = append(ids, fmt.Sprint(m["run_id"]))
	}
	return ids
}

// EVERY pi write carries the list of unsettled runs the previous file held (at
// most four, the oldest dropped), and a turn_start of a new run adds the previous
// run when it never settled: turn_start, turn_end and the settle merge all keep it.
func TestHookHandler_PiEveryWriteCarriesTheUnsettledRunList(t *testing.T) {
	piHookEnv(t, "inst-pi-orphans")
	runHookHandlerWith(t, piEdge("turn_start", "run-A")) // A aborted before its turn_end
	runHookHandlerWith(t, piEdge("turn_start", "run-B"))
	if got := orphanIDs(piOrphans(t, "inst-pi-orphans")); !reflect.DeepEqual(got, []string{"run-A"}) {
		t.Fatalf("B's turn_start over an aborted A: orphans %v, want [run-A]", got)
	}
	if _, has := piOrphans(t, "inst-pi-orphans")[0]["turn_end_ns"]; has {
		t.Errorf("an aborted run has no turn_end time")
	}
	runHookHandlerWith(t, piEdge("turn_end", "run-B")) // B's turn_end must not drop A
	if got := orphanIDs(piOrphans(t, "inst-pi-orphans")); !reflect.DeepEqual(got, []string{"run-A"}) {
		t.Fatalf("B's turn_end: orphans %v, want [run-A] carried", got)
	}
	ended := piHookStatusFile(t, "inst-pi-orphans")
	runHookHandlerWith(t, piEdge("turn_start", "run-C")) // B unsettled: A and B
	list := piOrphans(t, "inst-pi-orphans")
	if got := orphanIDs(list); !reflect.DeepEqual(got, []string{"run-A", "run-B"}) {
		t.Fatalf("C's turn_start: orphans %v, want [run-A run-B]", got)
	}
	if !reflect.DeepEqual(list[1]["turn_end_ns"], ended["received_at_ns"]) {
		t.Errorf("B's entry carries turn_end_ns %v, want its turn_end time %v", list[1]["turn_end_ns"], ended["received_at_ns"])
	}
	runHookHandlerWith(t, piEdge("turn_end", "run-C"))
	runHookHandlerWith(t, piSettlePayload("run-C")) // the settle merge keeps the list too
	if got := orphanIDs(piOrphans(t, "inst-pi-orphans")); !reflect.DeepEqual(got, []string{"run-A", "run-B"}) {
		t.Errorf("after C's settle merge: orphans %v, want [run-A run-B] carried", got)
	}
}

// At most four entries: a fifth unsettled run drops the oldest.
func TestHookHandler_PiTheOrphanListKeepsAtMostFourRuns(t *testing.T) {
	piHookEnv(t, "inst-pi-four")
	for _, run := range []string{"run-A", "run-B", "run-C", "run-D", "run-E"} {
		runHookHandlerWith(t, piEdge("turn_start", run))
		runHookHandlerWith(t, piEdge("turn_end", run))
	}
	runHookHandlerWith(t, piEdge("turn_start", "run-F"))
	if got := orphanIDs(piOrphans(t, "inst-pi-four")); !reflect.DeepEqual(got, []string{"run-B", "run-C", "run-D", "run-E"}) {
		t.Errorf("orphans %v, want [run-B run-C run-D run-E] (the oldest dropped)", got)
	}
}

// A settled run is carried too (the daemon may not have polled its settle yet),
// with its entry recording that it settled; a run's own next response (a
// tool-using run) is not an orphan of itself. (This replaces the earlier test that
// asserted a settled run is NOT carried.)
func TestHookHandler_PiTurnStartCarriesASettledRunAndRecordsThatItSettled(t *testing.T) {
	piHookEnv(t, "inst-pi-settledcarry")
	runHookHandlerWith(t, piEdge("turn_start", "run-A"))
	runHookHandlerWith(t, piEdge("turn_end", "run-A"))
	runHookHandlerWith(t, piSettlePayload("run-A"))
	runHookHandlerWith(t, piEdge("turn_start", "run-B"))
	list := piOrphans(t, "inst-pi-settledcarry")
	if !reflect.DeepEqual(orphanIDs(list), []string{"run-A"}) || list[0]["settled"] != true {
		t.Fatalf("a settled run overwritten by the next turn_start: orphans %v, want [run-A] with settled=true", list)
	}
	runHookHandlerWith(t, piEdge("turn_end", "run-B"))
	runHookHandlerWith(t, piEdge("turn_start", "run-B"))
	if got := orphanIDs(piOrphans(t, "inst-pi-settledcarry")); !reflect.DeepEqual(got, []string{"run-A"}) {
		t.Errorf("a run's own next response changed the list: %v, want [run-A]", got)
	}
}

// A settled run overwritten by a lifecycle write is carried as well.
func TestHookHandler_PiALifecycleWriteCarriesASettledRun(t *testing.T) {
	piHookEnv(t, "inst-pi-settledlife")
	runHookHandlerWith(t, piEdge("turn_start", "run-A"))
	runHookHandlerWith(t, piEdge("turn_end", "run-A"))
	runHookHandlerWith(t, piSettlePayload("run-A"))
	runHookHandlerWith(t, `{"hook_event_name":"session_start","source":"pi","cwd":"/tmp"}`)
	list := piOrphans(t, "inst-pi-settledlife")
	if !reflect.DeepEqual(orphanIDs(list), []string{"run-A"}) || list[0]["settled"] != true {
		t.Fatalf("a settled run overwritten by session_start: orphans %v, want [run-A] with settled=true", list)
	}
}

// ---- lifecycle writes keep unsettled runs ---------------------------------------------

// session_start and session_shutdown (/new, /resume, /fork, /reload, exit) rebuild
// the status file without run fields. They must carry the current unsettled run
// (with its turn_end time when it had one) and the existing orphan list forward, as
// turn_start does: the daemon cannot emit a run the file no longer names.
func TestHookHandler_PiLifecycleWritesCarryTheCurrentUnsettledRun(t *testing.T) {
	for _, lifecycle := range []string{"session_start", "session_shutdown"} {
		t.Run(lifecycle, func(t *testing.T) {
			piHookEnv(t, "inst-pi-"+lifecycle)
			runHookHandlerWith(t, piEdge("turn_start", "run-A"))
			runHookHandlerWith(t, piEdge("turn_end", "run-A"))
			ended := piHookStatusFile(t, "inst-pi-"+lifecycle)
			runHookHandlerWith(t, fmt.Sprintf(`{"hook_event_name":%q,"source":"pi","cwd":"/tmp"}`, lifecycle))
			got := piHookStatusFile(t, "inst-pi-"+lifecycle)
			if got["event"] != lifecycle {
				t.Fatalf("status file %v", got)
			}
			list := piOrphans(t, "inst-pi-"+lifecycle)
			if !reflect.DeepEqual(orphanIDs(list), []string{"run-A"}) {
				t.Fatalf("after %s the file carries %v, want [run-A] (the lifecycle write erased the unsettled run)", lifecycle, orphanIDs(list))
			}
			if !reflect.DeepEqual(list[0]["turn_end_ns"], ended["received_at_ns"]) {
				t.Errorf("A's entry carries turn_end_ns %v, want %v", list[0]["turn_end_ns"], ended["received_at_ns"])
			}
		})
	}
}

// An existing list survives a lifecycle write, and the current run joins it,
// settled or not.
func TestHookHandler_PiAnOrphanListSurvivesALifecycleWrite(t *testing.T) {
	piHookEnv(t, "inst-pi-listlife")
	runHookHandlerWith(t, piEdge("turn_start", "run-A"))
	runHookHandlerWith(t, piEdge("turn_start", "run-B")) // list [A]
	runHookHandlerWith(t, piEdge("turn_end", "run-B"))
	runHookHandlerWith(t, `{"hook_event_name":"session_start","source":"pi","cwd":"/tmp"}`)
	if got := orphanIDs(piOrphans(t, "inst-pi-listlife")); !reflect.DeepEqual(got, []string{"run-A", "run-B"}) {
		t.Fatalf("after session_start: %v, want [run-A run-B]", got)
	}
	runHookHandlerWith(t, piEdge("turn_start", "run-C"))
	runHookHandlerWith(t, piEdge("turn_end", "run-C"))
	runHookHandlerWith(t, piSettlePayload("run-C"))
	runHookHandlerWith(t, `{"hook_event_name":"session_shutdown","source":"pi","cwd":"/tmp"}`)
	if got := orphanIDs(piOrphans(t, "inst-pi-listlife")); !reflect.DeepEqual(got, []string{"run-A", "run-B", "run-C"}) {
		t.Errorf("after a settled run and session_shutdown: %v, want [run-A run-B run-C] (a settled run is carried too)", got)
	}
}

// An entry older than the consumed-key retention (~14 days) is dropped on the next
// pi write, so it cannot re-emit after its consumed key is pruned; a recent entry
// is kept.
func TestHookHandler_PiDropsOrphansOlderThanTheConsumedKeyRetention(t *testing.T) {
	piHookEnv(t, "inst-pi-retention")
	runHookHandlerWith(t, piEdge("turn_start", "run-X"))
	runHookHandlerWith(t, piEdge("turn_end", "run-X"))
	path := filepath.Join(session.GetHooksDir(), "inst-pi-retention.json")
	file := piHookStatusFile(t, "inst-pi-retention")
	file["pi_orphan_runs"] = []map[string]any{
		{"run_id": "run-OLD", "turn_end_ns": time.Now().Add(-15 * 24 * time.Hour).UnixNano()},
		{"run_id": "run-RECENT", "turn_end_ns": time.Now().Add(-time.Hour).UnixNano()},
	}
	data, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	runHookHandlerWith(t, piEdge("turn_start", "run-Y"))
	got := orphanIDs(piOrphans(t, "inst-pi-retention"))
	if !reflect.DeepEqual(got, []string{"run-RECENT", "run-X"}) {
		t.Errorf("orphans after the next pi write: %v, want [run-RECENT run-X] (the 15-day-old entry dropped)", got)
	}
}
