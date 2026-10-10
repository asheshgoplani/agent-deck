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
// notice placed before the remaining events. add reports whether it dropped an
// event and whether that drop opened a new notice (the first drop since the
// last notice went out).
func (b *conductorBacklog) add(d conductorDelivery) (dropped, newNotice bool) {
	alerts := 0
	for alerts < len(b.items) && b.items[alerts].Alert != "" {
		alerts++
	}
	if d.Alert != "" {
		for i := 0; i < alerts; i++ {
			if b.items[i].Alert == d.Alert {
				b.items[i] = d
				return false, false
			}
		}
		b.items = slices.Insert(b.items, alerts, d)
		return false, false
	}
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
	if events <= maxConductorBacklog {
		return false, false
	}
	oldest := b.items[first]
	b.items = slices.Delete(b.items, first, first+1)
	for i := range b.items {
		if b.items[i].Dropped > 0 {
			b.items[i].Dropped++
			return true, false
		}
	}
	b.items = slices.Insert(b.items, alerts, conductorDelivery{
		Conductor: oldest.Conductor, QueuedAt: oldest.QueuedAt, Dropped: 1,
	})
	return true, true
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
	// dropped counts, per conductor, the routed events dropped from a full
	// backlog over the queue's life, whether or not their notice went out.
	dropped map[string]int
	stopped bool
	started int // runners ever started; only grows
	runners sync.WaitGroup
	// done closes once every runner has returned after stop; nil before.
	done chan struct{}
	// warn logs overflow, never with mu held; tests replace it before use.
	warn func(msg string, args ...any)
}

func newConductorQueue(send func(conductorDelivery)) *conductorQueue {
	return &conductorQueue{
		send:     send,
		backlogs: make(map[string]*conductorBacklog),
		dropped:  make(map[string]int),
		warn:     uiLog.Warn,
	}
}

// enqueue queues d for its conductor and starts that conductor's runner if it
// is idle. After stop nothing new is started. Overflow is logged once when a
// conductor starts dropping and once, with the count, when its notice goes out
// (run), not per dropped event: a sustained stream into a busy pane must not
// turn into a stream of log writes.
func (q *conductorQueue) enqueue(d conductorDelivery) {
	q.mu.Lock()
	b := q.backlogs[d.Conductor]
	if b == nil {
		b = &conductorBacklog{}
		q.backlogs[d.Conductor] = b
	}
	dropped, newNotice := b.add(d)
	if dropped {
		q.dropped[d.Conductor]++
	}
	total := q.dropped[d.Conductor]
	if !b.running && !q.stopped {
		b.running = true
		q.started++
		q.runners.Add(1)
		go q.run(d.Conductor, b)
	}
	q.mu.Unlock()
	if newNotice {
		q.logOverflow("watcher_events_dropping_backlog_full",
			slog.String("conductor", d.Conductor), slog.Int("dropped_total", total))
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
		total := q.dropped[conductor]
		q.mu.Unlock()
		if next.Dropped > 0 {
			q.logOverflow("watcher_events_dropped_backlog_full",
				slog.String("conductor", conductor),
				slog.Int("dropped", next.Dropped), slog.Int("dropped_total", total))
		}
		q.deliver(next)
	}
}

// logOverflow writes an overflow warning through q.warn, falling back to uiLog
// when the hook is unset, and contains a panic in the hook: logging must never
// end the runner, which calls it outside deliver's recovery.
func (q *conductorQueue) logOverflow(msg string, args ...any) {
	defer func() {
		if r := recover(); r != nil {
			uiLog.Error("conductor_delivery_log_panic", slog.Any("panic", r))
		}
	}()
	warn := q.warn
	if warn == nil {
		warn = uiLog.Warn
	}
	warn(msg, args...)
}

// finished returns a channel that closes once the deliveries still running
// when stop was called have returned, including one stop gave up waiting for.
// It is nil before stop, and nil when no delivery is being sent: after stop a
// runner with nothing in flight only exits, it types nothing more.
func (q *conductorQueue) finished() <-chan struct{} {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.sending() == 0 {
		return nil
	}
	return q.done
}

// sending counts the deliveries being sent. q.mu must be held.
func (q *conductorQueue) sending() int {
	n := 0
	for _, b := range q.backlogs {
		if b.inflight != nil {
			n++
		}
	}
	return n
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
// returns, for the caller to report, what it leaves: the routed events still
// queued (undelivered); the routed events still being sent when the wait
// expired (unconfirmed: their outcome is unknown, not failed); and, per
// conductor, the events dropped from a full backlog during the queue's life.
// The three never overlap. Health alerts are left out: the next health tick
// restates them.
func (q *conductorQueue) stop(wait time.Duration) (undelivered, unconfirmed []conductorDelivery, dropped map[string]int) {
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
	conductors := make([]string, 0, len(q.backlogs))
	for c := range q.backlogs {
		conductors = append(conductors, c)
	}
	sort.Strings(conductors)
	for _, c := range conductors {
		b := q.backlogs[c]
		for _, d := range b.items {
			if d.Alert == "" && d.Dropped == 0 {
				undelivered = append(undelivered, d)
			}
		}
		b.items = nil
	}
	dropped = make(map[string]int, len(q.dropped))
	for c, n := range q.dropped {
		dropped[c] = n
	}
	q.mu.Unlock()

	select {
	case <-done:
		return undelivered, nil, dropped
	case <-time.After(wait):
	}
	q.mu.Lock()
	for _, c := range conductors {
		// An overflow notice in flight is already counted in dropped.
		if b := q.backlogs[c]; b != nil && b.inflight != nil && b.inflight.Alert == "" && b.inflight.Dropped == 0 {
			unconfirmed = append(unconfirmed, *b.inflight)
		}
	}
	sending := q.sending()
	q.mu.Unlock()
	// A wait that expired (at once, for a zero wait) with nothing in flight
	// only caught the runners winding down: a normal stop, not a timeout.
	if sending > 0 {
		uiLog.Warn("conductor_delivery_stop_timeout",
			slog.Duration("waited", wait), slog.Int("in_flight", sending))
	}
	return undelivered, unconfirmed, dropped
}
