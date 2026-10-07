package ui

import (
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
	"github.com/asheshgoplani/agent-deck/internal/watcher"
)

// relaySend sends on an unbuffered channel the relay reads, failing the test
// if the relay stops receiving.
func relaySend[T any](t *testing.T, ch chan<- T, v T) {
	t.Helper()
	select {
	case ch <- v:
	case <-time.After(5 * time.Second):
		t.Fatal("relay stopped consuming the engine channel")
	}
}

// relayDrain empties a panel channel and waits for the relay to close it.
func relayDrain[T any](t *testing.T, ch <-chan T) {
	t.Helper()
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-time.After(5 * time.Second):
			t.Fatal("panel channel did not close after the engine channel closed")
		}
	}
}

// TestRelayWatcherEngine_DeliversWhileThePanelIsNotRead covers #2524 at the
// relay: while the TUI is attached nobody reads the panel channels, yet every
// event and health state is delivered, once and in order, beyond the engine's
// 64-event buffer; the panel keeps one pending item to refresh from.
func TestRelayWatcherEngine_DeliversWhileThePanelIsNotRead(t *testing.T) {
	events := make(chan watcher.Event)
	health := make(chan watcher.HealthState)
	var gotEvents, gotHealth []string // appended on the relay goroutines; read after they finish
	panelEvents, panelHealth := relayWatcherEngine(events, health,
		func(e watcher.Event) { gotEvents = append(gotEvents, e.Sender) },
		func(s watcher.HealthState) { gotHealth = append(gotHealth, s.WatcherName) })

	for i := 0; i < 100; i++ {
		relaySend(t, events, watcher.Event{Sender: strconv.Itoa(i), RoutedTo: "demo"})
	}
	for i := 0; i < 20; i++ {
		relaySend(t, health, watcher.HealthState{WatcherName: strconv.Itoa(i), Status: watcher.HealthStatusError})
	}
	// Nothing has read the panel: it holds exactly the first item of each.
	if n := len(panelEvents); n != 1 {
		t.Errorf("panel events pending = %d, want 1", n)
	}
	if n := len(panelHealth); n != 1 {
		t.Errorf("panel health pending = %d, want 1", n)
	}
	close(events)
	close(health)
	relayDrain(t, panelEvents)
	relayDrain(t, panelHealth)

	if len(gotEvents) != 100 || len(gotHealth) != 20 {
		t.Fatalf("delivered %d events and %d health states, want 100 and 20", len(gotEvents), len(gotHealth))
	}
	for i, s := range gotEvents {
		if s != strconv.Itoa(i) {
			t.Fatalf("event %d delivered as %q: order or count broken", i, s)
		}
	}
}

// TestRelayWatcherEngine_SurvivesAPanickingDelivery: one bad item must not end
// the relay, or every later event would go undelivered again.
func TestRelayWatcherEngine_SurvivesAPanickingDelivery(t *testing.T) {
	events := make(chan watcher.Event)
	health := make(chan watcher.HealthState)
	delivered := make(chan string, 4)
	panelEvents, panelHealth := relayWatcherEngine(events, health,
		func(e watcher.Event) {
			if e.Sender == "bad" {
				panic("boom")
			}
			delivered <- e.Sender
		},
		func(s watcher.HealthState) {
			if s.WatcherName == "bad" {
				panic("boom")
			}
			delivered <- "health:" + s.WatcherName
		})

	relaySend(t, events, watcher.Event{Sender: "bad"})
	relaySend(t, events, watcher.Event{Sender: "good"})
	relaySend(t, health, watcher.HealthState{WatcherName: "bad"})
	relaySend(t, health, watcher.HealthState{WatcherName: "good"})
	close(events)
	close(health)
	relayDrain(t, panelEvents)
	relayDrain(t, panelHealth)

	close(delivered)
	got := map[string]bool{}
	for s := range delivered {
		got[s] = true
	}
	if len(got) != 2 || !got["good"] || !got["health:good"] {
		t.Fatalf("delivered %v after a panic, want the good event and the good health state", got)
	}
}

// paneQueueIdle waits until q has no runner left for any pane.
func paneQueueIdle(t *testing.T, q *paneDeliveryQueue) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		q.mu.Lock()
		n := len(q.pending)
		q.mu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("pane runner did not exit after its queue drained")
}

