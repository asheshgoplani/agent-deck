package session

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// NOTE: piWriter below is a MODEL of what hook-handler writes for each pi event, not
// the handler itself. The model is bridged to the real thing from both sides: the
// handler's own tests (cmd/agent-deck/hook_handler_pi_turn_id_test.go) pin what the
// real handler writes for the same sequences, and the comms matrix
// (tests/comms_matrix, scenario pi-child-two-turns) drives the real extension
// events through the real handler and daemon. A change to what the handler writes
// must change piWriter and those tests together.
//
// A pi child's later turns must wake its parent. transitionContentSignal gave pi
// no turn signal, so TurnFingerprint fell back to the constant "flip|running>
// waiting" and every run after the first was skipped as a known turn.
//
// A pi inbox turn (as in PR-A's ledger): the extension's
// agent_settled MERGES the run's identity into the status file turn_end wrote
// (pi_settle_turn_id, pi_settle_session_id, pi_settled_at), with no status edge.
// For a writer known to settle (the capability bit `settles`, sent by v4+
// extensions on turn_start and turn_end) a turn_end that has not settled is a
// status edge only: it commits nothing, however old, and each settle identity is
// one record. For a writer without the bit (an old extension, a pre-upgrade file)
// main's legacy waiting wake is unchanged. Every file below is written by
// piWriter, which refuses a sequence pi never produces.
//
// File keys and HookStatus fields assumed here: `settles` / Settles, `pi_run_id`
// / PiRunID (the run token the extension mints at agent_start), `pi_settle_*` /
// PiSettleTurnID, PiSettleSessionID, PiSettledAt.

// piWriter writes an instance's hook status file as hook-handler does for each
// event of a real pi run: session_start (waiting); per LLM response turn_start
// (running) then turn_end (waiting); and agent_settled, which merges the run's
// identity into the turn_end file without touching its status. It refuses an
// order pi does not produce: a turn_end without a turn_start before it, two
// turn_starts in a row, a settle that does not follow a turn_end, or a settle for
// another run.
type piWriter struct {
	t       *testing.T
	id      string
	settles bool // the writer sends the capability bit
	last    string
	run     string
	file    map[string]any
	at      time.Time // the instant of the next event
}

func newPiWriter(t *testing.T, id string, settles bool) *piWriter {
	return &piWriter{t: t, id: id, settles: settles, at: time.Now()}
}

// Age puts the next events d in the past.
func (w *piWriter) Age(d time.Duration) *piWriter { w.at = time.Now().Add(-d); return w }

func (w *piWriter) flush() {
	w.t.Helper()
	data, err := json.Marshal(w.file)
	if err != nil {
		w.t.Fatal(err)
	}
	if err := os.MkdirAll(GetHooksDir(), 0o755); err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(GetHooksDir(), w.id+".json"), data, 0o644); err != nil {
		w.t.Fatal(err)
	}
}

func (w *piWriter) write(event, status, run string) {
	w.t.Helper()
	w.at = w.at.Add(time.Millisecond) // events are ordered
	prev := w.file
	w.file = map[string]any{"status": status, "session_id": "pi-sess-1", "event": event,
		"ts": w.at.Unix(), "received_at_ns": w.at.UnixNano()}
	// As hook-handler does, on EVERY write: the list of previous runs the file held
	// is carried forward (pi_orphan_runs, at most four, oldest dropped), and a
	// turn_start or lifecycle write adds the previous run, settled or not.
	var orphans []map[string]any
	if prev != nil {
		if list, ok := prev["pi_orphan_runs"].([]map[string]any); ok {
			orphans = append(orphans, list...)
		}
		if (event == "turn_start" || event == "session_start" || event == "session_shutdown") && prev["settles"] == true &&
			prev["pi_run_id"] != nil && prev["pi_run_id"] != run {
			// The previous run is carried whether or not it settled: the daemon may
			// not have polled it yet. The entry records whether it settled.
			entry := map[string]any{"run_id": prev["pi_run_id"], "settled": prev["pi_settle_turn_id"] != nil}
			if prev["event"] == "turn_end" {
				entry["turn_end_ns"] = prev["received_at_ns"]
			}
			orphans = append(orphans, entry)
		}
		for len(orphans) > 4 {
			orphans = orphans[1:]
		}
	}
	if len(orphans) > 0 {
		w.file["pi_orphan_runs"] = orphans
	}
	if w.settles && run != "" {
		w.file["settles"] = true
		w.file["pi_run_id"] = run
	}
	w.last, w.run = event, run
	w.flush()
}

func (w *piWriter) SessionStart() *piWriter { w.write("session_start", "waiting", ""); return w }

// SessionShutdown is pi's session_shutdown (exit, /new, /resume, /fork, /reload):
// the status is dead. Like session_start it rebuilds the file, so it must carry
// the unsettled runs forward.
func (w *piWriter) SessionShutdown() *piWriter { w.write("session_shutdown", "dead", ""); return w }

func (w *piWriter) TurnStart(run string) *piWriter {
	w.t.Helper()
	if w.last == "turn_start" {
		w.t.Fatal("test sequence pi never produces: two turn_starts in a row")
	}
	w.write("turn_start", "running", run)
	return w
}

// TurnStartAfterAbort is the next run's turn_start when the previous run was
// aborted before its turn_end (pi emits no turn_end for it).
func (w *piWriter) TurnStartAfterAbort(run string) *piWriter {
	w.t.Helper()
	if w.last != "turn_start" {
		w.t.Fatalf("test sequence: TurnStartAfterAbort follows an aborted turn_start, not %q", w.last)
	}
	w.write("turn_start", "running", run)
	return w
}

func (w *piWriter) TurnEnd(run string) *piWriter {
	w.t.Helper()
	if w.last != "turn_start" {
		w.t.Fatalf("test sequence pi never produces: turn_end after %q (a turn_end follows its turn_start)", w.last)
	}
	w.write("turn_end", "waiting", run)
	return w
}

