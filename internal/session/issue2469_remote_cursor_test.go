package session

// Incremental remote talkback (issue #2469 family, PR3): the export ships only
// what is newer than the conductor's cursor, the cursor advances only once a
// whole batch landed, an old remote falls back to the full export, an
// ingested urgent record wakes the conductor once, and the daemon's scheduler
// backs off and turns a dead link into ONE urgent record.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func cursorTestHome(t *testing.T) {
	t.Helper()
	reviewTestHome(t, "default")
	inboxConfigOverride = nil
	t.Cleanup(func() { inboxConfigOverride = nil })
}

func journalTurn(t *testing.T, child, tier, text string, at time.Time) TurnJournalEntry {
	t.Helper()
	e, err := AppendTurnJournal(TurnJournalEntry{
		TS: at, Child: child, Profile: "default", Status: "waiting", Tier: tier,
		Trigger: TurnTriggerSend, UUID: child + "-" + text, TextHash: "h-" + text, Text: text,
	}, 0)
	if err != nil {
		t.Fatalf("append journal: %v", err)
	}
	return e
}

func TestIssue2469PR3_ExportAfterReturnsOnlyNewerLinesAndStableCursor(t *testing.T) {
	cursorTestHome(t)
	base := time.Now().Add(-10 * time.Minute)
	journalTurn(t, "w1", TurnTierInfo, "one", base)
	journalTurn(t, "w1", TurnTierUrgent, "two", base.Add(time.Second))
	if _, err := AppendTurnJournal(TurnJournalEntry{TS: base.Add(2 * time.Second), Child: "w1", Profile: "default",
		Status: "waiting", Tier: TurnTierUrgent, UUID: "w1-done", Text: "all done", DoneStatus: "ok", DoneSummary: "shipped"}, 0); err != nil {
		t.Fatal(err)
	}
	// Another parent's pending inbox must NOT cross with --after.
	if err := WriteInboxEvent("someone-else", TransitionNotificationEvent{ChildSessionID: "other-child",
		FromStatus: "running", ToStatus: "waiting", Timestamp: base}); err != nil {
		t.Fatal(err)
	}
	// A child with no journal (old producer) still reports its completion.
	if err := WriteLedgerEntry(CompletionLedgerEntry{ChildID: "legacy-w", Profile: "default", Status: "ok",
		Summary: "legacy done", FinishedAt: base.Add(3 * time.Second)}); err != nil {
		t.Fatal(err)
	}

	first, err := ExportRecordsAfter(DefaultProfile, RemoteCursor{})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(first.Records) != 4 {
		t.Fatalf("want 3 journal lines + 1 legacy ledger record, got %d: %+v", len(first.Records), first.Records)
	}
	for _, ev := range first.Records {
		if ev.ChildSessionID == "other-child" {
			t.Fatal("another parent's inbox was exported with --after")
		}
	}
	two := first.Records[1]
	if two.Tier != TurnTierUrgent || two.Text != "two" || two.Seq != 2 || two.Trigger != TurnTriggerSend {
		t.Fatalf("journal line not rendered with tier/text/seq: %+v", two)
	}
	if done := first.Records[2]; done.Kind != transitionKindFinished || done.DoneStatus != "ok" || done.Seq != 3 {
		t.Fatalf("a done line must be a finished record: %+v", done)
	}
	if first.CursorNext.Seqs["w1"] != 3 || !first.CursorNext.TS.Equal(base.Add(3*time.Second)) {
		t.Fatalf("cursor_next wrong: %+v", first.CursorNext)
	}

	// Round trip through the wire form, exactly as --after receives it.
	raw, _ := json.Marshal(first.CursorNext)
	cursor, err := ParseRemoteCursor(string(raw))
	if err != nil {
		t.Fatalf("parse cursor %s: %v", raw, err)
	}
	if !strings.Contains(string(raw), `"_ts"`) || cursor.Seqs["w1"] != 3 {
		t.Fatalf("wire form wrong: %s -> %+v", raw, cursor)
	}

	journalTurn(t, "w1", TurnTierUrgent, "four", base.Add(4*time.Second))
	second, err := ExportRecordsAfter(DefaultProfile, cursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Records) != 1 || second.Records[0].Seq != 4 || second.Records[0].Text != "four" {
		t.Fatalf("want only seq 4, got %+v", second.Records)
	}
	third, err := ExportRecordsAfter(DefaultProfile, second.CursorNext)
	if err != nil {
		t.Fatal(err)
	}
	if len(third.Records) != 0 {
		t.Fatalf("an up-to-date cursor must fetch nothing, got %+v", third.Records)
	}
	a, _ := json.Marshal(second.CursorNext)
	b, _ := json.Marshal(third.CursorNext)
	if string(a) != string(b) {
		t.Fatalf("cursor_next not stable: %s vs %s", a, b)
	}
}

