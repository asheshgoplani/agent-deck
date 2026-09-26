package session

import "testing"

func TestAcceptsInputWhileBusy(t *testing.T) {
	for _, tc := range []struct {
		tool string
		want bool
	}{
		{"claude", true},
		{"codex", false},
		{"pi", false},
		{"shell", false},
		{"unrecognized", false},
	} {
		if got := AcceptsInputWhileBusy(tc.tool); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.tool, got, tc.want)
		}
	}
}
