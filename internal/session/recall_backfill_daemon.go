package session

import (
	"context"
	"errors"
	"log/slog"

	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/recall"
	"github.com/asheshgoplani/agent-deck/internal/recall/ingest"
	"github.com/asheshgoplani/agent-deck/internal/recall/store"
)

// Initial recall backfill, driven from the notify-daemon (docs/recall.md,
// issue #2329). Everything here is a no-op while [recall] enabled = false
// or [recall] backfill_on_enable = false (default true), and every error is
// logged and swallowed: this runs off the daemon's own goroutine, with no
// caller to report failure to, and must never take status detection down
// with it.

var recallBackfillLog = logging.ForComponent(logging.CompRecall)

// maybeStartInitialRecallBackfill starts, at most once per TransitionDaemon,
// the background throttled pass that catches an empty or never-finished
// recall index up. Safe to call every poll tick: the mutex and
// recallBackfillStarted make every call after the first a no-op cheaper
// than the LoadUserConfig it would otherwise repeat.
func (d *TransitionDaemon) maybeStartInitialRecallBackfill(ctx context.Context) {
	d.recallBackfillMu.Lock()
	defer d.recallBackfillMu.Unlock()
	if d.recallBackfillStarted {
		return
	}
	cfg, err := LoadUserConfig()
	if err != nil || cfg == nil || !cfg.Recall.GetEnabled() || !cfg.Recall.GetBackfillOnEnable() {
		return
	}
	d.recallBackfillStarted = true
	d.recallBackfillWG.Add(1)
	go func() {
		defer d.recallBackfillWG.Done()
		if runInitialRecallBackfill(ctx) {
			return
		}
		// The attempt bailed out before it could even open the index or
		// read the persisted marker (a transient db-open/lock failure, or
		// the goroutine panicked): that is not "this daemon already
		// covered it", so the one-shot latch must not swallow every later
		// tick for the rest of this process's life. Clearing it lets the
		// next poll (at most notifyPollSlow away) try again instead of
		// leaving `initial_backfill` stuck at "pending" until an operator
		// restarts the daemon.
		d.recallBackfillMu.Lock()
		d.recallBackfillStarted = false
		d.recallBackfillMu.Unlock()
	}()
}

// runInitialRecallBackfill opens recall.db and, when the persisted marker
// (or an empty index) says the initial backfill never finished, runs it.
// It returns whether the attempt actually reached the point of deciding
// should-run (true) or bailed out early on a transient failure that the
// caller should retry on a later tick (false); a genuine "nothing to do"
// (should=false) counts as reached, not a bail-out.
func runInitialRecallBackfill(ctx context.Context) (attempted bool) {
	defer func() {
		if r := recover(); r != nil {
			recallBackfillLog.Warn("recall_initial_backfill_panic", slog.Any("panic", r))
			attempted = false
		}
	}()
	dbPath, err := recall.DBPath()
	if err != nil {
		return false
	}
	lockPath, err := recall.LockPath()
	if err != nil {
		return false
	}
	st, err := openRecallStoreForDaemon(dbPath, lockPath)
	if err != nil {
		recallBackfillLog.Warn("recall_initial_backfill_open_failed", slog.String("error", err.Error()))
		return false
	}
	defer st.Close()
	should, err := ingest.ShouldRunInitialBackfill(st)
	if err != nil {
		recallBackfillLog.Warn("recall_initial_backfill_status_failed", slog.String("error", err.Error()))
		return false
	}
	if !should {
		return true
	}
	cfg, err := LoadUserConfig()
	if err != nil || cfg == nil {
		return false
	}
	queuePath, _ := recall.QueuePath()
	reg := NewRecallRegistry("", nil)
	defer reg.Close()
	opts := ingest.Options{
		Roots:          RecallRoots(),
		Registry:       reg,
		TextTier:       cfg.Recall.GetTextTier(),
		PerSourceBytes: int64(cfg.Recall.GetPerSourceMB()) << 20,
		NewestFirst:    true,
		QueuePath:      queuePath,
	}
	topts := ingest.ThrottleOptions{LockPath: lockPath, MaxLoadAvg: cfg.Recall.GetMaxLoadAvg()}
	if _, err := ingest.RunInitialBackfill(ctx, st, opts, topts); err != nil && ctx.Err() == nil {
		recallBackfillLog.Warn("recall_initial_backfill_failed", slog.String("error", err.Error()))
	}
	return true
}

// openRecallStoreForDaemon mirrors the CLI's schema-mismatch handling
// (cmd/agent-deck/recall_cmd.go openRecallEnv): a stale schema is recreated
// only under the sweep lock, so a running backfill elsewhere is never
// pulled out from under it.
func openRecallStoreForDaemon(dbPath, lockPath string) (*store.Store, error) {
	st, err := store.OpenCurrent(dbPath)
	if err == nil {
		return st, nil
	}
	if !errors.Is(err, store.ErrSchema) {
		return nil, err
	}
	release, lerr := store.Lock(lockPath)
	if lerr != nil {
		return nil, lerr
	}
	defer release()
	return store.Open(dbPath)
}
