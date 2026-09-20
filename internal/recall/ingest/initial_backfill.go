package ingest

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/recall"
	"github.com/asheshgoplani/agent-deck/internal/recall/reader"
	"github.com/asheshgoplani/agent-deck/internal/recall/store"
)

// Initial backfill (docs/recall.md, issue #2329): enabling [recall] indexes
// nothing until someone runs `recall backfill` by hand, and that command
// refuses outright under the load gate a busy machine sits above most of the
// time. This is the daemon/timer path's answer: one bounded background pass,
// triggered the first time recall is enabled with an empty index or a
// never-finished marker, that throttles under load instead of refusing.

// Defaults for the throttled background pass. Chunks are small (a couple of
// seconds, a few megabytes) so any one chunk is cheap next to the manual
// backfill's unbounded pass; the sleep between them scales with load
// (ThrottleSleep) instead of the gate's outright refusal.
const (
	ThrottleChunkDeadline = 2 * time.Second
	ThrottleChunkBytes    = 8 << 20
	ThrottleMinSleep      = 250 * time.Millisecond
	ThrottleMidSleep      = 2 * time.Second
	ThrottleMaxSleep      = 15 * time.Second
	// ThrottleNice is the Linux nice delta applied to the pass's goroutine
	// (docs/recall.md); niceCurrentGoroutine is a no-op elsewhere.
	ThrottleNice = 10
)

// ShouldRunInitialBackfill reports whether the persisted marker (or, absent
// one, an empty index) says the initial backfill has not finished. Callers
// also need [recall] enabled and backfill_on_enable, which this function
// does not see: it only reads the index.
func ShouldRunInitialBackfill(st *store.Store) (bool, error) {
	status, err := st.InitialBackfillStatus()
	if err != nil {
		return false, err
	}
	return status.State != store.InitialBackfillDone, nil
}

// ThrottleOptions configures RunThrottledBackfill.
type ThrottleOptions struct {
	// LockPath is the machine-global sweep lock (recall.LockPath()).
	LockPath string
	// MaxLoadAvg is the same threshold the refusing gate uses
	// ([recall].max_loadavg); it only scales the sleep here, never refuses.
	MaxLoadAvg float64
	// LoadAvg overrides the platform reader in tests (default LoadAvg1).
	LoadAvg func() float64
	// ChunkDeadline and ChunkBytes bound one chunk's Sweep (defaults above).
	ChunkDeadline time.Duration
	ChunkBytes    int64
	// MinSleep, MidSleep and MaxSleep parameterize ThrottleSleep (defaults
	// above).
	MinSleep, MidSleep, MaxSleep time.Duration
	// Sleep overrides the pause in tests (default: a context-aware
	// time.Sleep).
	Sleep func(ctx context.Context, d time.Duration) error
	// Now overrides the clock in tests.
	Now func() time.Time
	// OnChunk, when set, is called after every chunk (progress, tests).
	OnChunk func(Result)
}

func (t *ThrottleOptions) setDefaults() {
	if t.LoadAvg == nil {
		t.LoadAvg = LoadAvg1
	}
	if t.ChunkDeadline <= 0 {
		t.ChunkDeadline = ThrottleChunkDeadline
	}
	if t.ChunkBytes <= 0 {
		t.ChunkBytes = ThrottleChunkBytes
	}
	if t.MinSleep <= 0 {
		t.MinSleep = ThrottleMinSleep
	}
	if t.MidSleep <= 0 {
		t.MidSleep = ThrottleMidSleep
	}
	if t.MaxSleep <= 0 {
		t.MaxSleep = ThrottleMaxSleep
	}
	if t.Sleep == nil {
		t.Sleep = ctxSleep
	}
	if t.Now == nil {
		t.Now = time.Now
	}
}

