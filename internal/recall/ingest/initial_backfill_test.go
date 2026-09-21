package ingest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/recall/reader"
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
	t.Run("sessions already indexed, no marker: still pending", func(t *testing.T) {
		// Regression (initial backfill completeness): a handful of sessions
		// an ordinary hook sweep wrote before the background pass ever ran
		// look identical, from the ledger alone, to a fully completed
		// manual `recall backfill`. Treating "any sessions exist" as proof
		// of completeness is exactly what let `recall status` report
		// "done" on a box where the background pass had barely scanned 2
		// of 10 configured roots. An unmarked index must always trigger
		// one real pass so completeness is verified, not assumed.
		f := newFixture(t, testcorpus.Options{Files: 3, Seed: 1})
		f.sweep(Options{})
		should, err := ShouldRunInitialBackfill(f.st)
		if err != nil || !should {
			t.Fatalf("should=%v err=%v, want true, nil (an unmarked index must always re-verify, even with existing sessions)", should, err)
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
		// A modern done marker always has roots_walked/roots_total recorded
		// (even at 0, when a real pass found no configured roots); only a
		// marker missing those fields outright (the legacy pre-#2337 shape,
		// covered by TestInitialBackfillStatus_LegacyDoneMarkerReVerifiedOnce)
		// is treated as pending.
		if err := f.st.SetInitialBackfillProgress(0, 0, 0, 0, nil); err != nil {
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
	// The regression this guards: a source that spans several chunks used
	// to be counted once per chunk it touched (Sweep re-walks and
	// re-reports the whole corpus every call), inflating Discovered/
	// Parsed/Sessions past the true file count once chunking was
	// exercised. finalizeThrottledResult derives them from the ledger
	// once, after the loop ends, so they must equal the real totals even
	// though this run took several chunks to get there.
	if res.Parsed != f.stats.Files {
		t.Fatalf("Parsed = %d, want %d (must count each file once, not once per chunk that touched it)", res.Parsed, f.stats.Files)
	}
	if res.Sessions != f.stats.Files {
		t.Fatalf("Sessions = %d, want %d", res.Sessions, f.stats.Files)
	}
	if res.Discovered != f.stats.Files {
		t.Fatalf("Discovered = %d, want %d", res.Discovered, f.stats.Files)
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
	if res.Deferred != 0 || res.Parsed != f.stats.Files || res.Sessions != f.stats.Files {
		t.Fatalf("did not finish under load: %+v", res)
	}
	if got := f.count(`SELECT count(*) FROM session`); got != int64(f.stats.Files) {
		t.Fatalf("sessions = %d, want %d", got, f.stats.Files)
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
	if res.Parsed != f.stats.Files {
		t.Fatalf("Parsed after resume = %d, want %d (the ledger's final state, not per-call chunk sums)", res.Parsed, f.stats.Files)
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

// multiRootFixture lays out n separate Claude config dirs, each with its
// own profile and its own transcript corpus, and opens one recall.db over
// all of them: the shape the initial backfill completeness bug actually
// needs (agentbox: 10 configured roots across several profiles, most of
// them never walked).
type multiRootFixture struct {
	t     *testing.T
	st    *store.Store
	roots []reader.Root
	files int // total files across every root
}

// newMultiRootFixture lays out one root per (profile, dirName) pair — two
// pairs may share a profile (one account with two config dirs, e.g.
// ashesh-buddii and ashesh-personal both owned by the same person) as long
// as dirName differs, since dirName also names the directory on disk.
func newMultiRootFixture(t *testing.T, roots []rootSpec, filesPerRoot int) *multiRootFixture {
	t.Helper()
	base := t.TempDir()
	mf := &multiRootFixture{t: t}
	for i, r := range roots {
		dir := filepath.Join(base, r.dirName)
		if _, err := testcorpus.Generate(dir, testcorpus.Options{Files: filesPerRoot, Seed: int64(i + 1)}); err != nil {
			t.Fatal(err)
		}
		mf.roots = append(mf.roots, reader.Root{Harness: reader.HarnessClaude, Profile: r.profile, Dir: dir, RetentionDays: 30})
		mf.files += filesPerRoot
	}
	st, err := store.Open(filepath.Join(base, "data", "recall.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	mf.st = st
	return mf
}

// rootSpec names one root's profile and its on-disk directory name.
type rootSpec struct{ profile, dirName string }

// sameNameRoots is the common case: profile and dirName are identical
// (each root belongs to a distinct profile).
func sameNameRoots(names ...string) []rootSpec {
	specs := make([]rootSpec, len(names))
	for i, n := range names {
		specs[i] = rootSpec{profile: n, dirName: n}
	}
	return specs
}

func (mf *multiRootFixture) count(q string, args ...any) int64 {
	mf.t.Helper()
	var n int64
	if err := mf.st.R.QueryRow(q, args...).Scan(&n); err != nil {
		mf.t.Fatalf("%s: %v", q, err)
	}
	return n
}

// TestRunInitialBackfill_MultiRootAllProfilesIndexed is the FIX's required
// multi-root coverage test: 3 roots, 2 profiles (one profile owns two of
// the roots, mirroring a harness with several config dirs for the same
// account), and the pass must index every session under every root, not
// just the first one or two it happens to reach.
func TestRunInitialBackfill_MultiRootAllProfilesIndexed(t *testing.T) {
	mf := newMultiRootFixture(t, []rootSpec{{profile: "alice", dirName: "alice"}, {profile: "alice", dirName: "alice-work"}, {profile: "bob", dirName: "bob"}}, 7)
	topts := ThrottleOptions{LockPath: mf.st.Path + ".lock", ChunkDeadline: time.Hour, ChunkBytes: 1 << 20, Sleep: noSleep}
	res, err := RunInitialBackfill(context.Background(), mf.st, Options{Roots: mf.roots}, topts)
	if err != nil {
		t.Fatalf("RunInitialBackfill: %v", err)
	}
	if res.Sessions != mf.files {
		t.Fatalf("Sessions = %d, want %d (every root's files, not just some)", res.Sessions, mf.files)
	}
	if got := mf.count(`SELECT count(*) FROM session`); got != int64(mf.files) {
		t.Fatalf("sessions in db = %d, want %d", got, mf.files)
	}
	if got := mf.count(`SELECT count(DISTINCT profile) FROM session`); got != 2 {
		t.Fatalf("distinct profiles indexed = %d, want 2 (alice and bob)", got)
	}
	status, err := mf.st.InitialBackfillStatus()
	if err != nil {
		t.Fatal(err)
	}
	if status.State != store.InitialBackfillDone {
		t.Fatalf("state = %q, want done", status.State)
	}
	if status.SessionsPending != 0 {
		t.Fatalf("sessions_pending = %d, want 0", status.SessionsPending)
	}
	if status.RootsWalked != 3 || status.RootsTotal != 3 {
		t.Fatalf("roots_walked/total = %d/%d, want 3/3", status.RootsWalked, status.RootsTotal)
	}
	if len(status.UnreadableRoots) != 0 {
		t.Fatalf("unreadable_roots = %v, want none", status.UnreadableRoots)
	}
}

// TestRunInitialBackfill_UnreadableRootReported covers the FIX's other
// required case: a root that exists but cannot be listed (a shared box's
// other-user config dir, e.g. the ~/.claude root reported permission-denied
// on innobox/sbbox) must show up in status rather than silently vanish
// from the walk, and the readable roots must still finish.
func TestRunInitialBackfill_UnreadableRootReported(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not portable to windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores permission bits, so the unreadable root would still be readable")
	}
	mf := newMultiRootFixture(t, sameNameRoots("alice", "bob"), 4)
	// bob's projects/ tree exists but is not listable: chmod, not a
	// missing directory (a missing one is a legitimate empty profile, not
	// an issue).
	bobProjects := filepath.Join(mf.roots[1].Dir, "projects")
	if err := os.Chmod(bobProjects, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(bobProjects, 0o755) })

	topts := ThrottleOptions{LockPath: mf.st.Path + ".lock", ChunkDeadline: time.Hour, ChunkBytes: 1 << 20, Sleep: noSleep}
	res, err := RunInitialBackfill(context.Background(), mf.st, Options{Roots: mf.roots}, topts)
	if err != nil {
		t.Fatalf("RunInitialBackfill: %v", err)
	}
	if res.Sessions != 4 {
		t.Fatalf("Sessions = %d, want 4 (alice's root only; bob's is unreadable)", res.Sessions)
	}
	if len(res.RootIssues) != 1 {
		t.Fatalf("RootIssues = %+v, want exactly one (bob's root)", res.RootIssues)
	}
	if res.RootIssues[0].Profile != "bob" {
		t.Fatalf("RootIssues[0].Profile = %q, want bob", res.RootIssues[0].Profile)
	}
	status, err := mf.st.InitialBackfillStatus()
	if err != nil {
		t.Fatal(err)
	}
	if len(status.UnreadableRoots) != 1 {
		t.Fatalf("status.UnreadableRoots = %v, want exactly one entry", status.UnreadableRoots)
	}
	if status.RootsWalked != 1 || status.RootsTotal != 2 {
		t.Fatalf("roots_walked/total = %d/%d, want 1/2 (alice walked, bob unreadable)", status.RootsWalked, status.RootsTotal)
	}
}

// TestInitialBackfillStatus_DoneStateInvariant is the FIX's done-state
// invariant: a marker-less index that already holds sessions from ordinary
// hook sweeps (a handful of active profiles) must never be reported "done"
// while other configured roots still hold unindexed transcripts — the
// exact shape of the live bug (agentbox: 3 sessions from 2 of 10 roots,
// state=done, ~950 files never scanned).
func TestInitialBackfillStatus_DoneStateInvariant(t *testing.T) {
	mf := newMultiRootFixture(t, sameNameRoots("active", "dormant"), 5)
	// Simulate ordinary hook-driven sweeps that only ever touched the
	// "active" profile's root, exactly as a Stop hook would: no
	// initial_backfill marker is ever written by a plain Sweep.
	if _, err := New(mf.st, Options{Roots: []reader.Root{mf.roots[0]}}).Sweep(context.Background()); err != nil {
		t.Fatalf("hook sweep: %v", err)
	}
	if got := mf.count(`SELECT count(*) FROM session`); got != 5 {
		t.Fatalf("precondition: sessions = %d, want 5 (only the active profile touched so far)", got)
	}

	status, err := mf.st.InitialBackfillStatus()
	if err != nil {
		t.Fatal(err)
	}
	if status.State == store.InitialBackfillDone {
		t.Fatalf("state = done with %d of %d roots ever walked; must never lie", 1, len(mf.roots))
	}

	// The real trigger sees the same thing and must still run a real pass
	// rather than trust the session count.
	should, err := ShouldRunInitialBackfill(mf.st)
	if err != nil || !should {
		t.Fatalf("should=%v err=%v, want true (an index with no marker must always re-verify)", should, err)
	}
	topts := ThrottleOptions{LockPath: mf.st.Path + ".lock", ChunkDeadline: time.Hour, ChunkBytes: 1 << 20, Sleep: noSleep}
	if _, err := RunInitialBackfill(context.Background(), mf.st, Options{Roots: mf.roots}, topts); err != nil {
		t.Fatalf("RunInitialBackfill: %v", err)
	}
	if got := mf.count(`SELECT count(*) FROM session`); got != int64(mf.files) {
		t.Fatalf("sessions after real pass = %d, want %d (dormant's root must be picked up too)", got, mf.files)
	}
	final, err := mf.st.InitialBackfillStatus()
	if err != nil {
		t.Fatal(err)
	}
	if final.State != store.InitialBackfillDone {
		t.Fatalf("state = %q after a real completed pass, want done", final.State)
	}
	if final.SessionsPending != 0 {
		t.Fatalf("sessions_pending = %d, want 0", final.SessionsPending)
	}
}

// TestRunInitialBackfill_BudgetExhaustionNeverFalselyDone rules out a
// tempting but wrong theory of the live bug: that a chunk's own small
// per-pass budget (ThrottleChunkBytes/ThrottleChunkDeadline) elapsing
// mid-walk gets mistaken for "nothing left to do". It does not — Deferred
// counts exactly the candidates a chunk's budget forced it to skip
// (ingest.go recordOutcome/deferCandidate), so a chunk that hits its
// budget mid-walk across several roots must report Deferred > 0 and leave
// the marker "running", and only a later chunk that truly needs to defer
// nothing may report "done".
func TestRunInitialBackfill_BudgetExhaustionNeverFalselyDone(t *testing.T) {
	mf := newMultiRootFixture(t, sameNameRoots("r1", "r2", "r3"), 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var chunks int32
	topts := ThrottleOptions{
		LockPath:      mf.st.Path + ".lock",
		ChunkDeadline: time.Hour,
		ChunkBytes:    96, // tiny: every chunk's budget elapses well before the walk finishes
		Sleep:         noSleep,
		OnChunk: func(res Result) {
			n := atomic.AddInt32(&chunks, 1)
			if n == 1 {
				// The very first chunk must NOT claim done just because its
				// own tiny budget ran out partway through a 30-file, 3-root
				// corpus.
				if res.Deferred == 0 {
					t.Errorf("first chunk (tiny budget, 30 files across 3 roots): Deferred = 0, want > 0")
				}
				status, err := mf.st.InitialBackfillStatus()
				if err != nil {
					t.Fatal(err)
				}
				if status.State == store.InitialBackfillDone {
					t.Fatal("state = done after the first budget-exhausted chunk; a budget elapsing must never look like completion")
				}
				cancel() // stop after inspecting the first chunk's state
			}
		},
	}
	_, err := RunInitialBackfill(ctx, mf.st, Options{Roots: mf.roots}, topts)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	mid, err := mf.st.InitialBackfillStatus()
	if err != nil {
		t.Fatal(err)
	}
	if mid.State != store.InitialBackfillRunning {
		t.Fatalf("state after budget-limited interruption = %q, want running (resumable)", mid.State)
	}

	// The next tick (a fresh call, as the daemon's poll loop would make it)
	// resumes from the ledger's own cursors and reaches a genuine done
	// across every root, not just the one the first chunk happened to
	// reach.
	res, err := RunInitialBackfill(context.Background(), mf.st, Options{Roots: mf.roots}, ThrottleOptions{
		LockPath: mf.st.Path + ".lock", ChunkDeadline: time.Hour, ChunkBytes: 96, Sleep: noSleep,
	})
	if err != nil {
		t.Fatalf("resume tick: %v", err)
	}
	if res.Deferred != 0 {
		t.Fatalf("resume tick did not finish: %+v", res)
	}
	if got := mf.count(`SELECT count(*) FROM session`); got != int64(mf.files) {
		t.Fatalf("sessions = %d, want %d (all 3 roots, not just the first chunk's)", got, mf.files)
	}
	final, err := mf.st.InitialBackfillStatus()
	if err != nil {
		t.Fatal(err)
	}
	if final.State != store.InitialBackfillDone {
		t.Fatalf("state = %q, want done", final.State)
	}
	if final.SessionsPending != 0 {
		t.Fatalf("sessions_pending = %d, want 0", final.SessionsPending)
	}
}

// TestInitialBackfillStatus_LegacyDoneMarkerReVerifiedOnce guards the
// g14/sbbox symptom: pre-#2337 code wrote a "done" marker with no
// roots_walked/roots_total meta rows at all (the fields didn't exist yet),
// and `recall status` on those hosts reports roots=None/None forever
// "complete" because InitialBackfillStatus used to trust state=="done" at
// face value. A legacy marker must be treated as pending exactly once, so
// the next background pass re-walks every root and persists a real marker;
// after that, done must mean done (no infinite re-verification loop), and a
// modern done marker that legitimately has roots_walked set must not be
// disturbed.
func TestInitialBackfillStatus_LegacyDoneMarkerReVerifiedOnce(t *testing.T) {
	f := newFixture(t, testcorpus.Options{Files: 4, Seed: 11})
	// Reproduce exactly what pre-#2337 code left behind: SetInitialBackfillState
	// only ever wrote state/done_at, never roots_walked/roots_total.
	if err := f.st.SetInitialBackfillState(store.InitialBackfillDone, 100); err != nil {
		t.Fatal(err)
	}

	status, err := f.st.InitialBackfillStatus()
	if err != nil {
		t.Fatal(err)
	}
	if status.State != store.InitialBackfillPending {
		t.Fatalf("state = %q, want pending (a legacy done marker with no roots_walked/roots_total must be re-verified once)", status.State)
	}

	should, err := ShouldRunInitialBackfill(f.st)
	if err != nil || !should {
		t.Fatalf("should=%v err=%v, want true, nil", should, err)
	}

	topts := ThrottleOptions{LockPath: f.st.Path + ".lock", ChunkDeadline: time.Hour, ChunkBytes: 1 << 20, Sleep: noSleep}
	if _, err := RunInitialBackfill(context.Background(), f.st, Options{Roots: f.roots()}, topts); err != nil {
		t.Fatalf("RunInitialBackfill: %v", err)
	}

	final, err := f.st.InitialBackfillStatus()
	if err != nil {
		t.Fatal(err)
	}
	if final.State != store.InitialBackfillDone {
		t.Fatalf("state after the real pass = %q, want done", final.State)
	}
	if final.RootsWalked == 0 || final.RootsWalked != final.RootsTotal {
		t.Fatalf("roots_walked=%d roots_total=%d, want equal and > 0 (a real marker must be persisted)", final.RootsWalked, final.RootsTotal)
	}

	// Not re-triggered: a real marker with roots_walked set must never be
	// consulted through the legacy fallback again.
	should, err = ShouldRunInitialBackfill(f.st)
	if err != nil || should {
		t.Fatalf("should=%v err=%v, want false, nil (a real marker must not be re-verified again)", should, err)
	}
	var chunks int32
	topts.OnChunk = func(Result) { atomic.AddInt32(&chunks, 1) }
	res, err := RunInitialBackfill(context.Background(), f.st, Options{Roots: f.roots()}, topts)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if res.Sessions != 0 || res.Parsed != 0 || atomic.LoadInt32(&chunks) != 0 {
		t.Fatalf("second run did work: res=%+v chunks=%d, want a no-op", res, chunks)
	}
}

// TestInitialBackfillStatus_ModernDoneMarkerUnaffected guards the flip side
// of the legacy-marker fix: a modern "done" marker that already has
// roots_walked set (even to a legitimate small/zero value) must be left
// alone, never forced back to pending.
func TestInitialBackfillStatus_ModernDoneMarkerUnaffected(t *testing.T) {
	f := newFixture(t, testcorpus.Options{Files: 0})
	if err := f.st.SetInitialBackfillState(store.InitialBackfillDone, 100); err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetInitialBackfillProgress(0, 0, 0, 0, nil); err != nil {
		t.Fatal(err)
	}
	status, err := f.st.InitialBackfillStatus()
	if err != nil {
		t.Fatal(err)
	}
	if status.State != store.InitialBackfillDone {
		t.Fatalf("state = %q, want done (a marker with roots_total explicitly recorded, even as 0, is not legacy)", status.State)
	}
	should, err := ShouldRunInitialBackfill(f.st)
	if err != nil || should {
		t.Fatalf("should=%v err=%v, want false, nil", should, err)
	}
}
