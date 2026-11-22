package main

import "testing"

// The order of the shared rule, on the matcher itself (the behaviour tests
// are in session_ref_match_test.go).
func TestMatchSessionRef_Order(t *testing.T) {
	ids := []string{"abcd1111", "abcd2222", "ffff0000", "eeee0000"}
	titles := []string{"one", "abcd2222", "ffff", micMarker + "One"}
	match := func(ref string) (int, []int, string) {
		return matchSessionRef(ref, len(ids), func(i int) string { return ids[i] }, func(i int) string { return titles[i] })
	}
	for _, tc := range []struct {
		ref    string
		index  int
		tied   int
		tiedOn string
	}{
		{"abcd2222", 1, 0, ""},                  // exact id, also a title
		{"one", 0, 0, ""},                       // exact title before the folded one
		{"ffff", 2, 0, ""},                      // exact title, also an id prefix of the same session
		{"eeee", 3, 0, ""},                      // unique id prefix
		{"ONE", -1, 2, sessionRefTiedTitle},     // folded title held twice
		{"abcd", -1, 2, sessionRefTiedID},       // id prefix held twice
		{"abc", -1, 0, ""},                      // too short for an id prefix
		{"", -1, 0, ""},                         // empty
		{micMarker, -1, 0, ""},                  // decoration only never matches by title
		{"  one  ", -1, 2, sessionRefTiedTitle}, // surrounding space: only the folded pass sees it
		{"eeee 0", -1, 0, ""},                   // a prefix has no spaces
		{micMarker + "one", -1, 2, sessionRefTiedTitle},
	} {
		index, tied, tiedOn := match(tc.ref)
		if index != tc.index || len(tied) != tc.tied || tiedOn != tc.tiedOn {
			t.Errorf("matchSessionRef(%q) = %d, %v, %q; want %d, %d tied, %q", tc.ref, index, tied, tiedOn, tc.index, tc.tied, tc.tiedOn)
		}
	}
}
