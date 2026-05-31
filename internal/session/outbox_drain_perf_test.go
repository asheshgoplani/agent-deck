package session

import (
	"os"
	"runtime"
	"runtime/debug"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// Perf gates for the durable per-parent outbox DRAIN (issues #1225/#1226,
// activated in #1227). The drain runs on every conductor Stop hook and every
// heartbeat (DrainForStopHook → DrainInboxForParent, and `agent-deck inbox
// drain self`), and until now it was ungated — the highest-ROI gap from the
// deferred list in #790/#1047. See docs/perf-budget-suite.md for the mandate.
//
// The drain path under test is purely in-process: DrainInboxForParent stages
// the inbox to an in-flight WAL (writeFileDurable), truncates the inbox,
// collapses last-wins per child, dedups against the consumed-turns ledger, and
// persists the ledger (writeFileDurable). It touches NO real tmux and spawns no
// child process. Both tests scope all state to t.TempDir via inboxTestHome
// (HOME override), so the files live on the test scratch dir, never the real
// ~/.agent-deck.
//
// Two complementary gates, one per tier in docs/perf-budget-suite.md:
//
//   - Tier 1 (WARM walltime) — TestPerf_OutboxDrain_Drain25. Catches a CPU /
//     Go-runtime regression in the drain ("we added 200ms of work to the save
//     path"). Classified WARM because, with fsync stubbed to a no-op via the
//     production seam (SetFsyncHookForTest), the measured work is pure
//     in-process Go against the page cache — no real-disk fsync, so disk speed
//     is irrelevant exactly as the WARM/tmpfs contract requires. This is more
//     robust than relying on t.TempDir happening to be tmpfs (it is on CI
//     Linux, but not on every dev box, e.g. WSL2/ext4).
//
//   - Tier 2 (count assertion) — TestPerf_OutboxDrain_FsyncCount. Catches an
//     I/O-PATTERN regression Tier 1 cannot see ("we now fsync once per record
//     instead of once per batch"). Asserts the drain issues a CONSTANT number
//     of durable writes regardless of how many messages it drains. No walltime
//     budget; the doc classifies fsync-count gates as COLD (syscall side).
//
// Representative N: the live drain stress test
// (TestIssue1225_DrainInbox_ConcurrentDrainNoDoubleNoLoss, inbox_consumer_test.go)
// fixes a conductor's fan-out at `const n = 25` distinct children. We reuse that
// figure so the perf load tracks the codebase's own notion of a busy drain
// rather than an invented number; childIDForN is the same id generator.
const outboxDrainFanout = 25

// Base local median observed under -race at PERF_BUDGET_MULTIPLIER=1.0
// (Linux WSL2 container, n=25 distinct-child records, fsync stubbed to no-op so
// the figure is disk-independent): ~2.1ms (six runs spanned 1.99–2.21ms).
// WarmBudget multiplies by 3 and applies the 1ms floor and the env multiplier:
//
//	base × 3 × multiplier, floored at 1ms
//	→ 6.3ms locally (mult 1.0), 12.6ms in CI (mult 2.0).
//
// The 3× factor over the observed median leaves headroom for the residual
// warm-host jitter the forced-GC discipline (see the test) can't fully remove,
// while still catching a CPU/Go-runtime regression that doubles the drain cost.
const outboxDrain25Base = 2100 * time.Microsecond

// TestPerf_OutboxDrain_Drain25 measures DrainInboxForParent over a representative
// 25-child inbox. WARM (docs/perf-budget-suite.md): fsync is stubbed to a no-op
// via the durable-write seam, so the timed window is pure in-process Go work
// against the page cache — the WARM/tmpfs contract holds on any filesystem.
//
// Setup (excluded from the timed window via TrimmedMeanWithSetup) rebuilds a
// fresh inbox of 25 distinct-child records and wipes the WAL + consumed-turns
// ledger each iteration, so every timed drain hits the identical steady-state
// path: stage 25 records to the WAL, truncate, persist a 25-entry ledger. That
// keeps the measurement stationary across the n=11 samples.
//
// TrimmedMeanWithSetup is the COLD (no-GC-control) measurement helper, but this
// test is WARM, so we compose the GC control ourselves exactly as
// perfbudget.go's docs instruct: disable auto-GC for the test lifetime and
// force a collection inside setup() before each timed op, so the drain never
// absorbs an unrelated GC pause — the same discipline TrimmedMeanWarm applies.
func TestPerf_OutboxDrain_Drain25(t *testing.T) {
	testutil.SkipIfShort(t)
	inboxTestHome(t)

	// Stub fsync: WARM measures CPU + Go-runtime, not disk. Restored on return.
	restore := SetFsyncHookForTest(func(*os.File) error { return nil })
	defer restore()

	// WARM GC control composed with TrimmedMeanWithSetup: auto-GC off for the
	// test, a forced collection per setup() (below) outside the timed window.
	defer debug.SetGCPercent(debug.SetGCPercent(-1))

	const parent = "conductor-perf-drain25"
	budget := testutil.WarmBudget(t, outboxDrain25Base)

	var lastDrained int
	got := testutil.TrimmedMeanWithSetup(
		func() {
			populateInboxForDrain(t, parent, outboxDrainFanout)
			runtime.GC() // pay collection outside the timed window (WARM discipline)
		},
		func() {
			events, err := DrainInboxForParent(parent)
			if err != nil {
				t.Fatalf("drain: %v", err)
			}
			lastDrained = len(events)
		},
	)

	// Sanity: the drain must actually deliver all 25 (a no-op drain would make
	// the walltime meaningless — the bug class a bare budget wouldn't notice).
	if lastDrained != outboxDrainFanout {
		t.Fatalf("drain delivered %d events, want %d (setup or dedup broke the steady state)", lastDrained, outboxDrainFanout)
	}
	if got > budget {
		t.Fatalf("outbox drain (n=%d) trimmed mean = %v, budget = %v (regression in DrainInboxForParent CPU/Go-runtime cost)", outboxDrainFanout, got, budget)
	}
	t.Logf("outbox drain (n=%d) trimmed mean = %v (budget = %v)", outboxDrainFanout, got, budget)
}

// TestPerf_OutboxDrain_FsyncCount is the Tier 2 I/O-pattern gate
// (docs/perf-budget-suite.md). The durable backing is *os.File (writeFileDurable:
// fsync the data file, atomic rename, best-effort fsync the parent dir), so the
// doc's "real-disk count assertions" tier applies: assert on fsync COUNT, not
// un-normalizable disk walltime.
//
// Derived by reading the drain code (inbox_consumer.go), NOT by recording
// observed values. DrainInboxForParent issues exactly TWO durable writes for any
// non-empty drain of distinct-child, not-yet-consumed records:
//
//  1. stageInboxDrainLocked → writeInflightLocked → writeFileDurable(WAL)
//     — stages ALL pending records in ONE write (1 data fsync + 1 dir fsync).
//  2. finalizeInboxDrain → saveConsumedTurnsLocked → writeFileDurable(ledger)
//     — persists the WHOLE consumed-turns map in ONE write (1 data fsync + 1
//     dir fsync).
//
// So a drain of N messages = 2 file fsyncs + 2 directory fsyncs, INDEPENDENT of
// N. Proving the count is identical for N=1 and N=25 is the regression guard: a
// change to fsync-per-record would make the file-fsync count scale with N. The
// inbox truncate (os.Remove) and the WAL drop (os.Remove) issue no fsync.
func TestPerf_OutboxDrain_FsyncCount(t *testing.T) {
	testutil.SkipIfShort(t)
	inboxTestHome(t)

	// Count fsyncs through the durable-write seam; real Sync still runs (cheap
	// on the scratch dir). Producer appends (CommitToInbox) fsync directly, not
	// through this seam, so setup contributes zero counted syncs — but Reset()
	// before each drain makes the assertion robust regardless.
	fc := new(testutil.FsyncCounter)
	restore := SetFsyncHookForTest(fc.Wrap(nil))
	defer restore()

	const (
		wantFileSyncs = 2 // WAL stage + consumed-ledger persist
		wantDirSyncs  = 2 // one best-effort parent-dir fsync per durable write
	)

	for _, n := range []int{1, outboxDrainFanout} {
		parent := "conductor-perf-fsync"
		populateInboxForDrain(t, parent, n)
		fc.Reset()

		events, err := DrainInboxForParent(parent)
		if err != nil {
			t.Fatalf("n=%d drain: %v", n, err)
		}
		if len(events) != n {
			t.Fatalf("n=%d: drain delivered %d events, want %d", n, len(events), n)
		}
		if fc.FileSyncs() != wantFileSyncs {
			t.Fatalf("n=%d: drain issued %d file fsyncs, want exactly %d (a per-record fsync regression scales this with N)", n, fc.FileSyncs(), wantFileSyncs)
		}
		if fc.DirSyncs() != wantDirSyncs {
			t.Fatalf("n=%d: drain issued %d directory fsyncs, want exactly %d", n, fc.DirSyncs(), wantDirSyncs)
		}
		t.Logf("n=%d: drain issued %d file + %d dir fsyncs (constant, as derived)", n, fc.FileSyncs(), fc.DirSyncs())
	}
}

// populateInboxForDrain rebuilds a fresh inbox of n distinct-child completion
// records for parent and wipes the in-flight WAL + consumed-turns ledger, so the
// next DrainInboxForParent hits the full steady-state path (stage N + persist an
// N-entry ledger) with no carry-over dedup from a prior drain. childIDForN is
// the shared id generator used by the #1225 drain tests.
func populateInboxForDrain(t *testing.T, parent string, n int) {
	t.Helper()
	// Wipe any state a prior iteration/drain left behind. Removing the consumed
	// ledger is what guarantees every record below is a NEW turn (not deduped),
	// so the drain always performs both durable writes.
	_ = os.Remove(InboxPathFor(parent))
	_ = os.Remove(inboxInflightPathFor(parent))
	_ = os.Remove(consumedTurnsPathFor(parent))
	ResetInboxFingerprintCacheForTest()

	now := time.Now()
	for i := 0; i < n; i++ {
		ev := TransitionNotificationEvent{
			ChildSessionID: childIDForN(i),
			ChildTitle:     "worker",
			Profile:        "personal",
			FromStatus:     "running",
			ToStatus:       "waiting",
			LastOutputHash: childIDForN(i) + "-turn", // distinct per child → distinct turn fingerprint
			Timestamp:      now,
		}
		if err := CommitToInbox(parent, ev); err != nil {
			t.Fatalf("populate commit %d: %v", i, err)
		}
	}
}
