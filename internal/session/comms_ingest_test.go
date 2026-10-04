package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/comms"
)

// Comms Ledger ingest (docs/comms.md): the daemon drains the producers'
// spool and commits one record per turn, next to the inbox, with the same
// classification for Claude and a prompt-derived one for every other
// harness. These tests drive ingestCommsSpool with the same fixture the
// issue #2469 tests use, so the ledger and the inbox can be compared.

type commsFixture struct {
	*turnTestFixture
	codex *Instance
	shell *Instance
}

func newCommsFixture(t *testing.T) *commsFixture {
	t.Helper()
	f := newTurnTestFixture(t)
	t.Cleanup(SetCommsLedgerForTest(true))
	codex := NewInstanceWithTool("codex-worker", t.TempDir(), "codex")
	codex.ID = "codex-2470"
	codex.ParentSessionID = f.parent.ID
	codex.Status = StatusWaiting
	shell := NewInstanceWithTool("shell-worker", t.TempDir(), "shell")
	shell.ID = "shell-2470"
	shell.ParentSessionID = f.parent.ID
	shell.Status = StatusWaiting
	f.byID[codex.ID] = codex
	f.byID[shell.ID] = shell
	saveCommsFixtureInstances(t, codex, shell)
	t.Cleanup(f.d.closeCommsLedgers)
	return &commsFixture{turnTestFixture: f, codex: codex, shell: shell}
}

// saveCommsFixtureInstances registers extra children in the profile store so
// the notifier can resolve their parent (the inbox path drops an unknown
// child as dropped_no_target).
func saveCommsFixtureInstances(t *testing.T, extra ...*Instance) {
	t.Helper()
	storage, err := NewStorageWithProfile("default")
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.Save(append(instances, extra...)); err != nil {
		t.Fatal(err)
	}
}

func (f *commsFixture) ledgerRecords(t *testing.T) []comms.Record {
	t.Helper()
	bus, err := comms.OpenReader("default")
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer bus.Close()
	recs, _, err := comms.ReadAfter(bus, 0, 0)
	if err != nil {
		t.Fatalf("ReadAfter: %v", err)
	}
	return recs
}

// fileModesEnforced reports whether a 0400 file really refuses O_RDWR here
// (false for a root that keeps CAP_DAC_OVERRIDE; Docker with --cap-drop ALL
// enforces modes even as root). The disk-fault tests need the refusal.
func fileModesEnforced(t *testing.T) bool {
	t.Helper()
	p := filepath.Join(t.TempDir(), "probe")
	if err := os.WriteFile(p, []byte("x"), 0o400); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(p, os.O_RDWR, 0)
	if err == nil {
		_ = f.Close()
		return false
	}
	return true
}

func spoolTurn(t *testing.T, e CommsSpoolEntry) {
	t.Helper()
	if e.Edge == "" {
		e.Edge = CommsEdgeTurnEnd
	}
	if err := WriteCommsSpool(e); err != nil {
		t.Fatalf("WriteCommsSpool: %v", err)
	}
}

func TestCommsIngest_ClaudeTurnMatchesTheInboxClassification(t *testing.T) {
	f := newCommsFixture(t)
	f.appendTurn(t, fxHuman("u0", "run the board"), fxAssistantText("a0", "Starting 13 lanes."))
	spoolTurn(t, CommsSpoolEntry{Harness: "claude", Event: "Stop", Instance: f.child.ID, SessionID: f.child.ClaudeSessionID,
		Text: "Starting 13 lanes.", TranscriptPath: f.transcript, TSignal: time.Now().UnixMilli() - 20})

	f.d.ingestCommsSpool("default", f.byID)

	recs := f.ledgerRecords(t)
	if len(recs) != 1 {
		t.Fatalf("records: %+v", recs)
	}
	r := recs[0]
	if r.Kind != comms.KindTurn || r.From != f.child.ID || r.Tool != "claude" || r.Tier != comms.TierInfo ||
		r.Trigger != TurnTriggerHuman || r.Text != "Starting 13 lanes." || r.Profile != "default" || r.Seq != 1 {
		t.Fatalf("claude record: %+v", r)
	}
	if len(r.To) != 1 || r.To[0] != f.parent.ID || r.ToProfile != "default" || r.ToStore == "" || r.ToStore != r.Store {
		t.Fatalf("addressing: to=%v to_profile=%s to_store=%s store=%s", r.To, r.ToProfile, r.ToStore, r.Store)
	}
	if r.Key != comms.Key(comms.KindTurn, f.child.ID, "a0") {
		t.Fatalf("key must be the transcript uuid: %s", r.Key)
	}
	if r.LatencyMS < 20 || r.TRecord == 0 || r.TSignal == 0 || r.Bytes != len(r.Text) || r.TH == "" {
		t.Fatalf("measurement fields: %+v", r)
	}
	if got, _ := ReadCommsSpool(f.child.ID); len(got) != 0 {
		t.Fatalf("spool not drained: %+v", got)
	}

	// The same Stop observed again (hook re-fire) is one record, not two.
	spoolTurn(t, CommsSpoolEntry{Harness: "claude", Event: "Stop", Instance: f.child.ID, Text: "Starting 13 lanes.", TranscriptPath: f.transcript})
	f.d.ingestCommsSpool("default", f.byID)
	if got := f.ledgerRecords(t); len(got) != 1 {
		t.Fatalf("re-fire produced %d records", len(got))
	}

	// A background turn: info, carried with its text, in the same ledger.
	f.appendTurn(t, fxTaskNotification("u1"), fxAssistantText("a2", "Lane C merged; verifier running."))
	spoolTurn(t, CommsSpoolEntry{Harness: "claude", Event: "Stop", Instance: f.child.ID, Text: "Lane C merged; verifier running.", TranscriptPath: f.transcript})
	f.d.ingestCommsSpool("default", f.byID)
	got := f.ledgerRecords(t)
	if len(got) != 2 || got[1].Tier != comms.TierInfo || got[1].Trigger != TurnTriggerTask || got[1].Seq != 2 {
		t.Fatalf("background turn: %+v", got)
	}
}

