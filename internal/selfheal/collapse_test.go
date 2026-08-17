package selfheal

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// pollRead builds the record the engine emits for one evaluation of one session:
// everything is stable except the timestamp and the output signature, which move
// on every poll.
func pollRead(sid string, at time.Time, d Decision) Event {
	return Event{
		TS:        formatTS(at),
		SessionID: sid,
		Title:     "worker-" + sid,
		Profile:   "default",
		Substate:  "",
		Decision:  d,
		Stage:     ModeObserve,
		Reads:     []ReadSig{{T: formatTS(at), Sig: fmt.Sprintf("sig-%d", at.Unix())}},
	}
}

func TestCollapsingSink_CollapsesIdenticalNoOpReads(t *testing.T) {
	mem := &MemorySink{}
	sink := NewCollapsingSink(mem, time.Hour)
	base := time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC)

	for i := 0; i < 600; i++ { // 20 minutes of a 2s poll on a healthy session
		if err := sink.Append(pollRead("s1", base.Add(time.Duration(i)*2*time.Second), DecisionSkipHealthy)); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	got := mem.Snapshot()
	if len(got) != 1 {
		t.Fatalf("600 identical evaluations wrote %d records, want 1 state-entry record", len(got))
	}
	if got[0].TS != formatTS(base) {
		t.Fatalf("entry record TS = %q, want the FIRST read %q", got[0].TS, formatTS(base))
	}
	if got[0].Repeat != 0 {
		t.Fatalf("entry record repeat = %d, want 0 (nothing suppressed before it)", got[0].Repeat)
	}
}

func TestCollapsingSink_StateChangeFlushesClosingRecordThenEntry(t *testing.T) {
	mem := &MemorySink{}
	sink := NewCollapsingSink(mem, time.Hour)
	base := time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC)

	// 5 healthy reads, then the session goes busy.
	for i := 0; i < 5; i++ {
		_ = sink.Append(pollRead("s1", base.Add(time.Duration(i)*2*time.Second), DecisionSkipHealthy))
	}
	changed := base.Add(10 * time.Second)
	_ = sink.Append(pollRead("s1", changed, DecisionSkipBusy))

	got := mem.Snapshot()
	if len(got) != 3 {
		t.Fatalf("want 3 records (entry, closing, new entry), got %d: %+v", len(got), got)
	}
	if got[0].Decision != DecisionSkipHealthy || got[0].TS != formatTS(base) || got[0].Repeat != 0 {
		t.Fatalf("record 0 must be the run's entry, got %+v", got[0])
	}
	// The closing record brackets the run: it carries the LAST suppressed read's
	// timestamp and how many were suppressed, so the run's exact extent survives.
	closing := got[1]
	if closing.Decision != DecisionSkipHealthy {
		t.Fatalf("record 1 must close the healthy run, got decision %q", closing.Decision)
	}
	if closing.Repeat != 4 {
		t.Fatalf("closing repeat = %d, want 4 suppressed reads", closing.Repeat)
	}
	if want := formatTS(base.Add(8 * time.Second)); closing.TS != want {
		t.Fatalf("closing TS = %q, want the last suppressed read %q", closing.TS, want)
	}
	if got[2].Decision != DecisionSkipBusy || got[2].TS != formatTS(changed) || got[2].Repeat != 0 {
		t.Fatalf("record 2 must be the new state's entry, got %+v", got[2])
	}
}

func TestCollapsingSink_HeartbeatEmitsWithRepeatCount(t *testing.T) {
	mem := &MemorySink{}
	sink := NewCollapsingSink(mem, 15*time.Minute)
	base := time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC)

	// One hour of a 2s poll in one unchanging state: entry + 4 heartbeats.
	for i := 0; i <= 1800; i++ {
		_ = sink.Append(pollRead("s1", base.Add(time.Duration(i)*2*time.Second), DecisionSkipDwell))
	}
	got := mem.Snapshot()
	if len(got) != 5 {
		t.Fatalf("1 hour in one state wrote %d records, want 5 (entry + 4 quarter-hour heartbeats): %v", len(got), tsOf(got))
	}
	for i, e := range got[1:] {
		if e.Repeat == 0 {
			t.Fatalf("heartbeat %d carries no repeat count — the suppressed reads are unaccounted for", i)
		}
	}
	// Every read is accounted for: 1801 reads = 5 written + the repeats they carry.
	total := len(got)
	for _, e := range got {
		total += e.Repeat
	}
	if total != 1801 {
		t.Fatalf("reads accounted for = %d, want 1801 (written + repeat counts)", total)
	}
}

