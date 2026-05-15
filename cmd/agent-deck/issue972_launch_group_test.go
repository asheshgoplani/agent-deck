package main

import "testing"

// TestLaunch_DerivesGroupFromCwdNotParent_RegressionFor972 pins the fix for
// https://github.com/asheshgoplani/agent-deck/issues/972.
//
// Bug: `agent-deck launch <project-path>` from inside a conductor session
// (whose own group is `conductor`) inherited the parent's group instead of
// deriving a group from the cwd / project path. Every conductor-spawned child
// landed in `conductor` and required a follow-up `agent-deck group move` to
// land in the project group.
//
// Expected priority (regression-pinned):
//  1. Explicit `-g/--group` always wins.
//  2. Otherwise, the cwd-derived project group wins.
//  3. Parent-session group is the fallback ONLY when no cwd-derived group is
//     available (e.g. an empty path mapping).
//
// Cross-reference: memory note
// `feedback_agent_deck_conductor_uses_agent_deck_group.md` — each conductor's
// children must land in that conductor's project group, never in `conductor`.
func TestLaunch_DerivesGroupFromCwdNotParent_RegressionFor972(t *testing.T) {
	tests := []struct {
		name                  string
		currentGroup          string
		cwdDerivedGroup       string
		parentGroup           string
		explicitGroupProvided bool
		want                  string
	}{
		{
			name:            "regression 972: cwd-derived group wins over conductor parent",
			currentGroup:    "",
			cwdDerivedGroup: "agent-deck",
			parentGroup:     "conductor",
			want:            "agent-deck",
		},
		{
			name:                  "explicit -g still wins over both cwd-derived and parent",
			currentGroup:          "ard",
			cwdDerivedGroup:       "agent-deck",
			parentGroup:           "conductor",
			explicitGroupProvided: true,
			want:                  "ard",
		},
		{
			name:            "parent group is fallback only when no cwd-derived group",
			currentGroup:    "",
			cwdDerivedGroup: "",
			parentGroup:     "conductor",
			want:            "conductor",
		},
		{
			name:            "no parent and no cwd-derived returns empty (caller chooses default)",
			currentGroup:    "",
			cwdDerivedGroup: "",
			parentGroup:     "",
			want:            "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveGroupSelection(tt.currentGroup, tt.cwdDerivedGroup, tt.parentGroup, tt.explicitGroupProvided)
			if got != tt.want {
				t.Fatalf("resolveGroupSelection(curr=%q, cwd=%q, parent=%q, explicit=%v) = %q, want %q",
					tt.currentGroup, tt.cwdDerivedGroup, tt.parentGroup, tt.explicitGroupProvided, got, tt.want)
			}
		})
	}
}