func TestCommsIngest_CodexTurnTakesTriggerFromThePromptEdge(t *testing.T) {
	f := newCommsFixture(t)
	// A tagged send started the turn; the codex notify carries the reply.
	spoolTurn(t, CommsSpoolEntry{Harness: "codex", Event: "agent-turn-complete", Instance: f.codex.ID, SessionID: "thread-1", TurnID: "turn-1",
		Text: "Done: tests green.", Prompt: "[agent-deck from:" + f.parent.ID + "] run the tests"})
	f.d.ingestCommsSpool("default", f.byID)
	recs := f.ledgerRecords(t)
	if len(recs) != 1 {
		t.Fatalf("records: %+v", recs)
	}
	r := recs[0]
	if r.Tool != "codex" || r.Trigger != TurnTriggerSend || r.ReplyTo != f.parent.ID || r.Tier != comms.TierInfo || r.Text != "Done: tests green." {
		t.Fatalf("codex send reply: %+v", r)
	}
	if r.Key != comms.Key(comms.KindTurn, f.codex.ID, "codex", "thread-1:turn-1") {
		t.Fatalf("codex key must use the thread and turn id: %s", r.Key)
	}

	// A prompt-start edge followed by a turn end (Gemini-style pairing).
	spoolTurn(t, CommsSpoolEntry{Harness: "codex", Event: "x", Edge: CommsEdgePromptStart, Instance: f.codex.ID, Prompt: "[HEARTBEAT] anything new?"})
	spoolTurn(t, CommsSpoolEntry{Harness: "codex", Event: "agent-turn-complete", Instance: f.codex.ID, SessionID: "thread-1", TurnID: "turn-2", Text: "Nothing new."})
	f.d.ingestCommsSpool("default", f.byID)
	recs = f.ledgerRecords(t)
	if len(recs) != 2 || recs[1].Trigger != TurnTriggerInbox || recs[1].Tier != comms.TierInfo {
		t.Fatalf("heartbeat-triggered turn: %+v", recs)
	}

	// No prompt seen: trigger unknown; a plain reply is info (#2478).
	spoolTurn(t, CommsSpoolEntry{Harness: "codex", Event: "agent-turn-complete", Instance: f.codex.ID, SessionID: "thread-1", TurnID: "turn-3", Text: "Something happened."})
	f.d.ingestCommsSpool("default", f.byID)
	recs = f.ledgerRecords(t)
	if len(recs) != 3 || recs[2].Trigger != TurnTriggerUnknown || recs[2].Tier != comms.TierInfo {
		t.Fatalf("unknown-trigger turn: %+v", recs[len(recs)-1])
	}

	// Repeated background text is noise: stored, tiered noise, countable.
	spoolTurn(t, CommsSpoolEntry{Harness: "codex", Event: "x", Edge: CommsEdgePromptStart, Instance: f.codex.ID, Prompt: "[HEARTBEAT] again"})
	spoolTurn(t, CommsSpoolEntry{Harness: "codex", Event: "agent-turn-complete", Instance: f.codex.ID, SessionID: "thread-1", TurnID: "turn-4", Text: "Something happened."})
	f.d.ingestCommsSpool("default", f.byID)
	recs = f.ledgerRecords(t)
	if len(recs) != 4 || recs[3].Tier != comms.TierNoise {
		t.Fatalf("repeated background text: %+v", recs[len(recs)-1])
	}

	// A completion sentinel and a question are urgent whatever started them.
	spoolTurn(t, CommsSpoolEntry{Harness: "codex", Event: "x", Edge: CommsEdgePromptStart, Instance: f.codex.ID, Prompt: "[HEARTBEAT] again"})
	spoolTurn(t, CommsSpoolEntry{Harness: "codex", Event: "agent-turn-complete", Instance: f.codex.ID, SessionID: "thread-1", TurnID: "turn-5",
		Text: "All done.\n===AGENTDECK_DONE=== status=ok summary=shipped it"})
	f.d.ingestCommsSpool("default", f.byID)
	recs = f.ledgerRecords(t)
	last := recs[len(recs)-1]
	if len(recs) != 5 || last.Tier != comms.TierUrgent || last.Done != "ok" || last.Summary != "shipped it" {
		t.Fatalf("sentinel turn: %+v", last)
	}
	spoolTurn(t, CommsSpoolEntry{Harness: "codex", Event: "x", Edge: CommsEdgePromptStart, Instance: f.codex.ID, Prompt: "<task-notification>bg</task-notification>"})
	spoolTurn(t, CommsSpoolEntry{Harness: "codex", Event: "agent-turn-complete", Instance: f.codex.ID, SessionID: "thread-1", TurnID: "turn-6", Text: "Should I retry?"})
	f.d.ingestCommsSpool("default", f.byID)
	recs = f.ledgerRecords(t)
	last = recs[len(recs)-1]
	if len(recs) != 6 || last.Tier != comms.TierUrgent || !last.Q || last.Trigger != TurnTriggerTask {
		t.Fatalf("question turn: %+v", last)
	}
	if got, _ := ReadCommsSpool(f.codex.ID); len(got) != 0 {
		t.Fatalf("spool not drained: %+v", got)
	}
}

func TestCommsIngest_OffByDefaultWritesNothing(t *testing.T) {
	f := newCommsFixture(t)
	spoolTurn(t, CommsSpoolEntry{Harness: "codex", Event: "agent-turn-complete", Instance: f.codex.ID, Text: "hello"})
	restore := SetCommsLedgerForTest(false)
	defer restore()
	f.d.ingestCommsSpool("default", f.byID)
	f.d.commsStatusEdge(f.shell, "running", "waiting", time.Now())
	if comms.Exists("default") {
		t.Fatal("ledger directory created with [comms] ledger off")
	}
	if got, _ := ReadCommsSpool(f.codex.ID); len(got) != 1 {
		t.Fatalf("spool must be left alone with the ledger off: %+v", got)
	}
}

