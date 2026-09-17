package health

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var fixtureNow = time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

func at(sec int) time.Time { return fixtureNow.Add(time.Duration(sec-3600) * time.Second) }

func cleanFixture() []Event {
	return []Event{
		{TS: at(0), SessionID: "s1", Kind: KindStatus, From: "idle", To: "running"},
		{TS: at(10), SessionID: "s1", Kind: KindStatus, From: "running", To: "waiting"},
		{TS: at(20), SessionID: "s1", Kind: KindSend, Detail: map[string]any{"outcome": SendConfirmed, "ack_ms": float64(400)}},
		{TS: at(21), SessionID: "s1", Kind: KindStatus, From: "waiting", To: "running"},
		{TS: at(51), SessionID: "s1", Kind: KindStatus, From: "running", To: "waiting"},
		{TS: at(60), SessionID: "s1", Kind: KindSend, Detail: map[string]any{"outcome": SendUnconfirmed}},
		{TS: at(61), SessionID: "s1", Kind: KindStatus, From: "waiting", To: "running"},
		{TS: at(81), SessionID: "s1", Kind: KindStatus, From: "running", To: "idle"},
		{TS: at(90), SessionID: "s1", Kind: KindRestart},
		{TS: at(100), SessionID: "s1", Kind: KindWorkerDone, Detail: map[string]any{"status": "ok", "duration_ms": float64(5000)}},
		{TS: at(5), SessionID: "s2", Kind: KindStatus, From: "idle", To: "running"},
	}
}

func TestSessionMetricsCleanFixture(t *testing.T) {
	m := ComputeSessionMetrics("s1", cleanFixture(), fixtureNow.Add(-time.Hour), fixtureNow)
	if m.Turns.Count != 3 {
		t.Fatalf("turns: %+v", m.Turns)
	}
	// durations 10s, 30s, 20s -> p50 20000, p95 30000
	if m.Turns.P50MS == nil || *m.Turns.P50MS != 20000 || m.Turns.P95MS == nil || *m.Turns.P95MS != 30000 {
		t.Fatalf("turn percentiles: %+v", m.Turns)
	}
	// waiting 10..21 (11s) + 51..61 (10s) = 21s; idle from 81 is not waiting.
	if m.WaitingMS == nil || *m.WaitingMS != 21000 {
		t.Fatalf("waiting: %v", m.WaitingMS)
	}
	if m.Sends.Count != 2 || m.Sends.Confirmed != 1 || m.Sends.Unconfirmed != 1 || m.Sends.Failed != 0 {
		t.Fatalf("sends: %+v", m.Sends)
	}
	if m.Sends.UnconfirmedRate == nil || *m.Sends.UnconfirmedRate != 0.5 {
		t.Fatalf("unconfirmed rate: %v", m.Sends.UnconfirmedRate)
	}
	if m.Sends.AckP50MS == nil || *m.Sends.AckP50MS != 400 || m.Sends.AckP95MS == nil || *m.Sends.AckP95MS != 400 {
		t.Fatalf("ack: %+v", m.Sends)
	}
	if m.Restarts != 1 {
		t.Fatalf("restarts: %d", m.Restarts)
	}
	if m.Worker == nil || m.Worker.Status != "ok" || m.Worker.DurationMS == nil || *m.Worker.DurationMS != 5000 {
		t.Fatalf("worker: %+v", m.Worker)
	}
	if m.LastStatusChange == nil || m.LastStatusChange.Status != "idle" || m.LastStatusChange.AgeMS != float64(fixtureNow.Sub(at(81))/time.Millisecond) {
		t.Fatalf("last status change: %+v", m.LastStatusChange)
	}
	if m.Events != 10 || m.Journal != JournalOK {
		t.Fatalf("events=%d journal=%q", m.Events, m.Journal)
	}
}

