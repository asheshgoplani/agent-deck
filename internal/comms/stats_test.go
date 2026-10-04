package comms

import (
	"testing"
	"time"
)

// `msg stats` computes the #2482 targets from the ledger alone. Each case
// below is a row the stats must count, and the empty window must never
// read as a pass.

func TestStatsMeasuresTheIssueTargets(t *testing.T) {
	l, r, _ := openPair(t)
	now := time.Now()
	at := func(minAgo int) int64 { return now.Add(-time.Duration(minAgo) * time.Minute).UnixMilli() }
	commit := func(rec Record, minAgo int) {
		rec.TRecord = at(minAgo)
		if rec.TSignal == 0 {
			rec.TSignal = rec.TRecord
		}
		mustCommit(t, l, rec)
	}
	// Outside the window (25 h ago): ignored.
	commit(Record{Kind: KindWake, From: "agent-deck", To: []string{"P"}, Trigger: "inbox", Via: "tmux", Text: "old"}, 25*60)

	// Parent P: 6 wakes in the last 2 hours (inbox typed x4, Stop block x1,
	// ledger typed x1) and 3 re-read calls; parent R: 1 wake.
	for i := 0; i < 4; i++ {
		commit(Record{Kind: KindWake, From: "agent-deck", To: []string{"P"}, Trigger: "inbox", Via: "tmux", Text: "[INBOX] urgent"}, 10+i)
	}
	commit(Record{Kind: KindWake, From: "agent-deck", To: []string{"P"}, Trigger: "inbox", Via: "stop", Text: "block"}, 20)
	commit(Record{Kind: KindWake, From: "agent-deck", To: []string{"P"}, Trigger: "ledger", Via: "tmux", Text: "[agent-deck msg] 1"}, 30)
	commit(Record{Kind: KindWake, From: "agent-deck", To: []string{"R"}, Trigger: "inbox", Via: "tmux", Text: "x"}, 40)
	commit(Record{Kind: KindCall, From: "P", State: CallSessionOutput, Ref: "c1"}, 11)
	commit(Record{Kind: KindCall, From: "P", State: CallInboxDrain, Ref: "P"}, 12)
	commit(Record{Kind: KindCall, From: "P", State: CallSessionOutput, Ref: "c2"}, 13)
	commit(Record{Kind: KindCall, From: "P", State: CallMsgRead, Ref: "P"}, 14) // a ledger read is not a re-read

	// Child records for P: 4 turns with text (one done), 1 status without
	// text, 1 noise (excluded), and one turn committed twice under two
	// keys within 5 s (a suspect duplicate).
	commit(Record{Kind: KindTurn, From: "c1", To: []string{"P"}, Tier: TierInfo, Text: "a", Key: "k1"}, 60)
	commit(Record{Kind: KindTurn, From: "c1", To: []string{"P"}, Tier: TierUrgent, Text: "done", Done: "ok", Key: "k2"}, 61)
	commit(Record{Kind: KindTurn, From: "c2", To: []string{"P"}, Tier: TierInfo, Text: "same", Key: "k3", TSignal: at(70)}, 70)
	commit(Record{Kind: KindTurn, From: "c2", To: []string{"P"}, Tier: TierUrgent, Text: "same", Key: "k4", TSignal: at(70) + 2000}, 70)
	commit(Record{Kind: KindStatus, From: "sh", To: []string{"P"}, State: "waiting"}, 71)
	commit(Record{Kind: KindTurn, From: "c1", To: []string{"P"}, Tier: TierNoise, Text: "a", Key: "k5"}, 72)

	// Sends: 2 complete, 1 without text hash.
	commit(Record{Kind: KindSend, From: "c1", To: []string{"c2", "P"}, Tier: TierInfo, Text: "hi", Req: "r1"}, 80)
	commit(Record{Kind: KindSend, From: "c2", To: []string{"c1", "P"}, Tier: TierInfo, Text: "back", Req: "r2"}, 81)
	commit(Record{Kind: KindSend, From: "c3", To: []string{"c1"}, Tier: TierInfo, Req: "r3"}, 82)
	// Final delivery states: r1 and r3 (r3 has no text), r2 none yet.
	commit(Record{Kind: KindDelivery, From: "c2", Req: "r1", State: StateLanded}, 80)
	commit(Record{Kind: KindDelivery, From: "c1", Req: "r3", State: StateFailed}, 82)

	// Imported: one with a measured cross-host latency, one unknown.
	commit(Record{Kind: KindTurn, From: "rc", To: []string{"P"}, Tier: TierInfo, Text: "remote", Origin: "r1", Store: "R1", Key: "rk1",
		TImport: at(90), XLatencyMS: 1200, XErrMS: 40}, 91)
	commit(Record{Kind: KindTurn, From: "rc", To: []string{"P"}, Tier: TierInfo, Text: "remote2", Origin: "r1", Store: "R1", Key: "rk2",
		TImport: at(92)}, 93)

	st, err := ComputeStats(r.Bus, r.Store, StatsOptions{Since: now.Add(-24 * time.Hour), Until: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	tg := st.Targets
	want := func(name string, got Target, value float64, n int, met bool) {
		t.Helper()
		if got.Value == nil || *got.Value != value || got.N != n || got.Met == nil || *got.Met != met {
			v := "nil"
			if got.Value != nil {
				v = "set"
			}
			t.Errorf("%s: %+v (value %s), want value %v n %d met %v", name, got, v, value, n, met)
		}
	}
	// The window is clipped to the first record (93 min ago): P had 6 wakes.
	hours := st.Hours
	if hours < 1.5 || hours > 1.6 {
		t.Fatalf("hours %.3f, want the span from the first in-window record (~1.55)", hours)
	}
	want("wakes per parent hour", tg.WakesPerParentHour, round2(6/hours), 2, round2(6/hours) <= 4)
	if st.Parents[0].PeakHour != 6 {
		t.Fatalf("P's 6 wakes fall within 20 minutes: peak hour %d", st.Parents[0].PeakHour)
	}
	// text: turns a, done, same, same, remote, remote2 (6) + status (no
	// text) + 3 sends (one without text) = 10, 8 with text.
	want("text pct", tg.TextPct, 80, 10, false)
	// deliverable turn+status to P (noise excluded): 4 + 1 + 2 imported = 7, 1 finished.
	want("records per finished", tg.RecordsPerFinished, 7, 1, false)
	// turns: 7 (incl. noise), 1 suspect duplicate.
	want("duplicate pct", tg.DuplicatePct, round2(100.0/7), 7, false)
	// calls: P made 3 output/drain calls over its 6 wakes, R none over 1.
	want("calls per wake", tg.CallsPerWake, round2(3.0/7), 7, false)
	// A send counts only with a sender, a text hash and a final state: r1.
	want("send sender+text+final", tg.SendSenderTextPct, round2(100.0/3), 3, false)
	want("cross-host latency", tg.CrossHostLatency, 1, 2, true)
	if len(st.Parents) != 2 || st.Parents[0].ID != "P" || st.Parents[0].Wakes != 6 || st.Parents[0].Calls != 3 ||
		st.Parents[0].WakesBy["inbox/tmux"] != 4 || st.Parents[0].WakesBy["inbox/stop"] != 1 || st.Parents[0].WakesBy["ledger/tmux"] != 1 {
		t.Fatalf("parents: %+v", st.Parents)
	}
	if st.Imported != 2 || st.ByKind[KindWake] != 7 {
		t.Fatalf("counts: imported %d by_kind %v", st.Imported, st.ByKind)
	}

	// A pulled send is measured on its origin host. It remains visible in
	// the audit counts but cannot change the local send denominator.
	commit(Record{Kind: KindSend, From: "remote-sender", To: []string{"c1", "P"}, Text: "remote send", Req: "remote-request", Origin: "peer", Store: "peer-store"}, 30)
	withRemote, err := ComputeStats(r.Bus, r.Store, StatsOptions{Since: now.Add(-24 * time.Hour), Until: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	want("local sends with pulled send", withRemote.Targets.SendSenderTextPct, round2(100.0/3), 3, false)
	if withRemote.Imported != 3 || withRemote.ByKind[KindSend] != 4 {
		t.Fatalf("pulled send missing from audit counts: %+v", withRemote)
	}

	// Only P.
	one, err := ComputeStats(r.Bus, r.Store, StatsOptions{Since: now.Add(-24 * time.Hour), Until: now.Add(time.Minute), Parent: "R"})
	if err != nil {
		t.Fatal(err)
	}
	if len(one.Parents) != 1 || one.Parents[0].ID != "R" || one.Targets.CallsPerWake.Value == nil || *one.Targets.CallsPerWake.Value != 0 {
		t.Fatalf("parent filter: %+v", one.Parents)
	}
}

func TestStatsOnAnEmptyWindowHaveNoVerdict(t *testing.T) {
	_, r, _ := openPair(t)
	st, err := ComputeStats(r.Bus, r.Store, StatsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for name, tg := range map[string]Target{"wakes": st.Targets.WakesPerParentHour, "text": st.Targets.TextPct, "finished": st.Targets.RecordsPerFinished,
		"dup": st.Targets.DuplicatePct, "calls": st.Targets.CallsPerWake, "sends": st.Targets.SendSenderTextPct, "xhost": st.Targets.CrossHostLatency} {
		if tg.Value != nil || tg.Met != nil {
			t.Fatalf("%s: an empty window must have no value and no verdict: %+v", name, tg)
		}
	}
	if st.Parents == nil || len(st.Parents) != 0 {
		t.Fatalf("parents must be an empty list: %#v", st.Parents)
	}
}

func round2(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100
}

// Verifier round 1 (#7, #6): calls from a parent that was never woken
// still count, and calls with no wake at all are not met; a window with
// records but no recorded wake has no wake verdict.
func TestStatsCallsWithoutWakesAreNotMet(t *testing.T) {
	l, r, _ := openPair(t)
	mustCommit(t, l, Record{Kind: KindTurn, From: "c", To: []string{"P"}, Tier: TierInfo, Text: "x"})
	for i := 0; i < 10; i++ {
		mustCommit(t, l, Record{Kind: KindCall, From: "Q", State: CallInboxDrain})
	}
	st, err := ComputeStats(r.Bus, r.Store, StatsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c := st.Targets.CallsPerWake
	if c.Value == nil || *c.Value != 10 || c.Met == nil || *c.Met {
		t.Fatalf("10 drains and no wake must not be met: %+v", c)
	}
	w := st.Targets.WakesPerParentHour
	if w.Met != nil {
		t.Fatalf("no wake record: no verdict: %+v", w)
	}
	mustCommit(t, l, Record{Kind: KindWake, From: "agent-deck", To: []string{"R"}, Trigger: "inbox", Via: "tmux", Text: "w"})
	st, _ = ComputeStats(r.Bus, r.Store, StatsOptions{})
	if c := st.Targets.CallsPerWake; c.Value == nil || *c.Value != 10 || c.N != 1 {
		t.Fatalf("calls by un-woken parents count against the wakes: %+v", c)
	}
}

// A window shorter than an hour never extrapolates: 3 wakes in 5 minutes
// are 3 per hour.
func TestStatsRateNeverExtrapolatesAShortWindow(t *testing.T) {
	l, r, _ := openPair(t)
	for i := 0; i < 3; i++ {
		mustCommit(t, l, Record{Kind: KindWake, From: "agent-deck", To: []string{"P"}, Trigger: "ledger", Via: "tmux", Text: "w"})
	}
	st, err := ComputeStats(r.Bus, r.Store, StatsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if v := st.Targets.WakesPerParentHour.Value; v == nil || *v != 3 || st.Parents[0].PeakHour != 3 {
		t.Fatalf("3 wakes in minutes: %+v %+v", st.Targets.WakesPerParentHour, st.Parents)
	}
}

// Verifier P3 round 4 (D): a parent observing its children's exchange sees
// their sends as complete when the delivery (addressed elsewhere) landed.
func TestStatsParentSeesObservedSendsComplete(t *testing.T) {
	l, r, _ := openPair(t)
	mustCommit(t, l, Record{Kind: KindSend, From: "c1", To: []string{"c2", "P"}, Tier: TierInfo, Text: "hi", Req: "q1"})
	mustCommit(t, l, Record{Kind: KindDelivery, From: "c2", Req: "q1", State: StateLanded})
	st, err := ComputeStats(r.Bus, r.Store, StatsOptions{Parent: "P"})
	if err != nil {
		t.Fatal(err)
	}
	if v := st.Targets.SendSenderTextPct.Value; v == nil || *v != 100 {
		t.Fatalf("observed send with a final state: %+v", st.Targets.SendSenderTextPct)
	}
}
