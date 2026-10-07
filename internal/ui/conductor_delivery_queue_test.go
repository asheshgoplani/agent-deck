package ui

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/watcher"
)

// sentLog records what a conductorQueue sends.
type sentLog struct {
	mu   sync.Mutex
	sent []conductorDelivery
}

func (l *sentLog) add(d conductorDelivery) {
	l.mu.Lock()
	l.sent = append(l.sent, d)
	l.mu.Unlock()
}

func (l *sentLog) texts() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.sent))
	for i, d := range l.sent {
		out[i] = d.text()
	}
	return out
}

// blockingSend returns a send func whose first call blocks until release is
// called, so a test can fill a backlog behind a delivery in flight, and a
// channel closed once that first call has started.
func blockingSend(t *testing.T, log *sentLog) (send func(conductorDelivery), started <-chan struct{}, release func()) {
	t.Helper()
	start := make(chan struct{})
	hold := make(chan struct{})
	var first, released sync.Once
	release = func() { released.Do(func() { close(hold) }) }
	t.Cleanup(release)
	send = func(d conductorDelivery) {
		blocked := false
		first.Do(func() {
			blocked = true
			close(start)
		})
		if blocked {
			<-hold
		}
		log.add(d)
	}
	return send, start, release
}

// busyQueue returns a queue for conductor "demo" whose first delivery is in
// flight and held until release.
func busyQueue(t *testing.T) (*conductorQueue, *sentLog, func()) {
	t.Helper()
	log := &sentLog{}
	send, started, release := blockingSend(t, log)
	q := newConductorQueue(send)
	q.enqueue(conductorDelivery{Conductor: "demo", Text: "in flight"})
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first delivery never started")
	}
	return q, log, release
}

func event(conductor, text string) conductorDelivery {
	return conductorDelivery{Conductor: conductor, Text: text}
}

func alert(conductor, watcherName, text string) conductorDelivery {
	return conductorDelivery{Conductor: conductor, Text: text, Alert: watcherName}
}

// queueIdle waits until q has no runner left for any conductor.
func queueIdle(t *testing.T, q *conductorQueue) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		q.mu.Lock()
		n := len(q.backlogs)
		q.mu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("conductor runner did not exit after its backlog drained")
}

// TestConductorQueue_OneConductorDeliversOneAtATimeInOrder: deliveries to one
// conductor never overlap and keep their order, so a burst cannot merge in the
// composer or arrive reordered.
func TestConductorQueue_OneConductorDeliversOneAtATimeInOrder(t *testing.T) {
	var inflight, maxInflight atomic.Int32
	log := &sentLog{}
	q := newConductorQueue(func(d conductorDelivery) {
		n := inflight.Add(1)
		for {
			m := maxInflight.Load()
			if n <= m || maxInflight.CompareAndSwap(m, n) {
				break
			}
		}
		log.add(d)
		time.Sleep(200 * time.Microsecond)
		inflight.Add(-1)
	})
	var want []string
	for i := 0; i < 50; i++ {
		want = append(want, strconv.Itoa(i))
		q.enqueue(event("demo", strconv.Itoa(i)))
	}
	queueIdle(t, q)
	if m := maxInflight.Load(); m != 1 {
		t.Errorf("deliveries to one conductor overlapped: %d in flight at once", m)
	}
	if got := log.texts(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("delivered %v, want %v", got, want)
	}
}

