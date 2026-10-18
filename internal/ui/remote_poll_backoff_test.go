package ui

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// fakeRemoteClock is a settable clock shared by the Home under test and its
// fake fetchers, so a "slow" poll costs no wall time.
type fakeRemoteClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeRemoteClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeRemoteClock) Set(t time.Time) {
	c.mu.Lock()
	c.now = t
	c.mu.Unlock()
}

func (c *fakeRemoteClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// clockedFetchRunner is a fake fetcher whose poll "takes" elapsed on the
// fake clock (or fails with err) and records when each poll started.
type clockedFetchRunner struct {
	stubFetchRunner
	clock   *fakeRemoteClock
	elapsed *atomic.Int64 // time.Duration
	err     *atomic.Value // error wrapper
	starts  *[]time.Duration
	epoch   time.Time
	mu      *sync.Mutex
}

type pollErr struct{ err error }

func (r clockedFetchRunner) FetchSessions(context.Context) ([]session.RemoteSessionInfo, *session.ListStats, error) {
	r.mu.Lock()
	*r.starts = append(*r.starts, r.clock.Now().Sub(r.epoch))
	r.mu.Unlock()
	r.clock.Advance(time.Duration(r.elapsed.Load()))
	if v, _ := r.err.Load().(pollErr); v.err != nil {
		return nil, nil, v.err
	}
	return nil, nil, nil
}

func newBackoffHome(t *testing.T) *Home {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	h := newTestHomeWithItems(100, 30, nil)
	t.Cleanup(h.cancel)
	h.remoteSessionRefreshSec = 15
	return h
}

// driveTicks runs one automatic poll round for remote "dev" at every
// interval tick from 0 to horizon (seconds since epoch) and returns the
// offsets at which a poll actually reached the host.
func driveTicks(t *testing.T, h *Home, clock *fakeRemoteClock, epoch time.Time, from, horizon time.Duration, runner clockedFetchRunner) {
	t.Helper()
	remotes := map[string]session.RemoteConfig{"dev": {Host: "test@invalid"}}
	interval := h.remotePollInterval()
	for at := from; at <= horizon; at += interval {
		clock.Set(epoch.Add(at))
		gen := atomic.AddUint64(&h.remoteFetchSeq, 1)
		cmds := h.remoteFetchCmds(gen, remotes)
		h.Update(remoteFetchRoundMsg{gen: gen, fetches: cmds})
		for _, cmd := range cmds {
			h.Update(cmd())
		}
	}
}

func setupClockedRunner(h *Home) (*fakeRemoteClock, time.Time, clockedFetchRunner) {
	epoch := time.Date(2026, 10, 18, 20, 0, 0, 0, time.UTC)
	clock := &fakeRemoteClock{now: epoch}
	h.remoteNow = clock.Now
	runner := clockedFetchRunner{
		stubFetchRunner: stubFetchRunner{name: "dev"},
		clock:           clock,
		elapsed:         &atomic.Int64{},
		err:             &atomic.Value{},
		starts:          &[]time.Duration{},
		epoch:           epoch,
		mu:              &sync.Mutex{},
	}
	runner.err.Store(pollErr{})
	h.newRemoteFetchRunner = func(string, session.RemoteConfig) remoteFetchRunner { return runner }
	return clock, epoch, runner
}

func secs(ds []time.Duration) []int {
	out := make([]int, len(ds))
	for i, d := range ds {
		out[i] = int(d / time.Second)
	}
	return out
}

// A host whose polls take longer than half the interval is polled on an
// exponential schedule (2x, 4x, 8x ... the interval, capped at 5 minutes)
// instead of on every tick, and returns to the normal cadence after the
// first fast poll.
func TestRemotePollSlowHostBacksOffExponentiallyAndResets(t *testing.T) {
	h := newBackoffHome(t)
	clock, epoch, runner := setupClockedRunner(h)

	runner.elapsed.Store(int64(10 * time.Second)) // > 15s/2: slow
	driveTicks(t, h, clock, epoch, 0, 900*time.Second, runner)
	// Each slow poll ends 10s after it starts and holds the host for
	// 30s, 60s, 120s, 240s, then the 300s cap; the next tick after that
	// starts the next poll.
	want := []int{0, 45, 120, 255, 510, 825}
	if got := secs(*runner.starts); !reflect.DeepEqual(got, want) {
		t.Fatalf("slow host poll starts (s) = %v, want %v", got, want)
	}

	// The host recovers: the first poll after the hold is fast and resets
	// the backoff, so the tick cadence resumes.
	runner.elapsed.Store(int64(time.Second))
	*runner.starts = nil
	driveTicks(t, h, clock, epoch, 915*time.Second, 1200*time.Second, runner)
	want = []int{1140, 1155, 1170, 1185, 1200}
	if got := secs(*runner.starts); !reflect.DeepEqual(got, want) {
		t.Fatalf("recovered host poll starts (s) = %v, want %v", got, want)
	}
}

// A failing or timed-out poll backs off the same way.
func TestRemotePollFailingHostBacksOff(t *testing.T) {
	h := newBackoffHome(t)
	clock, epoch, runner := setupClockedRunner(h)
	runner.err.Store(pollErr{fmt.Errorf("list: %w", context.DeadlineExceeded)})
	driveTicks(t, h, clock, epoch, 0, 1100*time.Second, runner)
	want := []int{0, 30, 90, 210, 450, 750, 1050}
	if got := secs(*runner.starts); !reflect.DeepEqual(got, want) {
		t.Fatalf("failing host poll starts (s) = %v, want %v", got, want)
	}
}

// A healthy host keeps the configured cadence.
func TestRemotePollFastHostKeepsCadence(t *testing.T) {
	h := newBackoffHome(t)
	clock, epoch, runner := setupClockedRunner(h)
	runner.elapsed.Store(int64(7 * time.Second)) // < 15s/2: not slow
	driveTicks(t, h, clock, epoch, 0, 60*time.Second, runner)
	want := []int{0, 15, 30, 45, 60}
	if got := secs(*runner.starts); !reflect.DeepEqual(got, want) {
		t.Fatalf("fast host poll starts (s) = %v, want %v", got, want)
	}
}

// Ctrl+R is the user asking for a refresh now: it clears the hold.
func TestRemotePollManualRefreshClearsBackoff(t *testing.T) {
	h := newBackoffHome(t)
	clock, epoch, runner := setupClockedRunner(h)
	runner.err.Store(pollErr{errors.New("ssh: connect to host: Connection refused")})
	driveTicks(t, h, clock, epoch, 0, 0, runner)
	if !h.remotePollBackoffs["dev"].active(clock.Now()) {
		t.Fatal("a failed poll must hold the host")
	}
	h.clearRemotePollBackoff("")
	runner.err.Store(pollErr{})
	driveTicks(t, h, clock, epoch, 5*time.Second, 5*time.Second, runner)
	if got := secs(*runner.starts); !reflect.DeepEqual(got, []int{0, 5}) {
		t.Fatalf("poll starts after manual refresh = %v, want [0 5]", got)
	}
}

func TestRemotePollBackoffSchedule(t *testing.T) {
	interval := 15 * time.Second
	now := time.Unix(0, 0)
	var b remotePollBackoff
	var got []time.Duration
	for i := 0; i < 7; i++ {
		b = nextRemotePollBackoff(b, now, 9*time.Second, interval, nil)
		got = append(got, b.until.Sub(now))
	}
	want := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 5 * time.Minute, 5 * time.Minute, 5 * time.Minute}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("backoff schedule = %v, want %v", got, want)
	}
	if b = nextRemotePollBackoff(b, now, 7*time.Second, interval, nil); b.strikes != 0 || b.active(now) {
		t.Fatalf("a fast success must reset the backoff, got %+v", b)
	}
	if b = nextRemotePollBackoff(b, now, 0, interval, errors.New("boom")); b.strikes != 1 || b.until.Sub(now) != 30*time.Second {
		t.Fatalf("a failure must add a strike, got %+v", b)
	}
}

