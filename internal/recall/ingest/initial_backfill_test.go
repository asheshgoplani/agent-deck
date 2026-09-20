package ingest

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/recall/store"
	"github.com/asheshgoplani/agent-deck/internal/recall/testcorpus"
)

func TestThrottleSleep_ScalesWithLoad(t *testing.T) {
	minS, midS, maxS := 250*time.Millisecond, 2*time.Second, 15*time.Second
	cases := []struct {
		name    string
		load    float64
		maxLoad float64
		want    time.Duration
	}{
		{"gate disabled", 99, 0, minS},
		{"quiet machine", 1, 4, minS},
		{"exactly half", 2, 4, minS},
		{"at the refusal threshold", 4, 4, midS},
		{"double the threshold", 8, 4, maxS},
		{"far past the threshold clamps", 40, 4, maxS},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ThrottleSleep(c.load, c.maxLoad, minS, midS, maxS)
			if got != c.want {
				t.Fatalf("ThrottleSleep(%v, %v) = %v, want %v", c.load, c.maxLoad, got, c.want)
			}
		})
	}
}

func TestThrottleSleep_MonotonicBetweenBands(t *testing.T) {
	minS, midS, maxS := 250*time.Millisecond, 2*time.Second, 15*time.Second
	prev := time.Duration(0)
	for load := 0.0; load <= 10; load += 0.25 {
		got := ThrottleSleep(load, 4, minS, midS, maxS)
		if got < prev {
			t.Fatalf("ThrottleSleep not monotonic at load=%v: got %v after %v", load, got, prev)
		}
		prev = got
	}
}

// noSleep replaces ThrottleOptions.Sleep in tests: it still respects
// cancellation (so an interrupt test can actually interrupt) but never
// waits in real time.
func noSleep(ctx context.Context, _ time.Duration) error {
	return ctx.Err()
}

func TestShouldRunInitialBackfill_TriggerMatrix(t *testing.T) {
	t.Run("empty index, no marker: pending", func(t *testing.T) {
		f := newFixture(t, testcorpus.Options{Files: 0})
		should, err := ShouldRunInitialBackfill(f.st)
		if err != nil || !should {
			t.Fatalf("should=%v err=%v, want true, nil", should, err)
		}
	})
	t.Run("sessions already indexed, no marker: done", func(t *testing.T) {
		f := newFixture(t, testcorpus.Options{Files: 3, Seed: 1})
		f.sweep(Options{})
		should, err := ShouldRunInitialBackfill(f.st)
		if err != nil || should {
			t.Fatalf("should=%v err=%v, want false, nil", should, err)
		}
	})
	t.Run("marker pending: triggers", func(t *testing.T) {
		f := newFixture(t, testcorpus.Options{Files: 0})
		if err := f.st.SetInitialBackfillState(store.InitialBackfillPending, 100); err != nil {
			t.Fatal(err)
		}
		should, err := ShouldRunInitialBackfill(f.st)
		if err != nil || !should {
			t.Fatalf("should=%v err=%v, want true, nil", should, err)
		}
	})
	t.Run("marker running (crash leftover): triggers", func(t *testing.T) {
		f := newFixture(t, testcorpus.Options{Files: 2, Seed: 2})
		f.sweep(Options{})
		if err := f.st.SetInitialBackfillState(store.InitialBackfillRunning, 100); err != nil {
			t.Fatal(err)
		}
		should, err := ShouldRunInitialBackfill(f.st)
		if err != nil || !should {
			t.Fatalf("should=%v err=%v, want true, nil (a leftover running marker must resume)", should, err)
		}
	})
	t.Run("marker done: never re-triggers", func(t *testing.T) {
		f := newFixture(t, testcorpus.Options{Files: 0})
		if err := f.st.SetInitialBackfillState(store.InitialBackfillDone, 100); err != nil {
			t.Fatal(err)
		}
		should, err := ShouldRunInitialBackfill(f.st)
		if err != nil || should {
			t.Fatalf("should=%v err=%v, want false, nil", should, err)
		}
	})
}

