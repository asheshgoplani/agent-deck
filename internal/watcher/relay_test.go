package watcher

import (
	"io"
	"log/slog"
	"strconv"
	"testing"
	"time"
)

// startTestRelay runs relayEngine with 1-slot panel channels, the size an
// EngineHost gives them.
func startTestRelay(events <-chan Event, health <-chan HealthState, deliverHealth func(HealthState)) (<-chan Event, <-chan HealthState, <-chan struct{}) {
	panelEvents := make(chan Event, 1)
	panelHealth := make(chan HealthState, 1)
	done := relayEngine(events, health, deliverHealth, panelEvents, panelHealth,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	return panelEvents, panelHealth, done
}

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

// relayWait waits for the relay to finish.
func relayWait(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("relay did not finish after the engine channels closed")
	}
}

// TestRelayEngine_DeliversWhileThePanelIsNotRead covers #2524 at the
// relay: while the TUI is attached nobody reads the panel channels, yet the
// relay keeps draining the engine beyond its 64-event buffer and delivers
// every health state, once and in order; the panel keeps one pending item to
// refresh from.
func TestRelayEngine_DeliversWhileThePanelIsNotRead(t *testing.T) {
	events := make(chan Event)
	health := make(chan HealthState)
	var gotHealth []string // appended on the relay goroutine; read after it finishes
	panelEvents, panelHealth, done := startTestRelay(events, health,
		func(s HealthState) { gotHealth = append(gotHealth, s.WatcherName) })

	for i := 0; i < 100; i++ {
		relaySend(t, events, Event{Sender: strconv.Itoa(i), RoutedTo: "demo"})
	}
	for i := 0; i < 20; i++ {
		relaySend(t, health, HealthState{WatcherName: strconv.Itoa(i), Status: HealthStatusError})
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
	// done closes once everything the engine handed over is dispatched, with
	// nobody reading the panel: shutdown can wait on it before stopping the
	// delivery queue.
	relayWait(t, done)
	relayDrain(t, panelEvents)
	relayDrain(t, panelHealth)

	if len(gotHealth) != 20 {
		t.Fatalf("delivered %d health states, want 20", len(gotHealth))
	}
	for i, s := range gotHealth {
		if s != strconv.Itoa(i) {
			t.Fatalf("health state %d delivered as %q: order or count broken", i, s)
		}
	}
}

// TestRelayEngine_SurvivesAPanickingDelivery: one bad item must not end
// the relay, or every later health alert would go undelivered.
func TestRelayEngine_SurvivesAPanickingDelivery(t *testing.T) {
	events := make(chan Event)
	health := make(chan HealthState)
	delivered := make(chan string, 4)
	panelEvents, panelHealth, done := startTestRelay(events, health,
		func(s HealthState) {
			if s.WatcherName == "bad" {
				panic("boom")
			}
			delivered <- s.WatcherName
		})

	relaySend(t, health, HealthState{WatcherName: "bad"})
	relaySend(t, health, HealthState{WatcherName: "good"})
	close(events)
	close(health)
	relayWait(t, done)
	relayDrain(t, panelEvents)
	relayDrain(t, panelHealth)

	close(delivered)
	var got []string
	for s := range delivered {
		got = append(got, s)
	}
	if len(got) != 1 || got[0] != "good" {
		t.Fatalf("delivered %v after a panic, want the good health state", got)
	}
}
