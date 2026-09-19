package session

import (
	"reflect"
	"testing"
	"time"
)

// TestMRUHistory_Table drives the ring through Visit/WalkBack/WalkForward/
// Alternate sequences and checks the resulting snapshot + current position at
// each step, covering the three properties #2058 calls out: bounded size,
// dedup on revisit, and stable ordering across a walk burst.
func TestMRUHistory_Table(t *testing.T) {
	type step struct {
		name string
		do   func(m *MRUHistory) (got string, ok bool)
		// checkResult gates the got/ok assertions below: mutation-only steps
		// (plain Visit calls) leave this false since their do() return value
		// is meaningless.
		checkResult bool
		// checkAlt gates the Alternate() assertion: only steps that spell out
		// wantAlt/wantAltOK opt in, so an intermediate "visit a"/"visit b" setup
		// step doesn't have to track the alternate pointer's incidental state.
		checkAlt bool

		wantEntries []string
		wantCurrent string
		wantAlt     string
		wantAltOK   bool
		wantGot     string
		wantOK      bool
	}

	cases := []struct {
		name  string
		steps []step
	}{
		{
			name: "visit appends and dedups",
			steps: []step{
				{
					name:        "visit a",
					do:          func(m *MRUHistory) (string, bool) { m.Visit("a"); return "", true },
					wantEntries: []string{"a"},
					wantCurrent: "a",
				},
				{
					name:        "visit b",
					do:          func(m *MRUHistory) (string, bool) { m.Visit("b"); return "", true },
					wantEntries: []string{"a", "b"},
					wantCurrent: "b",
					checkAlt:    true,
					wantAlt:     "a",
					wantAltOK:   true,
				},
				{
					name:        "revisit a moves it to the front, dedup (no duplicate)",
					do:          func(m *MRUHistory) (string, bool) { m.Visit("a"); return "", true },
					wantEntries: []string{"b", "a"},
					wantCurrent: "a",
					checkAlt:    true,
					wantAlt:     "b",
					wantAltOK:   true,
				},
				{
					name:        "re-visiting the current entry is a no-op (alt unchanged)",
					do:          func(m *MRUHistory) (string, bool) { m.Visit("a"); return "", true },
					wantEntries: []string{"b", "a"},
					wantCurrent: "a",
					checkAlt:    true,
					wantAlt:     "b",
					wantAltOK:   true,
				},
			},
		},
		{
			name: "walk burst holds the order stable, then a fresh visit truncates redo",
			steps: []step{
				{name: "visit a", do: func(m *MRUHistory) (string, bool) { m.Visit("a"); return "", true }},
				{name: "visit b", do: func(m *MRUHistory) (string, bool) { m.Visit("b"); return "", true }},
				{
					name:        "visit c",
					do:          func(m *MRUHistory) (string, bool) { m.Visit("c"); return "", true },
					wantEntries: []string{"a", "b", "c"},
					wantCurrent: "c",
					checkAlt:    true,
					wantAlt:     "b",
					wantAltOK:   true,
				},
				{
					name:        "walk back to b",
					do:          func(m *MRUHistory) (string, bool) { return m.WalkBack() },
					checkResult: true,
					wantGot:     "b",
					wantOK:      true,
					wantEntries: []string{"a", "b", "c"}, // unchanged: walking never mutates entries
					wantCurrent: "b",
					checkAlt:    true,
					wantAlt:     "b", // Visit("c") set alt="b" earlier; a pure walk step never touches it
					wantAltOK:   true,
				},
				{
					name:        "walk back to a",
					do:          func(m *MRUHistory) (string, bool) { return m.WalkBack() },
					checkResult: true,
					wantGot:     "a",
					wantOK:      true,
					wantEntries: []string{"a", "b", "c"},
					wantCurrent: "a",
				},
				{
					name:        "walk back past the start reports not-ok and does not move",
					do:          func(m *MRUHistory) (string, bool) { return m.WalkBack() },
					checkResult: true,
					wantGot:     "",
					wantOK:      false,
					wantEntries: []string{"a", "b", "c"},
					wantCurrent: "a",
				},
				{
					name:        "walk forward redoes to b",
					do:          func(m *MRUHistory) (string, bool) { return m.WalkForward() },
					checkResult: true,
					wantGot:     "b",
					wantOK:      true,
					wantEntries: []string{"a", "b", "c"},
					wantCurrent: "b",
				},
				{
					// Confirming the walked-to position via Visit (as the attach
					// handler does after WalkBack/WalkForward) must be a no-op: it
					// is already the current entry, so no truncation happens.
					name:        "confirming the walk step with Visit does not truncate the redo stack",
					do:          func(m *MRUHistory) (string, bool) { m.Visit("b"); return "", true },
					wantEntries: []string{"a", "b", "c"},
					wantCurrent: "b",
				},
				{
					name:        "walk forward again still reaches c (redo stack survived)",
					do:          func(m *MRUHistory) (string, bool) { return m.WalkForward() },
					checkResult: true,
					wantGot:     "c",
					wantOK:      true,
					wantEntries: []string{"a", "b", "c"},
					wantCurrent: "c",
				},
				{
					name:        "walk forward past the end reports not-ok",
					do:          func(m *MRUHistory) (string, bool) { return m.WalkForward() },
					checkResult: true,
					wantGot:     "",
					wantOK:      false,
					wantEntries: []string{"a", "b", "c"},
					wantCurrent: "c",
				},
				{
					name:        "walk back twice to a",
					do:          func(m *MRUHistory) (string, bool) { m.WalkBack(); return m.WalkBack() },
					checkResult: true,
					wantGot:     "a",
					wantOK:      true,
					wantEntries: []string{"a", "b", "c"},
					wantCurrent: "a",
				},
				{
					// A genuinely new visit while sitting mid-history (after
					// walking back) discards the forward/redo branch, exactly
					// like browser back/forward history.
					name:        "a fresh visit from mid-history truncates the redo stack",
					do:          func(m *MRUHistory) (string, bool) { m.Visit("d"); return "", true },
					wantEntries: []string{"a", "d"},
					wantCurrent: "d",
					checkAlt:    true,
					wantAlt:     "a",
					wantAltOK:   true,
				},
			},
		},
		{
			name: "alternate toggle swaps back and forth like vim Ctrl+^",
			steps: []step{
				{name: "visit a", do: func(m *MRUHistory) (string, bool) { m.Visit("a"); return "", true }},
				{
					name:        "visit b",
					do:          func(m *MRUHistory) (string, bool) { m.Visit("b"); return "", true },
					checkAlt:    true,
					wantAlt:     "a",
					wantAltOK:   true,
					wantCurrent: "b",
					wantEntries: []string{"a", "b"},
				},
				{
					name: "toggle to the alternate (a) and confirm via Visit, as the handler does",
					do: func(m *MRUHistory) (string, bool) {
						id, ok := m.Alternate()
						if ok {
							m.Visit(id)
						}
						return id, ok
					},
					checkResult: true,
					wantGot:     "a",
					wantOK:      true,
					wantCurrent: "a",
					checkAlt:    true,
					wantAlt:     "b", // swapped: the alternate is now the session we just left
					wantAltOK:   true,
					wantEntries: []string{"b", "a"},
				},
				{
					name: "toggle again swaps back to b",
					do: func(m *MRUHistory) (string, bool) {
						id, ok := m.Alternate()
						if ok {
							m.Visit(id)
						}
						return id, ok
					},
					checkResult: true,
					wantGot:     "b",
					wantOK:      true,
					wantCurrent: "b",
					checkAlt:    true,
					wantAlt:     "a",
					wantAltOK:   true,
					wantEntries: []string{"a", "b"},
				},
			},
		},
		{
			name: "bounded: exceeding capacity drops the oldest entry",
			steps: []step{
				{name: "visit a", do: func(m *MRUHistory) (string, bool) { m.Visit("a"); return "", true }},
				{
					name:      "visit b",
					do:        func(m *MRUHistory) (string, bool) { m.Visit("b"); return "", true },
					checkAlt:  true,
					wantAlt:   "a",
					wantAltOK: true,
				},
				{
					name:        "visit c overflows a capacity-2 ring, dropping a",
					do:          func(m *MRUHistory) (string, bool) { m.Visit("c"); return "", true },
					wantEntries: []string{"b", "c"},
					wantCurrent: "c",
					checkAlt:    true,
					wantAlt:     "b", // unchanged: current was "b" immediately before this visit
					wantAltOK:   true,
				},
				{
					name:        "walk back only reaches b, the oldest surviving entry",
					do:          func(m *MRUHistory) (string, bool) { return m.WalkBack() },
					checkResult: true,
					wantGot:     "b",
					wantOK:      true,
					wantEntries: []string{"b", "c"},
					wantCurrent: "b",
				},
			},
		},
		{
			name: "Remove drops a closed session and clears a stale alternate",
			steps: []step{
				{name: "visit a", do: func(m *MRUHistory) (string, bool) { m.Visit("a"); return "", true }},
				{
					name:      "visit b",
					do:        func(m *MRUHistory) (string, bool) { m.Visit("b"); return "", true },
					checkAlt:  true,
					wantAlt:   "a",
					wantAltOK: true,
				},
				{
					name:        "closing the alternate (a) removes it from the ring and clears alt",
					do:          func(m *MRUHistory) (string, bool) { m.Remove("a"); return "", true },
					wantEntries: []string{"b"},
					wantCurrent: "b",
					checkAlt:    true,
					wantAltOK:   false,
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			capacity := 50
			if tc.name == "bounded: exceeding capacity drops the oldest entry" {
				capacity = 2
			}
			m := NewMRUHistory(capacity)
			for _, st := range tc.steps {
				got, ok := st.do(m)
				if st.checkResult {
					if got != st.wantGot {
						t.Fatalf("%s: got = %q, want %q", st.name, got, st.wantGot)
					}
					if ok != st.wantOK {
						t.Fatalf("%s: ok = %v, want %v", st.name, ok, st.wantOK)
					}
				}
				if st.wantEntries != nil {
					if snap := m.Snapshot(); !reflect.DeepEqual(snap, st.wantEntries) {
						t.Fatalf("%s: entries = %v, want %v", st.name, snap, st.wantEntries)
					}
				}
				if st.wantCurrent != "" {
					if cur := m.currentID(); cur != st.wantCurrent {
						t.Fatalf("%s: current = %q, want %q", st.name, cur, st.wantCurrent)
					}
				}
				if st.checkAlt {
					if alt, altOK := m.Alternate(); altOK != st.wantAltOK || (altOK && alt != st.wantAlt) {
						t.Fatalf("%s: Alternate() = (%q, %v), want (%q, %v)", st.name, alt, altOK, st.wantAlt, st.wantAltOK)
					}
				}
			}
		})
	}
}

// TestMRUHistoryFromInstances_SurvivesRestart seeds a fresh ring from
// persisted LastAccessedAt timestamps the way a TUI restart does, and checks
// that the walk order and current position match what an uninterrupted
// in-memory session would have produced.
func TestMRUHistoryFromInstances_SurvivesRestart(t *testing.T) {
	base := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	instances := []*Instance{
		{ID: "old", LastAccessedAt: base.Add(-2 * time.Hour)},
		{ID: "mid", LastAccessedAt: base.Add(-1 * time.Hour)},
		{ID: "new", LastAccessedAt: base},
		{ID: "never-attached"}, // zero LastAccessedAt: excluded
		nil,                    // defensive: nil instances are skipped
	}

	m := NewMRUHistoryFromInstances(instances, 50)

	if got, want := m.Snapshot(), []string{"old", "mid", "new"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Snapshot() = %v, want %v", got, want)
	}
	if got := m.currentID(); got != "new" {
		t.Fatalf("currentID() = %q, want %q", got, "new")
	}
	if id, ok := m.WalkBack(); !ok || id != "mid" {
		t.Fatalf("WalkBack() = (%q, %v), want (%q, true)", id, ok, "mid")
	}
	if id, ok := m.WalkBack(); !ok || id != "old" {
		t.Fatalf("WalkBack() = (%q, %v), want (%q, true)", id, ok, "old")
	}
	if _, ok := m.WalkBack(); ok {
		t.Fatalf("WalkBack() past the oldest seeded entry should report ok=false")
	}
}

// TestMRUHistoryFromInstances_TrimsToCapacity checks that seeding from a
// persisted last_accessed column too, respects the bound rather than only
// enforcing it for in-process Visit calls.
func TestMRUHistoryFromInstances_TrimsToCapacity(t *testing.T) {
	base := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	instances := []*Instance{
		{ID: "a", LastAccessedAt: base.Add(-3 * time.Hour)},
		{ID: "b", LastAccessedAt: base.Add(-2 * time.Hour)},
		{ID: "c", LastAccessedAt: base.Add(-1 * time.Hour)},
	}

	m := NewMRUHistoryFromInstances(instances, 2)

	if got, want := m.Snapshot(), []string{"b", "c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Snapshot() = %v, want %v (oldest entry should be dropped)", got, want)
	}
	if got := m.currentID(); got != "c" {
		t.Fatalf("currentID() = %q, want %q", got, "c")
	}
}