// TestConductorQueue_NeverBlocksAndConductorsAreIndependent: while one
// conductor's delivery hangs (an occupied composer is held for seconds),
// queuing more for it returns at once and another conductor still gets its
// deliveries.
func TestConductorQueue_NeverBlocksAndConductorsAreIndependent(t *testing.T) {
	q, log, release := busyQueue(t)
	queued := make(chan struct{})
	go func() {
		for i := 0; i < 50; i++ {
			q.enqueue(event("demo", strconv.Itoa(i)))
		}
		close(queued)
	}()
	select {
	case <-queued:
	case <-time.After(5 * time.Second):
		t.Fatal("enqueue blocked behind a delivery in flight")
	}

	q.enqueue(event("other", "for other"))
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(strings.Join(log.texts(), ","), "for other") {
		if time.Now().After(deadline) {
			t.Fatal("a busy conductor held up another conductor's delivery")
		}
		time.Sleep(time.Millisecond)
	}
	if n := len(log.texts()); n != 1 {
		t.Fatalf("%d deliveries done while the busy conductor's first one was in flight, want only the other conductor's", n)
	}

	release()
	queueIdle(t, q)
	if n := len(log.texts()); n != 52 {
		t.Fatalf("delivered %d, want 52", n)
	}
}

// TestConductorQueue_SurvivesAPanickingDelivery: a panic in one delivery must
// not leave the conductor's backlog stuck.
func TestConductorQueue_SurvivesAPanickingDelivery(t *testing.T) {
	done := make(chan struct{})
	q := newConductorQueue(func(d conductorDelivery) {
		if d.Text == "bad" {
			panic("boom")
		}
		close(done)
	})
	q.enqueue(event("demo", "bad"))
	q.enqueue(event("demo", "good"))
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the delivery after a panic never ran")
	}
	queueIdle(t, q)
}

// TestConductorQueue_BoundsTheBacklogAndSaysWhatItDropped: while the pane
// stays busy the backlog keeps the newest maxConductorBacklog events, and the
// conductor is told how many older ones were dropped before it gets the rest.
func TestConductorQueue_BoundsTheBacklogAndSaysWhatItDropped(t *testing.T) {
	q, log, release := busyQueue(t)
	const extra = 10
	for i := 1; i <= maxConductorBacklog+extra; i++ {
		q.enqueue(event("demo", "e"+strconv.Itoa(i)))
	}
	q.mu.Lock()
	queued := len(q.backlogs["demo"].items)
	q.mu.Unlock()
	if queued != maxConductorBacklog+1 {
		t.Fatalf("backlog holds %d items, want %d events plus one notice", queued, maxConductorBacklog)
	}

	release()
	queueIdle(t, q)
	got := log.texts()
	if len(got) != maxConductorBacklog+2 {
		t.Fatalf("delivered %d, want the one in flight, the notice and %d events", len(got), maxConductorBacklog)
	}
	if !strings.Contains(got[1], strconv.Itoa(extra)+" routed watcher event(s)") {
		t.Fatalf("second delivery = %q, want the notice for %d dropped events", got[1], extra)
	}
	for i, text := range got[2:] {
		if want := "e" + strconv.Itoa(extra+1+i); text != want {
			t.Fatalf("event %d delivered as %q, want %q (the newest events, in order)", i, text, want)
		}
	}
}

// TestConductorQueue_HealthAlertsGoFirstAndKeepTheNewestPerWatcher: a health
// alert never waits behind a backlog of routed events, and a newer alert from
// the same watcher replaces the pending one.
func TestConductorQueue_HealthAlertsGoFirstAndKeepTheNewestPerWatcher(t *testing.T) {
	q, log, release := busyQueue(t)
	q.enqueue(event("demo", "e1"))
	q.enqueue(event("demo", "e2"))
	q.enqueue(alert("demo", "w1", "w1 old"))
	q.enqueue(alert("demo", "w2", "w2"))
	q.enqueue(event("demo", "e3"))
	q.enqueue(alert("demo", "w1", "w1 new"))

	release()
	queueIdle(t, q)
	want := "in flight,w1 new,w2,e1,e2,e3"
	if got := strings.Join(log.texts(), ","); got != want {
		t.Fatalf("delivered %s, want %s", got, want)
	}
}