// overlapRunner tracks how many polls of one host run at once.
type overlapRunner struct {
	stubFetchRunner
	inFlight, maxInFlight, calls *atomic.Int32
	hold                         time.Duration
}

func (r overlapRunner) FetchSessions(ctx context.Context) ([]session.RemoteSessionInfo, *session.ListStats, error) {
	r.calls.Add(1)
	n := r.inFlight.Add(1)
	defer r.inFlight.Add(-1)
	for {
		m := r.maxInFlight.Load()
		if n <= m || r.maxInFlight.CompareAndSwap(m, n) {
			break
		}
	}
	select {
	case <-time.After(r.hold):
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	return nil, nil, nil
}

// However many rounds are started while a slow poll runs (ticks, pushed
// changes, Ctrl+R, focus retries all start rounds), a host never has more
// than one poll in flight, and the skipped rounds keep its rows.
func TestRemotePollSlowFetcherNeverOverlapsPerHost(t *testing.T) {
	h := newBackoffHome(t)
	counters := map[string]*overlapRunner{}
	for _, name := range []string{"dev", "prod"} {
		counters[name] = &overlapRunner{
			stubFetchRunner: stubFetchRunner{name: name},
			inFlight:        &atomic.Int32{}, maxInFlight: &atomic.Int32{}, calls: &atomic.Int32{},
			hold: 150 * time.Millisecond,
		}
	}
	h.newRemoteFetchRunner = func(name string, _ session.RemoteConfig) remoteFetchRunner { return *counters[name] }
	remotes := map[string]session.RemoteConfig{"dev": {Host: "dev@invalid"}, "prod": {Host: "prod@invalid"}}
	var wg sync.WaitGroup
	for round := uint64(1); round <= 12; round++ {
		for _, cmd := range h.remoteFetchCmds(round, remotes) {
			wg.Add(1)
			go func() { defer wg.Done(); _ = cmd() }()
		}
		time.Sleep(5 * time.Millisecond)
	}
	wg.Wait()
	for name, c := range counters {
		if got := c.maxInFlight.Load(); got != 1 {
			t.Errorf("%s: %d polls in flight at once, want 1", name, got)
		}
		if got := c.calls.Load(); got < 1 || got > 2 {
			t.Errorf("%s: %d polls for 12 overlapping rounds, want 1 (2 if a round started after the first finished)", name, got)
		}
	}
}
