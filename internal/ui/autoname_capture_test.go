package ui

import "testing"

// TestShouldPersistAutoNameDesc locks the decision the background status loop
// makes before writing an auto-named session's task description to SQLite. This
// is the capture half of the reopen fix: the read/round-trip halves are tested
// elsewhere, but the live-pane → disk path runs only on a background tick with a
// real DB, so its branching is pinned here as a pure function.
func TestShouldPersistAutoNameDesc(t *testing.T) {
	cases := []struct {
		name          string
		autoName      bool
		paneTitle     string
		lastPersisted string
		wantDesc      string
		wantWrite     bool
	}{
		{"not auto-named: never write", false, "Some task", "", "", false},
		{"auto-named, new description: write", true, "Review SketchUp models", "", "Review SketchUp models", true},
		{"auto-named, changed description: write", true, "New task", "Old task", "New task", true},
		{"auto-named, empty pane must not clobber saved desc", true, "", "Old task", "", false},
		{"auto-named, unchanged: skip to avoid write pressure", true, "Same task", "Same task", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			desc, write := shouldPersistAutoNameDesc(tc.autoName, tc.paneTitle, tc.lastPersisted)
			if write != tc.wantWrite || desc != tc.wantDesc {
				t.Errorf("shouldPersistAutoNameDesc(%v, %q, %q) = (%q, %v), want (%q, %v)",
					tc.autoName, tc.paneTitle, tc.lastPersisted, desc, write, tc.wantDesc, tc.wantWrite)
			}
		})
	}
}