// A journaled child's completion is mirrored to the ledger with the same
// turn; it crosses once, and the next drain does not re-ship the ledger copy.
func TestIssue2469PR3_ExportAfterLedgerCopyOfJournaledDoneCrossesOnce(t *testing.T) {
	cursorTestHome(t)
	at := time.Now().Add(-time.Minute)
	if _, err := AppendTurnJournal(TurnJournalEntry{TS: at, Child: "w7", Profile: "default", Status: "waiting",
		Tier: TurnTierUrgent, UUID: "u7", DoneStatus: "ok", DoneSummary: "built"}, 0); err != nil {
		t.Fatal(err)
	}
	if err := WriteLedgerEntry(CompletionLedgerEntry{ChildID: "w7", Profile: "default", Status: "ok",
		Summary: "built", FinishedAt: at}); err != nil {
		t.Fatal(err)
	}
	first, err := ExportRecordsAfter(DefaultProfile, RemoteCursor{})
	if err != nil || len(first.Records) != 1 || first.Records[0].Seq != 1 {
		t.Fatalf("want the journal line only: %+v %v", first.Records, err)
	}
	second, err := ExportRecordsAfter(DefaultProfile, first.CursorNext)
	if err != nil || len(second.Records) != 0 {
		t.Fatalf("the ledger copy was re-shipped: %+v %v", second.Records, err)
	}
}

// A journal removed and recreated restarts its seqs below the cursor; the
// export must not go silent for that child forever.
func TestIssue2469PR3_ExportAfterJournalResetBelowCursorShipsTail(t *testing.T) {
	cursorTestHome(t)
	journalTurn(t, "w2", TurnTierUrgent, "fresh", time.Now().Add(-time.Minute))
	exp, err := ExportRecordsAfter(DefaultProfile, RemoteCursor{Seqs: map[string]int64{"w2": 40}})
	if err != nil {
		t.Fatal(err)
	}
	if len(exp.Records) != 1 || exp.CursorNext.Seqs["w2"] != 1 {
		t.Fatalf("reset journal lost: %+v next=%+v", exp.Records, exp.CursorNext)
	}
}

func TestIssue2469PR3_ParseRemoteCursorRejectsGarbage(t *testing.T) {
	for _, bad := range []string{"not json", `{"w1":"x"}`, `{"_ts":"yesterday"}`, `[1]`} {
		if _, err := ParseRemoteCursor(bad); err == nil {
			t.Fatalf("cursor %q accepted", bad)
		}
	}
	c, err := ParseRemoteCursor(`{"w1":2,"_future":"meta"}`)
	if err != nil || c.Seqs["w1"] != 2 {
		t.Fatalf("unknown _ key must be ignored: %+v %v", c, err)
	}
}