func TestRunThrottledBackfill_ChunksUntilCaughtUp(t *testing.T) {
	f := newFixture(t, testcorpus.Options{Files: 6, Seed: 9, SubagentEvery: 3})
	var chunks int32
	topts := ThrottleOptions{
		LockPath:      f.st.Path + ".lock",
		ChunkDeadline: time.Hour, // never the limiting factor here
		ChunkBytes:    64,        // tiny: forces several chunks over 6 files
		Sleep:         noSleep,
		OnChunk:       func(Result) { atomic.AddInt32(&chunks, 1) },
	}
	res, err := RunThrottledBackfill(context.Background(), f.st, Options{Roots: f.roots()}, topts)
	if err != nil {
		t.Fatalf("RunThrottledBackfill: %v", err)
	}
	if res.Deferred != 0 {
		t.Fatalf("finished with %d still deferred", res.Deferred)
	}
	if atomic.LoadInt32(&chunks) < 2 {
		t.Fatalf("chunks = %d, want at least 2 (a 64-byte cap over %d files should not finish in one)", chunks, f.stats.Files)
	}
	if got := f.count(`SELECT count(*) FROM session`); got != int64(f.stats.Files) {
		t.Fatalf("sessions = %d, want %d (every file indexed exactly once across chunks)", got, f.stats.Files)
	}
	if got := f.count(`SELECT sum(turns) FROM session`); got != int64(f.stats.Prompts) {
		t.Fatalf("turns = %d, want %d (chunking must not drop or duplicate rows)", got, f.stats.Prompts)
	}
}

