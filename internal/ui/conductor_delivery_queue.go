package ui

import (
	"log/slog"
	"sync"
	"time"
)

// conductorDelivery is one watcher health alert waiting for a conductor's
// pane. Routed events do not come through here: the engine queues each in the
// profile's send queue, which outlives this process (#2537).
type conductorDelivery struct {
	Conductor string
	Text      string
	QueuedAt  time.Time
	// Alert names the watcher the alert is about.
	Alert string
}

// conductorBacklog is one conductor's waiting alerts, oldest first.
type conductorBacklog struct {
	items   []conductorDelivery
	running bool // a runner is draining this backlog
}

// add queues d, replacing a pending alert about the same watcher with the
// newer state, so a watcher that flaps while the pane is busy is reported
// once, as it is now.
func (b *conductorBacklog) add(d conductorDelivery) {
	for i := range b.items {
		if b.items[i].Alert == d.Alert {
			b.items[i] = d
			return
		}
	}
	b.items = append(b.items, d)
}

// conductorQueue delivers watcher health alerts to conductor panes, one at a
// time per conductor and in order. A delivery (composer guard, paste, Enter
// 100 ms later, verify) owns the pane until it returns: two in flight together
// both passed the guard on an empty composer and pasted before either Enter,
// so a burst reached the conductor as one merged command. Queuing never
// blocks, so the relay keeps draining the engine while a pane is busy, and
// different conductors still deliver in parallel.
type conductorQueue struct {
	send func(conductorDelivery)

	mu       sync.Mutex
	backlogs map[string]*conductorBacklog
	stopped  bool
	started  int // runners ever started; only grows
	runners  sync.WaitGroup
	// done closes once every runner has returned after stop; nil before.
	done chan struct{}
}

func newConductorQueue(send func(conductorDelivery)) *conductorQueue {
	return &conductorQueue{
		send:     send,
		backlogs: make(map[string]*conductorBacklog),
	}
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
		q.started++
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
		q.mu.Unlock()
		q.deliver(next)
	}
}

// finished returns a channel that closes once the deliveries still running
// when stop was called have returned, including one stop gave up waiting for.
// It is nil before stop.
func (q *conductorQueue) finished() <-chan struct{} {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.done
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

// stop lets no further delivery start, drops the alerts still queued (the
// next health tick restates a watcher's state) and waits up to wait for the
// ones already in flight, so a quit does not leave a pasted message without
// its Enter.
func (q *conductorQueue) stop(wait time.Duration) {
	q.mu.Lock()
	q.stopped = true
	if q.done == nil {
		// No runner starts after stopped is set, so Wait cannot race an Add.
		q.done = make(chan struct{})
		go func(done chan struct{}) {
			q.runners.Wait()
			close(done)
		}(q.done)
	}
	done := q.done
	for _, b := range q.backlogs {
		b.items = nil
	}
	q.mu.Unlock()

	select {
	case <-done:
	case <-time.After(wait):
		uiLog.Warn("conductor_delivery_stop_timeout", slog.Duration("waited", wait))
	}
}