// localTalkbackDeps drains this test host into itself: the export is the
// real incremental export, the writer is alive.
func localTalkbackDeps(fetches *[]RemoteCursor) RemoteTalkbackDeps {
	return RemoteTalkbackDeps{
		RemoteProfile: DefaultProfile,
		FetchAfter: func(_ context.Context, c RemoteCursor) (RemoteExport, error) {
			*fetches = append(*fetches, c)
			exp, err := ExportRecordsAfter(DefaultProfile, c)
			exp.Writer = &WriterStatus{Running: true}
			return exp, err
		},
		FetchAll: func(context.Context) ([]TransitionNotificationEvent, error) {
			return nil, errors.New("legacy not expected")
		},
		WriterProbe: func(context.Context) (WriterStatus, error) { return WriterStatus{}, errors.New("probe not expected") },
	}
}

func TestIssue2469PR3_SecondDrainFetchesNothingNew(t *testing.T) {
	cursorTestHome(t)
	journalTurn(t, "w3", TurnTierUrgent, "hello", time.Now().Add(-time.Minute))
	var fetches []RemoteCursor
	deps := localTalkbackDeps(&fetches)

	res, err := RunRemoteTalkback(context.Background(), "boxb", "conductor-a", deps)
	if err != nil {
		t.Fatalf("drain 1: %v", err)
	}
	if res.Written != 1 || res.Legacy || res.CursorAfter == nil || res.CursorAfter.Seqs["w3"] != 1 {
		t.Fatalf("drain 1 result wrong: %+v", res)
	}
	saved, found, err := LoadRemoteCursor("boxb", "conductor-a")
	if err != nil || !found || saved.Seqs["w3"] != 1 {
		t.Fatalf("cursor not persisted: %+v found=%v err=%v", saved, found, err)
	}
	pending, _ := ReadInboxEvents("conductor-a")
	if len(pending) != 1 || pending[0].ChildSessionID != "boxb:w3" || pending[0].Tier != TurnTierUrgent || pending[0].Text != "hello" {
		t.Fatalf("ingested record lost its scope/tier/text: %+v", pending)
	}

	res, err = RunRemoteTalkback(context.Background(), "boxb", "conductor-a", deps)
	if err != nil {
		t.Fatalf("drain 2: %v", err)
	}
	if len(res.Stored) != 0 || res.Written != 0 {
		t.Fatalf("second drain re-shipped records: %+v", res)
	}
	if len(fetches) != 2 || fetches[1].Seqs["w3"] != 1 {
		t.Fatalf("second drain did not send the saved cursor: %+v", fetches)
	}
}

func TestIssue2469PR3_PartialBatchDoesNotAdvanceCursor(t *testing.T) {
	cursorTestHome(t)
	at := time.Now().Add(-time.Minute)
	for _, text := range []string{"a", "b", "c"} {
		journalTurn(t, "w4", TurnTierInfo, text, at)
	}
	var fetches []RemoteCursor
	deps := localTalkbackDeps(&fetches)

	calls := 0
	remoteIngestWrite = func(parent string, ev TransitionNotificationEvent) (InboxEventPresence, error) {
		calls++
		if calls == 2 {
			return InboxEventPresenceUnknown, errors.New("disk full")
		}
		return WriteInboxEventIfUnseen(parent, ev)
	}
	t.Cleanup(func() { remoteIngestWrite = WriteInboxEventIfUnseen })

	_, err := RunRemoteTalkback(context.Background(), "boxb", "conductor-p", deps)
	var te *RemoteTalkbackError
	if !errors.As(err, &te) || te.Stage != RemoteTalkbackStageIngest {
		t.Fatalf("want ingest failure, got %v", err)
	}
	if _, found, _ := LoadRemoteCursor("boxb", "conductor-p"); found {
		t.Fatal("cursor advanced past a batch whose write failed")
	}

	// An unknown (unconfirmed) write pins the cursor too, without failing.
	calls = 0
	remoteIngestWrite = func(parent string, ev TransitionNotificationEvent) (InboxEventPresence, error) {
		calls++
		if calls == 3 {
			return InboxEventPresenceUnknown, nil
		}
		return WriteInboxEventIfUnseen(parent, ev)
	}
	res, err := RunRemoteTalkback(context.Background(), "boxb", "conductor-p", deps)
	if err != nil || res.Unknown != 1 {
		t.Fatalf("unknown write: res=%+v err=%v", res, err)
	}
	if _, found, _ := LoadRemoteCursor("boxb", "conductor-p"); found {
		t.Fatal("cursor advanced past an unconfirmed record")
	}

	// Healthy again: the refetch lands, dedup absorbs the overlap, cursor moves.
	remoteIngestWrite = WriteInboxEventIfUnseen
	res, err = RunRemoteTalkback(context.Background(), "boxb", "conductor-p", deps)
	if err != nil {
		t.Fatal(err)
	}
	if res.Written != 1 || res.Duplicates != 2 || res.CursorAfter.Seqs["w4"] != 3 {
		t.Fatalf("refetch after a pinned cursor wrong: %+v", res)
	}
	if fetches[len(fetches)-1].Seqs["w4"] != 0 {
		t.Fatalf("pinned drains must resend the old cursor, sent %+v", fetches[len(fetches)-1])
	}
	pending, _ := ReadInboxEvents("conductor-p")
	if len(pending) != 3 {
		t.Fatalf("want each turn exactly once, got %d", len(pending))
	}
}