// TestConductorQueue_StopFinishesTheDeliveryInFlightAndReturnsTheRest: a quit
// waits for the delivery in flight, starts no other, and hands back the queued
// routed events (not the alerts) for the caller to record.
func TestConductorQueue_StopFinishesTheDeliveryInFlightAndReturnsTheRest(t *testing.T) {
	q, log, release := busyQueue(t)
	q.enqueue(event("demo", "e1"))
	q.enqueue(alert("demo", "w1", "alert"))
	q.enqueue(event("demo", "e2"))

	stopped := make(chan []conductorDelivery, 1)
	go func() { stopped <- q.stop(5 * time.Second) }()
	select {
	case <-stopped:
		t.Fatal("stop returned while a delivery was still in flight")
	case <-time.After(100 * time.Millisecond):
	}
	release()
	var pending []conductorDelivery
	select {
	case pending = <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("stop did not return after the delivery in flight finished")
	}
	var texts []string
	for _, d := range pending {
		texts = append(texts, d.text())
	}
	if got := strings.Join(texts, ","); got != "e1,e2" {
		t.Fatalf("stop returned %s, want e1,e2", got)
	}

	q.enqueue(event("demo", "after stop"))
	time.Sleep(50 * time.Millisecond)
	if got := strings.Join(log.texts(), ","); got != "in flight" {
		t.Fatalf("delivered %s, want only the delivery that was in flight", got)
	}
}

// TestConductorQueue_StopGivesUpOnAHungDelivery: a delivery that never
// returns cannot hold a quit past the wait.
func TestConductorQueue_StopGivesUpOnAHungDelivery(t *testing.T) {
	q, _, _ := busyQueue(t)
	q.enqueue(event("demo", "e1"))
	start := time.Now()
	pending := q.stop(50 * time.Millisecond)
	if waited := time.Since(start); waited > 2*time.Second {
		t.Fatalf("stop waited %s for a hung delivery", waited)
	}
	if len(pending) != 1 || pending[0].Text != "e1" {
		t.Fatalf("stop returned %+v, want e1", pending)
	}
}

// TestUndeliveredRecord_MergesBoundsAndIsTakenOnce: a quit appends to what an
// earlier quit left, the record stays bounded per conductor like a live
// backlog, and a start claims it exactly once.
func TestUndeliveredRecord_MergesBoundsAndIsTakenOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "watcher", "undelivered-test.json")
	if err := saveUndelivered(path, []conductorDelivery{event("demo", "a1"), event("demo", "a2")}); err != nil {
		t.Fatal(err)
	}
	var second []conductorDelivery
	for i := 1; i <= maxConductorBacklog; i++ {
		second = append(second, event("demo", "b"+strconv.Itoa(i)))
	}
	second = append(second, event("other", "x"))
	if err := saveUndelivered(path, second); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("record mode = %v (%v), want 0600", info, err)
	}

	got, err := takeUndelivered(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != maxConductorBacklog+2 {
		t.Fatalf("record holds %d items, want a notice, %d events for demo and 1 for other", len(got), maxConductorBacklog)
	}
	if got[0].Conductor != "demo" || got[0].Dropped != 2 {
		t.Fatalf("first item = %+v, want demo's notice for the 2 dropped events", got[0])
	}
	if got[1].Text != "b1" || got[maxConductorBacklog].Text != "b"+strconv.Itoa(maxConductorBacklog) {
		t.Fatalf("demo's events = %q..%q, want b1..b%d", got[1].Text, got[maxConductorBacklog].Text, maxConductorBacklog)
	}
	if last := got[len(got)-1]; last.Conductor != "other" || last.Text != "x" {
		t.Fatalf("last item = %+v, want other's event", last)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("record still present after it was taken: %v", err)
	}
	if again, err := takeUndelivered(path); err != nil || again != nil {
		t.Fatalf("second take = %v, %v; want nothing", again, err)
	}
}

