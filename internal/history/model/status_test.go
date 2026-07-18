package model

import "testing"

func TestStatusGlyphAndLabel(t *testing.T) {
	cases := map[SessionStatus][2]string{
		StatusClosed:      {"·", "idle"},
		StatusRecent:      {"○", "recent"},
		StatusRunningIdle: {"◐", "running"},
		StatusRunningBusy: {"●", "running"},
	}
	for st, want := range cases {
		if st.Glyph() != want[0] || st.Label() != want[1] {
			t.Errorf("%d: glyph=%q label=%q, want %v", st, st.Glyph(), st.Label(), want)
		}
	}
}