func TestIssue2469PR3_OldRemoteFallsBackToFullExport(t *testing.T) {
	cursorTestHome(t)
	r := &SSHRunner{Host: "worker@box-b", AgentDeckPath: "agent-deck"}
	var calls []string
	SetSSHRunnerRunFnForTest(r, func(args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		calls = append(calls, joined)
		switch {
		case strings.Contains(joined, "--after"):
			return nil, errors.New("ssh command failed: exit status 1: flag provided but not defined: -after")
		case joined == "inbox export --json --profile default":
			return []byte(`[{"child_session_id":"w5","profile":"default","kind":"finished","done_status":"ok","done_summary":"built",` +
				`"timestamp":"` + time.Now().Add(-time.Minute).UTC().Format(time.RFC3339) + `"}]`), nil
		case joined == "inbox writer-status --json":
			return []byte(`{"running":true,"detail":"ok"}`), nil
		}
		return nil, errors.New("unexpected " + joined)
	})
	deps := RemoteTalkbackDeps{RemoteProfile: DefaultProfile, FetchAfter: r.FetchRecordsAfter, FetchAll: r.FetchPendingRecords, WriterProbe: r.FetchWriterStatus}
	res, err := RunRemoteTalkback(context.Background(), "boxb", "conductor-old", deps)
	if err != nil {
		t.Fatalf("fallback drain: %v (calls %v)", err, calls)
	}
	if !res.Legacy || res.Written != 1 || res.CursorAfter != nil {
		t.Fatalf("want a legacy drain with no cursor: %+v", res)
	}
	if c, found, _ := LoadRemoteCursor("boxb", "conductor-old"); !found || !c.Legacy || len(c.Seqs) != 0 || len(c.Ledger) != 0 {
		t.Fatalf("a legacy drain keeps a position-less _legacy cursor (enrollment only): %+v found=%v", c, found)
	}
	if len(calls) != 3 {
		t.Fatalf("want --after attempt, full export, writer probe; got %v", calls)
	}

	// A remote that knows --after answers export + writer in ONE round trip.
	calls = nil
	SetSSHRunnerRunFnForTest(r, func(args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		return []byte(`{"records":[],"cursor_next":{"w5":2},"writer":{"running":true,"detail":"ok"}}`), nil
	})
	res, err = RunRemoteTalkback(context.Background(), "boxb", "conductor-old", deps)
	if err != nil || res.Legacy || len(calls) != 1 || !strings.Contains(calls[0], "--with-writer") {
		t.Fatalf("incremental drain not one round trip: res=%+v err=%v calls=%v", res, err, calls)
	}

	// A transport failure is a failed drain, never a silent legacy fallback.
	SetSSHRunnerRunFnForTest(r, func(args ...string) ([]byte, error) {
		return nil, errors.New("ssh command failed: connection refused")
	})
	if _, err := RunRemoteTalkback(context.Background(), "boxb", "conductor-old", deps); err == nil {
		t.Fatal("unreachable remote reported success")
	}
}

