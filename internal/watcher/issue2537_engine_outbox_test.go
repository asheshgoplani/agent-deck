package watcher

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/sendqueue"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// The engine's side of durable watcher delivery (#2537): a routed event is
// queued for its conductor when the engine routes it, before it is stored.

// recordingOutbox is an Outbox that remembers what it was given and whether
// the event was already stored at that moment.
type recordingOutbox struct {
	db         *statedb.StateDB
	mu         sync.Mutex
	events     []Event
	stored     []bool
	pending    map[string][]PendingDelivery
	pendingErr error
}

func (o *recordingOutbox) Enqueue(watcherID, _ string, evt Event) error {
	stored, err := o.db.HasWatcherEvent(watcherID, evt.DedupKey())
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, evt)
	o.stored = append(o.stored, stored || err != nil)
	return nil
}

func (o *recordingOutbox) Pending() (map[string][]PendingDelivery, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.pending, o.pendingErr
}

func (o *recordingOutbox) got() ([]Event, []bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]Event(nil), o.events...), append([]bool(nil), o.stored...)
}

func newOutboxTestEngine(t *testing.T, db *statedb.StateDB, outbox Outbox, health time.Duration) *Engine {
	t.Helper()
	return NewEngine(EngineConfig{
		DB:                  db,
		Router:              NewRouter(map[string]ClientEntry{"alice@example.com": {Conductor: "demo", Name: "Alice"}}),
		MaxEventsPerWatcher: 500,
		HealthCheckInterval: health,
		TriageSpawner:       &fakeSpawner{},
		TriageDir:           t.TempDir(),
		ClientsPath:         filepath.Join(t.TempDir(), "clients.json"),
		Outbox:              outbox,
	})
}

// runEvents runs one mock watcher "w1" emitting events through the engine
// and stops it once the writer had time to handle them.
func runEvents(t *testing.T, engine *Engine, events ...Event) {
	t.Helper()
	engine.RegisterAdapter("w1", &MockAdapter{events: events, listenDelay: 10 * time.Millisecond},
		AdapterConfig{Type: "mock", Name: "inbox"}, 60)
	if err := engine.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(time.Duration(len(events))*10*time.Millisecond + 300*time.Millisecond)
	engine.Stop()
}

// TestIssue2537_EngineQueuesARoutedEventBeforeStoringIt: an event routed to
// a conductor reaches the outbox once, routed, and before its watcher_events
// row exists, so a crash between the two cannot leave a stored event nothing
// delivers. A replay of a stored event, an event for triage and an event no
// rule routes are not queued.
func TestIssue2537_EngineQueuesARoutedEventBeforeStoringIt(t *testing.T) {
	db := newTestDB(t)
	saveTestWatcher(t, db, "w1", "inbox", "mock")
	outbox := &recordingOutbox{db: db}
	engine := newOutboxTestEngine(t, db, outbox, 0)
	routed := Event{Source: "mock", Sender: "alice@example.com", Subject: "hi", Body: "hello", Timestamp: time.Now()}
	unrouted := Event{Source: "mock", Sender: "mallory@example.com", Subject: "spam", Timestamp: time.Now()}

	runEvents(t, engine, routed, routed, unrouted)

	events, stored := outbox.got()
	if len(events) != 1 {
		t.Fatalf("outbox got %d events, want the routed one once", len(events))
	}
	if events[0].RoutedTo != "demo" || events[0].Body != "hello" {
		t.Fatalf("queued %+v, want the event routed to demo", events[0])
	}
	if stored[0] {
		t.Fatal("the event was stored before it was queued")
	}
	if n := countWatcherEvents(t, db, "w1"); n != 2 {
		t.Fatalf("watcher_events holds %d rows, want 2 (routed + triage)", n)
	}
}

