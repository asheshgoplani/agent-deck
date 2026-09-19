package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/asheshgoplani/agent-deck/internal/recall"
	"github.com/asheshgoplani/agent-deck/internal/recall/ingest"
	"github.com/asheshgoplani/agent-deck/internal/recall/query"
	"github.com/asheshgoplani/agent-deck/internal/recall/reader"
	"github.com/asheshgoplani/agent-deck/internal/recall/store"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// RecallSource is what the Recall search overlay reads: the same index
// `agent-deck recall search` reads, behind an interface so the overlay
// renders from a stub in tests. Nothing here runs on a keypress: Search
// and Show are reads of recall.db; Refresh is the bounded sweep the TUI
// runs in a tea.Cmd after the overlay opens.
type RecallSource interface {
	// Search ranks sessions for q (query.Searcher.Search with the default
	// filters, subagents hidden).
	Search(ctx context.Context, q string, limit int) (query.SearchResult, error)
	// Show returns the session card and its first turns for the preview.
	Show(ctx context.Context, sessID int64, turns int) (query.Detail, error)
	// Status is the index summary shown in the header.
	Status(ctx context.Context) (query.Status, error)
	// Refresh runs one bounded sweep pass (150 ms / 32 MB): the first pass
	// after opening is ungated, like the CLI's pre-search sweep; later
	// passes (the background continuation of what the first deferred) go
	// through the busy/load gate so they never compete with a running
	// agent. It returns what the pass left behind.
	Refresh(ctx context.Context, gated bool) (ingest.Result, error)
	Close()
}

// recallIndex is the RecallSource over the real recall.db of this machine
// and the active profile's state.db (hints, tags, deck ids).
type recallIndex struct {
	profile  string
	cfg      *session.UserConfig
	st       *store.Store
	q        *query.Searcher
	reg      *statedb.StateDB
	registry *session.RecallRegistry
	storage  *session.Storage
	lock     string
	queue    string
	roots    []reader.Root
	mu       sync.Mutex
}

// openRecallIndex opens the index for the TUI when [recall] enabled is
// set; nil (and no error) when recall is off, so callers fall back to the
// local title search exactly as before.
func openRecallIndex(profile string) (RecallSource, error) {
	cfg, err := session.LoadUserConfig()
	if err != nil || cfg == nil || !cfg.Recall.GetEnabled() {
		return nil, nil
	}
	dbPath, err := recall.DBPath()
	if err != nil {
		return nil, err
	}
	lock, err := recall.LockPath()
	if err != nil {
		return nil, err
	}
	queue, err := recall.QueuePath()
	if err != nil {
		return nil, err
	}
	st, err := store.OpenCurrent(dbPath)
	if errors.Is(err, store.ErrSchema) {
		// Another schema version: recreate only under the sweep lock so a
		// running backfill is never pulled out from under; a held lock
		// means no index for this TUI run rather than a wait at startup.
		release, lerr := store.Lock(lock)
		if lerr != nil {
			return nil, lerr
		}
		st, err = store.Open(dbPath)
		release()
	}
	if err != nil {
		return nil, err
	}
	idx := &recallIndex{profile: profile, cfg: cfg, st: st, lock: lock, queue: queue, roots: session.RecallRoots()}
	stateDB := ""
	if storage, err := session.NewStorageWithProfile(profile); err == nil {
		idx.storage = storage
		idx.reg = storage.GetDB()
		idx.profile = storage.Profile()
		if p, err := session.GetDBPathForProfile(idx.profile); err == nil {
			stateDB = p
		}
	}
	idx.registry = session.NewRecallRegistry(idx.profile, idx.reg)
	idx.q = query.New(st, stateDB)
	return idx, nil
}

func (r *recallIndex) Search(ctx context.Context, q string, limit int) (query.SearchResult, error) {
	return r.q.Search(ctx, query.SearchOptions{Query: q, Limit: limit})
}

func (r *recallIndex) Show(ctx context.Context, sessID int64, turns int) (query.Detail, error) {
	return r.q.Show(ctx, fmt.Sprintf("#%d", sessID), turns)
}

func (r *recallIndex) Status(ctx context.Context) (query.Status, error) {
	return r.q.Status(ctx)
}

// Refresh is one sweep pass under the interactive budget. The sweep lock
// is taken without waiting: another sweep running means the pass is
// skipped and the index served as-is.
func (r *recallIndex) Refresh(ctx context.Context, gated bool) (ingest.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	release, err := store.Lock(r.lock)
	if err != nil {
		return ingest.Result{}, err
	}
	defer release()
	opts := ingest.Options{
		Roots:          r.roots,
		Registry:       r.registry,
		TextTier:       r.cfg.Recall.GetTextTier(),
		PerSourceBytes: int64(r.cfg.Recall.GetPerSourceMB()) << 20,
		QueuePath:      r.queue,
		NewestFirst:    true,
		Budget:         reader.NewBudget(ingest.InteractiveDeadline, ingest.InteractiveBytes),
	}
	if gated {
		opts.Gate = &ingest.Gate{Busy: func() (bool, string) { return session.RecallBusy(r.reg) }, MaxLoadAvg: r.cfg.Recall.GetMaxLoadAvg()}
	}
	return ingest.New(r.st, opts).Sweep(ctx)
}

func (r *recallIndex) Close() {
	if r.st != nil {
		r.st.Close()
	}
	if r.registry != nil {
		r.registry.Close()
	}
	if r.storage != nil {
		r.storage.Close()
	}
}

// recallSkipped reports whether a Refresh error is one of the expected
// "not now" outcomes (lock held, gate refused) rather than a failure.
func recallSkipped(err error) bool {
	return errors.Is(err, store.ErrLocked) || errors.Is(err, ingest.ErrGated) || errors.Is(err, os.ErrNotExist)
}