func TestCommsIngest_UnknownInstancesAreLeftForTheirProfile(t *testing.T) {
	f := newCommsFixture(t)
	spoolTurn(t, CommsSpoolEntry{Harness: "codex", Event: "agent-turn-complete", Instance: "someone-elses-child", Text: "hello"})
	f.d.ingestCommsSpool("default", f.byID)
	if got, _ := ReadCommsSpool("someone-elses-child"); len(got) != 1 {
		t.Fatalf("foreign spool consumed: %+v", got)
	}
	if recs := f.ledgerRecords(t); len(recs) != 0 {
		t.Fatalf("foreign spool committed: %+v", recs)
	}
}

func TestCommsIngest_ShellToolGetsAStatusRecordOnce(t *testing.T) {
	f := newCommsFixture(t)
	at := time.Now()
	f.d.commsStatusEdge(f.shell, "running", "waiting", at)
	f.d.commsStatusEdge(f.shell, "running", "waiting", at.Add(time.Second)) // same edge, same (empty) signal, inside 90 s
	f.d.commsStatusEdge(f.codex, "running", "waiting", at)                  // codex has a text producer: no status edge
	f.d.ingestCommsSpool("default", f.byID)
	recs := f.ledgerRecords(t)
	if len(recs) != 1 || recs[0].Kind != comms.KindStatus || recs[0].From != f.shell.ID || recs[0].State != "waiting" || recs[0].Tool != "shell" || recs[0].Ref != "running" {
		t.Fatalf("status records: %+v", recs)
	}
	if !recs[0].IsUrgent() {
		t.Fatal("a status-only record has no tier and must count as urgent")
	}
	if entries, _ := ReadCommsSpool(f.shell.ID); len(entries) != 0 {
		t.Fatalf("status spool not drained: %+v", entries)
	}
}

func TestCommsIngest_RecordsSurviveADaemonRestart(t *testing.T) {
	f := newCommsFixture(t)
	spoolTurn(t, CommsSpoolEntry{Harness: "gemini", Event: "AfterAgent", Instance: f.codex.ID, Text: "one", Prompt: "hi"})
	entries, _ := ReadCommsSpool(f.codex.ID)
	replay, _ := os.ReadFile(entries[0].path)
	f.d.ingestCommsSpool("default", f.byID)
	f.d.closeCommsLedgers()
	if len(f.d.ledgers) != 0 {
		t.Fatal("ledgers not released")
	}
	// A fresh daemon: the SAME spool entry observed again (a retry after a
	// crash between commit and remove) is still a duplicate; the sequence
	// continues.
	d2 := &TransitionDaemon{}
	t.Cleanup(d2.closeCommsLedgers)
	if err := os.WriteFile(entries[0].path, replay, 0o600); err != nil {
		t.Fatal(err)
	}
	spoolTurn(t, CommsSpoolEntry{Harness: "gemini", Event: "AfterAgent", Instance: f.codex.ID, Text: "two", Prompt: "hi again"})
	d2.ingestCommsSpool("default", f.byID)
	recs := f.ledgerRecords(t)
	if len(recs) != 2 || recs[1].Text != "two" || recs[1].Seq != 2 {
		t.Fatalf("after restart: %+v", recs)
	}
	if entries, _ := os.ReadDir(commsSpoolInstanceDir(f.codex.ID)); len(entries) != 0 {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("spool left: %s", strings.Join(names, ","))
	}
}

func TestCommsIngest_ClaudeBacklogKeepsEveryTurnsOwnText(t *testing.T) {
	f := newCommsFixture(t)
	// Three turns finished while the daemon was down: three Stop entries, one
	// transcript whose tail describes only the last one.
	f.appendTurn(t, fxHuman("u1", "a"), fxAssistantText("a1", "T1 done."))
	spoolTurn(t, CommsSpoolEntry{Harness: "claude", Event: "Stop", Instance: f.child.ID, Text: "T1 done.", TranscriptPath: f.transcript, TSignal: 1})
	f.appendTurn(t, fxHuman("u2", "b"), fxAssistantText("a2", "T2 done."))
	spoolTurn(t, CommsSpoolEntry{Harness: "claude", Event: "Stop", Instance: f.child.ID, Text: "T2 done.", TranscriptPath: f.transcript, TSignal: 2})
	f.appendTurn(t, fxHuman("u3", "c"), fxAssistantText("a3", "T3 done."))
	spoolTurn(t, CommsSpoolEntry{Harness: "claude", Event: "Stop", Instance: f.child.ID, Text: "T3 done.", TranscriptPath: f.transcript, TSignal: 3})

	f.d.ingestCommsSpool("default", f.byID)
	recs := f.ledgerRecords(t)
	if len(recs) != 3 || recs[0].Text != "T1 done." || recs[1].Text != "T2 done." || recs[2].Text != "T3 done." {
		t.Fatalf("backlog records: %+v", recs)
	}
	// Only the turn the tail still describes is uuid-keyed and classified;
	// the older two carry their own text with the prompt-derived trigger.
	if recs[2].Key != comms.Key(comms.KindTurn, f.child.ID, "a3") || recs[2].Trigger != TurnTriggerHuman {
		t.Fatalf("tail turn: %+v", recs[2])
	}
	if recs[0].Key == comms.Key(comms.KindTurn, f.child.ID, "a3") || recs[0].Trigger != TurnTriggerUnknown {
		t.Fatalf("backlog turn: %+v", recs[0])
	}
}

func TestCommsIngest_SameTextDifferentTurnsAreTwoRecords(t *testing.T) {
	f := newCommsFixture(t)
	// Gemini has no turn id: two "Done." turns must not collapse.
	spoolTurn(t, CommsSpoolEntry{Harness: "gemini", Event: "AfterAgent", Instance: f.codex.ID, SessionID: "g1", Text: "Done.", Prompt: "do a"})
	spoolTurn(t, CommsSpoolEntry{Harness: "gemini", Event: "AfterAgent", Instance: f.codex.ID, SessionID: "g1", Text: "Done.", Prompt: "do b"})
	f.d.ingestCommsSpool("default", f.byID)
	recs := f.ledgerRecords(t)
	if len(recs) != 2 || recs[0].Key == recs[1].Key {
		t.Fatalf("same-text turns: %+v", recs)
	}
	// A new human turn with the same answer is news, not noise (a plain
	// reply is info since #2478).
	if recs[1].Tier != comms.TierInfo {
		t.Fatalf("second human-triggered turn: %+v", recs[1])
	}
}

