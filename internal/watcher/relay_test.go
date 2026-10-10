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
func startTestRelay(events <-chan Event, health <-chan HealthState, deliverEvent func(Event), deliverHealth func(HealthState)) (<-chan Event, <-chan HealthState, <-chan struct{}) {
	panelEvents := make(chan Event, 1)
	panelHealth := make(chan HealthState, 1)
	done := relayEngine(events, health, deliverEvent, deliverHealth, panelEvents, panelHealth,
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
// relay: while the TUI is attached nobody reads the panel channels, yet every
// event and health state is delivered, once and in order, beyond the engine's
// 64-event buffer; the panel keeps one pending item to refresh from.
func TestRelayEngine_DeliversWhileThePanelIsNotRead(t *testing.T) {
	events := make(chan Event)
	health := make(chan HealthState)
	var gotEvents, gotHealth []string // appended on the relay goroutines; read after they finish
	panelEvents, panelHealth, done := startTestRelay(events, health,
		func(e Event) { gotEvents = append(gotEvents, e.Sender) },
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

	if len(gotEvents) != 100 || len(gotHealth) != 20 {
		t.Fatalf("delivered %d events and %d health states, want 100 and 20", len(gotEvents), len(gotHealth))
	}
	for i, s := range gotEvents {
		if s != strconv.Itoa(i) {
			t.Fatalf("event %d delivered as %q: order or count broken", i, s)
		}
	}
}

// TestRelayEngine_SurvivesAPanickingDelivery: one bad item must not end
// the relay, or every later event would go undelivered again.
func TestRelayEngine_SurvivesAPanickingDelivery(t *testing.T) {
	events := make(chan Event)
	health := make(chan HealthState)
	delivered := make(chan string, 4)
	panelEvents, panelHealth, done := startTestRelay(events, health,
		func(e Event) {
			if e.Sender == "bad" {
				panic("boom")
			}
			delivered <- e.Sender
		},
		func(s HealthState) {
			if s.WatcherName == "bad" {
				panic("boom")
			}
			delivered <- "health:" + s.WatcherName
		})

	relaySend(t, events, Event{Sender: "bad"})
	relaySend(t, events, Event{Sender: "good"})
	relaySend(t, health, HealthState{WatcherName: "bad"})
	relaySend(t, health, HealthState{WatcherName: "good"})
	close(events)
	close(health)
	relayWait(t, done)
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
