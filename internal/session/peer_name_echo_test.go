package session

import "testing"

// The bug this guards: agent-deck launches every Claude session with
// `--name <ClaudePeerName()>` as a messaging address. Claude 2.1.237 records
// that in ~/.claude/sessions/<pid>.json with no nameSource, so the title sync
// read it back as a user rename — collapsing every session's title to its
// random handle and appending the id suffix again on each restart.
func TestIsClaudePeerNameEcho(t *testing.T) {
	const id = "07cc4f4e-1787223144"

	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"exact address the deck passed", "pale-swallow-07cc4f4e", true},
		{"address after one feedback round", "pale-swallow-07cc4f4e-07cc4f4e", true},
		{"address after two rounds", "dawn-canyon-07cc4f4e-07cc4f4e-07cc4f4e", true},
		{"address built from a since-renamed title", "old-title-07cc4f4e", true},
		{"claude's own derived task name", "debug-claude-startup-performance", false},
		{"a folder-derived name", "go-streaming-server", false},
		{"a human rename", "cleanup", false},
		{"another session's address", "pale-swallow-d0afc551", false},
		{"empty name", "", false},
	}
	for _, tc := range cases {
		if got := IsClaudePeerNameEcho(tc.in, id); got != tc.want {
			t.Errorf("%s: IsClaudePeerNameEcho(%q, %q) = %v, want %v", tc.name, tc.in, id, got, tc.want)
		}
	}

	if IsClaudePeerNameEcho("anything-07cc4f4e", "") {
		t.Error("an empty instance ID must never classify a name as an echo")
	}
}

// The address the deck actually generates must be recognized as its own echo —
// otherwise the guard drifts the moment ClaudePeerName's format changes.
func TestClaudePeerNameIsRecognizedAsEcho(t *testing.T) {
	inst := &Instance{ID: "ce08010d-1787223144", Title: "pearly-mesa-ce08010d"}
	addr := inst.ClaudePeerName()
	if !IsClaudePeerNameEcho(addr, inst.ID) {
		t.Fatalf("ClaudePeerName() = %q not recognized as an echo of instance %s", addr, inst.ID)
	}
}
