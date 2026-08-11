package main

import (
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

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
		inheritGroup          bool
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
			name:            "inherit-group: parent group wins over worktree cwd-derived group",
			currentGroup:    "",
			cwdDerivedGroup: "feat-profile",
			parentGroup:     "doozyx/voice-chat",
			inheritGroup:    true,
			want:            "doozyx/voice-chat",
		},
		{
			name:                  "inherit-group never overrides explicit -g",
			currentGroup:          "ard",
			cwdDerivedGroup:       "feat-profile",
			parentGroup:           "doozyx/voice-chat",
			explicitGroupProvided: true,
			inheritGroup:          true,
			want:                  "ard",
		},
		{
			name:            "inherit-group with empty parent falls back to cwd-derived",
			currentGroup:    "",
			cwdDerivedGroup: "feat-profile",
			parentGroup:     "",
			inheritGroup:    true,
			want:            "feat-profile",
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
			got := resolveGroupSelection(tt.currentGroup, tt.cwdDerivedGroup, tt.parentGroup, tt.explicitGroupProvided, tt.inheritGroup)
			if got != tt.want {
				t.Fatalf("resolveGroupSelection(curr=%q, cwd=%q, parent=%q, explicit=%v, inherit=%v) = %q, want %q",
					tt.currentGroup, tt.cwdDerivedGroup, tt.parentGroup, tt.explicitGroupProvided, tt.inheritGroup, got, tt.want)
			}
		})
	}
}

// TestLaunch_GroupRootUsesConfiguredDefaultPath pins the root-path variant of
// #972. The legacy path heuristic maps /.../DoozyX/Uniqcast to its parent
// directory ("DoozyX"), even when the deck explicitly declares that exact
// path as the root of the "uniqcast" group. A child launched at the group root
// must honor the declarative mapping before falling back to that heuristic.
func TestLaunch_GroupRootUsesConfiguredDefaultPath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "DoozyX", "Uniqcast")
	groups := []*session.GroupData{
		{Path: "doozyx", DefaultPath: filepath.Dir(root)},
		{Path: "uniqcast"},
	}
	cfg := &session.UserConfig{Groups: map[string]session.GroupSettings{
		"uniqcast": {DefaultPath: root},
	}}

	got := deriveLaunchGroup(root, nil, groups, cfg)
	if got != "uniqcast" {
		t.Fatalf("deriveLaunchGroup(%q) = %q, want configured root group %q", root, got, "uniqcast")
	}
}