// Settle is the run's agent_settled: it merges into the turn_end file.
func (w *piWriter) Settle(run, turnID string) *piWriter {
	w.t.Helper()
	if w.last != "turn_end" || w.file["pi_settle_turn_id"] != nil {
		w.t.Fatalf("test sequence pi never produces: a settle after %q", w.last)
	}
	if w.settles && run != w.run {
		w.t.Fatalf("test sequence: a settle for run %q merged into run %q's turn_end", run, w.run)
	}
	w.file["pi_settle_session_id"] = "pi-sess-1"
	w.file["pi_settle_turn_id"] = turnID
	w.file["pi_settled_at"] = w.at.UnixMilli()
	w.flush()
	return w
}

// Run is one whole settled run.
func (w *piWriter) Run(run, turnID string) *piWriter {
	return w.TurnStart(run).TurnEnd(run).Settle(run, turnID)
}

// piChild is the fixture's child turned into a pi session.
func piChild(f *turnTestFixture) *Instance {
	f.child.Tool = "pi"
	f.child.ClaudeSessionID = ""
	return f.child
}

func (f *turnTestFixture) piPoll() {
	f.d.recordTerminalTurns("default", f.byID, map[string]string{f.child.ID: "waiting", f.parent.ID: "waiting"}, nil)
}

func (f *turnTestFixture) wantRecords(t *testing.T, what string, records, wakes int) {
	t.Helper()
	if got := f.inboxRecords(t); len(got) != records || *f.sends != wakes {
		t.Fatalf("%s: %d inbox records, %d wakes, want %d and %d", what, len(got), *f.sends, records, wakes)
	}
}

// piSettleEvent is the transition the notifier builds for the child: the
// producer's own signal as its LastOutputHash.
func piSettleEvent(f *turnTestFixture) TransitionNotificationEvent {
	return TransitionNotificationEvent{ChildSessionID: f.child.ID, ChildTitle: f.child.Title, Profile: "default",
		FromStatus: "running", ToStatus: "waiting", Tier: TurnTierUrgent, Text: "run finished", Timestamp: time.Now(),
		LastOutputHash: transitionEventOutputHash(f.child)}
}

// HookStatus surfaces the settle fields, the capability bit and the run token.
// Read by reflection so a missing field fails the test, not the build.
func TestPiTurnFingerprint_HookStatusSurfacesTheSettleFields(t *testing.T) {
	f := newTurnTestFixture(t)
	piChild(f)
	newPiWriter(t, f.child.ID, true).Run("run-A", "entry-77")
	hs := readHookStatusFile(f.child.ID)
	if hs == nil {
		t.Fatal("no hook status")
	}
	check := func(who string, status *HookStatus) {
		v := reflect.ValueOf(status).Elem()
		str := func(name string) string {
			fv := v.FieldByName(name)
			if !fv.IsValid() {
				t.Fatalf("%s: HookStatus has no %s", who, name)
			}
			return fv.String()
		}
		if str("PiSettleTurnID") != "entry-77" || str("PiSettleSessionID") != "pi-sess-1" || str("PiRunID") != "run-A" {
			t.Fatalf("%s: settle/run fields %q %q %q", who, str("PiSettleTurnID"), str("PiSettleSessionID"), str("PiRunID"))
		}
		if at, s := v.FieldByName("PiSettledAt"), v.FieldByName("Settles"); !at.IsValid() || at.IsZero() || !s.IsValid() || !s.Bool() {
			t.Fatalf("%s: PiSettledAt / Settles not surfaced (%v / %v)", who, at, s)
		}
		if status.Event != "turn_end" || status.Status != "waiting" {
			t.Fatalf("%s: the settle changed the status: %q %q", who, status.Event, status.Status)
		}
	}
	check("daemon reader", hs)
	w, err := NewStatusFileWatcher(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	w.loadExisting() // the watcher loads existing files only in Start
	ws := w.GetHookStatus(f.child.ID)
	if ws == nil {
		t.Fatal("the watcher did not load the status file")
	}
	check("watcher", ws)
}

// Two runs that differ only in the settle's turn id are two turns: distinct
// signals and fingerprints.
func TestPiTurnFingerprint_TwoRunsHaveDistinctSignals(t *testing.T) {
	f := newTurnTestFixture(t)
	piChild(f)
	w := newPiWriter(t, f.child.ID, true)
	w.Run("run-A", "entry-1")
	first := piSettleEvent(f)
	w.Run("run-B", "entry-2")
	second := piSettleEvent(f)
	if first.LastOutputHash == "" || second.LastOutputHash == "" {
		t.Fatalf("a settled pi turn_end has no turn signal: %q %q", first.LastOutputHash, second.LastOutputHash)
	}
	if first.LastOutputHash == second.LastOutputHash || TurnFingerprint(first) == TurnFingerprint(second) {
		t.Fatalf("two turn ids share a signal %q / fingerprint %s", first.LastOutputHash, TurnFingerprint(first))
	}
}

// The same settle seen twice is one turn: the signal and the fingerprint repeat.
func TestPiTurnFingerprint_ASettleReplayKeepsItsFingerprint(t *testing.T) {
	f := newTurnTestFixture(t)
	piChild(f)
	newPiWriter(t, f.child.ID, true).Run("run-A", "entry-1")
	first, replay := piSettleEvent(f), piSettleEvent(f) // the daemon observes the same file twice
	if first.LastOutputHash == "" || TurnFingerprint(first) != TurnFingerprint(replay) {
		t.Fatalf("a replay changed the fingerprint: %q %s != %s", first.LastOutputHash, TurnFingerprint(first), TurnFingerprint(replay))
	}
}

// Two runs are two inbox records and two wakes, through the daemon's real poll.
func TestPiSettle_TwoRunsAreTwoInboxRecordsAndTwoWakes(t *testing.T) {
	f := newTurnTestFixture(t)
	piChild(f)
	w := newPiWriter(t, f.child.ID, true)
	w.TurnStart("run-A")
	f.piPoll()
	w.TurnEnd("run-A").Settle("run-A", "entry-1")
	f.piPoll()
	f.wantRecords(t, "first run", 1, 1)
	f.piPoll() // a replayed observation of the same run
	f.wantRecords(t, "a replayed poll", 1, 1)
	w.TurnStart("run-B")
	f.piPoll()
	w.TurnEnd("run-B").Settle("run-B", "entry-2")
	f.piPoll()
	f.wantRecords(t, "second run (skipped as a known turn?)", 2, 2)
}

// A mid-run turn_end (a tool-using run flaps turn_start/turn_end per LLM
// response) commits nothing; the run's settle commits it once.
func TestPiSettle_AMidRunTurnEndCommitsNothing(t *testing.T) {
	f := newTurnTestFixture(t)
	piChild(f)
	w := newPiWriter(t, f.child.ID, true)
	w.TurnStart("run-A").TurnEnd("run-A")
	f.piPoll()
	f.piPoll()
	f.wantRecords(t, "the first response's turn_end", 0, 0)
	w.TurnStart("run-A").TurnEnd("run-A") // the second LLM response of the same run
	f.piPoll()
	f.wantRecords(t, "the second response's turn_end", 0, 0)
	w.Settle("run-A", "entry-1")
	f.piPoll()
	f.wantRecords(t, "after the settle", 1, 1)
}

// Only a settling writer's waiting/idle turn_end is held. A session_start waiting
// (a restart or boot) and an error are not held: they emit as they do today.
func TestPiSettle_AnErrorOrASessionStartWaitingIsNotHeld(t *testing.T) {
	for name, tc := range map[string]struct {
		seq func(w *piWriter)
		to  string
	}{
		"session_start waiting":  {func(w *piWriter) { w.SessionStart() }, "waiting"},
		"error after a turn_end": {func(w *piWriter) { w.TurnStart("run-A").TurnEnd("run-A") }, "error"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newTurnTestFixture(t)
			piChild(f)
			tc.seq(newPiWriter(t, f.child.ID, true))
			f.d.recordTerminalTurns("default", f.byID, map[string]string{f.child.ID: tc.to, f.parent.ID: "waiting"}, nil)
			if got := f.inboxRecords(t); len(got) != 1 {
				t.Fatalf("%s was held: %d inbox records, want 1 (today's behaviour)", name, len(got))
			}
		})
	}
}