func TestIssue2469PR3_IngestedUrgentRecordWakesOnce(t *testing.T) {
	cursorTestHome(t)
	var mu sync.Mutex
	var sent []string
	remoteWakeWiring = func() *wakeNudgeWiring {
		return &wakeNudgeWiring{
			nudger: NewWakeNudger(0),
			now:    time.Now,
			isIdle: func(*Instance, string) bool { return true },
			send: func(_ *Instance, _ string, msg string) error {
				mu.Lock()
				defer mu.Unlock()
				sent = append(sent, msg)
				return nil
			},
		}
	}
	t.Cleanup(func() {
		remoteWakeWiring = func() *wakeNudgeWiring {
			w := defaultWakeNudgeWiring()
			w.send = func(parent *Instance, profile, message string) error {
				return sendWakeNudgeNoWait(profile, parent.ID, message)
			}
			return w
		}
	})
	parent := NewInstance("conductor-wake", t.TempDir())
	parent.ID = "conductor-wake"

	at := time.Now().Add(-time.Minute)
	journalTurn(t, "w6", TurnTierUrgent, "first-urgent", at)
	journalTurn(t, "w6", TurnTierInfo, "progress", at)
	journalTurn(t, "w6", TurnTierUrgent, "need-input", at)
	var fetches []RemoteCursor
	deps := localTalkbackDeps(&fetches)
	deps.Parent = func() (*Instance, string) { return parent, "default" }

	res, err := RunRemoteTalkback(context.Background(), "boxb", parent.ID, deps)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Woke || len(sent) != 1 || !strings.Contains(sent[0], "boxb:w6") {
		t.Fatalf("want exactly one wake naming the remote child, got woke=%v sent=%v", res.Woke, sent)
	}

	// Nothing new: no wake.
	if res, _ = RunRemoteTalkback(context.Background(), "boxb", parent.ID, deps); res.Woke || len(sent) != 1 {
		t.Fatalf("a drain with nothing fresh woke the parent: %v", sent)
	}
	// Info only: never wakes (the prompt-time drain delivers it).
	journalTurn(t, "w6", TurnTierInfo, "more progress", at)
	if res, _ = RunRemoteTalkback(context.Background(), "boxb", parent.ID, deps); res.Woke || len(sent) != 1 || res.Written != 1 {
		t.Fatalf("an info record woke the parent: res=%+v sent=%v", res, sent)
	}
	stats, _ := ReadInboxStats(parent.ID)
	if stats.WakeupsUrgent != 2 || stats.WakeupsSuppressed != 2 {
		t.Fatalf("stats not counted like local records: %+v", stats)
	}
}

type talkbackClock struct{ now time.Time }

func newTestTalkbackScheduler(clock *talkbackClock, parent *Instance, drain func() error) *remoteTalkbackScheduler {
	return &remoteTalkbackScheduler{
		state: map[string]*remoteTalkbackState{},
		now:   func() time.Time { return clock.now },
		remotes: func() map[string]RemoteConfig {
			return map[string]RemoteConfig{"boxb": {Host: "w@b", TalkbackIntervalSecs: 30}}
		},
		targets: func(string) ([]remoteTalkbackTarget, error) {
			return []remoteTalkbackTarget{{Parent: parent, Profile: "default"}}, nil
		},
		drain: func(context.Context, string, RemoteConfig, remoteTalkbackTarget) error { return drain() },
		alert: func(remote string, t remoteTalkbackTarget, text string, start time.Time) {
			writeTalkbackAlert(remote, t, text, start, nil)
		},
	}
}

