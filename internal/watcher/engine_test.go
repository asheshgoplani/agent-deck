package watcher

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// newTestDB creates a temporary StateDB for engine tests.
func newTestDB(t *testing.T) *statedb.StateDB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := statedb.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// newTestEngine creates an Engine with a fresh DB and the given client routing rules.
// HealthCheckInterval is 0 to disable the health loop in tests.
// Uses a fakeSpawner to avoid spawning real agent-deck subprocesses (goroutine-leak-safe).
func newTestEngine(t *testing.T, clients map[string]ClientEntry) (*Engine, *statedb.StateDB) {
	t.Helper()
	db := newTestDB(t)
	router := NewRouter(clients)
	cfg := EngineConfig{
		DB:                  db,
		Router:              router,
		MaxEventsPerWatcher: 500,
		HealthCheckInterval: 0,
		// Use fakeSpawner so tests never exec real agent-deck subprocesses.
		// This prevents os/exec.watchCtx goroutine leaks across tests.
		TriageSpawner: &fakeSpawner{},
		TriageDir:     t.TempDir(),
		ClientsPath:   filepath.Join(t.TempDir(), "clients.json"),
	}
	engine := NewEngine(cfg)
	return engine, db
}

// saveTestWatcher inserts a watcher row into the database for testing.
func saveTestWatcher(t *testing.T, db *statedb.StateDB, id, name, typ string) {
	t.Helper()
	now := time.Now()
	err := db.SaveWatcher(&statedb.WatcherRow{
		ID:        id,
		Name:      name,
		Type:      typ,
		Status:    "running",
		CreatedAt: now,
		UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("SaveWatcher: %v", err)
	}
}

// countWatcherEvents queries the watcher_events table and returns the row count for a given watcher.
func countWatcherEvents(t *testing.T, db *statedb.StateDB, watcherID string) int {
	t.Helper()
	var count int
	err := db.DB().QueryRow(
		`SELECT COUNT(*) FROM watcher_events WHERE watcher_id = ?`, watcherID,
	).Scan(&count)
	if err != nil {
		t.Fatalf("count watcher_events: %v", err)
	}
	return count
}

// queryWatcherEventRoutedTo returns the routed_to value for the first event matching the given watcher.
func queryWatcherEventRoutedTo(t *testing.T, db *statedb.StateDB, watcherID string) string {
	t.Helper()
	var routedTo string
	err := db.DB().QueryRow(
		`SELECT routed_to FROM watcher_events WHERE watcher_id = ? ORDER BY id LIMIT 1`, watcherID,
	).Scan(&routedTo)
	if err != nil {
		t.Fatalf("query routed_to: %v", err)
	}
	return routedTo
}

// drainEvents reads all available events from the channel within a timeout.
func drainEvents(ch <-chan Event, timeout time.Duration) []Event {
	var events []Event
	deadline := time.After(timeout)
	for {
		select {
		case evt, ok := <-ch:
			if !ok {
				return events
			}
			events = append(events, evt)
		case <-deadline:
			return events
		}
	}
}

// TestWatcherEngine_Dedup verifies that two events with identical DedupKey
// result in only one persisted row and one routed event (D-23).
func TestWatcherEngine_Dedup(t *testing.T) {
	engine, db := newTestEngine(t, nil)
	saveTestWatcher(t, db, "w1", "test-watcher", "mock")

	now := time.Now()
	identicalEvent := Event{
		Source:    "mock",
		Sender:    "test@example.com",
		Subject:   "same subject",
		Timestamp: now,
	}

	adapter := &MockAdapter{
		events:      []Event{identicalEvent, identicalEvent},
		listenDelay: 10 * time.Millisecond,
	}

	engine.RegisterAdapter("w1", adapter, AdapterConfig{Type: "mock", Name: "test-watcher"}, 60)

	if err := engine.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Wait for events to be processed by the writer loop.
	time.Sleep(200 * time.Millisecond)
	engine.Stop()

	// Verify: only 1 row in DB despite 2 identical events sent.
	count := countWatcherEvents(t, db, "w1")
	if count != 1 {
		t.Errorf("expected 1 event in DB (dedup), got %d", count)
	}

	// Verify: only 1 event on the routed channel.
	events := drainEvents(engine.EventCh(), 50*time.Millisecond)
	if len(events) != 1 {
		t.Errorf("expected 1 routed event, got %d", len(events))
	}
}

// TestWatcherEngine_Stop_NoLeaks verifies that starting an engine with 3 adapters
// (plus triage goroutines from Phase 18) and stopping it leaves no goroutine leaks.
// Uses a fakeSpawner to avoid real subprocess goroutine leaks (D-22, T-18-16).
func TestWatcherEngine_Stop_NoLeaks(t *testing.T) {
	defer goleak.VerifyNone(t,
		goleak.IgnoreTopFunction("database/sql.(*DB).connectionOpener"),
		goleak.IgnoreTopFunction("database/sql.(*DB).connectionResetter"),
		goleak.IgnoreAnyFunction("modernc.org"),
		goleak.IgnoreAnyFunction("poll.runtime_pollWait"),
		// Plan 17-01: adding the Google client pulls in go.opencensus.io, whose
		// stats worker is started from an init() and lives for the test binary.
		goleak.IgnoreTopFunction("go.opencensus.io/stats/view.(*worker).start"),
	)

	// Use fakeSpawner + fakeClient so triage goroutines start and stop cleanly (Task 5).
	engine, db, _, _, _ := newTestEngineWithTriage(t, nil)

	for i := 0; i < 3; i++ {
		wID := "w" + string(rune('1'+i))
		name := "watcher-" + string(rune('1'+i))
		saveTestWatcher(t, db, wID, name, "mock")

		adapter := &MockAdapter{
			events: []Event{
				{Source: "mock", Sender: "sender@test.com", Subject: "event", Timestamp: time.Now()},
			},
			listenDelay: 5 * time.Millisecond,
		}
		engine.RegisterAdapter(wID, adapter, AdapterConfig{Type: "mock", Name: name}, 60)
	}

	// Add one unrouted event to exercise triage goroutines (Task 5).
	saveTestWatcher(t, db, "w4", "watcher-unrouted", "mock")
	engine.RegisterAdapter("w4", &MockAdapter{
		events: []Event{
			{Source: "mock", Sender: "unrouted@nomatch.com", Subject: "unrouted", Timestamp: time.Now()},
		},
		listenDelay: 5 * time.Millisecond,
	}, AdapterConfig{Type: "mock", Name: "watcher-unrouted"}, 60)

	if err := engine.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	time.Sleep(200 * time.Millisecond)
	engine.Stop()

	// goleak.VerifyNone runs via defer and will fail the test if any goroutines leaked.
}

// TestWatcherEngine_KnownSenderRouting verifies that an event from a sender
// in the clients map is saved with the correct routed_to conductor.
func TestWatcherEngine_KnownSenderRouting(t *testing.T) {
	clients := map[string]ClientEntry{
		"user@company.com": {
			Conductor: "client-a",
			Group:     "client-a/inbox",
			Name:      "Client A",
		},
	}

	engine, db := newTestEngine(t, clients)
	saveTestWatcher(t, db, "w1", "test-watcher", "mock")

	adapter := &MockAdapter{
		events: []Event{
			{Source: "mock", Sender: "user@company.com", Subject: "test", Timestamp: time.Now()},
		},
	}

	engine.RegisterAdapter("w1", adapter, AdapterConfig{Type: "mock", Name: "test-watcher"}, 60)

	if err := engine.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	time.Sleep(200 * time.Millisecond)
	engine.Stop()

	routedTo := queryWatcherEventRoutedTo(t, db, "w1")
	if routedTo != "client-a" {
		t.Errorf("expected routed_to=client-a, got %q", routedTo)
	}
}

// TestWatcherEngine_UnknownSenderRouting verifies that an event from an unknown
// sender is saved with an empty routed_to field.
func TestWatcherEngine_UnknownSenderRouting(t *testing.T) {
	clients := map[string]ClientEntry{
		"known@company.com": {
			Conductor: "client-b",
			Group:     "client-b/inbox",
			Name:      "Client B",
		},
	}

	engine, db := newTestEngine(t, clients)
	saveTestWatcher(t, db, "w1", "test-watcher", "mock")

	adapter := &MockAdapter{
		events: []Event{
			{Source: "mock", Sender: "unknown@other.com", Subject: "test", Timestamp: time.Now()},
		},
	}

	engine.RegisterAdapter("w1", adapter, AdapterConfig{Type: "mock", Name: "test-watcher"}, 60)

	if err := engine.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	time.Sleep(200 * time.Millisecond)
	engine.Stop()

	routedTo := queryWatcherEventRoutedTo(t, db, "w1")
	// After Phase 18: unrouted events are sent to triage, so routed_to = "triage".
	if routedTo != "triage" {
		t.Errorf("expected routed_to=triage for unknown sender (Phase 18), got %q", routedTo)
	}
}

// TestWatcherEngine_ThreadReplyRouting verifies that an event with ParentDedupKey
// whose parent has a session_id gets ThreadSessionID set on the routed event.
func TestWatcherEngine_ThreadReplyRouting(t *testing.T) {
	engine, db := newTestEngine(t, nil)
	saveTestWatcher(t, db, "w1", "test-watcher", "mock")

	// Pre-insert a parent event with a known session_id
	_, err := db.DB().Exec(
		`INSERT INTO watcher_events (watcher_id, dedup_key, sender, subject, routed_to, session_id, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"w1", "slack-C123-1712345678.000", "slack:C123", "parent msg", "", "sess-123", time.Now().Unix(),
	)
	if err != nil {
		t.Fatalf("insert parent event: %v", err)
	}

	// Thread reply: ParentDedupKey points to the parent
	replyEvent := Event{
		Source:         "slack",
		Sender:         "slack:C123",
		Subject:        "reply msg",
		Timestamp:      time.Now(),
		CustomDedupKey: "slack-C123-1712345679.000",
		ParentDedupKey: "slack-C123-1712345678.000",
	}

	adapter := &MockAdapter{events: []Event{replyEvent}}
	engine.RegisterAdapter("w1", adapter, AdapterConfig{Type: "mock", Name: "test-watcher"}, 60)

	if err := engine.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Drain the routed event channel
	events := drainEvents(engine.EventCh(), 2*time.Second)
	engine.Stop()

	if len(events) != 1 {
		t.Fatalf("expected 1 routed event, got %d", len(events))
	}
	if events[0].ThreadSessionID != "sess-123" {
		t.Errorf("expected ThreadSessionID=sess-123, got %q", events[0].ThreadSessionID)
	}
}

// TestWatcherEngine_ThreadReplyFallback verifies that an event with ParentDedupKey
// whose parent has an empty session_id falls back to normal routing (ThreadSessionID empty).
func TestWatcherEngine_ThreadReplyFallback(t *testing.T) {
	engine, db := newTestEngine(t, nil)
	saveTestWatcher(t, db, "w1", "test-watcher", "mock")

	// Pre-insert a parent event with empty session_id
	_, err := db.DB().Exec(
		`INSERT INTO watcher_events (watcher_id, dedup_key, sender, subject, routed_to, session_id, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"w1", "slack-C123-1712345680.000", "slack:C123", "parent msg", "", "", time.Now().Unix(),
	)
	if err != nil {
		t.Fatalf("insert parent event: %v", err)
	}

	replyEvent := Event{
		Source:         "slack",
		Sender:         "slack:C123",
		Subject:        "reply msg",
		Timestamp:      time.Now(),
		CustomDedupKey: "slack-C123-1712345681.000",
		ParentDedupKey: "slack-C123-1712345680.000",
	}

	adapter := &MockAdapter{events: []Event{replyEvent}}
	engine.RegisterAdapter("w1", adapter, AdapterConfig{Type: "mock", Name: "test-watcher"}, 60)

	if err := engine.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	events := drainEvents(engine.EventCh(), 2*time.Second)
	engine.Stop()

	if len(events) != 1 {
		t.Fatalf("expected 1 routed event, got %d", len(events))
	}
	if events[0].ThreadSessionID != "" {
		t.Errorf("expected empty ThreadSessionID for fallback, got %q", events[0].ThreadSessionID)
	}
}

// TestWatcherEngine_ThreadReplyNoParent verifies that an event with ParentDedupKey
// but no parent event in DB falls back to normal routing.
func TestWatcherEngine_ThreadReplyNoParent(t *testing.T) {
	engine, db := newTestEngine(t, nil)
	saveTestWatcher(t, db, "w1", "test-watcher", "mock")

	replyEvent := Event{
		Source:         "slack",
		Sender:         "slack:C123",
		Subject:        "orphan reply",
		Timestamp:      time.Now(),
		CustomDedupKey: "slack-C123-1712345682.000",
		ParentDedupKey: "slack-C123-nonexistent.000",
	}

	adapter := &MockAdapter{events: []Event{replyEvent}}
	engine.RegisterAdapter("w1", adapter, AdapterConfig{Type: "mock", Name: "test-watcher"}, 60)

	if err := engine.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	events := drainEvents(engine.EventCh(), 2*time.Second)
	engine.Stop()

	if len(events) != 1 {
		t.Fatalf("expected 1 routed event, got %d", len(events))
	}
	if events[0].ThreadSessionID != "" {
		t.Errorf("expected empty ThreadSessionID when parent not found, got %q", events[0].ThreadSessionID)
	}
}

// TestEngine_UnroutedFlowEndToEnd is the Wave 3 integration test that proves
// the full Phase 18 pipeline works end-to-end:
//
//	unrouted event → writerLoop → triageReqCh → triageLoop → fakeSpawner →
//	result.json → reaper.scanOnce → AppendClientEntry → Router.Reload →
//	next Router.Match returns new route
//
// Per 18-RESEARCH.md §Q7 and 18-06-PLAN.md: this is the single most important
// artifact in Phase 18 — it composes Plans 18-01 through 18-04 into one test.
func TestEngine_UnroutedFlowEndToEnd(t *testing.T) {
	defer goleak.VerifyNone(t,
		goleak.IgnoreTopFunction("database/sql.(*DB).connectionOpener"),
		goleak.IgnoreTopFunction("database/sql.(*DB).connectionResetter"),
		goleak.IgnoreAnyFunction("modernc.org"),
		goleak.IgnoreAnyFunction("poll.runtime_pollWait"),
		goleak.IgnoreTopFunction("go.opencensus.io/stats/view.(*worker).start"),
	)

	// newTestEngineWithTriage wires a fakeSpawner + fakeClock + temp dirs.
	engine, db, spawner, _, triageDir := newTestEngineWithTriage(t, nil)
	// Ensure engine is always stopped — even on t.Errorf + t.Fatalf paths.
	t.Cleanup(func() { engine.Stop() })

	// Configure fakeSpawner to write result.json immediately on Spawn,
	// simulating a high-confidence triage session completing.
	spawner.resultWriter = func(req TriageRequest) {
		result := triageResult{
			RouteTo:       "client-a",
			Group:         "client-a/inbox",
			Name:          "Client A",
			Sender:        req.Event.Sender,
			Summary:       "new contact identified by triage",
			Confidence:    "high",
			ShouldPersist: true,
		}
		data, err := json.Marshal(result)
		if err != nil {
			return // test will fail via assertion below
		}
		if mkErr := os.MkdirAll(req.TriageDir, 0o700); mkErr != nil {
			return
		}
		_ = os.WriteFile(req.ResultPath, data, 0o600)
	}

	// Register one watcher + one unrouted event.
	saveTestWatcher(t, db, "w1", "test-watcher", "mock")
	unroutedSender := "new@clienta.com"
	adapter := &MockAdapter{
		events:      []Event{{Source: "mock", Sender: unroutedSender, Subject: "hello from new contact", Timestamp: time.Now()}},
		listenDelay: 5 * time.Millisecond,
	}
	engine.RegisterAdapter("w1", adapter, AdapterConfig{Type: "mock", Name: "test-watcher"}, 60)

	if err := engine.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// --- Phase 1: wait for fakeSpawner to be called (writerLoop → triageReqCh → triageLoop → Spawn) ---
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("timeout waiting for fakeSpawner.Spawn; call count=%d", spawner.callCount())
		case <-time.After(20 * time.Millisecond):
			if spawner.callCount() >= 1 {
				goto spawned
			}
		}
	}
spawned:

	// --- Phase 2: wait for result.json to be written, then trigger the reaper ---
	// fakeSpawner.resultWriter runs in a goroutine; result.json may not exist
	// immediately after Spawn returns. Poll until it appears (or timeout).
	spawner.mu.Lock()
	resultPath := spawner.calls[0].ResultPath
	spawner.mu.Unlock()

	deadline1b := time.After(3 * time.Second)
	for {
		select {
		case <-deadline1b:
			t.Fatalf("timeout waiting for result.json to be written at %s", resultPath)
		case <-time.After(10 * time.Millisecond):
			if _, statErr := os.Stat(resultPath); statErr == nil {
				goto resultReady
			}
		}
	}
resultReady:
	// Trigger the reaper immediately (bypass the 5s poll ticker).
	// engine.reaper is accessible from same package (package watcher).
	engine.reaper.scanOnce()

	// --- Phase 3: assert the full pipeline results ---

	// (a) watcher_events.routed_to updated to "client-a" by the reaper.
	routedTo := queryWatcherEventRoutedTo(t, db, "w1")
	if routedTo != "client-a" {
		t.Errorf("expected routed_to=client-a after reaper, got %q", routedTo)
	}

	// (b) clients.json on disk contains the new sender entry.
	clientsData, err := os.ReadFile(engine.cfg.ClientsPath)
	if err != nil {
		t.Fatalf("read clients.json: %v", err)
	}
	var clientsOnDisk map[string]ClientEntry
	if err := json.Unmarshal(clientsData, &clientsOnDisk); err != nil {
		t.Fatalf("parse clients.json: %v", err)
	}
	if _, ok := clientsOnDisk[unroutedSender]; !ok {
		t.Errorf("expected %q in clients.json; got keys: %v", unroutedSender, clientsOnDisk)
	}

	// (c) Router.Match now returns the new route (hot-reload succeeded).
	match := engine.cfg.Router.Match(unroutedSender)
	if match == nil {
		t.Fatalf("expected Router.Match(%q) to return non-nil after hot-reload", unroutedSender)
	}
	if match.Conductor != "client-a" {
		t.Errorf("expected conductor=client-a, got %q", match.Conductor)
	}

	// (d) Triage result file renamed to result.processed.json; result.json gone.
	dirEntries, dirErr := os.ReadDir(triageDir)
	if dirErr != nil {
		t.Fatalf("ReadDir triage dir: %v", dirErr)
	}
	var hashDirs []string
	for _, de := range dirEntries {
		if de.IsDir() {
			hashDirs = append(hashDirs, de.Name())
		}
	}
	if len(hashDirs) != 1 {
		t.Errorf("expected exactly 1 hash subdirectory in triage dir, got %d: %v", len(hashDirs), hashDirs)
	}
	if len(hashDirs) == 1 {
		hashDirPath := filepath.Join(triageDir, hashDirs[0])
		processedPath := filepath.Join(hashDirPath, "result.processed.json")
		resultPath := filepath.Join(hashDirPath, "result.json")
		if _, statErr := os.Stat(processedPath); statErr != nil {
			t.Errorf("expected result.processed.json to exist: %v", statErr)
		}
		if _, statErr := os.Stat(resultPath); statErr == nil {
			t.Error("expected result.json to be gone after reaper rename")
		}
	}

	// --- Phase 4: second event from same sender routes directly, no second spawn ---
	// After the router hot-reload, the same sender is now a known client. Inject a
	// second event directly into the engine's eventCh (same-package access) and verify:
	// (1) it routes to "client-a" (not "triage") in the DB, (2) spawner still has
	// exactly 1 call (no second triage spawn was triggered).
	saveTestWatcher(t, db, "w2", "test-watcher-2", "mock")
	tracker2 := NewHealthTracker("test-watcher-2", 60)
	secondEvent := Event{
		Source:    "mock",
		Sender:    unroutedSender,
		Subject:   "follow-up from now-known sender",
		Timestamp: time.Now(),
	}
	// Inject directly into the engine pipeline (writerLoop reads from eventCh).
	engine.eventCh <- eventEnvelope{
		event:     secondEvent,
		watcherID: "w2",
		tracker:   tracker2,
	}

	// Wait for the second event to be persisted and routed to "client-a".
	deadline2 := time.After(3 * time.Second)
	for {
		select {
		case <-deadline2:
			t.Fatalf("timeout waiting for second event to be routed directly")
		case <-time.After(30 * time.Millisecond):
			var routedTo2 string
			qErr := db.DB().QueryRow(
				`SELECT routed_to FROM watcher_events WHERE watcher_id = ? ORDER BY id LIMIT 1`, "w2",
			).Scan(&routedTo2)
			if qErr == nil && routedTo2 == "client-a" {
				goto routedDirectly
			}
		}
	}
routedDirectly:

	// Spawner must still have exactly 1 call — the second event was routed directly.
	if spawner.callCount() != 1 {
		t.Errorf("expected exactly 1 spawn total; second event should route directly; got %d spawns", spawner.callCount())
	}

	engine.Stop()
	// goleak.VerifyNone runs via defer.
}