// A record where an action actually RAN is never collapsed: those are the
// decisions the audit exists to prove.
func TestCollapsingSink_NeverSuppressesRecordWhereActionRan(t *testing.T) {
	mem := &MemorySink{}
	sink := NewCollapsingSink(mem, time.Hour)
	base := time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC)

	for i := 0; i < 3; i++ {
		ev := pollRead("s1", base.Add(time.Duration(i)*time.Second), DecisionAct)
		ev.Substate = "api-error"
		ev.Action = ActionResume
		ev.WouldHave = ActionResume
		ev.Outcome = "resumed:submitted"
		_ = sink.Append(ev)
	}
	if got := mem.Snapshot(); len(got) != 3 {
		t.Fatalf("executed actions collapsed: %d records, want 3", len(got))
	}
}

func TestCollapsingSink_PerSessionIndependent(t *testing.T) {
	mem := &MemorySink{}
	sink := NewCollapsingSink(mem, time.Hour)
	base := time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC)

	// Two sessions interleaved on the same poll, each in its own stable state.
	for i := 0; i < 50; i++ {
		at := base.Add(time.Duration(i) * 2 * time.Second)
		_ = sink.Append(pollRead("s1", at, DecisionSkipHealthy))
		_ = sink.Append(pollRead("s2", at, DecisionSkipBusy))
	}
	got := mem.Snapshot()
	if len(got) != 2 {
		t.Fatalf("interleaved sessions wrote %d records, want 1 entry each: %+v", len(got), got)
	}
	if got[0].SessionID != "s1" || got[1].SessionID != "s2" {
		t.Fatalf("per-session state leaked: %+v", got)
	}
}

// A record whose timestamp cannot be parsed is always written: the collapse is
// an optimisation, and an unreadable clock must never silently drop an event.
func TestCollapsingSink_UnparseableTimestampAlwaysEmits(t *testing.T) {
	mem := &MemorySink{}
	sink := NewCollapsingSink(mem, time.Hour)
	for i := 0; i < 4; i++ {
		ev := pollRead("s1", time.Now(), DecisionSkipHealthy)
		ev.TS = "not-a-timestamp"
		_ = sink.Append(ev)
	}
	if got := mem.Snapshot(); len(got) != 4 {
		t.Fatalf("unparseable timestamps collapsed: %d records, want 4", len(got))
	}
}

func TestCollapsingSink_ConcurrentAppends(t *testing.T) {
	mem := &MemorySink{}
	sink := NewCollapsingSink(mem, time.Hour)
	base := time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC)
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_ = sink.Append(pollRead(fmt.Sprintf("s%02d", i), base.Add(time.Duration(j)*2*time.Second), DecisionSkipHealthy))
			}
		}(i)
	}
	wg.Wait()
	got := mem.Snapshot()
	if len(got) != 40 {
		t.Fatalf("concurrent per-session streams wrote %d records, want 40 entries", len(got))
	}
}

// The point of the change, measured on the shape that produced 480 MB/day: a
// 34-session fleet on a 2s poll, all healthy.
func TestCollapsingSink_CutsMeasuredWorkloadVolume(t *testing.T) {
	mem := &MemorySink{}
	sink := NewCollapsingSink(mem, DefaultAuditHeartbeat)
	base := time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC)

	reads := 0
	for tick := 0; tick < 43200; tick++ { // 24 h at a 2s poll
		at := base.Add(time.Duration(tick) * 2 * time.Second)
		for s := 0; s < 34; s++ {
			_ = sink.Append(pollRead(fmt.Sprintf("s%02d", s), at, DecisionSkipHealthy))
			reads++
		}
	}
	written := len(mem.Snapshot())
	drop := 1 - float64(written)/float64(reads)
	if drop < 0.99 {
		t.Fatalf("volume drop = %.4f (%d of %d records), want ≥0.99", drop, written, reads)
	}
	t.Logf("24h x 34 sessions: %d reads -> %d records (%.2f%% drop)", reads, written, drop*100)
}

func tsOf(evs []Event) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.TS
	}
	return out
}
