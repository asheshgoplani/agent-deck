package statedb

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// Tier-1 WARM perf gates for the StateDB persistence layer.
//
// These tests cover the deferred "persistence-touching lifecycle" items from
// PR #790 / #1047 (see docs/perf-budget-suite.md, "Tier 1 vs Tier 2"). They
// run against a t.TempDir() SQLite database — tmpfs on CI Linux — so fsync is
// effectively free and the measured cost is CPU + Go-runtime cost only (driver
// marshalling, statement prep, row scanning, SQLite page churn). That is the
// regression class Tier 1 gates: "we added N ms of CPU work in the save/read
// path". It does NOT gate fsync/transaction *counts* — that is Tier 2 (count
// assertions), owned by a sibling session, see the //TODO in the watcher-event
// test below.
//
// Why WARM, not COLD: every operation is pure in-process Go against an
// SQLite handle on tmpfs. No process boundary, no real-disk fsync, no child
// spawn, no network — exactly the WARM criteria. WARM measurement
// (TrimmedMeanWarm / TrimmedMeanWarmWithSetup) forces a GC cycle per iteration and
// disables auto-GC inside the timed window, removing the dominant noise source;
// the tighter base×3 WarmBudget is therefore safe. See
// internal/testutil/perfbudget.go for the full cold/warm convention.
//
// Population size N: 50 rows. This is the representative upper-end session
// count cited in the codebase — internal/web/handlers_costs.go:529 notes the
// sidebar "grows past ~50 sessions" (and llms-full.txt cites 30-session
// workspaces). 50 is a realistic heavily-loaded deck, not an invented number.
//
// No real tmux. No network. Pure-Go in-process SQLite on tmpfs.

// perfPopN is the representative session population for the CRUD scaling gates.
// See file header: ~50 is the cited upper-end deck size
// (internal/web/handlers_costs.go:529).
const perfPopN = 50

// Base local medians observed under -race at PERF_BUDGET_MULTIPLIER=1.0
// (Linux/WSL2, this dev host). WarmBudget multiplies by 3 and applies the
// 1ms floor and the env multiplier (CI sets 2.0 → 6× local gate).
const (
	// 50× SaveInstance into an empty table (per-session insert path).
	// Local median under -race: ~206ms (50 autocommit WAL txns dominate).
	perfStateDBInsertBase = 210 * time.Millisecond // → WarmBudget = 630ms local, 1.26s CI
	// 50× InstanceExists (read-by-id query path; rm-verify uses this).
	// Local median under -race: ~9.3ms.
	perfStateDBReadByIDBase = 10 * time.Millisecond // → WarmBudget = 30ms local, 60ms CI
	// One LoadInstances over a 50-row table (list path).
	// Local median under -race: ~6.9ms.
	perfStateDBListBase = 7 * time.Millisecond // → WarmBudget = 21ms local, 42ms CI
	// 50× DeleteInstance (the `agent-deck rm` xargs path, issue #909).
	// Local median under -race: ~166ms (50 autocommit txns + withBusyRetry).
	perfStateDBDeleteBase = 170 * time.Millisecond // → WarmBudget = 510ms local, 1.02s CI
	// Ingest 50 fresh events on a 500-event steady-state table (insert + prune
	// per event; prune is the DELETE ... NOT IN subquery, the CPU target here).
	// Local median under -race: ~1.42s.
	perfWatcherIngestBase = 1450 * time.Millisecond // → WarmBudget = 4.35s local, 8.7s CI
)

// newPerfDB opens a migrated StateDB on a tmpfs-backed t.TempDir() path.
func newPerfDB(t *testing.T) *StateDB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "perf.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// perfRows builds n representative instance rows with stable ids.
func perfRows(n int) []*InstanceRow {
	rows := make([]*InstanceRow, n)
	now := time.Now()
	for i := 0; i < n; i++ {
		rows[i] = &InstanceRow{
			ID:          fmt.Sprintf("perf-%04d", i),
			Title:       fmt.Sprintf("Session %d", i),
			ProjectPath: fmt.Sprintf("/home/dev/project-%d", i),
			GroupPath:   fmt.Sprintf("group-%d", i%5),
			Order:       i,
			Tool:        "claude",
			Status:      "idle",
			CreatedAt:   now,
			ToolData:    json.RawMessage(`{"claude_session_id":"sess-abcdef"}`),
		}
	}
	return rows
}

