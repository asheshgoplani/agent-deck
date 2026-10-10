package ui

import (
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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
		out[i] = d.Text
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
	q.enqueue(alert("demo", "w0", "in flight"))
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

func texts(ds []conductorDelivery) []string {
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = d.Text
	}
	return out
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
		q.enqueue(alert("demo", "w"+strconv.Itoa(i), strconv.Itoa(i)))
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
			q.enqueue(alert("demo", "w"+strconv.Itoa(i+1), strconv.Itoa(i)))
		}
		close(queued)
	}()
	select {
	case <-queued:
	case <-time.After(5 * time.Second):
		t.Fatal("enqueue blocked behind a delivery in flight")
	}

	q.enqueue(alert("other", "w1", "for other"))
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
	q.enqueue(alert("demo", "w1", "bad"))
	q.enqueue(alert("demo", "w2", "good"))
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the delivery after a panic never ran")
	}
	queueIdle(t, q)
}

// TestConductorQueue_KeepsTheNewestAlertPerWatcher: a newer alert about a
// watcher replaces its pending one, in that one's place.
func TestConductorQueue_KeepsTheNewestAlertPerWatcher(t *testing.T) {
	q, log, release := busyQueue(t)
	q.enqueue(alert("demo", "w1", "w1 old"))
	q.enqueue(alert("demo", "w2", "w2"))
	q.enqueue(alert("demo", "w1", "w1 new"))

	release()
	queueIdle(t, q)
	want := "in flight,w1 new,w2"
	if got := strings.Join(log.texts(), ","); got != want {
		t.Fatalf("delivered %s, want %s", got, want)
	}
}

// TestConductorQueue_StopFinishesTheDeliveryInFlightAndStartsNoOther: a quit
// waits for the delivery in flight and starts no other; the queued alerts are
// dropped (the next engine owner restates each watcher's state).
func TestConductorQueue_StopFinishesTheDeliveryInFlightAndStartsNoOther(t *testing.T) {
	q, log, release := busyQueue(t)
	q.enqueue(alert("demo", "w1", "a1"))
	q.enqueue(alert("demo", "w2", "a2"))

	stopped := make(chan struct{})
	go func() {
		q.stop(5 * time.Second)
		close(stopped)
	}()
	// Release only once stop has taken the queue, so the runner cannot reach
	// a1 first however the goroutines are scheduled.
	waitStopped(t, q)
	select {
	case <-stopped:
		t.Fatal("stop returned while a delivery was still in flight")
	default:
	}
	release()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("stop did not return after the delivery in flight finished")
	}

	// After stop, enqueue starts no runner. started only grows, so a runner
	// started here would show even if it had already exited again; no wait
	// for a delivery that should not happen.
	q.mu.Lock()
	before := q.started
	q.mu.Unlock()
	q.enqueue(alert("demo", "w3", "after stop"))
	q.mu.Lock()
	after := q.started
	q.mu.Unlock()
	if after != before {
		t.Fatalf("enqueue after stop started %d runner(s)", after-before)
	}
	if got := strings.Join(log.texts(), ","); got != "in flight" {
		t.Fatalf("delivered %s, want only the delivery that was in flight", got)
	}
}

// TestConductorQueue_StopGivesUpOnAHungDelivery: a delivery that never
// returns cannot hold a quit past the wait; finished still tells the caller
// when it does return.
func TestConductorQueue_StopGivesUpOnAHungDelivery(t *testing.T) {
	q, _, release := busyQueue(t)
	start := time.Now()
	q.stop(50 * time.Millisecond)
	if waited := time.Since(start); waited > 2*time.Second {
		t.Fatalf("stop waited %s for a hung delivery", waited)
	}
	select {
	case <-q.finished():
		t.Fatal("finished closed while the delivery was still in flight")
	default:
	}
	release()
	select {
	case <-q.finished():
	case <-time.After(5 * time.Second):
		t.Fatal("finished did not close once the delivery returned")
	}
}
