package watcher

import (
	"log/slog"
	"sync"
)

// relayEngine consumes the engine's routed events and health states on
// goroutines of its own, hands each health state to deliverHealth (the
// conductor health alerts), and forwards both to panelEvents and panelHealth,
// which feed a TUI's watcher panel. Routed events need no delivery here: the
// engine queued each for its conductor before storing it (#2537).
//
// Delivery must not wait for the Bubble Tea loop. Attaching to a session runs
// through tea.Exec, which blocks that loop until the attach returns, so a
// delivery made from Update sat unsent for as long as the TUI showed a session
// and then went out in a burst on detach (#2524). Like statusWorker, the relay
// keeps running whatever the TUI is doing, and it is the only consumer of the
// engine's channels. A headless owner (`web --no-tui`) has no panel at all and
// runs the same relay (#2530).
//
// The panel forwards never block. The panel re-reads the database on every
// refresh, so while the TUI is not reading (attached), one pending item per
// channel is all it needs. The relay closes both panel channels once the
// engine's channels close (Engine.Stop), and the returned channel closes once
// both goroutines have handed on everything the engine had buffered.
func relayEngine(
	events <-chan Event,
	health <-chan HealthState,
	deliverHealth func(HealthState),
	panelEvents chan<- Event,
	panelHealth chan<- HealthState,
	log *slog.Logger,
) <-chan struct{} {
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
				log.Info("watcher_event_first_received",
					slog.String("sender", evt.Sender),
					slog.String("routed_to", evt.RoutedTo))
			}
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
			relayDeliver(log, "health", deliverHealth, state)
			select {
			case panelHealth <- state:
			default:
			}
		}
	}()

	return finished
}

// relayDeliver runs one delivery with panic recovery, like statusWorker does
// per update: a single bad item must not end the relay and with it every
// delivery after it. A nil deliver func delivers nothing.
func relayDeliver[T any](log *slog.Logger, kind string, deliver func(T), item T) {
	if deliver == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			log.Error("watcher_relay_panic", slog.String("kind", kind), slog.Any("panic", r))
		}
	}()
	deliver(item)
}