// TestIssue2537_TheSameEventRoutedTwiceIsOneRecord: the maintainer's test
// (c) through the engine. The second time, the event's row is gone (pruned
// past max_events_per_watcher), so the engine routes it again; the queue key
// keeps it to one record and one worker start.
func TestIssue2537_TheSameEventRoutedTwiceIsOneRecord(t *testing.T) {
	db := newTestDB(t)
	saveTestWatcher(t, db, "w1", "inbox", "mock")
	conductor := saveTestConductor(t, db, "demo")
	outbox, dir, spawned := newTestOutbox(t, db, 0)
	evt := Event{Source: "mock", Sender: "alice@example.com", Subject: "hi", Body: "routed twice", Timestamp: time.Now()}

	runEvents(t, newOutboxTestEngine(t, db, outbox, 0), evt)
	if _, err := db.DB().Exec(`DELETE FROM watcher_events WHERE watcher_id = ?`, "w1"); err != nil {
		t.Fatal(err)
	}
	runEvents(t, newOutboxTestEngine(t, db, outbox, 0), evt)

	if n := countWatcherEvents(t, db, "w1"); n != 1 {
		t.Fatalf("watcher_events holds %d rows after the second routing, want 1", n)
	}
	recs, err := sendqueue.List(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].SessionID != conductor {
		t.Fatalf("%d records (%+v), want one for the conductor", len(recs), recs)
	}
	if n := spawned.count(conductor); n != 1 {
		t.Fatalf("worker started %d times, want once", n)
	}
}

