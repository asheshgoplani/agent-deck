package main

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/fleet"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/stretchr/testify/require"
)

// newFleetCLIStorage opens a real Storage handle on the isolated test profile.
// Each call returns a fresh handle on the SAME on-disk state.db so we can
// simulate two concurrent processes the way production does (a recovery sweep
// running for minutes while the operator adds a session in the TUI).
func newFleetCLIStorage(t *testing.T) *session.Storage {
	t.Helper()
	s, err := session.NewStorageWithProfile("")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func fleetPersistInstance(id, title string, st session.Status) *session.Instance {
	return &session.Instance{
		ID:          id,
		Title:       title,
		ProjectPath: "/tmp/" + title,
		GroupPath:   "test",
		Command:     "claude",
		Tool:        "claude",
		Status:      st,
		CreatedAt:   time.Now().Add(-2 * time.Minute),
	}
}

// DATA-SAFETY REGRESSION GUARD for the whole recovery command.
//
// A 65-session sweep runs for minutes, so the snapshot it loaded is stale by the
// time it writes. The 2026-06-04 incident was exactly this shape: a stale
// snapshot written back through a sweeping save deleted rows another process had
// added. This test drives the EXACT seam the CLI wires (a fleet.Recoverer whose
// Persist is storage.PersistRecoveredInstances) and asserts a session added
// mid-sweep survives.
//
// A regression that swaps the persist back to saveSessionData / SaveWithGroups /
// SaveInstances is caught here, not only in a storage unit test.
func TestFleetRecoverCLI_PersistDoesNotClobberConcurrentAdd(t *testing.T) {
	// Own profile: saves are upsert-only, so rows written by other tests in the
	// shared _test profile would otherwise leak into this snapshot.
	t.Setenv("AGENTDECK_PROFILE", "_test_fleet_recover_persist")
	sweepStorage := newFleetCLIStorage(t)

	down := fleetPersistInstance("fleet-down", "down", session.StatusError)
	require.NoError(t, sweepStorage.SaveWithGroups(
		[]*session.Instance{down},
		session.NewGroupTree([]*session.Instance{down}),
	))

	// Step 1: the sweep loads its snapshot (one down session).
	snapshot, _, err := sweepStorage.LoadWithGroups()
	require.NoError(t, err)
	require.Len(t, snapshot, 1)

	// Step 2: a concurrent process inserts a brand-new session.
	addStorage := newFleetCLIStorage(t)
	added := fleetPersistInstance("fleet-added-concurrently", "added", session.StatusRunning)
	require.NoError(t, addStorage.InsertSessionAndVerify(
		added,
		session.NewGroupTree([]*session.Instance{down, added}),
	))

	// Step 3: the sweep restarts + persists through the production seam.
	as := fleet.Assessment{Total: 1, Down: 1, Candidates: []fleet.Candidate{{
		Instance: snapshot[0],
		Health:   fleet.HealthDown,
		Status:   string(session.StatusError),
	}}}
	rec := &fleet.Recoverer{
		Restart: func(inst *session.Instance) error {
			// Mirror what a real restart mutates: the session comes back running.
			inst.Status = session.StatusRunning
			return nil
		},
		Verify: func(*session.Instance) fleet.VerifyReport {
			return fleet.VerifyReport{PaneAlive: true, ToolStarted: true, Status: string(session.StatusRunning)}
		},
		Persist:   sweepStorage.PersistRecoveredInstances,
		Sleep:     func(time.Duration) {},
		NoSpacing: true,
	}

	sum := rec.Recover(as)
	require.Equal(t, 1, sum.Recovered, "sweep summary: %+v", sum)

	// Invariant 1: the concurrently-added session still exists.
	verifyStorage := newFleetCLIStorage(t)
	after, _, err := verifyStorage.LoadWithGroups()
	require.NoError(t, err)

	byID := map[string]*session.Instance{}
	for _, inst := range after {
		byID[inst.ID] = inst
	}
	require.Contains(t, byID, "fleet-added-concurrently",
		"recovery clobbered a session added during the sweep (the 2026-06-04 data-loss class)")

	// Invariant 2: the restarted session's new status was persisted.
	recovered := byID["fleet-down"]
	require.NotNil(t, recovered)
	require.Equal(t, session.StatusRunning, recovered.Status)

	// Invariant 3: the untouched row was not rewritten from the sweep's stale
	// snapshot — its own status survives.
	require.Equal(t, session.StatusRunning, byID["fleet-added-concurrently"].Status)
}

// PersistRecoveredInstances must ignore nils and report per-row failures without
// dropping the rows it can write.
func TestPersistRecoveredInstances_ToleratesNilEntries(t *testing.T) {
	t.Setenv("AGENTDECK_PROFILE", "_test_fleet_persist_nil")
	storage := newFleetCLIStorage(t)

	inst := fleetPersistInstance("fleet-nil-tolerant", "nil-tolerant", session.StatusRunning)
	require.NoError(t, storage.PersistRecoveredInstances([]*session.Instance{nil, inst, nil}))

	after, _, err := newFleetCLIStorage(t).LoadWithGroups()
	require.NoError(t, err)
	var found bool
	for _, got := range after {
		if got.ID == inst.ID {
			found = true
		}
	}
	require.True(t, found, "the non-nil instance was not persisted")
}