// TestUndeliveredRecord_UnreadableRecordIsKept: a truncated or corrupt record
// is neither deleted by a replay nor overwritten by the next quit; it stays on
// disk for recovery.
func TestUndeliveredRecord_UnreadableRecordIsKept(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "undelivered-test.json")
	const corrupt = `[{"conductor": "demo", "text": "trunc`
	if err := os.WriteFile(path, []byte(corrupt), 0o600); err != nil {
		t.Fatal(err)
	}
	if items, err := takeUndelivered(path); err == nil || items != nil {
		t.Fatalf("take of a corrupt record = %v, %v; want an error", items, err)
	}
	kept, _ := filepath.Glob(path + ".replay-*")
	if len(kept) != 1 {
		t.Fatalf("corrupt record not kept after a failed replay: %v", kept)
	}
	if data, _ := os.ReadFile(kept[0]); string(data) != corrupt {
		t.Fatalf("kept record = %q, want the original bytes", data)
	}

	if err := os.WriteFile(path, []byte(corrupt), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := saveUndelivered(path, []conductorDelivery{event("demo", "new")}); err != nil {
		t.Fatal(err)
	}
	aside, _ := filepath.Glob(path + ".unreadable-*")
	if len(aside) != 1 {
		t.Fatalf("corrupt record overwritten instead of kept aside: %v", aside)
	}
	if data, _ := os.ReadFile(aside[0]); string(data) != corrupt {
		t.Fatalf("kept record = %q, want the original bytes", data)
	}
	got, err := takeUndelivered(path)
	if err != nil || len(got) != 1 || got[0].Text != "new" {
		t.Fatalf("record after save = %+v, %v; want the new event", got, err)
	}
}

// TestWatcherDeliveries_QuitRecordsQueuedEventsAndTheNextStartDeliversThem:
// events still queued for a busy conductor when the TUI quits are recorded,
// and the next TUI start delivers them, in order, after its first session
// load.
func TestWatcherDeliveries_QuitRecordsQueuedEventsAndTheNextStartDeliversThem(t *testing.T) {
	setIsolatedAgentDeckDir(t)
	tmpHome := os.Getenv("HOME")
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpHome, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(tmpHome, ".cache"))
	path, err := undeliveredWatcherPath()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, tmpHome+string(filepath.Separator)) {
		t.Fatalf("undelivered record %q is outside the isolated HOME %q", path, tmpHome)
	}

	quitting := NewHome()
	log1 := &sentLog{}
	send, started, release := blockingSend(t, log1)
	quitting.conductorDeliveriesOnce.Do(func() { quitting.conductorDeliveries = newConductorQueue(send) })
	for _, m := range []string{"one", "two", "three"} {
		quitting.dispatchWatcherEvent(watcher.Event{Source: "slack", Sender: "alice", Body: m, RoutedTo: "demo"})
	}
	quitting.dispatchWatcherEvent(watcher.Event{Source: "slack", Sender: "bob", Body: "unrouted"})
	<-started
	stopped := make(chan struct{})
	go func() {
		quitting.stopConductorDeliveries(5 * time.Second)
		close(stopped)
	}()
	time.Sleep(50 * time.Millisecond)
	release()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("stopConductorDeliveries did not return")
	}
	if got := strings.Join(log1.texts(), "|"); got != "[slack] alice: one" {
		t.Fatalf("quitting TUI delivered %q, want only the event in flight", got)
	}

	starting := NewHome()
	starting.initialLoading = true
	log2 := &sentLog{}
	starting.conductorDeliveriesOnce.Do(func() { starting.conductorDeliveries = newConductorQueue(log2.add) })
	starting.Update(loadSessionsMsg{})
	queueIdle(t, starting.conductorDeliveries)
	if got := strings.Join(log2.texts(), "|"); got != "[slack] alice: two|[slack] alice: three" {
		t.Fatalf("next start delivered %q, want the two queued events in order", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("undelivered record still present after replay: %v", err)
	}
}