// TestIssue2537_HealthStateReportsUndeliveredEvents: each health state
// carries the watcher's routed events still waiting, per conductor, so the
// panel, the event bus and the health alerts (#2531) can see a stall.
func TestIssue2537_HealthStateReportsUndeliveredEvents(t *testing.T) {
	db := newTestDB(t)
	saveTestWatcher(t, db, "w1", "inbox", "mock")
	queued := time.Now().Add(-time.Hour)
	outbox := &recordingOutbox{db: db, pending: map[string][]PendingDelivery{
		"w1":    {{Conductor: "demo", Count: 3, Oldest: queued}},
		"other": {{Conductor: "ops", Count: 1, Oldest: queued}},
	}}
	engine := newOutboxTestEngine(t, db, outbox, 20*time.Millisecond)
	engine.RegisterAdapter("w1", &MockAdapter{}, AdapterConfig{Type: "mock", Name: "inbox"}, 60)
	if err := engine.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	select {
	case state := <-engine.HealthCh():
		if len(state.Undelivered) != 1 || state.Undelivered[0].Conductor != "demo" || state.Undelivered[0].Count != 3 || !state.Undelivered[0].Oldest.Equal(queued) {
			t.Fatalf("health state undelivered = %+v, want w1's 3 events for demo", state.Undelivered)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no health state")
	}
}

// TestIssue2537_HealthStateSaysWhenTheQueueCannotBeRead: when the send
// queue cannot be read, the health state says so instead of carrying no
// undelivered events, which reads as an empty queue.
func TestIssue2537_HealthStateSaysWhenTheQueueCannotBeRead(t *testing.T) {
	db := newTestDB(t)
	saveTestWatcher(t, db, "w1", "inbox", "mock")
	outbox := &recordingOutbox{db: db, pendingErr: errors.New("read sendqueue: permission denied")}
	engine := newOutboxTestEngine(t, db, outbox, 20*time.Millisecond)
	engine.RegisterAdapter("w1", &MockAdapter{}, AdapterConfig{Type: "mock", Name: "inbox"}, 60)
	if err := engine.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	select {
	case state := <-engine.HealthCh():
		if state.Undelivered != nil || state.UndeliveredError != "read sendqueue: permission denied" {
			t.Fatalf("health state undelivered %+v, error %q; want none and the queue's error", state.Undelivered, state.UndeliveredError)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no health state")
	}
}

// flakyOutbox fails Enqueue with err for its first failures calls, then
// records like recordingOutbox; failures < 0 fails every call.
type flakyOutbox struct {
	recordingOutbox
	err      error
	failures int

	callMu sync.Mutex
	calls  int
}

func (o *flakyOutbox) Enqueue(watcherID, name string, evt Event) error {
	o.callMu.Lock()
	o.calls++
	fail := o.failures < 0 || o.calls <= o.failures
	o.callMu.Unlock()
	if fail {
		return o.err
	}
	return o.recordingOutbox.Enqueue(watcherID, name, evt)
}

func (o *flakyOutbox) callCount() int {
	o.callMu.Lock()
	defer o.callMu.Unlock()
	return o.calls
}

// TestIssue2537_AnEventTheOutboxFailsToQueueIsQueuedOnRetry: the outbox
// fails twice for a reason a retry can fix (here a locked database). The
// event is not stored meanwhile, since a stored event is never queued again;
// the writer retries, the event is queued once, unstored at that point, and
// then stored.
func TestIssue2537_AnEventTheOutboxFailsToQueueIsQueuedOnRetry(t *testing.T) {
	db := newTestDB(t)
	saveTestWatcher(t, db, "w1", "inbox", "mock")
	outbox := &flakyOutbox{recordingOutbox: recordingOutbox{db: db}, err: errors.New("database is locked"), failures: 2}
	engine := newOutboxTestEngine(t, db, outbox, 0)
	engine.cfg.QueueRetryInterval = 100 * time.Millisecond
	evt := Event{Source: "mock", Sender: "alice@example.com", Subject: "hi", Body: "retried", Timestamp: time.Now()}
	engine.RegisterAdapter("w1", &MockAdapter{events: []Event{evt}, listenDelay: 10 * time.Millisecond},
		AdapterConfig{Type: "mock", Name: "inbox"}, 60)
	if err := engine.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(engine.Stop)

	deadline := time.Now().Add(5 * time.Second)
	for outbox.callCount() < 1 {
		if time.Now().After(deadline) {
			t.Fatal("the event never reached the outbox")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if n := countWatcherEvents(t, db, "w1"); n != 0 {
		t.Fatalf("the event was stored after the outbox failed to queue it (%d rows)", n)
	}
	for {
		if events, _ := outbox.got(); len(events) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the event was not queued after %d tries", outbox.callCount())
		}
		time.Sleep(10 * time.Millisecond)
	}
	for countWatcherEvents(t, db, "w1") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the event was not stored once it was queued")
		}
		time.Sleep(10 * time.Millisecond)
	}
	events, stored := outbox.got()
	if len(events) != 1 || stored[0] || outbox.callCount() != 3 {
		t.Fatalf("queued %d time(s) after %d tries, stored at that point %v; want once on the third try, unstored", len(events), outbox.callCount(), stored)
	}
}

// TestIssue2537_RoutedEventsKeepTheirOrderWhileTheOutboxRecovers: E1 fails
// to queue once; E2 arrives while E1 waits for the retry. E2 waits behind E1
// instead of reaching the send queue first, so the conductor gets them in
// order.
func TestIssue2537_RoutedEventsKeepTheirOrderWhileTheOutboxRecovers(t *testing.T) {
	db := newTestDB(t)
	saveTestWatcher(t, db, "w1", "inbox", "mock")
	outbox := &flakyOutbox{recordingOutbox: recordingOutbox{db: db}, err: errors.New("database is locked"), failures: 1}
	engine := newOutboxTestEngine(t, db, outbox, 0)
	engine.cfg.QueueRetryInterval = 50 * time.Millisecond
	now := time.Now()
	runEvents(t, engine,
		Event{Source: "mock", Sender: "alice@example.com", Subject: "first", Body: "E1", Timestamp: now},
		Event{Source: "mock", Sender: "alice@example.com", Subject: "second", Body: "E2", Timestamp: now.Add(time.Second)},
	)

	events, _ := outbox.got()
	var order []string
	for _, e := range events {
		order = append(order, e.Body)
	}
	if got := strings.Join(order, ","); got != "E1,E2" {
		t.Fatalf("queued %q after %d outbox calls, want E1 then E2", got, outbox.callCount())
	}
	if n := countWatcherEvents(t, db, "w1"); n != 2 {
		t.Fatalf("watcher_events holds %d rows, want both events", n)
	}
}

// TestIssue2537_AnEventForAConductorWithNoSessionIsStored: no retry can
// queue an event for a conductor with no session, so it is tried once and
// stored, as before.
func TestIssue2537_AnEventForAConductorWithNoSessionIsStored(t *testing.T) {
	db := newTestDB(t)
	saveTestWatcher(t, db, "w1", "inbox", "mock")
	outbox := &flakyOutbox{recordingOutbox: recordingOutbox{db: db}, err: fmt.Errorf("%w: conductor %q has no session", ErrNoConductorSession, "demo"), failures: -1}
	engine := newOutboxTestEngine(t, db, outbox, 0)
	engine.cfg.QueueRetryInterval = 50 * time.Millisecond
	runEvents(t, engine, Event{Source: "mock", Sender: "alice@example.com", Subject: "hi", Body: "no session", Timestamp: time.Now()})

	if n := countWatcherEvents(t, db, "w1"); n != 1 || outbox.callCount() != 1 {
		t.Fatalf("watcher_events holds %d rows after %d outbox calls, want the event stored after one", n, outbox.callCount())
	}
}

// TestIssue2537_AnEventTheOutboxNeverQueuedIsNotStored: the outbox fails
// until the engine stops. The event is retried, gets a last try at stop, and
// is then left unstored, with its watcher's resume cursor not moved past it,
// so a replay after the restart routes and queues it again. Storing it would
// lose it for good: a stored event is never queued.
func TestIssue2537_AnEventTheOutboxNeverQueuedIsNotStored(t *testing.T) {
	db := newTestDB(t)
	name := "inbox-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	saveTestWatcher(t, db, "w1", name, "mock")
	outbox := &flakyOutbox{recordingOutbox: recordingOutbox{db: db}, err: errors.New("database is locked"), failures: -1}
	engine := newOutboxTestEngine(t, db, outbox, 0)
	engine.cfg.QueueRetryInterval = 50 * time.Millisecond
	evt := Event{Source: "mock", Sender: "alice@example.com", Subject: "hi", Body: "never queued", Timestamp: time.Now(), Cursor: "c1"}
	engine.RegisterAdapter("w1", &MockAdapter{events: []Event{evt}, listenDelay: 10 * time.Millisecond},
		AdapterConfig{Type: "mock", Name: name}, 60)
	if err := engine.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(400 * time.Millisecond)
	engine.Stop()

	if n := countWatcherEvents(t, db, "w1"); n != 0 {
		t.Fatalf("watcher_events holds %d rows, want the unqueued event left unstored", n)
	}
	if calls := outbox.callCount(); calls < 3 {
		t.Fatalf("the outbox was tried %d times, want retries and a last try at stop", calls)
	}
	st, err := LoadState(name)
	if err != nil || st == nil || st.ReplayFrom.IsZero() || st.ReplayFrom.After(evt.Timestamp) {
		t.Fatalf("watcher state %+v (%v); want a replay position no later than the unqueued event", st, err)
	}
	if got, want := engine.resumeSince(AdapterConfig{Name: name}), strconv.FormatInt(st.ReplayFrom.Unix(), 10); got != want {
		t.Fatalf("a restart resumes from %q, want the replay position %q", got, want)
	}
}

// conductorOutbox fails Enqueue for events routed to failFor, and records
// the others like recordingOutbox.
type conductorOutbox struct {
	recordingOutbox
	failFor string
}

func (o *conductorOutbox) Enqueue(watcherID, name string, evt Event) error {
	if evt.RoutedTo == o.failFor {
		return errors.New("read the record queued under this key: input/output error")
	}
	return o.recordingOutbox.Enqueue(watcherID, name, evt)
}

// TestIssue2537_AConductorTheOutboxFailsDoesNotHoldBackAnother: the outbox
// keeps failing for ops but not for demo. Ops' event waits alone; demo's
// events behind it in arrival order are queued at once, in order. When the
// engine stops, ops' event is given up unstored, and the watcher's replay
// position stays before it although demo's later events moved the cursor.
func TestIssue2537_AConductorTheOutboxFailsDoesNotHoldBackAnother(t *testing.T) {
	db := newTestDB(t)
	name := "inbox-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	saveTestWatcher(t, db, "w1", name, "mock")
	outbox := &conductorOutbox{recordingOutbox: recordingOutbox{db: db}, failFor: "ops"}
	engine := NewEngine(EngineConfig{
		DB: db,
		Router: NewRouter(map[string]ClientEntry{
			"alice@example.com": {Conductor: "demo", Name: "Alice"},
			"bob@example.com":   {Conductor: "ops", Name: "Bob"},
		}),
		MaxEventsPerWatcher: 500,
		TriageSpawner:       &fakeSpawner{},
		TriageDir:           t.TempDir(),
		ClientsPath:         filepath.Join(t.TempDir(), "clients.json"),
		Outbox:              outbox,
		QueueRetryInterval:  50 * time.Millisecond,
	})
	start := time.Now().Add(-time.Minute)
	events := []Event{
		{Source: "mock", Sender: "bob@example.com", Subject: "x1", Body: "X1", Timestamp: start, Cursor: "x1"},
		{Source: "mock", Sender: "alice@example.com", Subject: "y1", Body: "Y1", Timestamp: start.Add(time.Second), Cursor: "y1"},
		{Source: "mock", Sender: "alice@example.com", Subject: "y2", Body: "Y2", Timestamp: start.Add(2 * time.Second), Cursor: "y2"},
	}
	engine.RegisterAdapter("w1", &MockAdapter{events: events, listenDelay: 10 * time.Millisecond},
		AdapterConfig{Type: "mock", Name: name}, 60)
	if err := engine.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if queued, _ := outbox.got(); len(queued) == 2 {
			break
		}
		if time.Now().After(deadline) {
			engine.Stop()
			queued, _ := outbox.got()
			t.Fatalf("demo's events were not queued while ops' X1 waited (queued %d)", len(queued))
		}
		time.Sleep(10 * time.Millisecond)
	}
	engine.Stop()

	queued, _ := outbox.got()
	var order []string
	for _, e := range queued {
		order = append(order, e.Body)
	}
	if got := strings.Join(order, ","); got != "Y1,Y2" {
		t.Fatalf("queued %q, want demo's Y1 then Y2 while ops' X1 waits", got)
	}
	if n := countWatcherEvents(t, db, "w1"); n != 2 {
		t.Fatalf("watcher_events holds %d rows, want demo's two and not ops' unqueued one", n)
	}
	st, err := LoadState(name)
	if err != nil || st == nil || st.ReplayFrom.IsZero() || st.ReplayFrom.After(start) {
		t.Fatalf("watcher state %+v (%v); want a replay position no later than X1 (%s)", st, err, start)
	}
}