// TestWatcherEngine_StopCancelsAdapters verifies that Stop() calls Teardown()
// on all registered adapters.
func TestWatcherEngine_StopCancelsAdapters(t *testing.T) {
	engine, db := newTestEngine(t, nil)
	saveTestWatcher(t, db, "w1", "watcher-1", "mock")
	saveTestWatcher(t, db, "w2", "watcher-2", "mock")

	adapter1 := &MockAdapter{} // No events, just blocks on ctx
	adapter2 := &MockAdapter{} // No events, just blocks on ctx

	engine.RegisterAdapter("w1", adapter1, AdapterConfig{Type: "mock", Name: "watcher-1"}, 60)
	engine.RegisterAdapter("w2", adapter2, AdapterConfig{Type: "mock", Name: "watcher-2"}, 60)

	if err := engine.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Give adapters time to start.
	time.Sleep(50 * time.Millisecond)
	engine.Stop()

	if !adapter1.teardownCalled {
		t.Error("adapter1.Teardown() was not called")
	}
	if !adapter2.teardownCalled {
		t.Error("adapter2.Teardown() was not called")
	}
}

// TestWatcherEngine_Stop_ClosesExportedChannels asserts that after Stop()
// returns, the exported EventCh and HealthCh are closed so consumers receive
// (_, false) instead of leaking forever (critical-hunt #9, V1.9 T5).
func TestWatcherEngine_Stop_ClosesExportedChannels(t *testing.T) {
	engine, _, _, _, _ := newTestEngineWithTriage(t, nil)

	if err := engine.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	engine.Stop()

	select {
	case _, ok := <-engine.EventCh():
		if ok {
			t.Errorf("EventCh: expected closed (ok=false), got ok=true")
		}
	case <-time.After(time.Second):
		t.Errorf("EventCh: expected closed channel, got blocking read")
	}

	select {
	case _, ok := <-engine.HealthCh():
		if ok {
			t.Errorf("HealthCh: expected closed (ok=false), got ok=true")
		}
	case <-time.After(time.Second):
		t.Errorf("HealthCh: expected closed channel, got blocking read")
	}
}