// TestPaneDeliveryQueue_OnePaneDeliversOneAtATimeInOrder: deliveries to one
// conductor pane never overlap and keep their queue order, so a burst cannot
// merge in the composer or arrive reordered.
func TestPaneDeliveryQueue_OnePaneDeliversOneAtATimeInOrder(t *testing.T) {
	var q paneDeliveryQueue
	var inflight, maxInflight atomic.Int32
	var mu sync.Mutex
	var order []int
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		q.enqueue("pane", func() {
			defer wg.Done()
			n := inflight.Add(1)
			for {
				m := maxInflight.Load()
				if n <= m || maxInflight.CompareAndSwap(m, n) {
					break
				}
			}
			mu.Lock()
			order = append(order, i)
			mu.Unlock()
			time.Sleep(200 * time.Microsecond)
			inflight.Add(-1)
		})
	}
	wg.Wait()
	paneQueueIdle(t, &q)
	if m := maxInflight.Load(); m != 1 {
		t.Errorf("deliveries to one pane overlapped: %d in flight at once", m)
	}
	for i, got := range order {
		if got != i {
			t.Fatalf("delivery %d ran as %d: queue order broken (%v)", i, got, order)
		}
	}
}

// TestPaneDeliveryQueue_NeverBlocksAndPanesAreIndependent: while one pane's
// delivery hangs (an occupied composer is held for seconds), queuing more for
// it returns at once and another pane still gets its deliveries.
func TestPaneDeliveryQueue_NeverBlocksAndPanesAreIndependent(t *testing.T) {
	var q paneDeliveryQueue
	release := make(chan struct{})
	started := make(chan struct{})
	var ran atomic.Int32
	q.enqueue("busy", func() {
		close(started)
		<-release
	})
	<-started

	queued := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			q.enqueue("busy", func() { ran.Add(1) })
		}
		close(queued)
	}()
	select {
	case <-queued:
	case <-time.After(5 * time.Second):
		t.Fatal("enqueue blocked behind a running delivery")
	}

	other := make(chan struct{})
	q.enqueue("other", func() { close(other) })
	select {
	case <-other:
	case <-time.After(5 * time.Second):
		t.Fatal("a busy pane held up another pane's delivery")
	}
	if n := ran.Load(); n != 0 {
		t.Fatalf("%d deliveries ran while the pane's first delivery was still running", n)
	}

	close(release)
	paneQueueIdle(t, &q)
	if n := ran.Load(); n != 100 {
		t.Fatalf("ran %d queued deliveries, want 100", n)
	}
}

// TestPaneDeliveryQueue_SurvivesAPanickingDelivery: a panic in one delivery
// must not leave the pane's queue stuck.
func TestPaneDeliveryQueue_SurvivesAPanickingDelivery(t *testing.T) {
	var q paneDeliveryQueue
	done := make(chan struct{})
	q.enqueue("pane", func() { panic("boom") })
	q.enqueue("pane", func() { close(done) })
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the delivery after a panic never ran")
	}
	paneQueueIdle(t, &q)
}

// TestConductorTmuxSession_ReadsTitleUnderInstanceLock: the relay looks the
// conductor up off the UI goroutine while renames and title sync write Title
// under the instance's own lock. Run with -race: a plain Title read races.
func TestConductorTmuxSession_ReadsTitleUnderInstanceLock(t *testing.T) {
	inst := session.NewInstanceWithTool(session.ConductorSessionTitle("demo"), t.TempDir(), "shell")
	ts := tmux.NewSession("conductor-demo", t.TempDir())
	inst.SetTmuxSessionForTest(ts)
	home := NewHome()
	home.instancesMu.Lock()
	home.instances = []*session.Instance{inst}
	home.instancesMu.Unlock()

	stop := make(chan struct{})
	renamed := make(chan struct{})
	go func() {
		defer close(renamed)
		for i := 0; ; i++ {
			select {
			case <-stop:
				inst.SetTitleThreadSafe(session.ConductorSessionTitle("demo"))
				return
			default:
			}
			inst.SetTitleThreadSafe(session.ConductorSessionTitle("demo-" + strconv.Itoa(i%2)))
		}
	}()
	for i := 0; i < 1000; i++ {
		_ = home.conductorTmuxSession("demo")
	}
	close(stop)
	<-renamed
	if got := home.conductorTmuxSession("demo"); got != ts {
		t.Fatalf("conductor lookup returned %v, want the conductor's tmux session", got)
	}
}
