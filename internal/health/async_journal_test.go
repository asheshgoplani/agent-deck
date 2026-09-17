package health

import (
	"sync"
	"testing"
	"time"
)

// stubAppender is a fake Appender used to simulate a slow or wedged volume
// without touching a real file: block, when non-nil, makes every Append wait
// until the channel closes.
type stubAppender struct {
	mu    sync.Mutex
	calls []Event
	block chan struct{}
}

func (s *stubAppender) Append(e Event) error {
	if s.block != nil {
		<-s.block
	}
	s.mu.Lock()
	s.calls = append(s.calls, e)
	s.mu.Unlock()
	return nil
}

func (s *stubAppender) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func testEvent() Event {
	return Event{TS: time.Now(), SessionID: "s", Kind: KindStatus, To: "waiting"}
}

// The whole point of AsyncWriter: Append must never block its caller, even
// when the writer goroutine is stuck behind an Appender that never returns.
func TestAsyncWriterAppendNeverBlocksOnWedgedAppender(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	w := NewAsyncWriter(&stubAppender{block: block}, 4)

	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			w.Append(testEvent())
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Append blocked on a wedged appender")
	}
}

func TestAsyncWriterDropsAndCountsWhenQueueFull(t *testing.T) {
	before := JournalDropped()
	block := make(chan struct{})
	defer close(block)
	w := NewAsyncWriter(&stubAppender{block: block}, 2)

	for i := 0; i < 20; i++ {
		w.Append(testEvent())
	}
	if after := JournalDropped(); after <= before {
		t.Fatalf("want drops counted, before=%d after=%d", before, after)
	}
}

// Under ordinary load nothing is dropped: everything appended before Stop is
// on disk once Stop returns, whether the writer goroutine got to it as it
// arrived or only in Stop's final drain.
func TestAsyncWriterStopDrainsEverythingAppended(t *testing.T) {
	a := &stubAppender{}
	w := NewAsyncWriter(a, DefaultJournalQueueSize)
	for i := 0; i < 20; i++ {
		w.Append(testEvent())
	}
	w.Stop(2 * time.Second)
	if got := a.count(); got != 20 {
		t.Fatalf("want all 20 events flushed under normal load, got %d", got)
	}
}

func TestAsyncWriterNilAppenderIsNoOp(t *testing.T) {
	w := NewAsyncWriter(nil, 4)
	if w != nil {
		t.Fatal("nil appender must yield a nil writer, mirroring Journal's kill switch")
	}
	w.Append(testEvent()) // must not panic
	w.Stop(time.Second)   // must not panic
}