func TestCommsIngest_FailedCommitIsRetriedWithItsTrigger(t *testing.T) {
	f := newCommsFixture(t)
	// Open the ledger, then break its directory so the commit fails.
	spoolTurn(t, CommsSpoolEntry{Harness: "codex", Event: "x", Edge: CommsEdgePromptStart, Instance: f.codex.ID, Prompt: "[agent-deck from:" + f.parent.ID + "] go"})
	spoolTurn(t, CommsSpoolEntry{Harness: "codex", Event: "agent-turn-complete", Instance: f.codex.ID, TurnID: "t1", Text: "reply"})
	l := f.d.commsLedgerFor("default")
	if l == nil {
		t.Fatal("ledger did not open")
	}
	dir, _ := comms.Dir("default")
	active := filepath.Join(dir, "active.ndjson")
	if err := os.Chmod(active, 0o400); err != nil {
		t.Fatal(err)
	}
	if !fileModesEnforced(t) {
		t.Skip("file modes not enforced in this environment")
	}
	f.d.ingestCommsSpool("default", f.byID)
	if entries, _ := ReadCommsSpool(f.codex.ID); len(entries) != 2 {
		t.Fatalf("failed commit must keep the prompt and the turn entry: %+v", entries)
	}
	if _, open := f.d.ledgers["default"]; open {
		t.Fatal("a failed ledger must be dropped so the next pass reopens it")
	}
	if err := os.Chmod(active, 0o600); err != nil {
		t.Fatal(err)
	}
	// Still inside the retry backoff: nothing happens this pass.
	f.d.ingestCommsSpool("default", f.byID)
	if recs := f.ledgerRecords(t); len(recs) != 0 {
		t.Fatalf("committed inside the backoff: %+v", recs)
	}
	// A NEW daemon object (restart) past the backoff: the prompt edge is
	// still on disk, so the retried turn keeps its trigger.
	d2 := &TransitionDaemon{}
	t.Cleanup(d2.closeCommsLedgers)
	f.d.closeCommsLedgers()
	d2.ingestCommsSpool("default", f.byID)
	f.d = d2
	recs := f.ledgerRecords(t)
	if len(recs) != 1 || recs[0].Trigger != TurnTriggerSend || recs[0].ReplyTo != f.parent.ID {
		t.Fatalf("retried turn lost its trigger: %+v", recs)
	}
	if entries, _ := ReadCommsSpool(f.codex.ID); len(entries) != 0 {
		t.Fatalf("spool not drained after the retry: %+v", entries)
	}
}