func TestRunThrottledBackfill_SingleFlightLock(t *testing.T) {
	f := newFixture(t, testcorpus.Options{Files: 2, Seed: 3})
	lockPath := f.st.Path + ".lock"
	release, err := store.Lock(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	var slept int32
	topts := ThrottleOptions{
		LockPath:      lockPath,
		ChunkDeadline: time.Hour,
		ChunkBytes:    1 << 20,
		Sleep: func(ctx context.Context, d time.Duration) error {
			n := atomic.AddInt32(&slept, 1)
			if n == 3 {
				release() // let the throttled pass in after a few held-lock retries
			}
			return ctx.Err()
		},
	}
	res, err := RunThrottledBackfill(context.Background(), f.st, Options{Roots: f.roots()}, topts)
	if err != nil {
		t.Fatalf("RunThrottledBackfill: %v", err)
	}
	if res.Deferred != 0 {
		t.Fatalf("still deferred after the lock freed: %+v", res)
	}
	if atomic.LoadInt32(&slept) < 3 {
		t.Fatalf("slept %d times, want at least 3 (it must back off while the lock is held, never proceed without it)", slept)
	}
	if got := f.count(`SELECT count(*) FROM session`); got != int64(f.stats.Files) {
		t.Fatalf("sessions = %d, want %d", got, f.stats.Files)
	}
}

func TestRunThrottledBackfill_NeverRefusesUnderLoad(t *testing.T) {
	// The manual `recall backfill` refuses outright above max_loadavg
	// (TestGate_RefusesWhileBusyOrLoaded); the throttled path must still
	// finish at the same simulated load, only slower.
	f := newFixture(t, testcorpus.Options{Files: 2, Seed: 4})
	topts := ThrottleOptions{
		LockPath:      f.st.Path + ".lock",
		ChunkDeadline: time.Hour,
		ChunkBytes:    1 << 20,
		MaxLoadAvg:    4,
		LoadAvg:       func() float64 { return 55 }, // far above the gate's refusal threshold
		Sleep:         noSleep,
	}
	res, err := RunThrottledBackfill(context.Background(), f.st, Options{Roots: f.roots()}, topts)
	if err != nil {
		t.Fatalf("RunThrottledBackfill under simulated load 55: %v", err)
	}
	if res.Deferred != 0 || res.Parsed != f.stats.Files {
		t.Fatalf("did not finish under load: %+v", res)
	}
}

func TestRunInitialBackfill_MarksDoneAndSkipsSecondRun(t *testing.T) {
	f := newFixture(t, testcorpus.Options{Files: 3, Seed: 5})
	topts := ThrottleOptions{LockPath: f.st.Path + ".lock", ChunkDeadline: time.Hour, ChunkBytes: 1 << 20, Sleep: noSleep}
	if _, err := RunInitialBackfill(context.Background(), f.st, Options{Roots: f.roots()}, topts); err != nil {
		t.Fatalf("first run: %v", err)
	}
	status, err := f.st.InitialBackfillStatus()
	if err != nil {
		t.Fatal(err)
	}
	if status.State != store.InitialBackfillDone {
		t.Fatalf("state = %q, want done", status.State)
	}
	if status.SessionsDone != f.stats.Files {
		t.Fatalf("sessions_done = %d, want %d", status.SessionsDone, f.stats.Files)
	}
	if status.SessionsPending != 0 {
		t.Fatalf("sessions_pending = %d, want 0", status.SessionsPending)
	}
	if status.DoneAt == 0 {
		t.Fatal("done_at not stamped")
	}

	var chunks int32
	topts.OnChunk = func(Result) { atomic.AddInt32(&chunks, 1) }
	res, err := RunInitialBackfill(context.Background(), f.st, Options{Roots: f.roots()}, topts)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if res.Sessions != 0 || res.Parsed != 0 {
		t.Fatalf("second run did work: %+v (a done marker must be a no-op)", res)
	}
	if atomic.LoadInt32(&chunks) != 0 {
		t.Fatalf("second run ran %d chunk(s), want 0", chunks)
	}
}

func TestRunInitialBackfill_ResumeAfterInterrupt(t *testing.T) {
	f := newFixture(t, testcorpus.Options{Files: 8, Seed: 6, SubagentEvery: 4})
	ctx, cancel := context.WithCancel(context.Background())
	var chunks int32
	topts := ThrottleOptions{
		LockPath:      f.st.Path + ".lock",
		ChunkDeadline: time.Hour,
		ChunkBytes:    64, // tiny: several chunks, so cancelling after the first leaves real work behind
		Sleep:         noSleep,
		OnChunk: func(Result) {
			if atomic.AddInt32(&chunks, 1) == 1 {
				cancel()
			}
		},
	}
	_, err := RunInitialBackfill(ctx, f.st, Options{Roots: f.roots()}, topts)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("first (interrupted) run: err=%v, want context.Canceled", err)
	}
	status, err := f.st.InitialBackfillStatus()
	if err != nil {
		t.Fatal(err)
	}
	if status.State != store.InitialBackfillRunning {
		t.Fatalf("state after interrupt = %q, want running (resumable)", status.State)
	}
	if status.SessionsPending == 0 {
		t.Fatal("sessions_pending = 0 after only one tiny chunk; the interrupt test needs real work left over")
	}

	// A fresh process (same recall.db) resumes: nothing already committed
	// is re-parsed, and the pass reaches done.
	topts2 := topts
	topts2.OnChunk = nil
	res, err := RunInitialBackfill(context.Background(), f.st, Options{Roots: f.roots()}, topts2)
	if err != nil {
		t.Fatalf("resume run: %v", err)
	}
	if res.Deferred != 0 {
		t.Fatalf("resume did not finish: %+v", res)
	}
	final, err := f.st.InitialBackfillStatus()
	if err != nil {
		t.Fatal(err)
	}
	if final.State != store.InitialBackfillDone {
		t.Fatalf("state after resume = %q, want done", final.State)
	}
	if got := f.count(`SELECT count(*) FROM session`); got != int64(f.stats.Files) {
		t.Fatalf("sessions = %d, want %d (resume must not duplicate or drop any)", got, f.stats.Files)
	}
	if got := f.count(`SELECT sum(turns) FROM session`); got != int64(f.stats.Prompts) {
		t.Fatalf("turns = %d, want %d", got, f.stats.Prompts)
	}
}
