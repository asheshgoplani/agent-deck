package ui

import (
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"sync"
	"time"
)

// maxConductorBacklog bounds the routed events waiting for one conductor's
// pane, matching the watcher engine's own 64-event buffer.
const maxConductorBacklog = 64

// conductorDelivery is one watcher message waiting for a conductor's pane.
type conductorDelivery struct {
	Conductor string
	Text      string
	QueuedAt  time.Time
	// Dropped > 0 marks the notice that stands in for that many routed
	// events dropped from a full backlog.
	Dropped int
	// Alert names the watcher of a health alert.
	Alert string
}

// text is what gets typed into the conductor's pane.
func (d conductorDelivery) text() string {
	if d.Dropped > 0 {
		return fmt.Sprintf("[agent-deck] %d routed watcher event(s) for this conductor were not delivered because its pane stayed busy; they are stored, see `agent-deck watcher status <name>`.", d.Dropped)
	}
	return d.Text
}

// conductorBacklog is one conductor's waiting messages: health alerts first,
// then the overflow notice if there is one, then routed events, each group in
// arrival order.
type conductorBacklog struct {
	items    []conductorDelivery
	running  bool               // a runner is draining this backlog
	inflight *conductorDelivery // the delivery the runner is sending, if any
}

// add queues d. A health alert goes ahead of every routed event, so it never
// waits behind a backlog of them, and replaces a pending alert from the same
// watcher with the newer state. Routed events are capped at
// maxConductorBacklog: past that the oldest is dropped and counted in a single
// notice placed before the remaining events.
func (b *conductorBacklog) add(d conductorDelivery) {
	alerts := 0
	for alerts < len(b.items) && b.items[alerts].Alert != "" {
		alerts++
	}
	switch {
	case d.Alert != "":
		for i := 0; i < alerts; i++ {
			if b.items[i].Alert == d.Alert {
				b.items[i] = d
				return
			}
		}
		b.items = slices.Insert(b.items, alerts, d)
	case d.Dropped > 0:
		b.addDropped(alerts, d)
	default:
		b.items = append(b.items, d)
		events := 0
		first := -1
		for i, it := range b.items {
			if it.Alert == "" && it.Dropped == 0 {
				if first < 0 {
					first = i
				}
				events++
			}
		}
		if events > maxConductorBacklog {
			dropped := b.items[first]
			b.items = slices.Delete(b.items, first, first+1)
			dropped.Text, dropped.Dropped = "", 1
			b.addDropped(alerts, dropped)
		}
	}
}

// addDropped counts notice.Dropped more dropped events in the pending notice,
// creating it right after the alerts when there is none.
func (b *conductorBacklog) addDropped(alerts int, notice conductorDelivery) {
	for i := range b.items {
		if b.items[i].Dropped > 0 {
			b.items[i].Dropped += notice.Dropped
			return
		}
	}
	b.items = slices.Insert(b.items, alerts, notice)
}

// conductorQueue delivers watcher messages to conductor panes, one at a time
// per conductor and in order. A delivery (composer guard, paste, Enter 100 ms
// later, verify) owns the pane until it returns: two in flight together both
// passed the guard on an empty composer and pasted before either Enter, so a
// burst reached the conductor as one merged command. Queuing never blocks, so
// the relay keeps draining the engine while a pane is busy; each conductor's
// backlog is bounded (conductorBacklog.add), and different conductors still
// deliver in parallel.
type conductorQueue struct {
	send func(conductorDelivery)

	mu       sync.Mutex
	backlogs map[string]*conductorBacklog
	stopped  bool
	runners  sync.WaitGroup
}

func newConductorQueue(send func(conductorDelivery)) *conductorQueue {
	return &conductorQueue{send: send, backlogs: make(map[string]*conductorBacklog)}
}

// enqueue queues d for its conductor and starts that conductor's runner if it
// is idle. After stop nothing new is started.
func (q *conductorQueue) enqueue(d conductorDelivery) {
	q.mu.Lock()
	defer q.mu.Unlock()
	b := q.backlogs[d.Conductor]
	if b == nil {
		b = &conductorBacklog{}
		q.backlogs[d.Conductor] = b
	}
	b.add(d)
	if !b.running && !q.stopped {
		b.running = true
		q.runners.Add(1)
		go q.run(d.Conductor, b)
	}
}

// run delivers one conductor's backlog in order and exits once it is empty or
// the queue stops.
func (q *conductorQueue) run(conductor string, b *conductorBacklog) {
	defer q.runners.Done()
	for {
		q.mu.Lock()
		b.inflight = nil
		if q.stopped || len(b.items) == 0 {
			b.running = false
			if len(b.items) == 0 {
				delete(q.backlogs, conductor)
			}
			q.mu.Unlock()
			return
		}
		next := b.items[0]
		b.items[0] = conductorDelivery{}
		b.items = b.items[1:]
		b.inflight = &next
		q.mu.Unlock()
		q.deliver(next)
	}
}

// deliver contains a panic in one delivery, so it cannot end the runner and
// with it every later delivery to that conductor.
func (q *conductorQueue) deliver(d conductorDelivery) {
	defer func() {
		if r := recover(); r != nil {
			uiLog.Error("conductor_delivery_panic", slog.String("conductor", d.Conductor), slog.Any("panic", r))
		}
	}()
	q.send(d)
}

// stop lets no further delivery start, waits up to wait for the ones already
// in flight (so a quit does not leave a pasted message without its Enter), and
// returns, for the caller to report, the routed events and overflow notices
// still queued and, if the wait expired, the ones still being sent: their
// outcome is unknown, not failed. Health alerts are left out: the next health
// tick restates them.
func (q *conductorQueue) stop(wait time.Duration) (queued, unconfirmed []conductorDelivery) {
	q.mu.Lock()
	q.stopped = true
	conductors := make([]string, 0, len(q.backlogs))
	for c := range q.backlogs {
		conductors = append(conductors, c)
	}
	sort.Strings(conductors)
	for _, c := range conductors {
		b := q.backlogs[c]
		for _, d := range b.items {
			if d.Alert == "" {
				queued = append(queued, d)
			}
		}
		b.items = nil
	}
	q.mu.Unlock()

	done := make(chan struct{})
	go func() {
		q.runners.Wait()
		close(done)
	}()
	select {
	case <-done:
		return queued, nil
	case <-time.After(wait):
	}
	q.mu.Lock()
	for _, c := range conductors {
		if b := q.backlogs[c]; b != nil && b.inflight != nil && b.inflight.Alert == "" {
			unconfirmed = append(unconfirmed, *b.inflight)
		}
	}
	q.mu.Unlock()
	uiLog.Warn("conductor_delivery_stop_timeout",
		slog.Duration("waited", wait), slog.Int("in_flight", len(unconfirmed)))
	return queued, unconfirmed
}