func TestCommsIngest_UsesTheParentConductorsInboxConfig(t *testing.T) {
	f := newCommsFixture(t)
	f.parent.Title = "conductor-quiet"
	off := false
	inboxConfigOverride = nil
	ClearUserConfigCache()
	cfgDir := filepath.Join(os.Getenv("HOME"), ".config", "agent-deck")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte("[conductors.quiet]\n[conductors.quiet.inbox]\nquestion_wakes = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)
	if got := ResolveInboxConfig(f.parent.Title); got.GetQuestionWakes() != off {
		t.Fatalf("config seam: question_wakes=%v", got.GetQuestionWakes())
	}
	spoolTurn(t, CommsSpoolEntry{Harness: "codex", Event: "x", Edge: CommsEdgePromptStart, Instance: f.codex.ID, Prompt: "[HEARTBEAT] news?"})
	spoolTurn(t, CommsSpoolEntry{Harness: "codex", Event: "agent-turn-complete", Instance: f.codex.ID, TurnID: "t1", Text: "Should I continue?"})
	f.d.ingestCommsSpool("default", f.byID)
	recs := f.ledgerRecords(t)
	if len(recs) != 1 || recs[0].Q || recs[0].Tier != comms.TierInfo {
		t.Fatalf("question with question_wakes=false for the parent conductor: %+v", recs)
	}
}

// G2: one event replayed 100 times is one logical record.
func TestCommsIngest_OneEventReplayedAHundredTimesIsOneRecord(t *testing.T) {
	f := newCommsFixture(t)
	spoolTurn(t, CommsSpoolEntry{Harness: "codex", Event: "agent-turn-complete", Instance: f.codex.ID, TurnID: "t1", Text: "the answer", Prompt: "ask"})
	entries, _ := ReadCommsSpool(f.codex.ID)
	replay, _ := os.ReadFile(entries[0].path)
	for i := 0; i < 100; i++ {
		if err := os.WriteFile(entries[0].path, replay, 0o600); err != nil {
			t.Fatal(err)
		}
		f.d.ingestCommsSpool("default", f.byID)
		if i%25 == 0 {
			// A restart in the middle must not forget the key.
			f.d.closeCommsLedgers()
		}
	}
	if recs := f.ledgerRecords(t); len(recs) != 1 {
		t.Fatalf("100 replays produced %d records", len(recs))
	}
}

// G3: one ingest owner per profile; a second daemon process never becomes
// a writer while the first holds the ledger.
func TestCommsIngest_SecondDaemonDoesNotOwnTheLedger(t *testing.T) {
	f := newCommsFixture(t)
	if f.d.commsLedgerFor("default") == nil {
		t.Fatal("first daemon did not open the ledger")
	}
	d2 := &TransitionDaemon{}
	t.Cleanup(d2.closeCommsLedgers)
	if d2.commsLedgerFor("default") != nil {
		t.Fatal("second daemon took the ledger while the first owns it")
	}
	spoolTurn(t, CommsSpoolEntry{Harness: "codex", Event: "agent-turn-complete", Instance: f.codex.ID, TurnID: "t1", Text: "x"})
	d2.ingestCommsSpool("default", f.byID)
	if entries, _ := ReadCommsSpool(f.codex.ID); len(entries) != 1 {
		t.Fatal("the non-owner consumed the spool")
	}
	// Ownership moves when the owner releases it (process exit releases
	// the flock the same way).
	f.d.closeCommsLedgers()
	d2.ledgerOpenFailed = nil // the failed open was a lock conflict; retry now
	if d2.commsLedgerFor("default") == nil {
		t.Fatal("second daemon could not take the released ledger")
	}
	d2.ingestCommsSpool("default", f.byID)
	if recs := f.ledgerRecords(t); len(recs) != 1 {
		t.Fatalf("new owner did not ingest: %+v", recs)
	}
}

// G4: the inbox stays the sole delivery authority. With the ledger ON and
// its disk broken, the inbox record and the wake still happen exactly as
// with the ledger off, and the spool entry stays for a retry.
func TestCommsIngest_LedgerDiskFailureNeverTouchesTheInboxPath(t *testing.T) {
	f := newCommsFixture(t)
	// A question to the parent: urgent, so the inbox path wakes it (#2478).
	f.appendTurn(t, fxHuman("u0", "run the board"), fxAssistantText("a0", "Starting 13 lanes. Which board first?"))
	spoolTurn(t, CommsSpoolEntry{Harness: "claude", Event: "Stop", Instance: f.child.ID, Text: "Starting 13 lanes. Which board first?", TranscriptPath: f.transcript})
	l := f.d.commsLedgerFor("default")
	if l == nil {
		t.Fatal("ledger did not open")
	}
	dir, _ := comms.Dir("default")
	active := filepath.Join(dir, "active.ndjson")
	if !fileModesEnforced(t) {
		t.Skip("file modes not enforced in this environment")
	}
	if err := os.Chmod(active, 0o400); err != nil {
		t.Fatal(err)
	}
	statuses := map[string]string{f.child.ID: "waiting", f.parent.ID: "waiting"}
	f.d.recordTerminalTurns("default", f.byID, statuses, nil) // the inbox path
	f.d.ingestCommsSpool("default", f.byID)                   // the ledger path, failing

	inbox := f.inboxRecords(t)
	if len(inbox) != 1 || inbox[0].Tier != TurnTierUrgent || inbox[0].Text != "Starting 13 lanes. Which board first?" || *f.sends != 1 {
		t.Fatalf("inbox path changed under a ledger failure: %+v sends=%d", inbox, *f.sends)
	}
	if entries, _ := ReadCommsSpool(f.child.ID); len(entries) != 1 {
		t.Fatal("spool entry must stay for a retry")
	}
	if recs := f.ledgerRecords(t); len(recs) != 0 {
		t.Fatalf("ledger wrote through a read-only file: %+v", recs)
	}
	_ = os.Chmod(active, 0o600)
}

// G4: with the ledger on, the inbox records and wakes of the issue #2469
// scenario are identical to the ledger-off run, and the ledger agrees with
// the inbox record for record.
func TestCommsIngest_InboxParityWithTheLedgerOn(t *testing.T) {
	run := func(t *testing.T, on bool) ([]TransitionNotificationEvent, int, []comms.Record) {
		f := newCommsFixture(t)
		t.Cleanup(SetCommsLedgerForTest(on))
		f.appendTurn(t, fxHuman("u0", "run the board"), fxAssistantText("a0", "Starting 13 lanes."))
		spoolTurn(t, CommsSpoolEntry{Harness: "claude", Event: "Stop", Instance: f.child.ID, Text: "Starting 13 lanes.", TranscriptPath: f.transcript})
		statuses := map[string]string{f.child.ID: "waiting", f.parent.ID: "waiting"}
		f.d.recordTerminalTurns("default", f.byID, statuses, nil)
		f.d.ingestCommsSpool("default", f.byID)
		f.appendTurn(t, fxTaskNotification("u1"), fxAssistantToolUse("a1"), fxToolResult("u2"), fxAssistantText("a2", "Lane C merged; verifier running."))
		spoolTurn(t, CommsSpoolEntry{Harness: "claude", Event: "Stop", Instance: f.child.ID, Text: "Lane C merged; verifier running.", TranscriptPath: f.transcript})
		for i := 0; i < 3; i++ {
			f.d.recordTerminalTurns("default", f.byID, statuses, nil)
			f.d.ingestCommsSpool("default", f.byID)
		}
		var ledger []comms.Record
		if on {
			ledger = f.ledgerRecords(t)
		}
		return f.inboxRecords(t), *f.sends, ledger
	}
	offInbox, offSends, _ := run(t, false)
	onInbox, onSends, ledger := run(t, true)
	if len(offInbox) != len(onInbox) || offSends != onSends {
		t.Fatalf("inbox differs with the ledger on: off=%d/%d on=%d/%d", len(offInbox), offSends, len(onInbox), onSends)
	}
	for i := range offInbox {
		a, b := offInbox[i], onInbox[i]
		if a.Tier != b.Tier || a.Trigger != b.Trigger || a.Text != b.Text || a.TurnUUID != b.TurnUUID || a.Seq != b.Seq {
			t.Fatalf("inbox record %d differs: off=%+v on=%+v", i, a, b)
		}
	}
	if len(ledger) != len(onInbox) {
		t.Fatalf("ledger has %d records, inbox %d", len(ledger), len(onInbox))
	}
	for i := range ledger {
		if ledger[i].Tier != onInbox[i].Tier || ledger[i].Trigger != onInbox[i].Trigger || ledger[i].Text != onInbox[i].Text ||
			ledger[i].Key != comms.Key(comms.KindTurn, onInbox[i].ChildSessionID, onInbox[i].TurnUUID) {
			t.Fatalf("ledger record %d disagrees with the inbox: %+v vs %+v", i, ledger[i], onInbox[i])
		}
	}
}

// G2 / G4: status edges are collapsed only by the inbox's content rule
// (same from->to edge; a non-empty output signal within the 2 h TTL, an
// empty one within the 90 s short window), never by a time bucket.
func TestCommsIngest_StatusEdgesCollapseByContentNotTime(t *testing.T) {
	f := newCommsFixture(t)
	at := time.Now().Add(-time.Hour)
	// A shell child has no output signal: the 90 s rule applies.
	f.d.commsStatusEdge(f.shell, "running", "waiting", at)
	f.d.commsStatusEdge(f.shell, "running", "waiting", at.Add(5*time.Minute))
	f.d.commsStatusEdge(f.shell, "running", "waiting", at.Add(30*time.Minute))
	f.d.commsStatusEdge(f.shell, "running", "waiting", at.Add(30*time.Minute+10*time.Second)) // inside 90 s: folded
	f.d.commsStatusEdge(f.shell, "waiting", "error", at.Add(31*time.Minute))                  // a different edge
	f.d.ingestCommsSpool("default", f.byID)
	recs := f.ledgerRecords(t)
	if len(recs) != 4 || recs[0].State != "waiting" || recs[1].State != "waiting" || recs[2].State != "waiting" || recs[3].State != "error" || recs[3].Ref != "waiting" {
		t.Fatalf("status records: %+v", recs)
	}
	// Gemini has no text producer in P1: status-only, same rule.
	gem := NewInstanceWithTool("gem", t.TempDir(), "gemini")
	gem.ID = "gem-1"
	gem.ParentSessionID = f.parent.ID
	f.byID[gem.ID] = gem
	f.d.commsStatusEdge(gem, "running", "waiting", at)
	f.d.ingestCommsSpool("default", f.byID)
	if recs := f.ledgerRecords(t); len(recs) != 5 || recs[4].Tool != "gemini" {
		t.Fatalf("gemini status record: %+v", recs)
	}
}

// G4: with the ledger on, a status-only child's inbox record and wake are
// unchanged even when the ledger disk is broken; the edge waits in the
// spool. Driven through emitTurn's legacy branch, the production wiring.
func TestCommsIngest_StatusOnlyChildInboxPathUnchangedUnderLedgerFailure(t *testing.T) {
	f := newCommsFixture(t)
	if !fileModesEnforced(t) {
		t.Skip("file modes not enforced in this environment")
	}
	gem := NewInstanceWithTool("gem", t.TempDir(), "gemini")
	gem.ID = "gem-2"
	gem.ParentSessionID = f.parent.ID
	gem.Status = StatusWaiting
	f.byID[gem.ID] = gem
	saveCommsFixtureInstances(t, gem)
	if f.d.commsLedgerFor("default") == nil {
		t.Fatal("ledger did not open")
	}
	dir, _ := comms.Dir("default")
	active := filepath.Join(dir, "active.ndjson")
	if err := os.Chmod(active, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(active, 0o600) })
	result, ok := f.d.emitTurn("default", gem, f.byID, "running", "waiting", time.Now(), true)
	if !ok || result.DeliveryResult == transitionDeliveryFailed {
		t.Fatalf("inbox path under ledger failure: ok=%v result=%+v", ok, result)
	}
	inbox := f.inboxRecords(t)
	if len(inbox) != 1 || inbox[0].ChildSessionID != gem.ID || *f.sends != 1 {
		t.Fatalf("inbox record / wake changed: %+v sends=%d", inbox, *f.sends)
	}
	f.d.ingestCommsSpool("default", f.byID)
	if entries, _ := ReadCommsSpool(gem.ID); len(entries) != 1 || entries[0].Edge != CommsEdgeStatus {
		t.Fatalf("status edge must wait in the spool: %+v", entries)
	}
	if recs := f.ledgerRecords(t); len(recs) != 0 {
		t.Fatalf("ledger wrote through a read-only file: %+v", recs)
	}
}