// Claude and Codex fingerprints do not move. Pinned values computed on main
// before the change; any difference re-keys every consumed-turn ledger.
func TestPiTurnFingerprint_ClaudeAndCodexFingerprintsAreUnchanged(t *testing.T) {
	base := TransitionNotificationEvent{ChildSessionID: "child-1", FromStatus: "running", ToStatus: "waiting"}
	for name, tc := range map[string]struct {
		hash string
		want string
	}{
		"claude transcript turn": {"turn:3f9c0d6e-1111-4222-8333-444455556666", "child-1@6101536119d89160"},
		"codex completion":       {"codex-completion:7:abc", "child-1@c02b701dc3f93bb5"},
		"no signal (flip)":       {"", "child-1@2830a2d1a8a1fb7f"},
	} {
		ev := base
		ev.LastOutputHash = tc.hash
		if got := TurnFingerprint(ev); got != tc.want {
			t.Errorf("%s: fingerprint %s, want %s", name, got, tc.want)
		}
	}
}

// ---- a pi inbox turn IS its settle ----------------------------------------------

// piOld is comfortably older than any plausible wait for a settle.
const piOld = 30 * time.Second

// A daemon restart between the turn_end and its settle gives exactly one record
// after the settle, and a later restart does not emit the consumed run again.
func TestPiSettle_ARestartBetweenTurnEndAndSettleGivesExactlyOneRecord(t *testing.T) {
	f := newTurnTestFixture(t)
	piChild(f)
	w := newPiWriter(t, f.child.ID, true)
	w.TurnStart("run-A").TurnEnd("run-A")
	f.piPoll()
	f.restartDaemon()
	f.piPoll()
	f.wantRecords(t, "an unsettled turn_end across a restart", 0, 0)
	w.Settle("run-A", "entry-1")
	f.restartDaemon()
	f.piPoll()
	f.piPoll()
	f.wantRecords(t, "after the settle and a restart", 1, 1)
	if _, err := DrainInboxForParent(f.parent.ID); err != nil {
		t.Fatal(err)
	}
	f.restartDaemon()
	f.piPoll()
	f.wantRecords(t, "a restart after the parent drained the run", 0, 1)
}

// restartDaemon is a fresh daemon over the same notifier and registry: its
// per-pass bookkeeping is gone, as after a restart.
func (f *turnTestFixture) restartDaemon() {
	f.d = &TransitionDaemon{
		notifier:      f.d.notifier,
		lastStatus:    map[string]map[string]string{},
		initialized:   map[string]bool{},
		lastDone:      map[string]map[string]DoneSignal{},
		lastTurn:      map[string]map[string]string{},
		lastDoneScan:  map[string]map[string]time.Time{},
		turnLiveCheck: func(*Instance) bool { return true },
	}
}

// piCandidate runs the hook-candidate emit path (the one SyncOnce takes for a
// turn_end the status file shows) on a daemon with no memory of earlier passes.
func (f *turnTestFixture) piCandidate() {
	f.restartDaemon()
	running := map[string]string{f.child.ID: "running", f.parent.ID: "waiting"}
	cand := map[string]hookTransitionCandidate{f.child.ID: {ToStatus: "waiting", Timestamp: time.Now(), Event: "turn_end"}}
	f.d.emitHookTransitionCandidates("default", f.byID, running, running, cand)
}