func TestSessionMetricsGapsAndUnknowns(t *testing.T) {
	events := []Event{
		// A running observed with no terminal edge in the window: not a turn.
		{TS: at(0), SessionID: "g", Kind: KindStatus, From: "idle", To: "running"},
		// Terminal edge with no preceding running (daemon restarted): counts as a
		// turn with unknown duration.
		{TS: at(30), SessionID: "g", Kind: KindStatus, From: "", To: "waiting"},
		{TS: at(40), SessionID: "g", Kind: KindSend, Detail: map[string]any{"outcome": "weird"}},
		{TS: at(41), SessionID: "g", Kind: KindSend},
		{TS: at(42), SessionID: "g", Kind: KindSend, Detail: map[string]any{"outcome": SendFailed}},
		{TS: at(50), SessionID: "g", Kind: KindStatus, From: "waiting", To: "running"},
	}
	m := ComputeSessionMetrics("g", events, fixtureNow.Add(-time.Hour), fixtureNow)
	if m.Turns.Count != 1 || m.Turns.Measured != 1 || m.Turns.P50MS == nil || *m.Turns.P50MS != 30000 {
		t.Fatalf("turns with gap: %+v", m.Turns)
	}
	if m.Sends.Count != 3 || m.Sends.Failed != 1 || m.Sends.Unknown != 2 || m.Sends.AckP50MS != nil {
		t.Fatalf("sends with unknown outcomes: %+v", m.Sends)
	}
	// Unknown outcomes are excluded from the rate's denominator: 0 unconfirmed of 1 classified.
	if m.Sends.UnconfirmedRate == nil || *m.Sends.UnconfirmedRate != 0 {
		t.Fatalf("rate: %v", m.Sends.UnconfirmedRate)
	}
	// waiting 30..50 = 20s
	if m.WaitingMS == nil || *m.WaitingMS != 20000 {
		t.Fatalf("waiting: %v", m.WaitingMS)
	}
	if m.Worker != nil || m.Restarts != 0 {
		t.Fatalf("fabricated: %+v", m)
	}
}

func TestSessionMetricsOpenWaitingIntervalEndsAtNow(t *testing.T) {
	events := []Event{{TS: at(3000), SessionID: "w", Kind: KindStatus, From: "running", To: "waiting"}}
	m := ComputeSessionMetrics("w", events, fixtureNow.Add(-time.Hour), fixtureNow)
	if m.WaitingMS == nil || *m.WaitingMS != 600000 {
		t.Fatalf("open waiting interval: %v", m.WaitingMS)
	}
}

func TestSessionMetricsNoEventsReportsUnknownNotZero(t *testing.T) {
	m := ComputeSessionMetrics("none", cleanFixture(), fixtureNow.Add(-time.Hour), fixtureNow)
	if m.Journal != JournalNoEvents || m.Events != 0 {
		t.Fatalf("%+v", m)
	}
	data, _ := json.Marshal(m)
	s := string(data)
	for _, want := range []string{`"p50_ms":null`, `"waiting_ms":null`, `"unconfirmed_rate":null`, `"worker":null`, `"last_status_change":null`, `"dead_letters":null`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
}

func TestAggregateSessions(t *testing.T) {
	events := cleanFixture()
	a := AggregateSessions(events, fixtureNow.Add(-time.Hour), fixtureNow)
	if a.SessionsObserved != 2 || a.Turns != 3 {
		t.Fatalf("%+v", a)
	}
	if a.TurnsPerDay == nil || *a.TurnsPerDay != 72 {
		t.Fatalf("turns/day over a one hour window: %v", a.TurnsPerDay)
	}
	if a.MedianTurnMS == nil || *a.MedianTurnMS != 20000 {
		t.Fatalf("median: %v", a.MedianTurnMS)
	}
	if a.UnconfirmedSendRate == nil || *a.UnconfirmedSendRate != 0.5 {
		t.Fatalf("rate: %v", a.UnconfirmedSendRate)
	}
	if a.RestartsPerDay == nil || *a.RestartsPerDay != 24 {
		t.Fatalf("restarts/day: %v", a.RestartsPerDay)
	}
	empty := AggregateSessions(nil, fixtureNow.Add(-time.Hour), fixtureNow)
	if empty.Journal != JournalNoEvents || empty.TurnsPerDay != nil || empty.MedianTurnMS != nil {
		t.Fatalf("empty aggregate fabricated numbers: %+v", empty)
	}
}

func TestPercentiles(t *testing.T) {
	p50, p95 := percentiles([]float64{30, 10, 20})
	if p50 != 20 || p95 != 30 {
		t.Fatalf("%v %v", p50, p95)
	}
	p50, p95 = percentiles([]float64{1, 2, 3, 4})
	if p50 != 2.5 || p95 != 4 {
		t.Fatalf("%v %v", p50, p95)
	}
}