// G3 / G5: same identity with different content is a conflict: quarantined,
// never a second record and never silently dropped.
func TestCommsIngest_ConflictingIdentityIsQuarantined(t *testing.T) {
	f := newCommsFixture(t)
	spoolTurn(t, CommsSpoolEntry{Harness: "codex", Event: "agent-turn-complete", Instance: f.codex.ID, SessionID: "th", TurnID: "t1", Text: "first answer", Prompt: "ask"})
	f.d.ingestCommsSpool("default", f.byID)
	spoolTurn(t, CommsSpoolEntry{Harness: "codex", Event: "agent-turn-complete", Instance: f.codex.ID, SessionID: "th", TurnID: "t1", Text: "a different answer", Prompt: "ask"})
	f.d.ingestCommsSpool("default", f.byID)
	if recs := f.ledgerRecords(t); len(recs) != 1 || recs[0].Text != "first answer" {
		t.Fatalf("conflict produced a record: %+v", recs)
	}
	if entries, _ := ReadCommsSpool(f.codex.ID); len(entries) != 0 {
		t.Fatalf("conflicting entry left in the spool: %+v", entries)
	}
	q, _ := os.ReadDir(filepath.Join(CommsSpoolDir(), "conflict", f.codex.ID))
	if len(q) != 1 {
		t.Fatalf("conflicting entry not quarantined: %v", q)
	}
}

// G4: the production wiring. A Claude child, a Codex child and a shell
// child go through the inbox path and the ledger ingest with the switch on;
// the inbox records are what they were, the ledger holds one record per
// edge, and shutdown releases the ledger and its lock.
func TestCommsIngest_ProductionWiringAndShutdown(t *testing.T) {
	f := newCommsFixture(t)
	f.appendTurn(t, fxHuman("u0", "go"), fxAssistantText("a0", "Claude done."))
	spoolTurn(t, CommsSpoolEntry{Harness: "claude", Event: "Stop", Instance: f.child.ID, Text: "Claude done.", TranscriptPath: f.transcript})
	spoolTurn(t, CommsSpoolEntry{Harness: "codex", Event: "agent-turn-complete", Instance: f.codex.ID, SessionID: "th", TurnID: "t9", Text: "Codex done.", Prompt: "go"})
	statuses := map[string]string{f.child.ID: "waiting", f.codex.ID: "waiting", f.shell.ID: "waiting", f.parent.ID: "waiting"}
	f.d.recordTerminalTurns("default", f.byID, statuses, nil) // inbox path for all three
	f.d.ingestCommsSpool("default", f.byID)                   // ledger path
	inbox := f.inboxRecords(t)
	if len(inbox) != 3 {
		t.Fatalf("inbox records: %+v", inbox)
	}
	recs := f.ledgerRecords(t)
	kinds := map[string]int{}
	for _, r := range recs {
		kinds[r.Kind+":"+r.Tool]++
	}
	if len(recs) != 3 || kinds["turn:claude"] != 1 || kinds["turn:codex"] != 1 || kinds["status:shell"] != 1 {
		t.Fatalf("ledger records: %v %+v", kinds, recs)
	}
	dir, _ := comms.Dir("default")
	f.d.shutdown()
	if len(f.d.ledgers) != 0 {
		t.Fatal("shutdown left a ledger open")
	}
	d2 := &TransitionDaemon{}
	t.Cleanup(d2.closeCommsLedgers)
	if d2.commsLedgerFor("default") == nil {
		t.Fatalf("daemon.lock not released at shutdown (%s)", dir)
	}
}