// Two settled runs inside the 90 s legacy window are two records, on the poll
// path and on the candidate path.
func TestPiSettle_TwoSettledRunsWithin90SecondsAreTwoRecords(t *testing.T) {
	f := newTurnTestFixture(t)
	piChild(f)
	w := newPiWriter(t, f.child.ID, true)
	w.Run("run-A", "entry-1")
	f.piPoll()
	w.Run("run-B", "entry-2")
	f.piPoll()
	f.wantRecords(t, "poll path, two settled runs seconds apart", 2, 2)
	w.Run("run-C", "entry-3")
	f.piCandidate()
	if got := f.inboxRecords(t); len(got) != 3 {
		t.Fatalf("candidate path: %d records, want 3", len(got))
	}
}

// ---- round 4 ----------------------------------------------------------------------

// A writer that does not send the capability bit (an old extension, a file from
// before the upgrade) is not known to settle: origin/main's legacy waiting wake is
// kept exactly: a waiting turn_end is one record, and a later run with the same
// constant fingerprint is skipped as a known turn.
func TestPiSettle_AWriterWithoutTheSettlesBitKeepsMainsWaitingWake(t *testing.T) {
	f := newTurnTestFixture(t)
	piChild(f)
	w := newPiWriter(t, f.child.ID, false) // no `settles` bit in its files
	w.TurnStart("")
	f.piPoll()
	w.TurnEnd("")
	f.piPoll()
	f.wantRecords(t, "a waiting turn_end from a writer that cannot settle", 1, 1)
	w.TurnStart("")
	f.piPoll()
	w.TurnEnd("")
	f.piPoll()
	f.wantRecords(t, "main's constant fingerprint (the later run is a known turn)", 1, 1)
}

// With the bit, the same sequence is settle-only (nothing on waiting).
func TestPiSettle_AWriterWithTheSettlesBitCommitsNothingOnWaiting(t *testing.T) {
	f := newTurnTestFixture(t)
	piChild(f)
	newPiWriter(t, f.child.ID, true).TurnStart("run-A").TurnEnd("run-A")
	f.piPoll()
	f.wantRecords(t, "a waiting turn_end from a writer that settles", 0, 0)
}

// A pi session_start waiting (a restart's empty-signal record) must not swallow
// the next real settle inside the 90 s legacy window.
func TestPiSettle_ASessionStartRecordDoesNotSwallowTheNextSettle(t *testing.T) {
	f := newTurnTestFixture(t)
	piChild(f)
	w := newPiWriter(t, f.child.ID, true)
	w.SessionStart()
	f.piPoll()
	f.wantRecords(t, "the session_start waiting", 1, 1)
	w.Run("run-A", "entry-1")
	f.piPoll()
	f.wantRecords(t, "the settle within 90 s of the session_start (swallowed by the empty-hash collapse?)", 2, 2)
}

// ---- end to end: a fresh daemon's SyncOnce over a real pi child ------------------

// A pi child with a live tmux pane, its real event sequence in the status file, and
// a fresh NewTransitionDaemon().SyncOnce per observation: each settle is one record
// and one wake, an unsettled turn_end none, and a replay none.
func TestPiSettle_ASyncOncePerObservationOnAFreshDaemonGivesOneRecordPerSettle(t *testing.T) {
	skipIfNoTmuxBinary(t)
	f := newTurnTestFixture(t)
	piChild(f)
	if err := f.child.tmuxSession.Start("exec sleep 3600"); err != nil {
		t.Fatalf("tmux start: %v", err)
	}
	t.Cleanup(func() { _ = f.child.tmuxSession.Kill() })
	f.child.Status = StatusWaiting
	storage, err := NewStorageWithProfile("default")
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	if err := storage.SaveWithGroups([]*Instance{f.parent, f.child}, nil); err != nil {
		t.Fatal(err)
	}
	sends := 0
	prevExec := wakeNudgeExec
	wakeNudgeExec = func(context.Context, string, ...string) error { sends++; return nil }
	t.Cleanup(func() { wakeNudgeExec = prevExec })
	tmux.RefreshExistingSessions()

	sync := func() { NewTransitionDaemon().SyncOnce(context.Background()) }
	w := newPiWriter(t, f.child.ID, true)
	w.TurnStart("run-A").TurnEnd("run-A")
	sync()
	if got := f.inboxRecords(t); len(got) != 0 {
		t.Fatalf("an unsettled turn_end: %d records through SyncOnce", len(got))
	}
	w.Settle("run-A", "entry-1")
	sync()
	sync() // a fresh daemon observing the same settle again
	if got := f.inboxRecords(t); len(got) != 1 {
		t.Fatalf("one settle through SyncOnce: %d records, want 1", len(got))
	}
	w.TurnStart("run-B").TurnEnd("run-B").Settle("run-B", "entry-2")
	sync()
	if got := f.inboxRecords(t); len(got) != 2 {
		t.Fatalf("two settles through SyncOnce on fresh daemons: %d records, want 2", len(got))
	}
}

// ---- round 5: an inbox turn is keyed by RUN, with a bounded fallback ----------------
// The identity of a settles-capable pi run is `pi-run:<sha(session:run_id)>`, used
// by the settle and by the fallback alike, through the one persisted
// recordTerminalTurns key. The settle arriving emits the run once; no settle
// within the 10 s grace (from the turn_end write) emits the same key once; a late
// settle finds the key seen, also across a restart. A run's record is therefore
// never worse than origin/main's and there is never a second one.

// piFresh is a run that has only just ended, well inside the 10 s grace.
const piFresh = time.Second

