package session

import (
	"encoding/json"
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
// layer" — the map ops are nanoseconds and gate nothing real. This test gates
// it at the storage layer by reconstructing the exact choreography the real
// user-facing handlers perform — cmd/agent-deck/group_cmd.go handleGroupCreate
// (~L396-442) and handleGroupDelete (~L689-705):
//
//	storage.LoadWithGroups()                     // full read of all instances + groups
//	session.NewGroupTreeWithGroups(insts, grps)  // rebuild the tree
//	groupTree.CreateGroup / DeleteGroup          // the mutation
//	storage.SaveWithGroups(insts, groupTree)     // rewrite the WHOLE instances
//	                                             // table + groups + Touch() + dedup
//
// This is deliberately NOT Storage.SaveGroupsOnly: that is the lightweight
// expand/collapse visual-state path (its own doc comment says so) and skips
// both the instances round-trip and Touch(). Group create/delete goes through
// SaveWithGroups, where the dominant cost is the instances-table read+rewrite
// — which is why this test runs against a populated deck (perfGroupPopN
// instances), not an empty DB.
//
// What this test does NOT cover: the literal CLI shell (flag parsing,
// NewStorageWithProfile, out.Success/os.Exit). Driving that in-process needs
// the same injection seam deferred for session create/delete — see the PR
// description. The seam-free remainder (flag parse + stdout formatting) carries
// no meaningful, tmux-touching cost, so the storage choreography above is the
// faithful Tier-1 target.
//
// Why WARM, not COLD: pure in-process Go against an SQLite handle on a
// t.TempDir() path (tmpfs on CI Linux). No process boundary, no real-disk
// fsync, no child spawn, no network. Seeded rows carry TmuxSession="" so
// LoadWithGroups' lazy tmux.ReconnectSessionLazy branch never runs — zero tmux
// code executes. TrimmedMeanWarm forces a GC cycle per iteration and disables
// auto-GC in the timed window, so the tighter base×3 WarmBudget is safe. See
// internal/testutil/perfbudget.go and docs/perf-budget-suite.md ("Tier 1 vs
// Tier 2").
//
// No real tmux. No network. Pure-Go in-process SQLite on tmpfs.

// perfGroupPopN is the instance population the deck is seeded with. 50 is the
// representative upper-end deck size cited in code (internal/web/handlers_costs.go:529
// notes the sidebar "grows past ~50 sessions"); group create/delete rewrites
// this whole table via SaveWithGroups, so a realistic population is the point.
const perfGroupPopN = 50

// perfGroupBaseline is the number of groups the seeded deck spans (5 sessions
// per group across the perfGroupPopN instances → ~10 groups).
const perfGroupBaseline = 10

// perfGroupBase is the local median observed under -race at
// PERF_BUDGET_MULTIPLIER=1.0 for the real create+delete choreography (two full
// LoadWithGroups → SaveWithGroups round trips over a perfGroupPopN-instance
// deck). WarmBudget multiplies by 3 and applies the 1ms floor and the env
// multiplier (CI sets 2.0 → 6× local gate).
//
// Local median under -race: ~67ms (two LoadWithGroups + two SaveWithGroups
// full instances-table rewrites over a 50-instance deck).
const perfGroupBase = 70 * time.Millisecond // → WarmBudget = 210ms local, 420ms CI

// newPerfStorage builds a Storage backed by a migrated SQLite DB on a
// tmpfs-backed t.TempDir() path. Constructed directly (rather than via
// NewStorageWithProfile) to keep the fixture hermetic — no HOME/profile
// migration scanning — while exercising the identical LoadWithGroups /
// SaveWithGroups paths the CLI handlers hit.
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

// seedDeck persists perfGroupPopN instances spread across perfGroupBaseline
// groups directly via the statedb layer (setup, excluded from timing). Rows
// carry TmuxSession="" so the later LoadWithGroups conversion runs no tmux code.
func seedDeck(t *testing.T, storage *Storage) {
	t.Helper()
	rows := make([]*statedb.InstanceRow, perfGroupPopN)
	now := time.Now()
	for i := 0; i < perfGroupPopN; i++ {
		rows[i] = &statedb.InstanceRow{
			ID:          fmt.Sprintf("perf-%04d", i),
			Title:       fmt.Sprintf("Session %d", i),
			ProjectPath: fmt.Sprintf("/home/dev/project-%d", i),
			GroupPath:   fmt.Sprintf("group-%d", i%perfGroupBaseline),
			Order:       i,
			Tool:        "claude",
			Status:      "idle",
			CreatedAt:   now,
			ToolData:    json.RawMessage(`{"claude_session_id":"sess-abcdef"}`),
		}
	}
	if err := storage.db.SaveInstances(rows); err != nil {
		t.Fatalf("seed SaveInstances: %v", err)
	}
}

// TestPerf_Group_CreateDelete gates the storage-mediated group create+delete
// lifecycle by reconstructing the handleGroupCreate / handleGroupDelete
// choreography (see file header) against a perfGroupPopN-instance deck.
//
// The timed op runs both halves — a create cycle then a delete cycle, each a
// full LoadWithGroups → rebuild tree → mutate → SaveWithGroups round trip.
// Creating then deleting the same ephemeral group is net-zero on persisted
// state, so the deck returns to baseline each iteration and TrimmedMeanWarm (no
// per-iter fixture rebuild) applies. The dominant measured cost is the two
// instances-table read+rewrite passes — exactly the regression class Tier 1
// gates ("we added N ms of CPU work in the group save path").
func TestPerf_Group_CreateDelete(t *testing.T) {
	testutil.SkipIfShort(t)
	storage := newPerfStorage(t)
	seedDeck(t, storage)

	budget := testutil.WarmBudget(t, perfGroupBase)

	// createCycle mirrors handleGroupCreate: load, rebuild tree, CreateGroup,
	// SaveWithGroups(loaded instances, tree).
	createCycle := func() {
		instances, groups, err := storage.LoadWithGroups()
		if err != nil {
			t.Fatalf("create LoadWithGroups: %v", err)
		}
		tree := NewGroupTreeWithGroups(instances, groups)
		tree.CreateGroup("perf-ephemeral")
		if err := storage.SaveWithGroups(instances, tree); err != nil {
			t.Fatalf("create SaveWithGroups: %v", err)
		}
	}

	// deleteCycle mirrors handleGroupDelete: load, rebuild tree, DeleteGroup,
	// SaveWithGroups(tree.GetAllInstances(), tree).
	deleteCycle := func() {
		instances, groups, err := storage.LoadWithGroups()
		if err != nil {
			t.Fatalf("delete LoadWithGroups: %v", err)
		}
		tree := NewGroupTreeWithGroups(instances, groups)
		tree.DeleteGroup("perf-ephemeral")
		if err := storage.SaveWithGroups(tree.GetAllInstances(), tree); err != nil {
			t.Fatalf("delete SaveWithGroups: %v", err)
		}
	}

	got := testutil.TrimmedMeanWarm(func() {
		createCycle()
		deleteCycle()
	})

	if got > budget {
		t.Fatalf("group create+delete (LoadWithGroups→SaveWithGroups ×2, %d-instance deck) trimmed mean = %v, budget = %v (regression in the group save path)", perfGroupPopN, got, budget)
	}
	t.Logf("group create+delete (LoadWithGroups→SaveWithGroups ×2, %d-instance deck) trimmed mean = %v (budget = %v)", perfGroupPopN, got, budget)
}