// Two same-text Claude turns less than the old 5 s skew apart are two
// records: only the tail turn takes the transcript identity.
func TestCommsIngest_SameTextTurnsSecondsApartStayDistinct(t *testing.T) {
	f := newCommsFixture(t)
	base := time.Now().Add(-time.Minute)
	f.appendTurn(t, fxHuman("u1", "a"), fxAssistantTextAt("a1", "ok", base))
	spoolTurn(t, CommsSpoolEntry{Harness: "claude", Event: "Stop", Instance: f.child.ID, Text: "ok", TranscriptPath: f.transcript, TSignal: base.UnixMilli() + 50})
	f.appendTurn(t, fxHuman("u2", "b"), fxAssistantTextAt("a2", "ok", base.Add(3*time.Second)))
	spoolTurn(t, CommsSpoolEntry{Harness: "claude", Event: "Stop", Instance: f.child.ID, Text: "ok", TranscriptPath: f.transcript, TSignal: base.Add(3*time.Second).UnixMilli() + 50})
	f.d.ingestCommsSpool("default", f.byID)
	recs := f.ledgerRecords(t)
	if len(recs) != 2 || recs[1].Key != comms.Key(comms.KindTurn, f.child.ID, "a2") || recs[0].Key == recs[1].Key {
		t.Fatalf("turns 3 s apart: %+v", recs)
	}
}

// A long Codex reply keeps its sentinel and question on the hook path and
// its record hash is the full-text hash.
func TestCommsIngest_LongCodexReplyKeepsSentinelQuestionAndFullHash(t *testing.T) {
	f := newCommsFixture(t)
	long := strings.Repeat("progress line\n", 200) + "===AGENTDECK_DONE=== status=ok summary=all green"
	if len(long) <= MaxTurnTextBytes {
		t.Fatal("fixture must exceed the record cap")
	}
	spoolTurn(t, CommsSpoolEntry{Harness: "codex", Event: "agent-turn-complete", Instance: f.codex.ID, SessionID: "th", TurnID: "t1", Text: long, Prompt: "[HEARTBEAT] news?"})
	question := strings.Repeat("detail\n", 400) + "Should I continue with plan B?"
	spoolTurn(t, CommsSpoolEntry{Harness: "codex", Event: "agent-turn-complete", Instance: f.codex.ID, SessionID: "th", TurnID: "t2", Text: question, Prompt: "[HEARTBEAT] news?"})
	f.d.ingestCommsSpool("default", f.byID)
	recs := f.ledgerRecords(t)
	if len(recs) != 2 {
		t.Fatalf("records: %d", len(recs))
	}
	if recs[0].Done != "ok" || recs[0].Summary != "all green" || recs[0].TH != turnTextHash(long) || recs[0].Tier != comms.TierUrgent || len(recs[0].Text) > MaxTurnTextBytes {
		t.Fatalf("long sentinel reply: %+v", recs[0])
	}
	if !recs[1].Q || recs[1].TH != turnTextHash(question) || recs[1].Tier != comms.TierUrgent {
		t.Fatalf("long question reply: %+v", recs[1])
	}
}

func fxAssistantTextAt(uuid, text string, at time.Time) string {
	rec := map[string]any{
		"type": "assistant", "uuid": uuid, "isSidechain": false, "timestamp": at.UTC().Format(time.RFC3339Nano),
		"message": map[string]any{"role": "assistant", "content": []map[string]any{{"type": "text", "text": text}}},
	}
	b, _ := json.Marshal(rec)
	return string(b)
}

// Round 2 item 2: three backlog turns that all say "Done." are three
// records; only the one the tail describes is uuid-keyed, and a re-fired
// Stop for that tail turn is still a duplicate.
func TestCommsIngest_SameTextClaudeBacklogIsThreeRecords(t *testing.T) {
	f := newCommsFixture(t)
	base := time.Now().Add(-time.Minute)
	f.appendTurn(t, fxHuman("u1", "a"), fxAssistantTextAt("a1", "Done.", base))
	spoolTurn(t, CommsSpoolEntry{Harness: "claude", Event: "Stop", Instance: f.child.ID, Text: "Done.", TranscriptPath: f.transcript, TSignal: base.UnixMilli() + 100})
	f.appendTurn(t, fxHuman("u2", "b"), fxAssistantTextAt("a2", "Done.", base.Add(10*time.Second)))
	spoolTurn(t, CommsSpoolEntry{Harness: "claude", Event: "Stop", Instance: f.child.ID, Text: "Done.", TranscriptPath: f.transcript, TSignal: base.Add(10*time.Second).UnixMilli() + 100})
	f.appendTurn(t, fxHuman("u3", "c"), fxAssistantTextAt("a3", "Done.", base.Add(20*time.Second)))
	spoolTurn(t, CommsSpoolEntry{Harness: "claude", Event: "Stop", Instance: f.child.ID, Text: "Done.", TranscriptPath: f.transcript, TSignal: base.Add(20*time.Second).UnixMilli() + 100})
	f.d.ingestCommsSpool("default", f.byID)
	recs := f.ledgerRecords(t)
	if len(recs) != 3 {
		t.Fatalf("same-text backlog: %d records %+v", len(recs), recs)
	}
	tailKey := comms.Key(comms.KindTurn, f.child.ID, "a3")
	if recs[2].Key != tailKey || recs[0].Key == tailKey || recs[1].Key == tailKey || recs[0].Key == recs[1].Key {
		t.Fatalf("keys: %s %s %s", recs[0].Key, recs[1].Key, recs[2].Key)
	}
	// A hook re-fire for the tail turn is one record, not four.
	spoolTurn(t, CommsSpoolEntry{Harness: "claude", Event: "Stop", Instance: f.child.ID, Text: "Done.", TranscriptPath: f.transcript, TSignal: base.Add(21 * time.Second).UnixMilli()})
	f.d.ingestCommsSpool("default", f.byID)
	if recs := f.ledgerRecords(t); len(recs) != 3 {
		t.Fatalf("re-fired Stop produced a record: %d", len(recs))
	}
}