// TestIssue2537_AnEventWhoseReplayPositionCannotBeWrittenIsStored: the
// outbox keeps failing and the watcher's state cannot be written either
// (state.json is a directory). Without a replay position a restart would
// skip the event, so at stop it is stored rather than left unstored.
func TestIssue2537_AnEventWhoseReplayPositionCannotBeWrittenIsStored(t *testing.T) {
	db := newTestDB(t)
	name := "inbox-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	saveTestWatcher(t, db, "w1", name, "mock")
	dir, err := WatcherDir(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "state.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	outbox := &flakyOutbox{recordingOutbox: recordingOutbox{db: db}, err: errors.New("database is locked"), failures: -1}
	engine := newOutboxTestEngine(t, db, outbox, 0)
	engine.cfg.QueueRetryInterval = 50 * time.Millisecond
	evt := Event{Source: "mock", Sender: "alice@example.com", Subject: "hi", Body: "no replay position", Timestamp: time.Now(), Cursor: "c1"}
	engine.RegisterAdapter("w1", &MockAdapter{events: []Event{evt}, listenDelay: 10 * time.Millisecond},
		AdapterConfig{Type: "mock", Name: name}, 60)
	if err := engine.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	engine.Stop()

	if n := countWatcherEvents(t, db, "w1"); n != 1 {
		t.Fatalf("watcher_events holds %d rows, want the event stored when no replay position could be written", n)
	}
}

// TestIssue2537_AnUnreadableStateKeepsTheEarlierReplayPosition: E1 left a
// replay position; then the state cannot be read while E2 is held. The
// engine must not start from an empty state and overwrite E1's earlier
// position with E2's later one, or a crash before E1 is queued would resume
// past it.
func TestIssue2537_AnUnreadableStateKeepsTheEarlierReplayPosition(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a file whatever its mode")
	}
	db := newTestDB(t)
	name := "inbox-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	engine := newOutboxTestEngine(t, db, &recordingOutbox{db: db}, 0)
	tracker := NewHealthTracker(name, 60)
	t1 := time.Now().Add(-time.Hour)
	e1 := eventEnvelope{event: Event{Sender: "alice@example.com", Timestamp: t1}, watcherID: "w1", tracker: tracker}
	e2 := eventEnvelope{event: Event{Sender: "alice@example.com", Timestamp: t1.Add(30 * time.Minute)}, watcherID: "w1", tracker: tracker}
	if err := engine.keepReplayPosition(e1); err != nil {
		t.Fatal(err)
	}
	dir, err := WatcherDir(name)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "state.json")
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	if err := engine.keepReplayPosition(e2); err == nil {
		t.Fatal("keepReplayPosition with an unreadable state reported no error")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := LoadState(name)
	if err != nil || st == nil || !st.ReplayFrom.Equal(t1.Add(-time.Second).UTC()) {
		t.Fatalf("watcher state %+v (%v); want E1's replay position kept", st, err)
	}
}