// ThrottleSleep scales the pause between chunks with load: at or under half
// of maxLoad it is min (steady progress on a quiet machine), at or over
// maxLoad it is max (the same load the refusing gate would reject work at,
// so this backs off hard instead), and the band between is linear from min
// to mid at the maxLoad point. maxLoad<=0 (the gate disabled) always
// sleeps min.
func ThrottleSleep(load, maxLoad float64, minS, midS, maxS time.Duration) time.Duration {
	if maxLoad <= 0 {
		return minS
	}
	ratio := load / maxLoad
	switch {
	case ratio <= 0.5:
		return minS
	case ratio <= 1.0:
		frac := (ratio - 0.5) / 0.5
		return minS + time.Duration(float64(midS-minS)*frac)
	default:
		frac := ratio - 1.0
		if frac > 1 {
			frac = 1
		}
		return midS + time.Duration(float64(maxS-midS)*frac)
	}
}

// ctxSleep is Sleep's default: it returns ctx.Err() early on cancellation
// instead of blocking the full duration.
func ctxSleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// RunThrottledBackfill runs a backfill as a sequence of small chunks instead
// of one gated pass. The manual `recall backfill` refuses outright under
// load (Options.Gate); this path never refuses so it still finishes on a
// heavy machine, just slower: base.Gate is ignored (every chunk runs
// ungated) and each chunk's budget is capped at ThrottleOptions' small
// deadline and byte cap regardless of what base.Budget/PerSourceBytes ask
// for. Never more than one chunk runs anywhere on the machine at a time:
// each chunk takes the machine-global sweep lock without waiting and yields
// it before sleeping, so an interactive search's pre-search sweep or a Stop
// hook's inline sweep is never starved for the whole pass, only for one
// chunk at a time. It returns when nothing was deferred at the end of a
// chunk (caught up) or ctx is done (the caller's marker stays "running":
// the next call resumes from the ledger's own per-source cursors, so
// nothing already committed is repeated).
func RunThrottledBackfill(ctx context.Context, st *store.Store, base Options, topts ThrottleOptions) (Result, error) {
	topts.setDefaults()
	niceCurrentGoroutine(ThrottleNice)
	var acc Result
	for {
		if err := ctx.Err(); err != nil {
			return finalizeThrottledResult(st, acc), err
		}
		release, err := store.Lock(topts.LockPath)
		if err != nil {
			if errors.Is(err, store.ErrLocked) {
				if serr := topts.Sleep(ctx, topts.MinSleep); serr != nil {
					return finalizeThrottledResult(st, acc), serr
				}
				continue
			}
			return finalizeThrottledResult(st, acc), err
		}
		chunkOpts := base
		chunkOpts.Gate = nil
		chunkOpts.Budget = reader.NewBudget(topts.ChunkDeadline, topts.ChunkBytes)
		if chunkOpts.PerSourceBytes <= 0 || chunkOpts.PerSourceBytes > topts.ChunkBytes {
			chunkOpts.PerSourceBytes = topts.ChunkBytes
		}
		chunkOpts.Now = topts.Now
		res, serr := New(st, chunkOpts).Sweep(ctx)
		release()
		// Every chunk's Sweep re-walks and re-reports the WHOLE corpus
		// (discover() has no notion of "since the last chunk"), so only the
		// fields that are genuinely incremental per chunk — bytes and
		// messages a resumed source's cursor had not reached yet — are
		// accumulated here. Count-style fields (how many sources are
		// Discovered/Parsed/Missing/...) would double-count a source
		// touched by more than one chunk if summed the same way;
		// finalizeThrottledResult derives them once, from the ledger's own
		// current state, after the loop ends.
		acc.Queued += res.Queued
		acc.BytesRead += res.BytesRead
		acc.Messages += res.Messages
		acc.Enriched += res.Enriched
		acc.EnrichDeferred += res.EnrichDeferred
		acc.ElapsedMS += res.ElapsedMS
		acc.Deferred = res.Deferred
		acc.DeferredBytes = res.DeferredBytes
		acc.DeferredPaths = res.DeferredPaths
		if topts.OnChunk != nil {
			topts.OnChunk(res)
		}
		if serr != nil {
			return finalizeThrottledResult(st, acc), serr
		}
		if res.Deferred == 0 {
			return finalizeThrottledResult(st, acc), nil
		}
		sleepFor := ThrottleSleep(topts.LoadAvg(), topts.MaxLoadAvg, topts.MinSleep, topts.MidSleep, topts.MaxSleep)
		if serr := topts.Sleep(ctx, sleepFor); serr != nil {
			return finalizeThrottledResult(st, acc), serr
		}
	}
}

