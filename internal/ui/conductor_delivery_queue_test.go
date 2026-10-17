package ui

import (
	"log/slog"
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

func texts(ds []conductorDelivery) []string {
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = d.text()
	}
	return out
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
	dropped := q.dropped["demo"]
	q.mu.Unlock()
	if queued != maxConductorBacklog+1 {
		t.Fatalf("backlog holds %d items, want %d events plus one notice", queued, maxConductorBacklog)
	}
	if dropped != extra {
		t.Fatalf("queue counted %d dropped events, want %d", dropped, extra)
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

// warnLog records the overflow warnings a conductorQueue logs.
type warnLog struct {
	mu    sync.Mutex
	lines []string
}

func (w *warnLog) warn(msg string, args ...any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	line := msg
	for _, a := range args {
		if attr, ok := a.(slog.Attr); ok {
			line += " " + attr.String()
		}
	}
	w.lines = append(w.lines, line)
}

func (w *warnLog) all() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.lines...)
}

// TestConductorQueue_LogsOverflowOncePerEpisodeNotPerDrop: a sustained stream
// into a busy pane logs when the conductor starts dropping and, with the
// count, when the notice goes out, not once per dropped event; the drop
// counter stays exact.
func TestConductorQueue_LogsOverflowOncePerEpisodeNotPerDrop(t *testing.T) {
	sent := &sentLog{}
	send, started, release := blockingSend(t, sent)
	q := newConductorQueue(send)
	w := &warnLog{}
	q.warn = w.warn // before any goroutine reads it

	q.enqueue(event("demo", "in flight"))
	<-started
	for i := 0; i < maxConductorBacklog+100; i++ {
		q.enqueue(event("demo", "e"+strconv.Itoa(i)))
	}
	if got := w.all(); len(got) != 1 || !strings.HasPrefix(got[0], "watcher_events_dropping_backlog_full conductor=demo dropped_total=1") {
		t.Fatalf("while dropping, logged %q; want one line for the first drop", got)
	}

	release()
	queueIdle(t, q)
	got := w.all()
	if len(got) != 2 || got[1] != "watcher_events_dropped_backlog_full conductor=demo dropped=100 dropped_total=100" {
		t.Fatalf("after the notice went out, logged %q; want the first-drop line and one line with dropped=100", got)
	}
	q.mu.Lock()
	dropped := q.dropped["demo"]
	q.mu.Unlock()
	if dropped != 100 {
		t.Fatalf("queue counted %d drops, want 100", dropped)
	}

	// A later overflow is a new episode and is logged again.
	send2, started2, release2 := blockingSend(t, sent)
	q.send = send2
	q.enqueue(event("demo", "in flight again"))
	<-started2
	for i := 0; i <= maxConductorBacklog; i++ {
		q.enqueue(event("demo", "f"+strconv.Itoa(i)))
	}
	release2()
	queueIdle(t, q)
	if got := w.all(); len(got) != 4 || got[3] != "watcher_events_dropped_backlog_full conductor=demo dropped=1 dropped_total=101" {
		t.Fatalf("second episode logged %q; want two more lines ending with dropped=1 dropped_total=101", got)
	}
}

