package watcher

import (
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/sendqueue"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// saveTestConductor stores the session of the conductor name, with id
// "cond-<name>", and returns that id.
func saveTestConductor(t *testing.T, db *statedb.StateDB, name string) string {
	t.Helper()
	id := "cond-" + name
	now := time.Now()
	if err := db.SaveInstance(&statedb.InstanceRow{
		ID: id, Title: session.ConductorSessionTitle(name), Tool: "claude",
		ProjectPath: t.TempDir(), GroupPath: "conductors", Status: "idle",
		CreatedAt: now, LastAccessed: now, IsConductor: true,
	}); err != nil {
		t.Fatalf("SaveInstance: %v", err)
	}
	return id
}

// spawnRecorder stands in for sendqueue.SpawnWorker: tests never start a
// real `session send-worker`.
type spawnRecorder struct {
	mu      sync.Mutex
	targets []string
}

func (r *spawnRecorder) spawn(_, sessionID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.targets = append(r.targets, sessionID)
	return nil
}

func (r *spawnRecorder) started(sessionID string) bool {
	return r.count(sessionID) > 0
}

func (r *spawnRecorder) count(sessionID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, id := range r.targets {
		if id == sessionID {
			n++
		}
	}
	return n
}

func newTestOutbox(t *testing.T, db *statedb.StateDB, deadline time.Duration) (*ConductorOutbox, string, *spawnRecorder) {
	t.Helper()
	t.Setenv("AGENTDECK_EVENTS_BUS", "off")
	dir := sendqueue.Dir(filepath.Dir(db.Path()))
	spawned := &spawnRecorder{}
	return NewConductorOutbox(OutboxConfig{
		Profile: "outboxtest", Dir: dir, DB: db, Deadline: deadline,
		SpawnWorker: spawned.spawn, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}), dir, spawned
}

// TestConductorOutbox_QueuesARoutedEventOnce: the maintainer's test (c) at
// the outbox. The same event routed twice is one record, addressed to the
// conductor's session, waiting for it while it is stopped, and one worker
// start.
func TestConductorOutbox_QueuesARoutedEventOnce(t *testing.T) {
	db := newTestDB(t)
	id := saveTestConductor(t, db, "demo")
	outbox, dir, spawned := newTestOutbox(t, db, 0)
	evt := Event{Source: "slack", Sender: "alice", Body: "line one\nline two", RoutedTo: "demo", CustomDedupKey: "slack-C1-1.0"}

	for i := 0; i < 2; i++ {
		if err := outbox.Enqueue("w1", "inbox", evt); err != nil {
			t.Fatalf("Enqueue %d: %v", i, err)
		}
	}
	recs, err := sendqueue.List(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("%d records for one event routed twice, want 1", len(recs))
	}
	r := recs[0]
	if r.SessionID != id || r.SessionTitle != "conductor-demo" || r.Tool != "claude" || r.State != sendqueue.StateQueued {
		t.Fatalf("record target = %+v", r)
	}
	if r.Message != "[slack] alice: line one line two" || r.Sender != "watcher:inbox" {
		t.Fatalf("message %q from %q", r.Message, r.Sender)
	}
	if r.Key != DeliveryKey("w1", evt) || !r.WaitWhileStopped || r.Deadline != "" {
		t.Fatalf("key %q wait %v deadline %q, want the event's key, waiting, no deadline", r.Key, r.WaitWhileStopped, r.Deadline)
	}
	if n := spawned.count(id); n != 1 {
		t.Fatalf("worker started %d times, want once", n)
	}
}

// TestConductorOutbox_DeadlineFromConfig: [watcher] delivery_deadline
// becomes the record's deadline.
func TestConductorOutbox_DeadlineFromConfig(t *testing.T) {
	db := newTestDB(t)
	saveTestConductor(t, db, "demo")
	outbox, dir, _ := newTestOutbox(t, db, 72*time.Hour)
	before := time.Now()
	if err := outbox.Enqueue("w1", "inbox", Event{Sender: "alice", Body: "x", RoutedTo: "demo"}); err != nil {
		t.Fatal(err)
	}
	recs, _ := sendqueue.List(dir, "")
	if len(recs) != 1 {
		t.Fatalf("%d records", len(recs))
	}
	deadline, err := time.Parse(time.RFC3339Nano, recs[0].Deadline)
	if err != nil || deadline.Before(before.Add(72*time.Hour)) || deadline.After(time.Now().Add(72*time.Hour)) {
		t.Fatalf("deadline %q, want 72h after queueing", recs[0].Deadline)
	}
}

// TestConductorOutbox_AConductorWithNoSessionQueuesNothing: there is no
// session to address, so nothing is queued and the error says why.
func TestConductorOutbox_AConductorWithNoSessionQueuesNothing(t *testing.T) {
	db := newTestDB(t)
	outbox, dir, spawned := newTestOutbox(t, db, 0)
	err := outbox.Enqueue("w1", "inbox", Event{Sender: "alice", Body: "x", RoutedTo: "ghost"})
	if err == nil || !strings.Contains(err.Error(), `"conductor-ghost"`) {
		t.Fatalf("Enqueue for a conductor with no session: %v", err)
	}
	if recs, _ := sendqueue.List(dir, ""); len(recs) != 0 || len(spawned.targets) != 0 {
		t.Fatalf("%d records, %d worker starts, want none", len(recs), len(spawned.targets))
	}
}

// TestConductorOutbox_SkipsAnArchivedConductorSession: an archived session
// keeps the conductor's title in storage. An event is queued for the live
// session even when the archived one comes first, and with only the archived
// one nothing is queued.
func TestConductorOutbox_SkipsAnArchivedConductorSession(t *testing.T) {
	db := newTestDB(t)
	now := time.Now()
	if err := db.SaveInstance(&statedb.InstanceRow{
		ID: "cond-demo-old", Title: session.ConductorSessionTitle("demo"), Tool: "claude",
		ProjectPath: t.TempDir(), GroupPath: "conductors", Status: "stopped",
		CreatedAt: now, LastAccessed: now, IsConductor: true, ArchivedAt: now,
	}); err != nil {
		t.Fatalf("SaveInstance: %v", err)
	}
	outbox, dir, spawned := newTestOutbox(t, db, 0)

	err := outbox.Enqueue("w1", "inbox", Event{Sender: "alice", Body: "only archived", RoutedTo: "demo"})
	if err == nil || !strings.Contains(err.Error(), "not archived") {
		t.Fatalf("Enqueue with only an archived conductor session: %v", err)
	}
	if recs, _ := sendqueue.List(dir, ""); len(recs) != 0 || len(spawned.targets) != 0 {
		t.Fatalf("%d records, %d worker starts for an archived conductor, want none", len(recs), len(spawned.targets))
	}

	if err := db.SaveInstance(&statedb.InstanceRow{
		ID: "cond-demo", Title: session.ConductorSessionTitle("demo"), Tool: "claude",
		ProjectPath: t.TempDir(), GroupPath: "conductors", Status: "idle", Order: 1,
		CreatedAt: now, LastAccessed: now, IsConductor: true,
	}); err != nil {
		t.Fatalf("SaveInstance: %v", err)
	}
	if err := outbox.Enqueue("w1", "inbox", Event{Sender: "alice", Body: "live", RoutedTo: "demo"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	recs, err := sendqueue.List(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].SessionID != "cond-demo" {
		t.Fatalf("records %+v, want one for the live session cond-demo", recs)
	}
	if spawned.started("cond-demo-old") || !spawned.started("cond-demo") {
		t.Fatalf("worker starts %v, want only cond-demo", spawned.targets)
	}
}

// TestConductorOutbox_FindsARenamedConductorByStoredName: the stable
// conductor identity (conductor_name in tool_data) wins over the title, so a
// renamed conductor still gets its routed events; an archived session with
// the same stored name is skipped, and a session titled "conductor-demo"
// whose stored name is another conductor is not demo.
func TestConductorOutbox_FindsARenamedConductorByStoredName(t *testing.T) {
	db := newTestDB(t)
	now := time.Now()
	save := func(id, title, stored string, order int, archived bool) {
		t.Helper()
		row := &statedb.InstanceRow{
			ID: id, Title: title, Tool: "claude",
			ProjectPath: t.TempDir(), GroupPath: "conductors", Status: "idle", Order: order,
			CreatedAt: now, LastAccessed: now, IsConductor: true,
			ToolData: session.WriteConductorNameToToolData(nil, stored),
		}
		if archived {
			row.ArchivedAt = now
		}
		if err := db.SaveInstance(row); err != nil {
			t.Fatalf("SaveInstance: %v", err)
		}
	}
	save("cond-other", session.ConductorSessionTitle("demo"), "other", 0, false)
	save("cond-demo-old", "old boss", "demo", 1, true)
	save("cond-demo", "renamed boss", "demo", 2, false)
	outbox, dir, spawned := newTestOutbox(t, db, 0)

	if err := outbox.Enqueue("w1", "inbox", Event{Sender: "alice", Body: "x", RoutedTo: "demo"}); err != nil {
		t.Fatalf("Enqueue for a renamed conductor: %v", err)
	}
	recs, err := sendqueue.List(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].SessionID != "cond-demo" {
		t.Fatalf("records %+v, want one for the renamed live session cond-demo", recs)
	}
	if spawned.started("cond-other") || spawned.started("cond-demo-old") || !spawned.started("cond-demo") {
		t.Fatalf("worker starts %v, want only cond-demo", spawned.targets)
	}
}

// TestConductorOutbox_ResumeStartsTheWorkersOfWaitingEvents: after a
// reboot, the conductors with routed events still waiting get a worker
// again; finished deliveries and plain `session send --queue` records do
// not.
func TestConductorOutbox_ResumeStartsTheWorkersOfWaitingEvents(t *testing.T) {
	db := newTestDB(t)
	demo := saveTestConductor(t, db, "demo")
	ops := saveTestConductor(t, db, "ops")
	idle := saveTestConductor(t, db, "idle")
	outbox, dir, spawned := newTestOutbox(t, db, 0)
	for _, evt := range []Event{
		{Sender: "a", Subject: "1", Body: "1", RoutedTo: "demo"},
		{Sender: "a", Subject: "2", Body: "2", RoutedTo: "demo"},
		{Sender: "b", Subject: "3", Body: "3", RoutedTo: "ops"},
		{Sender: "c", Subject: "4", Body: "4", RoutedTo: "idle"},
	} {
		if err := outbox.Enqueue("w1", "inbox", evt); err != nil {
			t.Fatal(err)
		}
	}
	recs, _ := sendqueue.List(dir, idle)
	if _, err := sendqueue.Update(dir, recs[0].SendID, time.Now(), func(r *sendqueue.Record) { r.State = sendqueue.StateLanded }); err != nil {
		t.Fatal(err)
	}
	if _, err := sendqueue.Enqueue("outboxtest", dir, sendqueue.Send{SessionID: "plain", Running: true, Message: "m", Sender: "cli"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	spawned.targets = nil

	outbox.Resume()

	if spawned.count(demo) != 1 || spawned.count(ops) != 1 || spawned.count(idle) != 0 || spawned.count("plain") != 0 {
		t.Fatalf("workers started for %v, want one each for %s and %s", spawned.targets, demo, ops)
	}
}

// TestPendingDeliveries_PerWatcherAndConductor: what `watcher status` and
// the health state report: per watcher, per conductor, how many routed events
// still wait and since when.
func TestPendingDeliveries_PerWatcherAndConductor(t *testing.T) {
	db := newTestDB(t)
	saveTestConductor(t, db, "demo")
	saveTestConductor(t, db, "ops")
	outbox, dir, _ := newTestOutbox(t, db, 0)
	for _, q := range []struct {
		watcher string
		evt     Event
	}{
		{"w1", Event{Sender: "a", Subject: "1", Body: "1", RoutedTo: "demo"}},
		{"w1", Event{Sender: "a", Subject: "2", Body: "2", RoutedTo: "demo"}},
		{"w1", Event{Sender: "a", Subject: "3", Body: "3", RoutedTo: "ops"}},
		{"w2", Event{Sender: "b", Subject: "4", Body: "4", RoutedTo: "demo"}},
		{"w2", Event{Sender: "b", Subject: "5", Body: "5", RoutedTo: "demo"}},
	} {
		if err := outbox.Enqueue(q.watcher, q.watcher, q.evt); err != nil {
			t.Fatal(err)
		}
	}
	recs, _ := sendqueue.List(dir, "")
	oldest, _ := time.Parse(time.RFC3339Nano, recs[0].CreatedAt)
	// w2's first event was delivered.
	if _, err := sendqueue.Update(dir, recs[3].SendID, time.Now(), func(r *sendqueue.Record) { r.State = sendqueue.StateLanded }); err != nil {
		t.Fatal(err)
	}

	pending, err := outbox.Pending()
	if err != nil {
		t.Fatal(err)
	}
	w1 := pending["w1"]
	if len(w1) != 2 || w1[0].Conductor != "demo" || w1[0].Count != 2 || !w1[0].Oldest.Equal(oldest) || w1[1].Conductor != "ops" || w1[1].Count != 1 {
		t.Fatalf("w1 pending = %+v", w1)
	}
	if w2 := pending["w2"]; len(w2) != 1 || w2[0].Conductor != "demo" || w2[0].Count != 1 {
		t.Fatalf("w2 pending = %+v", w2)
	}
}

// TestConductorMessage_UsesFullBody pins the second half of the
// Slack-truncation fix: the conductor gets the full message Body (not the
// first-line/200-byte Subject) as a single line.
func TestConductorMessage_UsesFullBody(t *testing.T) {
	full := "first line\nsecond line that used to be dropped\nthird line"
	msg := ConductorMessage(Event{
		Source:   "slack",
		Sender:   "slack:D0B434J6BTR",
		Subject:  "first line", // first-line label the bug used to deliver
		Body:     full,
		RoutedTo: "intelas-conductor",
	})
	if !strings.Contains(msg, "second line that used to be dropped") || !strings.Contains(msg, "third line") {
		t.Errorf("message dropped body lines: %q", msg)
	}
	if strings.ContainsAny(msg, "\n\r") {
		t.Errorf("message must be a single line, got %q", msg)
	}
	if want := "[slack] slack:D0B434J6BTR: "; !strings.HasPrefix(msg, want) {
		t.Errorf("prefix: want %q, got %q", want, msg)
	}
}

// TestConductorMessage_FallsBackToSubject covers v1 / pre-fix events that
// carry no Body: the conductor still gets the Subject.
func TestConductorMessage_FallsBackToSubject(t *testing.T) {
	msg := ConductorMessage(Event{Source: "slack", Sender: "slack:unknown", Subject: "only a subject", RoutedTo: "intelas-conductor"})
	if want := "[slack] slack:unknown: only a subject"; msg != want {
		t.Errorf("fallback: want %q, got %q", want, msg)
	}
}

// TestConductorOutbox_IgnoresAStoredNameOnAnUnflaggedRow (#2708): a worker
// row stamped with the conductor name (it once held the exact conductor
// title) but not flagged IsConductor never receives the conductor's
// deliveries, even when it comes first.
func TestConductorOutbox_IgnoresAStoredNameOnAnUnflaggedRow(t *testing.T) {
	db := newTestDB(t)
	now := time.Now()
	for i, r := range []struct {
		id, title string
		flagged   bool
	}{{"worker", "worker", false}, {"cond-demo", "renamed boss", true}} {
		if err := db.SaveInstance(&statedb.InstanceRow{
			ID: r.id, Title: r.title, Tool: "claude",
			ProjectPath: t.TempDir(), GroupPath: "conductors", Status: "idle", Order: i,
			CreatedAt: now, LastAccessed: now, IsConductor: r.flagged,
			ToolData: session.WriteConductorNameToToolData(nil, "demo"),
		}); err != nil {
			t.Fatalf("SaveInstance: %v", err)
		}
	}
	outbox, dir, spawned := newTestOutbox(t, db, 0)
	if err := outbox.Enqueue("w1", "inbox", Event{Sender: "alice", Body: "x", RoutedTo: "demo"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	recs, err := sendqueue.List(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].SessionID != "cond-demo" {
		t.Fatalf("records %+v, want one for the flagged conductor cond-demo", recs)
	}
	if spawned.started("worker") {
		t.Fatalf("worker started for the unflagged row: %v", spawned.targets)
	}
}

// TestConductorOutbox_CapsPendingDeliveriesPerWatcherAndConductor: a
// loopback webhook with no secret takes events from any local process, and
// each new body is a new key. The outbox stops queueing one watcher's events
// for a conductor once it holds MaxPending of them undelivered (or
// MaxPendingBytes of their message text) and refuses the new event with
// ErrDeliveryQueueFull. A replay of an event already queued is still
// "already queued", another conductor is not affected, and once the queue
// drains events are queued again, with one line naming the watcher.
func TestConductorOutbox_CapsPendingDeliveriesPerWatcherAndConductor(t *testing.T) {
	db := newTestDB(t)
	id := saveTestConductor(t, db, "demo")
	saveTestConductor(t, db, "other")
	t.Setenv("AGENTDECK_EVENTS_BUS", "off")
	dir := sendqueue.Dir(filepath.Dir(db.Path()))
	spawned := &spawnRecorder{}
	var logBuf strings.Builder
	var logMu sync.Mutex
	outbox := NewConductorOutbox(OutboxConfig{
		Profile: "outboxtest", Dir: dir, DB: db, SpawnWorker: spawned.spawn,
		MaxPending: 3,
		Logger:     slog.New(slog.NewTextHandler(&lockedWriter{w: &logBuf, mu: &logMu}, nil)),
	})
	ev := func(conductor string, i int) Event {
		return Event{Source: "webhook", Sender: "local", Body: "body " + strconv.Itoa(i), RoutedTo: conductor, CustomDedupKey: "hook-" + strconv.Itoa(i)}
	}
	for i := 0; i < 3; i++ {
		if err := outbox.Enqueue("w1", "hook", ev("demo", i)); err != nil {
			t.Fatalf("Enqueue %d: %v", i, err)
		}
	}
	for i := 3; i < 6; i++ {
		if err := outbox.Enqueue("w1", "hook", ev("demo", i)); !errors.Is(err, ErrDeliveryQueueFull) {
			t.Fatalf("Enqueue %d over the cap: %v, want ErrDeliveryQueueFull", i, err)
		}
	}
	if err := outbox.Enqueue("w1", "hook", ev("demo", 0)); err != nil {
		t.Fatalf("replay of a queued event over the cap: %v, want already queued", err)
	}
	if recs, _ := sendqueue.List(dir, id); len(recs) != 3 {
		t.Fatalf("%d records for demo, want the cap of 3", len(recs))
	}
	if err := outbox.Enqueue("w1", "hook", ev("other", 0)); err != nil {
		t.Fatalf("another conductor is capped too: %v", err)
	}

	// The worker delivers one: there is room again.
	recs, _ := sendqueue.List(dir, id)
	recs[0].State = sendqueue.StateLanded
	if err := sendqueue.Save(dir, recs[0]); err != nil {
		t.Fatal(err)
	}
	if err := outbox.Enqueue("w1", "hook", ev("demo", 9)); err != nil {
		t.Fatalf("Enqueue after the queue drained: %v", err)
	}
	logMu.Lock()
	logged := logBuf.String()
	logMu.Unlock()
	if n := strings.Count(logged, "watcher_delivery_queue_has_room"); n != 1 || !strings.Contains(logged, "watcher_delivery_queue_has_room watcher=hook conductor=demo") {
		t.Fatalf("%d has-room lines, want one naming watcher hook and conductor demo:\n%s", n, logged)
	}
}

// TestConductorOutbox_AFloodFromOneWatcherDoesNotRefuseAnother: an unsigned
// loopback webhook and a signed GitHub watcher route to the same conductor.
// A local flood fills the webhook's share of the queue; the GitHub watcher's
// events are still queued, and when the webhook gets room again the line
// that says so names the webhook, not the watcher whose event came next.
func TestConductorOutbox_AFloodFromOneWatcherDoesNotRefuseAnother(t *testing.T) {
	db := newTestDB(t)
	id := saveTestConductor(t, db, "demo")
	t.Setenv("AGENTDECK_EVENTS_BUS", "off")
	dir := sendqueue.Dir(filepath.Dir(db.Path()))
	spawned := &spawnRecorder{}
	var logBuf strings.Builder
	var logMu sync.Mutex
	outbox := NewConductorOutbox(OutboxConfig{
		Profile: "outboxtest", Dir: dir, DB: db, SpawnWorker: spawned.spawn,
		MaxPending: 3,
		Logger:     slog.New(slog.NewTextHandler(&lockedWriter{w: &logBuf, mu: &logMu}, nil)),
	})
	ev := func(prefix string, i int) Event {
		return Event{Source: prefix, Sender: prefix, Body: prefix + " " + strconv.Itoa(i), RoutedTo: "demo", CustomDedupKey: prefix + "-" + strconv.Itoa(i)}
	}
	for i := 0; i < 10; i++ {
		err := outbox.Enqueue("w-hook", "local-hook", ev("hook", i))
		if i < 3 && err != nil {
			t.Fatalf("hook Enqueue %d: %v", i, err)
		}
		if i >= 3 && !errors.Is(err, ErrDeliveryQueueFull) {
			t.Fatalf("hook Enqueue %d over its cap: %v, want ErrDeliveryQueueFull", i, err)
		}
	}
	for i := 0; i < 3; i++ {
		if err := outbox.Enqueue("w-gh", "github", ev("gh", i)); err != nil {
			t.Fatalf("signed github event %d refused by the webhook's flood: %v", i, err)
		}
	}
	if err := outbox.Enqueue("w-gh", "github", ev("gh", 3)); !errors.Is(err, ErrDeliveryQueueFull) {
		t.Fatalf("github over its own cap: %v, want ErrDeliveryQueueFull", err)
	}
	if recs, _ := sendqueue.List(dir, id); len(recs) != 6 {
		t.Fatalf("%d records for demo, want 3 per watcher", len(recs))
	}

	// One webhook event is delivered; a github event is refused again first,
	// then the webhook gets its room back.
	recs, _ := sendqueue.List(dir, id)
	for _, r := range recs {
		if strings.HasPrefix(r.Key, DeliveryKey("w-hook", ev("hook", 0))) {
			r.State = sendqueue.StateLanded
			if err := sendqueue.Save(dir, r); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := outbox.Enqueue("w-gh", "github", ev("gh", 4)); !errors.Is(err, ErrDeliveryQueueFull) {
		t.Fatalf("github got the webhook's room: %v", err)
	}
	if err := outbox.Enqueue("w-hook", "local-hook", ev("hook", 20)); err != nil {
		t.Fatalf("hook Enqueue after one was delivered: %v", err)
	}
	logMu.Lock()
	logged := logBuf.String()
	logMu.Unlock()
	if !strings.Contains(logged, "watcher_delivery_queue_has_room watcher=local-hook conductor=demo") || strings.Contains(logged, "has_room watcher=github") {
		t.Fatalf("has-room lines do not name the watcher that got room:\n%s", logged)
	}
}

// TestConductorOutbox_CapsPendingBytesPerConductor: the byte cap stops a few
// large bodies (a webhook body may be 1 MiB) before the entry cap does.
func TestConductorOutbox_CapsPendingBytesPerConductor(t *testing.T) {
	db := newTestDB(t)
	saveTestConductor(t, db, "demo")
	t.Setenv("AGENTDECK_EVENTS_BUS", "off")
	dir := sendqueue.Dir(filepath.Dir(db.Path()))
	spawned := &spawnRecorder{}
	outbox := NewConductorOutbox(OutboxConfig{
		Profile: "outboxtest", Dir: dir, DB: db, SpawnWorker: spawned.spawn,
		MaxPendingBytes: 3500, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	big := strings.Repeat("x", 1000)
	for i := 0; i < 3; i++ {
		if err := outbox.Enqueue("w1", "hook", Event{Sender: "local", Body: big + strconv.Itoa(i), RoutedTo: "demo", CustomDedupKey: "big-" + strconv.Itoa(i)}); err != nil {
			t.Fatalf("Enqueue %d: %v", i, err)
		}
	}
	if err := outbox.Enqueue("w1", "hook", Event{Sender: "local", Body: big + "3", RoutedTo: "demo", CustomDedupKey: "big-3"}); !errors.Is(err, ErrDeliveryQueueFull) {
		t.Fatalf("Enqueue over the byte cap: %v, want ErrDeliveryQueueFull", err)
	}
}

// TestConductorOutbox_DefaultCaps: an outbox configured with no caps gets
// the documented defaults, never "unlimited".
func TestConductorOutbox_DefaultCaps(t *testing.T) {
	o := NewConductorOutbox(OutboxConfig{})
	if o.maxPending != DefaultMaxPendingDeliveries || o.maxPendingBytes != DefaultMaxPendingDeliveryBytes {
		t.Fatalf("caps %d / %d, want %d / %d", o.maxPending, o.maxPendingBytes, DefaultMaxPendingDeliveries, DefaultMaxPendingDeliveryBytes)
	}
}

type lockedWriter struct {
	w  io.Writer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
