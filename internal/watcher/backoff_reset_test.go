package watcher

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

const (
	backoffTestInitial = 10 * time.Millisecond
	backoffTestMax     = 300 * time.Millisecond
)

func backoffTestCases() []struct {
	typ        string
	newAdapter func() WatcherAdapter
} {
	return []struct {
		typ        string
		newAdapter func() WatcherAdapter
	}{
		{"ntfy", func() WatcherAdapter {
			return &NtfyAdapter{initialBackoff: backoffTestInitial, maxBackoff: backoffTestMax}
		}},
		{"slack", func() WatcherAdapter {
			return &SlackAdapter{initialBackoff: backoffTestInitial, maxBackoff: backoffTestMax}
		}},
	}
}

// startBackoffTestListen sets up the adapter against server and runs Listen
// until the test ends.
func startBackoffTestListen(t *testing.T, a WatcherAdapter, typ, server string) {
	t.Helper()
	if err := a.Setup(context.Background(), AdapterConfig{
		Type:     typ,
		Name:     "backoff",
		Settings: map[string]string{"topic": "t", "server": server},
	}); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = a.Listen(ctx, make(chan Event, 10))
	}()
	t.Cleanup(func() { cancel(); <-done })
}

func writeOpenLine(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	_, _ = w.Write([]byte(`{"id":"o1","time":1700000000,"event":"open","topic":"t"}` + "\n"))
	w.(http.Flusher).Flush()
}

// The ntfy and slack adapters doubled their reconnect backoff on every drop
// and never reset it, so after a few failed reconnects a stream that later
// stayed up healthy still waited the full cap after every drop. A stream that
// stayed up at least the cap must put the next delay back to the initial value.
func TestListen_BackoffResetsAfterHealthyStream(t *testing.T) {
	const failures = 5 // 10, 20, 40, 80, 160ms: the next delay is the cap
	for _, tc := range backoffTestCases() {
		t.Run(tc.typ, func(t *testing.T) {
			var (
				mu         sync.Mutex
				conns      int
				healthyEnd time.Time
			)
			reconnected := make(chan time.Time, 1)
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				conns++
				n := conns
				mu.Unlock()
				switch {
				case n <= failures:
					http.Error(w, "unavailable", http.StatusServiceUnavailable)
				case n == failures+1:
					// Healthy stream: open event, stays up longer than the cap, then drops.
					writeOpenLine(w)
					time.Sleep(backoffTestMax + 50*time.Millisecond)
					mu.Lock()
					healthyEnd = time.Now()
					mu.Unlock()
				default:
					select {
					case reconnected <- time.Now():
					default:
					}
					<-r.Context().Done()
				}
			}))
			t.Cleanup(ts.Close)
			startBackoffTestListen(t, tc.newAdapter(), tc.typ, ts.URL)

			select {
			case at := <-reconnected:
				mu.Lock()
				gap := at.Sub(healthyEnd)
				mu.Unlock()
				// Without a reset the next delay is the cap (300ms).
				if gap > backoffTestMax/2 {
					t.Fatalf("reconnect after a healthy stream waited %v; want about the initial %v", gap, backoffTestInitial)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("adapter never reconnected after the healthy stream dropped")
			}
		})
	}
}

// A server that accepts, sends one line and drops at once is not healthy: the
// backoff must keep growing toward the cap instead of resetting every time.
func TestListen_BackoffGrowsWhenStreamDropsAtOnce(t *testing.T) {
	const conns = 6 // delays 10, 20, 40, 80, 160ms between them
	for _, tc := range backoffTestCases() {
		t.Run(tc.typ, func(t *testing.T) {
			var (
				mu       sync.Mutex
				arrivals []time.Time
			)
			enough := make(chan struct{})
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				arrivals = append(arrivals, time.Now())
				if len(arrivals) == conns {
					close(enough)
				}
				mu.Unlock()
				writeOpenLine(w)
			}))
			t.Cleanup(ts.Close)
			startBackoffTestListen(t, tc.newAdapter(), tc.typ, ts.URL)

			select {
			case <-enough:
			case <-time.After(10 * time.Second):
				t.Fatalf("adapter did not reconnect %d times", conns)
			}
			mu.Lock()
			last := arrivals[conns-1].Sub(arrivals[conns-2])
			mu.Unlock()
			// Growing backoff makes this gap 160ms; a reset on every drop keeps it at 10ms.
			if last < 100*time.Millisecond {
				t.Fatalf("gap before reconnect %d was %v; want the backoff to grow (about 160ms) for a stream that drops at once", conns, last)
			}
		})
	}
}
