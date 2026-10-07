package ui

import (
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

// waitStopped waits until stop has marked q stopped.
func waitStopped(t *testing.T, q *conductorQueue) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		q.mu.Lock()
		stopped := q.stopped
		q.mu.Unlock()
		if stopped {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("stop never took the queue")
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
	// Release only once stop has taken the queue, so the runner cannot reach
	// e1 first however the goroutines are scheduled.
	waitStopped(t, q)
	select {
	case <-stopped:
		t.Fatal("stop returned while a delivery was still in flight")
	default:
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

// TestWatcherDeliveries_QuitFinishesTheDeliveryInFlightAndStartsNoOther: on
// quit Home waits for the conductor delivery in flight, starts none of the
// queued ones, and reports how many routed events it leaves undelivered.
func TestWatcherDeliveries_QuitFinishesTheDeliveryInFlightAndStartsNoOther(t *testing.T) {
	home := NewHome()
	log := &sentLog{}
	send, started, release := blockingSend(t, log)
	home.conductorDeliveriesOnce.Do(func() { home.conductorDeliveries = newConductorQueue(send) })
	for _, m := range []string{"one", "two", "three"} {
		home.dispatchWatcherEvent(watcher.Event{Source: "slack", Sender: "alice", Body: m, RoutedTo: "demo"})
	}
	home.dispatchWatcherEvent(watcher.Event{Source: "slack", Sender: "bob", Body: "unrouted"})
	<-started

	left := make(chan int, 1)
	go func() { left <- home.stopConductorDeliveries(5 * time.Second) }()
	waitStopped(t, home.conductorDeliveries)
	release()
	select {
	case n := <-left:
		if n != 2 {
			t.Fatalf("stopConductorDeliveries left %d events undelivered, want 2", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stopConductorDeliveries did not return after the delivery in flight finished")
	}
	queueIdle(t, home.conductorDeliveries)
	if got := strings.Join(log.texts(), "|"); got != "[slack] alice: one" {
		t.Fatalf("delivered %q, want only the event in flight", got)
	}
}