// The settle never arrives: inside the grace the turn_end is held; after it the run
// is exactly one record and one wake. (Round 5 pinned the loss of a held run A when a
// run B followed it at once: that expectation is replaced. A held run is carried
// forward by the next turn_start and gets its own record.)
func TestPiSettle_ASettleThatNeverArrivesIsExactlyOneRecordAfterTheGrace(t *testing.T) {
	f := newTurnTestFixture(t)
	piChild(f)
	w := newPiWriter(t, f.child.ID, true).Age(piOld)
	w.TurnStart("run-A")
	f.piPoll()
	w.TurnEnd("run-A")
	f.piPoll()
	f.piPoll()
	f.wantRecords(t, "after the grace (one record per run, observed three times)", 1, 1)
	young := newPiWriter(t, f.child.ID, true).Age(piFresh)
	young.TurnStart("run-B").TurnEnd("run-B")
	f.piPoll()
	f.wantRecords(t, "a second run inside its grace", 1, 1)
}

// An unsettled run A followed at once by run B keeps its record: A is carried
// forward by B's turn_start, emitted once under its own key, and B proceeds on its
// own (here by the fallback).
func TestPiSettle_AHeldRunThatIsFollowedAtOnceByAnotherStillGetsItsRecord(t *testing.T) {
	f := newTurnTestFixture(t)
	piChild(f)
	w := newPiWriter(t, f.child.ID, true).Age(piFresh)
	w.TurnStart("run-A").TurnEnd("run-A")
	f.piPoll()
	f.wantRecords(t, "run A held inside its grace", 0, 0)
	w.Age(piOld).TurnStart("run-B") // B starts: A's turn_end is carried forward
	f.piPoll()
	f.wantRecords(t, "run A's record after B's turn_start (the file no longer holds A)", 1, 1)
	w.TurnEnd("run-B")
	f.piPoll()
	f.piPoll()
	f.wantRecords(t, "run B's fallback, and A not emitted again", 2, 2)
}

// A is aborted before its turn_end (pi emits none) and B starts within 10 s: A is
// one record, B is its own.
func TestPiSettle_AnAbortedRunFollowedWithinTheGraceKeepsItsRecord(t *testing.T) {
	f := newTurnTestFixture(t)
	piChild(f)
	w := newPiWriter(t, f.child.ID, true).Age(piFresh)
	w.TurnStart("run-A")
	f.piPoll()
	f.wantRecords(t, "run A held", 0, 0)
	w.TurnStartAfterAbort("run-B")
	f.piPoll()
	f.piPoll()
	f.wantRecords(t, "run A's record after B started (once, whatever the polls)", 1, 1)
	w.TurnEnd("run-B").Settle("run-B", "entry-B")
	f.piPoll()
	f.wantRecords(t, "run B settled: its own record", 2, 2)
	f.restartDaemon()
	f.piPoll()
	f.wantRecords(t, "a restart (A's persisted key)", 2, 2)
}

// An old extension (no settles bit): an empty turn_end record and a later settle
// collapse into ONE record, exactly as on main (the isDuplicate empty-hash collapse
// is exempted for pi-run signals only).
func TestPiSettle_AnOldExtensionsTurnEndAndLaterSettleAreOneRecord(t *testing.T) {
	f := newTurnTestFixture(t)
	piChild(f)
	w := newPiWriter(t, f.child.ID, false) // no bit, no run token
	w.TurnStart("").TurnEnd("")
	f.piPoll()
	f.wantRecords(t, "the legacy waiting record", 1, 1)
	w.Settle("", "entry-1")
	f.piPoll()
	f.wantRecords(t, "the settle after it (collapsed as on main)", 1, 1)
}

// A late settle after the fallback is no second record, nor after a daemon
// restart: the one persisted key is already seen. The parent drained the first
// record, so only the key (not a pending copy) can stop it.
func TestPiSettle_ALateSettleAfterTheFallbackIsNoSecondRecordEvenAcrossARestart(t *testing.T) {
	f := newTurnTestFixture(t)
	piChild(f)
	w := newPiWriter(t, f.child.ID, true).Age(piOld)
	w.TurnStart("run-A").TurnEnd("run-A")
	f.piPoll()
	f.wantRecords(t, "the fallback emit", 1, 1)
	if _, err := DrainInboxForParent(f.parent.ID); err != nil {
		t.Fatal(err)
	}
	w.Settle("run-A", "entry-1") // the settle, late
	f.piPoll()
	f.wantRecords(t, "the late settle", 0, 1)
	f.restartDaemon()
	f.piPoll()
	f.wantRecords(t, "the late settle on a restarted daemon", 0, 1)
}

// The same on the hook-candidate path, on a fresh daemon each time: the fallback
// is one record, its late settle and every replay none.
func TestPiSettle_TheHookCandidatePathHasTheSameKeyAndFallback(t *testing.T) {
	f := newTurnTestFixture(t)
	piChild(f)
	w := newPiWriter(t, f.child.ID, true).Age(piFresh)
	w.TurnStart("run-A").TurnEnd("run-A")
	f.piCandidate()
	f.wantRecords(t, "inside the grace", 0, 0)
	w.Age(piOld).TurnStart("run-B").TurnEnd("run-B")
	f.piCandidate()
	f.piCandidate()
	f.wantRecords(t, "the fallback, replayed on fresh daemons", 1, 1)
	w.Settle("run-B", "entry-2")
	f.piCandidate()
	f.wantRecords(t, "run B's late settle", 1, 1)
	w.Age(0).TurnStart("run-C").TurnEnd("run-C").Settle("run-C", "entry-3")
	f.piCandidate()
	f.wantRecords(t, "a settled run after it", 2, 2)
}

// A turn_start that never reaches a turn_end (the run was aborted) is held while
// fresh and, once older than the freshness bound plus the grace, is one record
// under the run's key.
func TestPiSettle_ARunAbortedBeforeItsTurnEndIsOneRecordAfterTheBound(t *testing.T) {
	f := newTurnTestFixture(t)
	piChild(f)
	w := newPiWriter(t, f.child.ID, true).Age(piFresh)
	w.TurnStart("run-A")
	f.piPoll()
	f.wantRecords(t, "a fresh turn_start", 0, 0)
	newPiWriter(t, f.child.ID, true).Age(10 * time.Minute).TurnStart("run-B")
	f.piPoll()
	f.piPoll()
	f.wantRecords(t, "an aborted run past the bound", 1, 1)
}

