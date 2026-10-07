package ui

import (
	"log/slog"
	"sync"

	"github.com/asheshgoplani/agent-deck/internal/watcher"
)

// relayWatcherEngine consumes the engine's routed events and health states on
// goroutines of its own, hands each one to its deliver func (the conductor-pane
// dispatchers), and then forwards it to the returned channels, which feed the
// TUI's watcher panel.
//
// Delivery must not wait for the Bubble Tea loop. Attaching to a session runs
// through tea.Exec, which blocks that loop until the attach returns, so a
// delivery made from Update sat unsent for as long as the TUI showed a session
// and then went out in a burst on detach (#2524). Like statusWorker, the relay
// keeps running whatever the TUI is doing, and it is the only consumer of the
// engine's channels, so each event is delivered once.
//
// The panel forwards never block. The panel re-reads the database on every
// refresh, so while the TUI is not reading (attached), one pending item per
// channel is all it needs. Both returned channels close once the engine's
// channels close (Engine.Stop), and done closes once both goroutines have
// handed on everything the engine had buffered.
func relayWatcherEngine(
	events <-chan watcher.Event,
	health <-chan watcher.HealthState,
	deliverEvent func(watcher.Event),
	deliverHealth func(watcher.HealthState),
) (<-chan watcher.Event, <-chan watcher.HealthState, <-chan struct{}) {
	panelEvents := make(chan watcher.Event, 1)
	panelHealth := make(chan watcher.HealthState, 1)
	var wg sync.WaitGroup
	wg.Add(2)
	finished := make(chan struct{})
	go func() {
		wg.Wait()
		close(finished)
	}()

	go func() {
		defer wg.Done()
		defer close(panelEvents)
		first := true
		for evt := range events {
			if first {
				// One log per engine instance to confirm the delivery path is alive.
				first = false
				uiLog.Info("watcher_event_first_received",
					slog.String("sender", evt.Sender),
					slog.String("routed_to", evt.RoutedTo))
			}
			relayDeliver("event", deliverEvent, evt)
			select {
			case panelEvents <- evt:
			default:
			}
		}
	}()

	go func() {
		defer wg.Done()
		defer close(panelHealth)
		for state := range health {
			relayDeliver("health", deliverHealth, state)
			select {
			case panelHealth <- state:
			default:
			}
		}
	}()

	return panelEvents, panelHealth, finished
}

// relayDeliver runs one delivery with panic recovery, like statusWorker does
// per update: a single bad item must not end the relay and with it every
// delivery after it.
func relayDeliver[T any](kind string, deliver func(T), item T) {
	defer func() {
		if r := recover(); r != nil {
			uiLog.Error("watcher_relay_panic", slog.String("kind", kind), slog.Any("panic", r))
		}
	}()
	deliver(item)
}