// clearInstances truncates the instances table (setup helper, excluded from
// the timed window).
func clearInstances(t *testing.T, db *StateDB) {
	t.Helper()
	if _, err := db.DB().Exec("DELETE FROM instances"); err != nil {
		t.Fatalf("clear instances: %v", err)
	}
}

// TestPerf_StateDB_InsertN gates the cost of creating perfPopN sessions one at
// a time via SaveInstance (the per-session save: SELECT existing tool_data +
// INSERT OR REPLACE). Catches CPU regressions in the row-marshalling /
// tool-data-merge path.
func TestPerf_StateDB_InsertN(t *testing.T) {
	testutil.SkipIfShort(t)
	db := newPerfDB(t)
	rows := perfRows(perfPopN)
	budget := testutil.WarmBudget(t, perfStateDBInsertBase)

	got := testutil.TrimmedMeanWarmWithSetup(
		func() { clearInstances(t, db) },
		func() {
			for _, r := range rows {
				if err := db.SaveInstance(r); err != nil {
					t.Fatalf("SaveInstance: %v", err)
				}
			}
		},
	)

	if got > budget {
		t.Fatalf("insert %d instances trimmed mean = %v, budget = %v (regression in SaveInstance marshalling/tool-data merge)", perfPopN, got, budget)
	}
	t.Logf("insert %d instances trimmed mean = %v (budget = %v)", perfPopN, got, budget)
}

// TestPerf_StateDB_ReadByID gates perfPopN read-by-id lookups (InstanceExists —
// the indexed point-query used by the rm-verify path, issue #909). The table is
// populated once outside the timed window; the op is read-only, so
// TrimmedMeanWarm (no per-iter fixture rebuild) is sufficient.
func TestPerf_StateDB_ReadByID(t *testing.T) {
	testutil.SkipIfShort(t)
	db := newPerfDB(t)
	rows := perfRows(perfPopN)
	if err := db.SaveInstances(rows); err != nil {
		t.Fatalf("SaveInstances: %v", err)
	}
	budget := testutil.WarmBudget(t, perfStateDBReadByIDBase)

	got := testutil.TrimmedMeanWarm(func() {
		for _, r := range rows {
			ok, err := db.InstanceExists(r.ID)
			if err != nil || !ok {
				t.Fatalf("InstanceExists(%s) = %v, %v", r.ID, ok, err)
			}
		}
	})

	if got > budget {
		t.Fatalf("read-by-id ×%d trimmed mean = %v, budget = %v (regression in point-query path)", perfPopN, got, budget)
	}
	t.Logf("read-by-id ×%d trimmed mean = %v (budget = %v)", perfPopN, got, budget)
}

// TestPerf_StateDB_List gates one LoadInstances over a perfPopN-row table (the
// full-scan + per-row Scan path hit on every reload). Read-only → TrimmedMeanWarm.
func TestPerf_StateDB_List(t *testing.T) {
	testutil.SkipIfShort(t)
	db := newPerfDB(t)
	rows := perfRows(perfPopN)
	if err := db.SaveInstances(rows); err != nil {
		t.Fatalf("SaveInstances: %v", err)
	}
	budget := testutil.WarmBudget(t, perfStateDBListBase)

	got := testutil.TrimmedMeanWarm(func() {
		loaded, err := db.LoadInstances()
		if err != nil {
			t.Fatalf("LoadInstances: %v", err)
		}
		if len(loaded) != perfPopN {
			t.Fatalf("LoadInstances returned %d rows, want %d", len(loaded), perfPopN)
		}
	})

	if got > budget {
		t.Fatalf("list %d instances trimmed mean = %v, budget = %v (regression in full-scan/row-scan path)", perfPopN, got, budget)
	}
	t.Logf("list %d instances trimmed mean = %v (budget = %v)", perfPopN, got, budget)
}

// TestPerf_StateDB_DeleteN gates removing perfPopN sessions one at a time via
// DeleteInstance (the `agent-deck rm` path; xargs -P fan-out hits this per
// row, issue #909). Each iteration mutates state, so the fixture is rebuilt in
// setup (excluded from timing) via TrimmedMeanWarmWithSetup.
func TestPerf_StateDB_DeleteN(t *testing.T) {
	testutil.SkipIfShort(t)
	db := newPerfDB(t)
	rows := perfRows(perfPopN)
	budget := testutil.WarmBudget(t, perfStateDBDeleteBase)

	got := testutil.TrimmedMeanWarmWithSetup(
		func() {
			clearInstances(t, db)
			if err := db.SaveInstances(rows); err != nil {
				t.Fatalf("repopulate: %v", err)
			}
		},
		func() {
			for _, r := range rows {
				if err := db.DeleteInstance(r.ID); err != nil {
					t.Fatalf("DeleteInstance: %v", err)
				}
			}
		},
	)

	if got > budget {
		t.Fatalf("delete %d instances trimmed mean = %v, budget = %v (regression in DeleteInstance path)", perfPopN, got, budget)
	}
	t.Logf("delete %d instances trimmed mean = %v (budget = %v)", perfPopN, got, budget)
}