// Two runs are two records whatever the mix of settle and fallback.
func TestPiSettle_TwoRunsAreTwoRecordsBySettleOrFallback(t *testing.T) {
	f := newTurnTestFixture(t)
	piChild(f)
	w := newPiWriter(t, f.child.ID, true).Age(piOld)
	w.TurnStart("run-A").TurnEnd("run-A") // never settles: the fallback
	f.piPoll()
	w.Age(0).TurnStart("run-B").TurnEnd("run-B").Settle("run-B", "entry-2") // settles
	f.piPoll()
	f.wantRecords(t, "one fallback run and one settled run", 2, 2)
}

// piStatusReads counts every read of the child's status file a pass makes through
// the seam, and calls inject after the first one (the run settles mid-pass).
func piStatusReads(t *testing.T, id string, inject func()) *int {
	t.Helper()
	orig := readHookStatusForPass
	t.Cleanup(func() { readHookStatusForPass = orig })
	reads := 0
	readHookStatusForPass = func(got string) *HookStatus {
		hs := orig(got)
		if got == id {
			reads++
			if reads == 1 && inject != nil {
				inject()
			}
		}
		return hs
	}
	return &reads
}

// The same with a snapshot past its grace: the pass emits the run under its key
// from that snapshot; the settle injected mid-pass is then no second record.
func TestPiSettle_AMidPassSettleAfterTheGraceIsNoSecondRecord(t *testing.T) {
	f := newTurnTestFixture(t)
	piChild(f)
	w := newPiWriter(t, f.child.ID, true).Age(piOld)
	w.TurnStart("run-A").TurnEnd("run-A")
	reads := piStatusReads(t, f.child.ID, func() { w.Settle("run-A", "entry-1") })
	f.piPoll()
	if *reads != 1 {
		t.Errorf("the pass read the child's status %d times, want 1", *reads)
	}
	f.piPoll()
	f.wantRecords(t, "the fallback and the settle that landed mid-pass", 1, 1)
}

// ---- round 7: up to four unsettled runs are carried forward --------------------------

// A, B and C in a row, A and B never settled: every write carries the list, so A
// and B each get one record under their own key and C its own. B's turn_end must
// not drop A.
func TestPiSettle_AListOfUnsettledRunsEachGetsOneRecord(t *testing.T) {
	f := newTurnTestFixture(t)
	piChild(f)
	w := newPiWriter(t, f.child.ID, true).Age(piFresh)
	w.TurnStart("run-A").TurnEnd("run-A")
	w.TurnStart("run-B")
	f.piPoll()
	f.wantRecords(t, "run A after B started", 1, 1)
	w.TurnEnd("run-B") // the turn_end must carry A's slot forward, not drop it
	f.piPoll()
	f.wantRecords(t, "B's turn_end (A is not emitted again, nor lost)", 1, 1)
	w.TurnStart("run-C")
	f.piPoll()
	f.wantRecords(t, "run B after C started", 2, 2)
	w.TurnEnd("run-C").Settle("run-C", "entry-C")
	f.piPoll()
	f.piPoll()
	f.wantRecords(t, "A, B and C one record each", 3, 3)
}

// A fifth unsettled run pushes the oldest out of the four-entry list: it gets no
// record (a documented bound), the other four and the settled run do.
func TestPiSettle_AFifthUnsettledRunDropsTheOldest(t *testing.T) {
	f := newTurnTestFixture(t)
	piChild(f)
	w := newPiWriter(t, f.child.ID, true).Age(piFresh)
	for _, run := range []string{"run-A", "run-B", "run-C", "run-D", "run-E"} {
		w.TurnStart(run).TurnEnd(run)
	}
	w.TurnStart("run-F").TurnEnd("run-F").Settle("run-F", "entry-F")
	f.piPoll()
	f.piPoll()
	f.wantRecords(t, "B, C, D, E (A dropped) and the settled F", 5, 5)
}

// A pi-run fingerprint is immutable: the 2 h switch of a stale hash to a timestamp
// fingerprint never applies to it, so an orphan re-observed after the dedupe TTL
// is the same turn, not a new one.
func TestPiTurnFingerprint_APiRunFingerprintNeverBecomesATimestamp(t *testing.T) {
	ev := TransitionNotificationEvent{ChildSessionID: "child-1", FromStatus: "running", ToStatus: "waiting",
		LastOutputHash: "pi-run:0123456789abcdef", Timestamp: time.Now()}
	stale := ev
	stale.OutputHashStale = true
	stale.Timestamp = ev.Timestamp.Add(3 * time.Hour)
	if TurnFingerprint(ev) != TurnFingerprint(stale) {
		t.Fatalf("a stale pi-run event has fingerprint %s, want the run's own %s", TurnFingerprint(stale), TurnFingerprint(ev))
	}
	// Other signals keep the stale rule: pinned so the exemption stays narrow.
	other := ev
	other.LastOutputHash = "turn:abc"
	otherStale := other
	otherStale.OutputHashStale = true
	otherStale.Timestamp = other.Timestamp.Add(3 * time.Hour)
	if TurnFingerprint(other) == TurnFingerprint(otherStale) {
		t.Error("the stale-hash rule no longer applies to a non-pi signal")
	}
}