// TestIssue2537_ARetryableFailureSurvivesARestartAndIsQueuedOnce: the
// outbox keeps failing for a reason a retry can fix until the engine stops.
// The event is left unstored with a replay position before it. A new
// engine (the next owner, after a restart or a handoff) replays it through
// the real send queue outbox: it is queued exactly once, even when the
// replay delivers it twice and a third owner replays it again, and it is
// stored once.
func TestIssue2537_ARetryableFailureSurvivesARestartAndIsQueuedOnce(t *testing.T) {
	db := newTestDB(t)
	name := "inbox-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	saveTestWatcher(t, db, "w1", name, "mock")
	conductorID := saveTestConductor(t, db, "demo")
	evt := Event{Source: "mock", Sender: "alice@example.com", Subject: "hi", Body: "survives a restart", Timestamp: time.Now(), Cursor: "c1"}

	run := func(outbox Outbox, events ...Event) {
		t.Helper()
		engine := newOutboxTestEngine(t, db, outbox, 0)
		engine.cfg.QueueRetryInterval = 50 * time.Millisecond
		engine.RegisterAdapter("w1", &MockAdapter{events: events, listenDelay: 10 * time.Millisecond},
			AdapterConfig{Type: "mock", Name: name}, 60)
		if err := engine.Start(); err != nil {
			t.Fatalf("Start: %v", err)
		}
		time.Sleep(time.Duration(len(events))*10*time.Millisecond + 300*time.Millisecond)
		engine.Stop()
	}

	failing := &flakyOutbox{recordingOutbox: recordingOutbox{db: db}, err: errors.New("database is locked"), failures: -1}
	run(failing, evt)
	if n := countWatcherEvents(t, db, "w1"); n != 0 {
		t.Fatalf("watcher_events holds %d rows after the outbox never took the event, want 0", n)
	}
	st, err := LoadState(name)
	if err != nil || st == nil || st.ReplayFrom.IsZero() || st.ReplayFrom.After(evt.Timestamp) {
		t.Fatalf("watcher state %+v (%v); want a replay position no later than the event", st, err)
	}

	outbox, dir, spawned := newTestOutbox(t, db, 0)
	run(outbox, evt, evt)
	run(outbox, evt)

	recs, err := sendqueue.List(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].SessionID != conductorID || recs[0].Key != DeliveryKey("w1", evt) {
		t.Fatalf("send queue records %+v; want one for %s under the event's key", recs, conductorID)
	}
	if n := spawned.count(conductorID); n != 1 {
		t.Fatalf("worker started %d times, want once", n)
	}
	if n := countWatcherEvents(t, db, "w1"); n != 1 {
		t.Fatalf("watcher_events holds %d rows, want the event stored once", n)
	}
}