// perfWatcherSteadyState is the number of events the watcher_events table is
// pre-filled to before the timed batch — the default retention bound
// (max_events_per_watcher default 500, internal/session/userconfig.go:3419).
const perfWatcherSteadyState = 500

// perfWatcherIngestBatch is the number of fresh events ingested per timed
// iteration on top of the steady state.
const perfWatcherIngestBatch = 50

// TestPerf_StateDB_WatcherEventIngest gates the watcher-event ingestion path:
// SaveWatcherEvent does an INSERT OR IGNORE followed (on a real insert) by a
// prune — DELETE ... WHERE id NOT IN (SELECT ... ORDER BY id DESC LIMIT N).
// That prune subquery is the CPU regression target; its cost scales with the
// steady-state table size, so we pre-fill to the default 500-event bound and
// time a 50-event batch on top. Each batch event triggers one insert + one
// prune at a full table.
//
// Fixture (a 500-row table) is rebuilt per iteration in setup, excluded from
// timing, via TrimmedMeanWarmWithSetup.
//
// TODO(tier2): a Tier 2 count assertion belongs here too — "ingesting one new
// event past the bound = exactly 1 INSERT + 1 prune DELETE, no extra round
// trips". Counts are syscall-side (COLD); left to the sibling outbox/drain
// session per docs/perf-budget-suite.md.
func TestPerf_StateDB_WatcherEventIngest(t *testing.T) {
	testutil.SkipIfShort(t)
	db := newPerfDB(t)

	const watcherID = "perf-watcher"
	if err := db.SaveWatcher(&WatcherRow{
		ID:     watcherID,
		Name:   "perf",
		Type:   "gmail",
		Status: "running",
	}); err != nil {
		t.Fatalf("SaveWatcher: %v", err)
	}

	// prefill rebuilds the steady-state table directly (bypassing the prune in
	// SaveWatcherEvent) so setup cost stays low and the timed op sees a full
	// 500-row table.
	prefill := func() {
		if _, err := db.DB().Exec("DELETE FROM watcher_events"); err != nil {
			t.Fatalf("clear events: %v", err)
		}
		tx, err := db.DB().Begin()
		if err != nil {
			t.Fatalf("begin prefill: %v", err)
		}
		stmt, err := tx.Prepare(`INSERT INTO watcher_events (watcher_id, dedup_key, created_at) VALUES (?, ?, ?)`)
		if err != nil {
			t.Fatalf("prepare prefill: %v", err)
		}
		now := time.Now().Unix()
		for i := 0; i < perfWatcherSteadyState; i++ {
			if _, err := stmt.Exec(watcherID, fmt.Sprintf("base-%05d", i), now+int64(i)); err != nil {
				t.Fatalf("prefill exec: %v", err)
			}
		}
		_ = stmt.Close()
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit prefill: %v", err)
		}
	}

	budget := testutil.WarmBudget(t, perfWatcherIngestBase)

	got := testutil.TrimmedMeanWarmWithSetup(
		prefill,
		func() {
			for i := 0; i < perfWatcherIngestBatch; i++ {
				inserted, err := db.SaveWatcherEvent(watcherID, fmt.Sprintf("batch-%05d", i), "sender@example.com", "subj", "", "", "", perfWatcherSteadyState)
				if err != nil {
					t.Fatalf("SaveWatcherEvent: %v", err)
				}
				if !inserted {
					t.Fatalf("SaveWatcherEvent batch-%05d unexpectedly deduped", i)
				}
			}
		},
	)

	if got > budget {
		t.Fatalf("ingest %d events @ steady-state %d trimmed mean = %v, budget = %v (regression in insert+prune path)", perfWatcherIngestBatch, perfWatcherSteadyState, got, budget)
	}
	t.Logf("ingest %d events @ steady-state %d trimmed mean = %v (budget = %v)", perfWatcherIngestBatch, perfWatcherSteadyState, got, budget)
}
