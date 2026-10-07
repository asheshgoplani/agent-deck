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
// channels close (Engine.Stop).
func relayWatcherEngine(
	events <-chan watcher.Event,
	health <-chan watcher.HealthState,
	deliverEvent func(watcher.Event),
	deliverHealth func(watcher.HealthState),
) (<-chan watcher.Event, <-chan watcher.HealthState) {
	panelEvents := make(chan watcher.Event, 1)
	panelHealth := make(chan watcher.HealthState, 1)

	go func() {
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
		defer close(panelHealth)
		for state := range health {
			relayDeliver("health", deliverHealth, state)
			select {
			case panelHealth <- state:
			default:
			}
		}
	}()

	return panelEvents, panelHealth
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

// paneDeliveryQueue runs the watcher deliveries for one conductor pane one at
// a time, in the order they were queued. A delivery (composer guard, paste,
// Enter 100 ms later, verify) owns the pane until it returns: two in flight
// together both pass the guard on an empty composer and paste before either
// Enter, so a burst of events reached the conductor as one merged command,
// out of order. Queuing never blocks, so the relay keeps draining the engine
// while a pane is busy, and different panes still deliver in parallel. The
// zero value is ready to use.
type paneDeliveryQueue struct {
	mu sync.Mutex
	// pending holds, per pane, the deliveries waiting behind the running one.
	// A pane has a key exactly while its runner goroutine is alive.
	pending map[string][]func()
}

// enqueue adds deliver to pane's queue and starts the pane's runner if it is
// idle.
func (q *paneDeliveryQueue) enqueue(pane string, deliver func()) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.pending == nil {
		q.pending = make(map[string][]func())
	}
	waiting, running := q.pending[pane]
	q.pending[pane] = append(waiting, deliver)
	if !running {
		go q.run(pane)
	}
}

// run delivers pane's queue in order and exits once it is empty.
func (q *paneDeliveryQueue) run(pane string) {
	for {
		q.mu.Lock()
		waiting := q.pending[pane]
		if len(waiting) == 0 {
			delete(q.pending, pane)
			q.mu.Unlock()
			return
		}
		next := waiting[0]
		waiting[0] = nil
		q.pending[pane] = waiting[1:]
		q.mu.Unlock()
		runDelivery(pane, next)
	}
}

// runDelivery contains a panic in one delivery, so it cannot stop the pane's
// runner and with it every later delivery to that pane.
func runDelivery(pane string, deliver func()) {
	defer func() {
		if r := recover(); r != nil {
			uiLog.Error("conductor_delivery_panic", slog.String("tmux_session", pane), slog.Any("panic", r))
		}
	}()
	deliver()
}
