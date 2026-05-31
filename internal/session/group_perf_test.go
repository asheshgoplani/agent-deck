package session

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// Tier-1 WARM perf gate for the storage-mediated group lifecycle.
//
// PR #790 explicitly carved group create/delete out of its first perf pass:
// done purely in-memory (GroupTree map mutation) it "exercises the wrong
// layer" — the map ops are nanoseconds and gate nothing real. This test does
// it AT the storage layer instead: each create/delete is followed by the
// persistence call the TUI/CLI actually make — Storage.SaveGroupsOnly, which
// rewrites the whole groups table in one transaction (DELETE FROM groups +
// re-INSERT all rows + COMMIT). That transaction commit is the CPU regression
// target Tier 1 gates.
//
// Why WARM, not COLD: the work is pure in-process Go against an SQLite handle
// on a t.TempDir() path (tmpfs on CI Linux). No process boundary, no real-disk
// fsync, no child spawn, no network. TrimmedMeanWarm forces a GC cycle per
// iteration and disables auto-GC in the timed window, so the tighter base×3
// WarmBudget is safe. See internal/testutil/perfbudget.go and
// docs/perf-budget-suite.md ("Tier 1 vs Tier 2").
//
// No real tmux. No network. Pure-Go in-process SQLite on tmpfs.

// perfGroupBaseline is the number of pre-existing groups the table is seeded
// with before the timed create/delete round trip. SaveGroupsOnly rewrites the
// full table, so a realistic baseline matters: a ~50-session deck (the cited
// upper-end size, internal/web/handlers_costs.go:529) organized at roughly
// five sessions per group lands near ten groups.
const perfGroupBaseline = 10

// perfGroupBase is the local median observed under -race at
// PERF_BUDGET_MULTIPLIER=1.0 for one create→persist→delete→persist round trip
// (two full-table-rewrite transactions). WarmBudget multiplies by 3 and
// applies the 1ms floor and the env multiplier (CI sets 2.0 → 6× local gate).
const perfGroupBase = 9 * time.Millisecond // → WarmBudget = 27ms local, 54ms CI

// newPerfStorage builds a Storage backed by a migrated SQLite DB on a
// tmpfs-backed t.TempDir() path. Constructed directly (rather than via
// NewStorageWithProfile) to keep the fixture hermetic — no HOME/profile
// migration scanning — while exercising the identical SaveGroupsOnly →
// db.SaveGroups path users hit.
func newPerfStorage(t *testing.T) *Storage {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	db, err := statedb.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return &Storage{db: db, dbPath: dbPath, profile: "perf"}
}

// TestPerf_Group_CreateDelete gates one storage-mediated group create+delete
// round trip against a representative baseline of pre-existing groups. The
// timed op creates a group, persists, deletes it, and persists again — two
// SaveGroupsOnly transactions. Because create-then-delete is net-zero on the
// tree, the fixture returns to baseline each iteration and TrimmedMeanWarm (no
// per-iter rebuild) applies.
func TestPerf_Group_CreateDelete(t *testing.T) {
	testutil.SkipIfShort(t)
	storage := newPerfStorage(t)

	// Seed a representative baseline of empty groups and persist once.
	tree := NewGroupTree(nil)
	for i := 0; i < perfGroupBaseline; i++ {
		tree.CreateGroup(fmt.Sprintf("group-%d", i))
	}
	if err := storage.SaveGroupsOnly(tree); err != nil {
		t.Fatalf("seed SaveGroupsOnly: %v", err)
	}

	budget := testutil.WarmBudget(t, perfGroupBase)

	got := testutil.TrimmedMeanWarm(func() {
		g := tree.CreateGroup("perf-ephemeral")
		if err := storage.SaveGroupsOnly(tree); err != nil {
			t.Fatalf("create SaveGroupsOnly: %v", err)
		}
		tree.DeleteGroup(g.Path)
		if err := storage.SaveGroupsOnly(tree); err != nil {
			t.Fatalf("delete SaveGroupsOnly: %v", err)
		}
	})

	if got > budget {
		t.Fatalf("group create+delete round trip trimmed mean = %v, budget = %v (regression in SaveGroupsOnly transaction path)", got, budget)
	}
	t.Logf("group create+delete round trip (baseline %d groups) trimmed mean = %v (budget = %v)", perfGroupBaseline, got, budget)
}