// Round 2 item 1: a reply longer than the spool cap still matches its
// transcript turn (the spool carries the full-text hash), so it keeps the
// transcript identity, trigger and the sentinel at its end.
func TestCommsIngest_LongClaudeReplyStillMatchesItsTranscriptTurn(t *testing.T) {
	f := newCommsFixture(t)
	long := strings.Repeat("progress line\n", 1400) + "===AGENTDECK_DONE=== status=ok summary=all green"
	if len(long) <= commsSpoolTextBytes {
		t.Fatalf("fixture must exceed the spool cap, got %d", len(long))
	}
	f.appendTurn(t, fxTaskNotification("u1"), fxAssistantText("a1", long))
	spoolTurn(t, CommsSpoolEntry{Harness: "claude", Event: "Stop", Instance: f.child.ID, Text: long, TranscriptPath: f.transcript})
	f.d.ingestCommsSpool("default", f.byID)
	recs := f.ledgerRecords(t)
	if len(recs) != 1 {
		t.Fatalf("records: %+v", recs)
	}
	r := recs[0]
	if r.Key != comms.Key(comms.KindTurn, f.child.ID, "a1") || r.Trigger != TurnTriggerTask || r.Done != "ok" || r.Summary != "all green" || r.Tier != comms.TierUrgent {
		t.Fatalf("long reply: %+v", r)
	}
	if len(r.Text) > MaxTurnTextBytes || r.TH != turnTextHash(long) {
		t.Fatalf("record text cap / hash: %d %s", len(r.Text), r.TH)
	}
	spoolTurn(t, CommsSpoolEntry{Harness: "claude", Event: "Stop", Instance: f.child.ID, Text: long, TranscriptPath: f.transcript})
	f.d.ingestCommsSpool("default", f.byID)
	if recs := f.ledgerRecords(t); len(recs) != 1 {
		t.Fatalf("re-fired long Stop duplicated: %d", len(recs))
	}
}

// Verifier defect 6 (G2/G3): a status record's identity is its spool entry
// id, so a replay (a crash after the commit, before the spool file was
// removed) is a duplicate however much later it comes and whatever was
// committed in between: replay safety never depends on a time window.
func TestCommsIngest_StatusEdgeReplayIsADuplicateOfItsSpoolID(t *testing.T) {
	f := newCommsFixture(t)
	at := time.Now().Add(-time.Hour)
	f.d.commsStatusEdge(f.shell, "running", "waiting", at)
	entries, err := ReadCommsSpool(f.shell.ID)
	if err != nil || len(entries) != 1 {
		t.Fatalf("spool: %+v %v", entries, err)
	}
	first := entries[0]
	raw, err := os.ReadFile(first.path)
	if err != nil {
		t.Fatal(err)
	}
	f.d.commsStatusEdge(f.shell, "waiting", "running", at.Add(3*time.Hour))
	f.d.ingestCommsSpool("default", f.byID)
	recs := f.ledgerRecords(t)
	if len(recs) != 2 || recs[0].Key != comms.Key(comms.KindStatus, f.shell.ID, first.ID()) {
		t.Fatalf("status records must be keyed on the spool id: %+v", recs)
	}
	// The crash: the first entry's file is back after both committed.
	if err := os.WriteFile(first.path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	f.d.ingestCommsSpool("default", f.byID)
	if recs := f.ledgerRecords(t); len(recs) != 2 {
		t.Fatalf("a replayed status edge became a second record: %+v", recs)
	}
	if left, _ := ReadCommsSpool(f.shell.ID); len(left) != 0 {
		t.Fatalf("replayed entry left in the spool: %+v", left)
	}
}

// Verifier round 1: a replayed status entry whose content no longer
// matches its stored record (the child was re-parented in between) is a
// conflict: quarantined like a turn, never a stuck spool that blocks every
// later edge.
func TestCommsIngest_ConflictingStatusReplayIsQuarantined(t *testing.T) {
	f := newCommsFixture(t)
	at := time.Now().Add(-time.Hour)
	f.d.commsStatusEdge(f.shell, "running", "waiting", at)
	entries, _ := ReadCommsSpool(f.shell.ID)
	if len(entries) != 1 {
		t.Fatalf("spool: %+v", entries)
	}
	first := entries[0]
	raw, err := os.ReadFile(first.path)
	if err != nil {
		t.Fatal(err)
	}
	f.d.commsStatusEdge(f.shell, "waiting", "running", at.Add(time.Minute))
	f.d.ingestCommsSpool("default", f.byID)
	if err := os.WriteFile(first.path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	f.shell.ParentSessionID = "another-parent"
	f.d.ingestCommsSpool("default", f.byID)
	f.d.commsStatusEdge(f.shell, "running", "error", at.Add(2*time.Minute))
	f.d.ingestCommsSpool("default", f.byID)
	recs := f.ledgerRecords(t)
	if len(recs) != 3 || recs[2].State != "error" {
		t.Fatalf("a later edge must still commit: %+v", recs)
	}
	if left, _ := ReadCommsSpool(f.shell.ID); len(left) != 0 {
		t.Fatalf("spool stuck behind the conflict: %+v", left)
	}
	if q, _ := os.ReadDir(filepath.Join(CommsSpoolDir(), "conflict", f.shell.ID)); len(q) != 1 {
		t.Fatalf("conflicting status entry not quarantined: %d files", len(q))
	}
}