// finalizeThrottledResult fills in Discovered/Unchanged/Parsed/Missing/
// Quarantined/Errors/Sessions from the ledger's own current state: a
// one-time query against `source` (grouped by state) and `session`,
// authoritative regardless of how many chunks it took to get there, so a
// source spanning several chunks is counted once, not once per chunk that
// touched it. acc's chunk-accumulated fields (BytesRead, Messages, Deferred
// and friends) are left as they are — those genuinely are the sum, or the
// last chunk's own snapshot, of real incremental work.
func finalizeThrottledResult(st *store.Store, acc Result) Result {
	rows, err := st.W.Query(`SELECT state, count(*) FROM source WHERE host_uid=? GROUP BY state`, store.LocalHostUID)
	if err != nil {
		return acc
	}
	defer rows.Close()
	var ok, partial, missing, quarantined, errored int
	for rows.Next() {
		var state, n int
		if err := rows.Scan(&state, &n); err != nil {
			return acc
		}
		switch state {
		case recall.SourceOK:
			ok = n
		case recall.SourcePartial:
			partial = n
		case recall.SourceMissing:
			missing = n
		case recall.SourceQuarantined:
			quarantined = n
		case recall.SourceError:
			errored = n
		}
	}
	if err := rows.Err(); err != nil {
		return acc
	}
	acc.Discovered = ok + partial + missing + quarantined + errored
	acc.Parsed = ok + partial
	acc.Unchanged = 0
	acc.Missing = missing
	acc.Quarantined = quarantined
	acc.Errors = errored
	_ = st.W.QueryRow(`SELECT count(*) FROM session`).Scan(&acc.Sessions)
	return acc
}

// RunInitialBackfill drives the persisted marker around RunThrottledBackfill:
// pending or a leftover "running" (a process that never finished) both mean
// there is work to do; done is a no-op. One log line marks the start and one
// the end (success or a still-resumable interruption), and progress is
// checkpointed to meta after every chunk so `recall status` mid-pass, or a
// restart's first look, reports the last chunk's numbers.
func RunInitialBackfill(ctx context.Context, st *store.Store, base Options, topts ThrottleOptions) (Result, error) {
	status, err := st.InitialBackfillStatus()
	if err != nil {
		return Result{}, err
	}
	if status.State == store.InitialBackfillDone {
		return Result{}, nil
	}
	now := time.Now
	if topts.Now != nil {
		now = topts.Now
	}
	if err := st.SetInitialBackfillState(store.InitialBackfillRunning, now().Unix()); err != nil {
		return Result{}, err
	}
	ingestLog.Info("recall_initial_backfill_started")
	onChunk := topts.OnChunk
	topts.OnChunk = func(res Result) {
		if onChunk != nil {
			onChunk(res)
		}
		var sessions int
		_ = st.W.QueryRow(`SELECT count(*) FROM session`).Scan(&sessions)
		_ = st.SetInitialBackfillProgress(sessions, res.Deferred)
	}
	result, rerr := RunThrottledBackfill(ctx, st, base, topts)
	if rerr != nil {
		ingestLog.Warn("recall_initial_backfill_interrupted", slog.String("error", rerr.Error()),
			slog.Int("sessions", result.Sessions), slog.Int("deferred", result.Deferred))
		return result, rerr
	}
	if err := st.SetInitialBackfillState(store.InitialBackfillDone, now().Unix()); err != nil {
		return result, err
	}
	ingestLog.Info("recall_initial_backfill_done", slog.Int("sessions", result.Sessions),
		slog.Int64("bytes_read", result.BytesRead), slog.Int64("elapsed_ms", result.ElapsedMS))
	return result, nil
}
