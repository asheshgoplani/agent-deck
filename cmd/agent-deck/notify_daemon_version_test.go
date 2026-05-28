package main

import "testing"

// Issue #1214 STEP 1: the daemon parses the on-disk binary's `version` output
// to decide whether to recycle. Pin the parse so a format change can't silently
// disable the staleness guard.
func TestParseAgentDeckVersion(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Agent Deck v1.9.42\n", "1.9.42"},
		{"Agent Deck v1.9.42 (update available: v1.9.43)\n", "1.9.42"},
		{"Agent Deck v1.9.42", "1.9.42"},
		{"Agent Deck v1.10.0 (update available: v1.11.0)\nextra\n", "1.10.0"},
		{"garbage output", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := parseAgentDeckVersion(c.in); got != c.want {
			t.Errorf("parseAgentDeckVersion(%q)=%q want %q", c.in, got, c.want)
		}
	}
}