// TestConductorQueue_OverflowLoggingCannotEndTheRunner: an unset or panicking
// warn hook must not take the runner down when it logs an overflow outside
// deliver's recovery; the notice and the kept events still go out.
func TestConductorQueue_OverflowLoggingCannotEndTheRunner(t *testing.T) {
	for name, warn := range map[string]func(string, ...any){
		"unset":     nil,
		"panicking": func(string, ...any) { panic("boom") },
	} {
		t.Run(name, func(t *testing.T) {
			sent := &sentLog{}
			send, started, release := blockingSend(t, sent)
			q := newConductorQueue(send)
			q.warn = warn // before any goroutine reads it

			q.enqueue(event("demo", "in flight"))
			<-started
			for i := 0; i <= maxConductorBacklog; i++ {
				q.enqueue(event("demo", "e"+strconv.Itoa(i)))
			}
			release()
			queueIdle(t, q)
			if got := sent.texts(); len(got) != maxConductorBacklog+2 || !strings.Contains(got[1], "1 routed watcher event(s)") {
				t.Fatalf("delivered %d messages (second %q); want the one in flight, the notice and %d events",
					len(got), got[min(1, len(got)-1)], maxConductorBacklog)
			}
		})
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

	type stopResult struct{ undelivered, unconfirmed []conductorDelivery }
	stopped := make(chan stopResult, 1)
	go func() {
		undelivered, unconfirmed, _ := q.stop(5 * time.Second)
		stopped <- stopResult{undelivered, unconfirmed}
	}()
	// Release only once stop has taken the queue, so the runner cannot reach
	// e1 first however the goroutines are scheduled.
	waitStopped(t, q)
	select {
	case <-stopped:
		t.Fatal("stop returned while a delivery was still in flight")
	default:
	}
	release()
	var res stopResult
	select {
	case res = <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("stop did not return after the delivery in flight finished")
	}
	if got := strings.Join(texts(res.undelivered), ","); got != "e1,e2" {
		t.Fatalf("stop returned %s undelivered, want e1,e2", got)
	}
	if len(res.unconfirmed) != 0 {
		t.Fatalf("stop reported %v as unconfirmed, but the delivery in flight finished", texts(res.unconfirmed))
	}

	// After stop, enqueue starts no runner. started only grows, so a runner
	// started here would show even if it had already exited again; no wait
	// for a delivery that should not happen.
	q.mu.Lock()
	before := q.started
	q.mu.Unlock()
	q.enqueue(event("demo", "after stop"))
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
// returns cannot hold a quit past the wait, and it is reported as still in
// flight (outcome unknown) next to the queued events.
func TestConductorQueue_StopGivesUpOnAHungDelivery(t *testing.T) {
	q, _, _ := busyQueue(t)
	q.enqueue(event("demo", "e1"))
	start := time.Now()
	undelivered, unconfirmed, _ := q.stop(50 * time.Millisecond)
	if waited := time.Since(start); waited > 2*time.Second {
		t.Fatalf("stop waited %s for a hung delivery", waited)
	}
	if got := strings.Join(texts(undelivered), ","); got != "e1" {
		t.Fatalf("stop returned %s undelivered, want e1", got)
	}
	if got := strings.Join(texts(unconfirmed), ","); got != "in flight" {
		t.Fatalf("stop returned %q as unconfirmed, want the delivery still in flight", got)
	}
}

// TestConductorQueue_StopLeavesAHungAlertOutOfTheReport: a health alert still
// being sent when the wait expires is not reported; the next health tick
// restates it.
func TestConductorQueue_StopLeavesAHungAlertOutOfTheReport(t *testing.T) {
	log := &sentLog{}
	send, started, _ := blockingSend(t, log)
	q := newConductorQueue(send)
	q.enqueue(alert("demo", "w1", "alert in flight"))
	<-started
	if undelivered, unconfirmed, _ := q.stop(50 * time.Millisecond); len(undelivered) != 0 || len(unconfirmed) != 0 {
		t.Fatalf("stop reported undelivered %v, unconfirmed %v; want nothing for an alert", texts(undelivered), texts(unconfirmed))
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

	type leftCounts struct{ undelivered, dropped, unconfirmed int }
	left := make(chan leftCounts, 1)
	go func() {
		undelivered, dropped, unconfirmed := home.stopConductorDeliveries(5 * time.Second)
		left <- leftCounts{undelivered, dropped, unconfirmed}
	}()
	waitStopped(t, home.conductorDeliveries)
	release()
	select {
	case n := <-left:
		if n != (leftCounts{undelivered: 2}) {
			t.Fatalf("stopConductorDeliveries reported %+v, want 2 undelivered and nothing else", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stopConductorDeliveries did not return after the delivery in flight finished")
	}
	queueIdle(t, home.conductorDeliveries)
	if got := strings.Join(log.texts(), "|"); got != "[slack] alice: one" {
		t.Fatalf("delivered %q, want only the event in flight", got)
	}
}

// TestWatcherDeliveries_QuitReportsADeliveryStillInFlightAtTheDeadline: when
// the quit wait expires mid-delivery, that event is reported as in flight with
// an unknown outcome, next to the queued ones, instead of disappearing from
// the report.
func TestWatcherDeliveries_QuitReportsADeliveryStillInFlightAtTheDeadline(t *testing.T) {
	home := NewHome()
	log := &sentLog{}
	send, started, _ := blockingSend(t, log)
	home.conductorDeliveriesOnce.Do(func() { home.conductorDeliveries = newConductorQueue(send) })
	home.dispatchWatcherEvent(watcher.Event{Source: "slack", Sender: "alice", Body: "only", RoutedTo: "demo"})
	<-started
	if undelivered, dropped, unconfirmed := home.stopConductorDeliveries(50 * time.Millisecond); undelivered != 0 || dropped != 0 || unconfirmed != 1 {
		t.Fatalf("stopConductorDeliveries left %d undelivered, %d dropped, %d unconfirmed; want 0, 0, 1", undelivered, dropped, unconfirmed)
	}
}

// overflowHome returns a Home whose conductor deliveries hold "first" until
// releaseFirst is closed and, if holdNotice is set, hold the overflow notice
// for good; noticeSending closes when the notice starts going out. It then
// queues one more event than the backlog keeps behind "first".
func overflowHome(t *testing.T, holdNotice bool) (home *Home, releaseFirst, noticeSending chan struct{}) {
	t.Helper()
	home = NewHome()
	firstHeld := make(chan struct{})
	releaseFirst = make(chan struct{})
	noticeSending = make(chan struct{})
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })
	home.conductorDeliveriesOnce.Do(func() {
		home.conductorDeliveries = newConductorQueue(func(d conductorDelivery) {
			switch {
			case d.Dropped > 0:
				close(noticeSending)
				if holdNotice {
					<-hang
				}
			case d.Text == "[slack] alice: first":
				close(firstHeld)
				<-releaseFirst
			}
		})
	})
	dispatch := func(body string) {
		home.dispatchWatcherEvent(watcher.Event{Source: "slack", Sender: "alice", Body: body, RoutedTo: "demo"})
	}
	dispatch("first")
	<-firstHeld
	for i := 1; i <= maxConductorBacklog+1; i++ {
		dispatch("e" + strconv.Itoa(i)) // one more than fits: e1 is dropped
	}
	return home, releaseFirst, noticeSending
}

// TestWatcherDeliveries_QuitReportsOverflowDropsAfterTheNoticeWentOut: events
// dropped from a full backlog are reported at quit even when their notice was
// delivered and the backlog drained long before.
func TestWatcherDeliveries_QuitReportsOverflowDropsAfterTheNoticeWentOut(t *testing.T) {
	home, releaseFirst, _ := overflowHome(t, false)
	close(releaseFirst)
	queueIdle(t, home.conductorDeliveries)

	undelivered, dropped, unconfirmed := home.stopConductorDeliveries(time.Second)
	if undelivered != 0 || dropped != 1 || unconfirmed != 0 {
		t.Fatalf("stopConductorDeliveries left %d undelivered, %d dropped, %d unconfirmed; want 0, 1, 0", undelivered, dropped, unconfirmed)
	}
}

// TestWatcherDeliveries_QuitReportsOverflowDropsWithTheNoticeInFlight: when
// the quit wait expires while the overflow notice is being sent, its events
// count once, as dropped, never as an unknown outcome.
func TestWatcherDeliveries_QuitReportsOverflowDropsWithTheNoticeInFlight(t *testing.T) {
	home, releaseFirst, noticeSending := overflowHome(t, true)
	close(releaseFirst)
	select {
	case <-noticeSending:
	case <-time.After(5 * time.Second):
		t.Fatal("the overflow notice was never sent")
	}

	undelivered, dropped, unconfirmed := home.stopConductorDeliveries(50 * time.Millisecond)
	if undelivered != maxConductorBacklog || dropped != 1 || unconfirmed != 0 {
		t.Fatalf("stopConductorDeliveries left %d undelivered, %d dropped, %d unconfirmed; want %d, 1, 0",
			undelivered, dropped, unconfirmed, maxConductorBacklog)
	}
}
