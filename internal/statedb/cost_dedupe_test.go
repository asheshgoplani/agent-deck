package statedb

import (
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func costIDs(t *testing.T, s *StateDB) []string {
	t.Helper()
	rows, err := s.DB().Query(`SELECT id FROM cost_events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func insertCost(t *testing.T, s *StateDB, id, session string, ts time.Time, in, out int64) {
	t.Helper()
	if _, err := s.DB().Exec(`INSERT INTO cost_events (id, session_id, timestamp, model, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cost_microdollars)
		VALUES (?, ?, ?, 'claude-test', ?, ?, 2, 1, 100)`, id, session, ts.UTC().Format(time.RFC3339), in, out); err != nil {
		t.Fatal(err)
	}
}

// Review follow-up (1): rows stored before the upgrade. An old Stop hook row
// "<instance>_<unix nanos>" and the importer row "<instance>_<record uuid>"
// for the same turn counted twice; the next open drops the hook row when it
// clearly matches, and only then.
func TestFoldLegacyHookCostRows(t *testing.T) {
	turn := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for _, order := range []string{"hook stored first", "importer stored first"} {
		t.Run(order, func(t *testing.T) {
			s, err := Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if err := s.Migrate(); err != nil {
				t.Fatal(err)
			}
			hook := func(id string, at time.Time, in int64) { insertCost(t, s, id, "inst", at, in, 50) }
			imp := func(id string, at time.Time, in int64) { insertCost(t, s, id, "inst", at, in, 50) }
			if order == "hook stored first" {
				hook("inst_1790000000000000001", turn.Add(20*time.Second), 1000) // matches a1
				imp("inst_a1", turn, 1000)
			} else {
				imp("inst_a1", turn, 1000)
				hook("inst_1790000000000000001", turn.Add(20*time.Second), 1000)
			}
			hook("inst_1790000000000000002", turn.Add(25*time.Second), 1000)       // same usage, a1 already absorbed one
			hook("inst_1790000000000000003", turn.Add(30*time.Second), 999)        // tokens differ: no match
			hook("inst_1790000000000000004", turn.Add(3*time.Hour), 1000)          // outside the window
			imp("inst_b2", turn.Add(time.Hour), 2000)                              // importer row without a hook copy
			insertCost(t, s, "other_1790000000000000005", "other", turn, 1000, 50) // another session's hook row
			// The upgrade: the first open with the fold not yet recorded.
			if _, err := s.DB().Exec(`DELETE FROM metadata WHERE key = ?`, costHookDedupeKey); err != nil {
				t.Fatal(err)
			}
			if err := s.Migrate(); err != nil {
				t.Fatal(err)
			}
			want := []string{"inst_1790000000000000002", "inst_1790000000000000003", "inst_1790000000000000004", "inst_a1", "inst_b2", "other_1790000000000000005"}
			if got := costIDs(t, s); !equalStrings(got, want) {
				t.Fatalf("rows after the fold = %v, want %v", got, want)
			}
			// Once only: a later open changes nothing.
			hook("inst_1790000000000000006", turn.Add(time.Hour+time.Second), 2000)
			if err := s.Migrate(); err != nil {
				t.Fatal(err)
			}
			if got := costIDs(t, s); len(got) != len(want)+1 {
				t.Fatalf("a second open folded again: %v", got)
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
