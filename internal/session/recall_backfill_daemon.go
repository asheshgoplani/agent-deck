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
	go runInitialRecallBackfill(ctx)
}

// runInitialRecallBackfill opens recall.db and, when the persisted marker
// (or an empty index) says the initial backfill never finished, runs it.
func runInitialRecallBackfill(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			recallBackfillLog.Warn("recall_initial_backfill_panic", slog.Any("panic", r))
		}
	}()
	dbPath, err := recall.DBPath()
	if err != nil {
		return
	}
	lockPath, err := recall.LockPath()
	if err != nil {
		return
	}
	st, err := openRecallStoreForDaemon(dbPath, lockPath)
	if err != nil {
		recallBackfillLog.Warn("recall_initial_backfill_open_failed", slog.String("error", err.Error()))
		return
	}
	defer st.Close()
	should, err := ingest.ShouldRunInitialBackfill(st)
	if err != nil {
		recallBackfillLog.Warn("recall_initial_backfill_status_failed", slog.String("error", err.Error()))
		return
	}
	if !should {
		return
	}
	cfg, err := LoadUserConfig()
	if err != nil || cfg == nil {
		return
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
