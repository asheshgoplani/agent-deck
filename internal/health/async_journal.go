package health

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultJournalQueueSize bounds an AsyncWriter's pending-event queue. Sized
// well above what one daemon pass produces (a pass writes at most one status
// event per instance that actually changed) so drops only happen when the
// underlying volume is genuinely stuck, not from ordinary burstiness.
const DefaultJournalQueueSize = 256

// dropLogInterval rate-limits the "queue full" warning so a persistently
// wedged volume logs once per minute, not once per dropped event.
const dropLogInterval = time.Minute

var journalDropped atomic.Int64

// JournalDropped returns the process-wide count of session-journal events
// dropped because an AsyncWriter's queue was full, since process start. It
// feeds health --json's journal_dropped.
func JournalDropped() int64 { return journalDropped.Load() }

// Appender is the write path an AsyncWriter drains into. *Journal implements
// it; tests substitute a fake to simulate a blocked or slow volume without
// touching a real file.
type Appender interface {
	Append(Event) error
}

// AsyncWriter decouples Journal.Append from its caller, mirroring how Start
// (health.go) drives appendSample from a dedicated ticker goroutine instead
// of the status-detection path it protects: a bounded channel plus one writer
// goroutine, so a slow or wedged volume can never block whoever calls Append.
// When the queue is full the event is dropped and counted rather than
// blocking the caller.
type AsyncWriter struct {
	appender Appender
	queue    chan Event

	done     chan struct{}
	finished chan struct{}
	stopOnce sync.Once

	logMu   sync.Mutex
	lastLog time.Time
}

// NewAsyncWriter starts the writer goroutine draining into a, with a queue of
// the given size. A nil a returns a nil writer, and Append/Stop on a nil
// *AsyncWriter are no-ops, so a caller with nothing to write (the
// session_events kill switch) needs no special case. A typed-nil appender,
// such as a nil *Journal, is not nil here and is the caller's to filter.
func NewAsyncWriter(a Appender, queueSize int) *AsyncWriter {
	if a == nil {
		return nil
	}
	w := &AsyncWriter{
		appender: a,
		queue:    make(chan Event, queueSize),
		done:     make(chan struct{}),
		finished: make(chan struct{}),
	}
	go w.run()
	return w
}

func (w *AsyncWriter) run() {
	defer close(w.finished)
	for {
		select {
		case e := <-w.queue:
			_ = w.appender.Append(e)
		case <-w.done:
			w.drain()
			return
		}
	}
}

// drain writes whatever is already queued on the way out, best-effort: it
// stops at the first empty read rather than waiting for more.
func (w *AsyncWriter) drain() {
	for {
		select {
		case e := <-w.queue:
			_ = w.appender.Append(e)
		default:
			return
		}
	}
}

// Append enqueues e without ever blocking the caller. A full queue means the
// writer goroutine is stuck behind a slow Append (a wedged volume); the event
// is dropped and counted instead of stalling whoever called Append.
func (w *AsyncWriter) Append(e Event) {
	if w == nil {
		return
	}
	select {
	case w.queue <- e:
	default:
		journalDropped.Add(1)
		w.logDrop()
	}
}

func (w *AsyncWriter) logDrop() {
	w.logMu.Lock()
	defer w.logMu.Unlock()
	now := time.Now()
	if !w.lastLog.IsZero() && now.Sub(w.lastLog) < dropLogInterval {
		return
	}
	w.lastLog = now
	slog.Warn("session event journal queue full, dropping event", "dropped_total", JournalDropped())
}

// Stop drains whatever is already queued (best-effort) and stops the writer
// goroutine, waiting at most timeout for it to finish. A clean shutdown must
// not itself hang on the same wedged volume this exists to protect against.
func (w *AsyncWriter) Stop(timeout time.Duration) {
	if w == nil {
		return
	}
	w.stopOnce.Do(func() { close(w.done) })
	select {
	case <-w.finished:
	case <-time.After(timeout):
	}
}