// Through the commit path: an orphan whose record was drained and whose notifier
// state is older than the 2 h dedupe TTL, observed again, is not committed again.
func TestPiSettle_AnOrphanReobservedAfterTheTTLIsNoSecondRecord(t *testing.T) {
	f := newTurnTestFixture(t)
	piChild(f)
	ev := TransitionNotificationEvent{ChildSessionID: f.child.ID, ChildTitle: f.child.Title, Profile: "default",
		FromStatus: "running", ToStatus: "waiting", Tier: TurnTierUrgent, Text: "run finished",
		LastOutputHash: "pi-run:0123456789abcdef", Timestamp: time.Now()}
	if committed, _, _ := f.d.notifier.commitEventToInbox(ev); !committed {
		t.Fatal("test setup: the first commit")
	}
	if _, err := DrainInboxForParent(f.parent.ID); err != nil {
		t.Fatal(err)
	}
	again := ev
	again.OutputHashStale = true // what the notifier flags once its record is past the TTL
	again.Timestamp = ev.Timestamp.Add(3 * time.Hour)
	f.d.notifier.commitEventToInbox(again)
	if got := f.inboxRecords(t); len(got) != 0 {
		t.Fatalf("an orphan re-observed after the TTL was committed again: %d records", len(got))
	}
}

// ---- round 8: lifecycle writes keep unsettled runs -----------------------------------
// session_start and session_shutdown (/new, /resume, /fork, /reload, exit) rebuild
// the status file without the run fields. They must carry the current unsettled run
// and the existing orphan list forward as every other write does. The lifecycle
// write itself still emits its own legacy record; the runs' records are the ones
// whose fingerprint is not that flip fingerprint.

func (f *turnTestFixture) runRecords(t *testing.T) int {
	t.Helper()
	flip := TurnFingerprint(TransitionNotificationEvent{ChildSessionID: f.child.ID, FromStatus: "running", ToStatus: "waiting"})
	n := 0
	for _, ev := range f.inboxRecords(t) {
		if ev.TurnFingerprint != flip {
			n++
		}
	}
	return n
}

// A run with a turn_end and no settle, then /new (session_shutdown, session_start)
// inside the fallback window: the run still gets exactly one record.
func TestPiSettle_ANewSessionInsideTheGraceKeepsAnUnsettledRun(t *testing.T) {
	for name, lifecycle := range map[string]func(w *piWriter){
		"session_start":    func(w *piWriter) { w.SessionStart() },
		"session_shutdown": func(w *piWriter) { w.SessionShutdown() },
		"shutdown then start (/new)": func(w *piWriter) {
			w.SessionShutdown().SessionStart()
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newTurnTestFixture(t)
			piChild(f)
			w := newPiWriter(t, f.child.ID, true).Age(piFresh)
			w.TurnStart("run-A").TurnEnd("run-A")
			lifecycle(w)
			f.piPoll()
			f.piPoll()
			if n := f.runRecords(t); n != 1 {
				t.Fatalf("run A has %d records after %s inside its grace, want exactly 1 (the lifecycle write erased it)", n, name)
			}
		})
	}
}

// An existing orphan list survives a session_start: A was unsettled when B started,
// B too is unsettled at /new: the carried list [A] and the current run B each have
// one record. (A settled run replaced before the daemon read it is not carried; its
// settle was the file's, and is outside this change.)
func TestPiSettle_AnOrphanListSurvivesASessionStart(t *testing.T) {
	f := newTurnTestFixture(t)
	piChild(f)
	w := newPiWriter(t, f.child.ID, true).Age(piFresh)
	w.TurnStart("run-A").TurnEnd("run-A")
	w.TurnStart("run-B").TurnEnd("run-B")
	w.SessionStart()
	f.piPoll()
	f.piPoll()
	if n := f.runRecords(t); n != 2 {
		t.Fatalf("A (in the list) and B (current, unsettled) have %d records after a session_start, want 2", n)
	}
}

// Note: the two tests above use piWriter, which models what a fixed hook-handler
// writes at a lifecycle event; the red tests are the handler's own, in cmd/agent-deck
// (TestHookHandler_PiLifecycleWrites...).

// ---- round 9: a settled run the daemon has not polled yet is carried too ---------------

// A settles, and B's turn_start lands before any poll (the daemon polls every 1-3 s):
// A's settle was the file's and is gone, but A's entry is carried, so A gets exactly
// one record under its own key, and B proceeds on its own.
func TestPiSettle_AnUnobservedSettledRunSurvivesTheNextTurnStart(t *testing.T) {
	f := newTurnTestFixture(t)
	piChild(f)
	w := newPiWriter(t, f.child.ID, true).Age(piFresh)
	w.Run("run-A", "entry-A")
	w.TurnStart("run-B") // before any poll
	f.piPoll()
	f.piPoll()
	if n := f.runRecords(t); n != 1 {
		t.Fatalf("run A (settled, then overwritten before a poll) has %d records, want exactly 1", n)
	}
	w.TurnEnd("run-B").Settle("run-B", "entry-B")
	f.piPoll()
	if n := f.runRecords(t); n != 2 {
		t.Fatalf("A and B: %d records, want 2", n)
	}
	f.restartDaemon()
	f.piPoll()
	if n := f.runRecords(t); n != 2 {
		t.Fatalf("a restart re-emitted a run: %d records, want 2", n)
	}
}

// The same when the overwrite is a session_start (/new).
func TestPiSettle_AnUnobservedSettledRunSurvivesASessionStart(t *testing.T) {
	f := newTurnTestFixture(t)
	piChild(f)
	w := newPiWriter(t, f.child.ID, true).Age(piFresh)
	w.Run("run-A", "entry-A")
	w.SessionStart() // before any poll
	f.piPoll()
	f.piPoll()
	if n := f.runRecords(t); n != 1 {
		t.Fatalf("run A (settled, then a session_start before a poll) has %d records, want exactly 1", n)
	}
}

// ---- round 10: an orphan older than the consumed-key retention ---------------------------

// An orphan entry older than the consumed-key retention (~14 days) must never emit:
// its consumed key may have been pruned, so a record for it could come back. A fresh
// daemon over a file that still carries a 15-day-old entry commits nothing for it;
// the current settled run still gets its own record.
func TestPiSettle_AnOrphanOlderThanTheConsumedKeyRetentionNeverEmits(t *testing.T) {
	f := newTurnTestFixture(t)
	piChild(f)
	w := newPiWriter(t, f.child.ID, true).Age(piFresh)
	w.Run("run-A", "entry-A")
	w.file["pi_orphan_runs"] = []map[string]any{
		{"run_id": "run-OLD", "turn_end_ns": time.Now().Add(-15 * 24 * time.Hour).UnixNano(), "settled": false},
	}
	w.flush()
	f.restartDaemon()
	f.piPoll()
	f.piPoll()
	if n := f.runRecords(t); n != 1 {
		t.Fatalf("%d run records, want 1 (run A only: the 15-day-old orphan must not emit)", n)
	}
}