func TestIssue2469PR3_SchedulerBackoffAndOneUrgentAlert(t *testing.T) {
	cursorTestHome(t)
	parent := NewInstance("conductor-sched", t.TempDir())
	parent.ID = "conductor-sched"
	clock := &talkbackClock{now: time.Now()}
	start := clock.now
	calls := 0
	fail := true
	s := newTestTalkbackScheduler(clock, parent, func() error {
		calls++
		if fail {
			return errors.New("ssh: connect to host b: Connection refused")
		}
		return nil
	})
	at := func(d time.Duration) {
		clock.now = start.Add(d)
		s.tick(context.Background())
		s.wg.Wait()
	}
	alerts := func() []TransitionNotificationEvent {
		evs, _ := ReadInboxEvents(parent.ID)
		return evs
	}

	at(0) // failure 1, retry after 60 s
	at(30 * time.Second)
	if calls != 1 {
		t.Fatalf("backoff ignored: %d calls", calls)
	}
	at(60 * time.Second)  // failure 2, retry after 120 s
	at(179 * time.Second) // still backing off
	if calls != 2 || len(alerts()) != 0 {
		t.Fatalf("calls=%d alerts=%d after two failures", calls, len(alerts()))
	}
	at(180 * time.Second) // failure 3: the alert
	got := alerts()
	if calls != 3 || len(got) != 1 {
		t.Fatalf("want one alert after 3 failures, calls=%d alerts=%+v", calls, got)
	}
	if got[0].Tier != TurnTierUrgent || got[0].Trigger != TurnTriggerSystem ||
		!strings.Contains(got[0].Text, "remote boxb: talkback failing for 3 min: ssh: connect to host b") {
		t.Fatalf("alert record wrong: %+v", got[0])
	}
	at(420 * time.Second) // failure 4 (backoff 240 s): no second alert
	at(900 * time.Second) // failure 5 (backoff 480 s)
	if calls != 5 || len(alerts()) != 1 {
		t.Fatalf("calls=%d alerts=%d: a streak alerts once", calls, len(alerts()))
	}
	if st := s.state["boxb"]; st.nextAt.Sub(clock.now) != remoteTalkbackBackoffMax {
		t.Fatalf("backoff not capped at 10 min: %v", st.nextAt.Sub(clock.now))
	}

	fail = false
	at(1500 * time.Second) // success clears the streak; normal interval again
	if st := s.state["boxb"]; st.failures != 0 || st.nextAt.Sub(clock.now) != 30*time.Second {
		t.Fatalf("success did not reset: %+v", st)
	}
	fail = true
	at(1530 * time.Second)
	at(1590 * time.Second)
	at(1710 * time.Second)
	if len(alerts()) != 2 {
		t.Fatalf("a new failure streak must alert again, got %d", len(alerts()))
	}
}

func TestIssue2469PR3_SchedulerSkipsTickWhileDrainInFlight(t *testing.T) {
	cursorTestHome(t)
	parent := NewInstance("conductor-busy", t.TempDir())
	parent.ID = "conductor-busy"
	clock := &talkbackClock{now: time.Now()}
	release := make(chan struct{})
	var mu sync.Mutex
	calls := 0
	s := newTestTalkbackScheduler(clock, parent, func() error {
		mu.Lock()
		calls++
		mu.Unlock()
		<-release
		return nil
	})
	s.tick(context.Background())
	clock.now = clock.now.Add(time.Hour)
	s.tick(context.Background())
	s.tick(context.Background())
	close(release)
	s.wg.Wait()
	if calls != 1 {
		t.Fatalf("a tick started a second drain while one was in flight: %d", calls)
	}
}
