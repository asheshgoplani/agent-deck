package session

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/stretchr/testify/require"
)

// rmTestStorage opens a Storage instance against a shared SQLite path,
// mirroring the multi-process layout that real parallel `agent-deck rm`
// invocations create. Each call returns a fresh *Storage with its own
// underlying *sql.DB connection pool.
func rmTestStorage(t *testing.T, dbPath string) *Storage {
	t.Helper()
	db, err := statedb.Open(dbPath)
	require.NoError(t, err)
	require.NoError(t, db.Migrate())
	t.Cleanup(func() { _ = db.Close() })
	return &Storage{db: db, dbPath: dbPath, profile: "_test"}
}

// TestRm_ParallelDoesNotLoseRemovals is the regression test for issue #909.
//
// Scenario: 14 sessions, 14 parallel `agent-deck rm` invocations (each from
// its own *Storage / *sql.DB pool, just like real xargs -P 14 processes).
// The load-modify-write inside SaveWithGroups (INSERT OR REPLACE for the
// remaining list) lets one process's commit resurrect rows that another
// process just deleted. Without the v1.9.1 verify-after-commit + busy retry,
// only ~3 of 14 actually persist as gone.
func TestRm_ParallelDoesNotLoseRemovals(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")

	const N = 14

	// Seed N stopped sessions through one storage.
	seed := rmTestStorage(t, dbPath)
	initial := make([]*Instance, N)
	for i := 0; i < N; i++ {
		initial[i] = &Instance{
			ID:          fmt.Sprintf("rm-race-session-%02d", i),
			Title:       fmt.Sprintf("Race %02d", i),
			ProjectPath: "/tmp/rm-race",
			GroupPath:   DefaultGroupPath,
			Command:     "echo",
			Tool:        "claude",
			Status:      StatusStopped,
			CreatedAt:   time.Now(),
		}
	}
	require.NoError(t, seed.SaveWithGroups(initial, NewGroupTree(initial)))

	// Fire N parallel "rm"s. Each goroutine simulates one agent-deck CLI
	// process: open its own Storage, load, filter, remove, save groups,
	// verify. This is exactly the flow handleRemove follows post-fix.
	barrier := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			s := rmTestStorage(t, dbPath)
			<-barrier
			removedID := fmt.Sprintf("rm-race-session-%02d", idx)
			loaded, groups, err := s.LoadWithGroups()
			if err != nil {
				errs[idx] = err
				return
			}
			newList := make([]*Instance, 0, len(loaded))
			for _, inst := range loaded {
				if inst.ID != removedID {
					newList = append(newList, inst)
				}
			}
			gt := NewGroupTreeWithGroups(newList, groups)
			errs[idx] = s.RemoveSessionAndVerify(removedID, newList, gt)
		}(i)
	}
	close(barrier)
	wg.Wait()

	// Every reported removal must have succeeded.
	for i, e := range errs {
		if e != nil {
			t.Fatalf("goroutine %d: RemoveSessionAndVerify failed: %v", i, e)
		}
	}

	// Independent storage: every row must be gone.
	check := rmTestStorage(t, dbPath)
	after, err := check.Load()
	require.NoError(t, err)
	if len(after) != 0 {
		survivors := make([]string, 0, len(after))
		for _, inst := range after {
			survivors = append(survivors, inst.ID)
		}
		t.Fatalf("expected 0 surviving sessions, got %d: %v", len(after), survivors)
	}
}