// ---- one status snapshot per instance per pass ---------------------------------------

// syncOnceFixture makes the fixture's child a pi session with a live tmux pane and
// a stubbed wake exec, so NewTransitionDaemon().SyncOnce runs end to end.
func syncOnceFixture(t *testing.T, f *turnTestFixture) {
	t.Helper()
	skipIfNoTmuxBinary(t)
	if err := f.child.tmuxSession.Start("exec sleep 3600"); err != nil {
		t.Fatalf("tmux start: %v", err)
	}
	t.Cleanup(func() { _ = f.child.tmuxSession.Kill() })
	f.child.Status = StatusWaiting
	storage, err := NewStorageWithProfile("default")
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	if err := storage.SaveWithGroups([]*Instance{f.parent, f.child}, nil); err != nil {
		t.Fatal(err)
	}
	prevExec := wakeNudgeExec
	wakeNudgeExec = func(context.Context, string, ...string) error { return nil }
	t.Cleanup(func() { wakeNudgeExec = prevExec })
	tmux.RefreshExistingSessions()
}

// One HookStatus snapshot per instance per pass, shared by the hook status lookup,
// recordTerminalTurns and emitTurn: every read of the child's status goes through
// readHookStatusForPass, once. A settle injected after that read neither changes the
// pass nor is lost: the next pass sees it and emits the run once. (Consolidates the
// former SyncOnceReadsOneStatusSnapshotPerInstance, APassReadsTheStatusOnce…
// and SyncOnceReadsOnceAndAMidPassSettle… tests; each assertion is a subtest.)
func TestPiSettle_OneStatusSnapshotPerInstancePerPass(t *testing.T) {
	t.Run("poll path: one read, a mid-pass settle is not lost", func(t *testing.T) {
		f := newTurnTestFixture(t)
		piChild(f)
		w := newPiWriter(t, f.child.ID, true).Age(piFresh)
		w.TurnStart("run-A").TurnEnd("run-A")
		reads := piStatusReads(t, f.child.ID, func() { w.Settle("run-A", "entry-1") })
		f.piPoll()
		if *reads != 1 {
			t.Errorf("the pass read the child's status %d times, want 1", *reads)
		}
		f.wantRecords(t, "the pass that held its unsettled snapshot", 0, 0)
		f.piPoll()
		f.wantRecords(t, "the next pass, which sees the settle", 1, 1)
	})
	t.Run("SyncOnce: one read, a mid-pass settle is not lost", func(t *testing.T) {
		f := newTurnTestFixture(t)
		piChild(f)
		syncOnceFixture(t, f)
		w := newPiWriter(t, f.child.ID, true).Age(piFresh)
		w.TurnStart("run-A").TurnEnd("run-A")
		reads := piStatusReads(t, f.child.ID, func() { w.Settle("run-A", "entry-1") })
		NewTransitionDaemon().SyncOnce(context.Background())
		if *reads != 1 {
			t.Errorf("SyncOnce read the child's status %d times (every direct read must go through the pass's snapshot), want 1", *reads)
		}
		if got := f.inboxRecords(t); len(got) != 0 {
			t.Fatalf("the pass acted on more than its snapshot: %d records", len(got))
		}
		NewTransitionDaemon().SyncOnce(context.Background())
		if got := f.inboxRecords(t); len(got) != 1 {
			t.Fatalf("the settle injected mid-pass was lost: %d records after the next pass, want 1", len(got))
		}
	})
	t.Run("SyncOnce: a settled run is read at most once", func(t *testing.T) {
		f := newTurnTestFixture(t)
		piChild(f)
		syncOnceFixture(t, f)
		newPiWriter(t, f.child.ID, true).Run("run-A", "entry-1")
		reads := piStatusReads(t, f.child.ID, nil)
		NewTransitionDaemon().SyncOnce(context.Background())
		if *reads > 1 {
			t.Errorf("SyncOnce read the child's status snapshot %d times, want at most 1", *reads)
		}
	})
}

// ---- a writer without the settles bit is origin/main, exactly -------------------------

// A writer without the capability bit (an old extension, a pre-upgrade file) keeps
// origin/main's behaviour exactly, including when its settle merges before the poll:
// one record, under main's flip fingerprint, never a per-run or per-settle key, and
// a later run is a known turn as on main.
func TestPiSettle_AWriterWithoutTheBitMatchesMainEvenWhenItsSettleMergesFirst(t *testing.T) {
	f := newTurnTestFixture(t)
	piChild(f)
	flip := TurnFingerprint(TransitionNotificationEvent{ChildSessionID: f.child.ID, FromStatus: "running", ToStatus: "waiting"})
	w := newPiWriter(t, f.child.ID, false)            // no bit, no run token
	w.TurnStart("").TurnEnd("").Settle("", "entry-1") // the settle merges before any poll
	f.piPoll()
	got := f.inboxRecords(t)
	if len(got) != 1 || *f.sends != 1 {
		t.Fatalf("%d records, %d wakes, want main's single record and wake", len(got), *f.sends)
	}
	if got[0].TurnFingerprint != flip {
		t.Errorf("the record carries fingerprint %s, want main's flip fingerprint %s (no pi-settle key without the bit)", got[0].TurnFingerprint, flip)
	}
	w.TurnStart("").TurnEnd("").Settle("", "entry-2")
	f.piPoll()
	f.wantRecords(t, "a later settled run from the same writer (a known turn on main)", 1, 1)
}
