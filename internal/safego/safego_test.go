package safego_test

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/safego"
)

// lockedBuffer is a bytes.Buffer guarded by a mutex so tests can read while
// the safego.Go goroutine is still writing the recover log record.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *lockedBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}

func captureLogger(buf *lockedBuffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// waitForLog polls buf until it is non-empty or deadline elapses.
func waitForLog(t *testing.T, buf *lockedBuffer, d time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if buf.Len() > 0 {
			return buf.String()
		}
		time.Sleep(5 * time.Millisecond)
	}
	return buf.String()
}

// TestGo_RecoversPanic_DoesNotKillCaller proves that a panic inside the
// goroutine started by safego.Go does not propagate; the test process must
// remain alive after the panic occurs.
func TestGo_RecoversPanic_DoesNotKillCaller(t *testing.T) {
	t.Parallel()

	buf := &lockedBuffer{}
	logger := captureLogger(buf)

	done := make(chan struct{})
	safego.Go(logger, "panicker", func() {
		defer close(done)
		panic("boom")
	})

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("goroutine never ran or never finished")
	}

	// If we got here, the panic was recovered (otherwise the runtime would
	// have killed the test binary).
}

// TestGo_LogsPanicAtWarn proves that a recovered panic is logged at WARN
// level with the helper name, the recovered value, and a stack trace.
func TestGo_LogsPanicAtWarn(t *testing.T) {
	t.Parallel()

	buf := &lockedBuffer{}
	logger := captureLogger(buf)

	safego.Go(logger, "worker-A", func() {
		panic("kaboom")
	})

	out := waitForLog(t, buf, 2*time.Second)
	if out == "" {
		t.Fatalf("expected log output, got empty buffer")
	}

	// Must be WARN
	if !strings.Contains(out, `"level":"WARN"`) {
		t.Errorf("expected WARN level log, got: %s", out)
	}
	// Must reference the helper name
	if !strings.Contains(out, "worker-A") {
		t.Errorf("expected log to include name 'worker-A', got: %s", out)
	}
	// Must include the recovered value
	if !strings.Contains(out, "kaboom") {
		t.Errorf("expected log to include panic value 'kaboom', got: %s", out)
	}
	// Must include a stack trace marker
	if !strings.Contains(out, "stack") || !strings.Contains(out, "safego_test.go") {
		t.Errorf("expected log to include stack trace mentioning safego_test.go, got: %s", out)
	}
}

// TestGo_NormalReturn_NoLog proves that fn returning normally produces zero
// log output (the recover arm should be quiet on the happy path).
func TestGo_NormalReturn_NoLog(t *testing.T) {
	t.Parallel()

	buf := &lockedBuffer{}
	logger := captureLogger(buf)

	done := make(chan struct{})
	safego.Go(logger, "normal", func() {
		defer close(done)
	})
	<-done
	// safego.Go has no work after fn() on the happy path, so a brief sleep
	// is enough to let the goroutine fully exit.
	time.Sleep(20 * time.Millisecond)

	if buf.Len() != 0 {
		t.Errorf("expected no log output on normal return, got: %s", buf.String())
	}
}

// TestGo_NilLogger_StillRecovers proves the helper is robust when callers
// pass a nil logger: the panic must still be swallowed (no process kill),
// even though the log record is dropped.
func TestGo_NilLogger_StillRecovers(t *testing.T) {
	t.Parallel()

	done := make(chan struct{})
	safego.Go(nil, "nil-logger", func() {
		defer close(done)
		panic("nil-logger-boom")
	})

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("goroutine never ran or never finished")
	}
}

// TestGo_ManyPanicsInParallel proves multiple panicking workers don't
// race the recover/log path. Run under -race to catch data races on the
// logger writer.
func TestGo_ManyPanicsInParallel(t *testing.T) {
	t.Parallel()

	buf := &lockedBuffer{}
	logger := captureLogger(buf)

	const n = 50
	for i := 0; i < n; i++ {
		i := i
		safego.Go(logger, "burst", func() {
			if i%2 == 0 {
				panic("half-of-them")
			}
		})
	}

	// Poll until we observe n/2 WARN lines, with a generous deadline.
	deadline := time.Now().Add(5 * time.Second)
	want := n / 2
	var got int
	for time.Now().Before(deadline) {
		got = strings.Count(buf.String(), `"level":"WARN"`)
		if got == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("expected %d WARN lines, got %d (output: %s)", want, got, buf.String())
}

// Sanity: helper signature matches expected shape for the wrapped sites.
// (compile-time check; ensures the public API stays stable.)
var _ = func() {
	safego.Go(slog.Default(), "shape-check", func() {})
}
